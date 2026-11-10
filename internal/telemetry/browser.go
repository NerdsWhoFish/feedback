package telemetry

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/NerdsWhoFish/feedback/internal/auth"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

// Rebuild envelopes so browser text and identifiers cannot enter telemetry.
func safeEnvelope(raw map[string]json.RawMessage, environment, version string) map[string]any {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result := map[string]any{"meta": map[string]any{"app": map[string]string{"name": "feedback", "version": version, "environment": environment}, "sdk": map[string]string{"name": "feedback-relay", "version": "1.0.0"}}}
	var exceptions []json.RawMessage
	if json.Unmarshal(raw["exceptions"], &exceptions) == nil && len(exceptions) > 0 {
		result["exceptions"] = []any{map[string]any{"type": "Error", "value": "Browser operation failed", "timestamp": now}}
	}
	var logs []json.RawMessage
	if json.Unmarshal(raw["logs"], &logs) == nil && len(logs) > 0 {
		result["logs"] = []any{map[string]any{"level": "error", "message": "Browser operation failed", "timestamp": now}}
	}
	var events []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw["events"], &events) == nil {
		allowed := map[string]bool{"request.load": true, "request.submit": true, "comment.submit": true, "vote.update": true, "status.update": true, "admin.update": true, "app.render": true, "view_changed": true, "session_start": true, "session_resume": true, "session_extend": true, "page_load": true}
		clean := []any{}
		for _, e := range events {
			if allowed[e.Name] && len(clean) < 20 {
				clean = append(clean, map[string]string{"name": e.Name, "timestamp": now})
			}
		}
		if len(clean) > 0 {
			result["events"] = clean
		}
	}
	var measurements []struct {
		Type   string             `json:"type"`
		Values map[string]float64 `json:"values"`
	}
	if json.Unmarshal(raw["measurements"], &measurements) == nil {
		clean := []any{}
		for _, m := range measurements {
			if m.Type != "web-vital" && m.Type != "web-vitals" {
				continue
			}
			values := map[string]float64{}
			for _, key := range []string{"cls", "fcp", "fid", "inp", "lcp", "ttfb"} {
				v, ok := m.Values[key]
				if !ok {
					v, ok = m.Values[strings.ToUpper(key)]
				}
				if ok && !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v < 1e9 {
					values[key] = v
				}
			}
			if len(values) > 0 && len(clean) < 10 {
				clean = append(clean, map[string]any{"type": "web-vitals", "values": values, "timestamp": now})
			}
		}
		if len(clean) > 0 {
			result["measurements"] = clean
		}
	}
	return result
}

func BrowserRelay(origin, endpoint, environment, version string, logger *slog.Logger) http.Handler {
	limiter := auth.NewLimiter(600)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if endpoint == "" {
			w.WriteHeader(204)
			return
		}
		if r.Header.Get("Origin") != origin {
			http.Error(w, "Forbidden", 403)
			return
		}
		if !limiter.Allow("relay") {
			http.Error(w, "Rate limit exceeded", 429)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		var raw map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&raw) != nil {
			http.Error(w, "Invalid telemetry", 400)
			return
		}
		envelope := safeEnvelope(raw, environment, version)
		if len(envelope) == 1 {
			w.WriteHeader(204)
			return
		}
		ctx, span := otel.Tracer("feedback/browser").Start(r.Context(), "browser.relay")
		defer span.End()
		if envelope["exceptions"] != nil || envelope["logs"] != nil {
			logger.ErrorContext(ctx, "browser operation failed")
		}
		body, _ := json.Marshal(envelope)
		request, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
		if err != nil {
			http.Error(w, "Telemetry unavailable", 503)
			return
		}
		var id [16]byte
		_, _ = rand.Read(id[:])
		request.Header.Set("X-Faro-Session-Id", hex.EncodeToString(id[:]))
		request.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(request)
		if err != nil {
			span.SetStatus(codes.Error, "relay failed")
			logger.ErrorContext(ctx, "browser telemetry delivery failed")
			http.Error(w, "Telemetry unavailable", 503)
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			span.SetStatus(codes.Error, "relay rejected")
			logger.ErrorContext(ctx, "browser telemetry delivery rejected", "status", resp.StatusCode)
			http.Error(w, "Telemetry unavailable", 503)
			return
		}
		w.WriteHeader(202)
	})
}
