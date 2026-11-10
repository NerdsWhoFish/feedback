ALTER TABLE users ADD COLUMN email_notifications boolean NOT NULL DEFAULT true;
ALTER TABLE users ADD COLUMN email_unsubscribe_token text NOT NULL UNIQUE DEFAULT replace(gen_random_uuid()::text || gen_random_uuid()::text, '-', '');
CREATE INDEX users_email_lower ON users(lower(email));

CREATE TABLE request_subscriptions (
 request_id uuid NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 subscribed boolean NOT NULL,
 PRIMARY KEY(request_id,user_id)
);

CREATE TABLE email_outbox (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 request_id uuid NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 recipient text NOT NULL,
 event text NOT NULL CHECK(event IN ('request_created','status_changed','comment_created')),
 payload jsonb NOT NULL,
 traceparent text NOT NULL DEFAULT '',
 attempts integer NOT NULL DEFAULT 0,
 first_attempt_at timestamptz,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease_until timestamptz,
 completed_at timestamptz,
 last_status integer NOT NULL DEFAULT 0,
 last_error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX email_pending ON email_outbox(next_attempt_at) WHERE completed_at IS NULL;
CREATE TABLE email_attempts (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 delivery_id uuid NOT NULL REFERENCES email_outbox(id) ON DELETE CASCADE,
 attempt integer NOT NULL,
 status integer NOT NULL,
 outcome text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);

-- Coordinate the provider's team-wide rate limit across application replicas.
CREATE TABLE email_dispatch_gate (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 next_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO email_dispatch_gate(singleton) VALUES(true);
