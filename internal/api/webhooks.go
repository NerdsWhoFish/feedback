package api

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func (s *Server) SetWebhookEncryptionKey(key []byte) error {
	if len(key) != 32 {
		return errors.New("webhook encryption key must be 32 bytes")
	}
	s.webhookKey = append([]byte{}, key...)
	return nil
}
func (s *Server) secretAEAD() (cipher.AEAD, error) {
	if len(s.webhookKey) != 32 {
		return nil, apiError{503, "webhooks_unavailable", "Webhook encryption is not configured."}
	}
	block, err := aes.NewCipher(s.webhookKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (s *Server) encryptSecret(secret string) ([]byte, error) {
	a, err := s.secretAEAD()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, []byte(secret), []byte("feedback/webhook/v1")), nil
}
func (s *Server) decryptSecret(value []byte) (string, error) {
	a, err := s.secretAEAD()
	if err != nil {
		return "", err
	}
	if len(value) < a.NonceSize() {
		return "", errors.New("invalid encrypted webhook secret")
	}
	plain, err := a.Open(nil, value[:a.NonceSize()], value[a.NonceSize():], []byte("feedback/webhook/v1"))
	return string(plain), err
}

var blockedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("fec0::/10"),
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, block := range blockedNetworks {
		if block.Contains(ip) {
			return false
		}
	}
	return true
}
func webhookURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(raw) > 2048 || (u.Port() != "" && u.Port() != "443") {
		return nil, invalid("Webhooks require a public HTTPS URL on port 443, without credentials or a fragment.")
	}
	return u, nil
}
func resolvePublic(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("webhook DNS lookup failed")
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, errors.New("webhook destination is not public")
		}
	}
	return ips, nil
}
func webhookClient() *http.Client {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" {
			return nil, errors.New("invalid webhook destination")
		}
		ips, err := resolvePublic(ctx, host)
		if err != nil {
			return nil, err
		}
		var last error
		for _, ip := range ips {
			conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}}
	return &http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("webhook redirects are forbidden") }}
}
func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request) error {
	if err := admin(r); err != nil {
		return err
	}
	id, err := s.project(r)
	if err != nil {
		return err
	}
	limit, offset, err := pagination(r)
	if err != nil {
		return err
	}
	items, err := maps(r.Context(), s.db, `SELECT id::text,url,events,created_at,disabled_at FROM webhooks WHERE project_id=$1 ORDER BY created_at DESC,id LIMIT $2 OFFSET $3`, id, limit, offset)
	if err != nil {
		return err
	}
	page(w, items, limit, offset)
	return nil
}
func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request) error {
	if err := admin(r); err != nil {
		return err
	}
	id, err := s.project(r)
	if err != nil {
		return err
	}
	var in struct {
		URL    string   `json:"url"`
		Events []string `json:"events"`
	}
	if err = decode(w, r, &in); err != nil {
		return err
	}
	u, err := webhookURL(in.URL)
	if err != nil {
		return err
	}
	if len(in.Events) < 1 || len(in.Events) > 3 {
		return invalid("Select 1 to 3 webhook events.")
	}
	seen := map[string]bool{}
	for _, event := range in.Events {
		if (event != "status_changed" && event != "request_created" && event != "comment_created") || seen[event] {
			return invalid("Unknown or duplicate webhook event.")
		}
		seen[event] = true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, err = resolvePublic(ctx, u.Hostname()); err != nil {
		return invalid("Webhook destination must resolve only to public addresses.")
	}
	secret, err := token()
	if err != nil {
		return err
	}
	encrypted, err := s.encryptSecret(secret)
	if err != nil {
		return err
	}
	items, err := maps(r.Context(), s.db, `INSERT INTO webhooks(project_id,url,events,encrypted_secret) VALUES($1,$2,$3,$4) RETURNING id::text,url,events,created_at,disabled_at`, id, in.URL, in.Events, encrypted)
	if err != nil {
		return err
	}
	items[0]["secret"] = secret
	write(w, 201, items[0])
	return nil
}
func (s *Server) deleteWebhook(w http.ResponseWriter, r *http.Request) error {
	if err := admin(r); err != nil {
		return err
	}
	id, err := s.project(r)
	if err != nil {
		return err
	}
	if !idPattern.MatchString(r.PathValue("id")) {
		return missing
	}
	tag, err := s.db.Exec(r.Context(), `UPDATE webhooks SET disabled_at=COALESCE(disabled_at,now()) WHERE id=$1 AND project_id=$2`, r.PathValue("id"), id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return missing
	}
	write(w, 204, nil)
	return nil
}
func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request) error {
	if err := admin(r); err != nil {
		return err
	}
	id, err := s.project(r)
	if err != nil {
		return err
	}
	limit, offset, err := pagination(r)
	if err != nil {
		return err
	}
	items, err := maps(r.Context(), s.db, `SELECT d.id::text,d.webhook_id::text,d.event,d.attempts,d.next_attempt_at,d.completed_at,d.last_status,d.last_error,d.created_at,COALESCE((SELECT jsonb_agg(jsonb_build_object('attempt',a.attempt,'status',a.status,'outcome',a.outcome,'created_at',a.created_at) ORDER BY a.id) FROM webhook_attempts a WHERE a.delivery_id=d.id),'[]'::jsonb) AS delivery_log FROM webhook_deliveries d JOIN webhooks w ON w.id=d.webhook_id WHERE w.project_id=$1 ORDER BY d.created_at DESC,d.id LIMIT $2 OFFSET $3`, id, limit, offset)
	if err != nil {
		return err
	}
	page(w, items, limit, offset)
	return nil
}
func (s *Server) enqueue(ctx context.Context, tx pgx.Tx, project, event string, payload map[string]any) error {
	payload["event"] = event
	payload["project_id"] = project
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	_, err = tx.Exec(ctx, `INSERT INTO webhook_deliveries(webhook_id,event,payload,traceparent) SELECT id,$2,$3,$4 FROM webhooks WHERE project_id=$1 AND disabled_at IS NULL AND $2=ANY(events)`, project, event, body, carrier.Get("traceparent"))
	return err
}

type delivery struct {
	ID, URL         string
	Secret, Payload []byte
	Attempt         int
	TraceParent     string
}

func (s *Server) claimDelivery(ctx context.Context) (delivery, error) {
	var d delivery
	// A crash during the last attempt must still reach a terminal state.
	if _, err := s.db.Exec(ctx, `UPDATE webhook_deliveries SET completed_at=now(),last_error='lease_expired',lease_until=NULL WHERE completed_at IS NULL AND attempts>=8 AND lease_until<now()`); err != nil {
		return d, err
	}
	err := s.db.QueryRow(ctx, `WITH candidate AS (SELECT d.id FROM webhook_deliveries d JOIN webhooks w ON w.id=d.webhook_id WHERE d.completed_at IS NULL AND d.attempts<8 AND d.next_attempt_at<=now() AND (d.lease_until IS NULL OR d.lease_until<now()) AND w.disabled_at IS NULL ORDER BY d.next_attempt_at FOR UPDATE OF d SKIP LOCKED LIMIT 1) UPDATE webhook_deliveries d SET attempts=attempts+1,lease_until=now()+interval '30 seconds' FROM candidate c,webhooks w WHERE d.id=c.id AND w.id=d.webhook_id RETURNING d.id::text,w.url,w.encrypted_secret,d.payload,d.attempts,d.traceparent`).Scan(&d.ID, &d.URL, &d.Secret, &d.Payload, &d.Attempt, &d.TraceParent)
	return d, err
}
func sign(secret, timestamp string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
func (s *Server) deliver(ctx context.Context, client *http.Client, d delivery) (int, string) {
	ctx, span := otel.Tracer("feedback/webhooks").Start(ctx, "webhook.http", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	secret, err := s.decryptSecret(d.Secret)
	if err != nil {
		return 0, "secret_unavailable"
	}
	if _, err = webhookURL(d.URL); err != nil {
		return 0, "invalid_destination"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, strings.NewReader(string(d.Payload)))
	if err != nil {
		return 0, "invalid_destination"
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Feedback-Delivery", d.ID)
	req.Header.Set("X-Feedback-Timestamp", timestamp)
	req.Header.Set("X-Feedback-Signature", sign(secret, timestamp, d.Payload))
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(req.Header))
	resp, err := client.Do(req)
	if err != nil {
		span.SetStatus(codes.Error, "transport_failed")
		return 0, "transport_failed"
	}
	defer func() { _ = resp.Body.Close() }()
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, "delivered"
	}
	span.SetStatus(codes.Error, "http_rejected")
	return resp.StatusCode, "http_rejected"
}
func (s *Server) completeDelivery(ctx context.Context, d delivery, status int, outcome string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	done := outcome == "delivered" || d.Attempt >= 8
	delay := time.Duration(15*(1<<min(d.Attempt-1, 8))) * time.Second
	tag, err := tx.Exec(ctx, `UPDATE webhook_deliveries SET last_status=$3,last_error=$4,lease_until=NULL,next_attempt_at=now()+($5*interval '1 second'),completed_at=CASE WHEN $6 THEN now() ELSE NULL END WHERE id=$1 AND attempts=$2`, d.ID, d.Attempt, status, outcome, delay.Seconds(), done)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO webhook_attempts(delivery_id,attempt,status,outcome) VALUES($1,$2,$3,$4)`, d.ID, d.Attempt, status, outcome); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Server) RunWebhooks(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	client := webhookClient()
	defer client.CloseIdleConnections()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if len(s.webhookKey) != 32 {
				continue
			}
			for range 10 {
				if ctx.Err() != nil {
					return
				}
				d, err := s.claimDelivery(ctx)
				if errors.Is(err, pgx.ErrNoRows) {
					break
				}
				if err != nil {
					s.log.ErrorContext(ctx, "webhook claim failed")
					break
				}
				parent := propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": d.TraceParent})
				spanCtx, span := otel.Tracer("feedback/webhooks").Start(parent, "webhook.delivery")
				status, outcome := s.deliver(spanCtx, client, d)
				span.SetAttributes(attribute.String("delivery.outcome", outcome), attribute.Int("http.response.status_code", status))
				if outcome != "delivered" {
					span.SetStatus(codes.Error, "delivery_failed")
				}
				s.log.InfoContext(spanCtx, "webhook attempt completed", "outcome", outcome, "status", status)
				if err = s.completeDelivery(spanCtx, d, status, outcome); err != nil {
					s.log.ErrorContext(spanCtx, "webhook result persistence failed")
				}
				span.End()
			}
		}
	}
}
