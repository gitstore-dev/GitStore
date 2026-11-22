# Implementation Plan: Git-Backed RBAC and Embedded OPA

**Branch**: `066-git-backed-rbac` | **Date**: 2026-11-09 | **Spec**: [spec.md](spec.md)
**Input**: `specs/066-git-backed-rbac/spec.md`

## Summary

Add runtime-manageable Role, ClusterRole, RoleBinding and ClusterRoleBinding
resources with Git authority, common push/GraphQL admission, datastore
projections and durable watch delivery. Ship one production AuthZ provider,
`opa`, using a compiled-in Rego module and per-API in-memory RBAC snapshots.
One provider-neutral controller group owns RBAC lifecycle and managed USER
namespace owner bindings; it does not translate data for OPA.

Expose access checks as transient `SubjectAccessReview` envelopes through
the `accessReview(review:)` Admin Query, returning the versioned result directly
without a mutation payload wrapper. Requests use `spec`; results use `status`.
Reviews are not a fifth Git-backed kind and have no durable resource lifecycle.

The November 9 clarification deliberately chooses authorization snapshots
current at validation start, not cross-repository commit-time linearizability.
A concurrent revocation can precede completion of an already-authorized
write. Conservative permission coverage requires explicit bind/escalate
where full coverage cannot be proved. This changes the earlier FR-012
interpretation; it is not an implementation of a global authorization fence.

The plan is not acceptance evidence. Implementation progress and verified
boundaries are recorded in the tasks and contracts; production activation
and final image-size results remain subject to the acceptance gates.

**Delivery scope (owner decision 2026-11-21):** close this feature on
verified authorization correctness, native-store semantics, concurrent
replicas, rolling upgrades, bounded work and functional fault recovery.
Sustained healthy/fault-load throughput and percentile acceptance and the
large-catalog peak-push interference gate remain pending under #451, not
passed or waived. Keep fail-closed freshness, configured revocation limits,
exact revision/UID truth and PR-006's recovery bound. A correctness-only
feature closeout does not authorize production activation or establish
production readiness.

Authorized resource-source inspection uses the approved bounded on-demand
Git-history approach, not persisted per-resource provenance. It follows
first-parent path-blob changes at the admitted revision; Git authors are
metadata, not authenticated actors. The header-opt-in response extension never adds
history reads to ordinary decisions. See the resource contract for bounds
and per-resource unavailability behavior. Resource envelopes retain only
`id`, `apiVersion`, `kind`, `metadata`, `spec` and `status`.
Canonical operation-audit columns are included in consolidated migration 014;
the owned development keyspace applied 015 before its explicitly approved
ledger reconciliation. Git
author/committer database persistence remains deferred.

## Technical Context

**Language/Version**: Go 1.27.2 for API/controller; repository-pinned Rust toolchain for Git-service hook/RPC changes.
**Primary Dependencies**: Existing gqlgen v0.17.90, Viper, Zap, gocqlx/v3 v3.0.4, patched scylla-cdc-go v1.2.1, controller Pond/Backoff/LevelDB, shared observability; add OPA v1.22.2 using its `/v1/rego` API.
**Storage**: Retained bare Git repositories; Scylla production projections, shared watch journal and UID-bound Namespace cleanup handoff; backend-neutral memdb contracts; existing controller disk-v2 checkpoints. No persisted OPA data.
**Testing**: Existing Go/Rust suites, race tests, datastore contracts, owned multi-process integration, capacity and chaos runners.
**Target Platform**: Existing Linux deployment images; macOS development; preserve supported image architectures.
**Project Type**: Cross-service backend feature, not a new service.
**Performance Goals**: Production acceptance in #451: decision p99 <=2 ms; admitted grant/revoke propagation p99 <=5 s with a reported, non-blocking p95 <=2 s optimization goal; 50 RBAC writes/min. Functional recovery <=30 s and configured fail-closed/revocation bounds remain feature gates.
**Constraints**: Fail closed before snapshot/watch readiness; no I/O per ordinary authorization decision; immutable built-ins; bounded rebuilds, payloads and fan-out; no operator Rego.
**Scale/Scope**: 1,000 namespaces, 10,000 roles, 50,000 bindings. RBAC paths must not scan the 5,000,000-product catalog; sustained catalog push traffic remains part of interference validation.
**Replica/Scaling Model**: At least two APIs with independent reconstructible OPA caches and two controllers with independent checkpoint volumes. Same-tier resource/projection writes use durable CAS; controllers use duplicate-safe recomputation, not process-local locks as distributed fences.

**Topology constraint**: Exactly one active Git service retains existing storage.
Git replacement is stop-before-start. No Git replication, sharding, routing
or zero-downtime replacement is introduced.

**Authentication/Authorization**: Preserve AuthN/UserDir/AuthZ independence and signed receiver-bound RPCs. Trusted UserDir Staff membership gates personal namespace creation. Every external mutation/read/watch/review is authorized; privileged lifecycle templates are server-validated even for administrators.
**Load/Backpressure Model**: Defaults in the capacity contract: four global tier rebuild workers, one rebuild per tier, bounded/coalesced work, 30 s validation deadline, 1,000-entry pages, 64 KiB manifests, 4 MiB/256-manifest candidate changes and 64 MiB tier snapshots. Bounds remain feature requirements. Thirty-minute healthy/fault-load acceptance is deferred to #451; offered load and completed throughput stay separately accounted.
**Capacity Profile**: Existing `namespace/watch` and `api/readiness` families
select bounded owned RBAC diagnostics with `CAPACITY_RBAC_WORKLOAD=1
MODE=diagnostic`, or the strict healthy runner with `MODE=alpha`. Alpha requires
the full dataset, 1,800-second mixed Namespace mutation schedule,
1,860-second ordinary window, maximum root preamble, repository-generated
source/artifact receipts, actual SDK memory/queue/process observations and
independently replayed provider/propagation percentile gates. Both families
require sealed domain postflight; production selection stays refused.
Runner integration (T092) is verified by dispatch and capacity-contract race
regressions; it is not sustained native acceptance. T102's separate
quickstart-validation gate is retired. T096/T098 and T097's sustained fault-load
portion remain pending in #451. T097's native functional recovery proof and
T103's correctness-only feature closeout remain required.
The existing namespace/watch harness now includes the tested bounded scheduler,
independent committed-vector oracle foundation and bounded native diagnostic
seeding with source/root/client joins. It also executes the existing k6 profile's
ordinary RBAC path and independently replays every scheduled source vector
within its actual process/capture window. Static diagnostic parity cannot
substitute for the complete alpha declaration and observations.
Public RBAC preflight verifies live owned processes,
epochs, executable digests, independent checkpoints and native topology before
seeding; they reverify unchanged epochs and independently replay the real k6
source receipts afterward. Native counts and complete independent source parity
now cover all 1,000 Namespaces, 10,000 Roles and 50,000 bindings on both APIs.
The unchanged shared 30-second source-readiness gate completes in 19.43 seconds.
The subsequent 500-second, 20,000-offer ordinary rotation at 40 requests/s passes
with exactly 12,000 allowed, 6,000 denied and 2,000 isolation denials and zero
wrong, dropped or unavailable decisions. This closes the native fixture/static
oracle prerequisite, not sustained acceptance. A later public full-geometry
diagnostic on source `466f99a2` completes all 36,000 ordinary offers in 36 seconds
at 1,000 requests/s total, with exact 60/30/10 outcomes and zero wrong,
unavailable or dropped requests. Twenty general Role resource/verb/condition
edits alternate Git and GraphQL admissions at 50/minute across four tiers;
all source/generation/HTTP joins, four forward restorations and sealed replay
pass. Both APIs' provider-call p99 is below 563 microseconds and propagation
p95/p99 below 1.542/1.546 seconds. Explicit finite allocations include the
existing directory cache/concurrency defaults without changing identity
freshness, deadlines or acceptance gates. This closes the public selector
prerequisite, not root-under-load, maximum-batch or sustained acceptance;
the thirty-minute healthy/fault and large-catalog gates remain pending.
A later full-duration attempt on the same source fails early with a correlated
Namespace-origin authorization outage. Bounded cleanup verifies the original
policy state on both replicas; this failed attempt does not inherit the short
diagnostic's pass or establish sustained stability. Decision logs now distinguish
missing, unverified, expired and future-dated origin proofs without exposing
tier identities. A subsequent full-geometry attempt identifies expired Namespace
proofs on unchanged tiers of both APIs. Live input collection now uses at most
four concurrent identities per batch, retaining all three authoritative reads,
ordered signed proofs, the 32-ref bound and pre-I/O clocks. Targeted race tests
prove the parallelism bound and unchanged identity/proof checks; a native
comparison still expires Namespace proofs after 186 admissions, despite safe
completed cleanup. The input optimization is not an availability repair.
A receiver regression separately proves cross-repository head-of-line blocking
behind a write lock. Its proof-only nonblocking lock admission withholds the
busy repository's result rather than aging unrelated proofs; signed bounds,
lock protection and freshness remain intact. Native full-window comparison is
still required, not inferred from these local tests.
Private source/decision evidence is now runtime-configured, not a test-only
snapshot observer. Its exclusive bounded streams preserve actual capture clocks
and unsampled individual receipts while default operational logging stays
sampled. The production activation refusal remains unchanged.
The bounded authored root oracle also covers custom unconditional ClusterRole
rule grant/revoke/restore/removal. It independently canonicalizes rule content,
pins Role mutation generations and retains each binding's original immutable
reference generation. Later newly unresolved root references cannot reinterpret
earlier live references. Conditional mutable root histories and the sustained
companion remain outside this diagnostic proof.
**Fault Profile**: Reuse owned `api-restart`/controller restart facilities; extend the existing runner for a bounded, owned watch-journal interruption. Verify domain recovery and process epochs, not just injector exit.
**Measured Implementation Cost**: T095's actual same-platform binary/image,
paired health-warmed process heap/RSS, prepared-decision component and SDK
telemetry regressions are recorded in `research.md`. The static-provider
release-runtime pair does not measure active RBAC cache cost; the separate
full-geometry diagnostic is not thirty-minute or peak-memory acceptance.

## Constitution Check

Pre-research evaluation: the feature fits the existing service boundaries.
Snapshot/watch completeness, cross-repository validation semantics and grant
coverage were design gates; the latter two were explicitly clarified before
design. No non-negotiable requirement is waived.

| Gate | Pre-design obligation | Post-design result |
|---|---|---|
| Test-first | Demonstrate failing contract, lifecycle, authorization and recovery tests first | Ordered delivery below and validation contract identify the failures |
| API/contract-first | Define public schema, datastore, RPC, event and error behavior before handlers | Four contracts define these boundaries; generated code follows later |
| Core-service boundary | API enforces/projects; controllers reconcile; Git stores/validates refs | No service reads another service's private storage |
| Replica safety | Two APIs/two controllers, CAS, restart/rolling tests; singleton Git | Complete snapshot fencing, UID guards and per-tier CAS specified; evidence still required |
| Multi-user security | Pluggable identities, scope isolation, safe delegation and revocation | Conservative coverage, protected templates, bounded snapshots; validation-start race explicitly documented |
| Production capacity | Bounded RBAC work independent of product dataset; sustained pushes | Declared RBAC dataset and interference workload; no readiness claim from planning |
| Repeatable evidence | Existing Makefile capacity/chaos families and domain verifier | Extensions and required receipts identified; no expensive workloads run here |
| Bounded work | Payload, snapshot, retry, queue and memory ceilings | Contracted limits; overload marks affected authorization unavailable |
| Observability | Exact evaluated identity, readiness, latency, errors, saturation | Existing bounded SDK diagnostics extended, no namespace metric labels |
| Incremental delivery | Schema-first disabled feature, compatible rolling deployment and controlled cutover | Enable dynamic grants only after every serving binary understands the contract |
| Simplicity | No new broker, network policy service or global fence | One RBAC store abstraction, one lifecycle group, embedded provider |

Post-design result: **PASS as an implementation plan**, not as evidence of
implemented replica safety, performance or production acceptance. The
explicitly selected snapshot semantics are weaker than linearizable
authorization; tests must demonstrate that exact boundary.

## Project Structure

### Documentation (this feature)

```text
specs/066-git-backed-rbac/
  spec.md
  plan.md
  research.md
  data-model.md
  quickstart.md
  contracts/
    resource-api.md
    admission-storage.md
    snapshot-provider.md
    capacity-recovery.md
```

[tasks.md](tasks.md) defines the implementation order and acceptance gates.

### Source Code (implementation destinations)

```text
gitstore-api/
  internal/auth/{types.go,registry.go,provider/rbaclocal/,provider/opa/}
  internal/rbac/                    # provider-neutral validation/model
  internal/app/                    # provider construction, readiness, shutdown
  internal/config/                 # existing configuration definitions
  internal/admission/
  internal/cataloggrpc/
  internal/datastore/{entities.go,datastore.go,memdb/,scylla/}
  internal/watchjournal/
  internal/admin/graph/
gitstore-controller-manager/
  internal/rbac/                    # single primary lifecycle group
  internal/{manager,listwatch,checkpoint,graphqlclient,status}/
schemas/rbac.authorization.gitstore.dev/v1beta1/
shared/schemas/                     # Admin SDL and finite action catalog
shared/proto/                      # only extensions required by common admission
gitstore-git-service/src/git/hooks/
tests/{integration,capacity,chaos}/
scripts/                           # existing runners, not new public command families
docs/{ADRs,implementation}/
config/
AGENTS.md                           # current-plan reference only
```

New paths above are planned, not created runtime packages. Confirm individual
schema/config filenames against current generators when implementing.

**Structure Decision**: Keep provider-neutral types in the API's existing
internal domain layout; do not introduce a shared module until another
service actually imports it. The controller consumes Admin GraphQL, not
API internals. Versioned resource schemas use the RBAC API group rather than
mislabeling these resources as the catalog API group.

## Phase 0: Research

### Approved Namespace cleanup refinement

Namespace deletion must not wait for child RBAC finalization. The selected
durable-handoff approach persists an immutable Namespace UID/name cleanup job
after proving authoring-manifest removal and before physically removing the
Namespace. Failure to persist the handoff blocks parent removal; child cleanup
does not. This replaces reliance on rediscovering children through a parent
which may already be gone.

The consolidated migration 014 includes a 64-bucket internal outbox and a permanent retired-tier
marker. The marker uses the existing per-tier CAS and rejects delayed
authoring/projection, including historical receipt replay. It is a Namespace
lifecycle tombstone, not a global authorization fence. Same-name replacements
have different UIDs and remain untouched.

The primary RBAC group's existing Manager/Pond workers drain one job at a
time through an enrolled-controller, globally Namespace-purge-authorized Admin
operation. Each attempt has a 30-second deadline and visits at most 100 children;
bindings are suppressed before claims are released and roles require the
authoritative zero-claim guard before removal. Foreign finalizers and uncertain
lookups retain work. Poll retries use their own disk-v2 schedule, not a new
service or a general orphan sweeper. The outbox is not a fifth resource kind
and has no public Node/list/watch lifecycle.

See [research.md](research.md) for verified integration points, choices,
alternatives and the isolated OPA size measurement. No unresolved
clarification marker remains.

Important baseline changes since the original spec: Admin/Storefront
listeners are already split; Kratos UserDir and Staff classification shipped;
bounded decision/snapshot diagnostics and SDK telemetry already exist;
migration history now includes 013. Reuse these, not the older doc 022
implementation sketch.

## Phase 1: Design

1. Define resource envelopes, fixed paths, immutable roleRef, deletion
   semantics, Node/Relay queries, review authorization and structured errors.
2. Extend the finite action catalog with CRUD/watch/bind/escalate,
   `status.write`, `purge` and scope-qualified access-review actions.
3. Implement a bounded, per-tier authoritative projection with revision CAS;
   atomically apply complete admitted change sets and expose stable paged
   snapshots. Register full-image CDC in the existing journal runner.
4. Validate whole Git candidates before ref acceptance. GraphQL produces
   the same candidate and uses the same admission. Capture authoritative
   per-tier Git/projection revision vectors at validation start; reject lag
   rather than authorizing from a known-outdated projection.
5. Keep the fixed OPA module prepared independently of data. Publish complete
   tier data/readiness/revision atomically, retaining exact identities for
   each decision. Never mark ready after only opening a watch connection.
6. Add one primary RBAC lifecycle controller using existing durable
   work/checkpoint/CAS helpers. Provision protected personal-owner bindings
   by fixed server-validated template under the enrolled controller identity.
7. Seed removable initial-admin binding once with a durable bootstrap receipt.
   Verify parity in an isolated candidate verifier before provider cutover;
   never merge live providers.

Details and invariants are normative for implementation in the linked
contracts and [data-model.md](data-model.md).

### Requirement coverage

| Requirements | Design and acceptance location |
|---|---|
| FR-001-005 | Data model; resource API and admission/storage contracts; push/GraphQL parity tests |
| FR-006-009 | Finite actions; conservative coverage; delegation matrix |
| FR-010/010a | Versioned snapshot model; fixed OPA module; unchanged provider-neutral decision fixtures |
| FR-011/011a/012/013 | Stable tier snapshots, readiness state machine, validation-start vector and unavailable errors |
| FR-014/015 | Subscription reauthorization and scope-authorized, transient SubjectAccessReview envelopes with revisioned status |
| FR-016/017 | Virtual immutable built-ins and once-only removable initial administrator |
| FR-018/018a-d | Unresolved-role bind rule, UID reference claims, primary controller, deletion suppression |
| FR-019-021 | Bounded data/work, exact revision telemetry and commit attribution |
| FR-022-024 | Static/dynamic separation, no custom Rego, doc 022 amendment |
| FR-025-031 | Staff owner template, protected binding, UID-safe lifecycle, cutover and static non-merging |
| PR-001/002/004 | CAS/UID/replica design, identity matrix and explicit bounds |
| PR-003/005 | Capacity/recovery contract and #451: unchanged dataset, offered load, oracle and production capacity gates |
| PR-006 | Native functional fault/recovery proof for feature closure; sustained fault-load acceptance in #451 |
| SC-001-008 | Test-first correctness matrix; propagation percentiles and sustained-load latency acceptance deferred to #451 |

## Phase 2: Delivery Planning

This is the input to task generation, not task execution.

| Slice | Stories | Deliverable and failing-test gate |
|---|---|---|
| Foundation | All | Schemas/catalog/model, API/store/RPC contracts, strict parser and provider-neutral decision/coverage fixtures |
| Resource pipeline | US2/US3 | memdb/Scylla CAS and CDC; pre-receive + GraphQL parity; whole-candidate rejection, projection lag and replay recovery tests |
| Provider | US1 | Embedded OPA, readiness, snapshot/watch bridge, bounded rebuilds, exact evaluated revisions; cold-start and revoked-grant regressions |
| Bootstrap/lifecycle | US4/US6 | Immutable built-ins, once-only initial admin, role/binding finalizers, Staff owner template, UID recreation and concurrent controllers |
| Inspectability | US5 | Authorized resource Node/connection/watch; transient SubjectAccessReview query input/result, spec/status, revision vector and hypothetical-input semantics |
| Acceptance | All | Real replica/rolling/functional-fault matrix, image delta, docs and correctness closeout; sustained capacity remains in #451 |

### Compatibility and cutover

Apply the next unused additive migration (014 at this baseline) using
`gitctl migrate` before starting binaries that require it. Preserve
migrations 001-013 and startup history validation. Deploy feature-capable
APIs/controllers while `rbac-local` remains the active provider; production
dynamic RBAC is disabled until all serving binaries understand its schemas,
actions and lifecycle. Do not send new actions/kinds to older binaries.

Seed equivalent grants and managed owner bindings through authorized common
admission, run the isolated candidate parity verifier, then switch the
serving cohort to OPA. Mixed old/static and new/dynamic serving replicas
must not accept dynamic changes: use a bounded write freeze and traffic
drain during the provider switch. New OPA replicas join traffic only after
FR-011a readiness; healthy replicas continue serving during same-contract
rolling replacement. Rollback to static policy requires a new parity export
and controlled switch, not replay of an obsolete file. Never silently undo
dynamic revocations. Git replacement, if required, is non-overlapping.

### ADR and documentation lifecycle

- Amend Accepted ADR-0010 and its index notes for dynamic RBAC and the
  validation-start consistency boundary without changing its acceptance date.
- Amend contributor design doc 022 to replace obsolete IAM entities and
  old endpoint assumptions. Update supported operator docs without internal
  spec/task references. Release changelogs communicate changes to external
  consumers; external quickstart parity is not an implementation gate.
- ADR-0018 remains Proposed: RBAC adopts one primary group and CAS-safe,
  repeatable lifecycle work, not the ADR's platform-wide lease/fencing
  decision. This plan does not implement its whole core and must not accept
  it as a side effect. If implementation scope is expanded to that core,
  task generation must explicitly add `Proposed -> Accepted (YYYY-MM-DD)`
  and index updates in that implementing PR, using the ADR dating rules.
- Leave ADR-0011/0014/0015/0016/0019/0020 Proposed; this feature does not
  implement their cores.
- Once every non-deferred feature correctness gate is verified, change
  spec 066 to Closed in the implementing PR with an explicit
  correctness-only acceptance summary. Do not close #451's outstanding
  production acceptance, authorize production activation or claim #487
  image-reduction completion.

## Complexity Tracking

No constitution exception requested. A tier-level revision/CAS boundary is
necessary to avoid partial RBAC publication; it is not a global transaction
coordinator. OPA is justified by the explicit provider requirement. No
additional service, provider-specific controller, persisted translated
policy or general-purpose containment solver is planned.
