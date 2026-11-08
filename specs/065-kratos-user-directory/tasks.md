# Tasks: Production User Directory Provider (Kratos)

**Input**: `specs/065-kratos-user-directory/plan.md`, `spec.md`, `data-model.md`, `research.md`, `contracts/` and `quickstart.md`.
**Prerequisite**: The implemented canonical authorization and cluster-scoped ServiceAccount contracts from feature 064.
**Tests**: Required by the specification's acceptance/production requirements and the constitution. Write focused tests first and observe the expected failure before implementing the corresponding behavior; preserve already-passing behavior.
**Organization**: Five user-story increments in specification priority order. Paths below are relative to the repository root. New filenames describe narrowly scoped additions; extend existing helpers rather than duplicating infrastructure.

## Format: `[ID] [P?] [Story] Description`

- `[P]` identifies independent files/work with no unfinished dependency within its indicated execution batch.
- `[US1]` through `[US5]` identify the corresponding specification story.
- Setup, foundation and final tasks have no story label.
- Completed tasks are marked `[X]`; unchecked tasks are not yet complete. Existing Actor lifecycle changes are a baseline to verify, not permission to redo them.

## Scope and implementation gates

GitStore remains authoritative for ServiceAccount UID, keys and enablement. Human identities, protected directory groups and human lifecycle facts remain external. No human/link/intent database, outbox, broker, DLQ, durable UserDir consumer checkpoint, restart replay, automatic repair, inventory sweep, external ServiceAccount watch API or ServiceAccount controller is scope.

Shared CDC/materializer publication remains durable and uses existing producer leases/progress. The UserDir consumer is deliberately best-effort: bounded buffers, a small finite retry budget, discard after exhaustion, and live subscription from the current stream boundary after restart. Lost events may leave missing/stale/orphaned mirrors indefinitely. Such a mirror cannot override authoritative ServiceAccount state.

The selected implementation-time pins must prove supported forward Relay behavior and unified query-time diagnostics for unsupported backward pagination. Keep the standard connection arguments; capability advertisement is deferred to #472. External ObjectMeta defaults to initiating-Principal ownership with represented-username fallback. Staff revisions are best-effort for integrated writes; Customer counters are -1/"-1". T031 verifies supported behavior and lost-update/coverage limitations, not invented provider CAS or backward emulation. Lifecycle/session enforcement is similarly gated by T034/T041 against the actual pin.

## Phase 1: Setup

**Purpose**: Establish supported pins, the current implementation baseline and reviewed external identity contracts without inventing infrastructure.

- [X] T001 Select the latest stable compatible implementation-time Kratos, Hydra and affected reference-image/client pins; record release-specific API/configuration evidence and any justified SDK dependency in `specs/065-kratos-user-directory/research.md`, referencing `compose.oidc.yml`, `config/oidc/kratos/kratos.yml`, `config/oidc/hydra/config.yaml` and `gitstore-api/go.mod`; do not use moving tags or historical planning pins as the release target.
- [X] T002 Verify the prerequisite authorization/ServiceAccount interfaces and already-present `Actor.lifecycle`/`ActorLifecycleState` conversion and tests in `shared/schemas/admin/schema.graphqls`, `shared/schemas/admin/serviceaccount.graphqls`, `gitstore-api/internal/admin/graph/resolver/serviceaccount_service.go` and `serviceaccount_service_test.go`; record remaining compatibility/rollout work in `specs/065-kratos-user-directory/plan.md` without reimplementing the lifecycle rename.
- [X] T003 Finalize provider-neutral profile, mapping, contact, lifecycle and bounded live-event contracts in `specs/065-kratos-user-directory/contracts/user-directory.md` and `data-model.md`; identify supported provider/issuer-qualified UID/external-ID/exact-username lookup and required metadata fields, and explicitly preserve the best-effort limitation and deferred capabilities.

**Checkpoint**: Pins and contracts are attributable to a tested release; implementation gaps are explicit.

**Execution status (2026-11-07)**: T001's v26.2.0 pins passed the owned
provider/schema/metadata fixture, Hydra discovery/client setup and UI startup.
T002 passed the existing API graph/auth suites. T003 retains the user's
ownership/counter and query-time backward-diagnostic decisions. The approved
replacement uses supported hooks/PATCH and removes the PostgreSQL extension.
The owned fixture verifies persisted actors/sentinels, public username guards,
blocking callback failures, adapter PATCH revisions and privileged Admin bypass.
Full GitStore admission/lifecycle/load
acceptance remains separate and is not marked complete.

Foundation progress: T004/T005/T007/T008 are implemented and covered by passing
auth/config/graph tests and focused Go vet. T004 includes the profile lookup/
group-capability/absence/outage enrichment matrix and anonymous bypass.
T006/T009 are implemented and passed the owned fixture
with race detection, including the actual Go webhook receiver.
The release-specific PUT/PATCH/SQL trace now establishes that local candidate
counter advancement is not remote CAS. The accepted replacement explicitly
allows lost increments/last-writer-wins and unsupported Customer counters.
The selected pin rejects PATCH `test`; no concurrency layer is added.
Docker Desktop was started with user authorization. All test-owned containers,
network and temporary database storage were cleaned up.
Full lint/config checks pass after the explicitly authorized corrections.

Adapter progress: T010/T013/T014/T015/T016 pass focused race-enabled tests.
The owned selected-pin fixture also proves bounded exact identifier and unique
external-ID lookup endpoints. The adapter is registered with an optional private webhook listener and now
enriches Admin/Git HTTP, fresh GraphQL operations and WebSocket initialization.
T011/T017/T019/T020 pass race-enabled security/app/auth/RBAC suites and Go vet.
Static directory lookup no longer requires static AuthN or a signing key and
cannot authenticate or issue sessions. Namespace creation uses the existing
policy evaluator's trusted username predicate and Staff binding.
WebSocket leases cannot outlive cached profile freshness or shorter token
expiry. Generation-safe local invalidation cancels real authenticated
WebSocket transports and permits fresh admission; SDK directory cache/request/
saturation signals have finite labels. T018/T021 are verified.
Combined deployed two-API/mixed-version rollout acceptance remains pending.
Policy-equivalent static/OIDC Staff outcomes passed against the owned Kratos
fixture with two independent directory caches, not two deployed API processes.
Repository validation now passes
`make lint` and `make check TARGET=all`: the two reported test files were
formatted, and the local API environment uses named Admin/Storefront ports
instead of `GITSTORE_API__PORT`. The environment file remains ignored and no
credentials were changed. Basic provider Upsert/Deactivate are implemented; complete
lifecycle/GraphQL administration acceptance remains pending.
Bounded native first/after provider pages, every-edge continuation and empty
terminal PageInfo are verified. The authenticated development HTTP courier,
explicit SMTPS catcher transport, endpoint isolation/body bounds and private
SDK/log telemetry passed focused and selected-pin owned acceptance.
Native contact/lifecycle and development courier acceptance now pass, including
real invitation setup/session revocation and subject-keyed contact changes.
US2's public Customer, dual login and static/OIDC/RBAC isolation matrix passes
against native Kratos plus focused in-process admission tests. This does not
claim deployed GitStore replica/rollout or GraphQL human administration completion.

Actor read progress: T033/T039/T040 now pass race-enabled schema/resolver,
API/security and provider suites plus Go vet. Concrete selectors distinguish
human and workload names, unified lists require both grants before enumeration,
and source-only ServiceAccount pages support both directions without UserDir.
Orphan-only nonterminal directory pages fail explicitly without inventory scans
or name rebinding. The action-specific administration schema and entrypoints,
safe self-update guards, protected group PATCH and redacted mutation logs are
implemented and covered by focused tests, but native and deployed administration
gates remain open. Customer memberships remain mandatory global configuration:
the user confirmed idempotent assignment of configured groups and rejection
of individual removal or assignment outside that configuration.

The approved schema addition shares human fields through `User implements Actor
& Node`; ServiceAccount remains outside User and exposes lazy `groups`.
Race-enabled transport/provider tests verify Staff/Customer User fragments,
source-UID-only workload lookup, missing/group-incapable directory empty lists,
operational errors, and no membership I/O on denied or unselected reads.
Group mutation names and Actor payloads are unchanged. Model-backed fields do
not require standalone resolvers solely for future cost annotations; complexity
policies must account for bounded page sizes and source/directory hydration.

Workload publication, UID-linked mirror application and live consumption are
implemented. Race-enabled source/provider/API tests cover ordered committed
MemDB writes, bounded retries/overflow, later delivery, current-boundary startup,
reload cancellation and callback rebinding. Native Scylla contracts and the
ServiceAccount CDC reader pass in an isolated owned keyspace, including partial
enrollment suppression, rejected writes, retained deletion UID and name reuse.
Native mirror acceptance passes through two selected-pin Kratos replicas using
a shared PostgreSQL store and a MemDB source. These separate native fixtures
do not establish end-to-end Scylla-to-Kratos delivery through two deployed
GitStore APIs, physical consumer crashes, rolling replacement or sustained load.
Exact native pre-013 upgrade acceptance now verifies retained ServiceAccount
UID/data, unchanged applied checksums and ledger write times, rejection before
upgrade, read-only startup after upgrade, and refusal of a rollback bundle
missing the forward migration. UID-enrichment tests reject old-name memberships
and preserve source-authenticated identity; native-I/O source-change tests force
retry. Existing JWT/assertion denial and orphan-hydration regressions pass.

Deployed administration/source acceptance now passes through two actual API
processes, native Kratos replicas with shared PostgreSQL, controlled OIDC/JWKS,
the development catcher and one unchanged current Git process. The human
matrix covers lifecycle/setup/contact/group/authorization operations,
concurrent best-effort writes, stable-endpoint forward continuation, redacted
audit receipts, WebSocket expiry, API restart and directory pause/restoration.
The shared-Scylla matrix proves real CDC mirror delivery, enrolled assertion/
access-token issuance, workload group authorization, exhaustion receipts on
both consumers, physical consumer crash/current-boundary restart, denial by
source authority with explicitly unexpired credentials despite a stale active
mirror, later delivery, deletion and UID-safe name reuse.

Fresh-journal startup and missing live discard logging exposed by that
deployment are fixed and covered by focused race-enabled regressions. All
owned directory fixtures pass together with driver race detection. Full
`make generate TARGET=all`, `make test`, `make lint` and `make check TARGET=all`
pass. Mixed-version/provider-switch, ambiguous-write and declared identity-scale
readiness/chaos gates remain open; these functional fixtures do not substitute
for them.

## Phase 2: Foundational contracts and configuration

**Purpose**: Establish the shared profile/configuration/schema prerequisites before story implementation.

### Tests first

- [X] T004 Add failing provider-contract tests in `gitstore-api/internal/auth/user_directory_contract_test.go` for trusted profile kind/canonical username/lifecycle/contact facts, bounded operation contracts, and distinct unlinked, unavailable and group-incapable behavior while retaining existing UserDir operations.
- [X] T005 [P] Extend `gitstore-api/internal/config/config_test.go` with failing cases for independent UserDir selection, Kratos-only required endpoint/mapping settings and protected optional Admin gateway credentials, provider-qualified mapping validation, two-second request bounds, finite concurrency/cache settings and unchanged `none` defaults.
- [X] T006 [P] Add selected-pin schema contract tests in `tests/integration/user_directory_schema_test.go` for three schema IDs, globally unique enrolled human password identifiers, registration actor metadata/Customer sentinels, public settings username guards, blocking hook failure and persisted GitStore PATCH revisions; distinguish privileged Admin/schema bypass and non-CAS writes from supported behavior.

### Implementation

- [X] T007 Extend the existing profile/principal and narrow directory capability contracts in `gitstore-api/internal/auth/types.go`, adapting `gitstore-api/internal/auth/provider/userdirnone/provider.go` and `gitstore-api/internal/auth/provider/staticusers/provider.go`; retain legacy operations, no-group capability semantics and provider-neutral inputs without a new persisted user model or GraphQL profile-type enum.
- [X] T008 Implement validated Kratos UserDir endpoint/secret/mapping and bounded request/cache settings in `gitstore-api/internal/config/config.go`, exposing non-secret examples in `config/config.toml` and `config/oidc/oidc.env.example`; no consumer checkpoint, DLQ or repair configuration is added.
- [X] T009 Add versioned `staff`, `customer` and `serviceAccount` schemas in `config/oidc/kratos/staff.schema.json`, `customer.schema.json` and `serviceaccount.schema.json`, register them in `config/oidc/kratos/kratos.yml` and mount them through `compose.oidc.yml`; configure opt-in supported blocking hooks with `gitstore-hooks.yml`/`webhook.jsonnet`, retaining the password session hook and standalone behavior. Remove the superseded SQL extension/job and alpha identity schema without a compatibility/migration layer, GitStore profile table or unrelated database upgrade.

**Checkpoint**: Shared contracts/configuration compile with `none` and standalone `static-users` UserDir. Selected-pin identity schemas are validated, not merely assumed.

## Phase 3: US1 - Any authenticated subject resolves to a profile and groups (P1, MVP)

**Goal**: Enrich authenticated subjects through a production Kratos UserDir before existing authorization, regardless of AuthN provider.

**Independent test**: Seed active staff `bob` and `merchandiser` membership in an owned directory. Authenticate through configured `static-users` and `oidc-jwt` mappings. The same policy allows Product creation in the bound namespace and denies it elsewhere; missing profiles, unavailable directories and `none` remain distinct.

### Tests first

- [X] T010 [US1] Add failing Kratos adapter tests in `gitstore-api/internal/auth/provider/kratos/provider_test.go` for direct UID/external-ID/supported exact-username mappings, provider/issuer collisions, profile/group provenance, reserved groups, missing profiles, errors, deadlines and no scan/email-link fallback.
- [X] T011 [P] [US1] Add failing orchestration tests in `gitstore-api/internal/middleware/security/secure_test.go` and `gitstore-api/internal/app/provider_registry_test.go` for enrichment before AuthZ on Admin/Git paths, anonymous bypass, fail-closed lookup errors, group-incapable providers and selecting static UserDir without static AuthN.
- [ ] T012 [P] [US1] Add failing acceptance cases in `tests/integration/user_directory_test.go` and `user_directory_rolling_upgrade_test.go` for the AuthN/UserDir/AuthZ matrix, mapped staff policy outcomes, five-minute cache/group/lifecycle freshness, WebSocket lease expiry and mixed-version replacement across two replicas using bounded test clocks/fixtures; define the combined rollout cases before dependent implementation.

### Implementation

- [X] T013 [US1] Implement the bounded authenticated Kratos Admin client in `gitstore-api/internal/auth/provider/kratos/client.go`, using existing HTTP/secret/observability helpers and T001's tested endpoints; classify not-found/conflict/invalid/unavailable errors, cap response sizes/concurrency and never log directory response bodies or secrets.
- [X] T014 [US1] Implement configured provider/issuer-qualified subject resolution in `gitstore-api/internal/auth/provider/kratos/mapping.go`; use Kratos UID for reference OIDC and immutable ServiceAccount UID-derived external IDs for workloads, reject unsupported exact-lookup capabilities, and never infer identity from email or coincidentally equal subjects.
- [X] T015 [US1] Implement profile conversion and existing UserDir lookup/group operations in `gitstore-api/internal/auth/provider/kratos/provider.go` and `profile.go`; trust schema assignment/protected metadata, synthesize exactly one profile group, render ordinary groups as `system:group:<name>` and distinguish absence from operational failure.
- [X] T016 [US1] Implement a bounded per-replica profile/group cache in `gitstore-api/internal/auth/provider/kratos/cache.go`, qualified by provider/issuer/identity with a TTL no greater than five minutes, bounded in-flight work and no cached outage converted into an empty group result.
- [X] T017 [US1] Register and orchestrate the Kratos provider in `gitstore-api/internal/app/server.go` and `gitstore-api/internal/middleware/security/secure.go`; enrich Admin and Git HTTP principals before existing AuthZ, retain AuthN-owned `system:authenticated`, and construct static UserDir independently of the AuthN chain.
- [X] T018 [US1] Integrate profile freshness with the existing WebSocket lifecycle in `gitstore-api/internal/app/graphql.go` and `server_websocket_test.go`; cap authenticated profile leases below five minutes and provide bounded local cancellation/invalidation without a new durable event/checkpoint mechanism.
- [X] T019 [US1] Expose the trusted canonical UserDir username to the existing RBAC `when` evaluator in `gitstore-api/internal/auth/provider/rbaclocal/provider.go`, `policy.go` and `policyfile_test.go`; implement the contract's username-match predicate without a policy-independent Namespace guard.
- [X] T020 [US1] Update `gitstore-api/policy.yaml.example` to bind Namespace creation to `system:staff` with USER-tier/canonical-name conditions and ordinary sample group role bindings; test customer/workload/nonmatching-name denial through the existing policy evaluator in `gitstore-api/internal/auth/provider/rbaclocal/rbaclocal_test.go`.
- [X] T021 [US1] Add bounded SDK-backed directory latency/cache/error/saturation signals and redacted logging in `gitstore-api/internal/auth/provider/kratos/metrics.go` and `gitstore-api/internal/auth/logging.go`, reusing shared observability and excluding identity/email/token labels.
- [X] T022 [US1] Execute the US1 fixture and focused adapter/security/app/RBAC suites, recording acceptance and any unsupported mapping combinations in `specs/065-kratos-user-directory/quickstart.md`; prove policy-equivalent staff outcomes and fail-closed behavior without introducing new public Makefile targets.

**Checkpoint**: Production directory enrichment works with pre-provisioned identities; US1 does not depend on human administration, ServiceAccount event delivery or mail.

## Phase 4: US2 - Customer identities are separated from staff (P1)

**Goal**: Permit customer-only self-registration and both human password identifiers while preserving protected identity fields and the existing endpoint boundary.

**Independent test**: Register a customer using the reference public flow, sign in using username and email, attempt conflicting identifiers/tampered traits and verify no staff groups or operator grants. Preserve the currently unavailable Storefront response; do not build a storefront UI.

### Tests first

- [X] T023 [US2] Extend the existing selected-pin self-service cases in `tests/integration/user_directory_schema_test.go` for customer-only registration, attempts to select Staff/ServiceAccount schemas, immutable username/protected fields and cross-schema identifier uniqueness with non-enumerating errors.
- [X] T024 [P] [US2] Extend `gitstore-api/internal/app/endpoint_admission_test.go` and `operation_admission_test.go` with customer/staff/workload boundary cases, tampered customer group metadata and unchanged `STOREFRONT_UNAVAILABLE` behavior.
- [X] T025 [P] [US2] Add dual-identifier login cases in `gitstore-oidc-bridge/internal/bridge/login_test.go` and the reused `tests/integration/user_directory_schema_test.go`, proving username/password and email/password resolve the same identity and that identity-conflict errors do not expose another type/subject.

### Implementation

- [X] T026 [US2] Restrict public registration/settings in `config/oidc/kratos/kratos.yml` and `customer.schema.json` using selected-pin supported flows; ensure customer-only schema assignment and that users cannot change canonical username, groups, lifecycle or administrator-controlled contact facts.
- [X] T027 [US2] Verify `gitstore-oidc-bridge/internal/bridge/login.go` and `internal/kratosclient/client.go` against both native password identifiers; no adaptation is needed because the bridge forwards the resulting session, not passwords. Preserve Kratos UID-based OIDC subjects, existing issuer/audience behavior and secret-safe errors.
- [X] T028 [US2] Implement configured customer ordinary-group restrictions in `gitstore-api/internal/auth/provider/kratos/profile.go` and `gitstore-api/internal/config/config.go`; ignore/report forged staff/reserved memberships and derive `system:customers` only from trusted schema classification.
- [X] T029 [US2] Apply trusted profile admission at the existing boundary in `gitstore-api/internal/app/endpoint_admission.go` and `operation_admission.go`; preserve supported routes and unavailable Storefront behavior, leaving Namespace resource conditions entirely to RBAC.
- [X] T030 [US2] Execute public registration/login and Customer static/OIDC/Kratos/RBAC isolation using the reused `tests/integration/user_directory_schema_test.go` and focused `gitstore-api/internal/app/endpoint_admission_test.go` suites; verify permissive-AuthZ admission denial and no identifier-based identity merging. Native bridge acceptance uses a stubbed Hydra callback, not a deployed token exchange; deployed replica acceptance remains T012/T078.

**Checkpoint**: Customer self-service cannot manufacture staff/workload identities or operator access; no new customer application/profile UI is introduced.

## Phase 5: US3 - Operators administer identities through GitStore (P2)

**Goal**: Expose concrete Actor/Node/Relay reads and authorized action/input/payload administration using all existing human lifecycle states.

**Independent test**: With administration grants, invite staff `carol`, resend/complete required setup, assign a group, suspend/deactivate/reactivate and delete. Without grants, every operation is denied without disclosing existence. Verify concrete actor IDs, forward Relay pages/unified backward diagnostics, initiator ownership and correct counters, source-backed ServiceAccount reads, safe email changes and forbidden self-edits.

### Capability proof and tests first

- [X] T031 [US3] Verify bounded first/after Relay edges/PageInfo, unified query-time diagnostics for last/before, initiating-owner/username-fallback semantics and best-effort Staff/unsupported Customer revisions against T001's pin in `gitstore-api/internal/auth/provider/kratos/pagination_test.go` and `administration_test.go`; record the contract in `contracts/admin-graphql.md` and `research.md`. Cover concurrent lost updates and untracked/Admin-bypass writes; do not emulate reverse pagination, assume remote CAS or implement #472 capability advertisement.
- [X] T032 [P] [US3] Add failing schema/lookup/authorization tests in `gitstore-api/internal/admin/graph/resolver/actor_contract_test.go` and `global_id_test.go` for Actor implementing Node, concrete Staff/Customer/existing ServiceAccount, separate `ActorBy` selectors, Relay edges, payloads, stable kind/UID IDs and unauthorized-result masking.
- [X] T033 [P] [US3] Add canonical action/default-role tests in `gitstore-api/internal/auth/provider/rbaclocal/user_actions_test.go` for cluster-scoped `user.create/read/list/update/delete/group.update` and unchanged source-owned ServiceAccount actions; verify dual grants on unified `actors` over the existing `gitstore-api/internal/app/endpoint_admission_test.go` transport fixture before any enumeration.
- [X] T034 [P] [US3] Add selected-pin failing lifecycle/administration tests in `gitstore-api/internal/auth/provider/kratos/lifecycle_test.go` and `administration_test.go` for INVITED/PENDING_VERIFICATION/ACTIVE/INACTIVE/SUSPENDED, resend, verified reactivation prerequisites, revocation/admission, protected fields, groups and contact operations.
- [X] T035 [P] [US3] Add failing API acceptance/race tests in `tests/integration/user_directory_admin_test.go` for authorized/denied mutations, safe self-edit flows, reserved groups, username/email conflicts, auditable redacted changes and concurrent administration across two API replicas.

### Implementation

- [X] T036 [US3] After T031 resolves, extend `shared/schemas/admin/schema.graphqls`, add `shared/schemas/admin/user.graphqls` and extend `serviceaccount.graphqls` with the reviewed Actor/Node envelope, concrete human types, connections/selectors and all action-specific inputs/payloads; regenerate through `make generate TARGET=all`, preserving the existing lifecycle conversion and excluding UserFilter, flat auditing fields and new profile enums.
- [X] T037 [US3] Extend the canonical vocabulary in `shared/schemas/authorization-actions.json` for `user.*`; the existing finite wildcard expansion in `gitstore-api/internal/auth/provider/rbaclocal/policy.go` needs no special case. Verify only `cluster-admin` grants these capabilities by default, reject namespaced role projection, and regenerate committed catalogs through `make generate TARGET=authz`.
- [X] T038 [US3] Integrate Staff/Customer/ServiceAccount global IDs and existing `node`/`nodes` dispatch/authorization in `gitstore-api/internal/admin/graph/resolver/global_id.go`, `query_helpers.go`, `actor_service.go` and `gitstore-api/internal/middleware/security/graphql.go`; preserve per-kind read permissions, source UID authority, checked-profile snapshots and nondisclosing errors.
- [X] T039 [US3] Implement the proved native provider pagination in `gitstore-api/internal/auth/provider/kratos/pagination.go`; bind opaque cursors to provider/connection, bound requests/pages and reject malformed/cross-provider cursors without scans, deep offsets, local cursor history or human Scylla projections. Provider race tests and owned selected-pin forward continuation evidence pass; unified backward diagnostics pass over the GraphQL transport.
- [X] T040 [US3] Implement human envelope conversion, `actor(by:)`, unified `actors` and the dedicated source-backed `serviceAccounts` connection in `gitstore-api/internal/admin/graph/resolver/actor_service.go`, `user.resolvers.go` and `serviceaccount_service.go`; require list grants before enumeration and hydrate workload mirrors through bounded authoritative UID reads, skipping stale/orphaned mirrors without UID/name rebinding. Race-enabled resolver/API tests verify separate selectors, source-only forward/backward pages and scoped cursors. Nonterminal orphan-only pages fail explicitly rather than scan ahead or emit a non-progressing cursor.
- [X] T041 [US3] Implement invitation/resend, verification-aware lifecycle, suspend/deactivate/reactivate/delete operations in `gitstore-api/internal/auth/provider/kratos/lifecycle.go` using protected metadata and tested provider flow/session APIs; prove non-ACTIVE admission/revocation and never equate native inactive with complete enforcement or invent a state-machine framework.
- [X] T042 [US3] Implement administrator-owned groups, consent and deliverability operations in `gitstore-api/internal/auth/provider/kratos/administration.go`; reject reserved/empty groups, enforce customer group restrictions, preserve workload source fields and reject human contact operations on ServiceAccounts.
- [X] T043 [US3] Implement safe display-name/email-change updates in `gitstore-api/internal/auth/provider/kratos/profile.go` and `administration.go`; preserve the previous verified deliverable address until trusted replacement verification, reject type/username/lifecycle/group self-edits and redact conflicts.
- [X] T044 [US3] Implement every reviewed action/input/payload mutation in `gitstore-api/internal/admin/graph/resolver/user.resolvers.go` and `actor_service.go`, using existing authorizers/error helpers and the operation/action matrix rather than raw identity returns or new authorization layers.
- [X] T045 [US3] Audit all directory mutations and invalidate affected local lookup/subscription leases through `gitstore-api/internal/admin/graph/resolver/actor_service.go`, `gitstore-api/internal/auth/logging.go` and `gitstore-api/internal/auth/provider/kratos/cache.go`; include actor/target/change/time without secrets or full emails, with the existing freshness bound on other replicas.
- [X] T046 [US3] Enforce human-only edit/lifecycle/delete/email operations and directory-only workload group administration in `gitstore-api/internal/admin/graph/resolver/actor_service.go`; source UID/keys/enablement/lifecycle continue through existing ServiceAccount administration and cannot be changed through user mutations.
- [X] T047 [US3] Extend reference identity/role fixture setup in `tests/integration/user_directory_admin_test.go` and `docs/developer-guide.md` with invited staff and `merchandiser`/`catalog-viewer` bindings using supported Admin/registration/bootstrap/assign-role mechanisms; use private runtime material, not committed user credentials or a production identity-sync CLI.
- [X] T048 [US3] Execute T035's two-replica administration, group/lease freshness and conflicting-write cases in `tests/integration/user_directory_admin_test.go`, verifying the documented provider concurrency behavior rather than assuming CAS or replica-local correctness.
- [X] T049 [US3] Regenerate and run focused schema/resolver/provider/action-catalog checks for `shared/schemas/admin/user.graphqls` and `gitstore-api/internal/admin/graph/resolver/actor_contract_test.go`; verify forward Relay pages/unified backward diagnostics, Actor/Node inheritance, payload conventions and the unchanged existing ServiceAccount API. Generated/catalog checks and resolver/provider race suites pass; this does not close separate native/deployed administration gates.
- [X] T050 [US3] Update `docs/api-reference.md` and `docs/user-guide.md` for concrete actors, supported pagination/selectors, invitation/lifecycle/group operations, metadata and contact semantics; use consumer-facing language without planning/task references or invented auditing guarantees.

**Checkpoint**: Concrete actors and directory administration are independently usable against a provisioned directory. ServiceAccount lookup/list remains source-backed even without UserDir; unified directory listing is not guaranteed complete after missed workload events.

## Phase 6: US4 - ServiceAccounts have directory profiles without logins (P2)

**Goal**: Feed the shared journal from authoritative ServiceAccount CDC and synchronize credentialless mirrors through a bounded, best-effort internal consumer.

**Independent test**: Create `ci-bot` with the provider available and verify its UID-linked credentialless profile and directory groups. Disable/delete/reuse the name and verify source authority. Exhaust delivery retries, overflow a buffer or crash the consumer: observe discard and continued live delivery without a checkpoint/replay/repair promise.

### Tests first

- [ ] T051 [US4] Add failing full-image CDC/enrollment/delete/schema-history tests in `gitstore-api/internal/datastore/scylla/serviceaccount_cdc_test.go` and `migration_schema_test.go`; cover conditional/ambiguous writes, incomplete projection enrollment, retained deletion UID and shared producer lease/progress behavior in an isolated owned keyspace.
- [X] T052 [P] [US4] Add failing backend parity tests in `gitstore-api/internal/datastore/memdb/serviceaccount_journal_test.go` for provider-neutral create/update/delete journal events, enrollment visibility, immutable UID and equivalent source scope without a new event bus.
- [X] T053 [P] [US4] Add failing finite-retry/live-consumer tests in `gitstore-api/internal/auth/provider/kratos/consumer_test.go` for success advancement, exhausted retries/discard, bounded buffer overflow, continued later events, cancellation/reload and startup at the current boundary with no persisted cursor or catch-up. Registry reload/callback coverage reuses `provider_registry_test.go`.
- [X] T054 [P] [US4] Add failing mirror/authority tests in `gitstore-api/internal/auth/provider/kratos/serviceaccounts_test.go` and `tests/integration/user_directory_serviceaccount_test.go` for immutable UID-derived external_id, no credentials/login/recovery/verification, duplicate/stale/out-of-order writes, preserved directory groups, finite retry/discard/restart and disabled/deleted/replaced source accounts across replicas.

### Implementation

- [X] T055 [US4] Enable full preimage/postimage CDC with the existing 14-day TTL only on `service_accounts_by_cluster_name` through the next reviewed incremental file, currently `gitstore-api/internal/datastore/scylla/migrations/013_service_account_cdc.cql`; recheck numbering when implementing, preserve applied `009_service_account.cql` checksums and test fresh/upgrade/startup-validation paths without new application tables or data deletion. Exact native upgrade/read-only/rollback-history coverage reuses `migration_test.go`.
- [X] T056 [US4] Extend the existing shared source/materializer integration in `gitstore-api/internal/datastore/scylla/catalog_cdc.go` and `serviceaccount.go` for cluster-scoped ServiceAccount events, gating completed enrollment/projection visibility and retaining deletion UID; reuse shared producer fencing/progress and adapt source scope instead of creating a raw Kratos CDC reader, standalone runner or private journal.
- [X] T057 [US4] Publish equivalent provider-neutral source events from `gitstore-api/internal/datastore/memdb/backend.go` through its existing `namespace_watch.go` journal implementation, preserving authoritative mutation/enrollment ordering and no process-local fan-out replacement.
- [X] T058 [US4] Implement idempotent UID-linked mirror application in `gitstore-api/internal/auth/provider/kratos/serviceaccounts.go`; use bounded current-source reads/revision checks, protected canonical subject/name metadata and selected-pin external_id lookup, preserving directory groups and never copying human credentials or workload private keys. Native acceptance runs through `owned_receiver_test.go` and `tests/integration/user_directory_serviceaccount_test.go`.
- [X] T059 [US4] Implement direct bounded journal subscription/application in `gitstore-api/internal/auth/provider/kratos/consumer.go`; retain only process-local position/retries, use a small fixed finite retry budget with bounded backoff/deadlines, emit redacted drop outcomes and move on after exhaustion/overflow without DLQ, SaveProgress extension, consumer fencing, inventory reconciliation or startup replay.
- [X] T060 [US4] Wire active profile-capable provider consumer startup/reload/shutdown in `gitstore-api/internal/app/server.go` and `provider_registry_test.go`; keep shared publication independent of provider selection and native ServiceAccount commits independent of Kratos, with no GraphQL/WebSocket self-client or checkpoint directory.
- [X] T061 [US4] Preserve authoritative ServiceAccount UID/key/enablement checks during enrichment and actor hydration in `gitstore-api/internal/auth/provider/kratos/serviceaccounts.go`, `gitstore-api/internal/middleware/security/secure.go` and `gitstore-api/internal/admin/graph/resolver/actor_service.go`; stale/missing mirrors cannot restore disabled/deleted identities or transfer old-name groups. UID-linkage/current-authority regressions reuse `user_directory_contract_test.go`, `serviceaccounts_test.go`, existing JWT/assertion denial suites and `actor_contract_test.go`.
- [X] T062 [US4] Extend SDK-backed signals in `gitstore-api/internal/auth/provider/kratos/metrics.go` for delivery attempts, latency, buffer saturation/overflow and exhausted/discarded outcomes; distinguish producer progress from best-effort consumer results without per-identity labels or a claimed durable acknowledgement.
- [X] T063 [US4] Execute live fault/name-reuse cases in `tests/integration/user_directory_serviceaccount_test.go`, including concurrent replicas, provider outage, process crash, restart from the current boundary and successful later events; accept absent/stale/orphaned mirrors after lost events but never an authoritative access bypass.
- [X] T064 [US4] Execute `make test TARGET=datastore` and isolated `DATASTORE=scylla` contracts for `gitstore-api/internal/datastore/scylla/serviceaccount_cdc_test.go`, then the focused consumer/authority suites; verify shared journal behavior for existing kinds is unchanged and no ServiceAccount watch endpoint, durable consumer table or checkpoint volume was introduced. Real reader coverage reuses `namespace_watch_integration_test.go`.

**Checkpoint**: Healthy delivery creates/updates credentialless mirrors; fault behavior is explicitly retry-and-discard. Producer durability is not promoted into UserDir consumer durability.

## Phase 7: US5 - Identity mail flows through GitStore to a notification provider (P3)

**Goal**: Use selected-pin Kratos HTTP courier delivery through an authenticated bounded GitStore adapter and a development mail provider; resolve contact facts by subject.

**Independent test**: Trigger reference-stack verification mail and observe receipt in the existing local catcher. Reject unauthenticated/malformed/oversized/saturated requests, preserve the old deliverable address during replacement verification and resolve bounce/consent facts by subject.

### Contract and tests first

- [X] T065 [US5] Finalize the selected-pin HTTP courier request template, internal Admin-listener mail route, method/API-key header, payload/deadline/concurrency bounds and error statuses in `specs/065-kratos-user-directory/contracts/kratos-mail.md`; add failing request/authentication/limit/redaction tests in `gitstore-api/internal/notification/mail_handler_test.go` before implementing the handler.
- [X] T066 [P] [US5] Add failing development-provider success/outage/saturation tests in `gitstore-api/internal/notification/development_test.go` and subject-keyed contact/verification/consent cases in `tests/integration/user_directory_mail_test.go`, allowing at-least-once courier duplicates without asserting exactly-once delivery.

### Implementation

- [X] T067 [US5] Define the minimal notification contract in `gitstore-api/internal/notification/provider.go` and implement only the bounded development mail-catcher transport in `development.go`; reuse the reference catcher and standard transports rather than adding Novu, a production delivery service or a persistent queue.
- [X] T068 [US5] Implement the reviewed authenticated mail adapter in `gitstore-api/internal/notification/mail_handler.go`, authenticating before body parsing, bounding content type/body/in-flight work and returning success only after provider acceptance; redact recipients, bodies, tokens, headers and recovery/verification secrets.
- [X] T069 [US5] Wire the internal Admin-listener mail route and required injected mail credential/provider settings in `gitstore-api/internal/app/server.go` and `gitstore-api/internal/config/config.go`; keep it off the Storefront schema and preserve existing endpoint body/routing limits.
- [X] T070 [US5] Configure selected-pin HTTP courier templates/URL/authentication in `config/oidc/kratos/kratos.yml`, `config/oidc/oidc.env.example` and `compose.oidc.yml`, keeping directory administration internal and secrets out of committed configuration and health checks.
- [X] T071 [US5] Complete subject-keyed deliverable-email/consent resolution in `gitstore-api/internal/auth/provider/kratos/profile.go` and `administration.go`; verify trusted email replacement, bounce updates and consent semantics through the existing directory contract without using email as a recipient identity or adding a profile database.
- [X] T072 [US5] Add bounded SDK-backed outcome/duration/template-class/correlation signals in `gitstore-api/internal/notification/mail_handler.go`, using shared observability and never logging message subject, recipient, body, credentials or recovery codes.
- [X] T073 [US5] Execute the authenticated courier/provider/contact scenarios in `tests/integration/user_directory_mail_test.go` against the exact reference pins, including duplicates, malformed/oversized payloads, outages and successful local catcher receipt.
- [X] T074 [US5] Document the supported development HTTP courier and subject-keyed contact contract in `docs/configuration.md` and `docs/api-reference.md`, explicitly deferring production notification providers and distinguishing mail's at-least-once behavior from best-effort ServiceAccount events.

**Checkpoint**: Identity mail and contact contracts work through the development provider; this does not ship a production notification service or a new identity-migration workflow.

## Phase 8: Polish and cross-cutting acceptance

**Purpose**: Verify the combined feature, document real operational limits and preserve existing readiness/rollout contracts.

- [X] T075 [P] Update `docs/configuration.md`, `docs/authorization.md` and `docs/developer-guide.md` with provider-independent configuration, reference OIDC/Kratos shared-store setup and operator warnings: discourage `static-users` AuthN/UserDir and `none` UserDir in production, including mixing static users with Kratos/oidc-jwt; explicitly document retry-and-discard, indefinitely stale/missing/orphaned mirrors, incomplete unified actor lists and GitStore ServiceAccount authority without pretending these combinations are configuration-invalid.
- [ ] T076 Extend existing readiness fixtures/preflight/domain verification in `tests/capacity/preflight/api-readiness.sh`, `tests/capacity/profiles/api-readiness.js` and `tests/capacity/README.md` for seeded identity counts, two APIs, actual principal/group/action receipts, directory call/cache limits and sustained event handling; reuse `scripts/run-capacity.sh` selectors and preserve all existing topology/dataset/resource gates.
- [X] T077 Add a bounded explicitly owned identity-service restart/pause experiment under `tests/chaos/profiles/identity-restart.json` and update `tests/chaos/README.md`, reusing `scripts/run-chaos.sh`; require explicit ownership/target/confirmation, restoration receipts and verifier-decided existing-profile authentication recovery within 30 seconds, with best-effort drops rather than an orphan-repair guarantee.
- [ ] T078 Execute mixed-version/concurrent-replica replacement and provider-switch acceptance in `tests/integration/user_directory_rolling_upgrade_test.go`, covering enrichment, public/management boundaries, actor reads, admin writes, live consumer cancellation and stale workload authority; keep exactly one unaffected Git service and do not claim controller or Git HA beyond exercised paths.
- [ ] T079 Run `make capacity TARGET=api PROFILE=readiness MODE=alpha` with T076's owned 1,000-staff/100,000-customer/100-ServiceAccount fixture and correctness receipts, then separately prove the declared 1,000,000-customer/1,000-ServiceAccount envelope; record attributable latency/call-rate/buffer/resource evidence in `specs/065-kratos-user-directory/plan.md`, not an operator run diary.
- [ ] T080 Run T077's confirmed identity faults and existing API replacement faults through `make chaos` while T079's bounded workload is active; record actual two-replica isolation, 30-second existing-profile authentication recovery, finite delivery drops and source authority in `specs/065-kratos-user-directory/plan.md`, explicitly marking any unexecuted acceptance path.
- [ ] T081 Execute the complete `specs/065-kratos-user-directory/quickstart.md` flow, including dual login, invitations/all lifecycle states, forward Relay pages/unified backward diagnostics, ownership/counters, workload authority/retry/discard and authenticated HTTP mail; update `quickstart.md` with supported behavior only and keep captured credentials/messages/private receipts outside versioned files.
- [X] T082 Run `make generate TARGET=all`, `make test`, `make lint`, `make check TARGET=all` and `git diff --check` for the changed schemas/API/configuration/tests, including `shared/schemas/admin/user.graphqls` and `gitstore-api/internal/auth/provider/kratos/provider.go`, plus `make pr-ready` before any PR creation; verify generated Admin/Storefront/catalog drift checks, no internal references in consumer docstrings and `graphify update .` after code changes.
- [ ] T083 Reconcile implementation and ADR/spec lifecycle in `specs/065-kratos-user-directory/spec.md`, `plan.md` and `docs/ADRs/README.md`: close the spec only after required outcomes/gates pass, and include a `Proposed` to `Accepted (YYYY-MM-DD)` status flip in the implementing PR only for any ADR whose core decision is actually implemented, following acceptance dating rules. Existing ADRs 0010/0012 stay accepted; credentialless mirrors alone do not implement ADR 0020's issuer-client/assertion/token-hook decision, so do not accept it or add that work.
- [X] T084 Audit downstream configuration/schema compatibility in `compose.oidc.yml`, `config/oidc/oidc.env.example` and `docs/development/make-reference.md`; flag `gitstore-dev/quickstart` follow-up for copied configuration/wiring, preserve supported selector families and explicitly document lifecycle-schema rollout/rollback limits without committing or publishing changes unless requested.

**Checkpoint**: Feature acceptance is measured and persistent, operator limitations are explicit, and unverified production paths are not called complete.

## Dependencies and execution order

### Phase dependencies

1. Setup: T001 selects the release for later capability proofs; T002 verifies the baseline; T003 reviews common contracts. Complete all three before foundation.
2. Foundation: T004/T005/T006 are independent test-definition tasks. T007 follows T004; T008 follows T005 and the reviewed contract; T009 follows T001/T006. Complete foundation before story implementation.
3. US1: T010/T011/T012 form an independent test batch. T013-T017 build the provider/client/mapping/cache/orchestration in order. T018 depends on T016/T017; T019 depends on trusted principal fields; T020 follows T019; T021 follows provider behavior; T022 verifies the story.
4. US2: Depends on US1 enrichment. T023/T024/T025 are independent test files; T026-T029 follow their corresponding tests, and T030 verifies the increment.
5. US3: Depends on US1 enrichment, not US2 completion. T031-T035 use separate test files. T031 gates T036/T039/T040; T034 gates T041-T043. T036/T037 precede T038/T040/T044; T038/T039 precede T040. Provider administration T041-T043 precedes T044, then audit/source-type enforcement and final integration/checks follow.
6. US4: Producer/consumer implementation depends on foundation and US1, not customer registration or human lifecycle administration. T051-T054 are an independent test batch. T055 precedes T056; T057 is the independent MemDB implementation after T052; T058/T059 precede T060. Public actor-hydration checks T061/T063 also require US3's read slice T036-T040, but the stream itself does not depend on invitation/lifecycle mutations. Directory-group administration through the user API is verified after T044.
7. US5: The delivery adapter T065-T070 can proceed after foundation independently of the ServiceAccount stream. Contact integration T071/T073 depends on US1 and US3's contact slice T042/T043; it does not depend on US4.
8. Final acceptance follows all five story checkpoints. T075 is independent documentation work once contracts stabilize. T079 depends on T076 and completed story fixtures; T080 depends on T077/T079. T081/T082 validate the integrated outcome, and T083/T084 finish lifecycle/compatibility handoff only after actual evidence.

### Story completion graph

```text
Setup -> Foundation -> US1 (MVP) -> US2
                         |
                         +-> US3 -- read slice ----> US4 public-read acceptance
                         |      \- contact slice -> US5 contact acceptance
                         +-> US4 stream/mirror core

Foundation ---------------------> US5 delivery adapter

US1 + US2 + US3 + US4 + US5 -> Combined acceptance -> Lifecycle/handoff
```

The arrows represent real shared contracts, not a requirement to finish every lower-priority story before independent work can begin. Public GraphQL source reads and provider event consumption are different boundaries.

### Within each story

Tests precede corresponding implementation. Resolve release-capability gates before dependent public schema/handlers. Generate model/catalog changes before compiling dependent resolvers/authorizers. Validate a story's independent scenario before treating its checkpoint as complete.

Tasks touching shared files such as `profile.go`, `config.go`, `server.go`, `actor_service.go`, `user.resolvers.go`, `compose.oidc.yml` or the same test file must be serialized even when their story branches are otherwise independent. A `[P]` batch does not authorize concurrent edits to the same file or running stateful tests against the same keyspace/deployment.

## Parallel execution examples

| Story | Independent task batch | Why it is safe |
|---|---|---|
| US1 | T010 provider contracts; T011 security/registry contracts; T012 integration contracts | Separate files; each uses the completed foundation |
| US2 | T023 public-flow cases; T024 admission cases; T025 dual-login cases | Separate test files; all use the US1 profile contract |
| US3 | T031 pagination proof; T032 schema/IDs; T033 canonical actions; T034 lifecycle/admin; T035 API races | Separate files/fixtures; capability results gate later implementation, not these test definitions |
| US4 | T051 Scylla producer; T052 MemDB producer; T053 live consumer; T054 mirror authority | Separate test files; use isolated backend fixtures and no shared deployment ownership |
| US5 | T065 mail-handler contract/tests; T066 development-provider/contact tests | Separate files; handler/provider tests require only foundation and contact integration runs after its explicit prerequisites |

Execute each batch's test-definition work together, then observe the corresponding expected failures and implement dependencies. These are planning examples, not instructions to launch agents or mutate shared deployments automatically.

## Implementation strategy

### MVP first

Complete setup/foundation and US1. Provision identities directly through trusted test/reference mechanisms; verify the mapped staff's group-bound authorization and fail-closed/freshness behavior. This is the MVP; do not pull in actor pagination, mail, workload recovery or a new persistence mechanism to demonstrate it.

### Incremental delivery

Add US2 customer isolation, US3's actor-read and administration slices, US4's shared producer/best-effort mirror handling and US5's courier/contact integration. Each increment retains its explicit independent acceptance scenario. US4 stream work and US5 delivery-adapter work can proceed alongside US3 where files and prerequisites do not overlap.

### Validation and rollout

Use focused existing Go package suites during implementation, the canonical `make test TARGET=datastore` backend selectors for datastore contracts and full aggregate checks at T082. Deployed integration/capacity/chaos work is opt-in and must own its fixtures. A local fake, a diagnostic receipt, a skipped deployed scenario or a healthy seed is not evidence of multi-replica fault acceptance. Successful seeded profile resolution does not promise recovery of discarded events.

Do not silently weaken Relay/lifecycle/capacity gates to close the spec. Record unsupported capabilities, skipped faults and version compatibility honestly. Preserve existing durable journal consumers and singleton Git storage; no extra user database, controller, replay checkpoint or repair operator setup is introduced.
