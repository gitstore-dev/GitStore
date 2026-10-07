# Repository durable-watch substrate

`watchRepositories` must not use the process-local event bus. It is a
controller input and therefore has to retain the same continuity guarantees as
the Namespace stream from spec 050: a controller can resume after an API
replica restart, receives one ordered stream across replicas, and fails closed
when retained history or the materializer is unavailable.

The correct substrate is one generic `ResourceWatchJournal`, not a journal per
kind. The shared journal owns opaque cursor encoding, retention, replay,
leases, bookmarks, backpressure, and delivery. Resource adapters own only an
authoritative source, pre/postimage conversion, label extraction, and a
source-specific progress checkpoint. Repository events use `Kind: Repository`
and preserve `namespace` and `name`, so the typed and generic Repository
projections filter one shared cursor stream without losing continuity.

## Required implementation slice

1. Extend the shared generic-journal migration to enable full pre/post-image
   CDC on `repositories_by_uid`; reuse the existing event, clock, lease, and
   progress tables. Do not add per-kind tables or cursors.
2. Add a Repository CDC reader that classifies authoritative
   `repositories_by_uid` transitions as `Kind: "Repository"`, emits committed
   postimages, and retains current/previous labels. Its source progress key is
   Repository-specific; journal sequencing and fencing remain shared.
3. Add the memdb authoritative-write adapter. It appends only after successful
   `CreateRepository`, `UpdateRepository`, and hard deletion. Status,
   finalizer, and deletion-marker changes are normal MODIFIED events;
   rejected, stale, and no-op writes append nothing.
4. Wire the reader into the generic materializer leadership runtime and its
   readiness checks. Repository readers must remain disabled until the shared
   migration and Repository source frontier are ready; they must never fall
   back to the event bus.
5. The typed and generic GraphQL projections consume the shared subscriber.
   The controller's global `watchRepositories(namespace: null, ...)` stream
   resumes with the same shared cursor after its list-and-drain bootstrap.

## Proof obligations

- A two-replica test proves a Repository event written through replica A can
  be resumed through replica B without loss or duplication beyond the
  documented at-least-once boundary.
- Namespace and Repository interleaving preserves one cursor order; projections
  emit only their selected kind while bookmarks retain the shared high-water.
- Cursor epoch mismatch, retention expiry, replay limit, overflow, and an
  unavailable/stale materializer terminate as typed watch errors.
- CDC classification tests cover create, mutable update, status update,
  deletion marker/finalizer update, hard delete, and rejected/conflicting/no-op
  writes.
- A rolling-upgrade test proves old replicas cannot expose a Repository watch
  before the generic migration and Repository source adapter are ready.

This is T028--T030's minimum production scope. The resolver projection is now
on the generic durable subscriber; the Repository source adapter and generic
runtime/readiness wiring remain required before those tasks are complete.
