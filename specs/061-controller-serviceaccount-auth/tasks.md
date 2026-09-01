# Tasks: Controller-Manager Service-Account Authentication (Phase 1)

**Input**: Design documents from `/specs/061-controller-serviceaccount-auth/`
**Prerequisites**: plan.md (required), spec.md (required for user stories), data-model.md, contracts/, research.md, quickstart.md

**Tests**: Test-first development is required for the datastore, provider, resolver, and controller-side credential work in this feature.

**Organization**: Tasks are grouped by user story to enable independent implementation and validation. Phase 3 (User Stories 1-3) is this spec's MVP — sufficient on its own to satisfy the "unblock spec 060" requirement per spec.md.

## Format: `[ID] [P?] [Story] Description with exact file path`

## Phase 1: Setup (Shared Infrastructure)

- [ ] T001 Confirm `docs/implementation/021-controller_service_account_auth.md`'s §8/§9 interface and claim sketches remain the authoritative reference; open (or link) a tracking GitHub issue for this spec's scope (spec.md Assumptions)
- [ ] T002 [P] Add failing test scaffolding directories: `gitstore-api/internal/auth/provider/serviceaccountassertion/`, `gitstore-api/internal/auth/provider/serviceaccountjwt/`, `gitstore-controller-manager/internal/graphqlclient/credential_test.go`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Land the persistent `ServiceAccount` datastore entity before any provider or resolver work begins.

**Checkpoint**: The datastore contract is in place; US1-US3 can proceed.

- [ ] T003 Add the `ServiceAccount`/`ServiceAccountPublicKey` structs to `gitstore-api/internal/datastore/entities.go`
- [ ] T004 Add `CreateServiceAccount`/`GetServiceAccountByUID`/`GetServiceAccountBySubject`/`ListServiceAccounts`/`UpdateServiceAccountKeys`/`SetServiceAccountDisabled`/`DeleteServiceAccount` to the `Datastore` interface in `gitstore-api/internal/datastore/datastore.go`
- [ ] T005 [P] Add failing contract tests for all `ServiceAccount` datastore methods against the memdb backend in `gitstore-api/internal/datastore/memdb/service_account_test.go`; implement the methods in `gitstore-api/internal/datastore/memdb/backend.go` until green
- [ ] T006 [P] Add failing contract tests for all `ServiceAccount` datastore methods against the Scylla backend (mirroring `scylla/file.go`'s test shape); implement `gitstore-api/internal/datastore/scylla/serviceaccount.go` and `gitstore-api/internal/datastore/scylla/migrations/007_service_account.cql` until green. **Before creating the `.cql` file, re-verify the number is still free across in-flight sibling branches, not just `main`** — this spec has already had to move from `005` (taken by spec 047, landed) to `006` (reserved by spec 050, still in flight). `main` alone will misleadingly show `006` as available. See research.md's reservation table
- [ ] T006a [P] Add a failing schema-contract test (extending `gitstore-api/internal/datastore/scylla/migration_test.go`, alongside the existing `TestRunMigrations_HasNoRepositorySecondaryIndexes`) asserting that (a) the `service_accounts_*` tables create **no** secondary index, and (b) their columns use the canonical envelope names and types — `creation_timestamp`/`update_timestamp`/`deletion_timestamp`, `creation_actor`/`update_actor`, `generation bigint`, `resource_version text`, and `uid` typed `uuid` rather than `text`. The existing index test is scoped to `repositories`/`mappings` names only, so it would not have caught a `service_accounts` index; extend the assertion rather than relying on it
- [ ] T007 [P] Add `AuthConfig.ServiceAccount ServiceAccountConfig` (issuer/audience/assertion_audience/signing_key/default_ttl/max_ttl/clock_skew) to `gitstore-api/internal/config/config.go`, with `signing_key` required only when a service-account provider is chained in (mirroring spec 060's `validateAuthChainConfig` conditional-requirement pattern, not `validate:"required"`). Register every new `auth.serviceaccount.*` key in `load()`'s `knownKeys` map and give each a `v.SetDefault(...)` — spec 047 / PR #370 added a strict-schema sweep that logs an `unknown configuration key` warning for any key absent from that map. Note #410 refactored `Load()` into `load(path)` + `LoadFrom(path)` and added a `sharedServiceKey` prefix allowlist; `auth.*` is **not** in that allowlist, so the `knownKeys` registration is mandatory, not optional
- [ ] T007a [P] Enforce FR-015c: `auth.serviceaccount.signing_key` MUST NOT be resolvable from a config file shared across services. Add a startup check (or an explicit documented refusal) covering #410's `compose.local.yml` topology, where one `config/config.toml` is mounted read-only into `git-service`, `api`, and `controller-manager` alike. Add a test asserting the API refuses to start with a service-account provider chained in and its signing key sourced from the shared file, with a remediation message naming the per-service mount to create instead

---

## Phase 3: User Story 1 - An operator can mint a working, non-human controller credential without `static-admin` or `static-users` existing (Priority: P1) 🎯 MVP (part 1 of 3)

**Goal**: `serviceaccount-assertion` and `serviceaccount-jwt` exist, are opt-in, and together let a caller exchange proof-of-possession for a usable access token.

**Independent Test**: Register a `ServiceAccount` (User Story 2's mutations, implemented alongside), sign a client assertion outside the codebase, call `issueServiceAccountToken`, and use the resulting access token to authenticate a GraphQL request — with zero `static-admin`/`static-users` identity configured.

### Tests for User Story 1

- [ ] T008 [P] [US1] Add failing unit tests for `serviceaccountassertion.Authenticate` (typ/kid/aud/exp/jti claim checks, `OutcomeChallenge` on wrong `typ`, `OutcomeDeny` on bad signature/replay/disabled account) in `gitstore-api/internal/auth/provider/serviceaccountassertion/provider_test.go`
- [ ] T009 [P] [US1] Add failing unit tests for `serviceaccountjwt.Authenticate` (iss/aud/exp/sa_uid claim checks, multi-key overlap-window verification, empty `Roles`, `OutcomeChallenge` vs `OutcomeDeny`) in `gitstore-api/internal/auth/provider/serviceaccountjwt/provider_test.go`

### Implementation for User Story 1

- [ ] T010 [US1] Implement `serviceaccountassertion.New`/`Authenticate`/replay cache in `gitstore-api/internal/auth/provider/serviceaccountassertion/provider.go` and `replay.go` until T008 is green
- [ ] T011 [US1] Implement `serviceaccountjwt.New`/`Authenticate`/issuer signing helper/`kid`-based key set in `gitstore-api/internal/auth/provider/serviceaccountjwt/provider.go` and `keys.go` until T009 is green
- [ ] T012 [US1] Wire both providers into `buildProviderRegistry`'s `switch` in `gitstore-api/internal/app/server.go`; confirm the default chain and startup behavior are unchanged when neither is listed
- [ ] T013 [US1] Add `gitstore_api_authn_requests_total{provider,outcome}` metric and confirm `DecisionLogger` fields render correctly for `serviceaccount:<namespace>:<name>` subjects

**Checkpoint**: `serviceaccount-jwt`/`serviceaccount-assertion` exist and verify tokens correctly; not yet usable end-to-end without User Story 2's issuance mutation.

---

## Phase 4: User Story 2 - An administrator can create, enroll keys for, rotate keys for, and delete ServiceAccounts (Priority: P1) 🎯 MVP (part 2 of 3)

**Goal**: The administrative CRUD surface and the assertion-gated issuance mutation exist, making User Story 1 actually reachable end-to-end.

**Independent Test**: `createServiceAccount` → `issueServiceAccountToken` succeeds; `rotateServiceAccountKey` add+remove correctly shifts which key's assertions are accepted; `deleteServiceAccount` blocks all further issuance/authentication for that subject.

### Tests for User Story 2

- [ ] T014 [P] [US2] Add failing resolver tests for `createServiceAccount` (success, duplicate rejection, zero-key rejection, authorization denial) in `gitstore-api/internal/graph/resolver/serviceaccount_service_test.go`
- [ ] T015 [P] [US2] Add failing resolver tests for `rotateServiceAccountKey` (add+remove overlap window, empty-result-after-removal rejection, authorization denial) in the same test file
- [ ] T016 [P] [US2] Add failing resolver tests for `deleteServiceAccount` (idempotent delete, subsequent-auth denial, authorization denial) in the same test file
- [ ] T017 [P] [US2] Add failing resolver tests for `issueServiceAccountToken` (success, wrong-subject/UID-mismatch denial, TTL clamping to `max_ttl`) in the same test file

### Implementation for User Story 2

- [ ] T018 [US2] Add `shared/schemas/serviceaccount.graphqls` per `contracts/serviceaccount-mutations.md`; regenerate gqlgen code (never hand-edited)
- [ ] T019 [US2] Implement `createServiceAccount`/`rotateServiceAccountKey`/`deleteServiceAccount`/`issueServiceAccountToken` resolvers in `gitstore-api/internal/graph/resolver/serviceaccount.resolvers.go` until T014-T017 are green
- [ ] T020 [US2] Add the `issueServiceAccountToken`-specific subject/UID field-level gate to `GraphQLFieldAuthorizer` in `gitstore-api/internal/middleware/security/graphql.go`, and add `serviceaccount.create`/`serviceaccount.key.rotate`/`serviceaccount.delete` action-string gating for the three CRUD mutations
- [ ] T021 [US2] Add a documented (not default) `gitstore-controller-manager` role and `serviceaccount:controllers:gitstore-controller-manager` `role_bindings` example to `gitstore-api/policy.yaml.example`, matching doc 021 §10b's corrected union role — it MUST include `repository.create.any`, `namespace.status.write`, and `namespace.watch` (added on the spec 050/#371 rebase) alongside the CategoryTaxonomy actions, and MUST carry §10b's inline comment explaining why `.own` is unreachable for a machine subject
- [ ] T021a [US2] Update `config/policy.yaml` (the dev policy committed by #410, extended by #371 with `namespace.watch`, mounted into `api`): replace its existing `controller` role — currently `*.status.write`/`category.status.write`/`namespace.status.write`/`namespace.watch`, missing `repository.create.any` — with the union role from T021, and re-key its `role_bindings` entry from the bare subject `controller` to `serviceaccount:controllers:gitstore-controller-manager`. Keep the `# DEVELOPMENT ONLY` header

**Checkpoint**: User Stories 1-2 together satisfy SC-001/SC-002 — an operator can mint a working credential with zero `static-admin`/`static-users` configured.

---

## Phase 5: User Story 3 - A controller's token carries least privilege, never `admin` (Priority: P1) 🎯 MVP (part 3 of 3)

**Goal**: Confirm, with tests, that authorization for `serviceaccount-jwt` principals flows entirely through unmodified `rbac-local` `role_bindings`, with no `Roles` ever set on the principal.

**Independent Test**: Bind two service accounts to two different roles; confirm differing authorization outcomes; confirm an unbound service account is denied everything under `default_deny`.

### Tests for User Story 3

- [ ] T022 [P] [US3] Add a failing integration test binding `serviceaccount:controllers:gitstore-controller-manager` to a role permitting only `category.status.write` and confirming an admin-only action is denied, in `tests/integration/serviceaccount_auth_test.go`
- [ ] T022a [P] [US3] Add a failing integration test covering the **namespace reconciler's** actions under the same binding (spec.md US3 scenarios 5-7, added on the spec 047 and spec 050 rebases): (a) provisioning a system repository into a namespace whose `CreationActor` is a *human* user succeeds — proving the role grants `repository.create.any`, the suffix `authorizeRepositoryTenant` actually derives for a machine subject, and that a `repository.create.own`-only role would fail here; (b) `completeNamespaceDeletion` succeeds via `namespace.status.write`; (c) `deleteNamespace` and `repository.delete.any` are denied; (d) the `rbac-local` authorization decision for `namespace.watch` is allowed for this binding and denied for a role lacking it — checked at the `AuthZProvider`/resolver level over an ordinary authenticated HTTP request, **not** by opening a live WebSocket subscription (that requires `transport.Websocket.InitFunc`, added only in US6/T039, a later independent phase; US3 MUST NOT depend on it)
- [ ] T023 [P] [US3] Add a failing unit test asserting `serviceaccountjwt.Authenticate`'s returned `Principal.Roles` is always empty, regardless of any `role_bindings` entry, in `gitstore-api/internal/auth/provider/serviceaccountjwt/provider_test.go`

### Implementation for User Story 3

- [ ] T024 [US3] Confirm (no code change expected — `rbac-local` requires none, per research.md Decision re-confirming spec 060's identical finding) that `RBACLocalProvider.Authorize`'s existing subject-keyed `role_bindings` merge handles `serviceaccount:...` subjects correctly; if any gap is found, fix only `rbac-local`'s handling of the *subject string format*, never its decision semantics (FR-021)
- [ ] T024a [US3] Verify no `authorizeRepositoryTenant` change is needed to make T022a pass — the `.own`/`.any` derivation is expected to work unmodified for machine subjects, resolving to `.any`. If a change *does* appear necessary, stop: narrowing `repository.create.any` for machine subjects is an `rbac-local`/authorization-semantics change that FR-021 forbids in this spec, and MUST be raised as a follow-on spec rather than absorbed here
- [ ] T025 [US3] Run T022-T023 to green; run full existing `rbaclocal` test suite to confirm zero regressions (post-design-gate requirement)

**Checkpoint**: User Stories 1-3 (this spec's full P1/MVP scope) are complete and independently verifiable. Spec 060 is now unblocked per spec.md's Success Criteria SC-001/SC-002/SC-003/SC-005.

---

## Phase 6: User Story 4 - The controller-manager acquires and renews its own credential automatically (Priority: P2)

**Goal**: Remove the manual token-refresh burden; recover automatically after extended downtime.

**Independent Test**: Start the controller-manager with only a `ServiceAccount` signer configured (no `GITSTORE_CONTROLLER__API_TOKEN`); confirm readiness with no administrator action; stop past token expiry; confirm recovery on restart.

### Tests for User Story 4

- [ ] T026 [P] [US4] Add failing unit tests for `StaticToken` (unchanged, always returns the configured string) in `gitstore-controller-manager/internal/graphqlclient/credential_test.go`
- [ ] T027 [P] [US4] Add failing unit tests for `ServiceAccountSource` (sign+exchange on first `Current`, cache reuse before expiry, proactive renewal before expiry, singleflight under concurrent callers, jittered backoff on exchange failure) in the same test file

### Implementation for User Story 4

- [ ] T028 [US4] Implement `Credential`/`CredentialSource`/`StaticToken`/`ServiceAccountSource` in `gitstore-controller-manager/internal/graphqlclient/credential.go` until T026-T027 are green
- [ ] T029 [US4] Rewire `Client.token string` → `Client.credentials CredentialSource` in `gitstore-controller-manager/internal/graphqlclient/client.go`'s `do()`/`Subscribe()`
- [ ] T029a [US4] Add a minimal bootstrap-tier `SecretResolver` (ADR 0009 §3) in `gitstore-controller-manager/internal/secret/`: the `Ref`/`BootstrapProviderConfig` types, a `file` provider, and an `env` provider, with ADR 0001's error classes (`InvalidRef`/`NotFound`/`MissingKey`/`Forbidden`/`ProviderUnavailable`) and fail-closed semantics. Prerequisite for T030/T031 — no component may read the private key via `os.ReadFile` (FR-015a)
- [ ] T030 [US4] Add `ServiceAccountNamespace`/`ServiceAccountName`/`ServiceAccountKeyID`/`ServiceAccountKeyRef`/`SecretProviderBootstrap` to `ControllerConfig` in `gitstore-controller-manager/internal/config/config.go`; make `ApiToken`'s required-check conditional on no signer being configured (mirroring T007's API-side pattern). Note: `ServiceAccountKeyRef` is an ADR 0001 `SecretRef`, **not** a filesystem path — the previously-drafted `ServiceAccountPrivateKeyFile` key is deliberately not introduced (FR-015a). Add keys via #410's `load(path)` (not the old inline `Load` body), preserving `LoadFrom`'s required-file semantics. Per FR-015c the controller's private key MUST resolve from a source mounted into `controller-manager` alone, never from the shared `/config/gitstore.toml`
- [ ] T030a [US4] Update `compose.local.yml` to mount per-service key material for the service-account flow (API signing key; controller private key) as separate, single-service, read-only mounts, and update `scripts/check-local-compose-config.sh` accordingly — it currently asserts exact counts (`-eq 3` for `/config/gitstore.toml`, `-eq 1` for `/config/policy.yaml`, `-ge 4` for `read_only: true`) and will fail as soon as mounts are added. Extend it to also assert that no single file is mounted into more than one service when it holds service-account key material (FR-015c)
- [ ] T031 [US4] Consolidate `cmd/controller/main.go`'s three independent `graphqlclient.New(...)` call sites (`registerNamespace`/`registerCategoryTaxonomy`/`registerProductWatch`) into one shared `buildCredentialSource`+client construction in `main()`, passed into all three (research.md Decision 3 — do not leave three independently-renewing sources)
- [ ] T031a [P] [US4] **Naming consistency — deliberately deferred, do not rename in this spec.** `registerProductWatch` is inconsistent with its `registerNamespace`/`registerCategoryTaxonomy` siblings, but the suffix encodes a real distinction: it registers a watch/cache feeding another reconciler's queue, not a reconciler of its own (it returns only a `Runner`, with no `NewReconciler` call). That distinction disappears once Product/ProductVariant gain their own reconcilers, at which point the right name is settled by what the function actually does rather than guessed at now. Revisit in the future Product/ProductVariant spec and rename all three together. This task exists only so T031 — which rewrites all three signatures to take a shared client — does not silently "tidy" the name mid-flight; keep the existing name in that diff
- [ ] T032 [US4] Wire `ServiceAccountSource`'s exchange-failure path to the existing `internal/health` readiness handler so "not ready" is reported until a credential is obtained

**Checkpoint**: Controller-manager operates with zero manual token refresh and zero `GITSTORE_CONTROLLER__API_TOKEN`; SC-004 satisfied.

---

## Phase 7: User Story 5 - Enrolling a controller identity is scriptable and idempotent (Priority: P2)

**Goal**: A single command replaces hand-signed assertions and manual mutation calls for real deployments and CI.

**Independent Test**: Run the enrollment command twice with identical inputs; confirm the second run is a no-op; confirm no bearer token appears in output or written files.

### Tests for User Story 5

- [ ] T033 [P] [US5] Add a failing idempotency test for the new `gitctl enroll-serviceaccount` subcommand in `gitstore-api/cmd/gitctl/enroll_serviceaccount_test.go`

### Implementation for User Story 5

- [ ] T034 [US5] Implement `enroll-serviceaccount` in `gitstore-api/cmd/gitctl/main.go`: generate or accept a private key, call `createServiceAccount`/`rotateServiceAccountKey` using an already-authenticated administrative session, write only the private key (restrictive permissions) locally
- [ ] T035 [US5] Add a grep-based CI check (or extend the existing credential-logging check, FR-020/SC-007) confirming no bearer/access token value appears in `enroll-serviceaccount`'s output or logs

**Checkpoint**: SC-007-adjacent tooling hygiene confirmed; enrollment no longer requires hand-signing assertions.

---

## Phase 8: User Story 6 - The controller's WebSocket subscription is bound to its access token's lifetime and revoked promptly (Priority: P3)

**Goal**: Authenticate `connection_init`, bind connection lifetime to token expiry, and cancel connections immediately on account disable/delete.

**Independent Test**: Open a subscription with a valid token; disable the underlying `ServiceAccount`; confirm immediate closure without waiting for natural token expiry.

### Tests for User Story 6

- [ ] T036 [P] [US6] Add failing tests for `transport.Websocket.InitFunc` accept/reject (valid token, expired token, malformed payload, disabled account) in `gitstore-api/internal/app/server_websocket_test.go`
- [ ] T037 [P] [US6] Add a failing test confirming an open connection is cancelled immediately when its `ServiceAccount` is disabled/deleted, in the same test file

### Implementation for User Story 6

- [ ] T038 [US6] Implement the in-memory, single-instance-scoped connection registry (UID → cancel functions) in `gitstore-api/internal/app/` (or a new `internal/wsregistry` package)
- [ ] T039 [US6] Add `transport.Websocket.InitFunc`/`CloseFunc` to `gqlServer.AddTransport` in `gitstore-api/internal/app/server.go` per `contracts/controller-credential-source.md`
- [ ] T040 [US6] Call `connectionRegistry.CancelAll(uid)` synchronously from `deleteServiceAccount`'s resolver and from the disable path, satisfying FR-019 within the single-instance profile

**Checkpoint**: SC-006 satisfied.

---

## Phase 9: Polish & Documentation

- [ ] T041 [P] Add the end-to-end integration test `tests/integration/serviceaccount_auth_test.go` covering create → enroll → assert → issue → authenticate → rotate → disable/delete → WebSocket revoke
- [ ] T042 [P] Add the "Addendum — controller/machine identity (spec 061)" paragraph beneath Phase 7 in `docs/implementation/020-pluggable_auth_architecture.md`, mirroring spec 059's existing addendum pattern exactly
- [ ] T043 [P] Update configuration documentation with the new `auth.serviceaccount.*`/`controller.serviceaccount_*` keys and `GITSTORE_CONTROLLER__API_TOKEN`'s deprecated-fallback status
- [ ] T044 [P] Add `docs/runbooks/controller-auth.md` (doc 021 §13): signing-key rotation with zero downtime, controller key re-enrollment, diagnosing "stuck in backoff," recovering from accidental `ServiceAccount` deletion
- [ ] T045 [P] Add a grep-based CI check asserting no `zap` call in `serviceaccountassertion`/`serviceaccountjwt`/`gitstore-controller-manager/internal/graphqlclient` logs a raw token/assertion/private-key value (FR-020, SC-007)
- [ ] T046 Run `make build`, `make test`, `make pr-ready`; confirm zero regressions in existing `rbaclocal`/`staticadmin`/`allowall`/`anonymous` provider test suites

## Dependencies & Execution Order

- Phase 1 (Setup) → Phase 2 (Foundational, blocks all provider/resolver work) → Phases 3-5 (US1-US3, this spec's MVP, sequential within each but the three stories together form one inseparable Phase 1 deliverable per plan.md's Complexity Tracking) → Phases 6-7 (US4-US5, either order, both P2, independent of each other) → Phase 8 (US6, P3, benefits from US4's real credential lifecycle but is independently testable with a hand-issued token) → Phase 9 (Polish).
- **MVP = Phases 1-5.** Spec 060 is unblocked once Phase 5's checkpoint is reached.
