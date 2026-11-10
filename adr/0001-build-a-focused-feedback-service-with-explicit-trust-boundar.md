# 1. Build a focused feedback service with explicit trust boundaries

Date: 2026-11-10

## Status

Proposed.

## Context and Problem Statement

Nerds Who Fish needs private project memberships, app-attributed submissions and a human approval gate for assistant implementation.
The requested deployment is one Go binary serving a React SPA with PostgreSQL on the home Kubernetes cluster.
Fider is an established feedback product, but this brief explicitly commissions a new service with these security boundaries; no claim is made that Fider cannot be extended to meet them.

## Considered Options

1. Build the requested Go, React and PostgreSQL service
2. Adopt Fider and adapt its authorization and API model
3. Use issue trackers separately per client

## Decision Outcome

Chosen: **option 1**.

Build the requested service. Keep request changes, audit history and webhook delivery enqueueing transactional in PostgreSQL. Use embedded migrations and plain SQL to make tenant predicates visible during review. Treat the approval state machine as a server policy, with integration tests using a real database.

## Consequences

### Good

- The authorization boundary matches the explicit v1 contract and can be tested independently of the UI.
- One binary and one datastore fit the intended deployment without additional queue infrastructure.
- Project isolation, app namespaces and human approval share one auditable model.

### Bad

- We own authentication integration, security patching, migrations, backups and feature maintenance.
- This version has fewer community integrations and operational history than an established product.
- A durable database-backed webhook queue adds contention and operational work to the database.

### Rejected because

- Fider remains a sensible alternative if its extension cost proves lower, but adopting and auditing those extensions is a different scope from the explicitly commissioned greenfield build.
- Separate issue trackers fragment client access and require another integration layer to enforce the application identity and assistant approval contract.
