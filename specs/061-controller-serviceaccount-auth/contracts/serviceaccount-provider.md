# Contract: `serviceaccount-assertion` and `serviceaccount-jwt` AuthN Providers

Both providers implement the existing, unmodified `auth.AuthNProvider` interface
(`gitstore-api/internal/auth/types.go`) — no interface change. Both are strictly
opt-in via `auth.authn.chain` (FR-009); neither is added to any default chain by
this spec.

## `serviceaccount-assertion` (`gitstore-api/internal/auth/provider/serviceaccountassertion`)

| Method | Behavior |
|---|---|
| `Name()` | `"serviceaccount-assertion"` |
| `Capabilities()` | `auth.CapAuthenticate` only — no `CapIssueSession`, no `CapIntrospect`. |
| `Authenticate(ctx, req)` | 1. Extract bearer token. Parse **without** verifying signature; check protected header `typ == "gitstore-sa-assertion+jwt"`. If not, return `OutcomeChallenge` ("not my token, try next"). 2. Look up the target `ServiceAccount` by the untrusted `sub`/`sa_uid` claims (for key lookup only — never trusted for authorization). If no such account, or it is disabled/deleted, or `sa_uid` mismatches, return `OutcomeDeny`. 3. Select the enrolled public key matching the assertion's `kid` header; if none, `OutcomeDeny`. 4. Verify signature against that key. 5. Verify `aud == auth.serviceaccount.assertion_audience`, `exp - iat <= 60s`, `nbf`/`iat` within `clock_skew`. 6. Verify `jti` has not been consumed in the replay window; record it. 7. On success, return `Principal{Subject: sub, AuthMethod: "serviceaccount-assertion"}` — this principal is valid for exactly one operation (see field-authorizer gate below). |
| `RevokeSession` | `auth.ErrNotSupported` — assertions are single-use by construction (replay cache), not a revocable session. |
| `RefreshSession` | `auth.ErrNotSupported` — there is no "session" to refresh; a new assertion is signed for each exchange. |
| `IssueSession` | `auth.ErrNotSupported` — this provider never mints tokens; that is `serviceaccount-jwt`'s issuer half, invoked only through the `issueServiceAccountToken` resolver, not through `ChainedAuthN.IssueSession`. |

**Field-level gate**: `gitstore-api/internal/middleware/security/graphql.go`'s `GraphQLFieldAuthorizer` MUST hard-deny any GraphQL operation authenticated via `AuthMethod == "serviceaccount-assertion"` except the single `issueServiceAccountToken` mutation field, and MUST additionally verify the authenticated `Principal.Subject`/UID exactly match the `ServiceAccount` targeted by that mutation's input — mirroring the existing per-field gate shape already used for `category.status.write` (doc 021 §8b). This is enforced independent of whatever `rbac-local` role, if any, is bound to the asserted subject — an administrator cannot use a `role_bindings` grant to make an assertion-authenticated principal usable for anything beyond issuance.

## `serviceaccount-jwt` (`gitstore-api/internal/auth/provider/serviceaccountjwt`)

| Method | Behavior |
|---|---|
| `Name()` | `"serviceaccount-jwt"` |
| `Capabilities()` | `auth.CapAuthenticate` only. |
| `Authenticate(ctx, req)` | 1. Extract bearer token. Parse without verifying signature; check `iss == auth.serviceaccount.issuer`. If not, `OutcomeChallenge`. 2. Verify signature against the currently-trusted signing key set (supports multiple simultaneously-trusted `kid`s during the rotation overlap window — FR-013). 3. Verify `aud` contains `auth.serviceaccount.audience`; if absent, `OutcomeDeny` (this is a real auth failure, not "not my token," since `iss` already matched). 4. Verify `exp`/`nbf`/`iat` with `clock_skew` leeway. 5. Look up the `ServiceAccount` by `sub`; if disabled, deleted, or `sa_uid` mismatches the record's current UID, `OutcomeDeny`. 6. Return `Principal{Subject: sub, Roles: nil, AuthMethod: "serviceaccount-jwt", ExpiresAt: exp, TokenID: jti}`. |
| `RevokeSession` | Optional per-token `jti` blacklist entry (defense in depth); account disable/delete is the authoritative, persistent revocation mechanism and does not depend on this method being called. |
| `RefreshSession` | `auth.ErrNotSupported` — service accounts renew via proof-of-possession (`serviceaccount-assertion` → `issueServiceAccountToken`), never via a refresh-token flow, and never using a still-valid previous access token as authorization for a new one. |
| `IssueSession` | `auth.ErrNotSupported` for the generic `ChainedAuthN.IssueSession(ctx, subject)` entry point — issuance happens exclusively through the assertion-gated `issueServiceAccountToken` resolver, which calls this provider's internal issuer half directly, not through the generic per-provider `IssueSession` dispatch (avoiding exactly the "first provider in the chain that supports IssueSession wins" ambiguity spec 060's research.md Decision 8 already identifies and avoids for `static-users`). |

## Decision-outcome summary (both providers)

| Condition | Outcome | Rationale |
|---|---|---|
| Wrong `iss` (assertion) / wrong `typ` header (jwt) | `OutcomeChallenge` | "Not my token" — preserves chain fallthrough to the next provider (e.g. `static-users`, `oidc-jwt`). |
| Right `iss`/`typ` but bad signature, expired, wrong audience, disabled/deleted account, UID mismatch, or (assertion only) replayed `jti` | `OutcomeDeny` | A real authentication failure for a token this provider does recognize as its own. |
| All checks pass | `OutcomeAllow` + populated `Principal` | — |

## `buildProviderRegistry` wiring (`gitstore-api/internal/app/server.go`)

```go
switch name {
case "static-admin": // or "static-users" post-spec-060
    ...
case "anonymous":
    ...
case "serviceaccount-assertion":
    p, err := serviceaccountassertion.New(cfg.Auth.ServiceAccount, store, log)
    ...
case "serviceaccount-jwt":
    p, err := serviceaccountjwt.New(cfg.Auth.ServiceAccount, store, log)
    ...
}
```

Reuses the exact `switch name { case "...": }` dispatch shape spec 060 already used to swap `"static-admin"` for `"static-users"` — no new dispatch mechanism.

## Metrics and logging (doc 021 §13, carried forward)

- New counter `gitstore_api_authn_requests_total{provider,outcome}`, extending the existing per-provider outcome pattern already shipped for git-http (`020-pluggable_auth_architecture.md` Phase 5's `gitstore_git_http_auth_requests_total{outcome,service}`).
- Existing `DecisionLogger` structured fields (`provider, subject, action, resource_kind, resource_name, outcome, reason, request_id, latency_ms`) require no new code — `subject` naturally renders as `serviceaccount:<namespace>:<name>` for both providers.
- Neither provider logs a raw assertion, access token, or private key value at any level (FR-020, SC-007) — enforced by a grep-based CI check asserting no `zap` call in either package logs a field matching the raw token/assertion value.
