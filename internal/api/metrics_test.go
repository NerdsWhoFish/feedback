package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NerdsWhoFish/feedback/internal/telemetry"
)

func TestRequestCountsReflectPersistedTransitions(t *testing.T) {
	f := testFixture(t)
	one := f.request(t, f.member, f.project, nil)
	f.request(t, f.outsider, f.other, nil)
	m := telemetry.NewMetrics(f.s.RequestCounts)
	f.s.SetMetrics(m)
	status, body := f.call(t, f.admin, http.MethodPost, "/api/v1/requests/"+one["id"].(string)+"/status", map[string]any{"status": "shipped"})
	expectStatus(t, 200, status, body)
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `feedback_requests{status="open"} 1`) || !strings.Contains(w.Body.String(), `feedback_requests{status="shipped"} 1`) {
		t.Fatalf("incorrect persisted counts: %s", w.Body.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.s.RequestCounts(ctx); err == nil {
		t.Fatal("canceled collection returned successful counts")
	}
}
