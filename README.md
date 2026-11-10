# Nerds Who Fish Feedback

A self-hosted request portal that keeps client projects private and gives assistants a human-controlled approval gate.

Members submit, vote and discuss requests in projects they belong to. Applications submit for their own end users with scoped keys. Administrators decide what gets built. An assistant can pick up approved work and report progress, but cannot approve a new request. The intended production origin is `https://feedback.nerdswhofish.com`.

## Architecture

One Go binary serves the versioned REST API, an assistant MCP endpoint and an embedded React, TypeScript and Vite SPA. PostgreSQL stores identities, sessions, memberships, requests, comments, votes, subscriptions, status history, changelog drafts and durable webhook and email queues. Embedded, versioned SQL migrations run under an advisory lock on startup. There is no separate queue service or frontend runtime.

Authentication and project authorization are separate. A valid login with no memberships sees no projects. Administrators are exact email addresses in `ADMIN_EMAILS` and see every project. Browser users are identified by OIDC issuer and subject. App end users are identified by `app:<app_name>` and their external ID, with no automatic email linking to browser accounts. An app claiming the administrator's email gains no administrator privileges.

The API uses plain JSON and errors shaped as `{"error":{"code":"forbidden","message":"..."}}`. List endpoints accept `limit` (1 to 100, default 50) and `offset` (default 0), returning `items`, `limit` and `offset`. Project route parameters accept a UUID or slug. Request detail includes the first 100 comments and status-history entries plus total counts; use its `/comments` and `/history` collections to page further. Public request endpoints have the same pagination without author or actor metadata. The checked-in [OpenAPI contract](docs/openapi.yaml) describes endpoints and permissions. Read the proposed [architecture decisions](adr/README.md) before deployment.

## Run locally

Install Go 1.26 or newer, Node 26, pnpm 10.28.2 and Docker Compose. PostgreSQL 17 is used by Compose and CI. Release builds also need GoReleaser 2.19.0. The Dockerfile packages a prebuilt binary; all Go and frontend compilation runs on the host or CI runner.

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
```

Tests create isolated schemas in the supplied database. Never point the test configuration at production. Without `TEST_DATABASE_URL`, database integration tests are skipped; CI always supplies PostgreSQL. `make build` installs the locked frontend dependencies, builds the SPA and embeds it in `bin/feedback`.

## Environment

| Variable | Meaning / default |
| --- | --- |
| `ENVIRONMENT` | `local`, `test`, `staging` or `production`; defaults to `local`. |
| `HTTP_ADDR` | HTTP listener, default `:8080`. Bind to loopback for local use. |
| `METRICS_ADDR` | Optional separate Prometheus listener, for example `127.0.0.1:9090`. Unset disables it. Never route it through public ingress. |
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
| `RESEND_API_KEY` | Server-only Resend API key. Unset disables email enqueue and delivery cleanly. |
| `EMAIL_FROM` | Required when Resend is enabled, for example `feedback@nerdswhofish.com`. Verify the sending domain in Resend first. |
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

Check the provider's login policy before inviting members. Membership in Feedback does not bypass Tinyauth's allowlist. Keep administrator access limited to exact `ADMIN_EMAILS`; do not turn a provider domain into an automatic admin grant.

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

Create a separate `assistant` key scoped to the projects it should operate. It may search scoped requests, post public or internal comments, and draft changelog entries linked to shipped requests. It cannot publish changelog entries, submit new requests, manage projects, memberships, keys or webhooks, or make approval and decline decisions.

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

### Connect an MCP client

The Streamable HTTP endpoint is `https://feedback.nerdswhofish.com/mcp`. Configure your client's remote HTTP transport with that URL and an `Authorization: Bearer <assistant-key>` header, loaded from its secret storage. Browser sessions and app keys are rejected. No OAuth discovery or stdio bridge is required; use a client that supports custom bearer headers. Keep keys out of checked-in client configuration.

For example, with the official [Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk), connect using `mcp.StreamableClientTransport{Endpoint: baseURL + "/mcp", HTTPClient: client}` where `client` injects the bearer header on every request. Then initialize the session, list tools, and call `list_requests` with `{"project":"my-app","status":"approved_for_build","q":"filters","limit":50}`. The SDK manages the protocol version and Accept headers. Clients using raw HTTP must send `Accept: application/json, text/event-stream`, `Content-Type: application/json`, and the negotiated `MCP-Protocol-Version` after initialization.

Available tools: `list_requests`, `get_request`, `list_comments`, `list_history`, `add_comment`, `move_status`, and `add_changelog`. `project` accepts a UUID or slug; `id` is a request UUID. List/search supports project, status, literal case-insensitive `q`, limit and offset. Detail includes the first 100 comments/history entries and totals; use the activity tools for remaining pages. Discover full argument schemas with `tools/list`.

The server uses the official SDK v1.8.0, stateless sessions and JSON responses. Every request revalidates the key, including revocation and rate limits. Tools invoke the REST handlers in process, so the approval rules above and project isolation apply identically. There are no administration tools. GET streams and DELETE session management return 405 because there is no persistent session.

### Changelog drafts

Moving a request to `shipped` creates one linked draft in the same database transaction. Re-shipping preserves that draft or its published version, including administrator edits. Existing changelog entries remain published when the migration runs.

Admins review drafts in Changelog, edit their title/body, and publish when ready. Members only see published entries. Assistants can read and create drafts but cannot publish or edit existing entries. REST creation defaults to a draft; browser admins can supply `published: true`. Admins use `PATCH /api/v1/projects/{project}/changelog/{id}` with `title`, `body` and/or `published` to edit, publish or unpublish. Linked request IDs remain immutable.

## Public status links and webhooks

Projects default to private status pages. Enabling `public_status_page` permits unguessable `/s/<token>` links for individual requests. The public response contains the title, status, public comment text and status history with timestamps. It excludes author data, actor names, request description, source, internal comments and project identifiers. Authors can still put personal information in a title or public comment, so review content before sharing a link. Turning the project setting off disables existing public links.

Administrators can register HTTPS webhooks for `request_created`, `status_changed` and `comment_created`. The signing secret is displayed once and encrypted in the database. Events and delivery jobs are committed with the domain change, with up to 8 attempts and exponential backoff starting at 15 seconds. The delivery log records each attempt's bounded outcome and HTTP status.

Each POST has `X-Feedback-Delivery` (a stable UUID across retries), `X-Feedback-Timestamp` (Unix seconds for this attempt), and `X-Feedback-Signature` (`sha256=` followed by lowercase hex). Compute HMAC-SHA256 using the displayed secret's literal text as key over `timestamp + "." + raw_body`, compare in constant time, reject timestamps outside a short tolerance, and deduplicate the delivery UUID. Do not parse and re-serialize JSON before signature verification. Any 2xx response acknowledges delivery. See [OpenAPI](docs/openapi.yaml) for the payload schema.

Only public network destinations are allowed, DNS is checked when connecting, redirects are refused and response bodies are not logged. Internal comments do not become public notifications. Keep the encryption key stable; key rotation needs a controlled re-encryption procedure and is not an automatic v1 feature.

## Email notifications through Resend

Verify `nerdswhofish.com` in your Resend account, configure its required DNS records, and set `RESEND_API_KEY` plus `EMAIL_FROM=feedback@nerdswhofish.com` in the server's secret environment. The sender must belong to a verified domain. Delivery uses Resend's [send email API](https://resend.com/docs/api-reference/emails/send-email), with no SMTP configuration. `BASE_URL` supplies notification and unsubscribe links.

New requests notify the configured `ADMIN_EMAILS`, including administrators who have not signed in yet. Status changes and new public comments notify request authors, voters and explicit followers, deduplicated by email address. App-submitted authors with an email are included without gaining browser membership or administrator privileges. Assistant keys never become recipients. Internal comments never enqueue email. Browser recipients must retain project access at delivery time; app authors retain access only to their own app request. Emails contain the request title and event, not comment text or internal details. App-only recipients are directed back to the submitting application instead of receiving an inaccessible portal link.

Users can follow/unfollow each request or turn all emails off in Email preferences. Authors and voters follow by default; explicit unfollow overrides that default. Every email includes a per-user unsubscribe link and one-click unsubscribe headers. Opening the link displays a confirmation form; only POST changes preferences, so email link scanners do not unsubscribe users. Opt-out applies across identities sharing that email and is checked again before queued delivery.

Email jobs commit with the request change. The worker uses leases, immutable payloads and stable `Idempotency-Key` values for retries. Database-wide pacing limits this deployment to one request per second across replicas; it honors `Retry-After` and exhausted rate-limit reset headers from Resend. If other applications share your Resend account quota, their traffic may still trigger throttling. Jobs retry transient transport errors, 408, 409, 429 and 5xx responses up to eight attempts with exponential backoff starting at 15 seconds. Permanent provider rejections stop immediately. Retries stop after 23 hours from the first attempt, before Resend's [24-hour idempotency window](https://resend.com/docs/dashboard/emails/idempotency-keys) expires. Provider acceptance is recorded as delivered; inbox placement and bounces remain visible in Resend.

Without `RESEND_API_KEY`, no new email jobs are enqueued and the worker stays off. Existing queued jobs remain for a later enablement, subject to consent, access and retry limits. Notification preferences remain available. Inspect `email_outbox` and `email_attempts` for pending/terminal delivery outcomes; monitor structured `email claim failed`, `email result persistence failed` and delivery outcome logs. Outbox payloads contain recipient data, so restrict database access, include the queue in backups, and apply your retention policy. Do not reset expired jobs for blind replay: investigate delivery in Resend before issuing a new notification.

## Prometheus metrics

Metrics are disabled by default and never registered on the public `HTTP_ADDR` listener. Set `METRICS_ADDR=127.0.0.1:9090` for a colocated scraper, then scrape `http://127.0.0.1:9090/metrics`. For Kubernetes, bind a separate port on the pod's private network, restrict it with a NetworkPolicy, and keep it out of the public Service/Ingress. The listener has no application authentication and must remain private.

Metrics include `feedback_http_requests_total{method,route,status}`, `feedback_http_request_duration_seconds{method,route}`, `feedback_webhook_delivery_attempts_total{outcome}`, and `feedback_requests{status}`. Labels use route templates and bounded enums, never raw paths, project IDs, emails or content. Request counts reflect committed PostgreSQL state on each scrape. Alert on `feedback_request_counts_collection_success == 0`, elevated HTTP errors and failed webhook outcomes; failed database collection does not silently report zero requests. Scrapes bound database collection to two seconds and allow at most two concurrent scrapes.

## Operations and delivery

`GET /healthz` checks process health; `GET /readyz` checks database readiness. Structured logs and distributed traces use bounded operation names and trace/span correlation. The frontend reuses the shared privacy-filtered Faro SDK and sends telemetry to a server relay. Do not send tokens, emails, names, request contents, raw URLs or query strings into telemetry.

The [deployment example](deploy/README.md) covers secret references, probes, security context, backup requirements and rollout checks. HTTPS ingress and Flux changes are separate work in the cluster repository.

CI runs frontend lint/typecheck/build, production dependency auditing, Go vet/race tests with PostgreSQL, and govulncheck on pull requests and `main`. The release dry-run workflow uses [Quill](https://github.com/NerdsWhoFish/quill) to cross-build and package both Linux architectures without publishing or tagging. CI also calls that workflow on `main`, including the Docker build and embedded version/commit checks. Quill tracks its documented `v1` alias; other direct action references are pinned to commits.

### Release

Open [Actions > Release](https://github.com/NerdsWhoFish/feedback/actions/workflows/release.yaml), select `main`, choose the scope and bump, then run the workflow. Only this manual workflow publishes. With no existing tags, `Release` plus `minor` creates `v0.1.0`; `patch` creates `v0.0.1`, and `major` creates `v1.0.0`. `Release Candidate` produces a prerelease; promoting a candidate ignores the bump and uses its version.

Quill runs GoReleaser before Docker. The GoReleaser before hook installs locked frontend dependencies and builds the SPA, then GoReleaser embeds it into static `linux/amd64` and `linux/arm64` binaries. It uploads binary archives and checksums to the GitHub release. The Dockerfile copies those artifacts into distroless and verifies the embedded version and commit before packaging. QEMU runs that verification for ARM64 on the AMD64 runner. The release disables Docker's snapshot dry run and shared cache so stable images consume the final release artifacts. PR and main dry runs still exercise Docker with an explicitly matched snapshot version.

Images publish to `ghcr.io/nerdswhofish/feedback` with SBOMs and provenance. Stable versions receive full, minor and major version tags plus `latest` and `production`. Candidates receive only their full candidate tag. Pin a reviewed digest when deploying.

No custom repository secrets are needed for releases. The workflow uses `GITHUB_TOKEN` with `contents: write` and `packages: write`. Organization policy must allow Actions and GHCR package creation. After the first release, check the package's visibility and set it to public if anonymous pulls are intended; a public repository does not automatically make a new GHCR package public. Configure required CI checks and human review before subsequent changes ship.

Validate release artifacts locally without Docker or publication:

```sh
goreleaser check
goreleaser release --snapshot --clean --skip=docker,publish
```

With Docker running, package a local snapshot using the same verification as CI:

```sh
docker buildx build --platform linux/amd64,linux/arm64 \
  --build-arg VERSION="$(jq -r .version dist/metadata.json)" \
  --build-arg COMMIT="$(git rev-parse HEAD)" \
  -t feedback:local .
```

Before production, review the ADRs, register the OIDC client, provision secrets and Grafana Faro, verify Tinyauth's client allowlist, configure the verified Resend sender if email is wanted, and exercise real OIDC, webhook, email and telemetry delivery in the target environment. Local tests do not establish that external configuration is correct.

## License

[Apache License 2.0](LICENSE). Third-party dependencies retain their own licenses.
