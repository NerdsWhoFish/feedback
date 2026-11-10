package telemetry

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var requestStatuses = [...]string{"open", "under_review", "planned", "approved_for_build", "in_progress", "shipped", "declined"}

type RequestCounts func(context.Context) (map[string]int64, error)

type Metrics struct {
	registry *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	webhooks *prometheus.CounterVec
}

func NewMetrics(counts RequestCounts) *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "feedback_http_requests_total", Help: "Completed HTTP requests."}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "feedback_http_request_duration_seconds", Help: "HTTP request duration in seconds.", Buckets: prometheus.DefBuckets}, []string{"method", "route"}),
		webhooks: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "feedback_webhook_delivery_attempts_total", Help: "Webhook delivery attempts by outcome, including retries."}, []string{"outcome"}),
	}
	m.registry.MustRegister(m.requests, m.duration, m.webhooks)
	if counts != nil {
		m.registry.MustRegister(&requestCountCollector{
			counts:   counts,
			requests: prometheus.NewDesc("feedback_requests", "Current requests by workflow status.", []string{"status"}, nil),
			success:  prometheus.NewDesc("feedback_request_counts_collection_success", "Whether the current request-count collection succeeded.", nil, nil),
		})
	}
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Timeout: 5 * time.Second, MaxRequestsInFlight: 2})
}

func (m *Metrics) observeHTTP(method, route string, status int, elapsed time.Duration) {
	if m == nil {
		return
	}
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
	default:
		method = "OTHER"
	}
	code := "other"
	if status >= 100 && status <= 599 {
		code = strconv.Itoa(status)
	}
	m.requests.WithLabelValues(method, route, code).Inc()
	m.duration.WithLabelValues(method, route).Observe(elapsed.Seconds())
}

func (m *Metrics) WebhookAttempt(outcome string) {
	if m == nil {
		return
	}
	switch outcome {
	case "delivered", "secret_unavailable", "invalid_destination", "transport_failed", "http_rejected":
	default:
		outcome = "other"
	}
	m.webhooks.WithLabelValues(outcome).Inc()
}

type requestCountCollector struct {
	counts            RequestCounts
	requests, success *prometheus.Desc
}

func (c *requestCountCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.requests
	ch <- c.success
}

func (c *requestCountCollector) Collect(ch chan<- prometheus.Metric) {
	// Collectors have no request context. Bound database work independently so a
	// disconnected scraper cannot leave an unbounded query behind.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	counts, err := c.counts(ctx)
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.success, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.success, prometheus.GaugeValue, 1)
	for _, status := range requestStatuses {
		ch <- prometheus.MustNewConstMetric(c.requests, prometheus.GaugeValue, float64(counts[status]), status)
	}
}
