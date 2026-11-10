package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	logpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestExportProtobufAndSafeResources(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	collector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/x-protobuf" {
			t.Error("non-protobuf telemetry")
		}
		if r.Header.Get("Authorization") != "Basic test-fixture" {
			t.Error("missing explicit authorization header")
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		switch r.URL.Path {
		case "/otlp/v1/traces":
			var req tracepb.ExportTraceServiceRequest
			if proto.Unmarshal(b, &req) != nil {
				t.Error("invalid trace protobuf")
			}
			for _, rs := range req.ResourceSpans {
				for _, a := range rs.Resource.Attributes {
					if a.Key != "service.name" && a.Key != "service.version" && a.Key != "deployment.environment.name" {
						t.Errorf("unexpected resource %s", a.Key)
					}
				}
			}
		case "/otlp/v1/logs":
			var req logpb.ExportLogsServiceRequest
			if proto.Unmarshal(b, &req) != nil {
				t.Error("invalid log protobuf")
			}
			for _, rl := range req.ResourceLogs {
				for _, scope := range rl.ScopeLogs {
					for _, record := range scope.LogRecords {
						if len(record.TraceId) != 16 || len(record.SpanId) != 8 {
							t.Error("log missing span correlation")
						}
					}
				}
			}
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		mu.Lock()
		counts[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(200)
	}))
	defer collector.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL+"/otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=Basic test-fixture")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://127.0.0.1:1/wrong")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "http://127.0.0.1:1/wrong")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/json")
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_INSECURE", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_INSECURE", "true")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
	t.Setenv("OTEL_SERVICE_NAME", "")
	logger, shutdown, err := setup(context.Background(), "local", "test", collector.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, span := otel.Tracer("test").Start(context.Background(), "test.operation")
	span.SetAttributes(attribute.String("operation", "test"))
	logger.InfoContext(ctx, "test operation")
	span.End()
	if err = shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if counts["/otlp/v1/traces"] != 1 || counts["/otlp/v1/logs"] != 1 {
		t.Fatalf("missing export: %v", counts)
	}
}

func TestTelemetryConfigurationFailsClosed(t *testing.T) {
	for _, raw := range []string{"Authorization", "Authorization=Basic test%0Asecret", "bad key=value", "Authorization="} {
		if _, err := parseHeaders(raw); err == nil {
			t.Fatalf("accepted invalid header %q", raw)
		}
	}
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "private.attribute=private-value")
	if _, _, err := Setup(context.Background(), "local", "test"); err == nil {
		t.Fatal("ambient resource accepted")
	}
}

func TestRouteAndDatabaseTelemetryExcludePrivateData(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /requests/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	r := httptest.NewRequest("GET", "https://example.com/requests/private-id?email=private@example.com", nil)
	r.Header.Set("Authorization", "Bearer private-token")
	HTTP(logger, mux).ServeHTTP(httptest.NewRecorder(), r)
	body := output.String()
	if strings.Contains(body, "private") || !strings.Contains(body, "/requests/{id}") {
		t.Fatalf("unsafe or missing route template: %s", body)
	}
}

func TestBrowserRelayRebuildsEnvelope(t *testing.T) {
	raw := map[string]json.RawMessage{
		"meta":         json.RawMessage(`{"user":{"email":"PRIVATE"},"page":{"url":"PRIVATE"}}`),
		"exceptions":   json.RawMessage(`[{"value":"PRIVATE","stacktrace":"PRIVATE"}]`),
		"logs":         json.RawMessage(`[{"message":"PRIVATE"}]`),
		"events":       json.RawMessage(`[{"name":"PRIVATE"},{"name":"page_load","attributes":{"url":"PRIVATE"}},{"name":"request.submit"}]`),
		"measurements": json.RawMessage(`[{"type":"web-vitals","values":{"lcp":100,"LCP":200,"private":10,"cls":0.1}}]`),
		"traces":       json.RawMessage(`{"PRIVATE":"PRIVATE"}`),
	}
	cleaned := safeEnvelope(raw, "test", "test")
	body, _ := json.Marshal(cleaned)
	if strings.Contains(string(body), "PRIVATE") || strings.Contains(string(body), "traces") || !strings.Contains(string(body), `"lcp":100`) {
		t.Fatalf("unsafe envelope: %s", body)
	}
	var received bool
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = true
		if len(r.Header.Get("X-Faro-Session-Id")) != 32 {
			t.Error("missing opaque envelope ID")
		}
		var envelope map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&envelope)
		if strings.Contains(string(envelope["meta"]), "PRIVATE") {
			t.Error("private metadata escaped")
		}
		w.WriteHeader(202)
	}))
	defer collector.Close()
	relay := BrowserRelay("https://feedback.example", collector.URL, "test", "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	original, _ := json.Marshal(raw)
	r := httptest.NewRequest("POST", "/api/v1/telemetry", bytes.NewReader(original))
	r.Header.Set("Origin", "https://feedback.example")
	w := httptest.NewRecorder()
	relay.ServeHTTP(w, r)
	if w.Code != 202 || !received {
		t.Fatalf("relay failed: %d", w.Code)
	}
	r = httptest.NewRequest("POST", "/api/v1/telemetry", bytes.NewReader(original))
	r.Header.Set("Origin", "https://attacker.example")
	w = httptest.NewRecorder()
	relay.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin relay accepted")
	}
}
