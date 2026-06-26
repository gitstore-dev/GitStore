# Contract: Session Lifecycle Interfaces — Phase 3

**Branch**: `032-auth-phase-3` | **Date**: 2026-06-26  
**Spec**: [spec.md](../spec.md) | **Research**: [research.md](../research.md)

---

## GraphQL Schema (unchanged)

The following schema is already defined in `shared/schemas/auth.graphqls` and requires **no changes** in Phase 3. It is reproduced here as the stable external contract.

```graphql
type AuthSession {
  token:     String!
  expiresAt: DateTime!
  user:      User!
}

type User {
  username: String!
  isAdmin:  Boolean!
}

input LoginInput {
  username: String!
  password: String!
}
type LoginPayload {
  session: AuthSession
}

input LogoutInput {
  clientMutationId: String   # retained for schema compatibility; ignored in Phase 3
}
type LogoutPayload {
  success: Boolean!
}

input RefreshTokenInput {
  clientMutationId: String   # retained for schema compatibility; ignored in Phase 3
}
type RefreshTokenPayload {
  session: AuthSession
}

extend type Mutation {
  login(input: LoginInput!): LoginPayload!
  logout(input: LogoutInput!): LogoutPayload!
  refreshToken(input: RefreshTokenInput!): RefreshTokenPayload!
}
```

---

## Go Interface Additions

### `auth.AuthNProvider` — `IssueSession` (new method)

```go
// IssueSession mints a new session token for the given subject.
// Returns (token, expiry, nil) on success.
// Returns ("", zero, ErrNotSupported) for providers that cannot issue tokens.
IssueSession(ctx context.Context, subject string) (token string, exp time.Time, err error)
```

**All providers** that previously implemented `AuthNProvider` must add this method. For Phase 3, only `static-admin` returns a real token; all others return `ErrNotSupported`.

---

### `auth.Principal` — `TokenID` field (new field)

```go
type Principal struct {
    // ... existing fields ...
    TokenID string `json:"jti,omitempty"` // JWT jti — set by static-admin on Bearer auth
}
```

**Contract guarantee**: `TokenID` is non-empty only when the authentication was performed via a JWT Bearer token. It is empty for Basic Auth sessions, anonymous principals, and any future provider that does not assign per-token identifiers. Callers of `RevokeSession` must treat an empty `TokenID` as a no-op rather than an error.

---

### Context Helpers (new)

```go
// ContextWithRawToken stores the raw Bearer token string in ctx.
// Called by ChainAuthMiddleware after extracting the Authorization header value.
func ContextWithRawToken(ctx context.Context, rawToken string) context.Context

// RawTokenFromContext retrieves the raw Bearer token stored by ContextWithRawToken.
// Returns "" if no raw token is present (unauthenticated requests, Basic Auth sessions).
func RawTokenFromContext(ctx context.Context) string
```

---

## Resolver Behaviour Contracts

### `login`

| Condition | Outcome |
|-----------|---------|
| Valid credentials | `LoginPayload{Session: {token, expiresAt, user{username, isAdmin: principal.IsAdmin()}}}` |
| Invalid credentials | `gqlerror` — "invalid username or password" |
| No provider supports `IssueSession` | `gqlerror` — "authentication service unavailable" |
| Registry not configured | `gqlerror` — "authentication service unavailable" |

**Post-Phase-3**: `user.isAdmin` is derived from `principal.IsAdmin()` (roles-based), not hardcoded `true`. `user.username` is `principal.Subject`.

---

### `logout`

| Condition | Outcome |
|-----------|---------|
| Valid authenticated session (Bearer) | `LogoutPayload{Success: true}` |
| Anonymous / unauthenticated | `gqlerror` — authentication required (401 semantics) |
| Token has no `jti` (e.g. fresh Basic Auth principal with no TokenID) | `LogoutPayload{Success: true}` (no-op; nothing to revoke) |
| `RevokeSession` returns `ErrNotSupported` for all providers | `gqlerror` — "logout not supported by active auth configuration" |
| `RevokeSession` returns a non-`ErrNotSupported` error | `gqlerror` — internal error |

---

### `refreshToken`

| Condition | Outcome |
|-----------|---------|
| Valid non-expired token | `RefreshTokenPayload{Session: {newToken, newExp, user}}` |
| Token expired within grace window | `RefreshTokenPayload{Session: {newToken, newExp, user}}` |
| Token expired beyond grace window | `gqlerror` — "token too old to refresh, please log in again" |
| Token already revoked (blacklisted) | `gqlerror` — "token has been revoked" |
| No raw token in context (no Bearer header) | `gqlerror` — authentication required |
| `RefreshSession` returns `ErrNotSupported` | `gqlerror` — "token refresh not supported by active auth configuration" |

---

## Error Vocabulary

All mutation errors are returned as GraphQL errors (not HTTP 4xx) since the endpoint is a single `/graphql` route. Error messages follow the pattern already in use in the codebase:

| Situation | GraphQL error message |
|-----------|----------------------|
| Unauthenticated caller on protected mutation | `"authentication required"` |
| Invalid credentials (login) | `"invalid username or password"` |
| Revoked token | `"token has been revoked"` |
| Expired token, beyond grace | `"token too old to refresh, please log in again"` |
| Provider feature not supported | `"<operation> not supported by active auth configuration"` |
| Internal errors | `"internal server error"` (details logged, not exposed) |

---

## Invariants

1. After `logout` succeeds, `Authenticate` with the same token returns `OutcomeDeny` — not `OutcomeChallenge` or `OutcomeAllow`.
2. After `refreshToken` succeeds, `Authenticate` with the **old** token returns `OutcomeDeny`.
3. The new token returned by `refreshToken` passes `Authenticate` with `OutcomeAllow`.
4. Two concurrent `refreshToken` calls with the same token: exactly one succeeds; the other fails because the first call blacklisted the token.
5. A `logout` call with an already-expired token still succeeds (idempotent safety margin — the token is harmless after expiry but adding it to the blacklist is always safe).
