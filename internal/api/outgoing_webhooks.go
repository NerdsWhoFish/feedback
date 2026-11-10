package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/NerdsWhoFish/feedback/internal/auth"
	"github.com/NerdsWhoFish/feedback/internal/config"
	"github.com/NerdsWhoFish/feedback/internal/webhook"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
)

type outgoingConfig struct {
	sender  *webhook.Sender
	baseURL string
}

func (s *Server) ConfigureOutgoingWebhook(destination, secret, baseURL string) error {
	return s.ConfigureOutgoingWebhookWithHeaders(destination, secret, baseURL, nil)
}

func (s *Server) ConfigureOutgoingWebhookWithHeaders(destination, secret, baseURL string, headers map[string]string) error {
	sender, err := webhook.NewWithHeaders(destination, secret, headers)
	if err != nil {
		return err
	}
	if sender == nil {
		s.outgoing = nil
		return nil
	}
	u, err := url.Parse(baseURL)
	if err != nil || !config.ValidEndpoint(baseURL, true) || u.ForceQuery || strings.Contains(baseURL, "#") || (u.Path != "" && u.Path != "/") {
		return errors.New("outgoing webhooks require a valid BASE_URL origin")
	}
	s.outgoing = &outgoingConfig{sender: sender, baseURL: strings.TrimRight(baseURL, "/")}
	return nil
}

func (s *Server) enqueueOutgoing(ctx context.Context, tx pgx.Tx, requestID, event string, principal auth.Principal, comment *webhook.Comment, change *webhook.Change) (err error) {
	if s.outgoing == nil {
		return nil
	}
	ctx, span := otel.Tracer("feedback/outgoing-webhooks").Start(ctx, "outgoing_webhook.enqueue")
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, "enqueue_failed")
		}
		span.End()
	}()
	timestamp := time.Now().UTC().Truncate(time.Second)
	p := webhook.Payload{Event: event, Timestamp: timestamp.Format(time.RFC3339), Comment: comment, Change: change}
	err = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text,p.id::text,p.name,r.id::text,r.title,r.description,r.status,u.email,u.name,r.source,
 COALESCE((SELECT i.subject FROM identities i WHERE i.user_id=r.author_id AND i.issuer=r.source ORDER BY i.subject LIMIT 1),'')
 FROM requests r JOIN projects p ON p.id=r.project_id JOIN users u ON u.id=r.author_id WHERE r.id=$1`, requestID).Scan(
		&p.DeliveryID, &p.Project.ID, &p.Project.Name, &p.Request.ID, &p.Request.Title, &p.Request.Body, &p.Request.Status,
		&p.Submitter.Email, &p.Submitter.Name, &p.Attribution.Source, &p.Attribution.ExternalID)
	if err != nil {
		return err
	}
	if strings.HasPrefix(p.Attribution.Source, "app:") {
		p.Attribution.AppName = strings.TrimPrefix(p.Attribution.Source, "app:")
	}
	query := url.Values{"project": {p.Project.ID}, "view": {"requests"}, "request": {requestID}}
	p.Request.URL = s.outgoing.baseURL + "/#" + query.Encode()
	p.Actor = webhook.Actor{Type: "user", Name: principal.Name, Email: principal.Email}
	if principal.KeyID != "" {
		p.Actor = webhook.Actor{Type: principal.KeyType, AppName: principal.AppName}
		if err = tx.QueryRow(ctx, `SELECT name FROM api_keys WHERE id=$1`, principal.KeyID).Scan(&p.Actor.Name); err != nil {
			return err
		}
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	_, err = tx.Exec(ctx, `INSERT INTO outgoing_webhook_deliveries(id,request_id,target,event,payload,traceparent,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`,
		p.DeliveryID, requestID, s.outgoing.sender.Target(), event, body, carrier.Get("traceparent"), timestamp)
	return err
}

type outgoingQueue struct{ db *pgxpool.Pool }

func (q *outgoingQueue) Claim(ctx context.Context, target string) (webhook.Delivery, error) {
	var d webhook.Delivery
	// Bounded cleanup includes old configurations so customer snapshots expire.
	if _, err := q.db.Exec(ctx, `DELETE FROM outgoing_webhook_deliveries WHERE id IN (SELECT id FROM outgoing_webhook_deliveries WHERE created_at<now()-interval '7 days' ORDER BY created_at LIMIT 100)`); err != nil {
		return d, err
	}
	if _, err := q.db.Exec(ctx, `UPDATE outgoing_webhook_deliveries SET completed_at=now(),lease_until=NULL,last_error=CASE WHEN attempts>=5 THEN 'lease_expired' ELSE 'expired' END
 WHERE id IN (SELECT id FROM outgoing_webhook_deliveries WHERE completed_at IS NULL AND (lease_until IS NULL OR lease_until<now()) AND (attempts>=5 OR created_at<now()-interval '24 hours') ORDER BY created_at LIMIT 100)`); err != nil {
		return d, err
	}
	var created time.Time
	err := q.db.QueryRow(ctx, `WITH candidate AS (
 SELECT id FROM outgoing_webhook_deliveries WHERE target=$1 AND completed_at IS NULL AND attempts<5
 AND created_at>=now()-interval '24 hours' AND next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<now())
 ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE outgoing_webhook_deliveries d SET attempts=attempts+1,lease_until=now()+interval '30 seconds'
 FROM candidate c WHERE d.id=c.id RETURNING d.id::text,d.event,d.payload,d.attempts,d.traceparent,d.created_at`, target).Scan(&d.ID, &d.Event, &d.Body, &d.Attempt, &d.TraceParent, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, webhook.ErrEmpty
	}
	d.Timestamp = created.UTC().Format(time.RFC3339)
	return d, err
}

func (q *outgoingQueue) Complete(ctx context.Context, d webhook.Delivery, result webhook.Result) error {
	_, err := q.db.Exec(ctx, `UPDATE outgoing_webhook_deliveries SET last_status=$3,last_error=$4,lease_until=NULL,
 next_attempt_at=now()+($5*interval '1 second'),completed_at=CASE WHEN $6 THEN now() ELSE NULL END
 WHERE id=$1 AND attempts=$2 AND completed_at IS NULL`, d.ID, d.Attempt, result.Status, result.Outcome, result.Delay.Seconds(), result.Terminal)
	return err
}

func (s *Server) RunOutgoingWebhooks(ctx context.Context) {
	if s.outgoing != nil {
		s.outgoing.sender.Run(ctx, &outgoingQueue{db: s.db}, s.log, s.metrics)
	}
}
