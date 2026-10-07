# Implementation Plan: Namespace Validation and Admission Matrix

**Branch**: `047-namespace-admission-matrix` | **Date**: 2026-08-29 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/047-namespace-admission-matrix/spec.md`

## Summary

Make Namespace create, update, and delete outcomes machine-distinguishable without
duplicating the lifecycle owned by spec 046. Refactor the existing validation
path into ordered structural/pre-receive and stateful policy phases, with
immutable changes carrying a distinct structural reason;
reject updates to terminating namespaces; aggregate bootstrap and non-empty
delete blockers; add an additive GraphQL deletion outcome; converge stale
Namespace admission work from the current Git head; preserve the complete
authored manifest and provenance; share identifier validation across GraphQL
and Git; and close repository-create versus Namespace-delete races with a
durable backend coordination fence that also covers repository transfer into a
target Namespace. Production activation is staged behind an explicit rollout
gate; rollback artifacts retain migration 005 even when behavior code is
reverted.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`); generated GraphQL/protobuf contracts use the repository's existing generators  
**Primary Dependencies**: Existing `cataloggrpc.ValidateResources`, `internal/validate` parser, `internal/namespace` admission helpers, gqlgen v0.17.90, `gqlerror`, `go-memdb`, gocqlx/gocql, zap; no new dependency  
**Storage**: Existing Namespace rows in go-memdb; ScyllaDB adds nullable internal `repository_creation_epoch` and `pending_repository_creations` fence columns to `namespaces_by_uid` via migration 005, with no new table or GraphQL field. Migration 005 is forward-only durable history and remains embedded in every supported behavior rollback artifact.  
**Testing**: Go unit, contract, resolver, admission, datastore-backed policy, multi-replica concurrency, and GraphQL response tests; existing Rust hook tests remain compatibility coverage  
**Target Platform**: Linux server; Darwin/Linux development environments  
**Project Type**: Go API/admission service with an unchanged Rust Git hook consumer and existing controller status projection  
**Performance Goals**: With 500 changed resource files containing at most 50 Namespace manifests, sustain 10 validation requests/second at 20 concurrent requests across two API replicas for 30 minutes; validation RPC p95 MUST be ≤100 ms and p99 MUST be ≤250 ms; internal error rate MUST be <0.1%; decision correctness MUST be 100%; after one API replica is replaced, throughput and error rate MUST recover within 30 seconds; each replica MUST remain below 80% CPU, show <10% retained-memory growth, and return goroutine count to within 5% of its pre-soak baseline  
**Constraints**: Structural failures, including immutable-name changes, short-circuit policy evaluation; immutable failures are distinct structural reasons rather than a third phase; policy decisions remain authoritative under concurrent API replicas; stale descendant commits converge only Namespace paths whose content is unchanged at the descendant head; bootstrap and non-empty delete blockers are both returned when both apply; repository creation, transfer, and termination use durable backend coordination rather than process-local locks; production Scylla deployments keep those mutations disabled until migration and fleet convergence are verified; no cross-resource namespace-existence plugin  
**Scale/Scope**: Namespace's own create/update/delete path only; namespace count is small relative to the 5,000,000-product catalogue, while push batches remain bounded by the existing admission request limits  
**Replica/Scaling Model**: Stateless validation runs on any API replica; stateful decisions use durable datastore reads, resource-version conditional writes, and the Scylla Namespace repository epoch/pending LWT fence (or one memdb write transaction); repository transfers reserve the target Namespace through the same fence; repeated deletion, descendant admission, concurrent updates, and repository create/transfer/delete races remain idempotent or conflict-safe across replicas. During a mixed-version rollout, a fleet-wide ingress/AuthZ maintenance deny is mandatory because pre-gate binaries cannot interpret the new config.  
**Authentication/Authorization**: Existing GraphQL field authorization remains the enforcement boundary; this feature changes only post-authorization validation and error reporting and does not broaden namespace or repository access  
**Load/Backpressure Model**: Reuse the existing bounded request/blob aggregation; no queues, goroutines, retries, or full scans are introduced; policy reads are keyed and deletion performs a constant number of datastore operations

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Gate evaluation |
|---|---|
| I. Test-First Development | PASS — phase ordering, reason codes, combined deletion blockers, idempotency, concurrency, and status assertions are specified as failing tests before implementation. |
| II. API-First Design | PASS — validation reason conventions and the additive GraphQL deletion outcome are documented in `contracts/` before resolver changes. |
| III. Clear Contracts & Versioning | PASS — GraphQL adds a non-null output field without changing existing selections; protobuf shape remains unchanged; schema, fence-gate, migration, and rollback-artifact order are explicit. |
| IV. Production Observability & Debuggability | PASS — rejection phase/reason, namespace, operation, and blocker count are structured log fields without manifest contents or credentials. |
| V. User Story Driven Development | PASS — implementation slices map directly to US1 phase separation, US2 deletion outcomes, and US3 persisted admission status. |
| VI. Independently Deployable Delivery | PASS — old clients ignore the additive GraphQL field; clients select `outcome` only after every API replica exposes it; production Scylla defaults the repository fence gate off until migration 005 and full API convergence are verified; rollback retains migration 005 and keeps unsafe mutations globally denied. |
| VII. Simplicity with Proven Scale | PASS — existing validation, gqlerror, lifecycle, and datastore primitives are extended; two internal Scylla fence columns are added instead of a service, lock manager, or table. |
| VIII. Horizontally Replicable Core Services | PASS — no process-local decision state; policy and repository lifecycle are rechecked at durable conditional-write boundaries. |
| IX. Multi-User Authentication, Authorization & Isolation | PASS — authorization precedes validation and deletion checks, with no new bypass or cross-namespace read surface. |
| X. Production Capacity, Backpressure & Load Validation | PASS — work is request-bounded and query-first, with two-replica concurrency and sustained validation coverage required. |

**Pre-design gate result**: PASS. No violations require a complexity exception.

## Project Structure

### Documentation (this feature)

```text
specs/047-namespace-admission-matrix/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── namespace-admission-matrix.md
│   └── namespace-deletion.graphqls
└── tasks.md
```

### Source Code (repository root)

```text
shared/
└── schemas/
    └── namespace.graphqls

gitstore-api/
└── internal/
    ├── cataloggrpc/
    │   ├── server.go
    │   └── server_test.go
    ├── namespace/
    │   ├── admission.go
    │   ├── admission_test.go
    │   └── validation.go
    ├── datastore/
    │   ├── memdb/backend.go
    │   └── scylla/
    │       ├── repository.go
    │       └── migrations/005_namespace_repository_fence.cql
    └── graph/
        ├── model/
        └── resolver/
            ├── service.go
            ├── namespace.resolvers.go
            └── namespace_service_test.go

gitstore-controller-manager/
└── internal/namespace/
    └── reconciler_test.go
```

**Structure Decision**: Extend the existing API validation/admission and
GraphQL lifecycle surfaces in place. The Git service continues to consume the
existing validation response, and the controller's status ownership remains
unchanged; both receive regression coverage rather than new implementations.

## Phase 0: Research Outcomes

Research is captured in [research.md](research.md). Decisions resolved:

- Treat structural/pre-receive and policy admission as the two ordered phases;
  immutable-name rejection is a separately coded structural outcome.
- Use existing `ValidationError.constraint` values for pre-receive reason
  classification; do not change the protobuf.
- Use GraphQL error extensions for machine-readable create/update/delete
  rejection reasons.
- Add `NamespaceDeletionOutcome` to distinguish `TERMINATION_STARTED` from
  `ALREADY_TERMINATING`; keep blockers as GraphQL precondition errors.
- Check already-terminating first, then aggregate bootstrap and non-empty
  blockers before mutating lifecycle state.
- Preserve spec 046's durable status, finalizer, and controller ownership.
- Keep policy reads keyed and recheck state at conditional writes for
  multi-replica safety.
- When a Namespace admission commit has a descendant head, read the same path
  from that head. Converge with the request actor/timestamp only when the
  descendant preserved that exact Namespace content; when the descendant
  changed the same content, the stale handler does not write and the exact-head
  handler owns content and audit attribution. Continue to skip stale
  non-Namespace admission work.
- Persist the complete authored Namespace envelope, labels, annotations, spec,
  body, and Git provenance. Author changes advance generation and
  resourceVersion; provenance-only changes advance only resourceVersion.
- Use one shared Namespace identifier validator for GraphQL and Git, while
  leaving bootstrap-name rejection in the policy phase.
- Coordinate repository creation, target-Namespace transfer, and Namespace
  termination durably: memdb uses one write transaction, while Scylla
  reserves/increments the target Namespace
  repository epoch and pending counter with LWT before repository commit and
  conditionally marks termination only when the observed epoch is unchanged and
  no creation remains pending.

No `NEEDS CLARIFICATION` remains.

## Phase 1: Design and Contracts

### Ordered validation pipeline

For each `ValidateResources` request:

1. Parse and structurally validate all candidate blobs, including envelope,
   name/path, required fields, reserved identifiers, duplicate identities, and
   same-path `metadata.name` immutability.
2. If any structural error exists, including an immutable-name error, return
   only structural-phase errors and do not run stateful policy checks.
3. Evaluate stateful Namespace policy for otherwise-valid entries: bootstrap
   target rejection already owned by spec 046, tier demotion, and update of a
   terminating Namespace. Spec 047 standardizes the phase/reason taxonomy; it
   does not introduce a new bootstrap create/update rule.
4. Return stable field/constraint/message values without exposing internal IDs.

GraphQL create/update follows the same phase order and returns `gqlerror.Error`
extensions with a stable category code and specific reason. Admission retains
the same policy checks at the conditional write boundary so a race cannot bypass
the preflight result.

The DNS-label and reserved-identifier rules are implemented once in
`internal/namespace` and called by both GraphQL input conversion and the Git
frontmatter parser. `default` and `gitstore-system` remain structurally valid so
the existing bootstrap policy can reject them with `BOOTSTRAP_NAMESPACE`.

A Namespace name change is correlated only when the old and proposed manifests
share the same repository path. A simultaneous path-and-name change is a new
declaration, not an inferred rename, because Namespace manifests carry no stable
author-controlled identity.

### Descendant convergence and authored state

GraphQL synchronous admission and catalog gRPC both treat Git head as the
authoritative ordering boundary. If the submitted commit has a descendant head,
they read and parse the Namespace manifest at the current head and original
path. When the bytes/content for that path are unchanged, a stale handler may
materialize the request using the request actor and timestamp. When the same
Namespace content changed, the stale handler performs no write; only the
exact-head handler may persist the descendant content and its actor/timestamp.
A ref change during parsing or writing retries or returns the existing
exact-head row. Stale non-Namespace paths retain the previous skip behavior.

The admitted row stores the complete accepted authored envelope, labels,
annotations, full `NamespaceSpec`, body, and Git path/SHA/ref provenance.
Changes to any authored field advance generation and resourceVersion;
provenance-only changes leave generation unchanged and advance only
resourceVersion.

### Deletion decision

`DeleteNamespace` reloads the authorized identity, then:

1. returns `ALREADY_TERMINATING` without advancing resource version when the
   current row already has a deletion timestamp;
2. evaluates bootstrap and indexed `HasRepositories` blockers independently;
3. returns one precondition error containing all applicable stable blocker
   reasons;
4. otherwise uses `MarkNamespaceDeletion` to atomically/conditionally prove no
   repository creation crossed the emptiness decision, write the deletion
   timestamp/finalizer with the expected resource version, and return
   `TERMINATION_STARTED`.

Repository creation uses `CreateRepositoryInActiveNamespace`; repository
transfer reserves the target Namespace inside `TransferRepository`. memdb
checks the target lifecycle and moves the mapping plus authoritative repository
row in one write transaction. Scylla first increments the target Namespace's
durable `repository_creation_epoch` and `pending_repository_creations` by LWT
only while `deletion_timestamp` is null. The repository create or transfer then
commits; completion decrements only the pending counter and leaves the epoch
monotonic. The deletion LWT requires the expected resource version, the same
observed epoch, and zero pending mutations. Thus an in-flight create/transfer is
visible through the pending counter, a completed mutation crossing the
emptiness read changes the epoch, and a winning termination blocks new target
reservations.

The additive GraphQL contract is defined in
[contracts/namespace-deletion.graphqls](contracts/namespace-deletion.graphqls).

Rollout has two independent gates:

1. GraphQL remains server-first: deploy `outcome` everywhere before clients
   select it.
2. Repository fencing uses
   `GITSTORE_FEATURES__NAMESPACE_REPOSITORY_FENCE=auto|disabled|enabled`.
   `auto` preserves memdb development behavior but resolves to disabled on
   Scylla. Before replacing the first production replica, deny
   `deleteNamespace`, `completeNamespaceDeletion`, `createRepository`, and
   `transferRepository` fleet-wide at ingress/AuthZ because legacy binaries do
   not understand the gate. Apply migration 005, deploy every new replica with
   the gate disabled, verify fleet and schema convergence, then deploy
   `enabled` everywhere before removing the external deny.

Rollback first restores the fleet-wide deny, then disables the gate. A
supported behavior rollback artifact may revert the new behavior but MUST keep
the complete migration bundle through
`005_namespace_repository_fence.cql`. Arbitrary older binaries embedding only
001-004 are unsupported after 005 because gocqlx correctly reports that the
database is ahead. The external deny remains until a forward-capable fenced
fleet is restored.

### Status contract

Admission continues to persist `AdmissionAccepted=True` with
`observedGeneration` equal to the admitted generation. The controller continues
to own `SystemRepoReady` and `Ready`, while `Terminating` remains derived from
deletion timestamp/finalizers. This feature adds contract tests, not a second
status writer or persisted decision record.

### Test-first implementation order

1. Add failing contract tests for phase short-circuiting and stable reason codes.
2. Add failing Namespace policy tests for tier demotion and terminating targets.
3. Add failing resolver tests for combined blockers and both deletion outcomes.
4. Add GraphQL contract/generated-code changes for the additive outcome field.
5. Implement ordered validation and shared typed reason helpers.
6. Implement aggregated deletion decisions and durable repository lifecycle
   coordination while preserving authorization and resource-version checks.
7. Add a cross-kind regression proving Namespace policy never evaluates or
   blocks other resource kinds.
8. Add two-replica/concurrent update/delete tests and mixed-version compatibility
   assertions, including server-first GraphQL rollout.
9. Add status and observability regression tests and a threshold-enforcing
   30-minute capacity test, then run focused tests and `make pr-ready`.

### Post-design Constitution Check

| Principle | Result |
|---|---|
| Test-First | PASS — every behavioral slice begins with contract/resolver tests. |
| API-First | PASS — reason and deletion-outcome contracts precede implementation. |
| Clear Contracts | PASS — stable machine codes, compatibility, and ownership are explicit. |
| Observability | PASS — structured phase/reason logging is required without sensitive payloads. |
| User Story Driven | PASS — each story has an independently testable implementation slice. |
| Independent Delivery | PASS — GraphQL evolution is additive and protobuf-compatible. |
| Simplicity | PASS — existing helpers and lifecycle state are reused, with two internal Scylla fence columns. |
| Replica Safety | PASS — durable reads, per-resource Git convergence, memdb transactions, and Scylla LWTs remain authoritative. |
| Multi-User Security | PASS — existing authz boundary remains unchanged and tested. |
| Production Capacity | PASS — constant/keyed datastore work and bounded push processing are preserved. |

**Post-design gate result**: PASS. No complexity exceptions required.

## Complexity Tracking

No constitution violations require justification.
