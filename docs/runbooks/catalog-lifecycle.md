# Catalog Lifecycle Operations

Products and CategoryTaxonomy resources are Git-backed desired state.
Foreground deletion is completed asynchronously by controllers; a resource
remaining visible with a deletion timestamp is not, by itself, an incident.
Do not remove `gitstore.dev/foreground-deletion` finalizers by hand.

## Product deletion

Git push and GraphQL mutations use the same admission pipeline and emit Product
Resource Watch events. GraphQL updates and deletion use stored repository and
source-path provenance; do not supply an alternative path.

A ProductVariant with a blocking Product owner reference prevents termination.
After deletion starts, the Product controller makes a fresh indexed blocker
check before completing deletion. Resolve or remove blocking ProductVariants
through their normal desired-state path and let reconciliation retry.

Completion conflicts are normal at-least-once work: consume the newer watch
event and retry, rather than forcing deletion.

## CategoryTaxonomy deletion

`deleteCategory` removes the manifest from Git in one commit and sets the
foreground finalizer and deletion timestamp. It returns `TERMINATION_STARTED`;
repetition returns `ALREADY_TERMINATING` without another commit.

Children with `blockOwnerDeletion=true` reject deletion. Products do not block
it: the controller removes their managed owner reference and writes
`CategoryResolved=False` with reason `CategoryDeleted`, without changing the
Git-authored `spec.categoryRef`. Once children drain and Product pages finish,
the controller calls `completeCategoryDeletion`, requiring
`categoryTaxonomy.purge`.

If termination stops progressing:

1. Query child categories and reparent or remove any blocking child.
2. Verify the controller is running and receiving watches. Replica replacement
   retains optimistic resource versions and idempotent Product decoupling.
3. Inspect controller errors and the Scylla `owner_reference_dependents`
   projection. [Audit and repair](scylla-projection-repair.md) stale projections
   before retrying completion; never remove the finalizer manually.
4. For slow Product draining, retain bounded page sizes and allow continuation
   reconciles rather than enlarging request timeouts.

Pre-receive checks category removal against the complete old and proposed
trees. A parent-only removal rejects; atomically deleting or reparenting its
children is accepted. Modified files also undergo removal/identity checks,
including multi-document files. API timeout, transport failure, or malformed
resource trees reject safely; schema validation remains active.

## Rollout and rollback

For Product durable watches:

1. Provision or validate the required Product CDC schema before enabling
   durable-watch readers.
2. Deploy APIs supporting Product journal events, then controllers supporting
   Product reconciliation and `completeProductDeletion`.
3. Keep incompatible mixed-version ingress denied until every API supports
   these contracts.
4. Verify a durable Product bookmark and controller Product checkpoint before
   resuming cursor-based traffic.

`WATCH_EXPIRED` triggers automatic relisting and a fresh checkpoint.
For `WATCH_UNAVAILABLE` or `MATERIALIZER_NOT_READY`, retain the cursor, back
off, and restore materializer health. Do not use event-bus cursors as a
multi-replica fallback.

Before rolling back Product readers, stop consumers and let them drain, then
disable the reader/materializer capability. Retain CDC and journal state while
issued cursors may still be replayed.

Category deletion enforcement requires coordinated API/controller versions and
valid owner references; incompatible old/new deployments are unsupported.
Resetting incompatible **disposable development fixtures** is not a production
upgrade procedure. Do not automatically delete or reseed persistent data,
including retained capacity datasets, during rollout or rollback.

See [upgrade compatibility](upgrades.md) for policy, API, and schema changes.
