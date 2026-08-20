<!--
Sync Impact Report:
- Version change: 1.0.0 -> 2.0.0
- Modified principles:
  - IV. Observability & Debuggability -> IV. Production Observability & Debuggability
  - VI. Incremental Delivery -> VI. Independently Deployable Delivery
  - VII. Simplicity & YAGNI -> VII. Simplicity with Proven Scale
- Added principles:
  - VIII. Horizontally Replicable Core Services
  - IX. Multi-User Authentication, Authorization & Isolation
  - X. Production Capacity, Backpressure & Load Validation
- Added sections:
  - Core Service Topology
  - Replica Safety
  - Production Capacity Envelope
- Removed sections:
  - None
- Templates requiring updates:
  - ✅ .specify/templates/plan-template.md
  - ✅ .specify/templates/spec-template.md
  - ✅ .specify/templates/tasks-template.md
  - ✅ .specify/templates/commands/ (directory absent; no command templates to update)
- Runtime guidance updated:
  - ✅ README.md
  - ✅ docs/architecture.md
  - ✅ docs/developer-guide.md
  - ✅ AGENTS.md
  - ✅ specs/048-scylla-query-design/plan.md
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
API, controller-manager, and Git-service replicas. Features MUST be deployable
incrementally, support rolling upgrades, and remain correct when old and new
replicas overlap within the documented compatibility window. Priority order is
defined by each feature specification rather than a fixed historical roadmap.

**Rationale:** Core services scale and roll independently. Delivery that requires
a simultaneous fleet restart is operationally unsafe.

### VII. Simplicity with Proven Scale

Implement the simplest design that satisfies the declared production envelope.
New services, brokers, caches, indexes, and abstractions MUST have measured or
contractual justification. Simplicity MUST NOT be used to justify process-local
correctness state, unbounded scans, single-replica assumptions, authorization
bypasses, or designs that fail at the required scale.

**Rationale:** Unnecessary components increase operational burden, but an
under-designed system merely defers that burden to production incidents.

### VIII. Horizontally Replicable Core Services (NON-NEGOTIABLE)

The API, controller manager, and Git service MUST each support deployment with
multiple replicas and autoscaling. Correctness MUST NOT depend on requests
returning to the same process. Durable state, work ownership, idempotency,
concurrency control, repository placement, and recovery MUST have an explicit
replica-safe design. A feature that introduces process-local correctness state
MUST provide a replica-consistent replacement before production use.

**Rationale:** Replica deployment is a core operating mode, not a future
optimization. Autoscaling must increase capacity without creating divergent
catalogue state, duplicate reconciliation, or conflicting Git references.

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
- Git-service replicas MUST define repository placement, single-writer or
  equivalent reference-update safety, routing, storage durability, and failover.
- Local memory and local filesystem state MAY be used for development or caches,
  but production correctness MUST survive process replacement and rescheduling.
- Rolling upgrades MUST preserve contract compatibility and avoid split-brain
  state.

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
- Autoscaling and failover tests MUST demonstrate correct behavior with at least
  two replicas for every affected core service.

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
- Core-service changes document and test behavior with multiple replicas.
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

**Version**: 2.0.0 | **Ratified**: 2026-03-09 | **Last Amended**: 2026-08-19
