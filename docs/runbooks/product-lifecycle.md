# Product Lifecycle Operations

## Scope

Products are Git-backed desired state. Git push and GraphQL mutations commit
the manifest, use the same admission pipeline, and emit Product Resource Watch
events. GraphQL updates and deletion use the Product's stored repository and
source-path provenance; operators must not supply an alternative path.

## Rollout

1. Apply the Product CDC migration before enabling Product durable-watch
   readers on any API replica.
2. Deploy API replicas that understand Product journal events, then deploy
   controller-manager replicas with the Product reconciler.
3. Keep mixed-version ingress denied until every API replica supports Product
   durable watches and `completeProductDeletion`.
4. Verify a durable Product bookmark and controller Product checkpoint before
   allowing user traffic to resume from cursors.

## Foreground deletion

A ProductVariant with a blocking Product owner reference prevents termination.
After a delete starts, the Product remains visible with a deletion timestamp
until the Product controller performs a fresh indexed blocker check and calls
completion. Never remove the `gitstore.dev/foreground-deletion` finalizer by
hand: resolve or remove the blocking ProductVariant through its normal desired
state path, then let reconciliation retry.

## Recovery and rollback

- `WATCH_EXPIRED`: retain no old cursor; the controller list/watch runner
  relists and persists a fresh checkpoint automatically.
- `WATCH_UNAVAILABLE` or `MATERIALIZER_NOT_READY`: retain the cursor, back
  off, and restore materializer health. Do not fall back to an event-bus cursor
  in a multi-replica deployment.
- To roll back readers, stop Product durable-watch consumers first, wait for
  them to drain, then disable the reader/materializer capability. Do not drop
  Product CDC or journal state while issued cursors may still be replayed.
- A controller completion conflict is normal at-least-once work: it must read
  the newer watch event and retry, not force deletion.
