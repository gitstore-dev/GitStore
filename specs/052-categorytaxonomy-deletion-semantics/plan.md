# Implementation Plan: CategoryTaxonomy Deletion Semantics

**Branch**: `052-categorytaxonomy-deletion-semantics` | **Date**: 2026-08-20 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/052-categorytaxonomy-deletion-semantics/spec.md`

## Summary

Safely delete a CategoryTaxonomy by blocking on child-category owner references while asynchronously decoupling Products. The API records Kubernetes-style `ownerReferences`, exposes a GraphQL deletion workflow, and uses a bounded reverse-owner index. The existing CategoryTaxonomy reconciler drives `Terminating`, repeats dependent checks, decouples Products, and completes with optimistic concurrency. Git admission receives an explicit precondition response so a blocked delete rejects its push.

## Technical Context

**Language/Version**: Go 1.26 (API/controller); Rust 1.x (Git service)
**Primary Dependencies**: gqlgen, gocqlx/gocql, go-memdb, zap, existing controller list/watch/cache/status packages; tonic/tokio
**Storage**: Existing catalog records plus durable reverse-owner-reference projection in ScyllaDB and equivalent indexed memdb data; existing CategoryTaxonomy status JSON
**Testing**: Go unit/datastore-contract/API/controller integration tests and Rust admission-hook tests
**Target Platform**: Linux containers and local Darwin development
**Project Type**: Independently deployable Go GraphQL API/controller and Rust Git service
**Performance Goals**: One indexed dependent check; bounded keyset Product pages; no catalog scans at 5,000,000 Products
**Constraints**: Preserve Git-authored `spec.categoryRef`; resource-version guarded completion; old readers tolerate absent additive metadata during rollout
**Scale/Scope**: Category trees and 5,000,000 Products under sustained Git pushes; deletion fan-out is paged, rate-limited, retryable, and resumable
**Replica/Scaling Model**: API operations are idempotent on durable state. Any controller replica may reconcile the same terminating category; duplicate decoupling is idempotent and stale completion is rejected. Deploy new API writers before controller enforcement; old controllers ignore additive fields.
**Authentication/Authorization**: Existing GraphQL category authorization remains at the API boundary; authenticated gRPC HMAC remains the Git-service boundary. All dependent lookups and mutations are namespace/repository scoped.
**Load/Backpressure Model**: Admission performs a limit-one lookup. Controller uses bounded keyset pages, existing worker/retry bounds, and continuation enqueueing. Capacity validation covers concurrent pushes/deletions, two controller replicas, restart during decoupling, and high-cardinality Products.

## Constitution Check

*Pre-design and post-design gate: PASS.*

- **Test-First**: Contract, admission, datastore, controller-restart, race, and load tests precede implementation.
- **API/Contract-First**: GraphQL, admission rejection, datastore, controller, and error semantics are specified in [contracts/deletion-semantics.md](./contracts/deletion-semantics.md).
- **Core-Service Boundary**: API owns durable lifecycle/admission, controller owns drain/decouple, Git service maps admission failure to push rejection.
- **Replica Safety**: Durable idempotency, rechecks, and resource-version guarded completion are required and tested with two reconcilers.
- **Multi-User Security**: Existing authorization/authentication and namespace/repository isolation are preserved.
- **Production Capacity / Bounded Work**: Reverse index, limit-one check, keyset pages, bounded workers/retries, and sustained-load validation avoid full scans.
- **Observability**: Structured logs and metrics cover rejection, terminating age, lookup latency, page progress, completion conflicts, and retries.
- **Incremental Delivery**: Additive types/projections deploy and backfill before enforcement; rollback disables behavior without removing additive data.
- **Simplicity**: Reuses the Namespace finalizer shape in the existing CategoryTaxonomy reconciler; no generic garbage collector.

## Project Structure

### Documentation

```text
specs/052-categorytaxonomy-deletion-semantics/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/deletion-semantics.md
└── tasks.md                 # Created by /speckit.tasks
```

### Source Code

```text
gitstore-api/
├── internal/catalog/                  # OwnerReference and conditions
├── internal/cataloggrpc/              # Git admission/deletion semantics
├── internal/datastore/{memdb,scylla}/ # Reverse-owner projections/queries
├── internal/graph/{model,resolver}/   # GraphQL lifecycle mutation
└── shared/schemas/                    # Additive GraphQL types

gitstore-controller-manager/
├── internal/categorytaxonomy/         # Termination and Product decoupling
├── internal/graphqlclient/            # Lifecycle/status operations
├── internal/listwatch/                # Lifecycle fields in list/watch
└── tests/integration/                 # Replica/restart tests

gitstore-git-service/
└── src/git/hooks/                     # Admission rejection → push rejection
```

**Structure Decision**: Extend existing API, controller-manager, and Git admission boundaries. The existing CategoryTaxonomy reconciler owns this resource-specific lifecycle.

## Complexity Tracking

No constitution violations or waivers are required.
