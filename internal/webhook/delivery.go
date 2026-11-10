package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/NerdsWhoFish/feedback/internal/config"
	"github.com/NerdsWhoFish/feedback/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const MaxAttempts = 5

var ErrEmpty = errors.New("no outgoing webhook ready")

type Delivery struct {
	ID, Event, Timestamp, TraceParent string
	Body                              []byte
	Attempt                           int
}

type Result struct {
	Status   int
	Outcome  string
	Terminal bool
	Delay    time.Duration
}

type Queue interface {
	Claim(context.Context, string) (Delivery, error)
	Complete(context.Context, Delivery, Result) error
}

type Sender struct {
	url, secret string
	headers     map[string]string
	client      *http.Client
	timeout     time.Duration
}

func New(url, secret string) (*Sender, error) {
	return NewWithHeaders(url, secret, nil)
}

func NewWithHeaders(url, secret string, headers map[string]string) (*Sender, error) {
	owned, err := config.CopyWebhookHeaders(headers)
	if err != nil {
		return nil, err
	}
	if url == "" {
		return nil, nil
	}
	if !config.ValidWebhookEndpoint(url) {
		return nil, errors.New("invalid WEBHOOK_URL")
	}
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("WEBHOOK_SECRET is required when WEBHOOK_URL is configured")
	}
	transport := &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second,
		MaxIdleConns: 1, MaxIdleConnsPerHost: 1, MaxConnsPerHost: 1, IdleConnTimeout: 30 * time.Second,
		MaxResponseHeaderBytes: 32 << 10,
	}
	return &Sender{url: url, secret: secret, headers: owned, timeout: 10 * time.Second, client: &http.Client{
		Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// Target binds URL/signing key only, allowing receiver auth to rotate.
func (s *Sender) Target() string { return signature(s.secret, []byte(s.url)) }

func signature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (s *Sender) Deliver(ctx context.Context, d Delivery) Result {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	ctx, span := otel.Tracer("feedback/outgoing-webhooks").Start(ctx, "outgoing_webhook.http", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	result := Result{Outcome: "transport_failed"}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(d.Body))
	if err == nil {
		for name, value := range s.headers {
			req.Header.Set(name, value)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Feedback-Event", d.Event)
		req.Header.Set("X-Feedback-Delivery", d.ID)
		req.Header.Set("X-Feedback-Timestamp", d.Timestamp)
		req.Header.Set("X-Feedback-Signature", signature(s.secret, d.Body))
		// Customer-controlled baggage and tracestate must not cross this boundary.
		clean := trace.SpanContextFromContext(ctx).WithTraceState(trace.TraceState{})
		propagation.TraceContext{}.Inject(trace.ContextWithSpanContext(ctx, clean), propagation.HeaderCarrier(req.Header))
		var response *http.Response
		response, err = s.client.Do(req)
		if err == nil {
			result.Status = response.StatusCode
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			result.Outcome = "http_rejected"
			if result.Status >= 200 && result.Status < 300 {
				result.Outcome = "delivered"
			}
		}
	}
	retry := err != nil || result.Status == 408 || result.Status == 429 || (result.Status >= 500 && result.Status <= 599)
	result.Terminal = !retry || d.Attempt >= MaxAttempts
	if !result.Terminal {
		result.Delay = time.Duration(15*(1<<min(max(d.Attempt-1, 0), MaxAttempts-1))) * time.Second
	}
	span.SetAttributes(attribute.Int("http.response.status_code", result.Status), attribute.String("delivery.outcome", result.Outcome))
	if result.Outcome != "delivered" {
		span.SetStatus(codes.Error, result.Outcome)
	}
	return result
}

// Run blocks until cancellation; join it before closing the database.
func (s *Sender) Run(ctx context.Context, queue Queue, logger *slog.Logger, metrics *telemetry.Metrics) {
	if s == nil {
		return
	}
	defer s.client.CloseIdleConnections()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		for range 10 {
			claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			d, err := queue.Claim(claimCtx, s.Target())
			cancel()
			if errors.Is(err, ErrEmpty) || ctx.Err() != nil {
				break
			}
			if err != nil {
				logger.ErrorContext(ctx, "outgoing webhook claim failed")
				break
			}
			parent := propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": d.TraceParent})
			jobCtx, span := otel.Tracer("feedback/outgoing-webhooks").Start(parent, "outgoing_webhook.delivery", trace.WithSpanKind(trace.SpanKindConsumer))
			result := s.Deliver(jobCtx, d)
			metrics.WebhookAttempt(result.Outcome)
			span.SetAttributes(attribute.String("event.name", d.Event), attribute.Int("delivery.attempt", d.Attempt), attribute.Bool("delivery.terminal", result.Terminal), attribute.String("delivery.outcome", result.Outcome))
			if result.Outcome != "delivered" {
				span.SetStatus(codes.Error, result.Outcome)
			}
			logger.InfoContext(jobCtx, "outgoing webhook attempt completed", "event", d.Event, "delivery_id", d.ID, "attempt", d.Attempt, "outcome", result.Outcome, "status", result.Status, "terminal", result.Terminal)
			completeCtx, completeCancel := context.WithTimeout(jobCtx, 5*time.Second)
			err = queue.Complete(completeCtx, d, result)
			completeCancel()
			if err != nil && ctx.Err() == nil {
				span.SetStatus(codes.Error, "result_persistence_failed")
				logger.ErrorContext(jobCtx, "outgoing webhook result persistence failed", "delivery_id", d.ID)
			}
			span.End()
			if ctx.Err() != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
