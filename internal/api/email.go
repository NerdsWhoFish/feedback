package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NerdsWhoFish/feedback/internal/auth"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type emailConfig struct {
	apiKey, from, baseURL string
	admins                []string
}

// ConfigureEmail must run before handlers and workers start.
func (s *Server) ConfigureEmail(apiKey, from, baseURL string, adminEmails map[string]bool) error {
	if apiKey == "" {
		s.email = nil
		return nil
	}
	if strings.ContainsAny(apiKey, "\r\n") {
		return errors.New("RESEND_API_KEY contains invalid characters")
	}
	address, err := mail.ParseAddress(from)
	if err != nil || strings.ContainsAny(from, "\r\n") || address.Address == "" {
		return errors.New("EMAIL_FROM must be a valid sender address when email is enabled")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("email requires a valid BASE_URL origin")
	}
	s.email = &emailConfig{apiKey: apiKey, from: from, baseURL: strings.TrimRight(baseURL, "/")}
	for address, enabled := range adminEmails {
		if enabled {
			s.email.admins = append(s.email.admins, strings.ToLower(address))
		}
	}
	if s.email.admins == nil {
		s.email.admins = []string{}
	}
	sort.Strings(s.email.admins)
	return nil
}

func (s *Server) emailPreferences(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	if !p.Browser() {
		return denied
	}
	var enabled bool
	if r.Method == http.MethodPut {
		var in struct {
			Enabled *bool `json:"enabled"`
		}
		if err := decode(w, r, &in); err != nil {
			return err
		}
		if in.Enabled == nil {
			return invalid("enabled is required.")
		}
		// OIDC and app identities sharing an email must agree on consent.
		if _, err := s.db.Exec(r.Context(), `UPDATE users SET email_notifications=$2 WHERE lower(email)=(SELECT lower(email) FROM users WHERE id=$1)`, p.UserID, *in.Enabled); err != nil {
			return err
		}
	}
	if err := s.db.QueryRow(r.Context(), `SELECT u.email_notifications AND NOT EXISTS(SELECT 1 FROM users opt WHERE lower(opt.email)=lower(u.email) AND NOT opt.email_notifications) FROM users u WHERE u.id=$1`, p.UserID).Scan(&enabled); err != nil {
		return err
	}
	write(w, 200, map[string]bool{"enabled": enabled, "available": s.email != nil})
	return nil
}

func (s *Server) subscription(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	if !p.Browser() {
		return denied
	}
	v, err := s.loadRequest(r)
	if err != nil {
		return err
	}
	if r.Method != http.MethodGet {
		if _, err = s.db.Exec(r.Context(), `INSERT INTO request_subscriptions(request_id,user_id,subscribed) VALUES($1,$2,$3) ON CONFLICT(request_id,user_id) DO UPDATE SET subscribed=excluded.subscribed`, v.ID, p.UserID, r.Method == http.MethodPut); err != nil {
			return err
		}
	}
	var subscribed bool
	if err = s.db.QueryRow(r.Context(), `SELECT COALESCE((SELECT subscribed FROM request_subscriptions WHERE request_id=$1 AND user_id=$2),$3 OR EXISTS(SELECT 1 FROM votes WHERE request_id=$1 AND user_id=$2))`, v.ID, p.UserID, v.AuthorID == p.UserID).Scan(&subscribed); err != nil {
		return err
	}
	write(w, 200, map[string]bool{"subscribed": subscribed})
	return nil
}

func (s *Server) emailUnsubscribe(w http.ResponseWriter, r *http.Request) error {
	tok := r.PathValue("token")
	if len(tok) != 64 || strings.Trim(tok, "0123456789abcdef") != "" {
		return missing
	}
	var exists bool
	if err := s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE email_unsubscribe_token=$1)`, tok).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return missing
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	if r.Method == http.MethodPost {
		if _, err := s.db.Exec(r.Context(), `UPDATE users SET email_notifications=false WHERE lower(email)=(SELECT lower(email) FROM users WHERE email_unsubscribe_token=$1)`, tok); err != nil {
			return err
		}
		_, _ = io.WriteString(w, "<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><title>Email preferences</title><h1>Unsubscribed</h1><p>You will no longer receive Feedback email notifications. You can enable them again in your account.</p></html>")
		return nil
	}
	_, _ = io.WriteString(w, "<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>Email preferences</title><h1>Unsubscribe from Feedback emails</h1><form method=\"post\"><button type=\"submit\">Unsubscribe</button></form></html>")
	return nil
}

type resendMessage struct {
	From    string            `json:"from"`
	To      []string          `json:"to"`
	Subject string            `json:"subject"`
	Text    string            `json:"text"`
	Headers map[string]string `json:"headers"`
}

// Recheck membership at dispatch so queued mail cannot bypass revocation.
const emailEligibility = `u.email_notifications AND u.email<>'' AND NOT EXISTS(SELECT 1 FROM users opt WHERE lower(opt.email)=lower(u.email) AND NOT opt.email_notifications) AND (
 ($2='request_created' AND lower(u.email)=ANY($3::text[])) OR
 ($2<>'request_created' AND ((u.id=r.author_id AND r.source LIKE 'app:%' AND EXISTS(SELECT 1 FROM identities i WHERE i.user_id=u.id AND i.issuer=r.source)) OR lower(u.email)=ANY($3::text[]) OR EXISTS(SELECT 1 FROM memberships m WHERE m.project_id=r.project_id AND m.email=lower(u.email)))
 AND COALESCE((SELECT subscribed FROM request_subscriptions sub WHERE sub.request_id=r.id AND sub.user_id=u.id),u.id=r.author_id OR EXISTS(SELECT 1 FROM votes v WHERE v.request_id=r.id AND v.user_id=u.id)))
)`

func (s *Server) enqueueEmail(ctx context.Context, tx pgx.Tx, requestID, event string) error {
	if s.email == nil {
		return nil
	}
	if event != "request_created" && event != "status_changed" && event != "comment_created" {
		return errors.New("unknown email event")
	}
	ctx, span := otel.Tracer("feedback/email").Start(ctx, "email.enqueue")
	defer span.End()
	if event == "request_created" {
		for _, address := range s.email.admins {
			// Create consent records for configured admins before their first login.
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "email-admin:"+address); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO users(email,name) SELECT $1,'Administrator' WHERE NOT EXISTS(SELECT 1 FROM users WHERE lower(email)=$1)`, address); err != nil {
				return err
			}
		}
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON(lower(u.email)) u.id::text,lower(u.email),u.email_unsubscribe_token,r.title,r.status,r.project_id::text,(lower(u.email)=ANY($3::text[]) OR EXISTS(SELECT 1 FROM memberships m WHERE m.project_id=r.project_id AND m.email=lower(u.email))) FROM users u CROSS JOIN requests r WHERE r.id=$1 AND `+emailEligibility+` ORDER BY lower(u.email),u.id`, requestID, event, s.email.admins)
	if err != nil {
		return err
	}
	type recipient struct {
		id, email, token, title, status, project string
		portal                                   bool
	}
	recipients, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (recipient, error) {
		var v recipient
		err := row.Scan(&v.id, &v.email, &v.token, &v.title, &v.status, &v.project, &v.portal)
		return v, err
	})
	if err != nil {
		return err
	}
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	for _, user := range recipients {
		if _, err = email(user.email); err != nil {
			continue
		}
		unsubscribe := s.email.baseURL + "/api/v1/email/unsubscribe/" + user.token
		query := url.Values{"project": {user.project}, "view": {"requests"}, "request": {requestID}}
		view := "Check this request in the application where you submitted it."
		if user.portal {
			view = "View request: " + s.email.baseURL + "/#" + query.Encode()
		}
		message := "A new request was submitted."
		switch event {
		case "status_changed":
			message = "The request status changed to " + user.status + "."
		case "comment_created":
			message = "A new public comment was added to the request."
		}
		payload, err := json.Marshal(resendMessage{From: s.email.from, To: []string{user.email}, Subject: "Feedback: " + strings.ReplaceAll(event, "_", " "), Text: message + "\n\n" + user.title + "\n\n" + view + "\n\nUnsubscribe from all Feedback emails: " + unsubscribe + "\n", Headers: map[string]string{"List-Unsubscribe": "<" + unsubscribe + ">", "List-Unsubscribe-Post": "List-Unsubscribe=One-Click"}})
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO email_outbox(request_id,user_id,recipient,event,payload,traceparent) VALUES($1,$2,$3,$4,$5,$6)`, requestID, user.id, user.email, event, payload, carrier.Get("traceparent")); err != nil {
			return err
		}
	}
	return nil
}

type emailDelivery struct {
	ID, UserID, RequestID, Recipient, Event, TraceParent string
	Payload                                              []byte
	Attempt                                              int
}

func (s *Server) claimEmail(ctx context.Context) (emailDelivery, error) {
	var d emailDelivery
	if s.email == nil {
		return d, pgx.ErrNoRows
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return d, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Stop before Resend's 24-hour deduplication expires to avoid duplicate mail.
	if _, err = tx.Exec(ctx, `UPDATE email_outbox SET completed_at=now(),last_error=CASE WHEN attempts>=8 THEN 'attempts_exhausted' ELSE 'idempotency_expired' END,lease_until=NULL WHERE completed_at IS NULL AND (lease_until IS NULL OR lease_until<now()) AND (attempts>=8 OR first_attempt_at<now()-interval '23 hours')`); err != nil {
		return d, err
	}
	var ready bool
	err = tx.QueryRow(ctx, `UPDATE email_dispatch_gate SET next_at=clock_timestamp()+interval '1 second' WHERE singleton AND next_at<=clock_timestamp() RETURNING true`).Scan(&ready)
	if errors.Is(err, pgx.ErrNoRows) {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return d, commitErr
		}
		return d, err
	}
	if err != nil {
		return d, err
	}
	err = tx.QueryRow(ctx, `WITH candidate AS (SELECT id FROM email_outbox WHERE completed_at IS NULL AND attempts<8 AND next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<now()) ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE email_outbox d SET attempts=attempts+1,first_attempt_at=COALESCE(first_attempt_at,now()),lease_until=now()+interval '30 seconds' FROM candidate c WHERE d.id=c.id RETURNING d.id::text,d.user_id::text,d.request_id::text,d.recipient,d.event,d.payload,d.attempts,d.traceparent`).Scan(&d.ID, &d.UserID, &d.RequestID, &d.Recipient, &d.Event, &d.Payload, &d.Attempt, &d.TraceParent)
	if errors.Is(err, pgx.ErrNoRows) {
		// Commit terminal cleanup even when there is no delivery to claim.
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return d, commitErr
		}
		return d, err
	}
	if err != nil {
		return d, err
	}
	return d, tx.Commit(ctx)
}

func (s *Server) emailAllowed(ctx context.Context, d emailDelivery) (bool, error) {
	var allowed bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u CROSS JOIN requests r WHERE r.id=$1 AND `+emailEligibility+` AND u.id=$4 AND lower(u.email)=$5)`, d.RequestID, d.Event, s.email.admins, d.UserID, d.Recipient).Scan(&allowed)
	return allowed, err
}

func resendClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("email redirects are forbidden") }}
}

func resendCooldown(h http.Header, now time.Time) time.Duration {
	var delay time.Duration
	if seconds, err := strconv.ParseInt(h.Get("Retry-After"), 10, 32); err == nil && seconds > 0 {
		delay = time.Duration(seconds) * time.Second
	} else if until, err := http.ParseTime(h.Get("Retry-After")); err == nil && until.After(now) {
		delay = until.Sub(now)
	}
	if h.Get("RateLimit-Remaining") == "0" {
		if seconds, err := strconv.ParseInt(h.Get("RateLimit-Reset"), 10, 32); err == nil && seconds > 0 {
			delay = max(delay, time.Duration(seconds)*time.Second)
		}
	}
	return delay
}

func (s *Server) deliverEmail(ctx context.Context, client *http.Client, d emailDelivery) (int, string, time.Duration) {
	allowed, err := s.emailAllowed(ctx, d)
	if err != nil {
		return 0, "eligibility_failed", 0
	}
	if !allowed {
		return 0, "suppressed", 0
	}
	ctx, span := otel.Tracer("feedback/email").Start(ctx, "resend.send", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", strings.NewReader(string(d.Payload)))
	if err != nil {
		return 0, "request_failed", 0
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.email.apiKey)
	req.Header.Set("Idempotency-Key", "feedback-email/"+d.ID)
	resp, err := client.Do(req)
	if err != nil {
		span.SetStatus(codes.Error, "transport_failed")
		return 0, "transport_failed", 0
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	delay := resendCooldown(resp.Header, time.Now())
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, "delivered", delay
	}
	span.SetStatus(codes.Error, "provider_rejected")
	if resp.StatusCode == 429 {
		return resp.StatusCode, "rate_limited", max(delay, time.Second)
	}
	if resp.StatusCode >= 500 || resp.StatusCode == 408 || resp.StatusCode == 409 {
		return resp.StatusCode, "provider_unavailable", delay
	}
	return resp.StatusCode, "provider_rejected", delay
}

func (s *Server) completeEmail(ctx context.Context, d emailDelivery, status int, outcome string, cooldown time.Duration) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	done := outcome == "delivered" || outcome == "suppressed" || outcome == "provider_rejected" || d.Attempt >= 8
	delay := max(time.Duration(15*(1<<min(d.Attempt-1, 8)))*time.Second, cooldown)
	tag, err := tx.Exec(ctx, `UPDATE email_outbox SET last_status=$3,last_error=$4,lease_until=NULL,next_attempt_at=now()+($5*interval '1 second'),completed_at=CASE WHEN $6 THEN now() ELSE NULL END WHERE id=$1 AND attempts=$2 AND completed_at IS NULL`, d.ID, d.Attempt, status, outcome, delay.Seconds(), done)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO email_attempts(delivery_id,attempt,status,outcome) VALUES($1,$2,$3,$4)`, d.ID, d.Attempt, status, outcome); err != nil {
		return err
	}
	if cooldown > 0 {
		if _, err = tx.Exec(ctx, `UPDATE email_dispatch_gate SET next_at=GREATEST(next_at,now()+($1*interval '1 second')) WHERE singleton`, cooldown.Seconds()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Server) RunEmail(ctx context.Context) {
	if s.email == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	client := resendClient()
	defer client.CloseIdleConnections()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d, err := s.claimEmail(ctx)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				s.log.ErrorContext(ctx, "email claim failed")
				continue
			}
			parent := propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": d.TraceParent})
			spanCtx, span := otel.Tracer("feedback/email").Start(parent, "email.delivery")
			status, outcome, cooldown := s.deliverEmail(spanCtx, client, d)
			span.SetAttributes(attribute.String("delivery.outcome", outcome), attribute.Int("http.response.status_code", status))
			if outcome != "delivered" && outcome != "suppressed" {
				span.SetStatus(codes.Error, "delivery_failed")
			}
			s.log.InfoContext(spanCtx, "email attempt completed", "outcome", outcome, "status", status)
			if err = s.completeEmail(spanCtx, d, status, outcome, cooldown); err != nil {
				s.log.ErrorContext(spanCtx, "email result persistence failed")
			}
			span.End()
		}
	}
}
