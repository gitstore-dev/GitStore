<!--
Sync Impact Report:
  - Version change: 4.0.0 -> 4.2.0 (MINOR: incorporates the draft 4.1.0 storage
    guidance, explicit profile/provider scope and initial-alpha activation
    guidance; production obligations retained)
  - Modified principles: III Clear Contracts & Versioning; VI Independently
    Deployable Delivery; VII Simplicity with Proven Scale;
    VIII Service-Specific Replica Safety;
    IX Multi-User Authentication, Authorization & Isolation;
    X Production Capacity, Backpressure & Load Validation
  - Added sections: Deployment Profile and Provider Scope;
    Storage Access and Automatic Drift Recovery
  - Removed sections: None
  - Templates updated: .specify/templates/plan-template.md,
    .specify/templates/spec-template.md, .specify/templates/tasks-template.md
  - Other templates checked: agent-file, checklist and constitution skeletons
    contain no conflicting replica gates; templates/commands/ is absent.
  - Runtime guidance updated: AGENTS.md, specs/AGENTS.md, tests/AGENTS.md,
    gitstore-api/AGENTS.md, gitstore-controller-manager/AGENTS.md,
    .claude/rules/scylla-migrations.md,
    docs/developer-guide.md, docs/development/production-readiness-testing.md
  - ProductVariant clarifications incorporated: focused implementation checks
    versus deployed qualification; coordinated initial-alpha activation with
    retained state and fix-forward recovery; no hypothetical legacy-release
    migration or downgrade promise. Incorporate the draft 4.1.0 mechanism-neutral
    storage, authority/claim fences, bounded automatic recovery, zero routine
    operator projection work and deliberate alpha re-baseline safeguards.
  - Development-only providers need no shared state, distributed coordination,
    failover or rolling-upgrade implementation. Shared production code still
    requires focused correctness tests; in-process memdb tests remain useful
    evidence of their tested boundaries, never proof of distributed memdb.
  - Production topology, security, capacity and qualification requirements are
    unchanged. Existing feature replica clauses inherit this provider scope.
  - Follow-up TODOs: None introduced. #605 still tracks cross-resource storage
    evaluation and existing recovery gaps; policy is not runtime delivery.
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

Released public interfaces MUST follow semantic versioning. Breaking changes
require a MAJOR version bump, additive features require MINOR, and compatible fixes
require PATCH. GraphQL changes MUST prefer additive evolution and deprecation
before removal. Persisted and inter-service contract changes MUST document
compatibility, rollout order, and rollback behavior.

An initial unreleased alpha contract MAY evolve without a hypothetical prior-release
upgrade/downgrade path or a MAJOR bump for each development iteration. It MUST
document coordinated activation, retained-data handling and fix-forward recovery.
Actual released contracts and retained deployment data keep their safeguards;
this exception does not change constitution versioning.

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

Every delivery slice MUST state its supported API, controller-manager and
Git-service version combinations, activation order and recovery contract.
Production API/controller changes MUST remain correct when compatible old and
new replicas overlap within a supported rolling-upgrade window.
Initial unreleased alpha activation MAY instead stop incompatible development
binaries in a coordinated maintenance window, preserve retained state and use
fix-forward recovery with downgrades unsupported. It MUST NOT imply rolling
compatibility or require legacy-release machinery solely for hypothetical
deployments. Future released upgrade paths MUST define safe version windows
before claiming rolling-upgrade support.
Git-service replacement MUST stop the old process before
starting its replacement against the retained repository storage; overlapping
Git writers and zero-downtime Git failover are not supported. Priority order is
defined by each feature specification rather than a fixed historical roadmap.

**Rationale:** Independent service delivery does not imply that every service
can scale horizontally or roll with overlapping instances.

### VII. Simplicity with Proven Scale

Implement the simplest design that satisfies the declared production envelope.
New services, brokers, caches, indexes, and abstractions MUST have measured or
contractual justification. Simplicity MUST NOT be used to justify process-local
state for distributed production correctness, unbounded scans, undocumented
production single-replica assumptions, authorization bypasses, or designs that
fail at the required scale. The explicit
singleton Git constraint below MUST NOT be treated as a defect that unrelated
features must solve.

Storage design MUST account for total operator and implementation cost, not
table count or presumed performance alone. Routine projection maintenance MUST
require zero operator actions. Additional persistent structures MAY trade disk
for bounded memory; whole-catalog in-memory comparison indexes are not an
acceptable production recovery design.

**Rationale:** Unnecessary components increase operational burden, but an
under-designed system merely defers that burden to production incidents.

### VIII. Service-Specific Replica Safety (NON-NEGOTIABLE)

For production-capable profiles and providers, API and controller-manager
changes MUST define and verify correctness with multiple replicas. Durable
state, work ownership, idempotency, concurrency control and recovery MUST have
an explicit replica-safe design. Process-local correctness state MUST NOT be
presented as replica-safe without a consistent replacement. This requirement
is not a claim that every current API provider,
controller operation or deployment already satisfies it; plans MUST identify
implementation gaps and limit readiness claims to verified paths.

Development-only profiles and providers are explicitly exempt from these
replica-safety requirements under Deployment Profile and Provider Scope below.
This exemption MUST NOT remove useful in-process concurrency tests or weaken
production invariants in code shared with production providers.

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
Identity and authorization decisions MUST be auditable, and MUST be
replica-consistent wherever the production replica contract applies.
Development-only providers retain authentication, authorization and isolation
within their declared local contract.

**Rationale:** Commerce operations involve multiple users and automation agents
with different privileges. A single-admin or trusted-network assumption is not
an acceptable production security model.

### X. Production Capacity, Backpressure & Load Validation (NON-NEGOTIABLE)

GitStore MUST support catalogues containing at least 5,000,000 products and
sustained peak Git push workloads. Core read and reconciliation paths MUST use
bounded, query-first access patterns. Push validation, admission, projection,
and reconciliation MUST apply bounded concurrency, backpressure, timeouts, and
retry policies rather than unbounded queues or goroutines.
Query-first means designing for explicit access patterns, not mandating
application-maintained tables or prohibiting database-managed indexes/views.

Every feature affecting a load-bearing path MUST define its production dataset,
request or push concurrency, payload shape, latency/error objectives, and soak
duration. It MUST validate those objectives with repeatable load, soak, or
capacity tests before production readiness is claimed.

**Rationale:** Short benchmarks do not prove that GitStore can sustain commerce
traffic. Capacity must remain stable under prolonged pushes, large catalogues,
replica changes, retries, and downstream slowdown.

## Architecture Constraints

### Deployment Profile and Provider Scope

- Plans MUST identify affected deployment profiles and provider contracts as
  development-only or production-capable, including topology, state lifetime
  and supported activation/version behavior. Existing generic feature replica
  clauses inherit this scope; choosing a development adapter for a test does
  not exempt the production code path it exercises.
- The local development profile includes the memdb datastore, rbac-local
  authorization, static-users authentication and user directory, and the
  optional development notification provider backed by MailSlurper. These and
  other explicitly documented development/test providers MAY use process-local state
  and single API/controller instances. They MUST NOT be required to implement
  shared durable state, cross-process coordination, replica failover, replica-wide
  state propagation or rolling upgrades solely to satisfy production replica
  requirements. In-memory state MAY be lost on restart under its declared
  development contract; retained Git repositories and other retained data
  MUST NOT be silently reset.
- Development exemptions do not waive ordinary correctness, authentication,
  authorization, tenant isolation, bounded work or data integrity within the
  supported local contract. They do not qualify these providers for production
  or permit production requirements to be bypassed by relabeling a deployment.
- Focused tests MAY construct multiple API handlers or reconcilers sharing one
  memdb instance to exercise changed request isolation, races, CAS, idempotency,
  replay or retry behavior. Preserve useful coverage and add it when changed
  shared-code risks require it. Such tests MUST NOT imply shared state between
  independent memdb processes, or distributed-provider/HA qualification.
  A new two-instance or deployed scaling test is not required for every feature.
- Production-capable paths retain their replica, durable recovery, compatible
  rolling-upgrade and qualification obligations. Test affected shared-code
  invariants with the smallest appropriate fixtures; qualify actual deployed
  provider/topology guarantees separately before making production claims.
  Selecting one development provider in a mixed profile MUST NOT exempt
  affected production-capable components or shared production paths.

### Storage Access and Automatic Drift Recovery

- Each access pattern MUST identify its authoritative state, consistency and
  ordering needs, partition/work bounds, dependencies and recovery owner.
  Secondary indexes (global or local), materialized views, explicit query tables
  and combinations are permitted when they satisfy those contracts. No mechanism
  is presumed faster or safer without applicable evidence.
- Derived lookup/list projections MUST be distinguished from identity
  reservations, unique claims, lifecycle fences, durable intents and retirement
  authority. Database indexes do not by themselves enforce these invariants.
  Recovery MUST NOT reconstruct authority from an unverified projection or
  overwrite accepted desired state, UID ownership or legitimate author choices.
- GitStore MUST automatically detect and converge repairable derived-state drift
  at startup/resume, when detected during operation and through bounded recurring
  verification for missed events. Database-managed maintenance or existing
  API/controller machinery MAY own this work; no particular daemon is required.
  Startup MUST NOT depend on loading the catalogue into memory or on an operator
  invoking a repair command. Production progress/work MUST survive interruption
  and converge safely under concurrent replicas/writers with bounded retries
  and backpressure.
- Missing, stale, dangling and duplicate derived entries MUST recover without
  routine operator audits, repair commands, manual table edits, restart rituals
  or deployment-order repair steps. Recovery MUST verify authoritative identity,
  version and any applicable intent/coverage fences before mutation. Uncertain
  evidence MUST fail closed, not grant authority or certify an empty dependency
  set. True authoritative conflicts or loss MUST remain visible and MUST NOT
  trigger arbitrary winner selection or destructive guesswork.
- Plans MUST define measurable detection/convergence and memory/disk/work budgets,
  automatic bootstrap/rebuild behavior, progress/readiness signals and tests for
  crash, concurrent writes and retry. Manual tools MAY remain for diagnostics or
  exceptional recovery; they MUST NOT be the normal convergence mechanism.
  Zero routine projection work does not eliminate infrastructure monitoring,
  backups, database repair, capacity management or authoritative disaster recovery.
- Before GA, ScyllaDB versions and storage layouts are revisable design choices,
  not assumed immutable pins. Implementations and tests MUST record the actual
  database build/configuration and verify required capabilities; this is not a
  promise of compatibility with every version or an unrecorded `latest` build.
  Deliberate alpha schema re-baselines MAY replace historical layouts, indexes
  and redundant tables with explicit data-preservation/conversion or fresh-keyspace
  procedures. Never silently rewrite an applied migration ledger or reset retained
  deployment data. Released-contract compatibility obligations still apply.

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

The API/controller distributed guarantees below apply to production-capable
profiles and providers; development-only contracts use the scope above. Git
singleton writer safety and retained-storage safeguards apply to every profile.

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
- Production replica qualification MUST demonstrate correct behavior with at
  least two instances of each affected API/controller service. Git MUST use
  exactly one active instance, including in production-mode capacity evidence. A capacity pass
  MUST NOT be described as proof of Git HA.

## Development Workflow

### Test-First Workflow (Enforced)

1. Define API, authorization, replica, and capacity contracts where applicable.
2. Write contract and integration tests.
3. Add focused concurrency, failure and recovery tests for changed risks under
   the affected provider contracts; apply the development replica exemption.
4. Verify the new tests fail for the expected reason.
5. Implement the minimum code required to pass.
6. Refactor while preserving all tests.
7. Run the smallest checks covering the changed behavior; schedule deployed
   load, soak and release rehearsals separately when qualifying production.
8. Commit tests and implementation in the same logical change.

### Task Execution Order

1. **Setup**: Project structure, tooling, and test fixtures.
2. **Foundational**: Contracts, auth boundaries, replica model, observability,
   and capacity harnesses that block user stories.
3. **User Stories**: Implement independently testable stories in feature-defined
   priority order.
4. **Production Readiness**: Load/soak, failover, rolling-upgrade, security, and
   runbook validation.

### Pull Request Readiness

- The full default `make pr-ready` passes before a pull request is considered ready.
- New behavior has tests that were demonstrated to fail before implementation.
- Affected concurrency, authorization, persistence and recovery boundaries
  have focused contract/integration tests and documented limitations. Reuse
  existing coverage; do not require a complete deployed old/new-version
  rehearsal or a new scaling test for every feature.
- Development-only providers have no replica-safety implementation or
  qualification gate. Preserve useful in-process concurrency tests and test
  shared production invariants without turning local providers into distributed
  services. Initial-alpha activation may use the coordinated contract above.
- Migration, wire-format or replay changes test the compatibility risks they
  introduce. A known unsafe rollout or data-loss path remains a blocker.
- Git changes preserve singleton writer safety and retained storage; tests
  cover changed recovery/replacement behavior, not Git replication.
- Protected operations include authentication and authorization tests.
- Logs, metrics, readiness, and error handling cover new operational states.
- Contract changes document compatibility, rollout, and rollback.
- Storage changes justify access structures and test bounded automatic drift
  detection/recovery without operator repair; known legacy gaps are explicitly
  tracked rather than advertised as self-healing.
- Documentation and runbooks are updated with the implementation.
- Constitution compliance is verified during PR review.

### Production Qualification

Before production readiness is claimed, affected load-bearing paths MUST meet
their declared capacity, sustained-load, backpressure and recovery objectives.
Use opt-in capacity/soak, chaos and full deployed rolling-upgrade rehearsals
appropriate to the changed boundaries. Reuse attributable evidence for
unchanged paths; requalify when changes invalidate it.

These checks MUST NOT become routine alpha feature or PR CI prerequisites
unless the delivery explicitly claims production qualification. Feature
plans MUST separate implementation completion from deferred qualification.
Deferred or failed qualification MUST remain visible and MUST NOT be presented
as a pass, a scaling guarantee or permission to bypass deployment safety.
Ordinary PR CI runs correctness, build, lint and security checks; expensive
deployed qualification is selected explicitly.

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

**Version**: 4.2.0 | **Ratified**: 2026-03-09 | **Last Amended**: 2026-11-25
