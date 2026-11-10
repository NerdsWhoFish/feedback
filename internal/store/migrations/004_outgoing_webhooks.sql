CREATE TABLE outgoing_webhook_deliveries (
 id uuid PRIMARY KEY,
 request_id uuid NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
 target text NOT NULL,
 event text NOT NULL CHECK(event IN ('request.created','comment.created','request.status_changed')),
 payload bytea NOT NULL,
 traceparent text NOT NULL DEFAULT '',
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 5),
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease_until timestamptz,
 completed_at timestamptz,
 last_status integer NOT NULL DEFAULT 0,
 last_error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL
);
CREATE INDEX outgoing_webhook_pending ON outgoing_webhook_deliveries(target,next_attempt_at) WHERE completed_at IS NULL;
CREATE INDEX outgoing_webhook_retention ON outgoing_webhook_deliveries(created_at);
