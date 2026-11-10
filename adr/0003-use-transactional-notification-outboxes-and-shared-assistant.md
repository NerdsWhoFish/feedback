# 3. Use transactional notification outboxes and shared assistant operations

Date: 2026-11-10

## Status

Accepted.

## Context and Problem Statement

Assistants need MCP access without a second authorization implementation. Shipping must produce a reviewable changelog draft. Notification delivery must survive failures without delaying domain transactions or duplicating mail.

## Considered Options

1. Stateless official MCP SDK adapting existing REST handlers; PostgreSQL notification outbox and Resend
2. Separate MCP business logic and synchronous email delivery
3. External message broker and SMTP delivery

## Decision Outcome

Chosen: **option 1**.

Use the official maintained Go MCP SDK in stateless Streamable HTTP mode with assistant-key authentication on each request. Route tools through the same in-process REST handlers. Commit automatic linked drafts and email jobs with the request change. Keep changelog publishing restricted to browser admins. Dispatch through Resend with immutable payloads, stable idempotency keys, shared database pacing and bounded retries. Stop retrying before Resend's 24-hour deduplication window expires. Serve Prometheus on a separate opt-in listener.

## Consequences

### Good

- One authorization and validation path protects both assistant transports.
- Transactional outboxes survive process restarts and avoid sending events for rolled-back changes.
- Admins retain editorial control; drafts stay hidden from members.
- No extra queue infrastructure, and private metrics remain disabled by default.

### Bad

- The MCP adapter inherits REST response shapes and bounded detail pages; clients must page activity explicitly.
- Outbox workers add database load and operations must inspect terminal delivery failures.
- Resend is an external delivery dependency; stopping before deduplication expiry favors avoiding duplicates over unlimited retry.

### Rejected because

- Separate business logic risks permission drift, while synchronous delivery couples writes to provider outages and cannot atomically coordinate mail with database commits.
- A broker adds operational overhead to a single-service application. SMTP was explicitly replaced by Joey's Resend choice and does not provide the required provider idempotency contract.
