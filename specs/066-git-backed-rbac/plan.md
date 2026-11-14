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
`accessReview(input:)`, following the existing TokenRequest input/payload
pattern. Requests use `spec`; results use `status`. Reviews are not a fifth
Git-backed kind and have no durable resource lifecycle.

The November 9 clarification deliberately chooses authorization snapshots
current at validation start, not cross-repository commit-time linearizability.
A concurrent revocation can precede completion of an already-authorized
write. Conservative permission coverage requires explicit bind/escalate
where full coverage cannot be proved. This changes the earlier FR-012
interpretation; it is not an implementation of a global authorization fence.

The plan is not acceptance evidence. Implementation progress and verified
boundaries are recorded in the tasks and contracts; production activation
and final image-size results remain subject to the acceptance gates.

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
**Performance Goals**: Decision p99 <=2 ms; admitted grant/revoke propagation p95 <=2 s and p99 <=5 s; recovery <=30 s; 50 RBAC writes/min.
**Constraints**: Fail closed before snapshot/watch readiness; no I/O per ordinary authorization decision; immutable built-ins; bounded rebuilds, payloads and fan-out; no operator Rego.
**Scale/Scope**: 1,000 namespaces, 10,000 roles, 50,000 bindings. RBAC paths must not scan the 5,000,000-product catalog; sustained catalog push traffic remains part of interference validation.
**Replica/Scaling Model**: At least two APIs with independent reconstructible OPA caches and two controllers with independent checkpoint volumes. Same-tier resource/projection writes use durable CAS; controllers use duplicate-safe recomputation, not process-local locks as distributed fences.

**Topology constraint**: Exactly one active Git service retains existing storage.
Git replacement is stop-before-start. No Git replication, sharding, routing
or zero-downtime replacement is introduced.

**Authentication/Authorization**: Preserve AuthN/UserDir/AuthZ independence and signed receiver-bound RPCs. Trusted UserDir Staff membership gates personal namespace creation. Every external mutation/read/watch/review is authorized; privileged lifecycle templates are server-validated even for administrators.
**Load/Backpressure Model**: Defaults in the capacity contract: four global tier rebuild workers, one rebuild per tier, bounded/coalesced work, 30 s validation deadline, 1,000-entry pages, 64 KiB manifests, 4 MiB/256-manifest candidate changes and 64 MiB tier snapshots. Thirty-minute healthy and fault workloads; offered load and completed throughput reported separately.
**Capacity Profile**: Extend existing `namespace/watch` and `api/readiness MODE=alpha` drivers and selectors. Their current implementations do not yet test RBAC; extensions are implementation prerequisites, not existing acceptance.
**Fault Profile**: Reuse owned `api-restart`/controller restart facilities; extend the existing runner for a bounded, owned watch-journal interruption. Verify domain recovery and process epochs, not just injector exit.

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
| PR-003/005/006 | Capacity/recovery contract: dataset, offered load, oracle, real replicas and physical faults |
| SC-001-008 | Healthy/fault gates and test-first matrix in capacity/recovery contract |

## Phase 2: Delivery Planning

This is the input to task generation, not task execution.

| Slice | Stories | Deliverable and failing-test gate |
|---|---|---|
| Foundation | All | Schemas/catalog/model, API/store/RPC contracts, strict parser and provider-neutral decision/coverage fixtures |
| Resource pipeline | US2/US3 | memdb/Scylla CAS and CDC; pre-receive + GraphQL parity; whole-candidate rejection, projection lag and replay recovery tests |
| Provider | US1 | Embedded OPA, readiness, snapshot/watch bridge, bounded rebuilds, exact evaluated revisions; cold-start and revoked-grant regressions |
| Bootstrap/lifecycle | US4/US6 | Immutable built-ins, once-only initial admin, role/binding finalizers, Staff owner template, UID recreation and concurrent controllers |
| Inspectability | US5 | Authorized resource Node/connection/watch; transient SubjectAccessReview input/payload, spec/status, revision vector and hypothetical-input semantics |
| Acceptance | All | Real replica/rolling/fault matrix, declared load, image delta, docs and lifecycle close-out |

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
- Once every feature acceptance gate is verified, change spec 066 to
  Closed in the implementing PR. Do not close spec 064's outstanding #451
  acceptance or claim #487 image-reduction completion.

## Complexity Tracking

No constitution exception requested. A tier-level revision/CAS boundary is
necessary to avoid partial RBAC publication; it is not a global transaction
coordinator. OPA is justified by the explicit provider requirement. No
additional service, provider-specific controller, persisted translated
policy or general-purpose containment solver is planned.
