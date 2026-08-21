# Contract: CategoryTaxonomy Deletion Semantics

**Feature**: [../spec.md](../spec.md) | **Data model**: [../data-model.md](../data-model.md)

## Additive metadata

`OwnerReference` gains `blockOwnerDeletion: Boolean!`; legacy absent data reads as `false`, while new writers always send it. GraphQL and controller list/watch contracts carry this additive field.

## GraphQL `deleteCategory`

Keep the existing `<verb>Category` GraphQL surface: implement the current
`deleteCategory` mutation for the CategoryTaxonomy-backed resource rather than
adding a parallel `deleteCategoryTaxonomy` operation.

| Condition | Result |
|---|---|
| Unauthorized or absent category | Existing authorization/not-found error |
| Blocking child found | Precondition error identifying child categories; no lifecycle mutation |
| Already terminating | Existing lifecycle payload; no duplicate deletion |
| No blocking child | Write finalizer/timestamp and `Terminating=True`; controller later completes |

Products are excluded from synchronous rejection.

## Git admission

The admission response carries an operation error such as `FailedPrecondition: child categories present`; the Rust hook rejects the push with that stable non-sensitive reason. Eligible Git deletion performs the same mark-delete transition as GraphQL rather than a hard delete.

## Datastore

The datastore exposes scope-bounded operations:

- blocking-dependent existence lookup;
- paged non-blocking Product dependent lookup;
- managed owner-reference/projection upsert and removal;
- expected-resource-version category mark-delete and completion.

Completion conflict/not-found is not a success until a fresh dependent check is performed.

## Controller

Every terminating reconcile rechecks children, processes at most one configured Product page, enqueues continuation when required, and completes only with no children and the observed resource version. Logs/metrics include owner/scope, page/result, lookup latency, conflicts, and retry class without catalog body content.

## Compatibility rollout

1. Deploy additive GraphQL/proto/types and projection schema.
2. Backfill currently resolved parent/category relationships.
3. Enable writers, then enforcement and controller drain.
4. Roll back behavior without deleting additive data; missing legacy projection entries never become a false blocking signal.
