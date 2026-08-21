# Research: CategoryTaxonomy Deletion Semantics

**Feature**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md)

## Decision 1: Reuse Namespace foreground-deletion lifecycle shape

**Decision**: Add CategoryTaxonomy-specific mark-delete and guarded-completion
operations modeled on the existing Namespace foreground-deletion finalizer workflow,
but expose them through the established `<verb>Category` GraphQL mutations. The
category controller completes deletion after rechecking blocking dependents.

**Rationale**: CategoryTaxonomy already has controller-managed status and a reconciler, so it can expose `Terminating`, recover after a restart, and safely handle races.

**Alternatives considered**: Hard-delete after a single check is rejected because a child can race it. A generic GC controller is rejected because no other in-scope resource needs the category-specific Product decoupling behavior.

## Decision 2: Durable reverse owner-reference projection

**Decision**: Add `blockOwnerDeletion` to owner references and maintain a reverse projection keyed by owner UID, dependent UID/kind, scope, and block flag. MemDB receives an indexed table; Scylla receives a scope-partitioned denormalized table. APIs offer a limit-one blocking lookup and bounded keyset pages for non-blocking Products.

**Rationale**: Owner references are raw JSON today and have no dependent query. Scanning/parsing catalog rows violates the five-million-product capacity constraint.

**Alternatives considered**: Matching authored names is rejected by FR-003 and reparent/rename races. Indexing JSON blobs is not a portable bounded query in either backend.

## Decision 3: Return rejection through Git admission

**Decision**: CategoryTaxonomy deletion precondition failures become an API admission response that the Rust hook maps to a rejected Git push; failures are not merely logged.

**Rationale**: Current delete admission hard-deletes and errors do not reliably reach the push. GraphQL-only safety would violate User Story 1.

**Alternatives considered**: Asynchronous Git deletion is rejected because it silently permits the invalid authoring operation. Reimplementing catalog checks in Rust is rejected because the API owns catalog state and authorization.

## Decision 4: Category controller decouples Products

**Decision**: Each terminating-category reconcile handles a bounded page of non-blocking Product dependents: remove its category owner reference and patch `CategoryResolved=False`, `CategoryDeleted`; do not change `spec.categoryRef`.

**Rationale**: Products must not block deletion, but they cannot silently retain resolved status after their category is gone. The work is idempotent and restart-resumable.

**Alternatives considered**: Clearing `spec.categoryRef` violates Git ownership. Blocking on Products violates the hybrid deletion policy.

## Decision 5: Additive staged rollout and backfill

**Decision**: Deploy additive types/projections first, backfill resolved relationships, enable owner-reference writes, then enforce deletion.

**Rationale**: Existing records lack owner references; immediate enforcement cannot see legacy children during a rolling upgrade.

**Alternatives considered**: Immediate enforcement has a correctness hole. Pausing writes for migration violates independently deployable delivery.
