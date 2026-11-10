package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NerdsWhoFish/feedback/internal/config"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func authorizationConfig(t *testing.T, authorization string) config.Config {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("BASE_URL", "http://localhost:8080")
	t.Setenv("ENVIRONMENT", "local")
	t.Setenv("WEBHOOK_URL", "https://receiver.example/hook")
	t.Setenv("WEBHOOK_SECRET", "fake-signing-secret")
	t.Setenv("WEBHOOK_HEADERS", `{"X-Routine-Key":"<fake-private-custom>"}`)
	t.Setenv("WEBHOOK_AUTHORIZATION", authorization)
	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAuthorizationShortcutForwardedVerbatim(t *testing.T) {
	for _, value := range []string{"", "<fake-raw-key>", "Bearer <fake-prefixed-key>"} {
		c := authorizationConfig(t, value)
		sender, err := NewWithHeaders(c.WebhookURL, c.WebhookSecret, c.WebhookHeaders)
		if err != nil {
			t.Fatal(err)
		}
		withoutHeaders, _ := New(c.WebhookURL, c.WebhookSecret)
		if sender.Target() != withoutHeaders.Target() {
			t.Fatal("authorization shortcut changed the delivery fingerprint")
		}
		sender.client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
			_, present := r.Header["Authorization"]
			if r.Header.Get("Authorization") != value || present != (value != "") || r.Header.Get("X-Routine-Key") != "<fake-private-custom>" {
				t.Error("shortcut was prefixed, changed, or lost with custom headers")
			}
			return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader(""))}, nil
		})
		if result := sender.Deliver(context.Background(), Delivery{Attempt: 1}); result.Outcome != "delivered" {
			t.Fatal("shortcut delivery failed")
		}
	}
}

func TestHeaderRetriesAndRotation(t *testing.T) {
	input := map[string]string{"authorization": "Bearer <fake-first-key>", "x-routine-key": "<fake-custom-key>"}
	first, err := NewWithHeaders("https://receiver.example/hook", "fake-signing-secret", input)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := input["Authorization"]; exists {
		t.Fatal("sender mutated caller headers")
	}
	input["authorization"] = "fake-caller-mutation"
	second, err := NewWithHeaders("https://receiver.example/hook", "fake-signing-secret", map[string]string{"Authorization": "Bearer <fake-rotated-key>", "X-Routine-Key": "<fake-custom-key>"})
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := New("https://receiver.example/hook", "fake-signing-secret")
	if first.Target() != second.Target() || first.Target() != legacy.Target() {
		t.Fatal("runtime auth changed destination identity")
	}
	d := Delivery{ID: "fake-delivery-id", Event: "request.created", Timestamp: "2026-11-10T12:00:00Z", Body: []byte(`{"event":"request.created","body":"unchanged"}`)}
	for i, sender := range []*Sender{first, first, second} {
		wantAuth := "Bearer <fake-first-key>"
		if i == 2 {
			wantAuth = "Bearer <fake-rotated-key>"
		}
		sender.client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Equal(body, d.Body) || r.Header.Get("X-Feedback-Signature") != signature("fake-signing-secret", d.Body) {
				t.Error("headers changed body or signature")
			}
			if r.Header.Get("Authorization") != wantAuth || r.Header.Get("X-Routine-Key") != "<fake-custom-key>" {
				t.Error("attempt missing current receiver credentials")
			}
			if r.Header.Get("X-Feedback-Event") != d.Event || r.Header.Get("X-Feedback-Delivery") != d.ID || r.Header.Get("X-Feedback-Timestamp") != d.Timestamp || r.Header.Get("Content-Type") != "application/json" {
				t.Error("protocol headers changed")
			}
			r.Header.Set("Authorization", "fake-transport-mutation")
			r.Header.Del("X-Routine-Key")
			status := 503
			if i == 2 {
				status = 204
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
		})
		d.Attempt = i + 1
		result := sender.Deliver(context.Background(), d)
		if result.Terminal != (i == 2) {
			t.Fatal("credential headers changed retry behavior")
		}
	}
}

func TestHeaderConfigurationValidation(t *testing.T) {
	for _, target := range []string{"", "https://receiver.example/hook"} {
		for _, headers := range []map[string]string{
			{"hOsT": "fake-routing-secret"}, {"tRaCePaReNt": "fake-trace-secret"}, {"bAgGaGe": "fake-baggage-secret"},
			{"Authorization": "fake-first", "authorization": "fake-second"}, {"fake-bad\nname": "fake-value"}, {"Authorization": "fake\r\nsecret"},
		} {
			sender, err := NewWithHeaders(target, "fake-signing-secret", headers)
			if err == nil || sender != nil {
				t.Fatal("unsafe programmatic headers accepted")
			}
			if strings.Contains(err.Error(), "fake-") {
				t.Fatal("configuration error leaked a credential")
			}
		}
	}
	for _, headers := range []map[string]string{nil, {}, {"Authorization": "Bearer <fake-key>"}} {
		sender, err := NewWithHeaders("", "", headers)
		if err != nil || sender != nil {
			t.Fatal("headers enabled an unconfigured destination")
		}
		sender.Run(context.Background(), nil, nil, nil)
	}
}

func TestHeaderCredentialsDoNotFollowRedirects(t *testing.T) {
	sender, err := NewWithHeaders("https://receiver.example/hook", "fake-signing-secret", map[string]string{"Authorization": "Bearer <fake-key>", "X-Routine-Key": "<fake-custom-key>"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	sender.client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "receiver.example" || r.Header.Get("Authorization") != "Bearer <fake-key>" || r.Header.Get("X-Routine-Key") != "<fake-custom-key>" {
			t.Error("credentials missing or redirected")
		}
		return &http.Response{StatusCode: 307, Header: http.Header{"Location": {"https://other.example/hook"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	result := sender.Deliver(context.Background(), Delivery{Attempt: 1})
	if calls != 1 || result.Status != 307 || !result.Terminal {
		t.Fatal("receiver authentication changed redirect policy")
	}
}

func TestHeaderOwnershipConcurrentDelivery(t *testing.T) {
	input := map[string]string{"Authorization": "Bearer <fake-key>"}
	sender, err := NewWithHeaders("https://receiver.example/hook", "fake-signing-secret", input)
	if err != nil {
		t.Fatal(err)
	}
	sender.client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer <fake-key>" {
			t.Error("sender retained caller-owned headers")
		}
		r.Header.Set("Authorization", "fake-request-mutation")
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 1000 {
			input["Authorization"] = "fake-concurrent-caller-mutation"
		}
	})
	for range 32 {
		wg.Go(func() {
			if result := sender.Deliver(context.Background(), Delivery{Attempt: 1}); result.Outcome != "delivered" {
				t.Error("concurrent delivery failed")
			}
		})
	}
	wg.Wait()
}

func TestHeaderSecretsExcludedFromDiagnostics(t *testing.T) {
	const authValue = "Bearer <fake-private-auth>"
	const customValue = "<fake-private-custom>"
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	defer func() { _ = provider.Shutdown(context.Background()) }()
	c := authorizationConfig(t, authValue)
	if c.WebhookHeaders["X-Routine-Key"] != customValue {
		t.Fatal("custom receiver credential missing")
	}
	sender, err := NewWithHeaders(c.WebhookURL, c.WebhookSecret, c.WebhookHeaders)
	if err != nil {
		t.Fatal(err)
	}
	sender.client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New(r.Header.Get("Authorization") + " " + r.Header.Get("X-Routine-Key"))
	})
	q := &memoryQueue{delivery: Delivery{ID: "fake-delivery-id", Event: "request.created"}, results: make(chan Result, 5)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs bytes.Buffer
	done := make(chan struct{})
	go func() { sender.Run(ctx, q, slog.New(slog.NewJSONHandler(&logs, nil)), nil); close(done) }()
	for range 5 {
		select {
		case result := <-q.results:
			if result.Outcome != "transport_failed" {
				t.Fatal("transport diagnostics reached the persisted outcome")
			}
		case <-time.After(time.Second):
			t.Fatal("worker stalled")
		}
	}
	cancel()
	<-done
	spans, err := json.Marshal(exporter.GetSpans())
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"fake-private-auth", "fake-private-custom"} {
		if strings.Contains(logs.String(), marker) || bytes.Contains(spans, []byte(marker)) || strings.Contains(sender.Target(), marker) {
			t.Fatal("receiver credentials escaped into diagnostics or fingerprint")
		}
	}
}
