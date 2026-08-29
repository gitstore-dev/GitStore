# Tasks: Local Multi-User AuthN Provider (`static-users`)

**Input**: Design documents from `/specs/060-local-multiuser-authn/`
**Prerequisites**: plan.md (required), spec.md (required for user stories), data-model.md, contracts/, research.md, quickstart.md

**Tests**: Test-first development is required for the provider, registry, and test-harness migration work in this feature.

**Organization**: Tasks are grouped by user story to enable independent implementation and validation.

## Format: `[ID] [P?] [Story] Description with exact file path`

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Establish the new provider package and confirm the two structural templates this feature draws from.

- [ ] T001 Create the `gitstore-api/internal/auth/provider/staticusers/` package skeleton (`provider.go`, `users.go` stubs)
- [ ] T002 [P] Re-confirm `rbaclocal/policy.go`'s `loadPolicy`/`validatePolicy` shape and `staticadmin/provider.go`'s session-lifecycle shape as the two structural templates (no code yet — a documentation/traceability checkpoint recorded in PR description, per research.md Decisions 2 and 4)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Land the user-list loader and the registry-level session-issuance routing fix before provider-level authentication work begins.

**Checkpoint**: `users.yaml` can be loaded/validated/reloaded, and `ChainedAuthN` can route session issuance to a named provider; US1-US4 can proceed.

- [ ] T003 [P] Add failing tests for `loadUsers`/`Reload` (missing file, malformed YAML, wrong `version`, duplicate username, empty `password_hash`) in `gitstore-api/internal/auth/provider/staticusers/staticusers_test.go`
- [ ] T004 Implement `UserList`/`UserEntry` and `loadUsers`/`validateUsers` in `gitstore-api/internal/auth/provider/staticusers/users.go` until T003 is green
- [ ] T005 [P] Add a failing test for `ChainedAuthN.IssueSessionFor(ctx, principal)` routing to the provider named by `principal.AuthMethod` (not the first `IssueSession`-supporting provider) in `gitstore-api/internal/auth/registry_test.go`
- [ ] T006 Implement `ChainedAuthN.IssueSessionFor` in `gitstore-api/internal/auth/registry.go` until T005 is green

---

## Phase 3: User Story 1 - An operator configures multiple named local users who can each authenticate with their own credentials (Priority: P1) 🎯 MVP

**Goal**: `static-users` authenticates two or more distinct configured identities via Basic Auth and issues each its own session token.

**Independent Test**: Configure two users, log in as each with their own credentials, confirm each token's subject matches only that user.

### Tests for User Story 1

- [ ] T007 [P] [US1] Add failing tests for `StaticUsersProvider.Authenticate` Basic Auth paths (success, unknown username, wrong password, malformed header) in `gitstore-api/internal/auth/provider/staticusers/staticusers_test.go`
- [ ] T008 [P] [US1] Add failing tests for `StaticUsersProvider.IssueSession`/`RevokeSession`/`RefreshSession` (mirroring `staticadmin_test.go`'s session-lifecycle coverage, own blacklist instance) in `gitstore-api/internal/auth/provider/staticusers/staticusers_test.go`
- [ ] T009 [P] [US1] Add a failing config-validation test asserting server construction fails when `static-users` is listed in `auth.authn.chain` but `auth.staticusers.jwt.secret` is empty, in `gitstore-api/internal/config/config_test.go`

### Implementation for User Story 1

- [ ] T010 [US1] Implement `StaticUsersProvider.Authenticate` (Basic Auth + Bearer verification against its own secret/issuer) in `gitstore-api/internal/auth/provider/staticusers/provider.go` until T007 is green
- [ ] T011 [US1] Implement `StaticUsersProvider.IssueSession`/`RevokeSession`/`RefreshSession` and its own `sessionBlacklist` in `gitstore-api/internal/auth/provider/staticusers/provider.go` until T008 is green
- [ ] T012 [US1] Add `StaticUsersConfig`/`auth.staticusers.*` Viper keys, defaults, and known-keys entries in `gitstore-api/internal/config/config.go` until T009 is green
- [ ] T013 [US1] Add the `case "static-users":` dispatch in `buildProviderRegistry` in `gitstore-api/internal/app/server.go`, including shutdown registration for the blacklist-pruning goroutine
- [ ] T014 [US1] Extend the existing SIGHUP reload handler in `gitstore-api/internal/app/server.go`'s `Start()` to also reload any active `static-users` provider

**Checkpoint**: An operator can configure `users.yaml`, chain in `static-users`, and log in as multiple distinct users — independently verifiable via `quickstart.md`'s manual steps 1-6.

---

## Phase 4: User Story 2 - `rbac-local`'s existing multi-subject `role_bindings` become exercisable end-to-end (Priority: P1)

**Goal**: Two real `static-users` identities bound to two different roles produce different, correct authorization outcomes, with zero `rbac-local` source changes.

**Independent Test**: Bind `alice`→`namespace-owner` and `bob`→`developer` in `policy.yaml`'s `role_bindings`; authenticate as each; confirm differing authorization outcomes for a role-differentiated action.

### Tests for User Story 2

- [ ] T015 [P] [US2] Add a failing test configuring two `static-users` identities bound to two different roles via `role_bindings` and asserting differing `rbac-local.Authorize` outcomes for a role-differentiated action, in `gitstore-api/internal/auth/provider/rbaclocal/rbaclocal_test.go` (new test function; no change to `rbaclocal`'s non-test source)

### Implementation for User Story 2

- [ ] T016 [US2] Confirm T015 passes with zero changes to `gitstore-api/internal/auth/provider/rbaclocal/provider.go` or `policy.go` (this task is a verification checkpoint per research.md Decision 5, not an implementation task — if it requires a source change, that is a signal the research finding needs revisiting before proceeding)

**Checkpoint**: `role_bindings` is proven to work end-to-end with real, distinct, credential-authenticated subjects for the first time.

---

## Phase 5: User Story 3 - A `static-users`-issued token can never be misinterpreted as a `static-admin` identity, or vice versa (Priority: P1)

**Goal**: Cross-provider token verification and session issuance are provably safe by construction.

**Independent Test**: A `static-users` token is rejected by `static-admin`'s verifier and vice versa; a `static-users` login always issues a `static-users`-signed token regardless of chain order.

### Tests for User Story 3

- [ ] T017 [P] [US3] Add a failing test asserting a `static-users`-issued bearer token is rejected (`Challenge`, not `Allow`) by a real `StaticAdminProvider.Authenticate` call, in `gitstore-api/internal/auth/provider/staticadmin/staticadmin_test.go` (new test function only; no change to `staticadmin`'s non-test source)
- [ ] T018 [P] [US3] Add a failing test asserting a `static-admin`-issued bearer token is rejected by a real `StaticUsersProvider.Authenticate` call, in `gitstore-api/internal/auth/provider/staticusers/staticusers_test.go`
- [ ] T019 [P] [US3] Add a failing `Login` resolver test asserting a `static-users` login issues a `static-users`-signed token even when `static-admin` is listed earlier in `auth.authn.chain`, in `gitstore-api/internal/graph/resolver/auth_resolvers_test.go`

### Implementation for User Story 3

- [ ] T020 [US3] Confirm T017/T018 pass given T010-T011's distinct-secret implementation (verification checkpoint — no new source change expected beyond Phase 3, per research.md Decision 4's shared-secret-risk closure)
- [ ] T021 [US3] Update `Login` in `gitstore-api/internal/graph/resolver/auth_service.go` to call `r.registry.AuthN().IssueSessionFor(ctx, principal)` instead of `IssueSession(ctx, principal.Subject)`, until T019 is green

**Checkpoint**: Adding `static-users` to the chain is proven, by test, not to create a privilege-escalation path in either direction.

---

## Phase 6: User Story 4 - The `test-user:` backdoor is retired in favor of real logins (Priority: P2)

**Goal**: `tests/integration`'s namespace-isolation coverage authenticates two real users through `static-users`, with the bypass mechanism removed entirely.

**Independent Test**: `TestRepositoryAuthorization_TwoUserNamespaceIsolation` passes using real logins; `grep -rn "test-user:" tests/integration/` returns zero matches.

### Tests for User Story 4

- [ ] T022 [US4] Confirm `TestRepositoryAuthorization_TwoUserNamespaceIsolation` and its helpers currently pass (baseline, before migration) — no code change

### Implementation for User Story 4

- [ ] T023 [US4] Remove the standalone `testUserAuthN` type (lines 194, 215-240) from `tests/integration/namespace_contract_test.go`
- [ ] T024 [US4] Replace the embedded helper-source string's `ProviderRegistry` wiring in `tests/integration/namespace_contract_test.go` with a real `staticusers.New(...)` call configured against a test-fixture `users.yaml` (`alice`/`bob`, throwaway bcrypt hashes) per `contracts/backdoor-retirement.md`, leaving `namespaceOwnerAuthZ` and the `/__test/resource-body` route untouched
- [ ] T025 [US4] Add a harness helper (e.g. `gqlAsRealUser`) in `tests/integration/namespace_contract_test.go` that performs a real `login` mutation for a given username/password and returns the resulting access token
- [ ] T026 [US4] Migrate `TestRepositoryAuthorization_TwoUserNamespaceIsolation`, `createNamespaceAsUser`, `createRepositoryAsUser`, `repositoriesAsUser` in `tests/integration/authz_repository_contract_test.go` to use T025's helper instead of `"test-user:<subject>"` strings
- [ ] T027 [US4] Confirm `grep -rn "test-user:" tests/integration/` returns zero matches and `TestRepositoryAuthorization_TwoUserNamespaceIsolation` passes with equivalent assertions

**Checkpoint**: No test-only unauthenticated-identity bypass remains anywhere in `tests/integration/`.

---

## Phase 7: Polish & Documentation

- [ ] T028 [P] Add a `static-users` subsection to `docs/implementation/020-pluggable_auth_architecture.md` §2 (sibling to §2a `static-admin`), the new Viper keys to §5a, and a new Rollout Phase entry to §7
- [ ] T029 [P] Add `gitstore-api/users.yaml.example` documenting the schema from `data-model.md`
- [ ] T030 Flag (via a tracked follow-up issue or a dated note, not an edit performed by this spec) that `docs/runbooks/production-readiness-testing.md` Pattern 4's worked-example citation should be updated to point at the migrated real-login test once T026 lands
- [ ] T031 Run `make build`, `make test`, `make lint`, `make pr-ready` and confirm all pass
