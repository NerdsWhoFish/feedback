package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestWebhookDestinationPolicy(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "192.0.2.1", "198.18.0.1", "224.0.0.1", "240.0.0.1", "::1", "::", "fe80::1", "fc00::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "2002:a00:1::1"} {
		if publicIP(netip.MustParseAddr(raw)) {
			t.Errorf("unsafe destination accepted: %s", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !publicIP(netip.MustParseAddr(raw)) {
			t.Errorf("public destination rejected: %s", raw)
		}
	}
	for _, raw := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com:8080", "https://example.com/#fragment", "file:///tmp/test"} {
		if _, err := webhookURL(raw); err == nil {
			t.Errorf("unsafe URL accepted: %s", raw)
		}
	}
	if _, err := webhookURL("https://example.com/callback"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := resolvePublic(ctx, "127.0.0.1"); err == nil {
		t.Fatal("loopback DNS accepted")
	}
	client := webhookClient()
	if err := client.CheckRedirect(nil, nil); err == nil {
		t.Fatal("redirects enabled")
	}
	transport := client.Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Fatal("ambient proxy enabled")
	}
	if _, err := transport.DialContext(ctx, "tcp", "127.0.0.1:443"); err == nil {
		t.Fatal("private dial accepted")
	}
}
func TestWebhookEncryption(t *testing.T) {
	s := &Server{}
	if err := s.SetWebhookEncryptionKey([]byte("bad")); err == nil {
		t.Fatal("short key accepted")
	}
	if _, err := s.encryptSecret("private"); err == nil {
		t.Fatal("missing encryption allowed")
	}
	if err := s.SetWebhookEncryptionKey(bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Fatal(err)
	}
	encrypted, err := s.encryptSecret("private")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("private")) {
		t.Fatal("plaintext secret stored")
	}
	plain, err := s.decryptSecret(encrypted)
	if err != nil || plain != "private" {
		t.Fatalf("decrypt failed %v", err)
	}
	encrypted[len(encrypted)-1] ^= 1
	if _, err = s.decryptSecret(encrypted); err == nil {
		t.Fatal("tamper accepted")
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestWebhookSigningAndBoundedResponse(t *testing.T) {
	s := &Server{}
	if err := s.SetWebhookEncryptionKey(bytes.Repeat([]byte{3}, 32)); err != nil {
		t.Fatal(err)
	}
	secret := "test-signing-secret"
	encrypted, err := s.encryptSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"event":"status_changed"}`)
	called := false
	client := &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		called = true
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(body, payload) {
			t.Fatal("payload changed")
		}
		if r.Header.Get("X-Feedback-Signature") != sign(secret, r.Header.Get("X-Feedback-Timestamp"), body) {
			t.Fatal("incorrect HMAC")
		}
		if r.Header.Get("X-Feedback-Delivery") != "delivery-1" {
			t.Fatal("missing idempotency ID")
		}
		return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader("accepted"))}, nil
	})}
	status, outcome := s.deliver(context.Background(), client, delivery{ID: "delivery-1", URL: "https://example.com/hook", Secret: encrypted, Payload: payload})
	if !called || status != 202 || outcome != "delivered" {
		t.Fatalf("delivery failed %d %s", status, outcome)
	}
}
func TestDurableWebhookOutboxRetriesAndPrivacy(t *testing.T) {
	f := testFixture(t)
	ctx := context.Background()
	if err := f.s.SetWebhookEncryptionKey(bytes.Repeat([]byte{4}, 32)); err != nil {
		t.Fatal(err)
	}
	encrypted, err := f.s.encryptSecret("delivery-secret")
	if err != nil {
		t.Fatal(err)
	}
	var webhookID string
	if err = f.db.QueryRow(ctx, `INSERT INTO webhooks(project_id,url,events,encrypted_secret) VALUES($1,'https://example.com/hook',ARRAY['request_created','status_changed','comment_created'],$2) RETURNING id::text`, f.project, encrypted).Scan(&webhookID); err != nil {
		t.Fatal(err)
	}
	created := f.request(t, f.member, f.project, nil)
	requestID := created["id"].(string)
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("outbox missing: %d %v", count, err)
	}
	code, body := f.call(t, f.admin, "POST", "/api/v1/requests/"+requestID+"/comments", map[string]any{"body": "sensitive staff discussion", "internal": true})
	expectStatus(t, 201, code, body)
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries`).Scan(&count); err != nil || count != 1 {
		t.Fatal("internal comment generated external webhook")
	}
	code, body = f.call(t, f.member, "POST", "/api/v1/requests/"+requestID+"/comments", map[string]any{"body": "public feedback"})
	expectStatus(t, 201, code, body)
	d, err := f.s.claimDelivery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.Attempt != 1 {
		t.Fatal("attempt was not claimed atomically")
	}
	payload := string(d.Payload)
	for _, forbidden := range []string{f.member.Email, "description", "A better fish finder", "public feedback", "sensitive"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("webhook leaked content %q", forbidden)
		}
	}
	if err = f.s.completeDelivery(ctx, d, 503, "http_rejected"); err != nil {
		t.Fatal(err)
	}
	var next time.Time
	var completed *time.Time
	if err = f.db.QueryRow(ctx, `SELECT next_attempt_at,completed_at FROM webhook_deliveries WHERE id=$1`, d.ID).Scan(&next, &completed); err != nil {
		t.Fatal(err)
	}
	if completed != nil || !next.After(time.Now()) {
		t.Fatal("retry not scheduled")
	}
	if _, err = f.db.Exec(ctx, `UPDATE webhook_deliveries SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	retry, err := f.s.claimDelivery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID != d.ID || retry.Attempt != 2 {
		t.Fatalf("bad retry %+v", retry)
	}
	if err = f.s.completeDelivery(ctx, retry, 200, "delivered"); err != nil {
		t.Fatal(err)
	}
	code, body = f.call(t, f.admin, "GET", "/api/v1/projects/"+f.project+"/deliveries", nil)
	expectStatus(t, 200, code, body)
	encoded, _ := json.Marshal(body)
	if bytes.Contains(encoded, []byte("delivery-secret")) {
		t.Fatal("delivery log exposed secret")
	}
	if !bytes.Contains(encoded, []byte("http_rejected")) || !bytes.Contains(encoded, []byte("delivered")) {
		t.Fatal("attempt log incomplete")
	}
	code, body = f.call(t, f.admin, "GET", "/api/v1/projects/"+f.project+"/webhooks", nil)
	expectStatus(t, 200, code, body)
	encoded, _ = json.Marshal(body)
	if bytes.Contains(encoded, []byte("secret")) {
		t.Fatal("webhook read exposed secret")
	}
	code, body = f.call(t, f.admin, "DELETE", "/api/v1/projects/"+f.project+"/webhooks/"+webhookID, nil)
	expectStatus(t, 204, code, body)
	if _, err = f.s.claimDelivery(ctx); err != pgx.ErrNoRows {
		t.Fatalf("disabled webhook still deliverable: %v", err)
	}
}
func TestConcurrentAssistantClaims(t *testing.T) {
	f := testFixture(t)
	created := f.request(t, f.member, f.project, nil)
	route := "/api/v1/requests/" + created["id"].(string) + "/status"
	code, body := f.call(t, f.admin, "POST", route, map[string]string{"status": "approved_for_build"})
	expectStatus(t, 200, code, body)
	var wg sync.WaitGroup
	results := make(chan int, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := f.call(t, f.assistant, "POST", route, map[string]string{"status": "in_progress"})
			results <- code
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for code := range results {
		if code == 200 {
			success++
		} else if code != 403 {
			t.Fatalf("unexpected concurrent response %d", code)
		}
	}
	if success != 1 {
		t.Fatalf("%d concurrent claims succeeded", success)
	}
}

func TestWebhookTracePropagationAndFinalLeaseRecovery(t *testing.T) {
	f := testFixture(t)
	if err := f.s.SetWebhookEncryptionKey(bytes.Repeat([]byte{8}, 32)); err != nil {
		t.Fatal(err)
	}
	encrypted, err := f.s.encryptSecret("trace-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = f.db.Exec(ctx, `INSERT INTO webhooks(project_id,url,events,encrypted_secret) VALUES($1,'https://example.com/hook',ARRAY['request_created'],$2)`, f.project, encrypted); err != nil {
		t.Fatal(err)
	}
	traceID, _ := trace.TraceIDFromHex("12345678901234567890123456789012")
	spanID, _ := trace.SpanIDFromHex("1234567890123456")
	state, _ := trace.ParseTraceState("private=do-not-forward")
	parent := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled, TraceState: state})
	ctx = trace.ContextWithSpanContext(ctx, parent)
	bag, _ := baggage.Parse("email=secret@example.com")
	ctx = baggage.ContextWithBaggage(ctx, bag)
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.enqueue(ctx, tx, f.project, "request_created", map[string]any{"request_id": "test"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	d, err := f.s.claimDelivery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.TraceParent != "00-12345678901234567890123456789012-1234567890123456-01" {
		t.Fatalf("unexpected stored parent: %s", d.TraceParent)
	}
	workerCtx := propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier{"traceparent": d.TraceParent})
	client := &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.Header.Get("Traceparent"), traceID.String()) {
			t.Fatal("outgoing trace is not correlated")
		}
		if r.Header.Get("Baggage") != "" || r.Header.Get("Tracestate") != "" {
			t.Fatal("untrusted trace metadata crossed outbox")
		}
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	if status, outcome := f.s.deliver(workerCtx, client, d); status != 204 || outcome != "delivered" {
		t.Fatalf("delivery: %d %s", status, outcome)
	}
	if _, err = f.db.Exec(context.Background(), `UPDATE webhook_deliveries SET attempts=8,lease_until=now()-interval '1 minute' WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.claimDelivery(context.Background()); err != pgx.ErrNoRows {
		t.Fatalf("exhausted delivery reclaimed: %v", err)
	}
	var completed bool
	var outcome string
	if err = f.db.QueryRow(context.Background(), `SELECT completed_at IS NOT NULL,last_error FROM webhook_deliveries WHERE id=$1`, d.ID).Scan(&completed, &outcome); err != nil {
		t.Fatal(err)
	}
	if !completed || outcome != "lease_expired" {
		t.Fatal("final crashed attempt did not become terminal")
	}
}
