# Implementation Plan: Pluggable AuthN/AuthZ — Phase 1 Interface Foundation

**Branch**: `031-pluggable-authn-authz` | **Date**: 2026-06-25 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `/specs/031-pluggable-authn-authz/spec.md`  
**GH Issue**: #225  
**Design Doc**: `docs/implementation/pluggable_auth_architecture.md`

## Summary

Replace the hardcoded `isAdmin=true` stubs and `middleware.User` context key with a real,
swappable auth framework. Phase 1 ships the Go interface contracts (`AuthNProvider`,
`AuthZProvider`, `UserDirProvider`, `Principal`, `Decision`) and four in-process providers
(`static-admin`, `allow-all`, `rbac-local`, `none-userdir`). The two live `isAdmin` checks
in `service.go` are migrated to `authz.Authorize` calls, and `callerUsernameOrAnon` is
fixed to return the real principal subject. All existing integration tests must continue
to pass without config changes.

## Technical Context

**Language/Version**: Go 1.25 (gitstore-api)  
**Primary Dependencies**: `golang-jwt/v5 v5.3.1` (already in go.mod), `github.com/spf13/viper v1.21.0`, `go.uber.org/zap v1.28.0`, `golang.org/x/crypto` (bcrypt, already in go.mod)  
**New Dependencies**: None for Phase 1 (go-oidc/v3 deferred to Phase 6)  
**Storage**: In-memory only (`sync.Map` for session blacklist) — no datastore changes  
**Testing**: `go test ./...` (unit + integration tests in `gitstore-api/tests/`)  
**Target Platform**: Linux server  
**Project Type**: Web service (GraphQL API)  
**Performance Goals**: Auth middleware adds < 1ms p95 overhead per request (HS256 validation is ~50µs)  
**Constraints**: Zero breaking changes to existing env vars, JWT format, or GraphQL schema  
**Scale/Scope**: Single-instance deployment; in-process blacklist is sufficient

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Notes |
|-----------|--------|-------|
| I. Test-First | PASS | Unit tests for each provider written before implementation; integration tests exist and must stay green |
| II. API-First | PASS | Interface contracts defined in `contracts/auth-interfaces.md` before any implementation file is created |
| III. Clear Contracts | PASS | `auth.AuthNProvider`, `auth.AuthZProvider`, `auth.UserDirProvider` interfaces are the stable contract; provider packages depend only on these |
| IV. Observability | PASS | `allow-all` emits structured startup warn; `AuthZProvider.Authorize` calls emit structured log lines with provider/action/outcome |
| V. User Story Driven | PASS | All work maps to User Stories 1–4 in spec.md with acceptance scenarios |
| VI. Incremental Delivery | PASS | Phase 1 is independently deployable; does not require Phases 2–7 |
| VII. Simplicity/YAGNI | PASS | No external service deps added; blacklist is in-memory; no fsnotify; no go-oidc in Phase 1 |

**Complexity Justification**: The `ProviderRegistry` indirection (vs. direct injection) is
required because Phase 2+ will swap providers at runtime (SIGHUP reload, future
hot-reconfiguration). Without the registry, every injection site would need updating on
each provider swap — which is exactly the problem this spec solves.

## Project Structure

### Documentation (this feature)

```text
specs/031-pluggable-authn-authz/
├── plan.md              # This file
├── spec.md              # Feature specification
├── research.md          # Phase 0 — all decisions resolved
├── data-model.md        # Phase 1 — entities, interfaces, state transitions
├── quickstart.md        # Phase 1 — local dev setup guide
├── contracts/
│   └── auth-interfaces.md   # Phase 1 — Go interface + config contracts
└── tasks.md             # Phase 2 output (/speckit.tasks command)
```

### Source Code (gitstore-api)

```text
gitstore-api/
├── internal/
│   ├── auth/
│   │   ├── types.go             # NEW — Principal, Decision, Outcome, interfaces, errors
│   │   ├── context.go           # NEW — ContextWithPrincipal, PrincipalFromContext
│   │   ├── registry.go          # NEW — ProviderRegistry, ChainedAuthN
│   │   ├── session.go           # EXISTING — SessionManager (unchanged in Phase 1)
│   │   └── provider/
│   │       ├── staticadmin/
│   │       │   └── provider.go  # NEW — StaticAdminProvider + sessionBlacklist
│   │       ├── allowall/
│   │       │   └── provider.go  # NEW — AllowAllProvider
│   │       ├── rbaclocal/
│   │       │   ├── provider.go  # NEW — RBACLocalProvider
│   │       │   └── policy.go    # NEW — Policy struct + YAML loader
│   │       ├── anonymous/
│   │       │   └── provider.go  # NEW — AnonymousProvider (last in chain)
│   │       └── userdirnone/
│   │           └── provider.go  # NEW — NoneProvider
│   ├── middleware/
│   │   └── auth.go              # MODIFIED — add AuthMiddleware, RequireAuth; add shim
│   ├── graph/
│   │   └── resolver/
│   │       ├── service.go       # MODIFIED — 2 isAdmin checks → authz.Authorize
│   │       ├── helpers.go       # MODIFIED — callerUsernameOrAnon uses PrincipalFromContext
│   │       └── namespace.resolvers.go  # MODIFIED — use PrincipalFromContext (remove GetUserFromContext calls)
│   └── app/
│       └── server.go            # MODIFIED — wire ProviderRegistry from Viper config
└── tests/
    ├── integration/             # EXISTING — must pass unchanged
    └── unit/
        └── auth/                # NEW — provider unit tests
```

## Phase 0 Research Summary

Research is complete. See [research.md](research.md) for all 7 decisions.

Key resolved decisions:
1. In-process providers only — no OPA/OIDC in Phase 1 (deferred to 6/7)
2. `golang-jwt/v5` for static-admin; no `go-oidc/v3` added yet
3. `allow-all` default for local-fast; `rbac-local` for local-secure
4. In-process `sync.Map` blacklist — Redis/ScyllaDB deferred to Phase 3
5. Compatibility shim in `middleware/auth.go` during migration
6. rbac-local hot-reload via SIGHUP only; no fsnotify
7. Viper reads `GITSTORE_AUTH__AUTHN__CHAIN` as `[]string` (comma-separated)

## Phase 1 Design Summary

### New packages

| Package | File | Responsibility |
|---------|------|---------------|
| `internal/auth` | `types.go` | All interfaces + Principal + Decision + errors |
| `internal/auth` | `context.go` | ctx helpers for Principal storage/retrieval |
| `internal/auth` | `registry.go` | ProviderRegistry + ChainedAuthN |
| `internal/auth/provider/staticadmin` | `provider.go` | HS256 JWT + Basic Auth + blacklist |
| `internal/auth/provider/allowall` | `provider.go` | Unconditional allow + startup warn |
| `internal/auth/provider/rbaclocal` | `provider.go`, `policy.go` | YAML RBAC evaluation |
| `internal/auth/provider/anonymous` | `provider.go` | Anonymous fallback (last in chain) |
| `internal/auth/provider/userdirnone` | `provider.go` | ErrNotSupported stub |

### Modified files

| File | Change |
|------|--------|
| `internal/middleware/auth.go` | Add `AuthMiddleware`, `RequireAuth`; shim `GetUserFromContext` |
| `internal/graph/resolver/service.go` | Replace 2 isAdmin params with `authz.Authorize` calls |
| `internal/graph/resolver/helpers.go` | `callerUsernameOrAnon` → `auth.PrincipalFromContext(ctx).Subject` |
| `internal/graph/resolver/namespace.resolvers.go` | Replace `middleware.GetUserFromContext` with `auth.PrincipalFromContext` |
| `internal/app/server.go` | Wire `ProviderRegistry` from Viper; inject into middleware and Service |

### No schema changes

The public GraphQL schema (`shared/schemas/auth.graphqls`) is unchanged in Phase 1.
`Logout` and `RefreshToken` remain stubbed; they are wired to the blacklist in Phase 3.

## Complexity Tracking

No constitution violations.
