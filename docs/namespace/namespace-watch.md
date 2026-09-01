# Namespace watch contract

`watchNamespaces` is the canonical, cluster-scoped subscription for building a
live Namespace cache. It is backed by the shared Scylla CDC journal, so a cursor
issued by one API replica can resume through another and survives process
replacement for the configured retention window. Callers require the
cluster-scoped `namespace.watch` authorization action.

This watch observes the Namespace lifecycle already shipped by spec 047. It
does not reopen or change that lifecycle contract.

## Race-free bootstrap

Start the subscription before listing. The bootstrap sentinel makes the first
event a durable BOOKMARK whose `resourceVersion` is the captured journal high
water:

```graphql
subscription BootstrapNamespaces {
  watchNamespaces(resourceVersion: "__namespace_watch_bootstrap__") {
    type
    name
    resourceVersion
    namespace {
      apiVersion
      kind
      metadata { name uid resourceVersion generation finalizers }
      spec { title tier }
      status {
        observedGeneration
        lastAppliedRevision
        conditions { type status reason message }
      }
      body
    }
  }
}
```

Keep that socket open, save the BOOKMARK cursor, and page the complete snapshot:

```graphql
query ListNamespaces($first: Int, $after: String) {
  namespaces(first: $first, after: $after) {
    edges {
      cursor
      node {
        apiVersion
        kind
        metadata { name uid resourceVersion generation finalizers }
        spec { title tier }
        status { observedGeneration lastAppliedRevision conditions { type status reason message } }
        body
      }
    }
    pageInfo { hasNextPage endCursor }
  }
}
```

Apply only buffered watch events strictly after the BOOKMARK to the completed
list. There is no list cursor: the pre-list BOOKMARK closes the list/watch race.
Persist the last applied event cursor, not merely the last received cursor.

## Resume and recovery

Resume strictly after a persisted cursor:

```graphql
subscription ResumeNamespaces($cursor: String!) {
  watchNamespaces(resourceVersion: $cursor) {
    type
    name
    resourceVersion
    namespace {
      metadata { name uid resourceVersion generation finalizers }
      spec { title tier }
      status { observedGeneration conditions { type status reason message } }
    }
  }
}
```

Delivery is at-least-once. Deduplicate by opaque `resourceVersion`; do not
parse or sort cursors in clients. A successful resume excludes the cursor event
itself and returns only later Namespace journal events in order.

Terminal GraphQL errors contain stable `extensions.code` and `extensions.reason`:

| Code | Reasons | Consumer action |
|---|---|---|
| `WATCH_EXPIRED` | `RETENTION_EXPIRED`, `EPOCH_MISMATCH`, `INCOMPATIBLE_CURSOR`, `INVALID_CURSOR`, `REPLAY_LIMIT`, `SUBSCRIBER_OVERFLOW`, `JOURNAL_DISCONTINUITY` | Discard the cursor and repeat bootstrap/list/drain. Never keep trusting the old cache. |
| `WATCH_UNAVAILABLE` | `MATERIALIZER_NOT_READY` | Retry with bounded exponential backoff. Readiness is denied until shared journal progress is fresh; do not re-list repeatedly while unavailable. |

A transport `complete` without one of these errors is normal only after client
cancellation or clean server shutdown. Backpressure overflow and any detected
journal gap terminate explicitly as `WATCH_EXPIRED`.

## Event payloads

| Type | `name` | `resourceVersion` | `namespace` |
|---|---|---|---|
| `ADDED` | resource name | journal cursor | full committed postimage |
| `MODIFIED` | resource name | journal cursor | full committed postimage |
| `DELETED` | last-known name | journal cursor | `null` |
| `BOOKMARK` | empty | advanced journal cursor | `null` |

Status, generation, resourceVersion, and finalizers in data-event postimages are
the authoritative spec-047 state. There is no `deletionTimestamp` field.
Entering or leaving Terminating is a `MODIFIED` event and is derived from the
foreground-deletion finalizer/status state. Final removal alone is `DELETED`.

Selectors use the existing `LabelSelectorInput`. ADDED/MODIFIED events match
their postimage labels and DELETED matches a private last-known label snapshot
stored with the journal row. BOOKMARK always bypasses filtering so every
subscriber can establish and advance its cursor. DELETED and BOOKMARK still
expose a null Namespace payload. Namespace itself is cluster-scoped, so the
subscription has no namespace argument.

The pre-existing `watchResources(kind: "Namespace")` path remains compatible
and uses the same cursor stream and error contract, but typed
`watchNamespaces` is recommended.

## Replica-local fan-out

Each API process tails the shared durable journal once and fans live events out
through a bounded in-memory ring. This keeps steady Scylla polling proportional
to API replica count rather than WebSocket count. The ring is not a source of
truth: initial resume, process replacement, and any subscriber that falls
behind the ring read the missing bounded range from Scylla before rejoining
live delivery. Replay-to-live registration captures a high-water under the
same tailer lock, so events cannot fall into a handoff gap.

A slow socket retains its independent output buffer and backpressure deadline;
it cannot stall the shared tailer or healthy peers. If continuity can no longer
be proven within the configured replay/buffer bounds, only that subscription
terminates with the documented `WATCH_EXPIRED` reason.

## Non-goals

This contract does not change Namespace mutation/admission semantics from spec
047, introduce cascade deletion or auto-drain, add a `deletionTimestamp`
equivalent, promise ordering relative to other resource kinds, or provide
exactly-once delivery.
