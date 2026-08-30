# Research: Replica-Safe Namespace Watch Infrastructure

## R1: Preserve spec 047 as the mutation authority

**Decision**: Treat shipped spec 047 as immutable behavior. Feature 050 observes
successful writes to the authoritative `namespaces_by_uid` table and does not
add policy checks, mutation outcomes, status writers, or repository-fence
branches.

**Rationale**: Spec 047 already defines conditional admission, authored-state
persistence, two-step deletion, repository creation/transfer fencing, generation
and resourceVersion changes, and status ownership. Duplicating any of those
decisions in a watch publisher creates a second source of truth.

**Alternatives considered**:

- Re-emit events directly from each resolver/catalog gRPC success path: rejected
  because it remains process-local and every new mutation path can forget to
  publish.
- Wrap spec-047 service methods in an outbox write: rejected because Scylla
  cannot atomically combine the existing conditional writes across their
  partitions with an application-managed journal partition.

## R2: Use Scylla CDC as the atomic production change source

**Decision**: Migration 006 adds an internal creation-commit marker and enables
Scylla CDC with full preimages, postimages, and a 14-day CDC TTL on
`namespaces_by_uid`. Production uses
`github.com/scylladb/scylla-cdc-go v1.2.1` to consume those changes.
The memdb backend implements the same journal interface transactionally/in
memory for development and backend-neutral contracts.

**Rationale**: Scylla documents that CDC log writes are synchronous with base
table writes, use the same consistency level, and acknowledge the base write
only after both are persisted. That supplies the atomic mutation/change
boundary without altering spec 047. Full postimages provide the accepted
Namespace state for added/modified events; full preimages preserve identity for
row deletion. The official Go reader handles CDC streams and topology
generations and supports durable per-stream progress.

**Alternatives considered**:

- Kafka/Redpanda plus a Scylla source connector: technically viable but adds a
  broker and connector when Scylla already stores a replicated CDC log.
- Direct dual-write from the API to Scylla/Kafka: rejected because a crash
  between the two commits can lose or invent an event.
- Scylla polling/diffing: rejected because it can miss intermediate state and
  deletion/recreation and requires repeated full scans.
- Keep the local ring with sticky sessions or peer broadcast: rejected because
  rolling replacement or a missed peer message still creates a silent gap.

References:

- [ScyllaDB CDC overview](https://docs.scylladb.com/manual/stable/features/cdc/cdc-intro.html)
- [ScyllaDB CDC consistency](https://github.com/scylladb/scylladb/blob/master/docs/features/cdc/cdc-consistency.rst)
- [ScyllaDB CDC log table](https://github.com/scylladb/scylladb/blob/master/docs/features/cdc/cdc-log-table.rst)
- [scylla-cdc-go v1.2.1](https://github.com/scylladb/scylla-cdc-go/releases/tag/v1.2.1)

## R3: Materialize CDC into a bounded global Namespace journal

**Decision**: An embedded materializer consumes every CDC stream, normalizes
one logical base-table mutation into one Namespace event, and appends it to
`namespace_watch_events`. A partition-local `namespace_watch_clock` stores the
sequence clock, static lease/fencing state, and per-stream progress so every
publication/progress LWT is atomically fenced. It allocates a monotonically
increasing sequence. The opaque cursor is
`nwv1:<epoch-uuid>:<sequence>`; event rows are bucketed by
`sequence / 4096` and retained for seven days.

The materializer centralizes CDC records through a sequencer. Records are
released only after every active stream watermark reaches them, then ordered by
`cdc$time`, stream ID, and arrival sequence for deterministic tie-breaking. A
newly discovered stream behind the published frontier fails closed and restarts
from durable progress instead of appending an older record out of order. The
published frontier is saved before per-stream progress, so the same fence is
restored after process replacement. The
journal append is the public per-kind ordering linearization point; causally
concurrent writes have no earlier externally observable total order.

Before publishing ADDED, the materializer verifies that the matching
`namespaces_by_bucket` row is visible. If the authoritative row still exists
without its list projection, progress is not advanced and CDC is retried; if
the authoritative row has already been rolled back, the staged addition is
acknowledged without inventing a successful event. This preserves the
bootstrap list/watch boundary without treating a query projection as an event
source.

Deletion uses the inverse ordering: the authoritative LWT commits before list
and name projections are removed. A conflicting or failed LWT cannot
transiently hide an otherwise unchanged Namespace from a bootstrap list. For a
successful delete, the CDC materializer waits until the exact
`namespaces_by_bucket` row is absent before publishing DELETED. A bootstrap
that still saw the projection therefore starts before that DELETED cursor and
will drain the event; a bootstrap at or after the event cannot list the row.

**Rationale**: CDC has multiple streams, so its native clustering order is only
per stream. A global sequence gives every API replica one stable replay order.
Bucketed event partitions prevent an unbounded hot partition while the singleton
clock is acceptable for the declared Namespace mutation envelope. Bounds checks
derive the first live TTL row from the current sequence bucket and advance the
stored lower bound monotonically; empty expired buckets are skipped once, so
cursor expiry and telemetry reflect actual retained rows. Each validation scans
at most 32 buckets and durably checkpoints its progress with one LWT. Until a
retained row or `highWater + 1` is reached, registration fails unavailable
instead of serving stale bounds. A TTL race during an established stream maps
to `RETENTION_EXPIRED` rather than an idle loop or generic discontinuity.
The schema retains the repository's established physical conventions:
`*_timestamp` names for CQL timestamps and `labels map<text, text>` for label
sets. JSON text is reserved for the versioned resource payload.
When replicas race to checkpoint the same retained lower bound, a CAS loser
rereads the clock and accepts a winner that has already reached its monotonic
target. An epoch change or a reread that remains below the computed target
continues to fail unavailable.

**Alternatives considered**:

- Expose raw CDC timeuuid/stream cursors: rejected because replay would require
  a vector cursor and would not provide the contract's single per-kind order.
- One unbucketed journal partition: rejected because retention volume can create
  an oversized partition.
- Per-Namespace cursors: rejected because clients need one cursor for the
  cluster-scoped Namespace list.

## R4: Make CDC processing crash-safe and at-least-once

**Decision**: Persist CDC progress only after the corresponding journal event is
durably appended. A crash after append but before progress save causes a
duplicate event with a later journal cursor on restart; this is permitted and
measured. A crash before append leaves progress unchanged and the change is
retried. The materializer lease is an optimization and load bound, not a
correctness assumption: the clock partition's static fencing token participates
in the same LWT as journal visibility and per-stream progress, preventing stale
lease holders from publishing or progressing. Orphan rows remain invisible and
replaceable; duplicate appends from append-before-progress recovery remain safe.

**Rationale**: The official CDC reader explicitly makes consumers responsible
for saving progress and resumes after the last saved record. Append-before-save
therefore prevents missing acknowledged mutations without requiring
cross-table transactions.

**Alternatives considered**:

- Save progress before append: rejected because a crash loses the event.
- Exactly-once deduplication: rejected because the public contract is
  at-least-once and cross-partition dedup/sequence transactions add complexity
  without improving controller correctness.

## R5: Tail the durable journal from every API replica

**Decision**: Replace direct Namespace subscriptions to the local event bus with
a `WatchJournal` interface. Each subscription captures the journal high-water
cursor atomically, optionally replays after a supplied cursor, then tails
bounded batches from the shared journal. All API replicas read the same cursor
space. The existing local event bus remains unchanged for other resource kinds
until their own migration specifications.

**Rationale**: This limits infrastructure expansion to Namespace as requested,
allows a cursor issued by replica A to resume through replica B, and avoids
reopening Category/Product/File contracts.

**Alternatives considered**:

- Replace every resource watch in feature 050: rejected as unrelated scope.
- One shared API-level consumer that fans out locally: rejected because a new
  replica still needs durable replay and local fan-out is not the source of
  truth.

## R6: Use bootstrap bookmark, then list, then drain

**Decision**: The sentinel bootstrap request registers the durable tail and
returns a `BOOKMARK` containing the captured high-water cursor before the
client performs the paginated `namespaces` query. The client keeps the
subscription open while listing and then applies queued events after that
cursor idempotently. Empty lists are valid.

**Rationale**: A mutation before the captured cursor is reflected by the list;
a mutation after it is queued in the stream. This closes the list/watch gap
without changing the existing Namespace connection shape.

**Alternatives considered**:

- Add a cursor to every Relay page: rejected because pagination pages cannot
  represent one atomic snapshot without datastore snapshot support.
- List first and subscribe from the maximum row resourceVersion: rejected
  because per-row resourceVersion is not a global event cursor and a mutation
  can occur in the gap.

## R7: Persist real idle bookmarks

**Decision**: The fenced materializer appends a durable `BOOKMARK` event every
30 seconds when no data event has advanced the journal. The bookmark has no
name or Namespace payload and advances the global sequence. Both typed and
generic Namespace subscriptions read the same record.

**Rationale**: A locally generated bookmark would not advance a shared cursor
or prove materializer health. A journaled bookmark refreshes retention position,
makes idle lag observable, and survives replica switching.

**Alternatives considered**:

- Repeat the current high-water cursor locally: rejected because FR-017 requires
  an advanced cursor.
- Let every API replica produce bookmarks: rejected because event rate would
  scale with replica count and obscure materializer ownership.

## R8: Fail closed on expiry, overflow, and infrastructure gaps

**Decision**: Cursor epoch mismatch, cursor older than the earliest retained
event, cursor ahead of high water, CDC/materializer lag beyond the configured
safe window, replay exceeding 100,000 events, and subscriber buffer overflow
all terminate with GraphQL error code `WATCH_EXPIRED` plus a bounded
`reason`. Journal/materializer unavailability before registration returns
`WATCH_UNAVAILABLE`. Neither is exposed to unauthorized callers.

**Rationale**: Every condition means the consumer cannot prove continuity.
Treating a closed channel as success would allow indefinitely stale state.

**Alternatives considered**:

- Silent reconnect from latest: rejected because it loses changes.
- Unbounded replay/buffers: rejected by the capacity and backpressure
  constitution.

## R9: Authorize both Namespace watch entry points

**Decision**: Extend `authorizeSubscription` so `watchNamespaces` and
`watchResources(kind: "Namespace")` both require cluster-scoped
`namespace.watch`. Authorization runs before cursor parsing, journal reads, or
subscription registration.

**Rationale**: Current middleware only protects File watches; Namespace generic
watch currently bypasses fine-grained AuthZ. The new typed field must not
preserve that leak.

**Alternatives considered**:

- Rely on authentication only: rejected because Namespace is an isolation and
  policy boundary.
- Filter individual events after replay: rejected because cursor/retention and
  Namespace identities would already be disclosed.

## R10: Roll out migration and schema server-first

**Decision**:

1. Apply migration 006, adding the internal commit marker, enabling CDC, and
   creating the journal-event table plus the partition-local
   clock/lease/progress table.
2. Deny both Namespace watch forms fleet-wide during mixed binaries.
3. Deploy all API replicas with schema support present and explicitly override
   the alpha-default-on durable watch gates to disabled.
4. Enable one fenced materializer, verify CDC progress and journal bookmarks,
   then enable journal readers on every replica.
5. Run cross-replica probes and remove the external deny.
6. Only then allow clients to select `watchNamespaces`.

Rollback restores the external deny first, disables reader/materializer, and
uses a supported artifact that still embeds migration 006. Spec-047 behavior
continues unchanged throughout.

**Rationale**: Old replicas cannot understand the typed field or shared cursor.
The deny prevents clients from observing mixed semantics.

**Alternatives considered**:

- Opportunistic mixed old/new watch service: rejected because cursors and
  delivery guarantees differ by replica.

## R11: Bound and observe the infrastructure

**Decision**:

- journal TTL: 7 days;
- CDC TTL: 14 days;
- journal bucket: 4,096 events;
- journal read batch: 256;
- subscriber buffer: 64;
- maximum single resume: 100,000 events;
- journal poll: 100 ms minimum with capped 2 s backoff;
- bookmark interval: 30 seconds;
- lease TTL: 30 seconds, renewed every 10 seconds;
- production validation: 10 sustained transitions/s, bursts of 100/s, 1,000
  subscriptions, two replicas, 60 minutes.

Expose counters/gauges/histograms for materializer leadership, CDC progress and
lag, append failures, journal high/oldest cursors, replay count/latency,
subscribers, expiries by bounded reason, overflows, bookmark lag, and end-to-end
delivery latency. Watch readiness fails when CDC lag exceeds 60 seconds or
journal continuity cannot be proven.

**Rationale**: These bounds satisfy the present feature envelope without tying
work to catalogue product count. Thresholds are explicit and testable.

**Alternatives considered**:

- Reuse the old ring capacity as the only bound: rejected because it is
  process-local and event-count-only.
