# Research: Pluggable AuthN/AuthZ Phase 1

> All findings sourced from `docs/implementation/pluggable_auth_architecture.md`
> (generated 2026-06-20 via deep-research, 110 agents, 27 sources, 20 verified claims).
> No further unknowns require external resolution for Phase 1.

---

## Decision 1: In-process providers only (no external gRPC/webhook in Phase 1)

**Decision**: Phase 1 ships four in-process providers only: `static-admin`, `allow-all`,
`rbac-local`, `none-userdir`. No OPA sidecar, no OpenFGA server, no webhook.

**Rationale**: External providers require HTTP client wrappers and circuit-breaker logic
before the interface contracts are even stable. The `local-fast` profile constraint
(zero external services) rules them out for Phase 1. Every future external provider
implements the same `AuthZProvider`/`AuthNProvider` interfaces — the upgrade is additive.

**Alternatives considered**:
- OPA sidecar from Phase 1: ruled out — requires running OPA process, adds mandatory
  external service dependency, violates local-fast constraint.
- OpenFGA: ruled out — requires PostgreSQL/MySQL + tuple-loading pipeline; deferred to
  investigation phase only (Phase 7 evaluation).

---

## Decision 2: JWT library — golang-jwt/v5 for static-admin, go-oidc/v3 for OIDC

**Decision**: Keep `golang-jwt/v5` (already in `go.mod`) for the `static-admin` HS256
path. The `oidc-jwt` provider (Phase 6) requires `github.com/coreos/go-oidc/v3` — NOT
added in Phase 1 since the oidc-jwt provider is deferred.

**Rationale**: `golang-jwt/v5` can verify HS256 but cannot perform JWKS-backed RSA/EC
verification. Phase 1 only needs HS256 (static-admin); Phase 6 adds `go-oidc/v3`.
Adding the dependency now without the implementation would be YAGNI.

**Alternatives considered**:
- Add `go-oidc/v3` in Phase 1: ruled out — OIDC provider is deferred to Phase 6; no
  code in Phase 1 uses it.

---

## Decision 3: AuthZ provider default — `allow-all` for local-fast, `rbac-local` for local-secure

**Decision**: `GITSTORE_AUTH__AUTHZ__PROVIDER` defaults to `allow-all` in the local-fast
`.env` profile and `rbac-local` in the local-secure profile.

**Rationale**: `allow-all` requires zero policy file management, suitable for fast local
dev. `rbac-local` enforces the production permission model locally for security testing.

**Alternatives considered**:
- Default to `rbac-local` always: ruled out — requires creating `policy.yaml` on every
  fresh clone, adding friction to the local-fast workflow.

---

## Decision 4: Token blacklist — in-process `sync.Map` only in Phase 1

**Decision**: The `static-admin` provider's session blacklist is an in-process `sync.Map`
keyed by `jti → expiresAt`. A background goroutine prunes expired entries every 5 minutes.

**Rationale**: Single-instance deployment makes Redis/ScyllaDB over-engineering for Phase 1.
The `SessionStore` interface extraction for multi-instance support is an additive change
that doesn't alter the `AuthNProvider` interface — deferred to Phase 3+.

**Alternatives considered**:
- Redis-backed blacklist: ruled out — introduces external service dependency in Phase 1.
- ScyllaDB blacklist table: ruled out — requires schema migration, adds hot-path write
  overhead before the auth framework is even stable.

---

## Decision 5: Context key migration — replace `userContextKey` with `principalContextKey`

**Decision**: A new `principalContextKey` (package-private type in `internal/auth/context.go`)
replaces `userContextKey` from `internal/middleware/auth.go`. A shim
`middleware.GetUserFromContext` wraps `auth.PrincipalFromContext` and returns a synthetic
`*middleware.User{Username: p.Subject, IsAdmin: p.IsAdmin()}` to support callers not yet
migrated.

**Rationale**: Avoids a big-bang migration of every caller; the shim is deleted after all
resolver files switch to `auth.PrincipalFromContext`.

**Alternatives considered**:
- Big-bang migrate all callers at once: ruled out — too many files touched, increases
  review surface and revert risk.
- Keep both context keys simultaneously: ruled out — two parallel auth paths would cause
  subtle bugs where one key is set but not the other.

---

## Decision 6: rbac-local policy hot-reload — SIGHUP only, no fsnotify in Phase 1

**Decision**: `RBACLocalProvider` reloads policy on SIGHUP or API restart. No
`fsnotify` watcher.

**Rationale**: `fsnotify` is not in `go.mod` and adds race complexity. Policy changes
are infrequent in Phase 1 (single admin user, developer environment). Adding `fsnotify`
is a Phase 3 enhancement if live policy reload proves necessary.

**Alternatives considered**:
- `fsnotify` watcher: ruled out — not in go.mod, adds dependency without clear phase-1
  demand.
- Periodic poll (every N seconds): ruled out — adds background goroutine complexity
  without the simplicity benefit of a one-shot SIGHUP.

---

## Decision 7: Viper config chain format — comma-separated string slice

**Decision**: `GITSTORE_AUTH__AUTHN__CHAIN=static-admin` (single provider) or
`GITSTORE_AUTH__AUTHN__CHAIN=oidc-jwt,static-admin` (multi-provider chain). Viper reads
this as a `[]string` via `viper.GetStringSlice("auth.authn.chain")`.

**Rationale**: Consistent with how other multi-value env vars are handled in the codebase
(comma-separated). Viper's `GetStringSlice` handles both single-value and
comma-separated formats.

**Alternatives considered**:
- JSON array env var: ruled out — harder to edit in `.env` files, non-standard for
  shell environments.

---

## Decision 8: Anonymous auth — explicit provider vs. chain fallthrough

**Decision**: Anonymous authentication is an explicit `AnonymousAuthNProvider` placed last in
the chain, not an unconditional fallthrough in `ChainedAuthN`.

**Rationale**: The original design fell through to `Anonymous()` whenever no provider claimed a
request. This conflates two distinct scenarios:
1. No credentials presented — anonymous is legitimate.
2. Credentials presented but no provider accepted them — this is an authentication failure,
   not an anonymous request.

With an explicit provider, scenario 1 is handled by `AnonymousProvider.Authenticate` returning
`Allow` when no credential signals are present. Scenario 2 causes `AnonymousProvider` to return
`Challenge`, so the chain ends with all providers having returned `Challenge`, and the fallthrough
returns `Deny`. This is consistent with how Spring Security models anonymous auth — the anonymous
principal is a valid identity produced by a real provider, not a default applied when auth "fails".

**Consequences**:
- `ChainedAuthN` fallthrough changes from `Allow(Anonymous())` to `Deny`.
- Default chain changes from `["static-admin"]` to `["static-admin", "anonymous"]`.
- Risk 5 (anonymous leaking into authz) becomes structurally prevented for the "credentials
  present but rejected" case; `RequireAuth` middleware still guards the "no credentials + 
  missing from chain" path.

**Alternatives considered**:
- Keep fallthrough-to-anon and guard with assertions: ruled out — defense-in-depth is weaker
  than structural prevention; a missed assertion silently permits unauthorized access.
- Deny all unauthenticated requests unconditionally (no anonymous provider): ruled out —
  read-only public access to repositories is a valid product use case; the `anonymous` provider
  makes that opt-in via chain configuration rather than a hardcoded decision.

---

## Phase 1 Scope Boundary (confirmed)

Phase 1 implements:
- `internal/auth/types.go` — interfaces + Principal + Decision + ErrNotSupported
- `internal/auth/context.go` — `ContextWithPrincipal`, `PrincipalFromContext`
- `internal/auth/registry.go` — `ProviderRegistry`, `ChainedAuthN`
- `internal/auth/provider/staticadmin/` — `StaticAdminProvider` + `sessionBlacklist`
- `internal/auth/provider/allowall/` — `AllowAllProvider`
- `internal/auth/provider/rbaclocal/` — `RBACLocalProvider` + Policy YAML
- `internal/auth/provider/anonymous/` — `AnonymousProvider` (last in chain)
- `internal/auth/provider/userdirnone/` — `NoneProvider`
- `internal/middleware/auth.go` — compatibility shim + wire new middleware
- `internal/graph/resolver/service.go` — replace 2 isAdmin checks with authz.Authorize
- `internal/graph/resolver/helpers.go` — fix callerUsernameOrAnon
- `internal/app/server.go` — wire ProviderRegistry from Viper config at startup

Phase 1 explicitly EXCLUDES (deferred):
- `oidc-jwt` provider (Phase 6)
- OPA provider (Phase 7)
- Logout / RefreshToken mutations wired to blacklist (Phase 3)
- gRPC HMAC interceptor (Phase 4)
- Git smart-HTTP AuthenticatedGitHandler (Phase 5)
- `go-oidc/v3` dependency (Phase 6)
