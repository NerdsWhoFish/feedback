package webhook

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NerdsWhoFish/feedback/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestSignatureKnownVector(t *testing.T) {
	const want = "sha256=f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"
	if got := signature("key", []byte("The quick brown fox jumps over the lazy dog")); got != want {
		t.Fatalf("signature %q, want %q", got, want)
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func stubSender(t *testing.T, handler http.HandlerFunc) *Sender {
	t.Helper()
	s, err := New("https://receiver.example/original", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	s.client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler(w, r)
		return w.Result(), nil
	})
	return s
}

func TestExactBodyAndStableHeaders(t *testing.T) {
	body := []byte("{\"event\":\"request.created\", \"body\":\"a & b\\n雪\"}\n")
	d := Delivery{ID: "delivery-1", Event: "request.created", Timestamp: "2026-11-10T12:00:00Z", Body: body, Attempt: 1}
	var first http.Header
	s := stubSender(t, func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(got, body) {
			t.Error("raw body changed")
		}
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Feedback-Signature") != signature("test-secret", body) {
			t.Error("incorrect signed POST")
		}
		for key, want := range map[string]string{"X-Feedback-Event": d.Event, "X-Feedback-Delivery": d.ID, "X-Feedback-Timestamp": d.Timestamp} {
			if r.Header.Get(key) != want {
				t.Errorf("%s = %q, want %q", key, r.Header.Get(key), want)
			}
		}
		if first != nil && first.Get("X-Feedback-Signature") != r.Header.Get("X-Feedback-Signature") {
			t.Error("signature changed on retry")
		}
		first = r.Header.Clone()
		if r.Header.Get("Traceparent") == "" || r.Header.Get("Baggage") != "" || r.Header.Get("Tracestate") != "" {
			t.Error("trace correlation missing or private trace metadata forwarded")
		}
		w.WriteHeader(204)
	})
	traceID, _ := trace.TraceIDFromHex("12345678901234567890123456789012")
	spanID, _ := trace.SpanIDFromHex("1234567890123456")
	state, _ := trace.ParseTraceState("private=do-not-forward")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceState: state}))
	bag, _ := baggage.Parse("email=private@example.com")
	ctx = baggage.ContextWithBaggage(ctx, bag)
	for attempt := 1; attempt <= 2; attempt++ {
		d.Attempt = attempt
		result := s.Deliver(ctx, d)
		if result.Status != 204 || result.Outcome != "delivered" || !result.Terminal {
			t.Fatalf("delivery: %+v", result)
		}
	}
}

func TestRedirectsAndProxyDisabled(t *testing.T) {
	configured, err := New("https://receiver.example/original", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if configured.client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("ambient proxy enabled")
	}
	for _, location := range []string{"/other", "https://other.example/hook", "http://receiver.example/hook", "http://169.254.169.254/"} {
		var calls int
		s := stubSender(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.URL.String() != "https://receiver.example/original" {
				t.Error("redirect leaked payload")
			}
			w.Header().Set("Location", location)
			w.WriteHeader(307)
		})
		result := s.Deliver(context.Background(), Delivery{Attempt: 1, Body: []byte(`{}`)})
		if result.Status != 307 || !result.Terminal || calls != 1 {
			t.Fatalf("redirect result: %+v, calls=%d", result, calls)
		}
	}
}

func TestRetryPolicy(t *testing.T) {
	for _, status := range []int{200, 202, 204, 301, 307, 400, 401, 403, 404, 408, 409, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := stubSender(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
			for attempt := 1; attempt <= 5; attempt++ {
				result := s.Deliver(context.Background(), Delivery{Attempt: attempt, Body: []byte(`{}`)})
				retry := (status == 408 || status == 429 || status >= 500) && attempt < 5
				if result.Terminal == retry || result.Status != status {
					t.Fatalf("attempt %d: %+v, retry=%v", attempt, result, retry)
				}
				if retry && result.Delay != time.Duration(15*(1<<(attempt-1)))*time.Second {
					t.Fatalf("incorrect backoff: %s", result.Delay)
				}
			}
		})
	}
}

func TestAttemptTimeout(t *testing.T) {
	s, _ := New("https://receiver.example", "test-secret")
	s.client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	s.timeout = 20 * time.Millisecond
	result := s.Deliver(context.Background(), Delivery{Attempt: 1})
	if result.Outcome != "transport_failed" || result.Terminal {
		t.Fatalf("timeout did not schedule retry: %+v", result)
	}
}

type memoryQueue struct {
	mu       sync.Mutex
	delivery Delivery
	done     bool
	results  chan Result
}

func (q *memoryQueue) Claim(ctx context.Context, target string) (Delivery, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.done {
		return Delivery{}, ErrEmpty
	}
	q.delivery.Attempt++
	return q.delivery, nil
}

func (q *memoryQueue) Complete(ctx context.Context, d Delivery, r Result) error {
	q.mu.Lock()
	q.done = r.Terminal
	q.mu.Unlock()
	q.results <- r
	return nil
}

func TestWorkerRetriesAndShutdown(t *testing.T) {
	for _, successAt := range []int{3, 0} {
		t.Run(map[int]string{3: "success", 0: "exhaustion"}[successAt], func(t *testing.T) {
			var calls int
			s := stubSender(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == successAt {
					w.WriteHeader(204)
				} else {
					w.WriteHeader(503)
				}
			})
			q := &memoryQueue{delivery: Delivery{ID: "id", Event: "request.created", Body: []byte(`{"private":"body"}`)}, results: make(chan Result, 5)}
			var logs bytes.Buffer
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				s.Run(ctx, q, slog.New(slog.NewJSONHandler(&logs, nil)), telemetry.NewMetrics(nil))
				close(done)
			}()
			want := 5
			if successAt > 0 {
				want = successAt
			}
			for attempt := 1; attempt <= want; attempt++ {
				select {
				case result := <-q.results:
					if result.Terminal != (attempt == want) {
						t.Fatalf("attempt %d: %+v", attempt, result)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("worker stopped making progress")
				}
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("worker failed to shut down")
			}
			if calls != want || !strings.Contains(logs.String(), "outgoing webhook attempt completed") {
				t.Fatal("unexpected deliveries or missing outcome log")
			}
			for _, private := range []string{s.url, "test-secret", "private", "body"} {
				if strings.Contains(logs.String(), private) {
					t.Fatalf("log leaked %q", private)
				}
			}
		})
	}
}

func TestDisabledAndInvalidConfiguration(t *testing.T) {
	s, err := New("", "unused-secret")
	if err != nil || s != nil {
		t.Fatal("unset URL must fully disable outgoing webhooks")
	}
	// Nil dependencies prove disabled mode performs no queue work or delivery.
	s.Run(context.Background(), nil, nil, nil)
	for _, input := range [][2]string{{"https://receiver.example", ""}, {"https://receiver.example", "  "}, {"http://receiver.example", "secret"}} {
		if _, err := New(input[0], input[1]); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	a, _ := New("https://receiver.example/a", "secret-a")
	b, _ := New("https://receiver.example/b", "secret-a")
	c, _ := New("https://receiver.example/a", "secret-b")
	if a.Target() == b.Target() || a.Target() == c.Target() {
		t.Fatal("queued payloads not bound to both destination and signing key")
	}
}

type failedTransport struct{}

func (failedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("private destination detail")
}

func TestTransportFailureExhaustion(t *testing.T) {
	s, _ := New("https://receiver.example", "test-secret")
	s.client.Transport = failedTransport{}
	for attempt := 1; attempt <= 5; attempt++ {
		r := s.Deliver(context.Background(), Delivery{Attempt: attempt})
		if r.Status != 0 || r.Outcome != "transport_failed" || r.Terminal != (attempt == 5) {
			t.Fatalf("attempt %d: %+v", attempt, r)
		}
	}
}

func TestWorkerBoundsInFlightAndCancels(t *testing.T) {
	s, _ := New("https://receiver.example", "test-secret")
	started := make(chan struct{})
	s.client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	q := &memoryQueue{delivery: Delivery{ID: "id", Event: "request.created"}, results: make(chan Result, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx, q, slog.New(slog.NewTextHandler(io.Discard, nil)), nil); close(done) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker never started delivery")
	}
	q.mu.Lock()
	claims := q.delivery.Attempt
	q.mu.Unlock()
	if claims != 1 {
		t.Fatalf("worker claimed %d rows while delivery was blocked", claims)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the in-flight delivery")
	}
}

type countedBody struct {
	read   int
	closed bool
}

func (b *countedBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	b.read += len(p)
	return len(p), nil
}

func (b *countedBody) Close() error { b.closed = true; return nil }

func TestResponseBodyBoundedAndClosed(t *testing.T) {
	s, _ := New("https://receiver.example", "test-secret")
	body := &countedBody{}
	s.client.Transport = roundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 204, Body: body, Header: make(http.Header)}, nil
	})
	result := s.Deliver(context.Background(), Delivery{Attempt: 1})
	if result.Outcome != "delivered" || body.read != 4096 || !body.closed {
		t.Fatalf("unbounded or unclosed response: result=%+v read=%d closed=%v", result, body.read, body.closed)
	}
}

func TestWorkerTelemetryPrivacy(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	defer func() { _ = provider.Shutdown(context.Background()) }()
	s := stubSender(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	q := &memoryQueue{delivery: Delivery{ID: "delivery-1", Event: "request.created", Body: []byte(`{"email":"private@example.com"}`), TraceParent: "00-12345678901234567890123456789012-1234567890123456-01"}, results: make(chan Result, 1)}
	metrics := telemetry.NewMetrics(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx, q, slog.New(slog.NewTextHandler(io.Discard, nil)), metrics); close(done) }()
	select {
	case <-q.results:
	case <-time.After(time.Second):
		t.Fatal("worker stalled")
	}
	cancel()
	<-done
	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want job and HTTP", len(spans))
	}
	for _, span := range spans {
		if span.SpanContext.TraceID().String() != "12345678901234567890123456789012" {
			t.Fatal("outbox trace correlation lost")
		}
		for _, attr := range span.Attributes {
			if strings.Contains(attr.Value.String(), "private@example.com") || strings.Contains(attr.Value.String(), "receiver.example") || strings.Contains(attr.Value.String(), "test-secret") {
				t.Fatal("private value exported in span")
			}
		}
	}
	w := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(w.Body.String(), `feedback_webhook_delivery_attempts_total{outcome="delivered"} 1`) {
		t.Fatal("delivery metric missing")
	}
	if strings.Contains(w.Body.String(), "delivery-1") || strings.Contains(w.Body.String(), "private@example.com") {
		t.Fatal("high-cardinality or private metric label")
	}
}
