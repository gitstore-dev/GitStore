# Phase 0 Research: Pluggable AuthN/AuthZ — Phase 3 Session Lifecycle

**Branch**: `032-auth-phase-3` | **Date**: 2026-06-26  
**Status**: Complete — all design decisions resolved

---

## D1 — How do resolvers obtain the raw Bearer token for `RefreshSession`?

**Decision**: Add `ContextWithRawToken` / `RawTokenFromContext` helpers to `internal/auth/context.go`. `ChainAuthMiddleware` extracts the raw `Authorization: Bearer <token>` value before calling the chain and stores it via `ContextWithRawToken`. Resolvers read it back via `RawTokenFromContext`.

**Rationale**: `RefreshSession` in `staticadmin` re-parses the token with `jwt.WithoutClaimsValidation()` to honour the grace window. It needs the original signed string — the `Principal` only carries derived fields. Storing the raw token in context is the minimal, non-leaky approach; it does not expose the token to unrelated code paths.

**Alternatives considered**:
- Pass the raw token as an argument to `RefreshSession` → already the case (the interface takes `oldToken string`); the question is how the resolver obtains it without reading the HTTP request directly — `ContextWithRawToken` is the answer.
- Store token in `Principal.Claims["raw"]` → leaks transport-layer artefact into the identity model; rejected.

---

## D2 — How does `logout` know the token's `jti` to pass to `RevokeSession`?

**Decision**: Add `TokenID string` to `auth.Principal`. `staticadmin.authenticateBearer` sets `principal.TokenID = claims.ID` after successful JWT validation. The `logout` resolver reads `principal.TokenID` and `principal.ExpiresAt` and calls `registry.AuthN().RevokeSession(ctx, principal.TokenID, principal.ExpiresAt)`.

**Rationale**: `jti` is a stable per-token identifier already tracked by the `sessionBlacklist`. Embedding it in `Principal` is correct — it is a property of the authenticated session, not a transport artefact. It costs nothing (one string field) and avoids re-parsing the raw token for logout.

**Alternatives considered**:
- Re-parse the raw token in the logout resolver to extract `jti` → redundant parse; the provider already validated it; rejected.
- Use `Principal.Claims["jti"]` → untyped map access; error-prone; rejected.

---

## D3 — How does `login` issue a token after migrating away from legacy `authMiddleware`?

**Decision**: Extend `AuthNProvider` with `IssueSession(ctx context.Context, subject string) (token string, exp time.Time, err error)`. `StaticAdminProvider` implements it via the existing `IssueToken`. All other providers return `ErrNotSupported`. `ChainedAuthN.IssueSession` delegates to the first supporting provider. The `login` resolver:
1. Builds `auth.AuthRequest{Header: "Authorization: Basic base64(username:password)"}`.
2. Calls `registry.AuthN().Authenticate(ctx, req)` to validate credentials.
3. On `OutcomeAllow`, calls `registry.AuthN().IssueSession(ctx, principal.Subject)` to mint the JWT.

**Rationale**: The existing `AuthNProvider` interface already has `RevokeSession` and `RefreshSession`; `IssueSession` completes the session lifecycle symmetrically. No new package is required. The `login` resolver can migrate fully without knowing which provider is active.

**Alternatives considered**:
- Keep `authMiddleware.GenerateSessionToken` for token minting, migrate only credential validation → partial migration; leaves dead code; `isAdmin` must still be hardcoded because the legacy token format carries `Claims{Username, IsAdmin}`; rejected.
- Type-assert to `*staticadmin.StaticAdminProvider` in the resolver → breaks provider pluggability; rejected.
- Introduce a separate `SessionIssuer` interface → extra indirection with no benefit at current scale; rejected.

---

## D4 — How is the refresh grace window enforced?

**Decision**: Add `refreshGrace time.Duration` to `StaticAdminProvider`, configured via `GITSTORE_AUTH__JWT__REFRESH_GRACE` (default `60s`). In `RefreshSession`, after parsing with `jwt.WithoutClaimsValidation()`, check:
```
if claims.ExpiresAt != nil && time.Now().After(claims.ExpiresAt.Time.Add(refreshGrace)) {
    return "", time.Time{}, errors.New("staticadmin: token too old to refresh")
}
```
This accepts tokens expired by up to `refreshGrace`; rejects tokens expired beyond it.

**Rationale**: The current `RefreshSession` uses `WithoutClaimsValidation()` with no upper bound — it would accept a token that expired weeks ago, which is a security regression. The grace window provides a bounded "refresh window" analogous to OIDC `refresh_token` validity. 60 s matches common JWT library defaults and the existing 2-minute leeway in `Authenticate`.

**Alternatives considered**:
- Use the existing 2-minute JWT leeway instead of a separate config → the `Authenticate` leeway is for clock skew, not intentional refresh; conflating them makes the leeway value ambiguous; rejected.
- Enforce grace window in the resolver (not the provider) → violates provider-encapsulation principle; each provider should own its own token semantics; rejected.

---

## D5 — How does `ProviderRegistry` reach the `auth.resolvers.go` file?

**Decision**: Add `Registry *auth.ProviderRegistry` to `resolver.ResolverDeps` and store it on the `Resolver` struct. `NewGraphQLHandler` already receives `registry`; it passes it through `ResolverDeps`. The `auth.resolvers.go` `Login`, `Logout`, and `RefreshToken` methods access it via `r.registry`.

**Rationale**: The `ResolverDeps` / `ServiceDeps` pattern is already established for injecting `AuthZ`. Reusing it for `Registry` is consistent and keeps the resolver testable — tests pass a mock/stub registry.

**Alternatives considered**:
- Use a new `WithRegistry(*auth.ProviderRegistry)` mutator on `Resolver` (like `WithAuthMiddleware`) → inconsistent with the `ResolverDeps` pattern used since Phase 1; rejected.
- Store registry on `Service` instead of `Resolver` → session lifecycle is a resolver-level concern (it reads/writes context); `Service` handles datastore operations; rejected.

---

## D6 — Should `logout` / `refreshToken` be guarded at the HTTP or resolver level?

**Decision**: Enforce authentication at the resolver level. The `logout` and `refreshToken` resolvers check `auth.PrincipalFromContext(ctx)` — if the principal is `nil` or anonymous (`AuthMethod == "none"`), return a `gqlerror`. No new HTTP middleware is needed; all GraphQL mutations already flow through `ChainAuthMiddleware`.

**Rationale**: GraphQL uses a single `/graphql` endpoint; per-mutation HTTP guards require either route splitting or a custom gqlgen directive. A resolver-level check is simpler, consistent with how `createNamespace` guards its own authz, and testable without an HTTP layer.

**Alternatives considered**:
- Add a gqlgen directive `@requireAuth` → powerful but requires code-gen changes and a schema amendment; deferred to a future polish phase.
- Split `/graphql/public` vs `/graphql/authed` routes → overengineered for current single-admin scope; rejected.

---

## Summary of code changes required

| File | Change |
|------|--------|
| `internal/auth/types.go` | Add `TokenID string` to `Principal`; add `IssueSession` to `AuthNProvider` interface |
| `internal/auth/context.go` | Add `ContextWithRawToken` / `RawTokenFromContext` helpers |
| `internal/auth/registry.go` | Add `ChainedAuthN.IssueSession` delegation method |
| `internal/auth/provider/staticadmin/provider.go` | Implement `IssueSession`; add `refreshGrace`; enforce grace in `RefreshSession`; set `TokenID` in `authenticateBearer` |
| `internal/auth/provider/anonymous/provider.go` | Implement `IssueSession` returning `ErrNotSupported` |
| `internal/middleware/auth.go` | Store raw token in context via `ContextWithRawToken` in `ChainAuthMiddleware` |
| `internal/graph/resolver/resolver.go` | Add `registry *auth.ProviderRegistry` to `Resolver`; wire from `ResolverDeps` |
| `internal/graph/resolver/auth.resolvers.go` | Implement `Login`, `Logout`, `RefreshToken` using registry |
| `tests/unit/auth/staticadmin_test.go` | Add grace-window and IssueSession tests |
| `tests/unit/resolver/auth_resolvers_test.go` | NEW — unit tests for all three auth resolver mutations |
