# Tasks: Local Multi-User AuthN + UserDir Provider (`static-users`)

**Input**: Design documents from `/specs/060-local-multiuser-authn/`
**Prerequisites**: plan.md (required), spec.md (required for user stories), data-model.md, contracts/, research.md, quickstart.md

**Tests**: Test-first development is required for the provider, registry/config, and test-harness migration work in this feature.

**Organization**: Tasks are grouped by user story to enable independent implementation and validation.

## Format: `[ID] [P?] [Story] Description with exact file path`

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Establish the new provider package before removing the old one.

- [ ] T001 Create the `gitstore-api/internal/auth/provider/staticusers/` package skeleton (`provider.go`, `users.go`, `errors.go` stubs)
- [ ] T002 [P] Move `jti.go`'s `generateJTI` from `staticadmin/` to `staticusers/` (unchanged content, new package name)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Land the user-list loader, UserDir read methods, the rbac-local helper, and the config-validation fix before the provider swap and harness migration begin.

**Checkpoint**: `users.yaml` can be loaded/validated/reloaded; UserDir reads work; `rbac-local` can report role_binding coverage; config validation is chain-aware. US1-US4 can proceed.

- [ ] T003 [P] Add failing tests for `loadUsers`/`Reload` (missing file, malformed YAML, wrong `version`, duplicate username, empty `password_hash`) in `gitstore-api/internal/auth/provider/staticusers/staticusers_test.go`, including an assertion that each error's message names the configured file path and includes a remediation hint (per FR-013a), not just the wrapped underlying error
- [ ] T004 Implement `UserList`/`UserEntry` (including `display_name`/`email`) and `loadUsers`/`validateUsers` in `gitstore-api/internal/auth/provider/staticusers/users.go` until T003 is green, wrapping the underlying `os.ReadFile`/`yaml.Unmarshal`/schema error with `%w` (per `contracts/static-users-provider.md`'s Config-validation contract section) while prepending the problem/fix/quickstart-pointer structure required by FR-013a
- [ ] T005 [P] Add failing tests for `GetBySubject`/`ListGroups`/`SearchUsers` (known user, unknown user → `ErrUserNotFound`, case-insensitive substring search) in `gitstore-api/internal/auth/provider/staticusers/staticusers_test.go`
- [ ] T006 Implement `ErrUserNotFound` in `errors.go` and the `UserDirProvider` methods in `provider.go` until T005 is green
- [ ] T007 [P] Add a failing test for `RBACLocalProvider.HasAnyRoleBindingFor([]string) bool` in `gitstore-api/internal/auth/provider/rbaclocal/rbaclocal_test.go`, confirming `Authorize`'s existing tests are unaffected
- [ ] T008 Implement `HasAnyRoleBindingFor` as an additive, read-only method in `gitstore-api/internal/auth/provider/rbaclocal/provider.go` until T007 is green
- [ ] T009 [P] Add failing tests for `validateAuthChainConfig` (static-users in chain + empty JWT secret → error; static-users absent + empty JWT secret → no error; empty gRPC HMAC secret → error regardless of chain) in `gitstore-api/internal/config/config_test.go`, including an assertion that the JWT-secret error's message contains the numbered remediation steps and `quickstart.md` pointer required by FR-013a/`contracts/static-users-provider.md`
- [ ] T010 Remove `JWTConfig.Secret`'s `validate:"required"` struct tag and implement `validateAuthChainConfig`, called from `validateConfig`, in `gitstore-api/internal/config/config.go` until T009 is green, using the exact multi-line error format specified in `contracts/static-users-provider.md`'s Config-validation contract section

---

## Phase 3: User Story 1 - An operator configures multiple named local users who can each authenticate with their own credentials (Priority: P1) 🎯 MVP

**Goal**: `static-users` authenticates two or more distinct configured identities via Basic Auth and issues each its own session token, working identically simply for a single-user configuration.

**Independent Test**: Configure two users, log in as each with their own credentials, confirm each token's subject matches only that user; confirm a single-user configuration is no harder than the old single-admin setup.

### Tests for User Story 1

- [ ] T011 [P] [US1] Add failing tests for `StaticUsersProvider.Authenticate` Basic Auth paths (success, unknown username, wrong password, malformed header) in `gitstore-api/internal/auth/provider/staticusers/staticusers_test.go`
- [ ] T012 [P] [US1] Add failing tests for `StaticUsersProvider.IssueSession`/`RevokeSession`/`RefreshSession`/Bearer verification in `gitstore-api/internal/auth/provider/staticusers/staticusers_test.go`

### Implementation for User Story 1

- [ ] T013 [US1] Implement `StaticUsersProvider.Authenticate` (Basic Auth + Bearer verification against `auth.jwt.secret`/`issuer`) in `gitstore-api/internal/auth/provider/staticusers/provider.go` until T011 is green
- [ ] T014 [US1] Implement `StaticUsersProvider.IssueSession`/`RevokeSession`/`RefreshSession` and its own `sessionBlacklist` until T012 is green

**Checkpoint**: A single- or multi-user `users.yaml` configuration works end-to-end for login — independently verifiable via `quickstart.md`'s fresh-setup steps.

---

## Phase 4: User Story 2 - `role_bindings` becomes the sole, load-bearing mechanism for any role (Priority: P1)

**Goal**: Two real `static-users` identities bound to two different roles produce different, correct authorization outcomes; an unbound identity is denied everything under `default_deny`; `Principal.IsAdmin()` is confirmed never authoritative for `static-users` principals.

**Independent Test**: Bind `alice`→`admin` and `bob`→`developer`; authenticate as each; confirm differing outcomes. Authenticate as an unbound third identity; confirm it is denied a `default_deny`-gated action.

### Tests for User Story 2

- [ ] T015 [P] [US2] Add a failing test configuring two `static-users` identities bound to two different roles via `role_bindings` and asserting differing `rbac-local.Authorize` outcomes, plus a third, unbound identity asserting denial under `default_deny`, in `gitstore-api/internal/auth/provider/rbaclocal/rbaclocal_test.go` (zero change to non-test `rbaclocal` source)
- [ ] T016 [P] [US2] Add a failing test asserting `StaticUsersProvider.Authenticate`'s resulting `Principal.Roles` is always empty, and `Principal.IsAdmin()` is always `false`, regardless of any `role_bindings` entry — in `gitstore-api/internal/auth/provider/staticusers/staticusers_test.go`

### Implementation for User Story 2

- [ ] T017 [US2] Confirm T015/T016 pass with zero implementation changes beyond Phase 2/3 (verification checkpoint — if either requires a source change, that is a signal research.md Decisions 2/9 need revisiting)
- [ ] T018 [US2] Update `Principal.IsAdmin()`'s doc comment in `gitstore-api/internal/auth/types.go` per FR-022/contracts/static-users-provider.md

**Checkpoint**: `role_bindings` is proven to be the sole source of any role, including "admin," for real, distinct, credential-authenticated subjects.

---

## Phase 5: User Story 3 - Migration never silently drops administrative access (Priority: P1)

**Goal**: The startup safety check fails fast exactly when it should, and only then; a fully-followed migration retains equivalent access.

**Independent Test**: Follow the migration procedure fully → succeeds with equivalent access. Skip the `role_bindings` step → server refuses to start with an actionable error. Use `allow-all` instead of `rbac-local` → no such check fires.

### Tests for User Story 3

- [ ] T019 [P] [US3] Add a failing test for `buildProviderRegistry` asserting construction fails when `static-users` + `rbac-local` are configured and zero configured usernames have a `role_bindings` entry, in `gitstore-api/internal/app/server_test.go`, including an assertion that the error message contains: the configured usernames, the `policy.yaml` path, the two numbered fix options, and the `quickstart.md` pointer (FR-013a)
- [ ] T020 [P] [US3] Add failing tests for the same function asserting construction succeeds when (a) `authz.provider` is `allow-all` under the same user/policy configuration, and (b) at least one configured username has a `role_bindings` entry

### Implementation for User Story 3

- [ ] T021 [US3] Implement the migration-safety check in `buildProviderRegistry` (`gitstore-api/internal/app/server.go`), calling `HasAnyRoleBindingFor`, using the exact multi-line error format specified in `contracts/static-users-provider.md`'s `buildProviderRegistry` wiring contract section, until T019/T020 are green
- [ ] T022 [US3] Remove `AuthConfig.Admin`/`UserConfig` (type and field) and add `AuthConfig.StaticUsers` in `gitstore-api/internal/config/config.go`; update `auth.authn.chain`'s default (config.go and server.go), defaults/known-keys map, and `MarshalLogObject`
- [ ] T023 [US3] Replace `buildProviderRegistry`'s `case "static-admin":` with `case "static-users":` (including wiring `static-users` as the `UserDirProvider` when active) in `gitstore-api/internal/app/server.go`; extend the existing SIGHUP reload handler

**Checkpoint**: An operator cannot migrate into a silent lockout — verified by test, not merely documented.

---

## Phase 6: `static-admin` removal and dependent-file fixes

**Goal**: Delete the replaced provider and fix every reference the removal breaks or leaves stale.

- [ ] T024 Delete `gitstore-api/internal/auth/provider/staticadmin/` (`provider.go`, `jti.go`, `staticadmin_test.go`) entirely
- [ ] T025 [P] Fix `gitstore-api/cmd/server/main_test.go`'s `staticadmin.New(...)` call to `staticusers.New(...)`
- [ ] T026 [P] Fix `gitstore-api/internal/middleware/security/secure_test.go`'s `newTestRegistry` helper the same way
- [ ] T027 [P] Fix `gitstore-api/internal/graph/resolver/auth_resolvers_test.go`'s `newTestRegistry` helper the same way
- [ ] T028 [P] Relabel the cosmetic `"static-admin"` `AuthMethod` string literals to `"static-users"` in `gitstore-api/internal/middleware/security/graphql_test.go`, `gitstore-api/internal/middleware/security/graphql_file_status_test.go`, `gitstore-api/internal/auth/provider/rbaclocal/rbaclocal_test.go`, `gitstore-api/internal/auth/provider/allowall/allowall_test.go`
- [ ] T029 Confirm `grep -rn "staticadmin\|static-admin" gitstore-api/ --include="*.go"` returns zero matches outside `gitstore-api/gen/` (explicitly out of scope, generated code)

---

## Phase 7: User Story 4 - Retire the `test-user:` backdoor and the harness's `static-admin` bootstrap (Priority: P2)

**Goal**: `tests/integration`'s namespace-isolation coverage authenticates real users through `static-users`, with both the bypass and the now-uncompilable `staticadmin` bootstrap removed.

**Independent Test**: `TestRepositoryAuthorization_TwoUserNamespaceIsolation` and the rest of `namespace_contract_test.go`'s suite pass; `grep -rn "test-user:\|staticadmin\|static-admin" tests/integration/` returns zero matches.

### Tests for User Story 4

- [ ] T030 [US4] Confirm the current test suite's baseline (before migration) — no code change

### Implementation for User Story 4

- [ ] T031 [US4] Remove the standalone `testUserAuthN` type and its embedded-source duplicate from `tests/integration/namespace_contract_test.go`
- [ ] T032 [US4] Replace the file's `staticadmin` import and bootstrap construction (both the standalone Go code and the embedded helper-source string) with a `staticusers.New(...)` call against a test-fixture `users.yaml` per `contracts/backdoor-retirement.md`
- [ ] T033 [US4] Fix `namespaceOwnerAuthZ.Authorize`'s `principal.IsAdmin()` check to recognize the harness's known bootstrap subject directly, since `static-users` never sets `Principal.Roles`
- [ ] T034 [US4] Add a harness helper (e.g. `gqlAsRealUser`) performing a real `login` mutation and returning the resulting access token
- [ ] T035 [US4] Migrate `TestRepositoryAuthorization_TwoUserNamespaceIsolation`, `createNamespaceAsUser`, `createRepositoryAsUser`, `repositoriesAsUser` in `tests/integration/authz_repository_contract_test.go` to use T034's helper instead of `"test-user:<subject>"` strings
- [ ] T036 [US4] Confirm `grep -rn "test-user:\|staticadmin\|static-admin" tests/integration/` returns zero matches and the full `namespace_contract_test.go`/`authz_repository_contract_test.go` suites pass

**Checkpoint**: No test-only unauthenticated-identity bypass, and no reference to the removed provider, remains anywhere in `tests/integration/`.

---

## Phase 8: Polish & Documentation

- [ ] T037 [P] Replace `Makefile`'s `gen-admin-password` target with `hash-static-user-password`; update `bootstrap-token`/`bootstrap-namespace`/`bootstrap-repository` hint text (keep `ADMIN_USERNAME`/`ADMIN_PASSWORD` variable names unchanged)
- [ ] T038 [P] Update `gitstore-api/.env.example` (remove `GITSTORE_AUTH__ADMIN__*` lines, update the chain example, document `GITSTORE_AUTH__STATICUSERS__USERS_FILE`)
- [ ] T039 [P] Add `gitstore-api/users.yaml.example` documenting the schema from `data-model.md`
- [ ] T040 [P] Rewrite `docs/implementation/020-pluggable_auth_architecture.md` §2a (static-users, not static-admin, including UserDir), §5a (config keys), §7 Phase 1 language
- [ ] T041 [P] Update `docs/user-guide.md:380` and `docs/api-reference.md:25`'s `static-admin` example references
- [ ] T042 Flag (via a tracked follow-up issue or dated note, not an edit performed by this spec) that `docs/implementation/021-controller_service_account_auth.md`'s `static-admin`-based "status quo" premise, and `docs/runbooks/production-readiness-testing.md`'s Pattern 4 citation, both need updating once this spec ships
- [ ] T043 Run `make build`, `make test`, `make lint`, `make pr-ready` and confirm all pass
