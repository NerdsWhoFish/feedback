# Architecture decisions

Both records are proposed and need human review before deployment.

| # | Decision | In one line |
| --- | --- | --- |
| [0001](0001-build-a-focused-feedback-service-with-explicit-trust-boundar.md) | Build a focused feedback service with explicit trust boundaries | Own project and assistant authorization in one Go and PostgreSQL service. |
| [0002](0002-use-oidc-sessions-and-separately-scoped-machine-identities.md) | Use OIDC sessions and separately scoped machine identities | Use OIDC browser sessions and disjoint app and assistant privileges. |
