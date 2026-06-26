# Implementation Plan: Pluggable AuthN/AuthZ — Phase 3 Session Lifecycle

**Branch**: `032-auth-phase-3` | **Date**: 2026-06-26 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/032-auth-phase-3/spec.md`
**GH Issue**: #226
**Design Doc**: `docs/implementation/pluggable_auth_architecture.md`

## Summary

Wire the `logout` and `refreshToken` GraphQL mutations to the auth provider chain introduced
in Phase 1, and migrate `login` away from legacy `authMiddleware` stubs. Phase 3 adds
`IssueSession` to `AuthNProvider`, stores the raw Bearer token in request context,
adds `TokenID` to `Principal`, and implements all three auth resolver mutations end-to-end.
No new dependencies or schema changes required.

## Technical Context

**Language/Version**: Go 1.25 (gitstore-api)
**Primary Dependencies**: `golang-jwt/v5 v5.3.1` (already in go.mod), `github.com/spf13/viper v1.21.0`, `go.uber.org/zap v1.28.0`
**New Dependencies**: None
**Storage**: In-memory only (existing `sync.Map` session blacklist in `staticadmin`) — no datastore changes
**Testing**: `go test ./...` (unit tests in `gitstore-api/tests/unit/`, integration tests in `gitstore-api/tests/integration/`)
**Target Platform**: Linux server
**Project Type**: Web service (GraphQL API)
**Performance Goals**: `logout` and `refreshToken` add < 1ms overhead versus `login` (blacklist lookup is O(1) map read)
**Constraints**: Zero breaking changes to existing env vars, JWT format, GraphQL schema, or integration tests
**Scale/Scope**: Single-instance deployment; in-process blacklist sufficient

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Notes |
|-----------|--------|-------|
| I. Test-First | PASS | Unit tests for resolver mutations and grace-window enforcement written before implementation; existing staticadmin tests extended |
| II. API-First | PASS | GraphQL schema unchanged (already defined); new `IssueSession` interface and context helpers defined in contracts/session-lifecycle-interfaces.md before implementation |
| III. Clear Contracts | PASS | `AuthNProvider.IssueSession`, `Principal.TokenID`, `ContextWithRawToken`/`RawTokenFromContext` all documented as stable contracts in contracts/ |
| IV. Observability | PASS | Logout and refresh operations emit structured zap log lines with principal subject, jti, and outcome |
| V. User Story Driven | PASS | All work maps to User Stories 1–3 in spec.md |
| VI. Incremental Delivery | PASS | Phase 3 is independently deployable; Phase 4 (gRPC HMAC) and Phase 5 (Git HTTP) do not depend on it |
| VII. Simplicity/YAGNI | PASS | No new packages, no new dependencies, no external services; `IssueSession` completes an existing interface rather than introducing a new one |

## Project Structure

### Documentation (this feature)

```text
specs/032-auth-phase-3/
├── plan.md              # This file
├── spec.md              # Feature specification
├── research.md          # Phase 0 — all decisions resolved
├── data-model.md        # Phase 1 — entities, interfaces, state transitions
├── quickstart.md        # Phase 1 — local dev guide
├── contracts/
│   └── session-lifecycle-interfaces.md   # Phase 1 — Go interface + GraphQL contracts
├── checklists/
│   └── requirements.md  # Spec quality checklist
└── tasks.md             # Phase 2 output (/speckit.tasks command — NOT created here)
```

### Source Code (gitstore-api)

```text
gitstore-api/
├── internal/
│   ├── auth/
│   │   ├── types.go             # MODIFIED — add TokenID to Principal; add IssueSession to AuthNProvider
│   │   ├── context.go           # MODIFIED — add ContextWithRawToken, RawTokenFromContext
│   │   └── registry.go          # MODIFIED — add ChainedAuthN.IssueSession delegation
│   ├── auth/provider/
│   │   ├── staticadmin/
│   │   │   └── provider.go      # MODIFIED — IssueSession, refreshGrace, TokenID in Principal
│   │   └── anonymous/
│   │       └── provider.go      # MODIFIED — add IssueSession returning ErrNotSupported
│   ├── middleware/
│   │   └── auth.go              # MODIFIED — ChainAuthMiddleware stores raw token via ContextWithRawToken
│   ├── graph/resolver/
│   │   ├── resolver.go          # MODIFIED — add registry field + ResolverDeps wiring
│   │   └── auth.resolvers.go    # MODIFIED — implement Login, Logout, RefreshToken
│   └── app/
│       └── server.go            # MODIFIED — pass registry to NewResolver via ResolverDeps
└── tests/
    ├── unit/
    │   ├── auth/
    │   │   └── staticadmin_test.go   # MODIFIED — add IssueSession, grace-window, TokenID tests
    │   └── resolver/
    │       └── auth_resolvers_test.go # NEW — unit tests for Login, Logout, RefreshToken resolvers
    └── integration/                  # EXISTING — must pass unchanged
```

**Structure Decision**: Single-project Go service (`gitstore-api`). No new packages — all changes are additive modifications to existing packages.

## Phase 0 Research Summary

Research is complete. See [research.md](research.md) for all 6 decisions.

Key resolved decisions:
1. **D1** — Raw token stored in context via `ContextWithRawToken`; `ChainAuthMiddleware` extracts and stores it.
2. **D2** — `Principal.TokenID` carries the JWT `jti`; set by `staticadmin.authenticateBearer`; used by `logout` resolver.
3. **D3** — `IssueSession(ctx, subject)` added to `AuthNProvider`; `ChainedAuthN.IssueSession` delegates first-wins; `login` resolver uses it.
4. **D4** — Refresh grace window via `GITSTORE_AUTH__JWT__REFRESH_GRACE` (default `60s`); enforced in `RefreshSession`.
5. **D5** — `registry *auth.ProviderRegistry` added to `Resolver` via `ResolverDeps`.
6. **D6** — Auth guard enforced at resolver level (anonymous check in `logout`/`refreshToken`); no new HTTP middleware.

## Phase 1 Design Summary

### New interface method

| Interface | Method | Implemented by |
|-----------|--------|---------------|
| `auth.AuthNProvider` | `IssueSession(ctx, subject) (token, exp, error)` | `static-admin` (real), `anonymous` (ErrNotSupported) |

### Modified types

| Type | Change |
|------|--------|
| `auth.Principal` | Add `TokenID string` field (jti from Bearer JWT) |
| `ChainedAuthN` | Add `IssueSession` delegation method |

### New context helpers

| Function | Purpose |
|----------|---------|
| `ContextWithRawToken(ctx, rawToken)` | Store raw Bearer string for refreshToken resolver |
| `RawTokenFromContext(ctx)` | Read raw Bearer string; returns "" if not set |

### New configuration key

| Env Var | Default | Purpose |
|---------|---------|---------|
| `GITSTORE_AUTH__JWT__REFRESH_GRACE` | `60s` | Grace window for expired token refresh |

### Resolver changes

| Resolver | Current state | After Phase 3 |
|----------|--------------|---------------|
| `Login` | Uses `authMiddleware.ValidateCredentials` + `authMiddleware.GenerateSessionToken`; hardcoded `isAdmin: true` | Uses `registry.AuthN().Authenticate` + `registry.AuthN().IssueSession`; `isAdmin` from `principal.IsAdmin()` |
| `Logout` | `return nil, gqlerror.Errorf("not implemented: Logout")` | Calls `registry.AuthN().RevokeSession(ctx, principal.TokenID, principal.ExpiresAt)` |
| `RefreshToken` | `return nil, gqlerror.Errorf("not implemented: RefreshToken")` | Calls `registry.AuthN().RefreshSession(ctx, RawTokenFromContext(ctx))` |

### No schema changes

`shared/schemas/auth.graphqls` is unchanged. `clientMutationId` fields in `LogoutInput` and `RefreshTokenInput` are retained as-is.

## Complexity Tracking

No constitution violations.
