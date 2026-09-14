# Repository Watch Contract

`watchRepositories` is the controller-facing Repository lifecycle stream. A
caller may provide `namespace` to narrow the stream, or omit it to receive the
global stream used by the Repository controller.

```graphql
subscription ($resourceVersion: String) {
  watchRepositories(resourceVersion: $resourceVersion) {
    type
    namespace
    name
    resourceVersion
    repository { id metadata { uid resourceVersion } }
  }
}
```

The cursor is opaque. It is not `metadata.resourceVersion`; clients must save
and replay the opaque watch value without parsing it. `WATCH_EXPIRED` means the
client must re-list and establish a new cursor. `WATCH_UNAVAILABLE` means the
watch materializer is not ready and the client should retry with backoff.

For a race-free initial controller view, the controller obtains a bookmark,
lists every Namespace and the repositories in each Namespace, then drains the
global stream after that bookmark. This prevents a namespace-scoped list from
missing a Repository written while the initial snapshot is in progress.

Repository events are authorization-gated before any cursor is parsed or a
subscription is registered. `ADDED` and `MODIFIED` carry a hydrated
Repository; `DELETED` and `BOOKMARK` do not carry a Repository object.

The production backing is the bounded, durable **generic resource journal**.
Namespace and Repository share its one cursor space; every data event carries
its canonical `kind` and namespace scope, while bookmarks remain shared. The
Repository CDC adapter consumes only the authoritative `repositories_by_uid`
projection, classifies its committed pre/postimages, and source-qualifies its
progress checkpoints. Replay, retention, lease fencing, append-before-progress,
and idle bookmarks therefore survive API/controller restarts and replica
replacement. A process-local event bus is not part of this contract.
