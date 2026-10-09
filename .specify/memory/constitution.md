<!--
Sync Impact Report:
- Version change: 2.0.0 -> 3.0.0
- Modified principles:
  - VI. Independently Deployable Delivery (service-specific rollout)
  - VII. Simplicity with Proven Scale (explicit singleton Git constraint)
  - VIII. Horizontally Replicable Core Services -> VIII. Service-Specific Replica Safety
- Added sections: None
- Removed sections: None
- Rationale: Correct the unsupported all-service replication mandate introduced
  in 5f69102 (#363); Git repository sharding/routing is not implemented.
  This is a MAJOR redefinition, not evidence that API/controller HA is complete.
- Templates requiring updates:
  - ✅ .specify/templates/plan-template.md
  - ✅ .specify/templates/spec-template.md
  - ✅ .specify/templates/tasks-template.md
  - ✅ .specify/templates/commands/ (directory absent; no command templates to update)
- Runtime guidance updated:
  - ✅ AGENTS.md (also used by the CLAUDE.md symlink)
  - ✅ README.md
  - ✅ docs/architecture/README.md
  - ✅ docs/developer-guide.md
  - ✅ docs/runbooks/production-readiness-testing.md
  - ✅ docs/ADRs/0015-resource-lifecycle-hooks.md
  - ✅ docs/ADRs/0018-controller-ownership-concurrency-and-fencing.md
  - ✅ docs/implementation/036-git-service-extension-architecture.md
  - ✅ gitstore-controller-manager/README.md
  - ✅ specs/063-implement-secret-adrs/ (spec, plan, research, capacity contract, tasks)
- Follow-up TODOs: None
-->

# GitStore Constitution

## Core Principles

### I. Test-First Development (NON-NEGOTIABLE)

Tests MUST be written before implementation code. Every user story requires the
smallest appropriate contract and integration tests, written first and verified
to fail before implementation begins. Changes to concurrency, replica behavior,
authorization, data integrity, or load-bearing paths MUST include tests for
failure and recovery, not only the successful path.

**Rationale:** GitStore accepts commerce data through Git and projects it across
multiple services and storage models. Test-first development prevents silent
data corruption and makes cross-service behavior reviewable.

### II. API-First Design

All service boundaries MUST define contracts before implementation. GraphQL
schemas, gRPC protocols, datastore interfaces, event shapes, and controller
contracts MUST be reviewed and version-controlled before handlers or consumers
are implemented. Authorization and error semantics are part of each contract.

**Rationale:** The Go API and controller manager, Rust Git service, and external
clients must evolve independently without relying on undocumented behavior.

### III. Clear Contracts & Versioning

All public interfaces MUST follow semantic versioning. Breaking changes require
a MAJOR version bump, additive features require MINOR, and compatible fixes
require PATCH. GraphQL changes MUST prefer additive evolution and deprecation
before removal. Persisted and inter-service contract changes MUST document
compatibility, rollout order, and rollback behavior.

**Rationale:** Stable contracts permit independent replica rollouts, safe
rollback, and compatibility across services during rolling deployment.

### IV. Production Observability & Debuggability

The API, controller manager, and Git service MUST emit structured logs, metrics,
health/readiness state, and correlation identifiers appropriate to their
boundaries. Signals MUST expose request latency, errors, authorization outcomes,
queue depth, retry/backoff behavior, replica saturation, Git push stages, and
controller convergence. Errors MUST identify the affected resource and operation
without exposing credentials or sensitive content.

**Rationale:** Autoscaled services and asynchronous reconciliation cannot be
operated safely without end-to-end evidence of load, failure, and recovery.

### V. User Story Driven Development

All work MUST map to prioritized user stories with independent acceptance
criteria. Tasks MUST retain story labels for traceability. Each story MUST state
the user-visible result and, when it affects a core service, measurable
availability, authorization, and capacity expectations.

**Rationale:** User stories keep production-hardening work tied to observable
outcomes rather than speculative infrastructure.

### VI. Independently Deployable Delivery

Every delivery slice MUST preserve compatibility with independently deployed
API, controller-manager, and Git-service versions. API/controller changes MUST
remain correct when old and new replicas overlap within the documented
compatibility window. Git-service replacement MUST stop the old process before
starting its replacement against the retained repository storage; overlapping
Git writers and zero-downtime Git failover are not supported. Priority order is
defined by each feature specification rather than a fixed historical roadmap.

**Rationale:** Independent service delivery does not imply that every service
can scale horizontally or roll with overlapping instances.

### VII. Simplicity with Proven Scale

Implement the simplest design that satisfies the declared production envelope.
New services, brokers, caches, indexes, and abstractions MUST have measured or
contractual justification. Simplicity MUST NOT be used to justify process-local
correctness state, unbounded scans, undocumented single-replica assumptions,
authorization bypasses, or designs that fail at the required scale. The explicit
singleton Git constraint below MUST NOT be treated as a defect that unrelated
features must solve.

**Rationale:** Unnecessary components increase operational burden, but an
under-designed system merely defers that burden to production incidents.

### VIII. Service-Specific Replica Safety (NON-NEGOTIABLE)

API and controller-manager changes MUST define and verify correctness with
multiple replicas. Durable state, work ownership, idempotency, concurrency
control and recovery MUST have an explicit replica-safe design. Process-local
correctness state MUST NOT be presented as replica-safe without a consistent
replacement. This requirement is not a claim that every current API provider,
controller operation or deployment already satisfies it; plans MUST identify
implementation gaps and limit readiness claims to verified paths.

The Git service is stateful and **singleton-only**: exactly one active
`gitstore-git-service` process per deployment. Repository sharding and
placement-aware routing are not implemented. Multiple Git instances with
separate volumes are not a supported sharded deployment, and a shared volume
does not make multiple writers safe. Feature specifications, plans, tasks and
capacity gates MUST NOT require Git replicas, autoscaling or HA unless the
feature explicitly implements and verifies repository sharding/placement,
routing, writer safety, storage durability and recovery. Such work requires
its own approved scope, not an inferred prerequisite of another feature.

**Rationale:** Replica-safe API/controller behavior and singleton Git storage
are different contracts. Treating an unimplemented scaling goal as a shipped
capability repeatedly creates unsafe deployments and unrelated scope expansion.

### IX. Multi-User Authentication, Authorization & Isolation (NON-NEGOTIABLE)

GitStore MUST support concurrent human, service, and agent identities through
the pluggable authentication and authorization infrastructure. Every external
and service-to-service entry point MUST authenticate callers and enforce
authorization at the owning service boundary. Namespace and repository access
MUST be isolated by policy; UI behavior MUST never be the enforcement layer.
Identity and authorization decisions MUST be auditable and replica-consistent.

**Rationale:** Commerce operations involve multiple users and automation agents
with different privileges. A single-admin or trusted-network assumption is not
an acceptable production security model.

### X. Production Capacity, Backpressure & Load Validation (NON-NEGOTIABLE)

GitStore MUST support catalogues containing at least 5,000,000 products and
sustained peak Git push workloads. Core read and reconciliation paths MUST use
bounded, query-first access patterns. Push validation, admission, projection,
and reconciliation MUST apply bounded concurrency, backpressure, timeouts, and
retry policies rather than unbounded queues or goroutines.

Every feature affecting a load-bearing path MUST define its production dataset,
request or push concurrency, payload shape, latency/error objectives, and soak
duration. It MUST validate those objectives with repeatable load, soak, or
capacity tests before production readiness is claimed.

**Rationale:** Short benchmarks do not prove that GitStore can sustain commerce
traffic. Capacity must remain stable under prolonged pushes, large catalogues,
replica changes, retries, and downstream slowdown.

## Architecture Constraints

### Core Service Topology

GitStore has three independently deployable core services:

1. **API (`gitstore-api`, Go)**: GraphQL, Git Smart HTTP front door,
   authentication/authorization, admission, and datastore access.
2. **Controller Manager (`gitstore-controller-manager`, Go)**: Watch, queue,
   reconcile, status, retry, and operational control loops.
3. **Git Service (`gitstore-git-service`, Rust)**: Bare repository storage,
   Git transport primitives, reference updates, and receive-hook execution.

The admin UI and other GraphQL/Git clients are optional consumers, not core
services. Core services communicate only through versioned contracts and MUST
not depend on another service's private storage.

### Replica Safety

- API replicas MUST keep durable resource, authorization, session, revocation,
  and idempotency semantics consistent across replicas.
- Controller-manager replicas MUST use idempotent reconciliation and an explicit
  coordination, partitioning, or duplicate-safe work model.
- Git service MUST remain singleton-only until the prerequisites in Principle
  VIII are implemented and verified. Tests MUST retain one Git process and
  MUST NOT infer replication support from disjoint repository volumes.
- Local memory and local filesystem state MAY be used for development or caches,
  but production correctness MUST survive process replacement and rescheduling.
  Git repository storage is authoritative persistent state, not a disposable
  cache; replacement MUST retain it and prevent writer overlap.
- API/controller rolling upgrades MUST preserve contract compatibility.
  Git upgrades MUST use non-overlapping replacement and account for downtime.

### Production Capacity Envelope

- Authoritative and projected catalogue storage MUST support at least 5,000,000
  products without full-dataset scans on routine request paths.
- Git push handling MUST remain stable during sustained peak traffic, including
  validation, admission, datastore projection, watch delivery, and controller
  convergence.
- Queues, partitions, batches, request bodies, worker pools, retries, and
  timeouts MUST have explicit bounds.
- Feature plans MUST state measurable p95/p99 latency, throughput, error-rate,
  recovery, and resource-saturation objectives for affected production paths.
- Replica tests MUST demonstrate correct behavior with at least two instances
  of each affected API/controller service. Git MUST use exactly one active
  instance, including in production-mode capacity evidence. A capacity pass
  MUST NOT be described as proof of Git HA.

## Development Workflow

### Test-First Workflow (Enforced)

1. Define API, authorization, replica, and capacity contracts where applicable.
2. Write contract and integration tests.
3. Add concurrency, failover, and recovery tests for core-service changes.
4. Verify the new tests fail for the expected reason.
5. Implement the minimum code required to pass.
6. Refactor while preserving all tests.
7. Run focused load or soak validation for load-bearing changes.
8. Commit tests and implementation in the same logical change.

### Task Execution Order

1. **Setup**: Project structure, tooling, and test fixtures.
2. **Foundational**: Contracts, auth boundaries, replica model, observability,
   and capacity harnesses that block user stories.
3. **User Stories**: Implement independently testable stories in feature-defined
   priority order.
4. **Production Readiness**: Load/soak, failover, rolling-upgrade, security, and
   runbook validation.

### Quality Gates

- `make pr-ready` passes before a pull request is considered ready.
- New behavior has tests that were demonstrated to fail before implementation.
- API/controller changes document and test concurrent-replica behavior and
  implementation limitations. Git changes document and test singleton
  concurrency, persistent-storage recovery and non-overlapping replacement
  where applicable, not Git replication.
- Load-bearing changes meet declared capacity and sustained-load objectives.
- Protected operations include authentication and authorization tests.
- Logs, metrics, readiness, and error handling cover new operational states.
- Contract changes document compatibility, rollout, and rollback.
- Documentation and runbooks are updated with the implementation.
- Constitution compliance is verified during PR review.

## Governance

### Authority

This constitution supersedes all other development practices, conventions, and
preferences. Runtime guidance may add detail but may not weaken these rules.

### Amendment Process

1. Propose an amendment with rationale and concrete examples.
2. Document impact on existing code, active plans, templates, and operations.
3. Require team consensus for MAJOR version changes.
4. Update dependent Spec Kit templates and runtime guidance.
5. Record the semantic version change and amendment date.

### Compliance Review

- Every feature plan MUST contain a pre-design and post-design constitution check.
- Every pull request MUST include constitution compliance verification.
- Principle violations MUST be explicit in the plan's complexity table, include
  rejected alternatives, identify risk, and link a remediation issue.
- Principles marked NON-NEGOTIABLE cannot be waived for production readiness.

### Version Control

- Constitution changes MUST increment the version.
- MAJOR: Principle removal, redefinition, or incompatible governance change.
- MINOR: New principle or materially expanded mandatory guidance.
- PATCH: Non-semantic clarification, wording improvement, or typo fix.

### Runtime Guidance

Day-to-day repository and agent instructions supplement this constitution.
When they conflict, this constitution takes precedence.

**Version**: 3.0.0 | **Ratified**: 2026-03-09 | **Last Amended**: 2026-10-03
