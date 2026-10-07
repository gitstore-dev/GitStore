# Implementation Plan: Product Git-Backed Lifecycle, Durable Watch, and Reconciliation

**Branch**: `055-product-deletion-safety` | **Date**: 2026-09-16 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/055-product-lifecycle/spec.md`

## Summary

Make Product follow the shipped Namespace and Repository lifecycle pattern.
Git remains canonical: Product pushes use catalog admission; GraphQL create
writes to implicit `gitstore-system`, while GraphQL update uses the admitted
Product's repository/source-path provenance (including non-system repositories)
and awaits admission. Close private
GraphQL/authentication/authorization gaps; replace Product's event-bus watch
with durable Resource Watch; and add a Product controller for status and
foreground deletion. ProductVariant gains a blocking Product owner reference;
CategoryTaxonomy fan-out stays independent. Retirement defines compatibility
for proposed workflow/publication designs but implements no release, snapshot,
publication, workflow, tag, or public Product-serving API.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`, `gitstore-controller-manager`); Rust 1.x (`gitstore-git-service`)
**Primary Dependencies**: Existing gqlgen v0.17.90, gocqlx/gocql, go-memdb, Git writer/catalog gRPC, `internal/watchjournal`, Scylla CDC, Prometheus, zap, controller ListWatcher/Runner/cache/status interfaces; no new dependency
**Storage**: Existing Product/ProductVariant lifecycle fields and owner-reference projections; add Product CDC source/migration/progress and bounded Product projection over the existing Resource Watch journal in Scylla and memdb; no publication storage
**Testing**: Go unit/resolver/middleware/datastore contracts, Rust admission tests if hook changes are needed, controller/two-replica integration, capacity/recovery profile, `make test`, `make build`, `make pr-ready`
**Target Platform**: Linux servers; Darwin/Linux development hosts
**Project Type**: Multi-service Git admission, GraphQL API/subscription, datastore journal, and controller-manager feature
**Performance Goals**: visibility p95 ≤1 s/p99 ≤3 s; 10,000-event replay p95 ≤5 s; recovery ≤30 s; zero missing acknowledged transitions; five-million Products and 1,000 subscriptions
**Constraints**: Git-only desired-state authority; no repository mutation input; `DeleteProductInput { id: ID }` resource-delete convention; private current-catalog GraphQL only; authorization before existence/cursor disclosure; no variant cascade; generated gqlgen/gRPC code is regenerated, never hand-edited
**Scale/Scope**: Namespace-scoped Products and blocking variants under sustained Git/GraphQL load with bounded list/watch/replay/deletion work
**Replica/Scaling Model**: Resource-version guarded writes; shared durable journal/cursors across API replicas; fenced CDC materializer; idempotent Product controller list/watch/cache/queue across at least two replicas
**Authentication/Authorization**: Existing human/service/controller providers with resource-aware Product read/create/update/delete/watch/status/completion actions, scope isolation, and audit decisions
**Load/Backpressure Model**: 4,096-event buckets, 256-event reads, 100,000 replay cap, 64-event buffers, 30-second delivery/bookmark/lease limits, bounded workers/retries; indexed blocker lookup
**Capacity Profile**: `tests/capacity/profiles/product-lifecycle.js`; `make capacity TARGET=product PROFILE=lifecycle MODE=<diagnostic|alpha|production>` validates two APIs/controllers, Git/GraphQL load, watches, replay, races, and correctness
**Fault Profile**: `tests/chaos/profiles/product-lifecycle.json`; API/controller replacement, materializer interruption, and mid-deletion failure with recovery ≤30 s

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-checked after Phase 1 design.*

| Principle                                                | Evaluation                                                                                                              |
|----------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------|
| I. Test-First Development                                | PASS — contract, admission, resolver, middleware, journal, controller, race, and capacity tests precede implementation. |
| II. API-First Design                                     | PASS — lifecycle/watch and Git admission/deletion contracts are defined first.                                          |
| III. Clear Contracts & Versioning                        | PASS — GraphQL/watch changes are additive; retirement is only a compatibility contract.                                 |
| IV. Production Observability & Debuggability             | PASS — journal, lifecycle, blocker, queue, auth, and latency signals are required.                                      |
| V. User Story Driven Development                         | PASS — authoring, secure read/watch, deletion safety, convergence, and retirement are independently verifiable.         |
| VI. Independently Deployable Delivery                    | PASS — migrate/types, journal readers, GraphQL/auth, controller, and enforcement ship in safe slices.                   |
| VII. Simplicity with Proven Scale                        | JUSTIFIED — reuse shipped Resource Watch/finalizer patterns; no broker or service is introduced.                        |
| VIII. Horizontally Replicable Core Services              | PASS — shared journal/fencing and optimistic versions eliminate process-local correctness.                              |
| IX. Multi-User Authentication, Authorization & Isolation | PASS — all Product reads/writes/watches are authorized before disclosure.                                               |
| X. Production Capacity, Backpressure & Load Validation   | PASS — explicit bounds and two-replica capacity/chaos evidence are required.                                            |

**Gate result**: PASS. The eventbus alternative cannot meet replay or replica
continuity requirements, justifying a per-kind adapter over proven durable
infrastructure.

## Project Structure

### Documentation

```mermaid
%%{init: {"treeView": {"showIcons": true}} }%%
treeView-beta
    specs/
        055-product-lifecycle/
            plan.md ## implementation plan
            research.md ## Phase 0 decisions
            data-model.md ## lifecycle and ownership model
            quickstart.md ## verification guide
            contracts/
                product-admission.md
                product-lifecycle.md
                product-watch.graphqls
            tasks.md ## generated by speckit-tasks
```

### Source changes

```mermaid
%%{init: {"treeView": {"showIcons": true}} }%%
treeView-beta
    shared/
        schemas/
            product.graphqls ## lifecycle mutation, envelope, and watch contract
    gitstore-api/
        internal/
            cataloggrpc/
                server.go ## Product admission/deletion and Variant owner references
            datastore/
                datastore.go
                memdb/
                scylla/
                    migrations/ ## lifecycle/blocker and Product CDC migration
            graph/
                resolver/
                    product.resolvers.go
                    product_status.go
                    watch.go
            middleware/
                security/
                    graphql.go ## Product resource-aware policy gates
            watchjournal/ ## existing durable journal/materializer wiring
    gitstore-controller-manager/
        cmd/
            controller/
                main.go ## Product registration
        internal/
            product/ ## reconciler, API client, and tests
            categorytaxonomy/ ## retain Product-to-category enqueue handler
    tests/
        contract/
        integration/
        capacity/
        chaos/
    docs/
        products/
        runbooks/
    Makefile ## canonical capacity interface
```

**Structure Decision**: Extend the existing three-service pipeline and generic
Resource Watch in place. Product gets an adapter and dedicated reconciler—not a
new service, broker, watcher family, or public catalog runtime.

## Phase 0: Research Outcomes

Research is in [research.md](research.md). It resolves every uncertainty:

- GraphQL create commits to implicit `gitstore-system`; GraphQL update follows
  stored source repository/path provenance and waits for shared admission.
- Product replaces local event history with durable typed/generic Resource Watch.
- Indexed blocking ProductVariant checks gate deletion and finalization; no cascade.
- Variant resolution writes the blocking reference and rejects a terminating parent.
- Product owns lifecycle reconciliation; CategoryTaxonomy fan-out remains separate.
- Retirement is private Product intent, not publication implementation.

No `NEEDS CLARIFICATION` remains.

## Phase 1: Design and Contracts

### Admission and GraphQL

Add declarative create/update envelopes and delete input. No mutation input has
a repository or path field. After scope authorization, create commits the
canonical manifest in namespace `gitstore-system`; update resolves the existing
Product's persisted repository/source-path provenance and writes that original
manifest, including for non-system Git repositories. Each waits for common
admission, then returns the admitted Product. Preserve Markdown body when
unchanged; failed admission returns no partial resource. Inputs omit UID,
versions, status, owners, finalizers, deletion timestamps, provenance, and
actor fields. `DeleteProductInput` is the standard `id: ID` shape; server-side
resolution supplies the Product's scope and canonical Git provenance. Private
lookup/list/node/relationship/count access authorizes
before lookup; typed/generic watches authorize before cursor parsing; status
and completion are controller-only with expected-resource-version conflicts.

### Durable watch

Add Product as a Resource Watch source. CDC of the authoritative Product row
creates events for admitted create/spec update, status update, termination
marker/finalizer writes, and final removal. A fenced materializer appends
before progress; list visibility gates ADDED. Typed and generic routes adapt
one journal/cursor. Controller ListWatcher uses durable bookmark → full list →
drain; expiry/unavailability rebuilds cache. Ship migration/types first; deploy
materializer/readers disabled until all API replicas understand Product cursor
semantics; enable routes and migrate controller consumers. Rollback disables
the routes safely without deleting additive journal/projection data.

### Ownership and deletion

Generalize git-service SchemaValidation from the existing CategoryTaxonomy
proposed-tree deletion check to an `OperationDelete` pre-receive contract for
all supported resource kinds. Add indexed live-blocker existence, expected-version mark-termination, and
completion operations. Before Git delete/lifecycle marking, admission rejects
live blockers. Eligible deletion writes deletion timestamp and foreground
finalizer, emits MODIFIED, and returns terminating. Reconciliation refetches,
checks version/finalizer and blockers, requeues boundedly on a blocker/conflict,
then removes the finalizer and final-deletes exactly once. Repeated delete is
idempotent. Variant admission/deferred resolution canonicalizes Product owner
references with `blockOwnerDeletion: true` and rejects a terminating target.
Category relationships stay non-blocking and retain affected-only fan-out.

### Observability and capacity

Add Product-labeled journal high-water/oldest, materializer lease/lag, append,
replay/subscriber/backpressure/expiry/bookmark, and admission-to-watch metrics.
Add lifecycle attempt/result, blocker lookup latency/result, finalizer age,
controller queue/retry/conflict, and auth-decision signals without secret/body
content. Product materializer readiness fails closed. The Makefile profile
captures declared two-replica latency and correctness evidence.

### Post-design constitution result

PASS — contracts are explicit, state is durable and replica-safe, all work is
bounded, authorization is pre-disclosure, and Product does not absorb category
or publication ownership.

## Complexity Tracking

| Violation                                  | Why Needed                                                     | Simpler Alternative Rejected Because                                                                            |
|--------------------------------------------|----------------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------|
| Product CDC/journal adapter and reconciler | Lifecycle correctness must survive API/controller replacement. | The current eventbus is process-local, misses ordinary updates, and cannot satisfy cursor/finalizer guarantees. |
