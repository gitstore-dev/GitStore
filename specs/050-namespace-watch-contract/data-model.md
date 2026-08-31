# Data Model: Replica-Safe Namespace Watch

## Existing Namespace (unchanged from spec 047)

The authoritative `datastore.Namespace` and its GraphQL `Namespace` envelope
remain unchanged. Feature 050 reads the committed values below but does not
change their validation or ownership:

- identity: UID and globally unique name;
- authored state: API version, kind, labels, annotations, spec, body;
- lifecycle: deletion timestamp and finalizers;
- status: observed generation, last applied revision, conditions;
- versioning: generation and per-resource resourceVersion;
- provenance: Git revision, path, SHA, and ref;
- audit actors/timestamps;
- repository fence epoch/pending counters.

The GraphQL event intentionally does not add deletion timestamp to
`NamespaceMetadata`; clients continue to derive `Terminating` from the
foreground-deletion finalizer/status condition contract established before
feature 050.

## NamespaceWatchEvent

A public, typed change envelope produced from the durable journal.

| Field | Type | Rules |
|---|---|---|
| type | WatchEventType | ADDED, MODIFIED, DELETED, or BOOKMARK |
| name | String | Namespace name for data events; empty string for BOOKMARK because the shared GraphQL field remains non-null |
| resourceVersion | String | Opaque journal cursor, not the Namespace row's optimistic-concurrency version |
| namespace | Namespace nullable | Full committed postimage for ADDED/MODIFIED; null for DELETED/BOOKMARK |

Validation:

- `namespace.metadata.name == name` for ADDED/MODIFIED.
- The envelope cursor is the journal cursor; the embedded
  `namespace.metadata.resourceVersion` is the resource's version.
- DELETED carries identity in `name` and null payload.
- BOOKMARK carries an advanced journal cursor, empty `name`, and null payload.
- Callers never compare or construct cursor components.

## CDCChangeIdentity

Unique identity of one Scylla CDC logical change.

| Field | Type | Rules |
|---|---|---|
| generation | timestamp | CDC topology generation |
| streamID | blob | CDC stream partition identifier |
| changeTime | timeuuid | Base-write timestamp |
| batchSequence | int | Orders records belonging to one write |
| table | text | Fixed to authoritative Namespace table |

The materializer groups delta/preimage/postimage rows belonging to the same
logical base-table mutation before event classification.

## NamespaceJournalClock

Singleton sequence allocator and journal epoch.

| Field | Type | Rules |
|---|---|---|
| kind | text partition key | Fixed to `Namespace` |
| epoch | uuid | Changes only on an explicit incompatible journal reset |
| nextSequence | bigint | Allocated by LWT; monotonically increases within epoch |
| updatedAt | timestamp | Operational signal |
| writerFence | bigint | Current materializer fencing token |

A failed allocation may leave a numeric gap. Cursors are opaque; contiguity of
integers is not a public guarantee. A cursor is emitted only after its event row
is durable.

## NamespaceWatchJournalEvent

Durable replay row.

| Field | Type | Rules |
|---|---|---|
| epoch | uuid | Cursor epoch |
| bucket | bigint | `sequence / 4096`; partition component |
| sequence | bigint | Global order and clustering key |
| event_type | text | ADDED/MODIFIED/DELETED/BOOKMARK |
| name | text | Namespace name; empty for BOOKMARK |
| payload | text nullable | Versioned JSON Namespace postimage for ADDED/MODIFIED |
| labels | map<text, text> nullable | Native CQL postimage or last-known labels used for filtering; never exposed as the resource payload |
| previous_labels | map<text, text> nullable | Native CQL MODIFIED preimage labels used to project selector entry/exit as ADDED/DELETED |
| deduplication_key | text | Stable CDC identity or bookmark identity |
| fencing_token | bigint | Lease that staged the row; stale orphan rows are replaceable before visibility advances |
| event_timestamp | timestamp | Source change time or bookmark append time |
| table TTL | seven days | Retention bound |

Primary key: `((epoch, bucket), sequence)`.

State transitions:

1. normalized CDC create -> ADDED;
2. normalized CDC update/status/finalizer write -> MODIFIED;
3. normalized final row deletion -> DELETED;
4. idle materializer interval -> BOOKMARK.

Rejected, failed, conflicted, or no-op spec-047 operations create no base-table
change and therefore no journal event.

Migration 006 adds an internal `watch_committed boolean` marker to
`namespaces_by_uid`. Creation sets it only after the listing projection is
durable. The initial false-marker CDC insert advances progress without a public
event; its false-to-true promotion is normalized to ADDED. Deletion preimages
with a false marker are recognized as rollback cleanup and are not published.

## NamespaceWatchClockAndProgress

One Scylla partition contains the singleton clock and static lease state plus
durable rows per CDC stream/generation. Co-location lets journal visibility and
progress writes condition atomically on the active lease and remains compatible
with `scylla-cdc-go.ProgressManager`.

| Field | Type | Rules |
|---|---|---|
| journal | text partition key | Fixed to `namespace` |
| stream_id | text clustering key | `__clock__` or encoded CDC stream/generation/frontier identity |
| epoch | uuid static | Cursor epoch |
| high_water | bigint static | Highest published sequence |
| oldest | bigint static | Monotonic retained lower bound |
| bucket_size | bigint static | Immutable partition layout for this journal epoch; replicas with a different configured value fail closed |
| update_timestamp | timestamp static | Last journal append time |
| cdc_progress_timestamp | timestamp static | Timestamp of the fenced global published frontier (the common active-stream watermark) used for lag/health; never an individual stream checkpoint or local write time |
| lease_holder | text static | Active materializer replica |
| fencing_token | bigint static | Stale leader cannot advance journal or progress |
| lease_expiration_timestamp | timestamp static | Lease validity bound |
| position | blob | Opaque CDC progress position |
| progress_update_timestamp | timestamp | Per-stream progress save time |

Restart resumes after the saved opaque `position`. Append-before-save permits
duplicates but prevents loss.

The sequencer maintains a watermark for every active CDC stream. It publishes
only through their common frontier and rejects a late stream whose first record
precedes the already-published frontier. That frontier is stored as a dedicated
progress row before per-stream progress and restored on restart. ADDED candidates additionally require
the corresponding list projection to be visible before append and progress
save.

## NamespaceMaterializerLease

Load-bounding leader lease.

The lease is the static state in `namespace_watch_clock`, not a separate table:

| Field | Type | Rules |
|---|---|---|
| lease_holder | text | API replica identity |
| fencing_token | bigint | Increments on acquisition |
| lease_expiration_timestamp | timestamp | 30-second lease; renewed every 10 seconds |

A lease holder may publish only while its fencing token matches the static
clock token and its expiry remains in the future in the same LWT. Brief overlap
may stage an orphan row, but it cannot make that row visible or advance shared
CDC progress; the next holder can replace it safely.

## NamespaceWatchCursor

Opaque external representation:

```text
nwv1:<epoch-uuid>:<base36-sequence>
```

Parsing rules:

- unknown prefix/version -> `WATCH_EXPIRED/INCOMPATIBLE_CURSOR`;
- epoch mismatch -> `WATCH_EXPIRED/EPOCH_MISMATCH`;
- sequence below oldest retained -> `WATCH_EXPIRED/RETENTION_EXPIRED`;
- sequence above high water -> `WATCH_EXPIRED/INVALID_CURSOR`;
- replay suffix above 100,000 -> `WATCH_EXPIRED/REPLAY_LIMIT`.

Before validating a new cursor, Scylla derives `oldest` from the first live TTL
row at or after the stored lower bound and advances the static clock value by
LWT. Empty expired sequence buckets are skipped monotonically with a 32-bucket
per-call budget; an incomplete scan is checkpointed and fails closed until a
later call reaches the exact retained bound.

The bootstrap sentinel remains private client/server coordination and is never
persisted as a resumable cursor.

## WatchRegistration

Ephemeral per GraphQL subscription.

| Field | Type | Rules |
|---|---|---|
| principal | authenticated principal | Authorized before journal access |
| startCursor | NamespaceWatchCursor | Captured high water or validated resume |
| nextSequence | bigint | Next journal sequence to deliver |
| buffer | channel capacity 64 | Overflow expires the watch |
| selector | label selector nullable | Evaluated against ADDED/MODIFIED postimage |
| state | REPLAYING/LIVE/EXPIRED/CLOSED | Monotonic lifecycle |
| expiryReason | bounded enum nullable | Never contains resource data |

State transitions:

```text
REGISTERING -> REPLAYING -> LIVE -> CLOSED
       |            |         |
       +----------> EXPIRED <-+
```

Any continuity failure transitions to EXPIRED, emits the typed GraphQL terminal
error, and requires a new bootstrap/list.

## Relationships

```text
namespaces_by_uid
      |
      | synchronous Scylla CDC
      v
namespaces_by_uid_scylla_cdc_log
      |
      | fenced materializer + durable progress
      v
NamespaceWatchJournalEvent <- NamespaceJournalClock
      |
      | bounded replay/tail from every API replica
      v
WatchRegistration
      |
      +--> watchNamespaces -> NamespaceWatchEvent
      +--> watchResources(kind: "Namespace") -> WatchEvent
```

## Retention and Recovery Invariants

- CDC TTL (14 days) is greater than journal TTL (7 days).
- The configurable readiness lag ceiling remains strictly below CDC retention,
  so a reader cannot be admitted after its recovery source has expired.
- A materializer lag above 60 seconds degrades watch readiness; lag approaching
  seven days requires operator intervention before journal expiry.
- Progress never advances past an event that was not durably journaled.
- Journal event order is global within one epoch.
- Epoch changes are explicit and force every existing cursor to expire.
- A replica restart does not change epoch or high water.
- The generic and typed Namespace paths read the identical journal rows.
