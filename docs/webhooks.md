# Outgoing webhooks

Use the environment-configured outgoing webhook to wake a trusted triage service when clients submit requests, add public comments, or change status. It sends rich customer data from every project. Keep the receiver under the same administrative control as Feedback, restrict its network access, and protect its stored payloads. Assistant API authentication is separate; see the [assistant workflow](../README.md#assistant-workflow).

## Configuration and trust boundary

```dotenv
BASE_URL=https://feedback.nerdswhofish.com
WEBHOOK_URL=https://triage.example.com/feedback/events
WEBHOOK_SECRET=<server-only-random-signing-secret>
```

Use a high-entropy secret with at least 32 random bytes, supplied through the server's secret-management workflow. The literal placeholder above is not a usable credential. Configuration is loaded at startup. An unset or empty `WEBHOOK_URL` disables this feature completely: no snapshots, queue polling, delivery or cleanup. A configured URL requires a nonblank `WEBHOOK_SECRET`; its exact bytes are the HMAC key. No key is generated, returned by an API, stored in a payload, or logged. The existing `WEBHOOK_ENCRYPTION_KEY` continues protecting separately registered per-project webhooks and retains its existing non-local configuration requirement.

HTTPS is accepted with a valid host and port. HTTP is accepted only for loopback IPs, `localhost`, single-label service names, and DNS names ending in `.svc` or `.svc.cluster.local`. Examples: `http://127.0.0.1:8081/events` and `http://triage.agents.svc:8080/events`. This follows the default internal-host policy of `config.ValidOTLPEndpoint`, with stricter URL syntax checks. `OTEL_EXPORTER_OTLP_INSECURE` never relaxes webhook validation. Public HTTP, private-IP HTTP, `service.namespace` shorthand, credentials, query strings, fragments, invalid ports, malformed DNS labels and ambiguous numeric IP spellings are rejected.

No redirects are followed, including same-host redirects. Ambient proxy settings are ignored. The operator-configured host is trusted and resolved normally on each connection; this is not a tenant-controlled destination or a DNS/IP allowlist. Use cluster network policies or an egress firewall to enforce the intended destination, especially for internal HTTP. TLS certificate verification remains enabled. No automatic endpoint discovery or insecure TLS option is provided.

## Events and payload

| Event | Trigger and extra fields |
| --- | --- |
| `request.created` | A successful browser/session or app-key submission. |
| `comment.created` | A successful public comment, with `comment: {id, body}`. Internal comments never emit an event. |
| `request.status_changed` | An actual committed status transition, with `change: {from, to}`. Forbidden and no-op changes emit nothing. |

An example app-key submission produces this shape. IDs and customer values below are illustrative:

```json
{
  "event": "request.created",
  "delivery_id": "56ca29a0-d0e6-4aba-b33a-ce6b21b61646",
  "timestamp": "2026-11-10T12:00:00Z",
  "project": {
    "id": "18aa264a-d85d-427e-9a99-0b7096d71832",
    "name": "Customer portal"
  },
  "request": {
    "id": "ab2132bf-f695-4bbf-9e97-d3486705a128",
    "title": "Remember my filters",
    "body": "Keep the selected filters between visits.",
    "status": "open",
    "url": "https://feedback.nerdswhofish.com/#project=18aa264a-d85d-427e-9a99-0b7096d71832&request=ab2132bf-f695-4bbf-9e97-d3486705a128&view=requests"
  },
  "submitter": {"email": "client@example.com", "name": "Client"},
  "attribution": {
    "source": "app:customer-portal",
    "app_name": "customer-portal",
    "external_id": "customer-123"
  },
  "actor": {"type": "app", "name": "Customer portal production", "app_name": "customer-portal"}
}
```

`request.body` maps to the API's `description`. The URL uses `BASE_URL` and the existing authenticated portal route; it never includes an anonymous share token. Browser submissions have `attribution: {"source":"portal"}` and a `user` actor with the browser user's actual name/email. App attribution comes from the persisted app identity; app assertions are not independent verification of end-user identity.

`submitter` always describes the request's author, using the current stored identity at event creation. `actor` describes who caused this particular event. Browser actors have `type: "user"`, `name` and `email`; machine actors have `type: "app"` or `"assistant"`, their configured key display name, and `app_name` only for apps. No invented machine email, raw API key, key hash or signing secret is included. For a public assistant comment, the original submitter stays unchanged while the actor identifies the assistant key and `comment` contains its ID and text. For a status change, `request.status` is the new value and `change` identifies both old and new values.

## Headers and verification

Each POST uses `Content-Type: application/json` and these headers:

| Header | Value |
| --- | --- |
| `X-Feedback-Signature` | `sha256=` plus lowercase hexadecimal HMAC-SHA256 of the exact raw JSON bytes, using `WEBHOOK_SECRET`. |
| `X-Feedback-Event` | The payload's `event`. |
| `X-Feedback-Delivery` | The payload's unique UUID `delivery_id`. |
| `X-Feedback-Timestamp` | The payload's `timestamp`: UTC RFC3339 at second precision, for example `2026-11-10T12:00:00Z`. |

The raw JSON is stored as bytes, not reserialized between attempts. The delivery ID, timestamp, payload and signature remain identical across retries and restarts. Only the body is signed. Verify that signature before trusting any parsed fields; use the signed body's timestamp and delivery ID for replay checks and deduplication. Require the unsigned headers to match those verified fields. Do not verify a parsed-and-remarshaled body, add a timestamp prefix to the HMAC, or trust a fresh header timestamp on an old body.

This Go verifier accepts the raw request body and returns authenticated metadata. It uses constant-time comparison and permits the 24-hour delivery window plus five minutes of clock skew:

```go
type EventMetadata struct {
    Event      string `json:"event"`
    DeliveryID string `json:"delivery_id"`
    Timestamp  string `json:"timestamp"`
}

func verify(raw []byte, h http.Header, secret []byte, now time.Time) (EventMetadata, error) {
    var event EventMetadata
    reject := errors.New("invalid webhook")
    if len(secret) == 0 {
        return event, reject
    }
    value := h.Get("X-Feedback-Signature")
    if !strings.HasPrefix(value, "sha256=") {
        return event, reject
    }
    supplied, err := hex.DecodeString(strings.TrimPrefix(value, "sha256="))
    mac := hmac.New(sha256.New, secret)
    _, _ = mac.Write(raw)
    if err != nil || !hmac.Equal(supplied, mac.Sum(nil)) {
        return event, reject
    }
    if err := json.Unmarshal(raw, &event); err != nil {
        return event, reject
    }
    switch event.Event {
    case "request.created", "comment.created", "request.status_changed":
    default:
        return event, reject
    }
    timestamp, err := time.Parse(time.RFC3339, event.Timestamp)
    if err != nil || timestamp.After(now.Add(5*time.Minute)) ||
        now.Sub(timestamp) > 24*time.Hour+5*time.Minute || event.DeliveryID == "" {
        return event, reject
    }
    if h.Get("X-Feedback-Event") != event.Event ||
        h.Get("X-Feedback-Delivery") != event.DeliveryID ||
        h.Get("X-Feedback-Timestamp") != event.Timestamp {
        return event, reject
    }
    return event, nil
}
```

Imports: `crypto/hmac`, `crypto/sha256`, `encoding/hex`, `encoding/json`, `errors`, `net/http`, `strings`, `time`. Read `raw` once through a bounded reader such as `http.MaxBytesReader`, before JSON middleware consumes or changes it. Never log the raw body or authentication material. After verification, atomically persist the signed `delivery_id` with the receiver's durable work item using a unique constraint; retain deduplication records for at least 25 hours. Return `2xx` only after durable acceptance, and also for already accepted duplicates. Keep the event timestamp and delivery ID when handing work to another job system.

## Delivery guarantees and operations

- The snapshot and outbox row commit in the same PostgreSQL transaction as the mutation. Workers can see only committed rows. A rollback emits nothing. Outbox insertion failure fails the transaction rather than silently losing its event; network delivery failure never changes or fails the committed request/comment/status mutation.
- One managed worker per process claims one row at a time with `FOR UPDATE SKIP LOCKED` and a 30-second lease. Memory does not grow with queue depth. A polling burst handles at most 10 attempts, then waits for the one-second ticker. Database queue depth depends on traffic and receiver capacity.
- There are at most five claimed attempts, each with a 10-second timeout. Transport errors, `408`, `429` and `5xx` retry after 15, 30, 60 and 120 seconds. Other non-`2xx` responses, including redirects, are permanent failures. `Retry-After` is not used. Any `2xx` is acceptance; response bodies are discarded with a 4 KiB read limit and are never logged.
- Delivery is best effort with possible duplicates, not exactly once or guaranteed success. A receiver may accept an attempt before Feedback loses the response or crashes. Retry uses the same delivery ID. Leases prevent normal concurrent claims across replicas; expired leases can produce duplicates. A crash during the final claimed attempt becomes terminal `lease_expired` after its lease expires.
- There is no strict ordering, especially across retries and replicas. For current state, read the authenticated request API rather than inferring state from arrival order. A snapshot represents event creation time; later profile or request changes do not modify its bytes.
- Shutdown cancels in-flight HTTP and database work and joins the worker before closing the database. It does not drain the backlog. Unfinished deliveries survive restart; a canceled attempt consumes a claim and becomes eligible when its lease expires.
- Deliveries older than 24 hours are no longer attempted and become terminal `expired` when cleanup runs. Cleanup deletes snapshots older than seven days in batches of at most 100. Cleanup runs only while this env-configured worker is enabled. If it remains disabled, an operator must apply the retention policy to the stored table separately.
- Each row is bound to the exact URL and secret through an HMAC fingerprint. Changing either setting pauses old pending rows; they are never sent to the new destination or signed with a replacement secret. Restoring the previous settings within 24 hours resumes them. Plan rotations by letting the queue finish first. Use identical settings across active replicas, and coordinate rotation because an old process can still deliver to its old configured destination. There is no replay/admin API for this queue.

`outgoing_webhook_deliveries` stores attempts, next attempt time, lease, completion time, last status and bounded outcome, as well as the private snapshot. It is separate from the legacy per-project queue and is not exposed by the existing delivery-list UI/API. Inspect only necessary operational columns, for example:

```sql
SELECT event, attempts, last_status, last_error,
       next_attempt_at, lease_until, completed_at
FROM outgoing_webhook_deliveries
ORDER BY created_at DESC
LIMIT 100;
```

Structured attempt logs include event type, random delivery ID, attempt number, HTTP status, bounded outcome and whether the attempt is terminal. They exclude bodies, customer IDs, email addresses, key material and destination URLs. The existing `feedback_webhook_delivery_attempts_total{outcome}` metric counts both webhook systems with bounded outcome labels. OTel spans cover enqueue, delivery jobs, HTTP attempts and the existing database tracer; logs correlate with trace/span IDs. Only `traceparent` crosses the outgoing boundary, preventing untrusted baggage/tracestate from exporting private values. Database failures and failed outcome persistence emit sanitized error logs. Expiry and retention are visible in database state rather than attempt metrics. Verify the configured receiver and telemetry in the deployment environment before relying on this integration.

The legacy project-admin webhooks keep their underscore event names, metadata-only payloads, timestamp-prefixed signatures and eight-attempt policy. Their verification protocol does not apply to this env-configured sender.
