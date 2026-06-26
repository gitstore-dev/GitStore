# Tasks: Pluggable AuthN/AuthZ — Phase 3 Session Lifecycle

**Input**: Design documents from `/specs/032-auth-phase-3/`
**Branch**: `032-auth-phase-3` | **GH Issue**: #226

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation and verified to FAIL.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no blocking dependencies)
- **[Story]**: Which user story this task belongs to (US1=Logout, US2=RefreshToken, US3=Login migration)
- Exact file paths are included in every task description

## Path Conventions

All paths are relative to the repo root. Source lives under `gitstore-api/`.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Create the test directory for the new resolver unit tests. No new dependencies or project setup required.

- [x] T001 Create directory `gitstore-api/tests/unit/resolver/` for new resolver-level auth mutation tests

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Interface and infrastructure changes that ALL three user stories depend on. No user story work can begin until this phase is complete.

**⚠️ CRITICAL**: Tests MUST be written first (T002) and verified to FAIL before any implementation (T003–T009).

### Tests for Foundational Changes

- [x] T002 Write failing unit tests for `IssueSession`, `refreshGrace` enforcement, and `principal.TokenID` population in `gitstore-api/tests/unit/auth/staticadmin_test.go`; run `go test ./tests/unit/auth/...` and verify they FAIL before continuing

### Implementation

- [x] T003 Add `TokenID string` field to `auth.Principal` and add `IssueSession(ctx context.Context, subject string) (token string, exp time.Time, err error)` to the `AuthNProvider` interface in `gitstore-api/internal/auth/types.go`
- [x] T004 [P] Add `ContextWithRawToken(ctx, rawToken string) context.Context` and `RawTokenFromContext(ctx) string` helpers to `gitstore-api/internal/auth/context.go`
- [x] T005 [P] Add `ChainedAuthN.IssueSession` delegation method (first-wins, returns `ErrNotSupported` when all providers return it) in `gitstore-api/internal/auth/registry.go`
- [x] T006 [P] Implement `IssueSession` returning `ErrNotSupported` on `AnonymousProvider` in `gitstore-api/internal/auth/provider/anonymous/provider.go`
- [x] T007 [P] Add `refreshGrace time.Duration` field to `StaticAdminProvider`; read it from `auth.jwt.refresh_grace` (default `60s`) in `New`; implement `IssueSession` as a thin wrapper over `issueToken`; set `principal.TokenID = claims.ID` in `authenticateBearer`; enforce grace window in `RefreshSession` in `gitstore-api/internal/auth/provider/staticadmin/provider.go`
- [x] T008 Add `ContextWithRawToken` call in `ChainAuthMiddleware` to store the raw Bearer string extracted from the `Authorization` header before calling `registry.AuthN().Authenticate` in `gitstore-api/internal/middleware/auth.go`
- [x] T009 Add `registry *auth.ProviderRegistry` field to `Resolver` struct and `ResolverDeps`; wire `registry` through `NewGraphQLHandler` and pass it via `ResolverDeps` in `NewServer` in `gitstore-api/internal/graph/resolver/resolver.go` and `gitstore-api/internal/app/server.go`

**Checkpoint**: Run `go test ./...` — all existing tests pass; T002 tests now pass. Foundation ready for all user story phases.

---

## Phase 3: User Story 1 — Logout (Priority: P1) 🎯 MVP

**Goal**: `logout` mutation extracts the caller's `TokenID` and `ExpiresAt` from the `Principal`, calls `RevokeSession`, and returns `{success: true}`. Any subsequent request with the same token is rejected.

**Independent Test**: `login` → `logout` → repeat the same authenticated query → expect `"token has been revoked"` error.

### Tests for User Story 1

> **Write these tests FIRST, verify they FAIL, then implement T011**

- [x] T010 [US1] Write failing unit tests for `Logout` in `gitstore-api/tests/unit/resolver/auth_resolvers_test.go` covering: (a) authenticated caller returns `success: true`; (b) anonymous/nil principal returns authentication error; (c) token with empty `TokenID` (Basic Auth session) returns `success: true` as a no-op; run `go test ./tests/unit/resolver/...` and verify they FAIL

### Implementation for User Story 1

- [x] T011 [US1] Implement `Logout` resolver: check `auth.PrincipalFromContext(ctx)` — return `gqlerror` if nil or `AuthMethod == "none"`; call `registry.AuthN().RevokeSession(ctx, principal.TokenID, principal.ExpiresAt)`; handle `ErrNotSupported` and internal errors; return `&model.LogoutPayload{Success: true}` on success in `gitstore-api/internal/graph/resolver/auth.resolvers.go`

**Checkpoint**: User Story 1 is fully functional and independently testable. `make test` passes.

---

## Phase 4: User Story 2 — RefreshToken (Priority: P1)

**Goal**: `refreshToken` mutation reads the raw Bearer token from context, calls `RefreshSession`, returns a new `AuthSession`, and causes the old token to be blacklisted.

**Independent Test**: `login` → `refreshToken` → confirm new session returned with later expiry → use old token → expect `"token has been revoked"` error.

### Tests for User Story 2

> **Write these tests FIRST, verify they FAIL, then implement T013**

- [x] T012 [US2] Write failing unit tests for `RefreshToken` in `gitstore-api/tests/unit/resolver/auth_resolvers_test.go` covering: (a) valid token returns new `AuthSession`; (b) token expired within grace window succeeds; (c) token expired beyond grace window returns error; (d) already-revoked token returns error; (e) no raw token in context (unauthenticated) returns authentication error; run `go test ./tests/unit/resolver/...` and verify they FAIL

### Implementation for User Story 2

- [x] T013 [US2] Implement `RefreshToken` resolver: call `auth.RawTokenFromContext(ctx)` — return `gqlerror` if empty; call `registry.AuthN().RefreshSession(ctx, rawToken)`; handle `ErrNotSupported`, "token too old to refresh", and "token is revoked" errors distinctly; on success build and return `&model.RefreshTokenPayload{Session: &model.AuthSession{Token, ExpiresAt, User}}` — derive `User.Username` from `PrincipalFromContext` and `User.IsAdmin` from `principal.IsAdmin()` in `gitstore-api/internal/graph/resolver/auth.resolvers.go`

**Checkpoint**: User Stories 1 and 2 are both functional and independently testable. `make test` passes.

---

## Phase 5: User Story 3 — Login Migration (Priority: P2)

**Goal**: `login` mutation routes through the `ProviderRegistry` AuthN chain instead of legacy `authMiddleware` stubs. `user.isAdmin` and `user.username` are derived from the real `Principal`, not hardcoded.

**Independent Test**: `login(username:"admin", password:"<correct>")` → `user.isAdmin` is `true`, `user.username` is `"admin"`. `login` with wrong password → authentication error.

### Tests for User Story 3

> **Write these tests FIRST, verify they FAIL, then implement T015**

- [x] T014 [US3] Write failing unit tests for migrated `Login` in `gitstore-api/tests/unit/resolver/auth_resolvers_test.go` covering: (a) valid credentials return `session` with `user.isAdmin == true` and `user.username == "admin"` (not hardcoded); (b) invalid credentials return authentication error; (c) nil/unconfigured registry returns service unavailable error; run `go test ./tests/unit/resolver/...` and verify they FAIL

### Implementation for User Story 3

- [x] T015 [US3] Implement migrated `Login` resolver: build `auth.AuthRequest{Header: http.Header{"Authorization": ["Basic base64(username:password)"]}}` from `input`; call `registry.AuthN().Authenticate(ctx, req)` — return `gqlerror` on `OutcomeDeny` or error; call `registry.AuthN().IssueSession(ctx, principal.Subject)` to mint the token; build and return `LoginPayload` with `User.Username = principal.Subject` and `User.IsAdmin = principal.IsAdmin()`; remove the `r.authMiddleware` code path for this resolver in `gitstore-api/internal/graph/resolver/auth.resolvers.go`

**Checkpoint**: All three user stories are independently functional. `make bootstrap` succeeds end-to-end. `make test` passes.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Observability, documentation, and final validation.

- [x] T016 [P] Add structured `zap` log lines to `logout` (subject, jti, outcome), `refreshToken` (subject, outcome), and migrated `login` (subject, outcome) in `gitstore-api/internal/graph/resolver/auth.resolvers.go`
- [x] T017 [P] Update `docs/implementation/pluggable_auth_architecture.md` to record Phase 3 completion and note the known blacklist-on-restart limitation
- [x] T018 Run `make pr-ready` and confirm all unit and integration tests pass without any configuration changes; smoke-test the full session lifecycle via `quickstart.md` steps

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately
- **Foundational (Phase 2)**: Depends on Phase 1 — **BLOCKS all user stories**
- **US1 Logout (Phase 3)**: Depends on Foundational completion
- **US2 RefreshToken (Phase 4)**: Depends on Foundational completion — independent of US1
- **US3 Login migration (Phase 5)**: Depends on Foundational completion — independent of US1 and US2
- **Polish (Phase 6)**: Depends on all desired user stories complete

### User Story Dependencies

- **US1 (Logout)**: Requires T003 (TokenID on Principal), T009 (registry in resolver)
- **US2 (RefreshToken)**: Requires T004 (ContextWithRawToken), T008 (middleware stores raw token), T009 (registry in resolver)
- **US3 (Login migration)**: Requires T005 (ChainedAuthN.IssueSession), T007 (StaticAdminProvider.IssueSession), T009 (registry in resolver)

### Within Each User Story

1. Tests written and verified FAIL
2. Implementation written to make tests pass
3. Full `go test ./...` run before moving to next story

### Parallel Opportunities

Inside Phase 2, once T003 completes, these run in parallel:
- T004 (context.go), T005 (registry.go), T006 (anonymous), T007 (staticadmin), T009 (resolver wiring)
- T008 runs after T004

User stories US1, US2, US3 can be worked on in parallel after Phase 2 — they touch different sections of `auth.resolvers.go` and have no runtime dependencies on each other.

---

## Parallel Example: Phase 2 after T003

```bash
# Once T003 (types.go changes) is committed, launch concurrently:
Task: T004 — ContextWithRawToken in context.go
Task: T005 — ChainedAuthN.IssueSession in registry.go
Task: T006 — anonymous IssueSession (ErrNotSupported)
Task: T007 — staticadmin IssueSession + grace + TokenID
Task: T009 — registry wiring in resolver.go + server.go

# Then, after T004:
Task: T008 — store raw token in ChainAuthMiddleware
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (T001)
2. Complete Phase 2: Foundational (T002–T009) — CRITICAL
3. Complete Phase 3: US1 Logout (T010–T011)
4. **STOP and VALIDATE**: `login` → `logout` → verify token rejected
5. Ship if logout is the blocking requirement

### Incremental Delivery

1. Phase 1 + Phase 2 → Foundation ready
2. Phase 3 (US1 Logout) → token revocation working → Deploy/Demo
3. Phase 4 (US2 RefreshToken) → session rotation working → Deploy/Demo
4. Phase 5 (US3 Login migration) → isAdmin/username from real principal → Deploy/Demo
5. Phase 6 (Polish) → observability + docs → PR ready

### Single-Developer Sequence

```
T001 → T002 → T003 → T004+T005+T006+T007+T009 (parallel) → T008
→ T010 → T011
→ T012 → T013
→ T014 → T015
→ T016+T017 (parallel) → T018
```

---

## Notes

- `[P]` tasks touch different files and have no dependency on incomplete peers within their phase
- Each user story is independently completable and testable
- Verify tests FAIL before implementing (T002 before T003, T010 before T011, T012 before T013, T014 before T015)
- `auth.resolvers.go` is modified across all three user story phases — write and commit one story at a time to avoid conflicts
- The REST `/api/login` handler (`handler.LoginHandler`) is out of Phase 3 scope; `authMiddleware` remains wired for it
- In-memory blacklist is lost on server restart — this is expected and documented in quickstart.md
