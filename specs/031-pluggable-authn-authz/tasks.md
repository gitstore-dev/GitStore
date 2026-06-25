# Tasks: Pluggable AuthN/AuthZ — Phase 1 Interface Foundation

**Input**: Design documents from `/specs/031-pluggable-authn-authz/`
**Prerequisites**: plan.md ✅, spec.md ✅, data-model.md ✅, contracts/auth-interfaces.md ✅, research.md ✅

**Tests**: Unit tests are required per the spec (SC-004). Integration tests must pass unchanged (SC-001, SC-002).

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (e.g., US1, US2, US3)
- Include exact file paths in descriptions

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Create the new package skeleton and wire up the base types so every subsequent task has a compile-clean foundation to build on.

- [X] T001 Create directory tree: `gitstore-api/internal/auth/`, `gitstore-api/internal/auth/provider/staticadmin/`, `gitstore-api/internal/auth/provider/allowall/`, `gitstore-api/internal/auth/provider/rbaclocal/`, `gitstore-api/internal/auth/provider/anonymous/`, `gitstore-api/internal/auth/provider/userdirnone/`, `gitstore-api/tests/unit/auth/`
- [X] T002 [P] Create `gitstore-api/internal/auth/types.go` — declare `Outcome`, `Decision`, `Principal`, `Anonymous()`, `IsAdmin()`, `Capability`, `AuthRequest`, `ResourceContext`, `UserProfile`, `AuthNProvider`, `AuthZProvider`, `UserDirProvider`, `ErrNotSupported`, and constructors `Allow()`, `Deny()`, `Challenge()` exactly as specified in `contracts/auth-interfaces.md` and `data-model.md`
- [X] T003 [P] Create `gitstore-api/internal/auth/context.go` — implement `ContextWithPrincipal` and `PrincipalFromContext` using a package-private `principalContextKey` struct as specified in `contracts/auth-interfaces.md`

**Checkpoint**: `go build ./gitstore-api/internal/auth/...` passes with zero errors.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core registry, chain, and all provider skeletons that every user story depends on. Must be complete before any user story work begins.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [X] T004 Create `gitstore-api/internal/auth/registry.go` — implement `ProviderRegistry` (with `sync.RWMutex`, `AuthN()`, `AuthZ()`, `UserDir()` accessors) and `ChainedAuthN` (with `Authenticate` implementing first-Allow-wins, explicit-Deny-short-circuits, Challenge-continues, and fallthrough-to-Deny semantics per `data-model.md` ChainedAuthN section; NOT fallthrough-to-Anonymous); also implement `RevokeSession` delegation
- [X] T005 [P] Create `gitstore-api/internal/auth/provider/userdirnone/provider.go` — implement `NoneProvider` returning `ErrNotSupported` for all `UserDirProvider` methods
- [X] T006 [P] Create `gitstore-api/internal/auth/provider/anonymous/provider.go` — implement `AnonymousProvider`: `Authenticate` returns `Allow(Anonymous())` only when `req.Header.Get("Authorization") == ""` AND `req.ForwardedSubject == ""`; returns `Challenge` otherwise; `RevokeSession` and `RefreshSession` return `ErrNotSupported`
- [X] T007 [P] Create `gitstore-api/internal/auth/provider/allowall/provider.go` — implement `AllowAllProvider.Authorize` returning unconditional `Allow`; emit `warn`-level zap log on `New()` startup: `"SECURITY: authz provider is allow-all — ALL authorization checks are disabled. DO NOT use in production."`
- [X] T008 Create `gitstore-api/internal/auth/provider/staticadmin/provider.go` — implement `StaticAdminProvider` skeleton: struct fields (`username`, `passwordHash`, `blacklist`), `Name()`, `Capabilities()`, `New()` constructor that reads `auth.admin.username` and `auth.admin.password_hash` from Viper and returns error if hash is empty; add `sessionBlacklist` type using `sync.Map[jti → expiresAt]` with `add()`, `isRevoked()` methods and a background goroutine that prunes expired entries every 5 minutes
- [X] T009 Create `gitstore-api/internal/auth/provider/rbaclocal/policy.go` — define `Policy`, `RolePolicy` structs matching the v1 YAML schema in `data-model.md`; implement YAML loader that validates `version == "v1"`, at least one role defined, non-empty role names and actions; `default_deny` defaults to `true` if absent
- [X] T010 Create `gitstore-api/internal/auth/provider/rbaclocal/provider.go` — implement `RBACLocalProvider` skeleton: struct fields (`mu sync.RWMutex`, `policy *Policy`, `path string`), `New()` constructor that calls `reload()`, `reload()` method that reads and validates the YAML policy file; leave `Authorize` method as `panic("not implemented")` for now (implemented in US2 phase)

**Checkpoint**: `go build ./gitstore-api/internal/auth/...` and `go build ./gitstore-api/internal/auth/provider/...` both pass.

---

## Phase 3: User Story 1 — Admin logs in and is recognized as admin (Priority: P1) 🎯 MVP

**Goal**: The `static-admin` provider validates JWT Bearer tokens and Basic Auth credentials, populates a `Principal` with `Roles: ["admin"]`, and the compatibility shim preserves all existing integration tests.

**Independent Test**: Run `make bootstrap` against a freshly started API — login → createNamespace → createRepository must all succeed without any config changes.

### Implementation for User Story 1

- [X] T011 [US1] Implement `StaticAdminProvider.Authenticate` in `gitstore-api/internal/auth/provider/staticadmin/provider.go` — Bearer JWT path: extract `Authorization: Bearer <jwt>`, parse with `golang-jwt/v5` (HS256, `auth.jwt.secret`); parse failure → `Challenge`; validate issuer == `auth.jwt.issuer`; check blacklist by `jti` → if revoked → `Deny`; build `Principal{Subject: claims.sub, Issuer: issuer, Roles: ["admin"], AuthMethod: "static-admin"}`; return `Allow`. Basic Auth path: decode `Authorization: Basic <b64>`; compare username; `bcrypt.CompareHashAndPassword`; on match build Principal and return `Allow`; on mismatch return `Challenge`. Apply `jwt.WithLeeway(2*time.Minute)` per Risk 1 in `pluggable_auth_architecture.md`
- [X] T012 [US1] Implement `StaticAdminProvider.RevokeSession` and `StaticAdminProvider.RefreshSession` in `gitstore-api/internal/auth/provider/staticadmin/provider.go` — `RevokeSession` calls `blacklist.add(jti, expiresAt)`; `RefreshSession` parses old token (ignoring expiry), validates all other claims, checks not blacklisted, revokes old jti, issues new JWT with new jti and `exp = now + auth.jwt.duration`
- [X] T013 [US1] Modify `gitstore-api/internal/middleware/auth.go` — add `AuthMiddleware(registry *auth.ProviderRegistry, logger *zap.Logger) func(http.Handler) http.Handler` that calls `registry.AuthN().Authenticate`, stores principal with `auth.ContextWithPrincipal`, returns 401 for `OutcomeDeny`; add `RequireAuth(next http.Handler) http.Handler` that returns 401 if `principal.AuthMethod == "none"`; add compatibility shim `GetUserFromContext(ctx) (*User, bool)` that wraps `auth.PrincipalFromContext` and returns `*User{Username: p.Subject, IsAdmin: p.IsAdmin()}`
- [X] T014 [US1] Modify `gitstore-api/internal/app/server.go` — wire `ProviderRegistry` from Viper config at startup: read `auth.authn.chain` via `viper.GetStringSlice`, instantiate each named provider (`static-admin`, `anonymous`), build `ChainedAuthN`; read `auth.authz.provider` and instantiate (`allow-all` or `rbac-local`); read `auth.userdir.provider` and instantiate (`none`); construct `ProviderRegistry` and inject into HTTP middleware and `Service`
- [X] T015 [US1] Write unit tests in `gitstore-api/tests/unit/auth/staticadmin_test.go` — cover: valid JWT → Allow with admin principal; expired JWT → Deny; blacklisted jti → Deny; wrong issuer → Challenge; valid Basic Auth → Allow; wrong password → Challenge; no Authorization header → Challenge (not my token)
- [X] T016 [US1] Write unit tests in `gitstore-api/tests/unit/auth/chain_test.go` — cover: first-Allow-wins; explicit-Deny-short-circuits; all-Challenge → Deny (not Anonymous); anonymous provider claims no-credential request → Allow(Anonymous); anonymous provider rejects credential-present request → Challenge

**Checkpoint**: `go test ./gitstore-api/tests/unit/auth/...` passes; `make bootstrap` completes end-to-end; all existing integration tests in `gitstore-api/tests/` pass.

---

## Phase 4: User Story 2 — AuthZ enforces namespace create/delete rules (Priority: P2)

**Goal**: The two hardcoded `isAdmin` checks in `service.go` are replaced by `authz.Authorize` calls; `rbac-local` provider evaluates YAML policy correctly.

**Independent Test**: Unit-test `createNamespace` and `deleteNamespace` in `service_test.go` with both `allow-all` and `rbac-local` providers — admin can create ORGANIZATION namespace; developer cannot delete another owner's namespace.

### Implementation for User Story 2

- [X] T017 [US2] Implement `RBACLocalProvider.Authorize` in `gitstore-api/internal/auth/provider/rbaclocal/provider.go` — for each role in `principal.Roles`: look up role in `policy.Roles`; check if action is in `role.Allow` (including `"*"` wildcard); check if action is in `role.Deny` (explicit deny overrides allow); if any deny matches → `Deny`; if any allow matches and no deny → `Allow`; no match + `default_deny: true` → `Deny`; no match + `default_deny: false` → `Allow`
- [X] T018 [US2] Modify `gitstore-api/internal/graph/resolver/service.go` — replace `createNamespace` isAdmin check with `authz.Authorize(ctx, principal, "namespace.create.organization", auth.ResourceContext{Kind: "namespace", Attrs: map[string]any{"tier": tier}})` per `data-model.md` §3b; replace `deleteNamespace` isAdmin check with owner-aware action selection (`"namespace.delete.own"` vs `"namespace.delete.any"`) and `authz.Authorize` call; inject `AuthZProvider` into `Service` struct
- [X] T019 [US2] Write unit tests in `gitstore-api/tests/unit/auth/rbaclocal_test.go` — cover: admin role → Allow on `namespace.delete.any`; developer role → Deny on `namespace.delete.any`; admin role → Allow on `namespace.create.organization`; anonymous role → Deny on `namespace.create.organization`; `default_deny: true` with unmatched action → Deny; explicit deny overrides allow; `"*"` wildcard matches all actions
- [X] T020 [US2] Write unit tests in `gitstore-api/tests/unit/auth/allowall_test.go` — cover: AllowAll returns Allow for any action/principal combination; startup warning is logged

**Checkpoint**: `go test ./gitstore-api/tests/unit/auth/...` passes; `createNamespace(tier: ORGANIZATION)` succeeds for admin and fails for developer with `rbac-local`; `deleteNamespace` by non-owner fails for developer but succeeds for admin.

---

## Phase 5: User Story 3 — allow-all AuthZ warns on startup (Priority: P2)

**Goal**: When `GITSTORE_AUTH__AUTHZ__PROVIDER=allow-all`, API logs a structured `warn`-level zap message at startup.

**Independent Test**: Start API with `GITSTORE_AUTH__AUTHZ__PROVIDER=allow-all` and grep structured logs for `"SECURITY: authz provider is allow-all"`.

### Implementation for User Story 3

> **Note**: The startup warning is already emitted by `AllowAllProvider.New()` implemented in T007. This phase validates and wires it end-to-end.

- [X] T021 [US3] Verify `gitstore-api/internal/app/server.go` wiring from T014 passes the application zap logger into `allowall.New(logger)` so the startup warning uses the production logger (not a no-op); add a log-level assertion in the server startup that outputs provider names at `info` level for observability
- [X] T022 [US3] Write unit test in `gitstore-api/tests/unit/auth/allowall_test.go` (extend T020) — use a `zap.NewNop()` observer to assert that `AllowAll.New(logger)` emits exactly one `warn`-level entry with message containing `"SECURITY: authz provider is allow-all"`

**Checkpoint**: Start API locally with `GITSTORE_AUTH__AUTHZ__PROVIDER=allow-all`; verify structured warn log appears; `go test ./gitstore-api/tests/unit/auth/...` passes.

---

## Phase 6: User Story 4 — callerUsernameOrAnon returns real subject (Priority: P2)

**Goal**: `helpers.go:callerUsernameOrAnon` returns the authenticated principal's `Subject` instead of always returning `"anon"`.

**Independent Test**: Create a namespace after authenticating; returned `namespace.createdBy` must equal `"admin"`, not `"anon"`.

### Implementation for User Story 4

- [X] T023 [US4] Modify `gitstore-api/internal/graph/resolver/helpers.go` — replace `middleware.GetUserFromContext` call in `callerUsernameOrAnon` with `auth.PrincipalFromContext(ctx)`; return `p.Subject` (which is `"anon"` for the `Anonymous()` principal, the real subject otherwise); handle nil principal gracefully (return `"anon"`)
- [X] T024 [US4] Modify `gitstore-api/internal/graph/resolver/namespace.resolvers.go` — replace all `middleware.GetUserFromContext` calls with `auth.PrincipalFromContext`; remove any remaining references to the old `middleware.User` type in this file
- [X] T025 [US4] Write integration test assertion (extend existing bootstrap test or `gitstore-api/tests/integration/`) — after `make bootstrap` flow, query `namespace.createdBy` on the created namespace and assert it equals `"admin"` not `"anon"`

**Checkpoint**: `namespace.createdBy` returns `"admin"` for authenticated requests; `callerUsernameOrAnon` returns `"anon"` for unauthenticated requests; all existing integration tests pass.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Cleanup, SIGHUP reload wiring, and validation of the full auth framework.

- [X] T026 [P] Add SIGHUP handler in `gitstore-api/internal/app/server.go` — on `SIGHUP`, call `rbacLocalProvider.reload()` (if `rbac-local` is active); log the reload outcome at `info` level with the policy file path
- [X] T027 [P] Add `DecisionLogger` middleware in `gitstore-api/internal/auth/registry.go` or a new `gitstore-api/internal/auth/logging.go` — wraps `AuthZProvider.Authorize` and emits a structured zap log line with fields: `provider`, `subject`, `action`, `resource_kind`, `resource_name`, `outcome`, `reason`, `latency_ms` per Decision 2 in `pluggable_auth_architecture.md`
- [X] T028 Delete the compatibility shim `GetUserFromContext` from `gitstore-api/internal/middleware/auth.go` once T023 and T024 confirm no remaining callers; verify with `grep -r "GetUserFromContext" gitstore-api/` returning zero results
- [X] T029 [P] Update `gitstore-api/internal/app/quickstart.md` validation — run through the quickstart flow end-to-end and confirm all steps produce the documented output
- [X] T030 [P] Update `docs/` — ensure `docs/implementation/pluggable_auth_architecture.md` reflects the final implemented state (anonymous provider placement, chain fallthrough behavior)
- [X] T031 Run `make pr-ready` and fix any lint, test, or license-check failures

**Checkpoint**: `make pr-ready` passes; `make bootstrap` end-to-end succeeds; all unit and integration tests green.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately
- **Foundational (Phase 2)**: Depends on Phase 1 — **BLOCKS all user stories**
- **US1 (Phase 3)**: Depends on Phase 2 — golden path, must complete before US2/US3/US4
- **US2 (Phase 4)**: Depends on Phase 2 (and Service wiring from T014 in US1) — can start after US1's T014
- **US3 (Phase 5)**: Depends on T007 (AllowAll provider from Phase 2) — independently completable after Phase 2
- **US4 (Phase 6)**: Depends on T013/T014 (middleware + server wiring from US1) — start after US1
- **Polish (Phase 7)**: Depends on all user stories complete

### User Story Dependencies

- **US1 (P1)**: Can start after Phase 2 — no dependencies on other stories; **required before US2/US4 can be wired**
- **US2 (P2)**: Depends on T014 (Service injection) from US1; RBACLocal provider itself is independently buildable
- **US3 (P2)**: Depends only on T007 (allowall provider) from Phase 2; independently testable
- **US4 (P2)**: Depends on T013 (middleware) and T014 (server wiring) from US1

### Within Each User Story

- Provider implementation before unit tests that exercise it
- Server wiring (T014) before integration tests
- `rbaclocal/policy.go` (T009) before `rbaclocal/provider.go` (T010/T017)
- `types.go` + `context.go` (T002/T003) before any provider or registry code

### Parallel Opportunities

- T002 and T003 (Setup) are fully independent — run in parallel
- T005, T006, T007 (Phase 2 provider skeletons) are fully independent — run in parallel
- T008, T009, T010 (static-admin + rbac-local setup) are independent of T005/T006/T007 — run in parallel
- T015 and T016 (US1 unit tests) can run in parallel after T011/T012
- T019 and T020 (US2 unit tests) can run in parallel after T017
- T026, T027, T029, T030 (Polish) are independent of each other — run in parallel

---

## Parallel Example: User Story 1

```
# After Phase 2 completes, run in parallel:
T011 — Implement StaticAdminProvider.Authenticate (staticadmin/provider.go)
T013 — Add AuthMiddleware + RequireAuth + shim (middleware/auth.go)

# Then in parallel after T011/T013:
T012 — Implement RevokeSession + RefreshSession (staticadmin/provider.go)
T014 — Wire ProviderRegistry in server.go

# Then in parallel after T011/T012:
T015 — Unit tests for staticadmin
T016 — Unit tests for chain
```

## Parallel Example: User Story 2

```
# After T014 (server wiring) completes:
T017 — Implement RBACLocalProvider.Authorize (rbaclocal/provider.go)
T018 — Replace isAdmin checks in service.go

# After T017 completes, in parallel:
T019 — Unit tests for rbaclocal
T020 — Unit tests for allowall (extends T020 already written)
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (T001–T003)
2. Complete Phase 2: Foundational (T004–T010) — **BLOCKS everything**
3. Complete Phase 3: User Story 1 (T011–T016)
4. **STOP and VALIDATE**: Run `make bootstrap`; existing integration tests pass; `namespace.createdBy` still works via shim
5. All existing functionality preserved — safe to ship as MVP

### Incremental Delivery

1. Phase 1 + Phase 2 → Framework skeleton ready
2. Phase 3 (US1) → Static admin JWT/Basic auth working; compatibility shim in place
3. Phase 4 (US2) → Real RBAC enforcement; isAdmin stubs removed
4. Phase 5 (US3) → Startup warning observable in logs
5. Phase 6 (US4) → Audit trail fixed (`createdBy` = real subject)
6. Phase 7 → Cleanup; shim deleted; decision logging active

### Parallel Team Strategy

After Phase 2 completes:
- Developer A: US1 (T011–T016) — JWT/Basic auth + server wiring
- Developer B: US2 provider work (T017, T019) — RBACLocal Authorize implementation
- Developer C: US3 + US4 (T021–T025) — wiring + resolver fixes

US2's service.go change (T018) depends on T014 from Developer A; coordinate at that handoff.

---

## Notes

- [P] tasks = different files, no dependencies between them
- [Story] label maps each task to a specific user story for traceability
- The `anonymous` provider (T006) MUST be last in `GITSTORE_AUTH__AUTHN__CHAIN` — placing it earlier shadows subsequent providers
- `ChainedAuthN` fallthrough is a `Deny`, not `Anonymous()` — anonymous access only arrives via the `anonymous` provider explicitly
- Compatibility shim in `middleware/auth.go` is temporary; delete in T028 after all callers migrate
- `GITSTORE_AUTH__AUTHN__CHAIN` default is `"static-admin,anonymous"` (comma-separated; Viper reads as `[]string`)
- All existing env vars (`GITSTORE_AUTH__ADMIN__*`, `GITSTORE_AUTH__JWT__*`) must continue to work unchanged
