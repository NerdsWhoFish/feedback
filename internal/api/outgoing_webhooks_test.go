package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NerdsWhoFish/feedback/internal/auth"
	"github.com/NerdsWhoFish/feedback/internal/webhook"
)

func configureOutgoing(t *testing.T, f *fixture, target string) *outgoingQueue {
	t.Helper()
	if err := f.s.ConfigureOutgoingWebhook(target, "test-outgoing-secret", "https://feedback.nerdswhofish.com"); err != nil {
		t.Fatal(err)
	}
	return &outgoingQueue{db: f.db}
}

func TestOutgoingDisabledWithoutDatabase(t *testing.T) {
	s := &Server{}
	if err := s.ConfigureOutgoingWebhook("", "unused-secret", ""); err != nil || s.outgoing != nil {
		t.Fatal("unset URL did not disable outgoing webhooks")
	}
	if err := s.enqueueOutgoing(context.Background(), nil, "", "request.created", auth.Principal{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	s.RunOutgoingWebhooks(context.Background())
	for _, base := range []string{"", "https://user:secret@feedback.example", "https://feedback.example/path", "https://feedback.example?", "https://feedback.example#"} {
		if err := s.ConfigureOutgoingWebhook("https://receiver.example", "test-secret", base); err == nil {
			t.Fatalf("invalid public base accepted: %q", base)
		}
	}
}

func claimOutgoing(t *testing.T, f *fixture, q *outgoingQueue) (webhook.Delivery, webhook.Payload) {
	t.Helper()
	d, err := q.Claim(context.Background(), f.s.outgoing.sender.Target())
	if err != nil {
		t.Fatal(err)
	}
	var p webhook.Payload
	if err := json.Unmarshal(d.Body, &p); err != nil {
		t.Fatal(err)
	}
	if d.ID != p.DeliveryID || d.Event != p.Event || d.Timestamp != p.Timestamp {
		t.Fatal("headers do not match signed payload")
	}
	return d, p
}

func TestOutgoingMutationEventsAndAttribution(t *testing.T) {
	f := testFixture(t)
	q := configureOutgoing(t, f, "https://receiver.example/hook")
	ctx := context.Background()
	created := f.request(t, f.member, f.project, nil)
	id := created["id"].(string)
	d, p := claimOutgoing(t, f, q)
	if p.Event != "request.created" || p.Project.ID != f.project || p.Project.Name != "alpha" || p.Request.ID != id || p.Request.Title != "A better fish finder" || p.Request.Body != "Make it useful." || p.Request.Status != "open" {
		t.Fatalf("bad creation snapshot: %+v", p)
	}
	if p.Request.URL != "https://feedback.nerdswhofish.com/#project="+f.project+"&request="+id+"&view=requests" || p.Submitter.Email != f.member.Email || p.Submitter.Name != f.member.Name || p.Actor.Type != "user" || p.Actor.Email != f.member.Email || p.Attribution.Source != "portal" {
		t.Fatalf("bad browser identity or URL: %+v", p)
	}
	if _, err := time.Parse(time.RFC3339, p.Timestamp); err != nil {
		t.Fatal(err)
	}
	if err := q.Complete(ctx, d, webhook.Result{Status: 204, Outcome: "delivered", Terminal: true}); err != nil {
		t.Fatal(err)
	}
	f.request(t, f.app, f.project, map[string]any{"end_user": map[string]string{"external_id": "customer-123", "email": "client@example.com", "name": "Client"}})
	d, p = claimOutgoing(t, f, q)
	if p.Submitter.Email != "client@example.com" || p.Submitter.Name != "Client" || p.Actor.Type != "app" || p.Actor.Name != "app" || p.Actor.AppName != "test-app" || p.Actor.Email != "" || p.Attribution.Source != "app:test-app" || p.Attribution.AppName != "test-app" || p.Attribution.ExternalID != "customer-123" {
		t.Fatalf("bad app attribution: %+v", p)
	}
	if bytes.Contains(d.Body, []byte("key_hash")) || bytes.Contains(d.Body, []byte("test-outgoing-secret")) || bytes.Contains(d.Body, []byte("public_token")) {
		t.Fatal("credential or share token leaked")
	}
	if err := q.Complete(ctx, d, webhook.Result{Status: 204, Outcome: "delivered", Terminal: true}); err != nil {
		t.Fatal(err)
	}
	code, response := f.call(t, f.admin, "POST", "/api/v1/requests/"+id+"/comments", map[string]any{"body": "private discussion", "internal": true})
	expectStatus(t, 201, code, response)
	if _, err := q.Claim(ctx, f.s.outgoing.sender.Target()); !errors.Is(err, webhook.ErrEmpty) {
		t.Fatalf("internal comment emitted: %v", err)
	}
	code, response = f.call(t, f.assistant, "POST", "/api/v1/requests/"+id+"/comments", map[string]any{"body": "Please clarify the filters."})
	expectStatus(t, 201, code, response)
	d, p = claimOutgoing(t, f, q)
	if p.Event != "comment.created" || p.Comment == nil || p.Comment.ID != response["id"] || p.Comment.Body != "Please clarify the filters." || p.Actor.Type != "assistant" || p.Actor.Name != "assistant" || p.Actor.Email != "" || p.Submitter.Email != f.member.Email {
		t.Fatalf("ambiguous comment attribution: %+v", p)
	}
	if err := q.Complete(ctx, d, webhook.Result{Status: 204, Outcome: "delivered", Terminal: true}); err != nil {
		t.Fatal(err)
	}
	code, response = f.call(t, f.admin, "POST", "/api/v1/requests/"+id+"/status", map[string]string{"status": "under_review"})
	expectStatus(t, 200, code, response)
	d, p = claimOutgoing(t, f, q)
	if p.Event != "request.status_changed" || p.Change == nil || p.Change.From != "open" || p.Change.To != "under_review" || p.Request.Status != "under_review" || p.Actor.Type != "user" || p.Actor.Email != f.admin.Email || p.Submitter.Email != f.member.Email {
		t.Fatalf("ambiguous status attribution: %+v", p)
	}
	if err := q.Complete(ctx, d, webhook.Result{Status: 204, Outcome: "delivered", Terminal: true}); err != nil {
		t.Fatal(err)
	}
	code, response = f.call(t, f.admin, "POST", "/api/v1/requests/"+id+"/status", map[string]string{"status": "under_review"})
	expectStatus(t, 200, code, response)
	code, response = f.call(t, f.assistant, "POST", "/api/v1/requests/"+id+"/status", map[string]string{"status": "approved_for_build"})
	expectStatus(t, 403, code, response)
	if _, err := q.Claim(ctx, f.s.outgoing.sender.Target()); !errors.Is(err, webhook.ErrEmpty) {
		t.Fatal("no-op or forbidden mutation emitted event")
	}
}

func TestOutgoingCommitBoundaryAndDisabled(t *testing.T) {
	f := testFixture(t)
	ctx := context.Background()
	created := f.request(t, f.member, f.project, nil)
	var count int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM outgoing_webhook_deliveries`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("disabled feature enqueued: %d %v", count, err)
	}
	q := configureOutgoing(t, f, "https://receiver.example/hook")
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := f.s.enqueueOutgoing(ctx, tx, created["id"].(string), "request.created", f.member, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(ctx, f.s.outgoing.sender.Target()); !errors.Is(err, webhook.ErrEmpty) {
		t.Fatal("uncommitted event became visible")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(ctx, f.s.outgoing.sender.Target()); !errors.Is(err, webhook.ErrEmpty) {
		t.Fatal("rolled-back event survived")
	}
}

func TestOutgoingFailureDoesNotBlockOrUndoMutation(t *testing.T) {
	f := testFixture(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(503)
	}))
	defer receiver.Close()
	defer close(release)
	q := configureOutgoing(t, f, receiver.URL)
	created := f.request(t, f.member, f.project, nil)
	d, _ := claimOutgoing(t, f, q)
	completed := make(chan webhook.Result, 1)
	go func() { completed <- f.s.outgoing.sender.Deliver(context.Background(), d) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("delivery not started")
	}
	r := httptest.NewRequest("POST", "/api/v1/projects/"+f.project+"/requests", strings.NewReader(`{"title":"While delivery is blocked"}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(auth.WithPrincipal(r.Context(), f.member))
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { f.mux.ServeHTTP(w, r); close(done) }()
	select {
	case <-done:
		if w.Code != 201 {
			t.Fatalf("mutation failed: %d", w.Code)
		}
	case <-time.After(time.Second):
		t.Fatal("mutation waited on webhook delivery")
	}
	release <- struct{}{}
	result := <-completed
	if err := q.Complete(context.Background(), d, result); err != nil {
		t.Fatal(err)
	}
	code, body := f.call(t, f.member, "GET", "/api/v1/requests/"+created["id"].(string), nil)
	expectStatus(t, 200, code, body)
	if body["status"] != "open" {
		t.Fatal("failed delivery changed domain state")
	}
}

func TestOutgoingDurableRetriesLeasesAndConfigBinding(t *testing.T) {
	f := testFixture(t)
	q := configureOutgoing(t, f, "https://receiver.example/hook")
	ctx := context.Background()
	f.request(t, f.member, f.project, nil)
	d, _ := claimOutgoing(t, f, q)
	if _, err := q.Claim(ctx, f.s.outgoing.sender.Target()); !errors.Is(err, webhook.ErrEmpty) {
		t.Fatal("leased delivery claimed twice")
	}
	if err := q.Complete(ctx, d, webhook.Result{Status: 503, Outcome: "http_rejected", Delay: 15 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(ctx, f.s.outgoing.sender.Target()); !errors.Is(err, webhook.ErrEmpty) {
		t.Fatal("backoff ignored")
	}
	if _, err := f.db.Exec(ctx, `UPDATE outgoing_webhook_deliveries SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE requests SET description='Changed after enqueue'`); err != nil {
		t.Fatal(err)
	}
	other, _ := webhook.New("https://other.example/hook", "test-outgoing-secret")
	if _, err := q.Claim(ctx, other.Target()); !errors.Is(err, webhook.ErrEmpty) {
		t.Fatal("configuration change rerouted queued customer data")
	}
	rotated, _ := webhook.New("https://receiver.example/hook", "rotated-secret")
	if _, err := q.Claim(ctx, rotated.Target()); !errors.Is(err, webhook.ErrEmpty) {
		t.Fatal("secret rotation changed retry signature")
	}
	restarted := &outgoingQueue{db: f.db}
	retry, _ := claimOutgoing(t, f, restarted)
	if retry.ID != d.ID || retry.Attempt != 2 || retry.Timestamp != d.Timestamp || !bytes.Equal(retry.Body, d.Body) {
		t.Fatal("retry changed identity or snapshot")
	}
	if err := q.Complete(ctx, d, webhook.Result{Status: 200, Outcome: "delivered", Terminal: true}); err != nil {
		t.Fatal(err)
	}
	var terminal bool
	if err := f.db.QueryRow(ctx, `SELECT completed_at IS NOT NULL FROM outgoing_webhook_deliveries WHERE id=$1`, d.ID).Scan(&terminal); err != nil || terminal {
		t.Fatal("stale completion overwrote new claim")
	}
	if _, err := f.db.Exec(ctx, `UPDATE outgoing_webhook_deliveries SET attempts=5,lease_until=now()-interval '1 second' WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(ctx, f.s.outgoing.sender.Target()); !errors.Is(err, webhook.ErrEmpty) {
		t.Fatal("exhausted crash lease reclaimed")
	}
	var outcome string
	if err := f.db.QueryRow(ctx, `SELECT completed_at IS NOT NULL,last_error FROM outgoing_webhook_deliveries WHERE id=$1`, d.ID).Scan(&terminal, &outcome); err != nil || !terminal || outcome != "lease_expired" {
		t.Fatalf("final lease not finalized: %v %q %v", terminal, outcome, err)
	}
}

func TestOutgoingExpiryAndRetention(t *testing.T) {
	f := testFixture(t)
	q := configureOutgoing(t, f, "https://receiver.example/hook")
	ctx := context.Background()
	f.request(t, f.member, f.project, nil)
	if _, err := f.db.Exec(ctx, `UPDATE outgoing_webhook_deliveries SET created_at=now()-interval '25 hours'`); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(ctx, "different-configuration"); !errors.Is(err, webhook.ErrEmpty) {
		t.Fatal("expired snapshot delivered")
	}
	var terminal bool
	var outcome string
	if err := f.db.QueryRow(ctx, `SELECT completed_at IS NOT NULL,last_error FROM outgoing_webhook_deliveries`).Scan(&terminal, &outcome); err != nil || !terminal || outcome != "expired" {
		t.Fatalf("old configuration not expired: %v %q %v", terminal, outcome, err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE outgoing_webhook_deliveries SET created_at=now()-interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(ctx, f.s.outgoing.sender.Target()); !errors.Is(err, webhook.ErrEmpty) {
		t.Fatal("retention cleanup failed")
	}
	var count int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM outgoing_webhook_deliveries`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("private snapshots retained indefinitely: %d %v", count, err)
	}
}

func TestOutgoingRollbackOnCommitFailure(t *testing.T) {
	f := testFixture(t)
	configureOutgoing(t, f, "https://receiver.example/hook")
	ctx := context.Background()
	if _, err := f.db.Exec(ctx, `CREATE FUNCTION reject_test_request_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test commit failure'; END; $$;
 CREATE CONSTRAINT TRIGGER reject_test_request_commit AFTER INSERT ON requests DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_test_request_commit()`); err != nil {
		t.Fatal(err)
	}
	code, response := f.call(t, f.member, "POST", "/api/v1/projects/"+f.project+"/requests", map[string]string{"title": "Must roll back"})
	expectStatus(t, 500, code, response)
	var requests, events int
	if err := f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM requests),(SELECT count(*) FROM outgoing_webhook_deliveries)`).Scan(&requests, &events); err != nil || requests != 0 || events != 0 {
		t.Fatalf("commit failure leaked request or event: %d %d %v", requests, events, err)
	}
}
