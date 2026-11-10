# 2. Use OIDC sessions and separately scoped machine identities

Date: 2026-11-10

## Status

Proposed.

## Context and Problem Statement

Browser clients must authenticate with Tinyauth while applications submit for their own users and an assistant performs approved implementation work.
The internet-facing application must remain safe even when directly reachable without a forward-auth proxy.
Email membership authorization is meaningful only for browser identity claims from the trusted issuer; an app may supply arbitrary end-user email addresses.

## Considered Options

1. OIDC authorization code with PKCE, server sessions and separate scoped app and assistant keys
2. Trust proxy Remote-* headers for browser identity
3. Use one general-purpose API key and link every supplied identity by email

## Decision Outcome

Chosen: **option 1**.

Use discovery with explicit endpoint fallback, signed ID-token verification, nonce, PKCE S256 and single-use state. Create opaque database-backed session cookies and require CSRF protection for cookie-authenticated writes. Keep app identities under an app namespace, without email linking to browser users. Hash API keys and scope them to projects. Only browser admins can grant approved_for_build; assistants may start approved work, ship in-progress work or return in-progress work to its prior approved state.

## Consequences

### Good

- Direct origin access cannot forge identity through Remote-* headers.
- Compromising one app key cannot gain browser admin privileges through an asserted administrator email.
- Assistant access cannot manufacture its own authorization to build.
- Session and key revocation take effect through server-side checks.

### Bad

- OIDC requires a registered confidential client, stable issuer/client identity and careful provider configuration.
- API-key holders can impersonate users inside their own app namespace, so each app remains responsible for authenticating its users.
- Server sessions introduce database reads and a CSRF token exchange for browser writes.
- Returning in_progress work to approved_for_build is an explicit assistant exception that needs regression coverage.

### Rejected because

- Proxy header trust couples authorization correctness to every ingress path and violates the explicit requirement not to trust forward-auth headers.
- A general key and email-based identity linking would let app-supplied claims cross privilege boundaries and could allow the assistant to approve its own work.
