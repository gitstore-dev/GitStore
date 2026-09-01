# Tasks: Local Multi-User AuthN + UserDir Provider (`static-users`)

**Input**: Design documents from `/specs/060-local-multiuser-authn/`
**Prerequisites**: plan.md (required), spec.md (required for user stories), data-model.md, contracts/, research.md, quickstart.md

**Tests**: Test-first development is required for the provider, registry/config, and test-harness migration work in this feature.

**Organization**: Tasks are grouped by user story to enable independent implementation and validation.

> ⚠️ **Ordering gate before implementation begins** (raised on PR #405, 2026-08-29): spec 061 (`061-controller-serviceaccount-auth`, PR #409) SHOULD land before, or in the same release window as, this spec. Removing `static-admin` leaves `gitstore-controller-manager` with no non-human credential path, and its only remaining local option would be a human-shaped `static-users` entry — the exact anti-pattern spec 061 exists to eliminate. Nothing here breaks at compile time (`graphqlclient.Client` and `make bootstrap-token` are provider-agnostic), so this is a release-sequencing and documentation gate, not a code dependency. See spec.md's Dependencies section (DEP-001/002/003), and T042/T042a.

## Format: `[ID] [P?] [Story] Description with exact file path`

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Establish the new provider package before removing the old one.

- [X] T001 Create the `gitstore-api/internal/auth/provider/staticusers/` package skeleton (`provider.go`, `users.go`, `errors.go` stubs)
- [X] T002 [P] Move `jti.go`'s `generateJTI` from `staticadmin/` to `staticusers/` (unchanged content, new package name)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Land the user-list loader, UserDir read methods, the rbac-local helper, and the config-validation fix before the provider swap and harness migration begin.

**Checkpoint**: `users.yaml` can be loaded/validated/reloaded; UserDir reads work; `rbac-local` can report role_binding coverage; config validation is chain-aware. US1-US4 can proceed.

- [X] T003 [P] Add tests for `loadUsers`/`Reload` (missing file, malformed YAML, wrong `version`, duplicate username, empty `password_hash`) in `gitstore-api/internal/auth/provider/staticusers/provider_test.go`, including configured-file remediation errors
- [X] T004 Implement `UserList`/`UserEntry` (including `display_name`/`email`) and `loadUsers`/`validateUsers` in `gitstore-api/internal/auth/provider/staticusers/users.go` until T003 is green, wrapping the underlying `os.ReadFile`/`yaml.Unmarshal`/schema error with `%w` (per `contracts/static-users-provider.md`'s Config-validation contract section) while prepending the problem/fix/quickstart-pointer structure required by FR-013a
- [X] T005 [P] Add tests for `GetBySubject`/`ListGroups`/`SearchUsers` in `gitstore-api/internal/auth/provider/staticusers/provider_test.go`
- [X] T006 Implement `ErrUserNotFound` in `errors.go` and the `UserDirProvider` methods in `provider.go` until T005 is green
- [X] T007 [P] Add coverage for `RBACLocalProvider.HasAnyRoleBindingFor([]string) bool` in `gitstore-api/internal/auth/provider/rbaclocal/rbaclocal_test.go`
- [X] T008 Implement `HasAnyRoleBindingFor` as an additive, read-only method in `gitstore-api/internal/auth/provider/rbaclocal/provider.go` until T007 is green
- [X] T009 [P] Add coverage for chain-aware JWT/HMAC validation and remediation errors in `gitstore-api/internal/config/config_test.go`
- [X] T010 Remove `JWTConfig.Secret`'s `validate:"required"` struct tag and implement `validateAuthChainConfig`, called from `validateConfig`, in `gitstore-api/internal/config/config.go` until T009 is green, using the exact multi-line error format specified in `contracts/static-users-provider.md`'s Config-validation contract section

---

## Phase 3: User Story 1 - An operator configures multiple named local users who can each authenticate with their own credentials (Priority: P1) 🎯 MVP

**Goal**: `static-users` authenticates two or more distinct configured identities via Basic Auth and issues each its own session token, working identically simply for a single-user configuration.

**Independent Test**: Configure two users, log in as each with their own credentials, confirm each token's subject matches only that user; confirm a single-user configuration is no harder than the old single-admin setup.

### Tests for User Story 1

- [X] T011 [P] [US1] Add tests for `StaticUsersProvider.Authenticate` Basic Auth paths in `gitstore-api/internal/auth/provider/staticusers/provider_test.go`
- [X] T012 [P] [US1] Add tests for session lifecycle and Bearer verification in `gitstore-api/internal/auth/provider/staticusers/provider_test.go`

### Implementation for User Story 1

- [X] T013 [US1] Implement `StaticUsersProvider.Authenticate` (Basic Auth + Bearer verification against `auth.jwt.secret`/`issuer`) in `gitstore-api/internal/auth/provider/staticusers/provider.go` until T011 is green
- [X] T014 [US1] Implement `StaticUsersProvider.IssueSession`/`RevokeSession`/`RefreshSession` and its own `sessionBlacklist` until T012 is green

**Checkpoint**: A single- or multi-user `users.yaml` configuration works end-to-end for login — independently verifiable via `quickstart.md`'s fresh-setup steps.

---

## Phase 4: User Story 2 - `role_bindings` becomes the sole, load-bearing mechanism for any role (Priority: P1)

**Goal**: Two real `static-users` identities bound to two different roles produce different, correct authorization outcomes; an unbound identity is denied everything under `default_deny`; `Principal.IsAdmin()` is confirmed never authoritative for `static-users` principals.

**Independent Test**: Bind `alice`→`admin` and `bob`→`developer`; authenticate as each; confirm differing outcomes. Authenticate as an unbound third identity; confirm it is denied a `default_deny`-gated action.

### Tests for User Story 2

- [X] T015 [P] [US2] Cover role-binding authorization outcomes in `gitstore-api/internal/auth/provider/rbaclocal/rbaclocal_test.go`
- [X] T016 [P] [US2] Cover role-free `static-users` principals and `Principal.IsAdmin()` semantics in provider tests

### Implementation for User Story 2

- [X] T017 [US2] Confirm role-binding tests pass without changing the AuthN/AuthZ separation
- [X] T018 [US2] Update `Principal.IsAdmin()`'s doc comment in `gitstore-api/internal/auth/types.go`

**Checkpoint**: `role_bindings` is proven to be the sole source of any role, including "admin," for real, distinct, credential-authenticated subjects.

---

## Phase 5: User Story 3 - Migration never silently drops administrative access (Priority: P1)

**Goal**: The startup safety check fails fast exactly when it should, and only then; a fully-followed migration retains equivalent access.

**Independent Test**: Follow the migration procedure fully → succeeds with equivalent access. Skip the `role_bindings` step → server refuses to start with an actionable error. Use `allow-all` instead of `rbac-local` → no such check fires.

### Tests for User Story 3

- [ ] T019 [P] [US3] Add a failing test for `buildProviderRegistry` asserting construction fails when `static-users` + `rbac-local` are configured and zero configured usernames have a `role_bindings` entry, in `gitstore-api/internal/app/server_test.go`, including an assertion that the error message contains: the configured usernames, the `policy.yaml` path, the two numbered fix options, and the `quickstart.md` pointer (FR-013a)
- [ ] T020 [P] [US3] Add failing tests for the same function asserting construction succeeds when (a) `authz.provider` is `allow-all` under the same user/policy configuration, and (b) at least one configured username has a `role_bindings` entry

### Implementation for User Story 3

- [X] T021 [US3] Implement the migration-safety check in `buildProviderRegistry` (`gitstore-api/internal/app/server.go`), calling `HasAnyRoleBindingFor`, using the exact multi-line error format specified in `contracts/static-users-provider.md`'s `buildProviderRegistry` wiring contract section, until T019/T020 are green
- [ ] T022 [US3] Remove `AuthConfig.Admin`/`UserConfig` (type and field) and add `AuthConfig.StaticUsers` in `gitstore-api/internal/config/config.go`; update `auth.authn.chain`'s default (config.go and server.go), defaults/known-keys map, and `MarshalLogObject`
- [X] T023 [US3] Replace `buildProviderRegistry`'s `case "static-admin":` with `case "static-users":` in `gitstore-api/internal/app/server.go`; add `static-users` as a valid `auth.userdir.provider` selector and pass the already-constructed instance to UserDir only when that selector is chosen; extend the existing SIGHUP reload handler

**Checkpoint**: An operator cannot migrate into a silent lockout — verified by test, not merely documented.

---

## Phase 6: `static-admin` removal and dependent-file fixes

**Goal**: Delete the replaced provider and fix every reference the removal breaks or leaves stale.

- [X] T024 Delete `gitstore-api/internal/auth/provider/staticadmin/` entirely
- [X] T025 [P] Migrate `gitstore-api/cmd/server/main_test.go` to `staticusers.New(...)`
- [X] T026 [P] Migrate the security test registry to `staticusers.New(...)`
- [X] T027 [P] Migrate the resolver test registry to `staticusers.New(...)`
- [X] T028 [P] Relabel cosmetic AuthMethod fixtures to `static-users`
- [X] T029 Confirm no non-generated Go source references the removed provider

---

## Phase 7: User Story 4 - Retire the `test-user:` backdoor and the harness's `static-admin` bootstrap (Priority: P2)

**Goal**: `tests/integration`'s namespace-isolation coverage authenticates real users through `static-users`, with both the bypass and the now-uncompilable `staticadmin` bootstrap removed.

**Independent Test**: `TestRepositoryAuthorization_TwoUserNamespaceIsolation` and the rest of `namespace_contract_test.go`'s suite pass; `grep -rn "test-user:\|staticadmin\|static-admin" tests/integration/` returns zero matches.

### Tests for User Story 4

- [X] T030 [US4] Confirm the current test suite's baseline

### Implementation for User Story 4

- [X] T031 [US4] Remove the standalone `testUserAuthN` type and embedded duplicate
- [X] T032 [US4] Migrate harness bootstrap construction to `staticusers.New(...)` with a test users file
- [X] T033 [US4] Make bootstrap authorization recognize the known subject explicitly
- [X] T034 [US4] Add a real-login harness helper
- [X] T035 [US4] Migrate repository authorization coverage to real user tokens
- [X] T036 [US4] Confirm the integration harness has no backdoor or removed-provider references and its suites pass

**Checkpoint**: No test-only unauthenticated-identity bypass, and no reference to the removed provider, remains anywhere in `tests/integration/`.

---

## Phase 8: Polish & Documentation

- [X] T037 [P] Replace the password helper with `hash-static-user-password` and update bootstrap hints
- [X] T038 [P] Update `gitstore-api/.env.example` for static-users configuration
- [X] T038a **[BLOCKING — local Compose profile fails to start without this]** Update `config/config.toml` (committed by #410, mounted read-only into `git-service`/`api`/`controller-manager` by `compose.local.yml`): change `[auth.authn] chain = ["static-admin", "anonymous"]` to the `static-users` chain, add `[auth.userdir] provider = "static-users"`, and replace the `[auth.admin]` section (`username`, `password_hash`) with the `static-users` users-file key. Deleting the provider without editing this file makes `validateAuthChainConfig` reject the committed dev config, so `docker compose --profile local` fails at startup for every developer. This file did not exist when this spec was written
- [X] T038b Add `config/users.yaml` as a tracked development-only fixture mirroring `config/policy.yaml`'s existing precedent and its `# DEVELOPMENT ONLY` header, with a placeholder bcrypt hash for the documented dev password, and mount it read-only into the `api` service in `compose.local.yml` only. The file must not be mounted into `git-service` or `controller-manager`, because the current shared `config/config.toml` is consumed by all three services but user credentials are API-only. Add a `!config/users.yaml` negation to `.gitignore` alongside the `!config/policy.yaml` one, so the unanchored `users.yaml` rule does not shadow it. **Decision to confirm before implementing**: this tracks a dev-only credential fixture in Git, which is the same posture `config/config.toml` already takes (it ships `auth.admin.password_hash` for `admin123`) — if that is not acceptable, the alternative is generating the fixture at compose-up time instead, and this task changes shape
- [X] T038c Update `scripts/check-local-compose-config.sh` for the API users-file mount
- [X] T038d Preserve the CI/build-context guard excluding operator policy and users files
- [X] T039 [P] Add `gitstore-api/users.yaml.example` documenting the schema from `data-model.md`, and `gitstore-api/policy.yaml.example` documenting `rbac-local`'s existing schema including the `role_bindings` entries this spec makes load-bearing (both delivered; real `users.yaml`/`policy.yaml` added to `.gitignore`, all `*.example` added to `.dockerignore`, so operator config can never be committed or baked into an image)
- [X] T040 [P] Rewrite the current architecture sections for static-users, UserDir, configuration, and Phase 1
- [X] T041 [P] Update user-guide and API-reference static-users examples
- [X] T042 Confirmed the implementation is independently rebased on `main`; spec 061 remains a separate follow-up and is not pulled into this branch. Its service-account path remains the required controller migration before production rollout.
- [X] T042a Document the post-migration controller authentication path as spec 061's `serviceaccount-jwt`; the migration guide explicitly forbids creating a human-shaped static user for the controller.
- [X] T042b Updated the production-readiness Pattern 4 note to describe the real-login-based test identities.
- [X] T043 Run the repository build/test/lint/PR-readiness gates and confirm all pass
