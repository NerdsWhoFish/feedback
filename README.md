# Nerds Who Fish Feedback

A self-hosted request portal that keeps client projects private and gives assistants a human-controlled approval gate.

Members submit, vote and discuss requests in projects they belong to. Applications submit for their own end users with scoped keys. Administrators decide what gets built. An assistant can pick up approved work and report progress, but cannot approve a new request. The intended production origin is `https://feedback.nerdswhofish.com`.

## Architecture

One Go binary serves the versioned REST API and an embedded React, TypeScript and Vite SPA. PostgreSQL stores identities, sessions, memberships, requests, comments, votes, status history, changelog entries and the durable webhook queue. Embedded, versioned SQL migrations run under an advisory lock on startup. There is no separate queue service or frontend runtime.

Authentication and project authorization are separate. A valid login with no memberships sees no projects. Administrators are exact email addresses in `ADMIN_EMAILS` and see every project. Browser users are identified by OIDC issuer and subject. App end users are identified by `app:<app_name>` and their external ID, with no automatic email linking to browser accounts. An app claiming the administrator's email gains no administrator privileges.

The API uses plain JSON and errors shaped as `{"error":{"code":"forbidden","message":"..."}}`. List endpoints accept `limit` (1 to 100, default 50) and `offset` (default 0), returning `items`, `limit` and `offset`. Project route parameters accept a UUID or slug. Request detail includes the first 100 comments and status-history entries plus total counts; use its `/comments` and `/history` collections to page further. Public request endpoints have the same pagination without author or actor metadata. The checked-in [OpenAPI contract](docs/openapi.yaml) describes endpoints and permissions. Read the proposed [architecture decisions](adr/README.md) before deployment.

## Run locally

Install Go 1.26 or newer, Node 22.12 or newer, pnpm 10.28.2 and Docker Compose. The Dockerfile uses Go 1.27 and Node 26. PostgreSQL 17 is used by Compose and CI.

```sh
docker compose up -d --wait postgres
cp .env.example .env
```

Edit `.env` with a dedicated development OIDC client, your administrator email and a persistent webhook encryption key. Generate that key from 32 random bytes with `openssl rand -base64 32`; store the result only in your local environment or secret store. Do not commit `.env`.

The binary reads its environment, not `.env` files. Export the completed configuration, then build and run:

```sh
set -a
. ./.env
set +a
make build
./bin/feedback
```

Open `http://localhost:8080`. The development client must allow `http://localhost:8080/auth/callback`. There is no anonymous account creation or development login bypass. For an unauthenticated shell of the portal, leave all three OIDC issuer/client/secret settings empty in local mode; login remains unavailable and protected APIs still reject anonymous calls.

For frontend development, keep the backend on port 8080 and run `pnpm --dir web dev`. Use the backend origin for login and ordinary end-to-end testing. Cookie writes enforce an exact `Origin` match to `BASE_URL`; a separately hosted Vite origin needs a same-origin development proxy setup, not relaxed CSRF checks.

Run against a disposable database for integration tests:

```sh
export TEST_DATABASE_URL='postgres://feedback:feedback-local@127.0.0.1:55432/feedback?sslmode=disable'
go test -race ./...
pnpm --dir web lint
pnpm --dir web typecheck
pnpm --dir web build
go vet ./...
docker build -t feedback:local .
```

Tests create isolated schemas in the supplied database. Never point the test configuration at production. Without `TEST_DATABASE_URL`, database integration tests are skipped; CI always supplies PostgreSQL. `make build` installs the locked frontend dependencies, builds the SPA and embeds it in `bin/feedback`.

## Environment

| Variable | Meaning / default |
| --- | --- |
| `ENVIRONMENT` | `local`, `test`, `staging` or `production`; defaults to `local`. |
| `HTTP_ADDR` | HTTP listener, default `:8080`. Bind to loopback for local use. |
| `BASE_URL` | Public origin without a path, default `http://localhost:8080`. HTTPS is required outside local loopback development. |
| `DATABASE_URL` | Required pgx PostgreSQL connection URL. Use TLS in production. |
| `OIDC_ISSUER` | `https://auth.theoutdoorprogrammer.com` for production. |
| `OIDC_CLIENT_ID` | Dedicated, stable Tinyauth client ID. |
| `OIDC_CLIENT_SECRET` | Confidential client secret, from the server's Secret. |
| `OIDC_REDIRECT_URL` | Defaults to `BASE_URL/auth/callback`; if set, must match exactly. |
| `OIDC_AUTH_URL` | Discovery fallback authorization endpoint. |
| `OIDC_TOKEN_URL` | Discovery fallback token endpoint. |
| `OIDC_USERINFO_URL` | Discovery fallback userinfo endpoint. |
| `OIDC_JWKS_URL` | Discovery fallback signing-key endpoint. All four fallback URLs are required together if discovery fails. |
| `ADMIN_EMAILS` | Comma-separated exact administrator emails, normalized to lowercase. Required outside local mode. |
| `SESSION_TTL` | Session lifetime, default `12h`, allowed range `1m` through `168h`. |
| `KEY_RATE_LIMIT_RPM` | Token bucket capacity and refill rate per key, per process, default `120`. |
| `WEBHOOK_ENCRYPTION_KEY` | Base64-encoded 32-byte key protecting webhook signing secrets at rest. Required outside local mode and to create webhooks. Back it up with PostgreSQL. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP HTTP/protobuf base endpoint. Production destination: `https://otlp-gateway-prod-us-east-3.grafana.net/otlp`. |
| `OTEL_EXPORTER_OTLP_HEADERS` | Server-only ingestion headers, for example `Authorization=Basic <credential>`. Never expose to the frontend. |
| `FARO_COLLECTOR_URL` | Server-only Grafana Faro collector endpoint for the filtered browser telemetry relay. |
| `TEST_DATABASE_URL` | Integration-test database only; unused by the application. |

Configure OIDC issuer, client ID and client secret together. Production and staging require OIDC, administrator emails, webhook encryption, OTLP and Faro endpoints. Arbitrary `OTEL_RESOURCE_ATTRIBUTES` and `OTEL_SERVICE_NAME` overrides are rejected to preserve the telemetry resource allowlist. Configure the approved Grafana destinations and verify ingestion before opening production access.

## Register the Tinyauth client

Use a dedicated Tinyauth v5.2 client named `feedback`. The [official OIDC guide](https://tinyauth.app/docs/guides/oidc/) documents client creation and registration. On the provider host, generate a client with `tinyauth oidc create feedback`, place its secret in the existing secret-management workflow, and configure:

```text
TINYAUTH_OIDC_CLIENTS_FEEDBACK_NAME=feedback
TINYAUTH_OIDC_CLIENTS_FEEDBACK_CLIENTID=<generated client ID>
TINYAUTH_OIDC_CLIENTS_FEEDBACK_CLIENTSECRETFILE=<mounted secret file>
TINYAUTH_OIDC_CLIENTS_FEEDBACK_TRUSTEDREDIRECTURIS=https://feedback.nerdswhofish.com/auth/callback
```

Set the corresponding application `OIDC_CLIENT_ID` and `OIDC_CLIENT_SECRET`. Feedback uses authorization code flow, PKCE S256, nonce and scopes `openid profile email`. It verifies the signed ID token and matching userinfo subject and requires a verified email. Discovery is at `/.well-known/openid-configuration`. Fallback paths are `/authorize`, `/api/oidc/token`, `/api/oidc/userinfo` and `/.well-known/jwks.json`.

Keep the client ID and provider usernames stable: Tinyauth derives subjects from them. Tinyauth v5.2 emits `email_verified` for nonempty email claims, so trust depends on the provider's upstream identity configuration, not that claim alone. The [v5.2 implementation](https://github.com/tinyauthapp/tinyauth/blob/v5.2.0/internal/service/oidc_service.go) defines those behaviors.

The existing provider catalog describes a Google domain allowlist that excludes ordinary Gmail accounts. Before inviting client Gmail addresses, deliberately update the provider allowlist in its own configuration. Membership in Feedback does not bypass Tinyauth's login policy. Keep administrator access limited to exact `ADMIN_EMAILS`; do not turn a provider domain into an automatic admin grant.

Feedback sets its own host-only, HttpOnly, Secure, SameSite=Lax cookie named `__Host-feedback`. Local HTTP uses `feedback_local`. Cookie-authenticated writes require both an exact public `Origin` and the `X-CSRF-Token` returned by `GET /api/v1/me`. Session tokens are stored as hashes; logout revokes the server session. `Remote-*` forward-auth headers are ignored.

No provider registration or live configuration change is included in this repository build.

## Admin and application workflow

1. Sign in as an address in `ADMIN_EMAILS`.
2. Create a project in the admin area, then add member email addresses.
3. Create an `app` key with a stable lowercase app name and one or more project scopes. Copy the displayed token immediately. Only its hash is stored; it cannot be retrieved later.
4. Keep the key on the application's server. Authenticate the end user there, then pass the stable external ID, email and name on submission. Never accept an arbitrary external ID from an unauthenticated client.
5. Revoke an old key in the admin area after issuing its replacement. Reuse the app name when rotating keys to preserve the identity namespace.

Use an app key to submit a request:

```sh
curl --fail-with-body https://feedback.nerdswhofish.com/api/v1/projects/my-app/requests \
  -H "Authorization: Bearer $FEEDBACK_APP_KEY" \
  -H 'Content-Type: application/json' \
  --data '{"title":"Remember my filters","description":"Keep the selected filters between visits.","end_user":{"external_id":"customer-123","email":"client@example.com","name":"Client"}}'
```

List that user's requests, or retrieve one request including its public comments and status history:

```sh
curl --fail-with-body --get \
  https://feedback.nerdswhofish.com/api/v1/projects/my-app/requests \
  -H "Authorization: Bearer $FEEDBACK_APP_KEY" \
  --data-urlencode 'external_id=customer-123' \
  --data-urlencode 'limit=50'

curl --fail-with-body --get \
  "https://feedback.nerdswhofish.com/api/v1/requests/$REQUEST_ID" \
  -H "Authorization: Bearer $FEEDBACK_APP_KEY" \
  --data-urlencode 'external_id=customer-123'
```

The server checks key scope, app namespace and end-user ownership. A matching email in another app does not grant access. Bearer calls do not need a CSRF token and are rate-limited; a `429` includes `Retry-After`.

## Assistant workflow

Create a separate `assistant` key scoped to the projects it should operate. It may read scoped requests, post public or internal comments, and add changelog entries linked to shipped requests. It cannot submit new requests, manage projects, memberships, keys or webhooks, or make approval and decline decisions.

| Current status | Assistant may set |
| --- | --- |
| `approved_for_build` | `in_progress` |
| `in_progress` | `shipped`, `approved_for_build` |
| Any other status | Nothing |

Only a browser admin can grant `approved_for_build` to previously unapproved work. Returning an already authorized `in_progress` request to the queue is allowed. Status changes and their actor are recorded transactionally.

```sh
curl --fail-with-body --get \
  https://feedback.nerdswhofish.com/api/v1/projects/my-app/requests \
  -H "Authorization: Bearer $FEEDBACK_ASSISTANT_KEY" \
  --data-urlencode 'status=approved_for_build'

curl --fail-with-body \
  "https://feedback.nerdswhofish.com/api/v1/requests/$REQUEST_ID/status" \
  -H "Authorization: Bearer $FEEDBACK_ASSISTANT_KEY" \
  -H 'Content-Type: application/json' \
  --data '{"status":"in_progress"}'
```

## Public status links and webhooks

Projects default to private status pages. Enabling `public_status_page` permits unguessable `/s/<token>` links for individual requests. The public response contains the title, status, public comment text and status history with timestamps. It excludes author data, actor names, request description, source, internal comments and project identifiers. Authors can still put personal information in a title or public comment, so review content before sharing a link. Turning the project setting off disables existing public links.

Administrators can register HTTPS webhooks for `request_created`, `status_changed` and `comment_created`. The signing secret is displayed once and encrypted in the database. Events and delivery jobs are committed with the domain change, with up to 8 attempts and exponential backoff starting at 15 seconds. The delivery log records each attempt's bounded outcome and HTTP status.

Each POST has `X-Feedback-Delivery` (a stable UUID across retries), `X-Feedback-Timestamp` (Unix seconds for this attempt), and `X-Feedback-Signature` (`sha256=` followed by lowercase hex). Compute HMAC-SHA256 using the displayed secret's literal text as key over `timestamp + "." + raw_body`, compare in constant time, reject timestamps outside a short tolerance, and deduplicate the delivery UUID. Do not parse and re-serialize JSON before signature verification. Any 2xx response acknowledges delivery. See [OpenAPI](docs/openapi.yaml) for the payload schema.

Only public network destinations are allowed, DNS is checked when connecting, redirects are refused and response bodies are not logged. Internal comments do not become public notifications. Keep the encryption key stable; key rotation needs a controlled re-encryption procedure and is not an automatic v1 feature.

The transactional event seam can support email later. Email delivery, an MCP endpoint, automatic draft changelog entries and Prometheus metrics are outside this version.

## Operations and delivery

`GET /healthz` checks process health; `GET /readyz` checks database readiness. Structured logs and distributed traces use bounded operation names and trace/span correlation. The frontend reuses the shared privacy-filtered Faro SDK and sends telemetry to a server relay. Do not send tokens, emails, names, request contents, raw URLs or query strings into telemetry.

The [deployment example](deploy/README.md) covers secret references, probes, security context, backup requirements and rollout checks. HTTPS ingress and Flux changes are separate work in the cluster repository. No GitHub repository, image publication or deployment has been performed by this local build.

CI runs frontend lint/typecheck/build, production dependency auditing, Go vet/race tests with PostgreSQL, and govulncheck. After a future authorized push to `main` or a `v*` tag, it builds and pushes `linux/amd64` and `linux/arm64` images to `ghcr.io/nerdswhofish/feedback` with provenance and SBOMs. Pull requests run checks without publishing. Configure required checks and human review before release. Action references are pinned to commits; review dependency and base-image updates regularly.

Before production, review both proposed ADRs, register the OIDC client, provision secrets and Grafana Faro, verify Tinyauth's client allowlist, and exercise real OIDC, webhook and telemetry delivery in the target environment. Local tests do not establish that external configuration is correct.

## License

[Apache License 2.0](LICENSE) is the proposed license for this local v1. Confirm the licensing decision before publishing the repository. Third-party dependencies retain their own licenses.
