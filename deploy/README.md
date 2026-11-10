# Kubernetes deployment

The example runs one unprivileged, read-only container and a ClusterIP Service. PostgreSQL, ingress, DNS, network policy, TLS and Flux wiring belong to the cluster repository. Replace the moving image tag with the reviewed image digest before deployment.

Create a Secret named `feedback` in the chosen namespace using your secret-management workflow. Required keys are `DATABASE_URL`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `ADMIN_EMAILS`, `WEBHOOK_ENCRYPTION_KEY`, `OTEL_EXPORTER_OTLP_HEADERS` and `FARO_COLLECTOR_URL`. Do not commit a populated Secret. `DATABASE_URL` should use a dedicated role, TLS and the appropriate CA for the database. Grant that role permission to migrate its own schema.

The OTLP header uses `Authorization=Basic <credential>` syntax, without a trailing newline. Keep it in the Secret, never in the ConfigMap or browser bundle. Provision the Grafana Faro application separately and put its collector endpoint in `FARO_COLLECTOR_URL`; the browser sends to this application's filtered relay.

Generate `WEBHOOK_ENCRYPTION_KEY` once from 32 random bytes encoded as base64, persist it alongside database backups, and preserve it through rollout. Changing it makes existing webhook signing secrets unreadable. Back up PostgreSQL and verify a restore before launch.

The binary applies embedded migrations under a PostgreSQL advisory lock before serving. Migrations are forward-only. Test backup and rollback procedures for schema changes; rolling back an image does not undo a database migration. Multiple replicas share sessions and the webhook queue, but key rate limits are per process, so multiply the configured limit by replica count when sizing ingress controls.

Allow outbound connections only to PostgreSQL, Tinyauth, Grafana and approved public webhook destinations. Webhook destinations must resolve to public addresses. Private cluster endpoints are intentionally rejected. Terminate TLS at the ingress and preserve the application's public origin. Do not use `Remote-*` headers as identity.

After reviewing and applying the manifests in the cluster repo, test `/healthz`, `/readyz`, a real OIDC login, project isolation and a signed webhook. Verify traces, correlated logs, browser measurements and exporter failures in Grafana before opening access. No infrastructure, provider registration or telemetry destination was provisioned by this local build.
