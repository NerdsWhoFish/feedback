package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/NerdsWhoFish/feedback/internal/config"
	"github.com/go-logr/logr"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	logsdk "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type handler struct{ local, remote slog.Handler }

func (h handler) Enabled(ctx context.Context, l slog.Level) bool { return h.local.Enabled(ctx, l) }
func (h handler) Handle(ctx context.Context, r slog.Record) error {
	sc := trace.SpanContextFromContext(ctx)
	if sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	err := h.local.Handle(ctx, r)
	if h.remote != nil {
		err = errors.Join(err, h.remote.Handle(ctx, r))
	}
	return err
}
func (h handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.local = h.local.WithAttrs(attrs)
	if h.remote != nil {
		h.remote = h.remote.WithAttrs(attrs)
	}
	return h
}
func (h handler) WithGroup(name string) slog.Handler {
	h.local = h.local.WithGroup(name)
	if h.remote != nil {
		h.remote = h.remote.WithGroup(name)
	}
	return h
}

func Setup(ctx context.Context, environment, version string) (*slog.Logger, func(context.Context) error, error) {
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return setup(ctx, environment, version, client)
}

func setup(ctx context.Context, environment, version string, client *http.Client) (*slog.Logger, func(context.Context) error, error) {
	local := slog.NewJSONHandler(os.Stderr, nil)
	logger := slog.New(handler{local: local})
	// SDK diagnostics can contain raw headers before the error handler runs.
	otel.SetLogger(logr.Discard())
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) { logger.Error("telemetry export failed") }))
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	base := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if environment != "local" && base == "" {
		return nil, nil, errors.New("OTEL_EXPORTER_OTLP_ENDPOINT is required outside local development")
	}
	if os.Getenv("OTEL_RESOURCE_ATTRIBUTES") != "" || os.Getenv("OTEL_SERVICE_NAME") != "" {
		return nil, nil, errors.New("ambient telemetry resource identity is not supported")
	}
	res := resource.NewSchemaless(attribute.String("service.name", "feedback"), attribute.String("service.version", version), attribute.String("deployment.environment.name", environment))
	opts := []tracesdk.TracerProviderOption{tracesdk.WithResource(res)}
	var lp *logsdk.LoggerProvider
	if base != "" {
		if !config.ValidEndpoint(base, environment == "local") {
			return nil, nil, errors.New("invalid OTLP endpoint")
		}
		headers, err := parseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
		if err != nil {
			return nil, nil, err
		}
		tr, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(strings.TrimRight(base, "/")+"/v1/traces"), otlptracehttp.WithHeaders(headers), otlptracehttp.WithHTTPClient(client), otlptracehttp.WithEncoding(otlptracehttp.EncodingProtobuf))
		if err != nil {
			return nil, nil, errors.New("trace exporter configuration failed")
		}
		le, err := otlploghttp.New(ctx, otlploghttp.WithEndpointURL(strings.TrimRight(base, "/")+"/v1/logs"), otlploghttp.WithHeaders(headers), otlploghttp.WithHTTPClient(client))
		if err != nil {
			_ = tr.Shutdown(ctx)
			return nil, nil, errors.New("log exporter configuration failed")
		}
		opts = append(opts, tracesdk.WithBatcher(tr))
		lp = logsdk.NewLoggerProvider(logsdk.WithResource(res), logsdk.WithProcessor(logsdk.NewBatchProcessor(le)))
		logger = slog.New(handler{local: local, remote: otelslog.NewHandler("feedback", otelslog.WithLoggerProvider(lp))})
	}
	tp := tracesdk.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	slog.SetDefault(logger)
	return logger, func(ctx context.Context) error {
		err := tp.Shutdown(ctx)
		if lp != nil {
			err = errors.Join(err, lp.Shutdown(ctx))
		}
		return err
	}, nil
}

func parseHeaders(raw string) (map[string]string, error) {
	headers := map[string]string{}
	if raw == "" {
		return headers, nil
	}
	for _, part := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, errors.New("invalid OTLP header configuration")
		}
		decoded, err := url.PathUnescape(value)
		if err != nil || strings.ContainsAny(decoded, "\r\n") || decoded == "" {
			return nil, errors.New("invalid OTLP header configuration")
		}
		for _, c := range key {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
				return nil, errors.New("invalid OTLP header configuration")
			}
		}
		headers[key] = decoded
	}
	return headers, nil
}

type response struct {
	http.ResponseWriter
	status int
}

func (w *response) WriteHeader(status int) {
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *response) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (w *response) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *response) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *response) Flush() { _ = w.FlushError() }

func HTTP(logger *slog.Logger, next http.Handler) http.Handler {
	return HTTPWithMetrics(logger, next, nil)
}

func HTTPWithMetrics(logger *slog.Logger, next http.Handler, metrics *Metrics) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer("feedback/http").Start(ctx, "http.request", trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		request := r.WithContext(ctx)
		rw := &response{ResponseWriter: w}
		defer func() {
			if recover() != nil {
				logger.ErrorContext(ctx, "request panic")
				if rw.status == 0 {
					http.Error(rw, "Internal server error", 500)
				}
			}
			route := request.Pattern
			if route == "" {
				route = "unmatched"
			}
			status := rw.status
			if status == 0 {
				status = 200
			}
			metrics.observeHTTP(request.Method, route, status, time.Since(start))
			span.SetName(route)
			span.SetAttributes(attribute.String("http.route", route), attribute.Int("http.response.status_code", status))
			if status >= 500 {
				span.SetStatus(codes.Error, "request failed")
			}
			level := slog.LevelInfo
			if status >= 500 {
				level = slog.LevelError
			}
			logger.Log(ctx, level, "request completed", "route", route, "status", status, "duration_ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(rw, request)
	})
}

type PGTracer struct{}

func (PGTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	op := "query"
	words := strings.Fields(data.SQL)
	if len(words) > 0 {
		switch strings.ToUpper(words[0]) {
		case "SELECT", "INSERT", "UPDATE", "DELETE", "BEGIN", "COMMIT", "ROLLBACK", "CREATE", "ALTER":
			op = strings.ToLower(words[0])
		}
	}
	ctx, _ = otel.Tracer("feedback/database").Start(ctx, "db."+op, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attribute.String("db.system.name", "postgresql")))
	return ctx
}
func (PGTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span := trace.SpanFromContext(ctx)
	if data.Err != nil && !errors.Is(data.Err, pgx.ErrNoRows) {
		span.SetStatus(codes.Error, "database operation failed")
	}
	span.End()
}
