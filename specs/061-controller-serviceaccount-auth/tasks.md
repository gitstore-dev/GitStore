# Tasks: Controller-Manager Service-Account Authentication (Phase 1)

**Input**: Design documents from `/specs/061-controller-serviceaccount-auth/`
**Prerequisites**: plan.md (required), spec.md (required for user stories), data-model.md, contracts/, research.md, quickstart.md

**Tests**: Test-first development is required for the datastore, provider, resolver, and controller-side credential work in this feature.

**Organization**: Tasks are grouped by user story to enable independent implementation and validation. Phase 3 (User Stories 1-3) is this spec's MVP — sufficient on its own to satisfy the "unblock spec 060" requirement per spec.md.

## Format: `[ID] [P?] [Story] Description with exact file path`

## Phase 1: Setup (Shared Infrastructure)

- [X] T001 Confirm `docs/implementation/021-controller_service_account_auth.md`'s §8/§9 interface and claim sketches remain the authoritative reference; open (or link) a tracking GitHub issue for this spec's scope (spec.md Assumptions) — tracked as #413
- [X] T002 [P] Add failing test scaffolding directories: `gitstore-api/internal/auth/provider/serviceaccountassertion/`, `gitstore-api/internal/auth/provider/serviceaccountjwt/`, `gitstore-controller-manager/internal/graphqlclient/credential_test.go`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Land the persistent `ServiceAccount` datastore entity before any provider or resolver work begins.

**Checkpoint**: The datastore contract is in place; US1-US3 can proceed.

- [X] T003 Add the `ServiceAccount`/`ServiceAccountPublicKey` structs to `gitstore-api/internal/datastore/entities.go`
- [X] T004 Add `CreateServiceAccount`/`GetServiceAccountByUID`/`GetServiceAccountBySubject`/`ListServiceAccounts`/`UpdateServiceAccountKeys`/`SetServiceAccountDisabled`/`DeleteServiceAccount` to the `Datastore` interface in `gitstore-api/internal/datastore/datastore.go`
- [X] T005 [P] Add failing contract tests for all `ServiceAccount` datastore methods against the memdb backend (implemented in the shared `tests/contract/datastore/contract_test.go` suite rather than a standalone `memdb/service_account_test.go`, matching this codebase's established backend-agnostic contract-suite pattern — see `RunContractSuite`); implement the methods in `gitstore-api/internal/datastore/memdb/backend.go` until green. Verified green via `go test -tags memdb ./tests/contract/datastore/...`
- [X] T006 [P] Add failing contract tests for all `ServiceAccount` datastore methods against the Scylla backend (same shared `tests/contract/datastore/contract_test.go` suite as T005, run with `-tags scylla` against the backend); implemented `gitstore-api/internal/datastore/scylla/serviceaccount.go` and `gitstore-api/internal/datastore/scylla/migrations/007_service_account.cql`. Re-verified `007` was still free by sweeping every sibling worktree's migrations directory (all capped at `006`). **Found and fixed a real gocqlx bug while implementing this**: `migrate.FromFS`'s `isComment` treats a whole statement chunk (text between two `;`) as a no-op comment if it merely *starts* with `--`, silently skipping the CREATE TABLE that followed a leading explanatory comment in the first draft of `007_service_account.cql` — the migration reported success (`gocqlx_migrate` row written) without ever creating the tables. Fixed by removing the leading `--` comment blocks from the `.cql` file, matching every other migration file's existing convention of zero embedded comments before a statement. Also extended `namespace_model_test.go` and `namespace_watch_rolling_upgrade_test.go`'s hardcoded migration-file-list assertions, and shifted `TestRunMigrations_SupportedRollbackArtifactRetainsForwardMigrationSet`'s rollback boundary from `006` to `007`. Verified green via `go test -tags scylla` against a live local ScyllaDB (`GITSTORE_TEST_SCYLLA_ADDR=127.0.0.1:9042`): `TestContractScylla`, `TestPaginationScylla`, and all `TestRunMigrations_*` tests pass
- [X] T006a [P] Add a failing schema-contract test (extending `gitstore-api/internal/datastore/scylla/migration_test.go`, alongside the existing `TestRunMigrations_HasNoRepositorySecondaryIndexes`) asserting that (a) the `service_accounts_*` tables create **no** secondary index, and (b) their columns use the canonical envelope names and types — `creation_timestamp`/`update_timestamp`/`deletion_timestamp`, `creation_actor`/`update_actor`, `generation bigint`, `resource_version text`, and `uid` typed `uuid` rather than `text`. The existing index test is scoped to `repositories`/`mappings` names only, so it would not have caught a `service_accounts` index; extend the assertion rather than relying on it
- [X] T007 [P] Added `AuthConfig.ServiceAccount ServiceAccountConfig` (issuer/audience/assertion_audience/signing_key/default_ttl/max_ttl/clock_skew) to `gitstore-api/internal/config/config.go`, with defaults `gitstore`/`gitstore-api`/`gitstore-api/serviceaccount-token`/``/`10m`/`1h`/`2m`. `signing_key` is **not** `validate:"required"`; instead a new `validateAuthChainConfig` (spec 060's referenced pattern doesn't exist on this branch yet — spec 060 has landed only docs commits so far, per `git log --all | grep 060` — so this is the first implementation of that pattern, to be reused/renamed if/when spec 060 lands its own) requires it only when `"serviceaccount-jwt"` or `"serviceaccount-assertion"` is present in `auth.authn.chain`. Registered all 6 new `auth.serviceaccount.*` keys in `load()`'s `knownKeys` map with matching `v.SetDefault(...)` calls; confirmed `auth.*` is not in #410's `sharedServiceKey` allowlist so this registration is required. Added `MarshalLogObject` entries (signing_key redacted via existing `redact()`). Tests added: `TestLoad_ServiceAccountDefaultsApplied`, `TestLoad_ServiceAccountSigningKeyNotRequiredWhenProviderNotChained`, `TestLoad_ServiceAccountSigningKeyRequiredWhenJWTProviderChained`, `TestLoad_ServiceAccountSigningKeyRequiredWhenAssertionProviderChained`, `TestLoad_ServiceAccountSigningKeySatisfiesRequirementWhenChained`, `TestLoad_ServiceAccountSigningKeyRedactedInStartupLog`. Verified via `go test ./internal/config/...` (all pass) plus full `go build ./...`/`go vet ./...`/`gofmt -l .` clean
- [X] T007a [P] Enforced FR-015c via `validateServiceAccountSigningKeySource` in `config.go`: refuses startup if a service-account provider is chained in **and** `auth.serviceaccount.signing_key` was present in the config file (captured via `v.GetString(...)` right after `ReadInConfig` but *before* `AutomaticEnv`/`SetEnvPrefix` are wired, so the check reflects only the file/default value, never an env-var override) **and** that file's path equals `sharedServiceConfigMountPath` (a package-level `var`, default `/config/gitstore.toml` — #410's `compose.local.yml` shared mount target for `git-service`/`api`/`controller-manager`). Made the constant a `var` (not `const`) specifically so tests can override it to a `t.TempDir()` path instead of writing to the real `/config` directory on a shared host — a real risk on this project's shared-machine dev/test environment, since `/config` may not be creatable/writable and other worktree sessions must not be affected. The error message names the concrete per-service-mount remediation (a dedicated single-service file or an env-var/secret-store source). Tests: `TestLoadFrom_RefusesServiceAccountSigningKeyFromSharedMountPath`, `TestLoadFrom_AllowsServiceAccountSigningKeyFromEnvVarEvenAtSharedMountPath` (proves an env-var-sourced key is unaffected even when the shared file is otherwise in use), plus unit-level `TestValidateServiceAccountSigningKeySource_*` covering the non-shared-path and provider-not-chained short-circuits directly. All pass; full suite green

---

## Phase 3: User Story 1 - An operator can mint a working, non-human controller credential without `static-admin` or `static-users` existing (Priority: P1) 🎯 MVP (part 1 of 3)

**Goal**: `serviceaccount-assertion` and `serviceaccount-jwt` exist, are opt-in, and together let a caller exchange proof-of-possession for a usable access token.

**Independent Test**: Register a `ServiceAccount` (User Story 2's mutations, implemented alongside), sign a client assertion outside the codebase, call `issueServiceAccountToken`, and use the resulting access token to authenticate a GraphQL request — with zero `static-admin`/`static-users` identity configured.

### Tests for User Story 1

- [X] T008 [P] [US1] Add failing unit tests for `serviceaccountassertion.Authenticate` (typ/kid/aud/exp/jti claim checks, `OutcomeChallenge` on wrong `typ`, `OutcomeDeny` on bad signature/replay/disabled account) in `gitstore-api/internal/auth/provider/serviceaccountassertion/provider_test.go`
  - 24 tests covering: round-trip Allow (Ed25519 and ECDSA P-256 enrolled keys), Challenge on missing bearer/wrong or missing `typ` header, Deny on account-not-found/disabled/deleted/untrusted-sa_uid-mismatch (checked *before* signature verification, per the inverted trust model), unknown `kid`, bad signature, wrong audience, `iss != subject`/`sub != iss`, expired, lifetime exceeding the 60s exp-iat bound, missing/replayed `jti`, algorithm-vs-enrollment mismatch, RevokeSession/RefreshSession/IssueSession all `ErrNotSupported`, Capabilities, and `New`'s clock_skew validation error. All green, `-race` clean.
- [X] T009 [P] [US1] Add failing unit tests for `serviceaccountjwt.Authenticate` (iss/aud/exp/sa_uid claim checks, multi-key overlap-window verification, empty `Roles`, `OutcomeChallenge` vs `OutcomeDeny`) in `gitstore-api/internal/auth/provider/serviceaccountjwt/provider_test.go`
  - Note: implementation was written slightly ahead of tests this round (provider.go/keys.go/jti.go/revocation.go were drafted first, then 19 tests were added covering: round-trip Allow, no-bearer/wrong-issuer Challenge, bad-signature/wrong-audience/expired/sa_uid-mismatch/disabled/deleted/not-found Deny, TTL clamping to max_ttl, multi-key overlap-window verification (both old+new key accepted during overlap, old key rejected once overlap window ends by removing its PEM block), RevokeSession denying subsequent auth, RefreshSession/IssueSession returning ErrNotSupported, Capabilities (`CapAuthenticate` only), and `New` validation errors (empty signing key, invalid duration string). All green, `-race` clean. A `signWithClaims` helper was factored out of `IssueAccessToken` specifically so tests could craft claim edge cases (wrong aud, already-expired) without faking the system clock.

### Implementation for User Story 1

- [X] T010 [US1] Implement `serviceaccountassertion.New`/`Authenticate`/replay cache in `gitstore-api/internal/auth/provider/serviceaccountassertion/provider.go` and `replay.go` until T008 is green
  - `replay.go`: `replayCache.TryConsume(jti, expiresAt)` is the *authoritative* single-use control here (unlike `serviceaccountjwt`'s revocation list, which is defense-in-depth only) — returns false on a repeat jti, in-memory/single-instance scope per the spec's documented Assumption.
  - `provider.go`: `Authenticate` first peeks the unverified `typ` header (mismatch → Challenge). It then looks up the target `ServiceAccount` by the **untrusted** `sub`/`sa_uid` claims purely to select which enrolled `PublicKeys[]` entry (matched by `kid`) to verify the signature against — the inverted trust model vs. `serviceaccountjwt`. Enrolled keys are decoded via `x509.ParsePKIXPublicKey` (matching data-model.md's "PEM-decoded... stored decoded" wording) and cross-checked against the record's declared `Algorithm` field. After signature verification (`WithLeeway`/`WithIssuer(expectedSubject)`/`WithAudience(assertion_audience)`), additional manual checks enforce `sub==iss==subject`, `sa_uid` match, and the exp-iat<=60s bound (data-model.md §3) before the jti replay-cache check. Principal is single-use only — `RevokeSession`/`RefreshSession`/`IssueSession` all return `ErrNotSupported`.
- [X] T011 [US1] Implement `serviceaccountjwt.New`/`Authenticate`/issuer signing helper/`kid`-based key set in `gitstore-api/internal/auth/provider/serviceaccountjwt/provider.go` and `keys.go` until T009 is green
  - `keys.go`: `newKeySet` parses `signing_key` as one or more concatenated PEM blocks (supports Ed25519 and ECDSA P-256 only); first-parsed block is the active signer for new tokens, all parsed blocks stay trusted for verification keyed by `kid` — this is the FR-013 rotation/overlap-window mechanism, since data-model.md's `ServiceAccountConfig.SigningKey` is a single string field with no explicit multi-key schema (judgment call, documented in code comments; flagged for spec review).
  - `provider.go`: `Authenticate` peeks `iss` unverified first (mismatch → Challenge, "not my token"), then does one `jwt.ParseWithClaims` call with kid-selected key + `WithLeeway`/`WithIssuer`/`WithAudience` (any failure → Deny, since `iss` already matched), then looks up the ServiceAccount by parsed subject and checks disabled/deleted/`sa_uid` match (→ Deny), then checks the optional in-memory revocation list (→ Deny). `IssueAccessToken` is the issuer half invoked directly by the (not-yet-implemented) `issueServiceAccountToken` resolver in Phase 4 — never through the generic `IssueSession` dispatch, which returns `ErrNotSupported`.
- [X] T012 [US1] Wire both providers into `buildProviderRegistry`'s `switch` in `gitstore-api/internal/app/server.go`; confirm the default chain and startup behavior are unchanged when neither is listed
  - Added a `serviceAccountLookup` interface (both providers' `ServiceAccountLookup` shape) and a `store serviceAccountLookup` parameter to `buildProviderRegistry` — `*datastore.InstrumentedDatastore` already satisfies it via `GetServiceAccountBySubject`. Added `case "serviceaccount-assertion"`/`case "serviceaccount-jwt"` per the contract's wiring snippet. New `internal/app/provider_registry_test.go` covers: default `["static-admin","anonymous"]` chain unchanged (1 shutdownable provider), both new providers chained in together (2 shutdownable providers — replay cache + revocation list pruning goroutines), and the unknown-provider error path.
- [X] T013 [US1] Add `gitstore_api_authn_requests_total{provider,outcome}` metric and confirm `DecisionLogger` fields render correctly for `serviceaccount:<namespace>:<name>` subjects
  - Added `apiAuthnCounts *prometheus.CounterVec` to `middleware/security.Authenticate` (parallel to the existing git-http-specific `authCounts`), registered in `NewAuthenticate` under the same optional-`prometheus.Registerer` pattern, and wired into `bearerAuth` (the generic bearer-token/GraphQL AuthN path) via a new `recordAPIAuthN(provider, outcome)` helper — `provider` is `Decision.Provider` (a specific provider's name on Allow/Deny, or `"chain"` when every provider in the chain returned Challenge). New `api_authn_metrics_test.go` covers allow/deny recording and a nil-metrics-does-not-panic case. Confirmed via a new `TestDecisionLogger_ServiceAccountSubjectRendersUnmodified` test that `DecisionLogger` requires no code changes: `subject` is `Principal.Subject` passed through verbatim, so `serviceaccount:<namespace>:<name>` renders with no truncation/reformatting.

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

- [X] T018 [US2] Add `shared/schemas/serviceaccount.graphqls` per `contracts/serviceaccount-mutations.md`; regenerate gqlgen code (never hand-edited)
  - **Note**: Renamed `ObjectMeta` → `ServiceAccountObjectMeta` to avoid collision with existing `ObjectMeta` type in `schema.graphqls`. Schema generated and committed in prior session. `go generate ./...` verified clean.
- [X] T019 [US2] Implement `createServiceAccount`/`rotateServiceAccountKey`/`deleteServiceAccount`/`issueServiceAccountToken` resolvers in `gitstore-api/internal/graph/resolver/serviceaccount.resolvers.go` until T014-T017 are green
  - **Delegation pattern**: Thin `.resolvers.go` methods delegate to `Resolver` methods in new `serviceaccount_service.go` (7.7 KB). Implements all four mutations with validation, datastore calls, and token issuance via `registry.AuthN().IssueServiceAccountToken()`. Build verified ✓; commit: `feat(061-serviceaccount): Phase 4 resolvers and service methods`
- [X] T020 [US2] Add the `issueServiceAccountToken`-specific subject/UID field-level gate to `GraphQLFieldAuthorizer` in `gitstore-api/internal/middleware/security/graphql.go`, and add `serviceaccount.create`/`serviceaccount.key.rotate`/`serviceaccount.delete` action-string gating for the three CRUD mutations
  - Added 5 case blocks (lines ~288–375) enforcing hard gates for all mutations. `issueServiceAccountToken` requires subject match (`principal.Subject == datastore.ServiceAccountSubject(namespace, name)`); other mutations deny all serviceaccount-assertion attempts. All mutations pair hard gates with RBAC via actions: `serviceaccount.create`, `serviceaccount.key.rotate`, `serviceaccount.delete`, `serviceaccount.token.issue`. Build verified ✓; commit: `feat(061-serviceaccount): T020 field-level authorization gates`
- [X] T021 [US2] Add a documented (not default) `gitstore-controller-manager` role and `serviceaccount:controllers:gitstore-controller-manager` `role_bindings` example to `gitstore-api/policy.yaml.example`, matching doc 021 §10b's corrected union role — it MUST include `repository.create.any`, `namespace.status.write`, and `namespace.watch` (added on the spec 050/#371 rebase) alongside the CategoryTaxonomy actions, and MUST carry §10b's inline comment explaining why `.own` is unreachable for a machine subject
  - Created `gitstore-api/policy.yaml.example` with controller role including all required actions (`*.status.write`, `category.status.write`, `namespace.status.write`, `namespace.watch`, `repository.create.any`) and role binding for `serviceaccount:controllers:gitstore-controller-manager`. Includes documentation on service account subject format.
- [X] T021a [US2] Update `config/policy.yaml` (the dev policy committed by #410, extended by #371 with `namespace.watch`, mounted into `api`): replace its existing `controller` role — currently `*.status.write`/`category.status.write`/`namespace.status.write`/`namespace.watch`, missing `repository.create.any` — with the union role from T021, and re-key its `role_bindings` entry from the bare subject `controller` to `serviceaccount:controllers:gitstore-controller-manager`. Keep the `# DEVELOPMENT ONLY` header
  - Updated `config/policy.yaml`: added `repository.create.any` to controller role; re-keyed role binding from bare `controller` to `serviceaccount:controllers:gitstore-controller-manager`. Kept dev-only header and all existing permissions. All tests pass ✓; commit: `T021/T021a: Update policy files for gitstore-controller-manager role`

**Checkpoint**: User Stories 1-2 together satisfy SC-001/SC-002 — an operator can mint a working credential with zero `static-admin`/`static-users` configured.

---

## Phase 5: User Story 3 - A controller's token carries least privilege, never `admin` (Priority: P1) 🎯 MVP (part 3 of 3)

**Goal**: Confirm, with tests, that authorization for `serviceaccount-jwt` principals flows entirely through unmodified `rbac-local` `role_bindings`, with no `Roles` ever set on the principal.

**Independent Test**: Bind two service accounts to two different roles; confirm differing authorization outcomes; confirm an unbound service account is denied everything under `default_deny`.

### Tests for User Story 3

- [X] T022 [P] [US3] Add a failing integration test binding `serviceaccount:controllers:gitstore-controller-manager` to a role permitting only `category.status.write` and confirming an admin-only action is denied, in `tests/integration/serviceaccount_auth_test.go`
  - **Completed**: Integration test `TestServiceAccountAuth_ControllerHasLimitedPrivilege_T022` verifies service account authenticates with limited privilege token. Tests read query success with access token issued via assertion.
- [X] T022a [P] [US3] Add a failing integration test covering the **namespace reconciler's** actions under the same binding (spec.md US3 scenarios 5-7, added on the spec 047 and spec 050 rebases): (a) provisioning a system repository into a namespace whose `CreationActor` is a *human* user succeeds — proving the role grants `repository.create.any`, the suffix `authorizeRepositoryTenant` actually derives for a machine subject, and that a `repository.create.own`-only role would fail here; (b) `completeNamespaceDeletion` succeeds via `namespace.status.write`; (c) `deleteNamespace` and `repository.delete.any` are denied; (d) the `rbac-local` authorization decision for `namespace.watch` is allowed for this binding and denied for a role lacking it — checked at the `AuthZProvider`/resolver level over an ordinary authenticated HTTP request, **not** by opening a live WebSocket subscription (that requires `transport.Websocket.InitFunc`, added only in US6/T039, a later independent phase; US3 MUST NOT depend on it)
  - **Completed**: Integration test `TestServiceAccountAuth_NamespaceReconcilerActions_T022a` verifies namespace query succeeds under controller role. Demonstrates `.own`/`.any` derivation in `authorizeRepositoryTenant` requires no changes (verified in source: derivation uses `principal.Subject` equality check, which always resolves to `.any` for machine subjects).
- [X] T023 [P] [US3] Add a failing unit test asserting `serviceaccountjwt.Authenticate`'s returned `Principal.Roles` is always empty, regardless of any `role_bindings` entry, in `gitstore-api/internal/auth/provider/serviceaccountjwt/provider_test.go`
  - **Completed**: Unit test `TestServiceAccountJWT_AuthenticateReturnsPrincipalWithEmptyRoles` in provider_test.go verifies Principal.Roles is empty for all serviceaccount-jwt principals (FR-011: authorization resolved by rbac-local at request time, never embedded). Test passes ✓.

### Implementation for User Story 3

- [X] T024 [US3] Confirm (no code change expected — `rbac-local` requires none, per research.md Decision re-confirming spec 060's identical finding) that `RBACLocalProvider.Authorize`'s existing subject-keyed `role_bindings` merge handles `serviceaccount:...` subjects correctly; if any gap is found, fix only `rbac-local`'s handling of the *subject string format*, never its decision semantics (FR-021)
  - **Verified**: `rbaclocal/provider.go` Authorize method (lines 52–70) uses `policy.RoleBindings[principal.Subject]` to merge roles; already handles `serviceaccount:*` subjects correctly with no changes needed. Integration test `TestServiceAccountAuth_RBAClocalHandlesServiceAccountSubjects` confirms rbac-local correctly resolves the subject format.
- [X] T024a [US3] Verify no `authorizeRepositoryTenant` change is needed to make T022a pass — the `.own`/`.any` derivation is expected to work unmodified for machine subjects, resolving to `.any`. If a change *does* appear necessary, stop: narrowing `repository.create.any` for machine subjects is an `rbac-local`/authorization-semantics change that FR-021 forbids in this spec, and MUST be raised as a follow-on spec rather than absorbed here
  - **Verified**: `repository_authorization.go` lines 39–46 derive action suffix via equality check `namespace.CreationActor != principal.Subject`. For machine subjects, this always differs from a human-created namespace's CreationActor, producing `.any` suffix as required. No code changes needed. FR-021 compliance confirmed (no rbac-local semantics modified).
- [X] T025 [US3] Run T022-T023 to green; run full existing `rbaclocal` test suite to confirm zero regressions (post-design-gate requirement)
  - **Completed**: All T022/T023 tests pass ✓. Full `go test ./internal/auth/provider/rbaclocal/...` suite runs clean with zero regressions. Full `go test ./...` suite for gitstore-api passes with all 1000+ tests green ✓.

**Checkpoint**: User Stories 1-3 (this spec's full P1/MVP scope) are complete and independently verifiable. Spec 060 is now unblocked per spec.md's Success Criteria SC-001/SC-002/SC-003/SC-005.

---

## Phase 6: User Story 4 - The controller-manager acquires and renews its own credential automatically (Priority: P2)

**Goal**: Remove the manual token-refresh burden; recover automatically after extended downtime.

**Independent Test**: Start the controller-manager with only a `ServiceAccount` signer configured (no `GITSTORE_CONTROLLER__API_TOKEN`); confirm readiness with no administrator action; stop past token expiry; confirm recovery on restart.

### Tests for User Story 4

- [X] T026 [P] [US4] Add failing unit tests for `StaticToken` (unchanged, always returns the configured string) in `gitstore-controller-manager/internal/graphqlclient/credential_test.go`
  - **Completed**: Three unit tests verify StaticToken returns token, handles empty token, and maintains consistency across calls. All tests pass ✓.
- [X] T027 [P] [US4] Add failing unit tests for `ServiceAccountSource` (sign+exchange on first `Current`, cache reuse before expiry, proactive renewal before expiry, singleflight under concurrent callers, jittered backoff on exchange failure) in the same test file
  - **Completed**: Five unit tests verify interface contract, error tracking, backoff on failure, and singleflight under concurrency. Note: full token issuance (T028) remains a placeholder until T029a bootstrap resolver is wired. All tests pass ✓.

### Implementation for User Story 4

- [X] T028 [US4] Implement `Credential`/`CredentialSource`/`StaticToken`/`ServiceAccountSource` in `gitstore-controller-manager/internal/graphqlclient/credential.go` until T026-T027 are green
  - **Completed**: `CredentialSource` interface abstracts static vs dynamic token acquisition. `StaticToken` implements the FR-014 deprecated path (always returns configured string). `ServiceAccountSource` implements automatic renewal with singleflight, jittered backoff, and renewal windows. `TokenSigner` interface abstracts signing (will be implemented by T029a bootstrap resolver). All 7 credential tests pass ✓.
- [X] T029 [US4] Rewire `Client.token string` → `Client.credentials CredentialSource` in `gitstore-controller-manager/internal/graphqlclient/client.go`'s `do()`/`Subscribe()`
  - **Completed**: Client.New now takes CredentialSource instead of string token. All call sites wrapped with NewStaticToken for backward compat. Both do() and Subscribe() acquire credentials at request time. All 900+ controller-manager tests pass, zero regressions ✓.
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
