# Research: Product Git-Backed Lifecycle

## R1 — Canonical Product authoring path

**Decision**: Git remains the only authoring source. A Product manifest pushed
to an authorized repository is admitted by the existing catalog pipeline.
GraphQL mutations do not take a repository input. Creation writes to the
Product namespace's `gitstore-system` repository; an update follows the
existing Product's admitted repository and source-path provenance, including a
non-system Git repository, and waits for the resulting admission outcome.

**Rationale**: Namespace and Repository already use this commit-and-wait
pattern. It supplies one validation, identity, provenance, audit, and rollback
model, while allowing a Product admitted in a non-system Git repository to be
updated through GraphQL without relocating its canonical file.

**Alternatives considered**:

- Direct GraphQL datastore writes — rejected because they create a second
  desired-state authority.
- A repository argument on GraphQL mutations — rejected by clarification; it
  diverges from Namespace and Repository mutation conventions.

**Evidence**: `docs/ADRs/0002-namespace-lifecycle.md`,
`gitstore-api/internal/graph/resolver/service.go`,
`gitstore-api/internal/graph/resolver/repository.resolvers.go`, and
`specs/058-repository-git-backed-lifecycle/contracts/repository-admission.md`.

## R1a — Resource deletion input convention

**Decision**: Product adopts the standard GraphQL delete envelope:
`input DeleteProductInput { id: ID }`. Resource identity, namespace scope, and
Git provenance are resolved server-side from that ID.

**Rationale**: Resource-specific deletion selectors have diverged. A common ID
shape gives clients one predictable lifecycle API and preserves server-side
authorization and canonical-source routing.

**Alternatives considered**:

- Namespace/name input — rejected because it perpetuates per-resource input
  divergence and exposes a second identity shape.


## R2 — Durable Product Resource Watch

**Decision**: Replace Product's process-local event bus history with the
shipped shared Resource Watch journal. Product gets a CDC adapter/source,
durable journal records, a typed `watchProducts` adapter, and generic
`watchResources(kind: "Product")` routing over the same cursor space.

**Rationale**: Namespace and Repository already prove the required
cross-replica cursor, bootstrap, replay, bookmark, backpressure, fencing, and
recovery behavior. A generic journal avoids a third bespoke watch design.

**Alternatives considered**:

- Retain the Product event bus — rejected: history is process-local, ordinary
  spec updates are not consistently published, and a reconnect on another API
  replica can lose transitions.
- Create a Product-only watcher — rejected: it duplicates the shipped generic
  durable watch substrate.

**Evidence**: `specs/050-namespace-watch-contract/plan.md`,
`gitstore-api/internal/watchjournal/`,
`gitstore-api/internal/graph/resolver/product.resolvers.go`,
`gitstore-api/internal/graph/resolver/repository_watch.go`, and
`gitstore-api/internal/graph/resolver/watch.go`.

## R3 — Foreground Product deletion and ProductVariant ownership

**Decision**: Product deletion is a foreground state machine. Admission checks
an indexed ProductVariant owner-reference projection before marking a Product
terminating. If clear, it writes the deletion timestamp and
`gitstore.dev/foreground-deletion` finalizer. The Product controller repeats
the indexed check at completion and only then removes the finalizer/finalizes.
ProductVariant has a system-owned Product owner reference with
`blockOwnerDeletion: true`; a new or newly resolved variant targeting a
terminating Product is rejected. No variant is cascaded.

**Rationale**: This exactly expresses the clarified safety rule and preserves
the established Namespace/Repository finalizer pattern under races and replica
handoff.

**Alternatives considered**:

- Immediate hard delete — rejected because it can orphan variants.
- Cascade-delete variants — rejected by the feature's pure-block rule.
- Admit new blockers during termination — rejected by clarification because it
  prevents bounded deletion convergence.

**Evidence**: `docs/ADRs/0002-namespace-lifecycle.md`,
`gitstore-controller-manager/internal/namespace/reconciler.go`,
`gitstore-controller-manager/internal/repository/reconciler.go`,
`gitstore-api/internal/datastore/entities.go`, and
`specs/052-categorytaxonomy-deletion-semantics/contracts/deletion-semantics.md`.

## R4 — Product GraphQL security and envelope completion

**Decision**: Add resource-envelope Product mutations and close authorization
at every private Product surface: lookup, connection, node, relationship/count
read, authoring mutation, deletion, typed/generic watch, and controller status
or completion. Authorization occurs before Product existence, journal cursor,
or replay data is disclosed. Authors cannot write system metadata/status.

**Rationale**: Product currently has read and event-bus gaps, whereas the
Repository resolver/middleware establishes the desired resource-aware policy
shape. No public Product API is added; public snapshots remain a separate
future boundary.

**Alternatives considered**:

- Preserve permissive Product reads — rejected because it leaks tenant state.
- Use a source `Published` condition for public access — rejected because
  publication documents require immutable, target-specific snapshot authority.

**Evidence**: `shared/schemas/product.graphqls`,
`gitstore-api/internal/graph/resolver/product.resolvers.go`,
`gitstore-api/internal/middleware/security/graphql.go`,
`gitstore-api/internal/graph/resolver/repository_authorization_test.go`, and
`docs/products/publication-lifecycle.md`.

## R5 — Controller composition and category fan-out

**Decision**: Register a dedicated Product lifecycle reconciler using the
existing list/watch/cache/queue/status interfaces. Keep the existing
Product-to-CategoryTaxonomy enqueue handler as an independent consumer of the
same durable Product stream. Product lifecycle reconciliation owns only Product
status/finalizer work; it does not own category status or publication/workflow
resources.

**Rationale**: The current Product watcher only feeds category count work; it
is not a Product reconciler. Separating responsibilities preserves the shipped
CategoryTaxonomy contract and avoids conflicting status writers.

**Alternatives considered**:

- Expand the CategoryTaxonomy controller to complete Product deletion —
  rejected because it violates ownership and couples unrelated convergence.
- Implement publication/release logic in the Product controller — rejected as
  outside this feature's private-current-catalog boundary.

**Evidence**: `gitstore-controller-manager/internal/categorytaxonomy/products.go`,
`gitstore-controller-manager/internal/listwatch/graphql_listwatcher.go`,
`gitstore-controller-manager/internal/repository/reconciler.go`,
`docs/implementation/037-custom-commerce-workflows.md`, and
`docs/products/publication-lifecycle.md`.

## R6 — Retirement compatibility

**Decision**: Author Product `spec.lifecycle.state` as `ACTIVE` or `RETIRED`.
Retirement is not deletion and is Product-only: a retired Product makes its
child variants ineligible for a newly prepared release. A candidate that
explicitly includes either is rejected. This feature supplies the Product
contract only; it does not implement release preparation, publications,
snapshots, tags, workflow executions, or public reads.

**Rationale**: The existing proposed commerce workflow and publication designs
need stable Product-facing eligibility semantics without allowing mutable source
rows to rewrite an active public offer.

**Alternatives considered**:

- Independently retire ProductVariants — deferred by clarification.
- Silently omit retired entries from a release — rejected because it changes
  an auditable release candidate without author intent.

**Evidence**: `docs/implementation/037-custom-commerce-workflows.md` and
`docs/products/publication-lifecycle.md`.

All planning uncertainties are resolved; no `NEEDS CLARIFICATION` remains.
