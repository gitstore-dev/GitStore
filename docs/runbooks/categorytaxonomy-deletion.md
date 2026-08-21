# CategoryTaxonomy foreground deletion

## Normal operation

`deleteCategory` marks an eligible category with
`gitstore.dev/foreground-deletion` and a deletion timestamp. Child categories
with `blockOwnerDeletion=true` reject the request. Products never block it:
the controller removes their managed owner reference and writes
`CategoryResolved=False` with reason `CategoryDeleted`; it does not change the
Git-authored `spec.categoryRef`.

Monitor terminating categories, controller retry/conflict logs, and the
bounded Product-page progress. A category should disappear after children have
drained and Product pages complete.

## Remediation

1. Query the category and its child categories. Reparent or remove any child
   that still has a blocking parent owner reference.
2. Verify the controller is running and receiving category watches. Restarting
   a replica is safe: the lifecycle is stored with an optimistic resource
   version and Product decoupling is idempotent.
3. If a category remains terminating, inspect controller errors and the
   Scylla `owner_reference_dependents` projection. Backfill or repair stale
   projections before retrying completion; do not remove the finalizer by hand.
4. If Product drain is slow, retain the configured bounded page size and allow
   continuation reconciles rather than increasing a request timeout.

Run the owner-reference backfill before enabling enforcement on records
written before owner references existed. It is idempotent; use `--dry-run`
first and retain the returned `resumeAfter` value to resume after an
interruption:

```bash
(cd gitstore-api && go run ./cmd/backfill-owner-references --dry-run)
(cd gitstore-api && go run ./cmd/backfill-owner-references --resume-after '<cursor>')
```

## Rollout and rollback

Deploy the additive schema and GraphQL/list-watch fields first, then backfill
existing resolved owner references, enable writers, and finally enable deletion
enforcement and controller draining. Rollback may disable enforcement and
draining without deleting owner-reference metadata or the Scylla projection.

CategoryTaxonomy manifest removal is checked synchronously in pre-receive
against the full old and proposed ref trees. A parent-only deletion rejects;
deleting or reparenting its children atomically is accepted. Products remain
non-blocking. API timeout, transport failure, or malformed proposed resource
trees reject the push safely. Creates and updates remain asynchronous
post-receive admission and do not incur this pre-receive API call.
