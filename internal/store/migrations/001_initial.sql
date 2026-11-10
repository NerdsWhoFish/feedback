CREATE TABLE users (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 email text NOT NULL,
 name text NOT NULL,
 role text NOT NULL DEFAULT 'member' CHECK(role IN ('admin','member')),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE identities (
 issuer text NOT NULL,
 subject text NOT NULL,
 user_id uuid NOT NULL REFERENCES users(id),
 PRIMARY KEY(issuer,subject)
);
CREATE TABLE sessions (
 token_hash text PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 csrf_token text NOT NULL,
 expires_at timestamptz NOT NULL
);
CREATE INDEX sessions_expiry ON sessions(expires_at);
CREATE TABLE oidc_states (
 token_hash text PRIMARY KEY,
 nonce text NOT NULL,
 verifier text NOT NULL,
 expires_at timestamptz NOT NULL
);
CREATE TABLE projects (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 slug text NOT NULL UNIQUE,
 name text NOT NULL,
 description text NOT NULL DEFAULT '',
 public_status_page boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE memberships (
 project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 email text NOT NULL CHECK(email=lower(email)),
 PRIMARY KEY(project_id,email)
);
CREATE TABLE api_keys (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 name text NOT NULL,
 key_type text NOT NULL CHECK(key_type IN ('app','assistant')),
 app_name text NOT NULL DEFAULT '',
 key_hash text NOT NULL UNIQUE,
 revoked_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK((key_type='app' AND app_name<>'') OR (key_type='assistant' AND app_name=''))
);
CREATE TABLE key_projects (
 key_id uuid NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
 project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 PRIMARY KEY(key_id,project_id)
);
CREATE TABLE requests (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 project_id uuid NOT NULL REFERENCES projects(id),
 title text NOT NULL CHECK(length(title) BETWEEN 1 AND 200),
 description text NOT NULL CHECK(length(description)<=20000),
 status text NOT NULL DEFAULT 'open' CHECK(status IN ('open','under_review','planned','approved_for_build','in_progress','shipped','declined')),
 build_authorized boolean NOT NULL DEFAULT false,
 author_id uuid NOT NULL REFERENCES users(id),
 source text NOT NULL,
 public_token text NOT NULL UNIQUE,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX requests_project_created ON requests(project_id,created_at DESC,id);
CREATE INDEX requests_project_status ON requests(project_id,status,created_at DESC);
CREATE INDEX requests_author ON requests(author_id,project_id);
CREATE TABLE comments (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 request_id uuid NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
 author_id uuid REFERENCES users(id),
 key_id uuid REFERENCES api_keys(id),
 body text NOT NULL CHECK(length(body) BETWEEN 1 AND 10000),
 internal boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK((author_id IS NULL) <> (key_id IS NULL))
);
CREATE INDEX comments_request ON comments(request_id,created_at);
CREATE TABLE votes (
 request_id uuid NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
 user_id uuid NOT NULL REFERENCES users(id),
 PRIMARY KEY(request_id,user_id)
);
CREATE TABLE status_history (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 request_id uuid NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
 from_status text NOT NULL,
 to_status text NOT NULL,
 actor_id uuid REFERENCES users(id),
 key_id uuid REFERENCES api_keys(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK((actor_id IS NULL) <> (key_id IS NULL))
);
CREATE INDEX status_history_request ON status_history(request_id,created_at);
CREATE TABLE changelog (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 project_id uuid NOT NULL REFERENCES projects(id),
 title text NOT NULL,
 body text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE changelog_requests (
 changelog_id uuid NOT NULL REFERENCES changelog(id) ON DELETE CASCADE,
 request_id uuid NOT NULL REFERENCES requests(id),
 PRIMARY KEY(changelog_id,request_id)
);
CREATE TABLE webhooks (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 project_id uuid NOT NULL REFERENCES projects(id),
 url text NOT NULL,
 events text[] NOT NULL,
 encrypted_secret bytea NOT NULL,
 disabled_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE webhook_deliveries (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 webhook_id uuid NOT NULL REFERENCES webhooks(id),
 event text NOT NULL,
 payload jsonb NOT NULL,
 traceparent text NOT NULL DEFAULT '',
 attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease_until timestamptz,
 completed_at timestamptz,
 last_status integer NOT NULL DEFAULT 0,
 last_error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_pending ON webhook_deliveries(next_attempt_at) WHERE completed_at IS NULL;
CREATE TABLE webhook_attempts (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 delivery_id uuid NOT NULL REFERENCES webhook_deliveries(id),
 attempt integer NOT NULL,
 status integer NOT NULL,
 outcome text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
