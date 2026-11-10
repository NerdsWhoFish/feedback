# Kubernetes deployment

The example runs one unprivileged, read-only container and a ClusterIP Service. PostgreSQL, ingress, DNS, network policy, TLS and Flux wiring belong to the cluster repository. Replace the moving image tag with the reviewed image digest before deployment.

Create a Secret named `feedback` in the chosen namespace using your secret-management workflow. Required keys are `DATABASE_URL`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `ADMIN_EMAILS`, `WEBHOOK_ENCRYPTION_KEY`, `OTEL_EXPORTER_OTLP_HEADERS` and `FARO_COLLECTOR_URL`. Do not commit a populated Secret. `DATABASE_URL` should use a dedicated role, TLS and the appropriate CA for the database. Grant that role permission to migrate its own schema.

For email, add `RESEND_API_KEY` and `EMAIL_FROM` to the Secret. Verify the sending domain in Resend before enabling, for example `feedback@nerdswhofish.com`. With no API key the queue and worker stay disabled. All replicas share the email outbox and database rate gate. Inspect terminal `email_outbox` outcomes and `email_attempts` when delivery fails. Restoring a database backup can restore pending notifications, so review queue state and Resend delivery history before enabling a restored worker.

Prometheus is optional. Set `METRICS_ADDR=:9090` only when a private scraper is configured, expose a separate named container port, and allow that port solely from monitoring workloads with a NetworkPolicy. Keep it out of public Service and Ingress routes. The public listener never serves metrics. A colocated scraper can instead use `127.0.0.1:9090`.

The OTLP header uses `Authorization=Basic <credential>` syntax, without a trailing newline. Keep it in the Secret, never in the ConfigMap or browser bundle. Provision the Grafana Faro application separately and put its collector endpoint in `FARO_COLLECTOR_URL`; the browser sends to this application's filtered relay.

Generate `WEBHOOK_ENCRYPTION_KEY` once from 32 random bytes encoded as base64, persist it alongside database backups, and preserve it through rollout. Changing it makes existing webhook signing secrets unreadable. Back up PostgreSQL and verify a restore before launch.

The binary applies embedded migrations under a PostgreSQL advisory lock before serving. Migrations are forward-only. Test backup and rollback procedures for schema changes; rolling back an image does not undo a database migration. Multiple replicas share sessions and the webhook queue, but key rate limits are per process, so multiply the configured limit by replica count when sizing ingress controls.

Allow outbound connections only to PostgreSQL, Tinyauth, Grafana, `api.resend.com` when enabled and approved public webhook destinations. Webhook destinations must resolve to public addresses. Private cluster endpoints are intentionally rejected. Terminate TLS at the ingress and preserve the application's public origin. Do not use `Remote-*` headers as identity.

After reviewing and applying the manifests in the cluster repo, test `/healthz`, `/readyz`, a real OIDC login, project isolation and a signed webhook. Verify traces, correlated logs, browser measurements and exporter failures in Grafana before opening access. No infrastructure, provider registration or telemetry destination was provisioned by this local build.
