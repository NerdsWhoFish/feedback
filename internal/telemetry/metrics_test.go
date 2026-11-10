package telemetry

import (
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
)

func scrapeMetrics(t *testing.T, m *Metrics) string {
	t.Helper()
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("scrape returned %d: %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

func TestHTTPMetricsBoundLabelsAndRecordErrors(t *testing.T) {
	m := NewMetrics(nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /requests/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("GET /panic", func(http.ResponseWriter, *http.Request) { panic("private panic") })
	h := HTTPWithMetrics(logger, mux, m)
	for _, target := range []string{"/requests/private-id?email=private@example.com", "/panic", "/private-missing"} {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.Header.Set("Authorization", "Bearer private-token")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PRIVATE-METHOD", "/private-missing", nil))
	body := scrapeMetrics(t, m)
	for _, want := range []string{
		`feedback_http_requests_total{method="GET",route="GET /requests/{id}",status="204"} 1`,
		`feedback_http_requests_total{method="GET",route="GET /panic",status="500"} 1`,
		`feedback_http_requests_total{method="GET",route="unmatched",status="404"} 1`,
		`feedback_http_requests_total{method="OTHER",route="unmatched",status="404"} 1`,
		`feedback_http_request_duration_seconds_count{method="GET",route="GET /requests/{id}"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in scrape", want)
		}
	}
	if strings.Contains(strings.ToLower(body), "private") {
		t.Fatal("private request data appeared in metrics")
	}
}

func TestHTTPMetricsPreserveStreamingFlush(t *testing.T) {
	m := NewMetrics(nil)
	h := HTTPWithMetrics(slog.New(slog.NewTextHandler(io.Discard, nil)), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Fatal("HTTP middleware removed Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte("data: ready\n\n"))
		w.(http.Flusher).Flush()
	}), m)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if !w.Flushed || w.Code != 200 || w.Body.String() != "data: ready\n\n" {
		t.Fatalf("streaming failed: %+v", w)
	}
	if !strings.Contains(scrapeMetrics(t, m), `feedback_http_requests_total{method="POST",route="unmatched",status="200"} 1`) {
		t.Fatal("flush-only response did not record status")
	}
}

func TestRequestMetricsReportScrapeFailuresWithoutStaleCounts(t *testing.T) {
	failed := false
	m := NewMetrics(func(ctx context.Context) (map[string]int64, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second {
			t.Error("database collection lacks bounded deadline")
		}
		if failed {
			return nil, errors.New("private database failure")
		}
		return map[string]int64{"open": 3, "shipped": 2, "private-status": 99}, nil
	})
	body := scrapeMetrics(t, m)
	for _, want := range []string{`feedback_requests{status="open"} 3`, `feedback_requests{status="shipped"} 2`, `feedback_requests{status="planned"} 0`, "feedback_request_counts_collection_success 1"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(body, "private-status") {
		t.Fatal("unbounded workflow status label")
	}
	failed = true
	body = scrapeMetrics(t, m)
	if !strings.Contains(body, "feedback_request_counts_collection_success 0") || strings.Contains(body, "feedback_requests{") || strings.Contains(body, "private") {
		t.Fatalf("collection error was hidden or leaked: %s", body)
	}
}

func TestWebhookMetricsConcurrentAndBounded(t *testing.T) {
	m := NewMetrics(nil)
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			m.WebhookAttempt("delivered")
			m.WebhookAttempt("http_rejected")
			m.WebhookAttempt("private-upstream-error")
		})
	}
	workers.Wait()
	body := scrapeMetrics(t, m)
	for _, outcome := range []string{"delivered", "http_rejected", "other"} {
		if !strings.Contains(body, `feedback_webhook_delivery_attempts_total{outcome="`+outcome+`"} 20`) {
			t.Errorf("missing outcome %s", outcome)
		}
	}
	if strings.Contains(body, "private") {
		t.Fatal("unbounded webhook outcome")
	}
	if strings.Contains(scrapeMetrics(t, NewMetrics(nil)), "feedback_webhook_delivery_attempts_total{") {
		t.Fatal("registries share process-global state")
	}
}
