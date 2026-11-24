# Tasks: Git-Backed RBAC and Embedded OPA

**Input**: Design documents in `specs/066-git-backed-rbac/`.
**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md),
[research.md](research.md), [data-model.md](data-model.md),
[resource API](contracts/resource-api.md),
[admission/storage](contracts/admission-storage.md),
[snapshot/provider](contracts/snapshot-provider.md) and
[capacity/recovery](contracts/capacity-recovery.md).
**Tests**: Required by the feature's acceptance scenarios and Constitution
Principle I. Write the named tests first and demonstrate the expected failure
before implementing the corresponding behavior.
**Organization**: Setup, shared foundation, then P1 stories US3, US2, US1,
US6; P2 story US4; P3 story US5; final correctness acceptance. The P1 order
puts safe common admission before exposing writes and runtime convergence.

**Owner decision (2026-11-21):** production performance/capacity acceptance
moves to #451. T096/T098 and T097's thirty-minute fault-load portion are
deferred, not passed or retired. T097's native functional recovery proof
and T103's correctness closeout remain feature requirements. Configured
freshness/revocation/resource bounds, no-wrong-grant checks and the declared
functional recovery deadline are unchanged. Earlier experiment narratives
retain their original outcomes; they do not override this delivery scope.

## Format and execution rules

Every task uses `- [ ] Tnnn [P?] [USn?] Description with paths`.
`[P]` identifies file-disjoint work that can run with other ready tasks in
the same named parallel batch; it never waives prerequisites or test-first
ordering. Unmarked tasks run sequentially within their phase unless the
dependency section explicitly permits a separate branch.

Paths are repository-relative. New RBAC files named below are planned
implementation destinations; extend existing helpers rather than duplicate
them. Generated files must come from the existing generators. Read the
applicable scoped `AGENTS.md` before implementation. This file schedules
implementation; no checkbox implies that code or acceptance already exists.

## Phase 1: Setup

**Goal**: Establish reproducible dependencies, reusable test inputs and a
disabled-by-default integration boundary.

- [X] T001 Record the pre-OPA API binary/image baseline with identical architecture/build flags in `specs/066-git-backed-rbac/research.md`, then add OPA v1.22.2 through the existing Go dependency tooling in `gitstore-api/go.mod` and `gitstore-api/go.sum`; review transitive changes and preserve existing shared-module replacements.
- [X] T002 [P] Add model-v1 fixture inputs for all four kinds, namespace UID recreation, unresolved references, conditional rules and subject/group cases in `gitstore-api/internal/rbac/testdata/model-v1.json`; reuse the existing provider-neutral cases in `gitstore-api/internal/auth/provider/rbaclocal/rbaclocal_test.go` without weakening their expected outcomes.
- [X] T003 [P] Add failing tests for OPA selection, invalid limits, disabled dynamic writes and unchanged static-provider defaults in `gitstore-api/internal/config/config_test.go` and `gitstore-api/internal/app/provider_registry_test.go`; distinguish configuring an unavailable provider from silently falling back to another provider.
- [X] T004 Implement the tested provider/limit configuration and disabled dynamic-RBAC rollout boundary in `gitstore-api/internal/config/config.go`, `gitstore-api/internal/app/server.go` and `config/config.toml`; keep unsupported/incomplete OPA startup fail-closed and preserve AuthN/UserDir/AuthZ independence.

**Checkpoint**: Existing deployments retain their behavior. Contract work can
land without activating dynamic writes or serving an incomplete provider.

## Phase 2: Foundational contracts and durable model

**Goal**: Establish shared contracts, atomic state, the fixed evaluation
engine and safe extension points. This phase blocks story implementation.

### Tests first

- [X] T005 [P] Add failing strict model/schema tests in `gitstore-api/internal/rbac/model_test.go` for API version, all four envelopes, canonical paths, wrong repository scope, unknown/deny/legacy fields, partial wildcards, reserved subjects, immutable roleRef, duplicate identities and payload/rule/subject limits.
- [X] T006 [P] Add backend-neutral transaction/snapshot/reference-guard contracts in the existing shared harness at `gitstore-api/tests/contract/datastore/rbac_test.go` and native cases in `rbac_scylla_test.go`; cover stale CAS, concurrent quota admission, all-or-none rows/counts/receipts, status races, inconsistent pages, response loss and claim-vs-termination recovery.
- [X] T007 [P] Add fixed-policy tests in `gitstore-api/internal/auth/provider/opa/policy_test.go` using the unchanged provider-neutral decision expectations: invalid actions, scoped wildcards, resourceNames, all condition forms, owner/target-owner precedence, subject kinds, trusted masters, terminating resources and undefined/error output.

### Implementation

- [X] T008 [P] Define `schemas/rbac.authorization.gitstore.dev/v1beta1/role.schema.json`, `clusterrole.schema.json`, `rolebinding.schema.json` and `clusterrolebinding.schema.json`; register CRUD/watch, bind/escalate, purge/status and scoped review capabilities in `shared/schemas/authorization-actions.json`, including their correct scope and controller applicability.
- [X] T009 [P] Implement provider-neutral model-v1 envelopes, deterministic canonicalization and bounded parsing in `gitstore-api/internal/rbac/model.go` and `gitstore-api/internal/rbac/manifest.go`; reuse existing v1beta1 rule/condition validation from `gitstore-api/internal/auth/provider/rbaclocal/policy.go` without creating another decision evaluator or changing static policy semantics.
- [X] T010 Define `RBACStore` and its optional existing-datastore capability in `gitstore-api/internal/datastore/rbac.go` and `instrumented_rbac.go`: typed envelopes/get/list, whole-tier conditional apply, lifecycle CAS, complete snapshot, projection receipt/repair and role-UID claim operations. Keep storage DTOs independent of auth/provider imports and expose domain aliases in `internal/rbac/store.go`.
- [X] T011 Add `gitstore-api/internal/datastore/scylla/migrations/014_rbac.cql` after rechecking that 014 is still unused; define canonical RBAC resources with operation-audit columns, query-first UID/reference projections, role guards, UID-bound namespace cleanup handoffs, same-partition revision/count sentinels and authoritative full-image CDC with 14-day retention. With explicit user approval, consolidated the unreleased namespace-cleanup migrations. For operation audit, normally applied 015 to the owned development keyspace before folding its columns into 014 and reconciling only that ledger under the existing migration lock. All 138 canonical rows, schema columns and migration entries 001-013 were preserved; consolidated 014 retains its original start time and 015's completion time. Retained-keyspace `ValidateSchema` passes and normal `RunMigrations` is a no-op with unchanged ledger write timestamps. Preserve every other deployment's migration history; shared memdb/Scylla audit contracts, fresh/upgrade migration checks, native CDC images and legacy-payload retention remain required validation.
- [X] T012 [P] Implement memdb whole-candidate transactions, UID/resourceVersion checks, tier quota counts, repairable projections and stable paged snapshots in `gitstore-api/internal/datastore/memdb/rbac.go` and `gitstore-api/internal/datastore/memdb/schema.go`; include atomic role-UID lifecycle/claim operations in `rbac_guards.go` so all T006 store contracts pass before the native gate.
- [X] T013 [P] Implement Scylla conditional batches and bounded query-first reads in `gitstore-api/internal/datastore/scylla/rbac.go`, sharing mutation planning through `internal/rbac/store_plan.go`, including role-UID lifecycle/claim operations in `rbac_guards.go`; use consistency levels that cannot miss completed conditional writes and never emulate atomic publication with a per-row write loop.
- [X] T014 Run `make test TARGET=datastore` and `GOFLAGS=-p=1 make test TARGET=datastore DATASTORE=scylla SCYLLA_TEST_ADDR=<owned-address>` against the existing shared harness in `gitstore-api/tests/contract/datastore/rbac_test.go` and `rbac_scylla_test.go`; prove native batch restrictions, migration splitting, CDC images and lost-response behavior before proceeding, leaving this gate blocked if native Scylla is unavailable. Package serialization avoids competing schema-heavy suites on the one-shard owned fixture; in-test writer concurrency remains enabled.
- [X] T015 Extend shared journal payload discrimination and memdb delivery in `gitstore-api/internal/watchjournal/` and `gitstore-api/internal/datastore/memdb/rbac.go` for the four RBAC kinds and tier progress signals; keep internal sentinels out of public resource events and retain resumable cursors.
- [X] T016 Register the authoritative RBAC CDC source in `gitstore-api/internal/datastore/scylla/catalog_cdc.go` and extend its existing tests to cover reordered/duplicate rows, sentinel progress, full images and replay into the shared journal; do not add a per-kind journal or another CDC daemon.
- [X] T017 Implement committed-candidate projection receipts and bounded idempotent repair in `gitstore-api/internal/admission/rbac_projection.go`, `gitstore-api/internal/admission/committed_manifest.go` and `gitstore-api/internal/datastore/scylla/repair.go`; preserve committed Git authority after projection failure and repair lookup/reference projections from authoritative rows.
- [X] T018 Implement complete model-v1 snapshot acquisition in `gitstore-api/internal/rbac/snapshot.go` using sentinel-before/pages/sentinel-after, namespace UID/lifecycle, canonical Git/projection identity, catalog/built-in digests, 1,000-row pages, 64 MiB tier limit and at most three retries within a 30-second validation deadline.
- [X] T019 Implement the fixed embedded module, prepared named decision query and deterministic indexed model-v1 translation in `gitstore-api/internal/auth/provider/opa/policy.rego`, `policy.go` and `translation.go`; allow only required deterministic built-ins, prohibit remote/operator policy loading and make malformed/undefined/error results unavailable rather than allowed.
- [X] T020 Implement release-owned built-in definitions and system bindings in `gitstore-api/internal/rbac/builtins.go`, preserving trusted identity semantics and `namespace-owner -> system:authenticated` versus `namespace-creator -> system:staff`; give namespace admin its scoped RBAC/review capabilities, retain edit/view restrictions and derive deterministic release identities/digests.
- [X] T021 Add failing claim-before-resolution and pending-claim recovery tests in `gitstore-api/internal/rbac/references_test.go`, then implement coordination in `references.go` using T012/T013's atomic store operations; acquire while live before publishing resolution, close acquisition on termination, release after grant suppression and reconcile uncertain pending claims conservatively after crashes.
- [X] T022 Implement shared built-in/provenance/lifecycle guards in `gitstore-api/internal/rbac/protection.go`; reject forged managed identity, immutable-roleRef changes, cross-UID attachment and unauthorized status/finalizer writes, keeping managed-template maintenance unavailable until the verified controller path in US6 exists.
- [X] T023 Define the four resource envelopes, scoped selectors, Relay connections, action-specific mutations, status/purge inputs and watch payloads in `shared/schemas/admin/rbac.graphqls`; follow existing Node/metadata/error conventions, keep them Admin-only and leave the transient review types to US5.
- [X] T024 Extend optional pinned-evaluation, conservative-coverage and snapshot-diagnostic capabilities in `gitstore-api/internal/auth/types.go` and `gitstore-api/internal/auth/registry.go`, with tests for providers that omit them; keep `AuthZProvider.Authorize` compatible and reject unsupported dynamic validation rather than inventing evidence or merging providers.
- [X] T025 Extend `shared/proto/gitstore/catalog/v1/catalog_service.proto` only where current validation messages lack bounded candidate/tree/OID/actor metadata; preserve receiver-bound signatures and whole-receive identity, use existing Buf generation for Go/Rust stubs and add compatibility tests for missing or inconsistent metadata.
- [X] T026 Run `make generate TARGET=all`, the foundation tests and generated-source checks for `shared/schemas/admin/rbac.graphqls`, `shared/schemas/authorization-actions.json` and `shared/proto/gitstore/catalog/v1/catalog_service.proto`; verify existing static-provider fixtures still pass and document the completed native-store gate in `specs/066-git-backed-rbac/contracts/admission-storage.md`.

**Checkpoint**: Contracts and native store semantics are established; the
fixed engine can be exercised on immutable test snapshots. Production OPA
activation, transport admission and lifecycle reconciliation remain disabled
until their story tasks land.

## Phase 3: US3 - Prevent privilege escalation (P1)

**Goal**: Make common candidate validation safe before attaching it to both
write transports. This is the first P1 slice because later writes depend on it.
**Independent test**: Invoke common admission against real immutable store
snapshots; a caller lacking product purge cannot grant it, cannot bind
broader authority, but can delegate provably covered edit permissions.
Transport-level repetition belongs to US2; deployed enforcement follows US1.

### Tests first

- [X] T027 [P] [US3] Add conservative-coverage cases in `gitstore-api/internal/auth/provider/opa/coverage_test.go`: equal/broader scope, every expanded finite action, resource-name supersets, unconditional coverage of a conditional candidate, caller-owner predicates that do not cover another subject, unresolved-role bind and providers with no proof capability.
- [X] T028 [P] [US3] Add authoritative-capture and race tests in `gitstore-api/internal/rbac/validation_test.go`: projection/Git mismatch, stale ordinary caches, capture metadata, candidate self-grants, changed-candidate recapture, validation cancellation/deadline and revocation committed after capture.
- [X] T029 [P] [US3] Add common-admission authorization matrix tests in `gitstore-api/internal/admission/rbac_test.go` for role/binding create/update/delete, explicit bind/escalate, cluster-wide containment, missing UserDir subjects, protected resources and whole-candidate rejection.

### Implementation

- [X] T030 [US3] Implement authoritative validation-start capture and pinned evaluation in `gitstore-api/internal/rbac/validation.go`; require current canonical Git tips and matching complete projections, preserve individually captured tier identities, bypass watch caches, bound retries/deadlines and recapture for every new candidate after Git conflict.
- [X] T031 [US3] Add the fixed named coverage query to `gitstore-api/internal/auth/provider/opa/policy.rego` and its optional capability adapter in `coverage.go`; accept ordinary delegation only with unconditional covering authority at sufficient scope and name supersets, otherwise require the appropriate bind/escalate permission.
- [X] T032 [US3] Implement one whole-candidate admission validator in `gitstore-api/internal/admission/rbac.go`; authorize operations from pre-change captured authority, validate final-tree references separately, require explicit bind for absent roles, enforce all scope/path/size/quota/protection checks and never let a candidate authorize itself.
- [X] T033 [US3] Integrate the role-UID guards and deletion suppression into `gitstore-api/internal/admission/rbac.go` and `gitstore-api/internal/rbac/protection.go`; a target terminating after capture may leave an authorized binding unresolved but must never force a resolved link or revive a terminating grant.
- [X] T034 [US3] Demonstrate the selected race in `gitstore-api/internal/rbac/validation_test.go`: a captured authorization may finish after a cross-repository revocation, a new attempt sees the revocation and no expired attempt is reusable; do not add a global lock, commit fence or distributed Git intent protocol.
- [X] T035 [US3] Run the focused coverage/admission tests, including their concurrency cases, and record the implemented proof boundary in `specs/066-git-backed-rbac/contracts/admission-storage.md`; distinguish a policy denial from retryable authorization unavailability and retain structured bounded rejection reasons.

**Checkpoint**: Common admission is safe and independently testable.
Public write paths are not considered complete until US2 passes the same
matrix through Git and GraphQL.

## Phase 4: US2 - Manage RBAC through Git and GraphQL (P1)

**Goal**: Expose identical, attributable and recoverable Git-backed resource
operations through both transports.
**Independent test**: Push a Role/RoleBinding, read the admitted commit and
projection, reject an invalid multi-manifest candidate, and revert it; repeat
equivalent mutations through Admin GraphQL using the same validator and
prepared-engine fixture. Runtime propagation is verified in US1.

### Tests first

- [X] T036 [P] [US2] Add native hook tests in `gitstore-git-service/src/git/hooks/validation_handler.rs` for canonical RBAC YAML detection without catalog frontmatter, old/new tree metadata, deletes/renames, final-tree validation, actor spoofing, noncanonical branches and atomic multi-ref support or whole-push rejection.
- [X] T037 [P] [US2] Add Admin resource contract tests in `gitstore-api/internal/admin/graph/resolver/rbac_contract_test.go` for all four CRUD families, Node/nodes, scoped Relay lists, optimistic conflicts, metadata/author attribution, partial-outcome errors and rejection from Storefront.
- [X] T038 [P] [US2] Add committed-write/recovery tests in `gitstore-api/internal/cataloggrpc/rbac_projection_test.go` for commit-before-projection failure, response loss, repair replay, conflicting Git tips and no partially published tier.

### Implementation

- [X] T039 [US2] Extend native gix candidate extraction and blocking pre-receive wiring in `gitstore-git-service/src/git/hooks/validation_handler.rs` and `gitstore-git-service/src/git/hooks/mod.rs`; include RBAC manifests/removals/renames under the fixed paths, validate the receive as a unit and reject unsupported non-atomic multi-ref RBAC pushes before any ref changes.
- [X] T040 [US2] Wire signed complete-tree `ValidateRBACReceive` candidates to shared RBAC admission in `gitstore-api/internal/cataloggrpc/server.go` and `gitstore-api/internal/cataloggrpc/manifest_validator.go`; authenticate the forwarded actor, verify repository/ref/OID identity, reject unsupported metadata and keep post-receive notification separate from the security veto.
- [X] T041 [US2] Implement create/update/delete mutations in `gitstore-api/internal/admin/graph/resolver/rbac.resolvers.go` and `rbac_service.go` using the existing commit/admission helpers, fixed paths and expected old OID; preserve caller attribution and return the committed revision plus partial-outcome diagnostics when projection fails.
- [X] T042 [US2] Implement scoped resource get/list, Node/nodes dispatch and virtual/persisted merge in `gitstore-api/internal/admin/graph/resolver/rbac_service.go`, `global_id.go` and `schema.resolvers.go`; reuse existing keyset selectors/cursors, authorize before disclosing existence and keep pages stable within their declared revision.
- [X] T043 [US2] Implement authorized status/finalizer and complete-deletion endpoints in `gitstore-api/internal/admin/graph/resolver/rbac.resolvers.go` and `status_generic.go`; enforce controller-owned fields, UID/resourceVersion conditions, purge authority and no-claim guards independently of external spec permissions.
- [X] T044 [US2] Connect admitted commits to idempotent whole-tier projection and typed/generic durable watches in `gitstore-api/internal/cataloggrpc/committed_manifest.go`, `gitstore-api/internal/admin/graph/resolver/watch.go` and `gitstore-api/internal/datastore/scylla/catalog_cdc.go`; expose no per-process fan-out or internal sentinel resources.
- [X] T045 [US2] Implement bounded GraphQL conflict retry with fresh candidate capture in `gitstore-api/internal/admin/graph/resolver/rbac_service.go` and test concurrent push/GraphQL updates in `rbac_contract_test.go`; never reuse an authorization from a superseded candidate or retry a successful unknown-outcome commit as a new resource.
- [X] T046 [US2] Regenerate the Admin schema and protobuf consumers with `make generate TARGET=all`, then run focused Rust hooks, Admin resolver and catalog gRPC suites for `shared/schemas/admin/rbac.graphqls` and `gitstore-api/internal/cataloggrpc/rbac_projection_test.go`; preserve existing catalog hook behavior.
- [X] T047 [US2] Add and run cross-transport parity/revert tests in `tests/integration/rbac_git_graphql_test.go` for all four kinds, the US3 escalation matrix, accepted manifests, wrong locations, immutable roleRef, whole-push rejection and committed-write repair; distinguish constructed common-admission fixtures from deployed watch/OPA convergence.

**Checkpoint**: Git and GraphQL exercise the same safe, versioned resource
pipeline. Do not enable production dynamic writes before the OPA readiness,
bootstrap and cutover gates.

## Phase 5: US1 - Grant and revoke at runtime (P1)

**Goal**: Every API evaluates current translated data locally and converges
on grants/revocations without restarts or request-time storage reads.
**Independent test**: In an initialized owned two-API fixture, bind a group
to `edit`, observe allowed product writes on both replicas, delete the
binding and observe denial on both APIs and an existing subscription.

### Tests first

- [X] T048 [P] [US1] Add provider-publication tests in `gitstore-api/internal/auth/provider/opa/provider_test.go` for exact evaluated revisions, concurrent replacement/pinned readers, unknown actions, terminating tiers, static/dynamic non-merging and no datastore/UserDir/network reads during ordinary decisions.
- [X] T049 [P] [US1] Add snapshot/watch/readiness tests in `gitstore-api/internal/rbac/manager_test.go`: capture-before-scan races, newer buffered dirty revisions, partial lists, lost cursors/epochs, healthy quiet tiers versus missing heartbeats, queue overflow, budget pressure and complete resync.
- [X] T050 [P] [US1] Add two-API grant/revoke and long-lived-subscription tests in `tests/integration/rbac_runtime_test.go`; begin with a preinitialized authorized fixture, use real signed writes, target each replica directly and verify cross-namespace denial and fail-closed cold-start behavior.

### Implementation

- [X] T051 [US1] Add bounded snapshot/index memory accounting in `gitstore-api/internal/auth/provider/opa/translation.go` and `gitstore-api/internal/rbac/budget.go`; reserve a 512 MiB aggregate translated budget and 64 MiB scratch per rebuild before allocation, accounting for pinned readers and old/new replacements rather than only the newest tier.
- [X] T052 [US1] Implement the complete-snapshot/watch bridge in `gitstore-api/internal/rbac/manager.go`: capture a cursor before scan, keep the watch live, retain newer coalesced dirty revisions, verify continuity/source progress and atomically install snapshot/readiness/identity without publishing partial CDC batches.
- [X] T053 [US1] Implement cluster and per-namespace readiness transitions in `gitstore-api/internal/rbac/manager.go` and `gitstore-api/internal/app/bootstrap.go`; missing/partial/stale/cursorless tiers return retryable unavailability, healthy quiet tiers stay usable and reconnect alone never marks a tier Ready.
- [X] T054 [US1] Register the `opa` provider and lifecycle shutdown in `gitstore-api/internal/auth/provider/opa/provider.go`, `gitstore-api/internal/auth/registry.go` and `gitstore-api/internal/app/bootstrap.go`; prepare the fixed policy once, select one provider only and expose OPA to traffic only after complete cluster snapshot/watch readiness.
- [X] T055 [US1] Attach the evaluated revision vector and catalog/built-in identities atomically to decisions in `gitstore-api/internal/auth/types.go` and `gitstore-api/internal/auth/logging.go`; extend existing SDK instruments for rebuilds, lag, recovery, fail-closed reasons and saturation, with finite labels and tests in `gitstore-api/internal/auth/metrics_sdk_test.go`.
- [X] T056 [US1] Add bounded subscription reauthorization in `gitstore-api/internal/admin/graph/resolver/watch.go` for RBAC tier changes, namespace termination and UserDir membership/lifecycle changes; suspend/close on unavailable state and ensure revoked grants cannot continue delivering while resync is pending.
- [X] T057 [US1] Consume Namespace UID/lifecycle invalidation through the shared watch in `gitstore-api/internal/rbac/manager.go`; suppress old-UID grants independently of delayed binding cleanup and prevent namespace-name reuse from selecting a stale translated tier.
- [X] T058 [US1] Enforce four rebuild workers, one rebuild per tier, a 1,024-entry coalesced queue, bounded request/evaluation concurrency and fail-closed overload in `gitstore-api/internal/rbac/manager.go` and `gitstore-api/internal/auth/provider/opa/provider.go`; test saturation, cancellation, pinned-budget reclamation and shutdown without unbounded work.
- [X] T059 [US1] Run cold-boot, cursor-loss and source-progress recovery through the real app/readiness wiring in `gitstore-api/internal/app/rbac_readiness_test.go` and `tests/integration/rbac_runtime_test.go`; verify no trusted-identity fast path bypasses known unavailability and a failed namespace does not falsely mark another tier ready.
- [X] T060 [US1] Add static-provider and controlled-provider-switch regression tests in `gitstore-api/internal/app/provider_registry_test.go` and `gitstore-api/internal/auth/provider/rbaclocal/rbaclocal_test.go`; preserve static file semantics, forbid live provider merging and reject dynamic writes during incompatible/static serving overlap.
- [X] T061 [US1] Run `tests/integration/rbac_runtime_test.go` on two owned APIs sharing native Scylla and one Git service; verify the US1 grant/revoke, subscription and restart scenarios at each admitted revision and keep production activation gated until US4 bootstrap and final rollout/acceptance tasks pass.

**Checkpoint**: Runtime authorization works in an initialized isolated
fixture with real replica delivery. This is the functional MVP, not proof
of fresh-deployment bootstrap or production capacity.

## Phase 6: US6 - Provision personal namespace access (P1)

**Goal**: Staff USER namespace owners receive one protected namespaced
binding through the single provider-neutral lifecycle group.
**Independent test**: Create a Staff USER namespace, verify one owner binding
and access on both APIs, attempt owner/admin tampering, then restart
controllers and delete/recreate the namespace without duplicate or stale-UID grants.

### Tests first

- [X] T062 [P] [US6] Add managed-template admission tests in `gitstore-api/internal/admission/rbac_managed_test.go`: verified Staff subject distinct from username, Customer/ServiceAccount rejection, copied annotations, owner/admin tampering, non-managed collision, wrong role/UID and deletion/revert through either transport.
- [X] T063 [P] [US6] Add controller lifecycle tests in `gitstore-controller-manager/internal/rbac/reconciler_test.go` for duplicate replicas, CAS conflicts, unresolved-to-resolved links, claim acquisition/release crashes, role finalization races, namespace cascades, durable retries and delayed old-UID work.
- [X] T064 [P] [US6] Add two-controller owner-access scenarios in `tests/integration/rbac_owner_access_test.go` for both namespace creation paths, existing USER namespace migration, Pending/Ready/Failed conditions, username/name/owner invariants and isolation despite namespace-full-control wildcard grants.

### Implementation

- [X] T065 [US6] Implement typed RBAC list/watch/status/finalization clients in `gitstore-controller-manager/internal/rbac/graphql_client.go` using `internal/graphqlclient.Client`; consume Admin GraphQL contracts, existing enrolled credentials and bounded paged reads without importing API internals.
- [X] T066 [US6] Register one primary RBAC group in `gitstore-controller-manager/cmd/controller/main.go` and `gitstore-controller-manager/internal/rbac/reconciler.go`; reuse PagedListWatcher, disk-v2 work/index/checkpoint stores, Pond and Backoff, with separate per-replica checkpoint volumes and no provider-specific reconciliation group.
- [X] T067 [US6] Reconcile immutable resolved-role UID links, non-blocking Namespace ownerReferences and `rbac.authorization.gitstore.dev/lifecycle` in `gitstore-controller-manager/internal/rbac/reconciler.go`; claim live role UIDs before publishing resolved links and never silently reattach a same-name recreated role.
- [X] T068 [US6] Implement bounded binding/role finalization and namespace cascade in `gitstore-controller-manager/internal/rbac/reconciler.go`; release claims only after grant suppression, condition final removal on authoritative no-claim state, retain lookup errors as blocking and avoid scanning unrelated namespaces.
- [X] T069 [US6] Enable only the verified managed binding template in `gitstore-api/internal/admission/rbac_managed.go` and `gitstore-api/internal/rbac/protection.go`; constrain enrolled controller write/bind/status/purge capabilities, reject external replacement including administrators, and retain initiating owner/migration attribution alongside the system writer.
- [X] T070 [US6] Provision deterministic `namespace-owner-access` in `gitstore-controller-manager/internal/rbac/owner_access.go` from a live USER Namespace UID and trusted Staff owner subject; use fixed `namespace-full-control`, common admission and durable retries without giving bootstrap bind/escalate rights to the user.
- [X] T071 [US6] Materialize owner-access Pending/Ready/Failed and admitted binding revision through existing Namespace status patches in `gitstore-controller-manager/internal/rbac/owner_access.go` and `gitstore-controller-manager/internal/namespace/graphql_client.go`; distinguish admission success from per-API cache readiness and preserve other controllers' fields.
- [X] T072 [US6] Add bounded paged catch-up for existing live USER namespaces to `gitstore-controller-manager/internal/rbac/owner_access.go`; keep progress/retries durable, fail closed on ordinary-manifest collisions and skip terminating or superseded UIDs without username substitution in `rbac-local`.
- [X] T073 [US6] Run lifecycle/owner tests and the owned two-controller scenarios in `tests/integration/rbac_owner_access_test.go`; replace one controller while retaining its private checkpoint volume without overlap and verify exactly one binding, correct reference cleanup and zero cross-namespace/stale-UID grants.

The owner catch-up/replacement scenario now passes on the owned native fixture. It
creates the USER Namespace before controllers start, injects bounded
pre-execution provisioning/admission failures, checks Pending/Failed/Ready,
and requires replacement to resume private disk-v2 work without another
Namespace snapshot. Post-shutdown read-only checkpoint receipts prove unchanged
active/generation metadata and no staging snapshot; both replicas retain their
private checkpoint directories. The former HTTP list counter was invalid:
RBAC namespace enumeration uses the same full Namespace list query as the
Namespace runner. Bounded empty-range CDC probes now allow the retained APIs to
reach startup readiness without resetting journals or changing source-freshness
gates. The passing run reached BindingAdmitted, restarted both controllers,
verified the managed binding template and usable owner access on both APIs,
denied cross-namespace access and finalized the owned Namespace. Helpers stopped
all processes they launched and retained existing storage. Earlier transient
authorization unavailability is not disproved by this bounded functional run;
sustained freshness remains an acceptance obligation. T064/T073 remain open
for deliberate collision and the complete migration/stale-UID/reference-cleanup
matrix; this does not supersede earlier passing creation-transport evidence
or establish old-writer provenance cutover.

A separate native Namespace-recreation scenario now creates a resolved ordinary
Role/RoleBinding claim before deletion, recreates the same Namespace name,
requires a different managed binding UID on both APIs and reviews the old
subject against a same-name replacement Role. The full unprofiled two-API run
now passes, including fresh managed-binding identity and stale-subject denial.
Cleanup requires both APIs to report the old Namespace UID absent, not merely
name-index disappearance. Earlier availability failures remain diagnostic
history, not successful denials. Guarded recovery retains unrelated storage.

A focused failing regression separately proved that queued namespace rebuilds
could starve installed cluster-origin checks. Fair dispatch now alternates
rebuilds with oldest-due checks within the existing worker and staleness bounds;
RBAC/app race suites and integration vet pass. Further diagnosis found idle
checkpoint contention and repeated signed manifest scans. Progress-only
checkpoints now coalesce for 500 ms without delaying the 25 ms event flush;
one per-runtime 128-entry immutable-digest cache reuses only completely verified
trees while rereading live identity/ref facts. Bounds and replay durability
remain unchanged.

The next native failure exposed a two-controller Namespace finalization race:
a losing resourceVersion CAS restored name/list indexes after the winner
deleted the authoritative UID, blocking recreation and CDC startup. Finalization
now commits the UID CAS before cleanup, with no loser restoration. A committed
full-image CDC deletion confirms authoritative absence and resumes UID-bound
projection cleanup without removing a same-name replacement. The fresh-keyspace
orphan/replacement regression failed before the repair and passes afterward
under race; restart repaired the retained orphan without resetting data.
The full unprofiled recreation and retained-checkpoint catch-up scenarios pass.
Four-package Scylla/RBAC/app/catalog race selectors also pass. T064/T073 remain
open for the complete pre-existing collision/migration/identity/reference matrix;
broader acceptance runs have not started.

The expanded two-API recovery slice now submits an ordinary reserved-name
binding while both controllers' provisioning work is pending: administrators
receive FORBIDDEN, no binding is materialized and no implicit owner grant
appears. After real controller admission, external subject replacement and
deletion also receive FORBIDDEN and preserve binding identity/spec. Catch-up and
expanded recreation pass. One expanded recreation attempt timed out during
final Namespace cleanup with authorization unavailability; exact UID/run-label
recovery finalized it, and the unchanged-bound unprofiled rerun passed. This
observed availability failure is not erased by the functional pass.

Further native coverage now rejects managed-binding subject updates and
deletion by both the owner and administrator through GraphQL and signed Git;
rejected receives preserve the canonical ref. Administrator annotation updates
and the dedicated ownership-transfer mutation cannot change USER ownership.
The expanded retained-checkpoint catch-up run passes with these checks.

A new signed-Git Namespace catch-up scenario provisions its temporary
source-write grant with both controllers, stops them while retaining their
private checkpoints, then creates the Namespace before resuming recovery.
Its bounded runs exposed repeatable availability failures: repository updates
timed out during cleanup, and a protected diagnostic run lost readiness on both
APIs and exhausted the existing clone deadline with HTTP 503. A later
unprofiled expanded GraphQL recreation run also exceeded its Namespace cleanup
deadline. These are failures, not successful denials or acceptance evidence.
Temporary source grants were recovered using exact UID/actor/run-label receipts;
the final failed Namespace subsequently finalized without a data reset.
Source-grant cleanup now runs before the test context is canceled and registers
ownership-checked recovery before creation can partially succeed. Integration
compilation/vet passes; T064/T073 remain open pending native availability
recovery and the complete prior-state collision matrix.

Readiness investigation reproduced unnecessary projection-repair scans after
canonical-source failures in a focused regression. Recovery now writes only
after a proven canonical/projection identity mismatch; lost-callback recovery
still passes. Private stage attribution localized long root reads to immutable
Git tree scans (roughly 10–13 seconds), while datastore snapshot scans took
roughly 2–16 milliseconds. File reads now overlap in bounded pairs without
changing payload limits, retry/deadline budgets or the final live identity
check. The overlap and concurrent Namespace-version rejection regression
failed before the change and passes afterward under race.

On the final uninstrumented source, signed-Git owner catch-up, Namespace
recreation and GraphQL retained-checkpoint catch-up all pass with two APIs and
two controllers. Affected app/RBAC/catalog race tests and repository lint pass.
Already-terminating fixture recovery sent no new deletion mutation; all helper
listeners closed and retained storage was preserved. Earlier failed runs remain
failures, and these functional passes do not establish sustained acceptance.
T064/T073 remain open for the complete prior-state collision matrix.

The prior-state collision policy now distinguishes an ordinary persisted UID
without managed provenance from the protected deterministic managed identity.
Normal authorized deletion of a proven ordinary collision is permitted;
reserved-name creation/adoption, unknown identities and copied provenance
remain rejected. Unchanged ordinary manifests no longer block unrelated
authorized Git writes, and committed projection recovery preserves the
ordinary UID before marking its deletion. Focused authoring regressions failed
before the correction; affected admission/RBAC/catalog/resolver race tests pass.
The paired native prior-state seed and cleanup scenario remains outstanding.

Final-source native revalidation exposed a separate committed-write recovery
gap: a canceled index repair left the authoritative tier committed and pending,
which snapshot capture returned successfully but publication correctly rejected.
The runtime now also invokes recovery for a verified successful snapshot with
pending indexes; unavailable-source failures still never trigger speculative
repair. The committed-pending readiness regression fails without the fix and
passes with it alongside the clean-source/outage cases. Five affected API
package race selectors pass. The subsequent uninstrumented signed-Git owner
catch-up/replacement scenario passes in 46.68 seconds with existing deadlines,
two APIs/controllers, protected managed bindings and exact fixture cleanup.
This remains bounded functional evidence, not sustained acceptance or closure
of the native prior-state collision matrix.

The native ordinary-collision cleanup slice now passes without skips in
37.19 seconds: both APIs preserve the ordinary identity and deny implicit
owner access/unauthorized cleanup; normal administrator deletion permits
deterministic managed reprovisioning and usable owner access. The exact
test-owned Namespace finalizes and all helper listeners close. An earlier
run exceeded the unchanged recovery deadline; that failure remains recorded.
The malformed historical fixture was separately restored using exact
ownership/version guards before normal deletion, with retained Git history
preserved; restoration is not scenario-pass evidence. Integration vet,
repository lint and whitespace checks pass. T064/T073 remain open for the
remaining identity/reference matrix and recovery reliability.

The expanded collision case subsequently passes in 39.75 seconds with explicit
both-API NOT_FOUND checks for the removed ordinary and managed binding UIDs.
A combined final-source owner run passes GraphQL retained-checkpoint recovery
and signed-Git retained-checkpoint recovery, but Namespace recreation times out
waiting for replacement owner access; its deferred cleanup encounters HTTP 503.
Exact UID/run-label recovery then finalizes only that failed test fixture, and
all helper listeners close. T064/T073 remain open: isolated successes do not
override the combined-run availability failure or establish recovery acceptance.
Private failure classification shows Scylla repository/Namespace cancellations
and 11-second timeouts during the combined run; this is diagnostic evidence,
not an established root cause. No staleness/retry/recovery gate was relaxed.

A fresh isolated native deployment now passes all three scenarios together
without skips on unchanged production source: GraphQL retained-checkpoint
catch-up in 33.18 seconds, signed-Git catch-up in 29.69 seconds and Namespace
recreation in 44.62 seconds. Both APIs use the current embedded migrations;
the controller identity and signing material were freshly enrolled rather than
adopting receipts from the lost disposable test database. All owned listeners
close without forced stops, and the persistent test foundation is unchanged.
This does not establish the historical failure's root cause, sustained
availability or the remaining identity/reference matrix; T064/T073 stay open.

After integrating the completed cleanup PRs, the full remaining native owner
matrix passes without skips on fresh owned Scylla keyspaces. Ordinary
prior-state collision cleanup passes in 11.61 seconds, followed by GraphQL
retained-checkpoint catch-up in 25.37 seconds, signed-Git catch-up in 30.22 seconds
and Namespace recreation/reference cleanup in 74.20 seconds. A separate
97.06-second case verifies live Staff identities whose authenticated subject
differs from their directory username, rejects username substitution on both
APIs, and exercises GraphQL recreation plus signed-Git owner recovery. Wrong-name
creation exposed a missing canonical denial code; its focused regression failed
before reusing the existing authorization helper and now passes with the full
security middleware race suite. Controller RBAC/Namespace race suites and
integration vet also pass. All owned listeners close without forced stops;
checkpoint isolation, foundation ownership and retained assets are preserved.
These results close T064/T073's functional identity, collision, stale-reference
and controller-replacement matrix, not sustained capacity or rolling-release
acceptance. Earlier failures remain evidence; their underlying availability
cause is not claimed to be established.

**Checkpoint**: Managed owner access and general RBAC lifecycle are
duplicate-safe and recoverable. Their safety derives from UID/CAS/claims,
not shared local checkpoints, global authorization locks or controller leases.

## Phase 7: US4 - Bootstrap safely and protect built-ins (P2)

**Goal**: Fresh deployments initialize one removable administrator without
temporary external allow-all access or re-granting removed authority.
**Independent test**: Start with configured initial admin, verify system
identities and built-ins, remove the ordinary initial-admin binding, restart
and verify it remains removed; all built-in mutation attempts are rejected.

### Tests first

- [X] T074 [P] [US4] Add built-in identity/immutability/release-change tests in `gitstore-api/internal/rbac/builtins_test.go` and Admin merge tests in the existing `gitstore-api/internal/admin/graph/resolver/rbac_contract_test.go`; include all built-in roles/system bindings, deterministic IDs/revisions and pagination without duplicates.
- [X] T075 [P] [US4] Add bootstrap tests in `gitstore-api/internal/app/rbac_bootstrap_test.go` for concurrent APIs, Git/projection unavailability, crash after commit before receipt, retry after lost acknowledgment and deletion followed by restart without initial-admin re-creation.

### Implementation

- [X] T076 [US4] Finish release-visible virtual built-ins in `gitstore-api/internal/rbac/builtins.go` and `gitstore-api/internal/admin/graph/resolver/rbac_service.go`; reject their creation/update/deletion on both transports, keep them effective without Git manifests and preserve a separate removable initial-admin binding.
- [X] T077 [US4] Implement the narrow initialization path in `gitstore-api/internal/app/rbac_bootstrap.go` using common admission; seed only the configured first administrator in an uninitialized root scope, record the system actor and never expose a process-local bypass to external requests.
- [X] T078 [US4] Persist/reconcile the once-only receipt through existing root Namespace bootstrap status with UID/resourceVersion CAS in `gitstore-api/internal/app/rbac_bootstrap.go` and `gitstore-api/internal/datastore/namespace_contract.go`; recover a committed binding from its OID without duplicating it or re-seeding after deliberate removal.
- [X] T079 [US4] Integrate bounded bootstrap/projection retries with OPA readiness in `gitstore-api/internal/app/bootstrap.go`; keep APIs unready while required durable state is unavailable, preserve empty default `system:masters` membership and verify the controller/internal identities can recover without granting temporary external authority.
- [X] T080 [US4] Add and run fresh-deployment and same-contract built-in upgrade scenarios in `tests/integration/rbac_bootstrap_test.go`; verify initial admin, system/controller access, immutable built-ins, removable ordinary administrator binding and removal survival across real API process replacement.

Native partial evidence now passes: a fresh owned Scylla/Git deployment with
two APIs shares one initial administrator, preserves immutable controller
built-ins, revokes the ordinary administrator on both replicas and keeps that
grant revoked after each real API process is replaced.

The real controller-enrollment slice also passes without skips on final source:
assigned-UID enrollment, eight fresh 60-second assertion exchanges, controller
reads and privilege-denial checks across administrator revocation and both API
replacements. The finite scenario completes in 12.00 seconds using two APIs,
singleton native Git, real Kratos enrichment and a fresh RF1 Scylla keyspace
with the retained 14-migration contract. All nine listeners close, retained
deployment state is unchanged, and private ownership/restoration receipts are
preserved. Replacements use the same binary; this does not prove a distinct
old/new release upgrade. T080 remains open for that same-contract upgrade.

The same-contract built-in upgrade slice now passes without skips in
13.83 seconds on a separate fresh owned keyspace. Two distinct API binaries
exercise a private previous-release fixture whose view rule lacks management
reads and the unmodified current release. Rolling replacement verifies changed
rule content and per-resource versions, stable built-in UIDs, agreement between
upgraded replicas, fresh controller assertion exchanges and continued denial of
the revoked administrator on both APIs, including the mixed-version window.
An unchanged-content negative control fails at the release-change assertion.
All nine listeners close, singleton Git shuts down cleanly and retained
fixtures/ledgers remain unchanged. This closes T080's functional release-fixture
matrix, not T094's production cutover/rollback matrix or image-upgrade acceptance.
Integration vet, repository lint and whitespace checks pass.

**Checkpoint**: Fresh bootstrap and initialized runtime operation both work.
Production enablement still requires the controlled cutover and acceptance gates.

## Phase 8: US5 - Inspect grants and review access (P3)

**Goal**: Authorized auditors inspect role resolution and obtain a versioned,
transient SubjectAccessReview without changing authority.
**Independent test**: Read an unresolved binding and its condition; review a
subject/action/resource at identified revisions; verify namespace isolation,
review permission and hypothetical group handling.

### Tests first

- [X] T081 [P] [US5] Add SubjectAccessReview schema/resolver tests in `gitstore-api/internal/admin/graph/resolver/access_review_test.go`: version/kind defaults and rejection, `spec` echo, decision/revisions in `status`, omitted versus empty groups, hypothetical context, reviewer/target separation, infrastructure errors and absence of persisted Node/list/watch behavior.
- [X] T082 [P] [US5] Add auditor inspection tests in `gitstore-api/internal/admin/graph/resolver/rbac_inspection_test.go` for scoped lists, unresolved-role conditions, resolved UID links, built-in visibility, denied view-role access, authorized watch/resume and target-existence information boundaries.

### Implementation

- [X] T083 [US5] Extend `shared/schemas/admin/rbac.graphqls` with `AccessReviewInput`, an `accessReview` Query returning `SubjectAccessReview` directly and typed spec/status/revision objects; reserve payload wrappers for mutations, use `authorization.gitstore.dev/v1beta1`, canonical action/resource references and no durable metadata requirements, Node identity or SelfSubjectAccessReview endpoint.
- [X] T084 [US5] Implement reviewer authorization, authoritative resource lookup and trusted target membership in `gitstore-api/internal/admin/graph/resolver/access_review.go`; require `role.access-review.create` or `clusterRole.access-review.create` for the target scope before disclosure, and mark explicitly supplied groups/context hypothetical rather than authenticating as them.
- [X] T085 [US5] Evaluate the review through the active provider's authoritative pinned-model capability in `gitstore-api/internal/admin/graph/resolver/access_review.go`; echo accepted spec parameters, return exact evaluated revision/digest metadata in status and use the existing retryable GraphQL error envelope for missing state instead of fabricated denial.
- [X] T086 [US5] Complete authorized source/revision diagnostics in `gitstore-api/internal/admin/graph/resolver/rbac_service.go`, `provenance.go` and `gitstore-api/internal/auth/logging.go`; trace grants to a Git commit/author or named release built-in while bounding reasons and excluding raw policy, full memberships and credential material. Use `GitStore-Provenance-Include: true` and response-path-associated `extensions.provenance` (partial #488), not a top-level resource `source`; preserve the canonical envelope and cap inspection at 64 entries.
- [X] T087 [US5] Complete binding-resolution/status and inspection watch output in `gitstore-api/internal/admin/graph/resolver/rbac.resolvers.go`; expose immutable resolved UIDs, current conditions and lifecycle state, reuse durable watch authorization and never calculate resource aggregates with live resolver scans.
- [X] T088 [US5] Regenerate the schema and add/run `tests/integration/rbac_access_review_test.go` with real reviewer identities; cover admin namespace scope, cluster review privilege, self-target still requiring review permission, hypothetical inputs, transient no-storage behavior and enforcement agreement at the same revision/input.

**Checkpoint**: Inspection and reviews are usable without introducing another
persisted kind or an alternate authorization evaluator.

## Phase 9: Polish, compatibility and correctness acceptance

**Goal**: Prove feature correctness on owned infrastructure and publish
supported operating behavior. Keep the unchanged production-capacity envelope
pending in #451. Tests below must precede runner changes.

- [X] T089 Add failing local workload/verifier regressions in `tests/integration/rbac_capacity_contract_test.go` and `scripts/test-capacity-dispatch.sh`: exact offered/completed/dropped accounting, independently checked policy revisions, owned topology/dataset proof, private fixtures, fault epochs and unchanged default health-only driver behavior.
- [X] T090 Extend the existing namespace/watch harness in `tests/integration/namespace_watch_capacity_test.go` with bounded RBAC fixture seeding and an independent revision-aware decision oracle; verify native counts for 1,000 namespaces, 10,000 roles and 50,000 bindings, rotate subjects/namespaces and avoid catalog-wide scans or a second capacity framework.
- [X] T091 Extend `tests/capacity/profiles/api-readiness.js`, `tests/capacity/preflight/api-readiness.sh` and `tests/capacity/verifiers/api-readiness.sh` with an explicit RBAC workload selector under the current Makefile interface; require two directly probed APIs/two controllers/one Git, and preserve the existing health-only and identity scenarios when not selected.
- [X] T092 Wire RBAC propagation observers, workload bounds and domain verification into `scripts/run-capacity-target.sh` and `tests/integration/namespace_watch_capacity_test.go`; use existing `TARGET=namespace PROFILE=watch` and `TARGET=api PROFILE=readiness MODE=alpha` families, bounded pending offers and the contract's 60/30/10 outcome mix without suppressing errors or drops.
- [X] T093 Extend `scripts/run-chaos.sh` and its existing regression coverage for explicitly owned journal interruption, API restart and controller replacement; declare bounded fault windows, verify ownership/restoration and process epochs, retain independent controller checkpoint volumes and prohibit overlapping singleton Git replacement.
- [X] T094 Add and run `tests/integration/rbac_rolling_upgrade_test.go` for old/new feature-disabled compatibility, additive migration with read-only startup validation, seeded parity, USER-owner catch-up, provider-switch write freeze/traffic drain, snapshot-ready traffic admission, compatible rolling replacements and refusal of stale static-policy rollback.
- [X] T095 Measure actual before/after API binary and same-architecture image size, heap/RSS, prepared-evaluation latency and existing SDK telemetry regressions using `docker/api.Dockerfile`, `gitstore-api/internal/auth/metrics_sdk_test.go` and `specs/066-git-backed-rbac/research.md`; distinguish these results from the isolated planning probe and do not claim image-reduction issue #487 is complete.
- T096 **Deferred to #451 (2026-11-21), not passed**: run both extended capacity families for the declared unprofiled 30-minute healthy workload and record attributable results in `specs/066-git-backed-rbac/contracts/capacity-recovery.md`: 1,000 requests/s total, 50 admitted RBAC writes/min, at most four writers, decision p99 <=2 ms, propagation p99 <=5 s with p95 reported against the non-blocking <=2 s goal, zero wrong/dropped decisions and all queue/snapshot/memory limits. This is no longer a feature-closeout prerequisite.
- [X] T097 Prove native functional fault/recovery correctness on two APIs/two controllers and one Git service with bounded owned fault windows and an independent revision-aware decision oracle: API restart, below/above-bound journal interruption, cursor-loss full resync, controller recovery and recovery <=30 s. Verify surviving peers, readiness before traffic, no expired/revoked/stale-UID grants and no duplicate managed bindings. Record actual domain recovery separately from injector/process receipts in `specs/066-git-backed-rbac/contracts/capacity-recovery.md`. The original 30-minute fault-load/rate/percentile acceptance remains pending under #451; it is not required for this functional proof.
- T098 **Deferred to #451 (2026-11-21), not passed**: run the existing sustained peak catalog-push profile concurrently with RBAC against an eligible owned retained dataset of at least 5,000,000 products, preserving original thresholds/storage/history. Record scoped interference acceptance in `specs/066-git-backed-rbac/contracts/capacity-recovery.md`; an unavailable runtime/asset leaves production acceptance pending, not reduced to alpha. This is no longer a feature-closeout prerequisite.
- [X] T099 Update `docs/configuration.md`, `docs/api-reference.md`, `docs/storage-groups/transient.md` and `docs/runbooks/controller-auth.md` for implemented provider settings, SubjectAccessReview, freshness/readiness, bootstrap, protected owner bindings, safe cutover/rollback and supported diagnostics; keep user-facing docs free of internal spec/task references and private experiment data.
- [X] T100 Amend `docs/ADRs/0010-authorization-model.md`, `docs/ADRs/README.md` and `docs/implementation/022-opa-data-authorization.md` for the implemented dynamic model and validation-start boundary, preserving ADR-0010's acceptance date; explicitly verify ADR-0018 remains Proposed because platform-wide lease/fencing is not implemented, and require a separately scoped status-flip task if that core is later approved.
- [X] T101 Run focused suites/race checks, `make build`, `make test`, `make lint`, `make check TARGET=all` and the full default `make pr-ready`; refresh the code graph with `graphify update .`, check generated drift and confirm all source changes behind `shared/schemas/admin/rbac.graphqls` and `gitstore-api/internal/rbac/` retain the declared compatibility and no-secret guarantees.
- T102 **Retired (2026-11-19)**: the separate quickstart-validation closure gate is no longer required. Existing command documentation remains maintained under T099 and source validation under T101; retirement does not claim another quickstart or native acceptance pass.
- [X] T103 After every non-deferred functional, native-store, concurrent-replica/rolling, bounded-work and functional fault/recovery gate passes, update `specs/066-git-backed-rbac/spec.md` to Closed and finalize an explicitly correctness-only acceptance summary in `specs/066-git-backed-rbac/contracts/capacity-recovery.md` in the implementing PR. Retain Draft/pending status while a correctness proof such as T097 is missing. Leave T096/T098 and sustained fault-load acceptance pending in #451; do not claim production readiness/activation or completion of unrelated Proposed ADRs, #487 or GraphQL-alignment #548.

Correctness-only closeout passes on the implementation branch after T097's
native journal and controller domain proofs, the native five-minute
directory-lease-boundary proof for #588, the full default `make pr-ready`,
serialized `make check TARGET=all`, affected race suites and AST refresh.
There are 100 completed tasks, no open non-deferred tasks, two tasks deferred
to #451 and one retired task. Independent main-based PRs must preserve their
actual implementation/task state rather than importing unchecked acceptance
work as completed. Production OPA remains gated.

Main-reconciled executable source `74862cb6` additionally passes fresh native
journal recovery and the distinct implemented-version rolling/cutover matrix;
the exact sources, bounds and separate failed gated-main diagnostic are
recorded in [capacity/recovery](contracts/capacity-recovery.md#main-reconciled-executable-verification-2026-11-21).
These receipts do not imply that an open delivery PR has merged.

T092 is complete as runner integration, independently of sustained acceptance.
The implementation includes strict healthy-alpha dispatch for both
existing families, source/artifact build receipts, actual SDK resource captures,
original-ACK percentile replay and mandatory sealed domain postflight.
Focused rejection/positive contracts live in
`tests/integration/rbac_capacity_alpha_test.go` and
`scripts/test-capacity-dispatch.sh`; the capacity README and quickstart describe
the supported declaration and private evidence handling. On source `463149d4`,
the capacity-dispatch regressions and all `TestRBACCapacity` race contracts pass
(125.380 seconds for the Go suite). Both public selector families retain owned
preflight, bounded work and independent sealed postflight; production selection
still refuses unsupported acceptance. Full native healthy, sustained-fault and
larger-catalog acceptance remains pending in #451; T097 separately requires
native functional domain recovery. None is inferred from these local runner
checks. T102 is retired rather than reported as a validation pass.

T101 passes on source `886e409e`: the full default `make pr-ready` includes
canonical lint, build and test suites, and `make check TARGET=all` separately
passes configuration/Compose, license, credential-leakage and generated-drift
checks. Focused API auth/RBAC/OPA/app and capacity-contract race suites and vet
also pass. Runtime reservation/queue telemetry, complete provider-call timing
joins and full-window summary verification are included. This is source
validation, not a substitute for pending native acceptance; rerun the affected
checks after subsequent implementation changes.

The subsequent finite origin-failure attribution change also passes the full
default `make pr-ready`, API auth/OPA/app race suites and separately serialized
`make check TARGET=all`; AST refresh and whitespace checks pass. Its reasons
distinguish absent tiers, unverified proofs, expiry and clock skew while keeping
the same fail-closed error class and freshness bound. It is diagnostic
attribution, not a repair or pass for the failed full-duration attempt.

The bounded parallel origin-input follow-up passes source/proof/expiry race
suites, the full default `make pr-ready`, serialized `make check TARGET=all`
and AST refresh. Its blocked-reader regression proves four concurrent
identities and ordered proof association without caching live facts or changing
freshness. Native sustained validation remains pending.

The receiver's per-repository proof-isolation regression also passes after
failing against a held write lock. The full default `make pr-ready`, serialized
`make check TARGET=all`, signed-proof regressions and AST refresh pass with the
proof-only nonblocking read-lock change. Native availability remains a separate
pending gate; busy repositories never receive an affirmative proof.

T095's exact same-platform pre-OPA/linked release-image pair now also has actual
serial process heap/RSS measurements with identical memdb/static/allow-all
configuration, container limits and health/metric sampling. Median heap is
22,205,104 versus 23,607,640 bytes; median RSS is 51,859,456 versus 60,096,512
bytes. These health-warmed measurements are not active-RBAC, after-GC or peak
memory acceptance. Together with the recorded actual binary/image comparison,
prepared-decision benchmark and current-source API/shared SDK race regressions,
they complete the scoped cost/telemetry task. Full-geometry active-provider
diagnostics remain separately attributed; no sustained gate or #487 claim is
inferred from either measurement.

T094's final-source feature-disabled production matrix passes against the exact
pre-RBAC release, including additive migration, read-only refusal, caller/UID
preservation and unchanged singleton Git history. The separate forward diagnostic
matrix passes with distinct compatible API/controller sources, real in-flight
drain/write freeze, source-ready admission, seeded parity, USER-owner catch-up,
private checkpoint recovery and off-traffic stale static rollback refusal.
The compatible API baseline contains the verified payload-free deletion repair;
an earlier buggy baseline remains failed evidence, not a healthy upgrade claim.
Static rollback refusal is the independent frontend admission oracle, not an
intrinsic static-provider startup or hot-reload check. Full default readiness
and affected race/vet checks pass. Production OPA activation and sustained
healthy/fault/interference gates remain pending; T094 does not close them.

The retained native corpus now has all 1,000 import acknowledgements and
61,000 distinct Namespace/policy UIDs. Fresh independent source/ordinary-grantee
joins cover every Namespace on both APIs within the unchanged shared
30-second gate (9.75 and 19.43 seconds), and the five original ordinary policy
cases pass. On source `7c814edeb29b2bef80b0c576909b312ad5e2a5ca`, the complete
500-second rotation at 40 requests/s completes all 20,000 offered requests:
12,000 allowed, 6,000 denied and 2,000 isolation denials, with zero dropped,
unavailable or wrong decisions. The independent source/vector joins and native
counts close T090's fixture/oracle prerequisite, not sustained acceptance.
The repair batches fresh Namespace-origin proofs within the existing shared
32-ref bound and four workers, preserving root reservation, five-second
freshness, pre-I/O proof clocks and generation fencing. Original failed results,
all seed receipts, identities and storage remain unchanged. Both controllers
retain their independent checkpoints; all owned listeners close normally
without forced stops and the isolated database process is unchanged.
The later selector proof below closes T091 independently of the remaining
healthy/fault/interference gates; no acceptance requirement is waived.

T091's public selector and complete independent verifier pass on source
`466f99a28fbcd6376870f35922abe84ee0a1d0cd` at the full native geometry.
The 36-second diagnostic completes 18,000 ordinary offers on each direct API
with exact 10,800/5,400/1,800 allow/deny/isolation outcomes and zero wrong,
unavailable or dropped requests. Twenty admissions at 50/minute exercise
general Role resource/verb/condition edits through ten Git pushes and ten
GraphQL updates; both APIs replay every source and original acknowledgement.
All four forward restorations and independent sealed replay pass. Finite
ingress/VU allocations support the offered rate, and the directory uses its
existing 4,096-entry/16-lookup defaults rather than the small fixture's 128/8;
identity TTLs, deadlines and all acceptance gates remain unchanged. Earlier
directory failures remain failed evidence. Original seed receipts and resource
identities are preserved; live canonical refs advance only through recorded
owned writes and restorations. T092 still requires the complete sustained/root/
maximum-batch and alpha-mode path. Seven formal tasks remain open; this short
diagnostic does not close T095-T098, T102 or T103.

T089's local test-first foundation is implemented in the existing namespace/watch
harness. The scheduler computes every scheduled offer independently of timer
delivery, bounds pending work and drain, preserves the full load window, and
reports uncompleted offers as drops. The independent oracle copies predeclared
expectations keyed by complete cluster/Namespace UID, Git revision and content
digest vectors; it rejects unknown vectors and expired grant/revoke propagation,
including intermediate revocations followed by re-grants. Local contracts also
reject shared controller checkpoints, foreign/duplicate/stopped processes,
incorrect native-count observations, public/symlinked fixture files, counter
resets and unconfirmed fault windows. Missing contracts and silent dispatcher
fallback failed before implementation; focused race and dispatcher regressions
pass. The default aggregate test selector includes these regressions.

The oracle now accepts independent canonical root and Namespace admission clocks
and retains each component's first supersession deadline. Mixed old/new vectors
cannot restart the stale tier's allowance, even for an unchanged verdict; late
verified aliases inherit both clocks. Four mixed-tier regressions fail on the
composite-clock implementation and pass after the correction. All RBAC capacity
race contracts and repository lint pass, including atomic rejection, future
admissions and alias-clock immutability. Decision checks use indexed vectors and
component clocks rather than scanning the full history. The native hook still
pins root intent, so this is not dynamic-root or sustained acceptance.

This is not deployed capacity evidence. `CAPACITY_RBAC_WORKLOAD=1` is explicitly
refused until T090-T092 finish native seeding/probes, ordinary decision observations,
propagation and domain-verifier receipts; default health-only and identity
drivers remain unchanged. T090's native fixture and static source/oracle rotation are verified above;
the sustained mutable-policy driver remains incomplete. The owner matrix is now
closed by the fresh final-source results above. Historical stacks concentrate the repeated
Repository status timeouts at the Namespace catalog-fence acquisition path;
they do not establish the underlying cause. No production repair was inferred.
Use canonical validation commands; a private launcher's fixed disk-space
threshold is not a repository or user gate.

The bounded native seeder now passes a two-Namespace/twenty-Role/one-hundred-
binding diagnostic with exact policy/UID readback on both APIs. Four real Staff
principals also pass ordinary Product-list probes for unconditional, conditional
group and built-in grants, unresolved roles and unbound subjects. Readback work
shares each replica's existing complexity budget; no administrator exemption or
increased convergence deadline was introduced. Seeding encountered transient
authorization unavailability before convergence. Full native scale, independent
committed-vector observation and sustained decision/propagation drivers remain
unfinished, so T090-T092 stay open.
Seeding requires an explicitly finite whole-test deadline, separate from the
thirty-second per-replica readback checks and thirty-minute acceptance duration.
New seed receipts also retain all six bounded import revisions and their
client-observed successful-push acknowledgement clocks before remote-ref or
replica readback. Local regressions preserve those clocks through serialization
and later admissions and reject duplicate/reordered or oversized records.
Historical receipts without these fields cannot supply propagation timing.
Topology regressions now accept the retained multi-peer Scylla shape without
changing exact replica counts, singleton Git or private-checkpoint constraints.
These are prerequisites, not completion of the deployed oracle or workload.
The existing scheduler is now also connected to ordinary authenticated
Product/Role list requests behind an optional diagnostic seed-fixture field.
Requests and per-API 60/30/10 expectations are declared before execution;
denials exclude every declared Group member and direct/built-in grantee.
Local rotation tests cover all 1,000 Namespace positions, including Group/direct
grant overlap. HTTP/source-join regressions pass for exact actions, real
FORBIDDEN, infrastructure unavailability, malformed/oversized replies and
partial-data leakage. The static native hook requires both replica source/log
joins and caps its diagnostic request set. Its fresh two-Namespace execution
passes all 120 requests (72 allows, 36 denials, twelve isolation denials) with
zero wrong, dropped or unavailable outcomes and both APIs' source/root joins.
This six-second diagnostic does not close T090-T092, implement
dynamic committed-policy history or replace the full healthy/fault workloads.
A further optional diagnostic phase now revokes and restores one owned direct
organization grant through Role-rule edits in real signed Git pushes. Its concurrent two-API
observer uses the original successful-push clocks, bounded polling and the
unchanged five-second deadline; malformed/unavailable/late replies cannot become
successful revocations. Group overlap and the protected personal owner are
excluded before mutations. Local race regressions pass and private receipts
retain partial failures. An earlier binding-subject diagnostic failed on
authorization unavailability about 750 ms after acknowledgement. Bounded
stable-identity replacement now preserves the existing publication's original
freshness proof without renewal; fault, expiry and lifecycle guards remain
fail-closed and all auth/RBAC/app race suites pass. A native binding-edit
follow-up then passed both actual HTTP propagation phases, but the strict
source join correctly rejected its not-yet-observed binding generation.
That failed evidence remains preserved. The fresh Role-rule variant now passes
its complete native source/history join: both APIs revoke in about 1.30-1.41 s
and restore in about 1.15 s, with original Role/binding UIDs and restored specs.
The static 120-request phase also passes. Two observations per mutation do not
establish percentiles, binding-edit joins or sustained acceptance, and the
five-second deadline is unchanged. Captured-source test processes use unsampled decision
logs after a separate run exposed missing sampled receipts. Both replicas clean
up without forced stops, all owned listeners close and the foundation remains
unchanged. The bounded phase now registers the independently authored
Namespace grant/revoke/regrant history, verifies complete captured models and
immutable UIDs, and joins each observed HTTP result's actual decision vector to
the committed oracle under fixed root intent. Sealed replay rejects pending
source aliases and matching HTTP/server verdicts that contradict authored
policy; verified aliases cannot restart clocks or conceal intermediate
revocations. General dynamic root history, a passing native dynamic execution
and sustained drivers remain unfinished, so T090-T092 remain open.
At the default principal budget, one complete paced readback of all one thousand
Namespaces alone requires more than one hour per replica; the earlier arbitrary
one-hour whole-test guard could not prepare the declared dataset. Shared
observation slots now retain a bounded four-query burst within the existing
principal burst allowance, without increasing the sustained refill rate.
Admission-slot waits consume the readback deadline and late successes are rejected. A
four-actually-concurrent-writer diagnostic failed this strict bound with pending
binding reconciliation and authorization unavailability; it remains failure
evidence, not a four-writer capacity claim.

Owned native source observation now records canonical candidates before provider
preparation through an internal constructor option, with no public endpoint or
production configuration switch. Streaming regressions reject altered digests,
incomplete/overlapping models and hidden gaps. Both APIs pass independent source
hashing, authored-policy, exact-count and immutable-UID checks in a two-Namespace
diagnostic, alongside all five ordinary-principal probes. Ordinary Product
decision vectors join to the captured source models; authoritative
write-validation vectors are a separate capture path. This establishes source
observability, not the finished committed-vector decision/propagation driver.
Local oracle regressions now also cover independently verified status-source
aliases: unchanged authored Git vectors retain their original admission time
and expected cases, and late alias registration cannot conceal intermediate
revocations. Unknown digests still fail until explicitly verified and registered.
The diagnostic harness now predeclares ordinary request IDs and checks exact
subject/action/Namespace/outcome receipts against both captured policy tiers.
Canonical nested role-reference generations and Namespace Role UIDs are also
validated. Contract tests, sealed replay and fresh native client-declared trace
checks pass. Staged ten-manifest imports pass for one writer/two Namespaces and
four actually concurrent writers/four Namespaces, with exact final counts and
UID stability across commits. Every batch retains the thirty-second per-API
bound. A further fresh two-Namespace diagnostic verifies the independently
authored bootstrap administrator grant and policy-tree digest, pins root and
binding incarnations through both APIs, and excludes uninitialized/unresolved
root candidates from decision-vector joins. General root-policy changes,
dynamic committed-policy registration and full staged scale remain pending.
The larger four-writer staged diagnostic failed after forty-seven complete
Namespace receipts and one partial receipt at the unchanged per-API deadline.
Source files stayed below their capture bounds; offline provider preparation of
complete models does not reproduce live allocation exhaustion. This remains a
failed scale diagnostic, not native-scale acceptance.
A separate one-writer medium diagnostic passes all sixty-four Namespaces,
640 Roles and 3,200 bindings in 3,214 seconds, with both-API source/policy/UID
checks, independently authored root verification and ordinary client traces.
That run used the pre-rule-sharing provider. Fresh merged-provider parity passes
four Namespaces, forty Roles and two hundred bindings with four concurrent
writers in 100.27 seconds. Both have complete terminal receipts, no forced stops,
closed owned listeners and an unchanged foundation. Neither establishes the
earlier four-writer failure's cause or full-scale/healthy/fault acceptance.
A later full-count one-writer attempt with 128 real Staff principals fails in
its first Namespace after 41.45 seconds at the unchanged per-API bound, with
authorization unavailable. A bounded two-Namespace control using the same subject
pool and no source observation passes; neither setting alone is a proven cause.
The failed fixture remains preserved, all owned listeners close without forced
stops and the foundation is unchanged. Full scale remains unverified.
Single-bulk-commit imports failed with default resync/checkpoint settings: a full-size
two-writer attempt stopped after twelve complete Namespace receipts and one
partial receipt. The one-writer full-size attempt also failed the strict bound
after fifty-one complete receipts and one partial receipt. Both fixtures stopped
without forced termination, closed their owned listeners and preserved the
foundation. Tier revision conflicts, pending projection repair and authorization
unavailability are observed; no single underlying cause is proven. No full native
dataset or acceptance task is checked complete.

A fresh one-writer control completes eighty Namespaces, 800 Roles and 4,000
bindings with the original thirty-second per-API bound, complete terminal
receipts, no forced stops, closed listeners and an unchanged foundation. It
uses the pre-review bounded-replacement runtime without source/profile captures.
No image/full-suite build overlaps it, though bounded single-host-core Go
regressions do. This is not a cause-isolating comparison, verification of the
subsequent retry/memory fixes at that scale, or full-scale/sustained acceptance;
T090-T092 remain open.

The reviewed-runtime eighty-Namespace control fails after 3,053.80 seconds,
with sixty-seven complete receipts, 670 Roles, 3,350 bindings and one partial
Namespace. Its fourth import acknowledgement is followed by canonical
`git ls-remote` authorization-unavailable responses through the existing
thirty-second retry window; this is not a per-API readback timeout. Both API
failure profiles show all four workers doing full Git-backed origin rebuilds,
not the earlier CQL-wait profile. No local builds or tests overlap the workload.
Cleanup closes owned listeners without forced stops and preserves the
foundation and failed evidence. The native cause remains unproven.

A separate regression reproduces healthy root proof expiry while all four
workers are blocked in Namespace scans. Scheduling now reserves capacity for
origin checks or prioritized shared root replacement when publications are
installed and at least two workers are configured. The regression also verifies
real origin failures still fail closed and queued scans recover after release.
Manager race tests pass without increasing worker, freshness, retry, timeout or
memory bounds. This fixes a reproduced scheduling defect, not proof that the
native control failure is resolved; T090-T092 and scale acceptance remain open.

The corrected reviewed-source runtime then completes eighty Namespaces, 800
Roles and 4,000 bindings in 3,220.25 seconds. All six canonical admissions are
complete for each Namespace; no local builds or tests overlap the one-writer,
eight-principal control. Source/request/propagation/startup-profile captures are
disabled. Normal cleanup closes every owned listener without forced stops and
preserves the foundation. This verifies the current runtime at medium scale,
not the required thousand Namespaces or sustained/fault acceptance.

The earlier retry/memory review fixes pass a fresh two-API Role-rule
diagnostic with all 120 ordinary outcomes and complete dynamic source joins:
revocation is about 2.47 seconds on both APIs and restoration about 1.26 seconds.
Manager/application race cases and final isolated default checks pass. These
individual observations pass five seconds, not the separate two-second p95 gate
or a full-scale/healthy/fault acceptance task.

The diagnostic now also performs binding-subject revocation/restoration after
the Role-rule pair. Each authored admission pins mutation generations separately
from the original immutable reference-resolution generation, and retains the
same UIDs, successful-push clocks and five-second limit. Regressions fail before
the correction for both a self-consistent older binding generation and conflated
reference generations. A fresh two-API/two-controller/singleton-Git run passes
all five authored source/history states and 120 static outcomes in 71.10 seconds.
Binding generations advance to two and three while the reference pin stays at
one; both APIs verify binding revocation in about 2.47-2.57 seconds and restoration
in about 2.63-2.74 seconds. The failed first expanded fixture is preserved. These
are bounded observations, not the two-second p95 gate, thousand-Namespace counts
or sustained acceptance; T090-T092 remain open.

The diagnostic also verifies independently authored root binding histories.
A complete two-API/two-controller/singleton-Git native run passes in 66.49 seconds:
120 exact ordinary outcomes, four Namespace mutation phases and four root
grant/revoke/restore/removal phases, with every observed vector joined to its
independent source history. Its oracle separates controller acknowledgement
from an already resolved immutable role reference, and separately declares the
inactive initial-reference transition under the unchanged successful-push-plus-
five-second bound. Unresolved states never count as ready aliases. Root removal
pins any terminating row and accepts its later physical absence, but rejects a
live retained binding. Sealed regressions cover these states and independent
clocks; static warm-up verifies an evaluated admitted source before measurement.
No runtime publication gate or controller authorization bypass is introduced.
This closes the bounded binding-root diagnostic gap, not mutable ClusterRole
rule histories, native thousand-Namespace counts, percentiles or sustained
acceptance; the eleven remaining formal tasks are still open.

Seeding now declares ten-manifest or complete 60-manifest imports. Full-count
seeding defaults to one candidate per Namespace, retaining every changed
resource, the actual successful-push clock and exact two-API UID/reference
readback. The existing four-second shared readback pacing and 30-second
deadline remain unchanged; six repeated imports otherwise impose at least
6,000 readbacks per API for the declared dataset. A fresh two-Namespace native
proof passes one complete admission per Namespace, all 120 ordinary outcomes
and every Namespace/root history join in 53.68 seconds. Local race contracts
cover declaration bounds and immutable admission clocks. This verifies the
bounded complete-import mechanism, not T090's required thousand-Namespace
counts or T096 throughput and percentile gates.

The k6 ordinary-request path and bounded full-window receipt verifier are now
connected to the existing native fixture. A fresh combined run passes in
58.00 seconds with two APIs/two controllers/singleton Git, two complete
Namespace imports, 120 native ordinary outcomes, 360 real k6 outcomes and all
eight Namespace/root binding mutation phases. Each API's 180 k6 decisions
contain exactly 108 allows, 54 denials and 18 owner-isolation denials, with zero
wrong/unavailable/dropped results and exact independently authored source joins.
Test-first regressions catch real k6 boundary over-offers and native/k6 request
correlation collisions. The independent existing readiness verifier replays
sealed private counts, authored cases, k6 counters and source clocks; it rejects
non-diagnostic claims and different evidence directories. Source models from
later mutation phases cannot contaminate the earlier k6 window, nor may a
source alias captured after a decision supply its proof. All RBAC capacity
race contracts, integration vet and dispatcher regressions pass. Storage and
the live database process remain unchanged, with normal owned-process cleanup.
T090-T092 remain open for the full native working set,
general mutable policy histories and sustained admitted writes/propagation.

The existing public diagnostic families now dispatch the actual native
seed/k6/source verifier rather than refusing or falling back. Live preflight
requires two directly probed APIs/two controllers/singleton Git, owned Scylla,
unchanged process epochs/executable digests and independent private checkpoints.
The finite driver independently enumerates native counts, joins every ordinary
decision, rechecks the live topology afterward and replays sealed k6 evidence.
A fresh public api/readiness run passes in 52.87 seconds and an independently
fresh namespace/watch run passes in 52.20 seconds, both with exact two-API
60/30/10 outcomes, all eight binding mutation phases and sealed replay. Test-first diagnostic
dispatch/count-bound regressions and missing-fixture/unsupported-family checks
pass under the existing local suite. Alpha/production remain fail-closed;
T090-T092 are not closed by bounded test-composition evidence.

Source/decision evidence production is now runtime-configured rather than
test-only snapshot injection. The private exclusive 1-GiB source and 8-GiB
decision streams retain complete source clocks, unsampled individual receipts
and sticky failures without changing default operational-log sampling.
Constructor/shutdown and capture-selection reload boundaries are tested under
race; selecting evidence never opens the pending production OPA gate.
A fresh public diagnostic with the configured producer passes in 57.84 seconds,
and independent sealed replay passes after all owned processes stop.
The runtime producer is complete; general mutable policy histories and the
declared sustained admitted-write/propagation driver remain pending.

The root oracle and existing propagation diagnostic now additionally cover an
independently authored custom ClusterRole's unconditional rule grant/revoke/
restore/removal. Canonical tree-content tests distinguish rule revisions;
source tests reject older Role generations, replacement reference UIDs and
invented reference generations while accepting unchanged original references.
Later newly unresolved root grants retain prior authored truth without
reinterpreting earlier live references. The fresh public run passes in
67.96 seconds with all twelve mutation phases, exact k6 outcomes and source
joins. The first run's verifier-only pending-creation assumption failed and is
preserved, not counted as a pass. RBAC capacity race/vet and canonical lint pass.
T090-T092 remain open for the complete native working set, conditional/general
policy histories and the declared sustained mutation/propagation envelope.

The independent public verifier now replays the dynamic phases as well as
static k6 traffic. Private named receipts retain every HTTP probe, the four-phase
Namespace history and two separately bounded four-phase root probe stages.
Replay reconstructs the mutation recipes from the original seed plan, requires
both APIs and every phase, and verifies original acknowledgement clocks,
generations, immutable references and complete source/decision joins.
The first fresh run exposed later root captures being checked against the
earlier fixed-root Namespace phase; original capture-clock scoping fixes that
without reinterpreting its history. The corrected fresh public run passes in
58.54 seconds, and all twelve phases independently replay after owned shutdown.
Separate persisted-evidence tampering checks reject missing API probes,
altered root Role generations and omitted root stages. The failed run remains
preserved. This closes sealed diagnostic propagation replay, not T090-T092's
native scale, general policy or sustained-write envelope.

The existing bounded scheduler now supports a per-minute rate period while
preserving the default per-second behavior. A test-first timed regression
verifies that 50 writes/minute uses original 1.2-second due times, rather than
rounding to one write/second or issuing a batch; the required thirty-minute
offer count is 1,500. Race/vet and canonical lint pass. This is a prerequisite
for the concurrent sustained mutation companion, not completion of T091/T092.

The bounded concurrent companion is now implemented and native-verified.
It independently enrolls a groupless holdout, rotates total 50/minute writes
across four owned ORGANIZATION Namespaces with one serialized writer, and
retains original acknowledgement/supersession clocks and immutable references.
Idempotent cleanup joins k6 before ownership-pinned restoration, including
process/evidence/assertion failures; redacted bounded failed-response receipts
distinguish HTTP/GraphQL/transport failures without exposing bodies or tokens.
The final-source public diagnostic passes in 88.30 seconds: all ten writes
admitted without drops, 1,440 ordinary outcomes in exact per-API 60/30/10,
zero wrong/unavailable results, two restored grants and independent ordinary
and mutation replay after shutdown. Earlier runs remain failures: synchronous
observations blocked scheduling; later provider errors and one unclassified
HTTP-unavailable response failed the hard gate. Exact recovery restored the
two remaining old fixture grants to generation five and independently verified
denials without rewinding refs. Later launcher/recovery-helper configuration
and clock failures are not acceptance evidence. T090-T092 remain open for
general mutation histories, full scale, required rates/durations and percentiles.

The representative provider reservation regression now uses the seeded
conditional, multi-resource rules and binding mix. It exposed a separate
one-thousand-Namespace budget problem hidden by the simpler benchmark fixture.
Sharing authored rules across action indexes makes that regression pass without
changing the eightfold reservation multiplier, 512 MiB limit, replacement
headroom or logical expansion bound. Local before/after measurements are in
`research.md`; they do not prove native full scale or close T095/T096.

T093's owned injector extension is now implemented and verified independently
of the unfinished workload drivers. Test-first dispatcher cases rejected the
old runner's silent RBAC fallback. Regressions now pass for ownership/window
refusal, independent peers/checkpoints, singleton Git exclusion, observed effects,
partial failures and bounded timeout restoration. Real Docker/Pumba checks
through `make chaos` pass API/controller restart and 2.5/6-second shared-source
pauses using responsive HTTP stand-ins and distinct retained checkpoint-marker
mounts. All four exact test-owned containers are removed; the foundation and
retained assets remain unchanged. Two earlier private fixture attempts failed
on Docker-proxy readiness/ephemeral port mappings and remain failure evidence;
the passing fixture uses bounded readiness retries and restart-stable mappings.
These checks prove injector/process behavior only, not API/controller domain
recovery, selective receiver interruption, cursor-loss resync or T097 acceptance.
The runner records that distinction and never marks domain recovery verified.
Final-source aggregate/native/healthy/fault/large-catalog gates remain pending,
and neither production activation nor spec closure is authorized by these local
results.

**Checkpoint**: Production readiness is claimed only for actually verified
code, topology and workloads. Planning or local fixtures do not satisfy the
deployed fault, load or large-catalog gates.

## Dependencies and execution order

### Story dependency graph

```text
Setup T001-T004
       |
Foundation T005-T026 -- native Scylla gate T014 before consumers
       |
US3 T027-T035: shared safe admission
       |
US2 T036-T047: both write transports and durable resource API
       |
US1 T048-T061: live OPA snapshots/readiness and replica convergence
       |
       +---------------------+
       |                     |
US6 T062-T073           US4 T074-T080
owner/lifecycle         fresh bootstrap
       |
US5 T081-T088
inspection/review
       |
       +-- all six stories --> T089-T103: acceptance and close-out
```

US4 can proceed after US1 without waiting for US6, but its final integration
must preserve the owner and controller behavior when merged. US5 depends on
US6's resolution/status behavior as well as US1/US2. Review contract tests
can be drafted earlier; they do not justify skipping those prerequisites.
P1 stories are listed before P2/P3 while these real dependencies remain
explicit. The stories are independently testable at their checkpoints, not
six independently deployable production features.

### Within-phase dependencies

| Phase | Required execution order |
|---|---|
| Setup | T001, T002 and T003 can prepare separate inputs concurrently; T004 follows T003. Complete all four before foundation. |
| Foundation | T005/T006/T007 are independent test batches. T008 and T009 follow T005. T010 follows T009; T011 follows T006/T010. T012/T013 follow T010/T011 and may run together; T014 requires both. T015-T018 follow the native gate. T019 follows T007/T009; T020 follows T008/T009; T021 follows T014; T022 follows T020/T021. T023-T025 define separate boundaries; T024 also needs T018/T019. T026 joins every foundation task. |
| US3 | T027-T029 first; T030-T033 then implement capture, coverage and common checks. T034/T035 validate the resulting race/security boundary before US2 exposes it. |
| US2 | T036-T038 first, then T039-T045. T046 regenerates/tests; T047 joins transport parity and recovery. No new transport bypasses US3. |
| US1 | T048-T050 first. T051-T054 establish budget, bridge, readiness and provider; T055-T058 add exact diagnostics, reauthorization and bounded invalidation; T059-T061 verify the integrated fixture. |
| US6 | T062-T064 first. T065-T068 build the shared client/group and lifecycle; T069 must enable the validated template before T070 writes managed bindings; T071/T072 add status/catch-up; T073 joins replica recovery. |
| US4 | T074/T075 first, then T076-T079; T080 verifies fresh deployment/removal/upgrade independently of a pre-seeded fixture. |
| US5 | T081/T082 first; T083 defines SDL before T084-T087 handlers/diagnostics; T088 regenerates and joins integration. |
| Acceptance | T089 precedes T090-T093 runner changes; T094/T095 require complete story implementations. T097 requires an owned native runtime and independent functional fault/recovery oracle. T096/T098 and sustained fault-load acceptance remain pending in #451. T099/T100 document implemented behavior; T101 validates the final source. T102 is retired. T103 requires all non-retired, non-deferred correctness tasks; it cannot bypass T097 or establish production readiness. |

US1's fixture is explicitly preinitialized; it is not a replacement for US4's
fresh-bootstrap test. The safe externally usable MVP requires US3 + US2 +
US1 and shared foundations; enabling fresh production deployments also
requires US4 and the rollout and production-capacity gates. Correctness-only
feature completion additionally requires US6, US5 and all non-deferred
acceptance; it does not authorize production activation.

## Parallel execution examples

Only launch tasks in a batch after their common prerequisites are complete.
Do not run tasks that edit the same source/generated files together.

| Story | File-disjoint parallel test batch | Join before implementation |
|---|---|---|
| US3 | T027 `provider/opa/coverage_test.go`; T028 `internal/rbac/validation_test.go`; T029 `internal/admission/rbac_test.go` | All three fail as expected before T030-T033. |
| US2 | T036 Rust `validation_handler.rs`; T037 Admin `rbac_contract_test.go`; T038 gRPC `rbac_projection_test.go` | All three fail as expected before T039-T045. |
| US1 | T048 `provider/opa/provider_test.go`; T049 `internal/rbac/manager_test.go`; T050 `tests/integration/rbac_runtime_test.go` | All three fail as expected before T051-T058. |
| US6 | T062 API `rbac_managed_test.go`; T063 controller `rbac/reconciler_test.go`; T064 integration `rbac_owner_access_test.go` | All three fail as expected before T065-T072. |
| US4 | T074 built-in/Admin tests; T075 app `rbac_bootstrap_test.go` | Both fail as expected before T076-T079. |
| US5 | T081 `access_review_test.go`; T082 `rbac_inspection_test.go` | Both fail as expected before T083-T087. |

Setup offers T002/T003 in separate files. Foundation offers T005-T007,
then T008/T009, and T012/T013 after their listed prerequisites. US4 can
start alongside the US6 branch after US1, but serialize any shared app,
resolver or generated-file edits. Code generation has one owner per batch.

## Requirement traceability

| Requirement | Primary tasks and verification |
|---|---|
| FR-001-005 | T005-T018, T023/T025/T026, T036-T047 |
| FR-006-009 | T008, T020, T024, T027-T035, T047 |
| FR-010/010a | T001/T002/T007/T009/T019/T024, T048/T051/T054, T095 |
| FR-011/011a/012/013 | T014-T018, T028/T030/T034, T049/T052-T059, T085, T097 |
| FR-014 | T050/T056/T061, T082/T087, T096/T097 |
| FR-015 | T008/T081-T088; transient envelope and no separate self-review |
| FR-016/017 | T020/T022, T074-T080 |
| FR-018/018a-d | T006/T021/T022/T033/T043, T063/T065-T068/T073 |
| FR-019-021 | T005/T006/T008/T012-T018, T038/T041/T045/T051/T055/T058/T086, T089-T098 |
| FR-022-024 | T003/T004/T019/T024/T060, T094/T099/T100 |
| FR-025-031 | T057/T060, T062-T073, T094/T096/T097 |
| PR-001/002/004 | T006/T014/T021/T027-T050/T058-T064/T073/T080/T088/T094 |
| PR-003/005 | T089-T092; T096/T098 and sustained fault-load acceptance deferred to #451 with unchanged full-load and retained-dataset gates |
| PR-006 | T059/T061/T073/T093/T094/T097; native functional domain recovery remains required |
| SC-001-008 | T047/T061/T073/T080/T088/T094/T097/T103 for correctness; SC-001 percentiles and SC-007 sustained-load latency remain in #451 |

## Implementation strategy

1. Establish additive, disabled contracts and native atomicity before
   consumers. Stop if the Scylla proof fails; do not replace it with a
   weaker per-row transaction or extrapolate from memdb.
2. Deliver US3, US2 and US1 as the smallest safe end-to-end runtime slice.
   Validate grant/revoke and isolation independently before adding owner
   automation. Test-only prepared fixtures never bypass production admission.
3. Add US6 personal-owner/lifecycle work, US4 first-start bootstrap and US5
   diagnostics as separately tested increments. Keep dynamic writes gated
   while old/static serving replicas remain in the cohort.
4. Prove compatible cutover, replica correctness, bounded work and native
   functional fault recovery before correctness-only feature closure.
   Healthy/fault-load capacity and large-catalog interference remain in #451
   before production activation. Keep ownership, resource bounds, receipts
   and pending evidence explicit.
5. Amend already-Accepted ADR-0010 without changing its acceptance date.
   No Proposed ADR's whole core is implemented by this scope, so there is no
   automatic Proposed-to-Accepted flip. If platform-wide ADR-0018 fencing
   is explicitly added later, add its implementation and
   `Accepted (YYYY-MM-DD)`/index tasks in that same implementing PR, using
   `docs/ADRs/README.md` dating rules.

No task adds a second production evaluator, a provider-specific controller,
operator Rego, live provider merging, a global authorization fence,
SelfSubjectAccessReview, a GraphQL-wide alignment migration, Git HA or an
external quickstart parity obligation. Do not commit or push merely because
this task list was generated.
