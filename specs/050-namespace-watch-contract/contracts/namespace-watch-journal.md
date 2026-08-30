# Contract: Durable Namespace Watch Journal

## Scope boundary

The journal observes the committed effects of shipped spec 047. It does not
change Namespace validation, admission reasons, mutation responses, repository
fencing, finalizer ownership, status ownership, generation, or per-resource
resourceVersion.

## Production source

- Migration 006 enables CDC with full preimage/postimage and 14-day TTL on
  `namespaces_by_uid`.
- Scylla acknowledges a successful base write only with its CDC write at the
  same consistency level.
- The materializer reads CDC with a consistency level satisfying the base-write
  consistency intersection.
- Only the authoritative by-UID table is consumed; query projections do not
  generate duplicate logical events.

## Classification

| Committed effect | Journal event | Payload |
|---|---|---|
| First authoritative Namespace row | ADDED | Full postimage |
| Authored/status/finalizer/provenance update | MODIFIED | Full postimage |
| Deletion marker/finalizer attached | MODIFIED | Full postimage |
| Idempotent repeated delete with no row write | none | none |
| Permanent authoritative row removal | DELETED | name plus private last-known selector labels; GraphQL payload remains null |
| 30 seconds without a data event | BOOKMARK | none |
| Rejected/failed/conflicting mutation | none | none |

One CDC logical mutation produces at most one normalized data event. CDC
reprocessing may append the same logical event again with a later cursor.

## Cursor and replay

External cursor:

```text
nwv1:<epoch-uuid>:<base36-sequence>
```

- Cursor is opaque and kind-specific.
- Sequence orders events inside an epoch.
- The journal retains seven days, bucketed in groups of 4,096 events.
- Reads return at most 256 events per query.
- One subscription may replay at most 100,000 events.
- Resume starts strictly after the supplied cursor.
- Cursor epoch mismatch, expiry, invalid future sequence, missing retained
  bucket, replay overflow, or sustained subscriber backpressure timeout returns
  `WATCH_EXPIRED`.

GraphQL terminal error:

```json
{
  "message": "namespace watch continuity cannot be guaranteed; re-list",
  "extensions": {
    "code": "WATCH_EXPIRED",
    "reason": "RETENTION_EXPIRED"
  }
}
```

Allowed bounded reasons:

- `RETENTION_EXPIRED`
- `EPOCH_MISMATCH`
- `INCOMPATIBLE_CURSOR`
- `INVALID_CURSOR`
- `REPLAY_LIMIT`
- `SUBSCRIBER_OVERFLOW`
- `JOURNAL_DISCONTINUITY`

Infrastructure unavailable before registration:

```json
{
  "message": "namespace watch is temporarily unavailable",
  "extensions": {
    "code": "WATCH_UNAVAILABLE",
    "reason": "MATERIALIZER_NOT_READY"
  }
}
```

## Initial list-then-watch sequence

1. Authenticate and authorize `namespace.watch`.
2. Open `watchNamespaces(resourceVersion:
   "__namespace_watch_bootstrap__")`.
3. Server registers at journal high water and emits a BOOKMARK with cursor C.
4. Keep the subscription open and query every page of `namespaces`.
5. Build/replace the local snapshot from the list.
6. Apply queued events strictly after C, idempotently by Namespace identity.
7. Persist each last-applied event cursor.
8. On `WATCH_EXPIRED`, discard the cursor and repeat from step 2.

This ordering closes the list/watch gap: changes at or before C are in the
snapshot; changes after C are queued by the registered stream.

## Delivery and ordering

- At-least-once, never exactly-once.
- Global deterministic order inside the Namespace journal epoch.
- No order relative to another resource kind.
- Generic and typed Namespace subscriptions expose the same ordered rows.
- Duplicate ADDED/MODIFIED/DELETED may occur after materializer recovery.
- Consumers apply level-triggered/idempotent state updates.

## Backpressure

- Per-subscription channel capacity: 64.
- Journal fetch: 256.
- Poll starts at 100 ms and backs off to at most 2 seconds while idle.
- If the producer cannot enqueue without losing continuity, it expires the
  subscription with `SUBSCRIBER_OVERFLOW`.
- No dropped event is represented as normal close.

## Authorization

Before cursor parsing or journal access:

```text
action: namespace.watch
resource.kind: Namespace
resource.name: empty (cluster scope)
resource.attrs: {}
```

Denied response uses `FORBIDDEN` and reveals no cursor validity, oldest/high
water, replay count, Namespace identity, selector match, or materializer state.

## Materializer progress

For each CDC stream:

1. Normalize the logical CDC change.
2. Allocate a journal sequence with fenced LWT.
3. Append the journal event with TTL.
4. Save CDC progress using an LWT conditioned on the same partition-local
   holder, fencing token, and non-expired lease used to publish the event.

Progress is never saved before step 3. Crash between 3 and 4 replays and may
duplicate; crash before 3 retries without loss.

## Rollout

1. Deny generic and typed Namespace subscriptions fleet-wide.
2. Apply migration 006.
3. Deploy schema/readers/materializer code everywhere while explicitly
   overriding the alpha-default-on activation gates to disabled.
4. Enable fenced materializer and wait for persisted CDC query progress; a
   bookmark/high water alone does not certify reader health.
5. Enable journal readers everywhere.
6. Prove cross-replica bootstrap/resume/expiry.
7. Remove deny and enable clients to select `watchNamespaces`.

Rollback restores the deny first and retains migration 006 in the supported
binary. Spec-047 Namespace mutations stay available and unchanged.

## Operational signals

Required bounded-cardinality signals:

- materializer lease holder/fencing token and renewal failures;
- CDC generation/progress/lag;
- journal append attempts/failures/duplicates;
- oldest/high-water sequence and journal rows;
- subscribers by typed/generic path;
- replay events and latency;
- expiry by reason;
- overflow count;
- bookmark age;
- end-to-end CDC-to-delivery latency;
- watch readiness and unavailable reason.
