package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NerdsWhoFish/feedback/internal/auth"
	"github.com/jackc/pgx/v5"
)

func enableEmail(t *testing.T, f *fixture) {
	t.Helper()
	if err := f.s.ConfigureEmail("test-resend-key", "Feedback <feedback@nerdswhofish.com>", "https://feedback.example.com", map[string]bool{f.admin.Email: true}); err != nil {
		t.Fatal(err)
	}
}

func emailCount(t *testing.T, f *fixture, where string, args ...any) int {
	t.Helper()
	var count int
	if err := f.db.QueryRow(context.Background(), `SELECT count(*) FROM email_outbox WHERE `+where, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func resetEmailGate(t *testing.T, f *fixture) {
	t.Helper()
	if _, err := f.db.Exec(context.Background(), `UPDATE email_dispatch_gate SET next_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
}

func TestEmailConfigDisabledAndValidation(t *testing.T) {
	s := &Server{}
	if err := s.ConfigureEmail("", "", "", nil); err != nil || s.email != nil {
		t.Fatal("email did not disable cleanly")
	}
	if err := s.enqueueEmail(context.Background(), nil, "unused", "request_created"); err != nil {
		t.Fatal(err)
	}
	s.RunEmail(context.Background())
	for _, tc := range []struct{ key, from, base string }{
		{"key", "", "https://example.com"},
		{"key", "x@example.com\r\nBcc: evil@example.com", "https://example.com"},
		{"key\nheader", "x@example.com", "https://example.com"},
		{"key", "x@example.com", "https://user:pass@example.com"},
		{"key", "x@example.com", "https://example.com/path"},
	} {
		if err := s.ConfigureEmail(tc.key, tc.from, tc.base, nil); err == nil {
			t.Errorf("invalid configuration accepted: %#v", tc)
		}
	}
	if err := resendClient().CheckRedirect(nil, nil); err == nil {
		t.Fatal("email redirects enabled")
	}
}

func TestEmailRecipientsSubscriptionsAndConsent(t *testing.T) {
	f := testFixture(t)
	ctx := context.Background()
	f.request(t, f.member, f.project, nil)
	if count := emailCount(t, f, "true"); count != 0 {
		t.Fatalf("disabled email enqueued %d deliveries", count)
	}
	enableEmail(t, f)
	created := f.request(t, f.member, f.project, nil)
	id := created["id"].(string)
	if count := emailCount(t, f, "event='request_created'"); count != 1 {
		t.Fatalf("new request admin recipients=%d", count)
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO memberships(project_id,email) VALUES($1,$2)`, f.project, f.outsider.Email); err != nil {
		t.Fatal(err)
	}
	code, body := f.call(t, f.outsider, "PUT", "/api/v1/requests/"+id+"/vote", nil)
	expectStatus(t, 200, code, body)
	code, body = f.call(t, f.admin, "PUT", "/api/v1/requests/"+id+"/subscription", nil)
	expectStatus(t, 200, code, body)
	code, body = f.call(t, f.member, "GET", "/api/v1/requests/"+id+"/subscription", nil)
	expectStatus(t, 200, code, body)
	if body["subscribed"] != true {
		t.Fatal("author not automatically subscribed")
	}
	code, body = f.call(t, f.admin, "POST", "/api/v1/requests/"+id+"/status", map[string]string{"status": "planned"})
	expectStatus(t, 200, code, body)
	if count := emailCount(t, f, "event='status_changed'"); count != 3 {
		t.Fatalf("author/voter/subscriber recipients=%d", count)
	}
	code, body = f.call(t, f.admin, "POST", "/api/v1/requests/"+id+"/comments", map[string]any{"body": "internal staff discussion", "internal": true})
	expectStatus(t, 201, code, body)
	if count := emailCount(t, f, "event='comment_created'"); count != 0 {
		t.Fatal("internal comment leaked to email")
	}
	code, body = f.call(t, f.outsider, "DELETE", "/api/v1/requests/"+id+"/subscription", nil)
	expectStatus(t, 200, code, body)
	code, body = f.call(t, f.admin, "PUT", "/api/v1/me/notifications", map[string]bool{"enabled": false})
	expectStatus(t, 200, code, body)
	code, body = f.call(t, f.assistant, "POST", "/api/v1/requests/"+id+"/comments", map[string]any{"body": "public update"})
	expectStatus(t, 201, code, body)
	if count := emailCount(t, f, "event='comment_created'"); count != 1 {
		t.Fatalf("opt-outs ignored: %d", count)
	}
	for _, p := range []auth.Principal{f.app, f.assistant} {
		for _, route := range []string{"/api/v1/me/notifications", "/api/v1/requests/" + id + "/subscription"} {
			code, body = f.call(t, p, "GET", route, nil)
			expectStatus(t, 403, code, body)
		}
	}
	if count := emailCount(t, f, "recipient NOT IN ($1,$2,$3)", f.member.Email, f.admin.Email, f.outsider.Email); count != 0 {
		t.Fatal("unexpected recipient, potentially assistant")
	}
}

func TestEmailAppAuthorsDeduplicationAndUnsubscribe(t *testing.T) {
	f := testFixture(t)
	enableEmail(t, f)
	ctx := context.Background()
	created := f.request(t, f.app, f.project, map[string]any{"title": "App feedback", "end_user": map[string]string{"external_id": "customer-1", "email": f.member.Email, "name": "App customer"}})
	id := created["id"].(string)
	code, body := f.call(t, f.member, "PUT", "/api/v1/requests/"+id+"/vote", nil)
	expectStatus(t, 200, code, body)
	code, body = f.call(t, f.admin, "POST", "/api/v1/requests/"+id+"/status", map[string]string{"status": "under_review"})
	expectStatus(t, 200, code, body)
	if count := emailCount(t, f, "event='status_changed'"); count != 1 {
		t.Fatalf("duplicate identities got %d emails", count)
	}
	var unsub string
	if err := f.db.QueryRow(ctx, `SELECT email_unsubscribe_token FROM users WHERE id=$1`, f.member.UserID).Scan(&unsub); err != nil {
		t.Fatal(err)
	}
	route := "/api/v1/email/unsubscribe/" + unsub
	get := httptest.NewRecorder()
	f.mux.ServeHTTP(get, httptest.NewRequest("GET", route, nil))
	if get.Code != 200 || !strings.Contains(get.Body.String(), `method="post"`) {
		t.Fatal("unsubscribe confirmation missing")
	}
	var enabled bool
	if err := f.db.QueryRow(ctx, `SELECT email_notifications FROM users WHERE id=$1`, f.member.UserID).Scan(&enabled); err != nil || !enabled {
		t.Fatal("GET unsubscribed user")
	}
	post := httptest.NewRecorder()
	f.mux.ServeHTTP(post, httptest.NewRequest("POST", route, strings.NewReader("List-Unsubscribe=One-Click")))
	if post.Code != 200 {
		t.Fatal("one-click unsubscribe failed")
	}
	var optedIn int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM users WHERE lower(email)=$1 AND email_notifications`, f.member.Email).Scan(&optedIn); err != nil || optedIn != 0 {
		t.Fatal("duplicate account bypassed unsubscribe")
	}
	code, body = f.call(t, f.admin, "POST", "/api/v1/requests/"+id+"/status", map[string]string{"status": "planned"})
	expectStatus(t, 200, code, body)
	if count := emailCount(t, f, "event='status_changed'"); count != 1 {
		t.Fatal("unsubscribed app author still notified")
	}
	code, body = f.call(t, f.member, "PUT", "/api/v1/me/notifications", map[string]bool{"enabled": true})
	expectStatus(t, 200, code, body)
	if body["enabled"] != true || body["available"] != true {
		t.Fatal("preferences response wrong")
	}
}

func TestEmailConfiguredAdminsAndQueuedRevocation(t *testing.T) {
	f := testFixture(t)
	ctx := context.Background()
	if err := f.s.ConfigureEmail("test-key", "feedback@nerdswhofish.com", "https://feedback.example.com", map[string]bool{"new-admin@example.com": true}); err != nil {
		t.Fatal(err)
	}
	created := f.request(t, f.member, f.project, nil)
	id := created["id"].(string)
	if count := emailCount(t, f, "recipient='new-admin@example.com'"); count != 1 {
		t.Fatal("configured admin before first login missed notification")
	}
	if count := emailCount(t, f, "recipient=$1", f.admin.Email); count != 0 {
		t.Fatal("stale database admin role bypassed configuration")
	}
	d, err := f.s.claimEmail(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.s.email.admins = []string{}
	allowed, err := f.s.emailAllowed(ctx, d)
	if err != nil || allowed {
		t.Fatal("removed administrator still eligible")
	}
	if _, err = f.db.Exec(ctx, `INSERT INTO memberships(project_id,email) VALUES($1,$2)`, f.project, f.outsider.Email); err != nil {
		t.Fatal(err)
	}
	code, body := f.call(t, f.outsider, "PUT", "/api/v1/requests/"+id+"/subscription", nil)
	expectStatus(t, 200, code, body)
	code, body = f.call(t, f.admin, "POST", "/api/v1/requests/"+id+"/status", map[string]string{"status": "planned"})
	expectStatus(t, 200, code, body)
	if _, err = f.db.Exec(ctx, `DELETE FROM memberships WHERE project_id=$1 AND email=$2`, f.project, f.outsider.Email); err != nil {
		t.Fatal(err)
	}
	var voter emailDelivery
	if err = f.db.QueryRow(ctx, `SELECT id::text,user_id::text,request_id::text,recipient,event FROM email_outbox WHERE recipient=$1`, f.outsider.Email).Scan(&voter.ID, &voter.UserID, &voter.RequestID, &voter.Recipient, &voter.Event); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("revoked recipient reached Resend")
		return nil, nil
	})}
	if status, outcome, _ := f.s.deliverEmail(ctx, client, voter); status != 0 || outcome != "suppressed" {
		t.Fatal("membership revocation did not suppress queued delivery")
	}
	if _, err = f.db.Exec(ctx, `UPDATE users SET email='changed@example.com' WHERE id=$1`, f.member.UserID); err != nil {
		t.Fatal(err)
	}
	var author emailDelivery
	if err = f.db.QueryRow(ctx, `SELECT id::text,user_id::text,request_id::text,recipient,event FROM email_outbox WHERE recipient=$1`, f.member.Email).Scan(&author.ID, &author.UserID, &author.RequestID, &author.Recipient, &author.Event); err != nil {
		t.Fatal(err)
	}
	if allowed, err = f.s.emailAllowed(ctx, author); err != nil || allowed {
		t.Fatal("changed address still eligible")
	}
}

func TestEmailDeliveryRetryIdempotencyAndPacing(t *testing.T) {
	f := testFixture(t)
	enableEmail(t, f)
	ctx := context.Background()
	f.request(t, f.member, f.project, nil)
	d, err := f.s.claimEmail(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var initial []byte
	var initialKey string
	client := &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.resend.com/emails" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-resend-key" {
			t.Fatal("incorrect Resend request")
		}
		initial, _ = io.ReadAll(r.Body)
		initialKey = r.Header.Get("Idempotency-Key")
		var payload resendMessage
		if err := json.Unmarshal(initial, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.To) != 1 || payload.To[0] != f.admin.Email || !strings.Contains(payload.Text, "Unsubscribe") || payload.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
			t.Fatal("incorrect Resend payload")
		}
		if r.Header.Get("Baggage") != "" || r.Header.Get("Tracestate") != "" {
			t.Fatal("private context sent to provider")
		}
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"120"}}, Body: io.NopCloser(strings.NewReader("private provider error"))}, nil
	})}
	status, outcome, delay := f.s.deliverEmail(ctx, client, d)
	if status != 429 || outcome != "rate_limited" || delay != 120*time.Second {
		t.Fatal("rate limit not honored")
	}
	if err = f.s.completeEmail(ctx, d, status, outcome, delay); err != nil {
		t.Fatal(err)
	}
	f.request(t, f.member, f.project, nil)
	if _, err = f.s.claimEmail(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("shared provider cooldown ignored")
	}
	var next, gate time.Time
	if err = f.db.QueryRow(ctx, `SELECT next_attempt_at,(SELECT next_at FROM email_dispatch_gate) FROM email_outbox WHERE id=$1`, d.ID).Scan(&next, &gate); err != nil {
		t.Fatal(err)
	}
	if next.Before(time.Now().Add(110*time.Second)) || gate.Before(time.Now().Add(110*time.Second)) {
		t.Fatal("cooldown not durable")
	}
	resetEmailGate(t, f)
	f.s.email.from = "changed@nerdswhofish.com"
	f.s.email.baseURL = "https://changed.example.com"
	if _, err = f.db.Exec(ctx, `UPDATE email_outbox SET next_attempt_at=now()-interval '1 hour' WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	retry, err := f.s.claimEmail(ctx)
	if err != nil || retry.ID != d.ID || retry.Attempt != 2 {
		t.Fatalf("retry claim failed: %+v %v", retry, err)
	}
	client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if !bytes.Equal(body, initial) || r.Header.Get("Idempotency-Key") != initialKey {
			t.Fatal("retry changed body or idempotency key")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"provider-id"}`))}, nil
	})
	status, outcome, delay = f.s.deliverEmail(ctx, client, retry)
	if err = f.s.completeEmail(ctx, retry, status, outcome, delay); err != nil {
		t.Fatal(err)
	}
	if count := emailCount(t, f, "id=$1 AND completed_at IS NOT NULL AND last_error='delivered'", d.ID); count != 1 {
		t.Fatal("successful send not completed")
	}
	var logs int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM email_attempts WHERE delivery_id=$1`, d.ID).Scan(&logs); err != nil || logs != 2 {
		t.Fatal("attempt history incomplete")
	}
}

func TestEmailLeaseExpiryConsentAndConcurrentClaims(t *testing.T) {
	f := testFixture(t)
	enableEmail(t, f)
	ctx := context.Background()
	for range 5 {
		f.request(t, f.member, f.project, nil)
	}
	var wg sync.WaitGroup
	claims := make(chan emailDelivery, 5)
	errorsCh := make(chan error, 5)
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := f.s.claimEmail(ctx)
			if err == nil {
				claims <- d
			} else if !errors.Is(err, pgx.ErrNoRows) {
				errorsCh <- err
			}
		}()
	}
	wg.Wait()
	close(claims)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("global pace allowed %d concurrent claims", len(claims))
	}
	d := <-claims
	if _, err := f.db.Exec(ctx, `UPDATE users SET email_notifications=false WHERE id=$1`, d.UserID); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("opted-out recipient reached provider")
		return nil, nil
	})}
	status, outcome, delay := f.s.deliverEmail(ctx, client, d)
	if outcome != "suppressed" {
		t.Fatal("queued opt-out not respected")
	}
	if err := f.s.completeEmail(ctx, d, status, outcome, delay); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE email_outbox SET attempts=8,lease_until=now()-interval '1 minute' WHERE completed_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	resetEmailGate(t, f)
	if _, err := f.s.claimEmail(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("exhausted attempts reclaimed")
	}
	if count := emailCount(t, f, "completed_at IS NULL"); count != 0 {
		t.Fatal("final crashed leases not terminated")
	}
	f.request(t, f.member, f.project, nil)
	if _, err := f.db.Exec(ctx, `UPDATE users SET email_notifications=true WHERE id=$1`, f.admin.UserID); err != nil {
		t.Fatal(err)
	}
	f.request(t, f.member, f.project, nil)
	if _, err := f.db.Exec(ctx, `UPDATE email_outbox SET attempts=1,first_attempt_at=now()-interval '24 hours' WHERE completed_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	resetEmailGate(t, f)
	if _, err := f.s.claimEmail(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("expired idempotency key retried")
	}
	if count := emailCount(t, f, "last_error='idempotency_expired'"); count != 1 {
		t.Fatal("uncertain old send not terminated")
	}
}

func TestResendCooldown(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		header http.Header
		want   time.Duration
	}{
		{http.Header{"Retry-After": []string{"12"}}, 12 * time.Second},
		{http.Header{"Retry-After": []string{now.Add(30 * time.Second).Format(http.TimeFormat)}}, 30 * time.Second},
		{http.Header{"Ratelimit-Remaining": []string{"0"}, "Ratelimit-Reset": []string{"45"}}, 45 * time.Second},
		{http.Header{"Retry-After": []string{"nonsense"}}, 0},
		{http.Header{"Retry-After": []string{"-1"}}, 0},
	} {
		if got := resendCooldown(tc.header, now); got != tc.want {
			t.Errorf("cooldown=%s want %s", got, tc.want)
		}
	}
}

func TestEmailRollbackAndBrowserAuthorRevocation(t *testing.T) {
	f := testFixture(t)
	enableEmail(t, f)
	ctx := context.Background()
	created := f.request(t, f.member, f.project, nil)
	id := created["id"].(string)
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.enqueueEmail(ctx, tx, id, "comment_created"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if count := emailCount(t, f, "event='comment_created'"); count != 0 {
		t.Fatal("rolled-back notification escaped transaction")
	}
	code, body := f.call(t, f.admin, "POST", "/api/v1/requests/"+id+"/status", map[string]string{"status": "planned"})
	expectStatus(t, 200, code, body)
	var d emailDelivery
	if err = f.db.QueryRow(ctx, `SELECT id::text,user_id::text,request_id::text,recipient,event FROM email_outbox WHERE recipient=$1`, f.member.Email).Scan(&d.ID, &d.UserID, &d.RequestID, &d.Recipient, &d.Event); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `DELETE FROM memberships WHERE project_id=$1 AND email=$2`, f.project, f.member.Email); err != nil {
		t.Fatal(err)
	}
	if allowed, err := f.s.emailAllowed(ctx, d); err != nil || allowed {
		t.Fatal("browser author membership revocation did not suppress queued mail")
	}
	code, body = f.call(t, f.admin, "POST", "/api/v1/requests/"+id+"/status", map[string]string{"status": "under_review"})
	expectStatus(t, 200, code, body)
	if count := emailCount(t, f, "recipient=$1", f.member.Email); count != 1 {
		t.Fatal("revoked browser author received new notification")
	}
}

func TestEmailEffectiveOptOutAcrossNewIdentities(t *testing.T) {
	f := testFixture(t)
	enableEmail(t, f)
	ctx := context.Background()
	if _, err := f.db.Exec(ctx, `INSERT INTO users(email,name,email_notifications) VALUES($1,'Old app identity',false)`, f.member.Email); err != nil {
		t.Fatal(err)
	}
	code, body := f.call(t, f.member, "GET", "/api/v1/me/notifications", nil)
	expectStatus(t, 200, code, body)
	if body["enabled"] != false {
		t.Fatal("new identity hid existing opt-out")
	}
	code, body = f.call(t, f.member, "PUT", "/api/v1/me/notifications", map[string]bool{"enabled": true})
	expectStatus(t, 200, code, body)
	if body["enabled"] != true {
		t.Fatal("explicit opt-in did not update all identities")
	}
}

func TestEmailProviderFailureClassification(t *testing.T) {
	f := testFixture(t)
	enableEmail(t, f)
	ctx := context.Background()
	f.request(t, f.member, f.project, nil)
	d, err := f.s.claimEmail(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status  int
		outcome string
	}{
		{400, "provider_rejected"}, {401, "provider_rejected"}, {403, "provider_rejected"},
		{408, "provider_unavailable"}, {409, "provider_unavailable"}, {503, "provider_unavailable"},
	} {
		client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader("sensitive response"))}, nil
		})}
		status, outcome, _ := f.s.deliverEmail(ctx, client, d)
		if status != tc.status || outcome != tc.outcome {
			t.Errorf("status %d classified %s", status, outcome)
		}
	}
	client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) { return nil, errors.New("sensitive transport failure") })}
	if status, outcome, _ := f.s.deliverEmail(ctx, client, d); status != 0 || outcome != "transport_failed" {
		t.Fatal("transport failure classification incorrect")
	}
	if err = f.s.completeEmail(ctx, d, 401, "provider_rejected", 0); err != nil {
		t.Fatal(err)
	}
	if count := emailCount(t, f, "id=$1 AND completed_at IS NOT NULL", d.ID); count != 1 {
		t.Fatal("permanent provider rejection was retried")
	}
}

func TestEmailAppOnlyAuthorWithoutPortalAccess(t *testing.T) {
	f := testFixture(t)
	enableEmail(t, f)
	ctx := context.Background()
	created := f.request(t, f.app, f.project, map[string]any{"title": "App-only request", "end_user": map[string]string{"external_id": "app-only", "email": "app-only@example.com", "name": "App customer"}})
	id := created["id"].(string)
	code, body := f.call(t, f.admin, "POST", "/api/v1/requests/"+id+"/status", map[string]string{"status": "planned"})
	expectStatus(t, 200, code, body)
	var payload []byte
	if err := f.db.QueryRow(ctx, `SELECT payload FROM email_outbox WHERE recipient='app-only@example.com' AND event='status_changed'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var message resendMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message.Text, "Check this request in the application where you submitted it.") || !strings.Contains(message.Text, "planned") || !strings.Contains(message.Text, "App-only request") {
		t.Fatal("app-only notification is not useful")
	}
	if strings.Contains(message.Text, "/#") || strings.Contains(message.Text, "/s/") || strings.Contains(message.Text, "public_token") {
		t.Fatal("app-only notification linked inaccessible portal or leaked public capability")
	}
}
