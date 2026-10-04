# Runbook: Controller Watch API and Status-Write Diagnostics

## Symptom

A controller's `watchCategories`/`watchResources` subscription disconnects and reconnects (a normal occurrence), or its `updateCategoryStatus`/`updateResourceStatus` writes are being rejected. This runbook helps distinguish a transient, self-healing disconnect from a cursor that has actually expired (requiring a re-list), and helps interpret a sustained status-write-conflict rate.

Every watched kind (Namespace, Repository, CategoryTaxonomy, Product and File) is
served from the same durable, replica-safe watch journal, so one set of signals
covers all of them. A cursor issued by one API replica can be resumed on any
other replica.

## Diagnostic Steps: Watch Disconnects

1. Check how many subscribers are connected and whether streams are being
   terminated:

   ```promql
   gitstore_resource_watch_subscribers
   sum by (reason) (rate(gitstore_resource_watch_expired_total[5m]))
   ```

   A reconnect that resumes from its cursor without a matching
   `expired_total` increment is a normal transient disconnect and needs no
   operator action.

2. If streams are expiring, the `reason` label tells you why:

   - `RETENTION_EXPIRED` or `REPLAY_LIMIT`: the client was disconnected longer
     than the retained journal window (`watch.namespace.journal_retention_seconds`)
     or would need to replay more than `watch.namespace.max_replay_events`.
   - `INVALID_CURSOR`, `INCOMPATIBLE_CURSOR` or `EPOCH_MISMATCH`: the client presented a cursor the
     journal never issued, for example one checkpointed before an upgrade.
   - `SUBSCRIBER_OVERFLOW`: the client did not drain its stream fast enough.
   - `JOURNAL_DISCONTINUITY`: see "Recovery by wire code" below; page the
     datastore owner.

3. Check for slow subscribers:

   ```promql
   rate(gitstore_resource_watch_overflow_total[5m])
   ```

   A sustained non-zero rate means a subscriber's buffer
   (`watch.namespace.subscriber_buffer`) filled up and its stream was ended with
   `WATCH_EXPIRED/SUBSCRIBER_OVERFLOW`. Overflow never drops events silently.

## Recovery Actions: Watch Disconnects

- **Transient reconnect (no `expired_total` increment)**: no action needed.
- **Expired cursor**: the controller's `listwatch.Runner` discards the cursor,
  re-lists from a fresh journal bookmark and resumes automatically. Repeated
  `RETENTION_EXPIRED`/`REPLAY_LIMIT` expiries for a controller mean it is
  disconnected for longer than the retained window; fix the outage or raise
  `journal_retention_seconds`/`max_replay_events` within their documented bounds.
- **Overflow**: the affected controller is not draining its watch fast enough.
  Check its queue-depth/worker-saturation signals (see
  [controller-lag.md](./controller-lag.md)) before reconnecting.

## Diagnostic Steps: Status-Write Conflicts

1. Check the conflict rate for the affected kind:

   ```promql
   rate(gitstore_status_write_conflicts_total{kind="<Kind>"}[5m])
   ```

   A `StatusConflict` response is expected occasionally (any concurrent write to the same resource can trigger one) — the reconciler pattern already handles this by retrying with fresh cache state. A sustained non-zero rate, rather than an occasional blip, is the signal worth investigating.

2. Check `gitstore-api` logs for the specific resources involved:

   ```text
   "status write conflict" kind=<Kind> namespace=<ns> name=<name>
   ```

   If the same `namespace`/`name` pair appears repeatedly in a short window, two writers are racing on that specific resource. If many different resources of the same kind are conflicting, the reconciler for that kind may be operating on a stale cache snapshot (e.g. a watch resume gap — cross-reference with the watch-disconnect signals above).

## Controller API throttling and recovery

### Expired-cursor recovery and readiness

Initial checkpoint restoration and expired-cursor re-listing close per-kind
dispatch admission. Recovery drains in-flight reconciliation (including its
completion callbacks), keeps pending replay work, and atomically replaces the
cache before dispatch resumes. Every runner waits
for a bookmark on the resumed watch, not merely a successful asynchronous
WebSocket open. Cancellation does not reopen admission or discard the old
checkpoint cursor while listing is incomplete.

Product enumeration streams 1,000-row pages: five million rows require 5,000
Product requests rather than 50,000. Successful enumeration pages report a
high-water count; revisiting the same pages after a failed list does not reset
the no-progress watchdog. The existing stall threshold still applies to a
recovery that stops making progress. Disk merge and retirement batches also
advance recovery progress; the page counter is not exclusively an HTTP-request
counter.

`GET /health` distinguishes liveness from readiness: progressing recovery can
return HTTP 200 with `ready:false`, `kinds.<Kind>.recovering:true` and recovery
page/row/timestamp details. Consumers that require a reconciled controller
must check `ready`, not just HTTP status. Capacity setup waits at most ten
minutes for recovery before creating the authoring Namespace/Repository/File
fixtures, not after the first Git clone; unhealthy or unauthenticated
controllers still fail immediately. Load and final acceptance require readiness.
No retry is counted as successful reconciliation.

Correlate these per-kind metrics by scrape instance and
`gitstore_controller_process_instance_info` across replacements:

```promql
gitstore_controller_recovery_in_progress
gitstore_controller_recovery_pages
time() - gitstore_controller_recovery_last_progress_timestamp_seconds
gitstore_controller_stalled_workers
rate(gitstore_controller_reconcile_total[1m])
rate(gitstore_controller_conflict_requeues_total{kind="Product"}[1m])
```

The conflict counter currently instruments Product status and deletion conflicts.
Controller snapshots retain all per-kind health details before assertions.
For historical metrics, enable the capacity scraper with both API and controller
targets as described in `tests/capacity/README.md`; unscripted `/metrics` reads
cannot recover counters after a process has exited.

This coordination is per process. The disk-backed rollout below introduces a
new checkpoint format; old controllers still use the memory-backed path.
The updated secret-capacity observer requires the new health shape on both
replicas; do not use mixed-version results to claim bounded-memory recovery.

The controller shares one outbound request budget across all reconciliation
kinds, list/status requests and WebSocket upgrades. Defaults are
`controller.api_client.requests_per_second = 40` and `burst = 10`, leaving
headroom under the API's unchanged 50 requests/second, burst-100 per-IP limit
for token exchange. Budget waits happen after potentially slow credential
renewal, preventing a burst when exchange waiters wake up. Cached service-account
tokens are revalidated after the wait so expired or replaced credentials are
not sent. Waits obey caller cancellation and do not accumulate a separate
unbounded request queue.
An HTTP 429 applies a one-second cooldown to this shared client. Mutations are
not automatically replayed by the HTTP client.

Token renewal remains independent and singleflight. A failed early renewal
preserves the cached token **only until its actual expiry**, including during
exchange backoff. The failure remains observable through credential exchange
metrics and the credential source's last error. Expired tokens are never
returned. Token-exchange and WebSocket-upgrade HTTP 429 responses retain the
same retryable throttle classification as HTTP queries and mutations.

The manager defers throttled work without quarantine or a successful-reconcile
acknowledgement, including throttles encountered during an existing retry.
Throttle waits occupy bounded worker slots and are canceled at shutdown;
checkpoint replay keys remain pending until real success. Non-throttle
failures still use the existing retry/quarantine policy. A delayed requeue
returned during retry is also preserved instead of being mistaken for success.
Initial/recovery list retries continue with capped exponential delay until
success or cancellation, without the backoff library's default 15-minute
elapsed-time cutoff.

Request budgets are **per controller process**, not a fleet-wide reservation.
If multiple controllers or other clients share one egress IP to an API replica,
size their aggregate budgets below that replica's per-IP limit with renewal
headroom. Do not bypass authentication or raise API limits merely to hide 429s.
During rolling upgrades, new binaries accept the old configuration and use
these defaults. Add explicit `controller.api_client` settings only after all
controllers that will read that shared config support the new keys; older
binaries reject unknown settings. Mixed-version fleets can still experience
throttling from unpaced old controllers.

Operational `GET /health`, `GET /ready` and `GET /metrics` requests use
independent per-IP, per-route buckets with the same configured rate and burst.
They remain rate-limited, but application traffic cannot consume their quota.
GraphQL HTTP requests and WebSocket upgrades continue sharing the application
bucket. This changes neither authentication nor authorization.

## Bounded disk-backed controllers

The production Namespace, Repository, CategoryTaxonomy and Product controllers
use the pure-Go LevelDB store for projections, relation indexes and pending
reconciliation obligations. Listing applies backpressure between bounded
pages, never collecting the catalog into a slice. The in-memory cache object
only holds recovery/admission state; reconciliation uses error-returning disk
lookups. A storage failure is not treated as a missing resource.
Cross-kind readers yield during long generation operations rather than holding
another controller's dispatch slot behind a multi-million-row merge.

Each kind uses a locked `<Kind>.disk-v2` directory on the existing checkpoint
volume. Separate replicas must retain separate directories; this is local
recovery state, not a shared coordination database. Replacement of a replica
must be non-overlapping on its volume: stop its old process before starting
another writer. The other replica keeps its own independent store.
The engine uses a 4 MiB write buffer, an 8 MiB block cache and a 32-file cache.
Application pages are limited to 256 entries and 4 MiB, and projections to 1 MiB.
These are component bounds, not a claim that a whole controller fits in their sum.

Snapshots are staged without replacing the active generation. Publication
atomically switches the generation and watch cursor after preserving unfinished
and deleted-resource work. Related work is generation-scoped too, so an
unpublished category membership cannot be dispatched. Synchronous transactions
couple watch changes with their cursor and work obligations; acknowledgements
must match the captured work token. A stale completion cannot erase newer work.
Interrupted staging is discarded before resuming the last published cursor.
Expired cursors trigger a streamed replacement. Retired generations are removed
in bounded batches before recovered work is released; LevelDB compaction may
reclaim their physical table space later. Budget storage for simultaneous
generations and compaction, not just one steady-state catalog. Cleanup advances
an exclusive key cursor instead of repeatedly scanning already-deleted keys;
interrupted cleanup resumes safely from the remaining persisted rows.

The manager reserves at most its configured worker count. Ready-work indexes
prioritize live changes over snapshot replay; absolute retry deadlines and
quarantine records are stored on disk, not in a timer or map per key. Periodic
resync does not bypass existing deferred or quarantined work or restart an
already-running sweep. Relation fan-out
has a durable, paginated scan cursor, and restarts that cursor when its source
generation changes. Cross-kind notifications are acknowledged only after the
destination work is durably recorded.
Queue depth includes deferred obligations, but a future deadline alone does
not make an idle controller stalled; stall detection uses runnable work. This
backlog is a disk-backed count, not the size of an in-memory queue. Existing
checkpoint last-write/failure metrics now include durable queue and staging
transactions; use the recovery state to distinguish progress from publication.

Product/category membership and child counts are maintained transactionally
with projections, then materialized into API status by reconcilers. They do not
require live API aggregates or a full Product listing for each category.
Category ancestry walks retain at most 128 names, matching the existing signed
8-bit depth representation; deeper hierarchies fail explicitly rather than
overflowing depth or consuming unbounded memory. Cycle participants retain their
previous depth/path. The Repository storage-verification cache is capped at
4,096 entries; eviction only causes an earlier storage recheck.

Corruption, incompatible schemas and I/O failures return errors rather than an
empty catalog. Watch persistence failures close recovery admission until
durable progress resumes. Quarantine listing is paginated; see
[controller-poisoned-item.md](./controller-poisoned-item.md).
Decoded status normalizes an absent or JSON-null `resolved` payload to nil.
Otherwise a disk round trip turns nil into non-nil `"null"` bytes, causing
already-converged Products to rewrite status and repeatedly enqueue themselves.
Normalization also applies to existing v2 projections; no volume wipe or
checkpoint-format migration is required.

Legacy JSON checkpoints and experimental `disk-v1` directories are neither
loaded nor removed. The first upgrade performs a bounded cold list into v2;
later process replacements resume v2. Retain the per-replica volume. An old
binary cannot read v2 and still has the original memory behavior; rolling back
is not a supported way to recover a five-million-row controller. This changes
no API datastore schema, authentication mechanism or Docker memory allocation.

Storage sizing: a five-million-Product projection set, including one full
snapshot replacement, has been exercised under a 512 MiB container memory limit
without an OOM. Peak Go heap stayed below 60 MiB. On-disk allocation peaked near
2 GiB during replacement, because the old and new generations coexist until
retirement and LevelDB reclaims retired table space only on later compaction.
Budget checkpoint storage for two simultaneous generations plus compaction
headroom, not for one steady-state catalog.

Separate contracts cover process-kill/reopen durability, incomplete-snapshot
isolation, concurrent updates, stale acknowledgements, rejected oversized pages,
membership reassignment and kind/replica isolation. Integration contracts cover
streamed restart/expiry recovery, bounded dispatch, newer in-flight work,
durable schedules, fan-out across generations and paginated poison reads.
These are storage and dispatch bounds, not production capacity acceptance; use
`make capacity` against a real deployment for throughput and rolling-recovery
evidence.

Typed and generic watches of every kind require the durable journal. Development
and tests wire the memdb journal; a deployment without a configured journal
returns `WATCH_UNAVAILABLE` instead of an unreplayable stream.

## Recovery Actions: Status-Write Conflicts

- **Occasional conflicts, low rate**: no action needed — this is the optimistic-concurrency mechanism working as intended.
- **Sustained conflicts on the same resource**: identify the writers and their observed resource versions. Replica races can produce expected optimistic-concurrency conflicts; investigate stale projections or failed recovery admission rather than disabling a healthy replica solely because conflicts occurred.
- **Sustained conflicts across many resources of one kind, correlated with watch-expiry or event-drop signals for that kind**: the reconciler is working from stale cache data. Fix the underlying watch-consumption lag first (see above); the conflict rate should subside once the cache catches up.

## Verification

- `sum by (reason) (rate(gitstore_resource_watch_expired_total[5m]))` returns to `0` (or the controller correctly re-lists whenever it is non-zero).
- `rate(gitstore_resource_watch_overflow_total[5m])` returns to `0`.
- `rate(gitstore_status_write_conflicts_total{kind}[5m])` returns to its prior baseline (occasional, not sustained).

## Durable watch journal: schema, rollout and recovery

The journal uses Scylla CDC (full preimage/postimage, 14-day TTL) on each
watched kind's authoritative table, plus the shared `resource_watch_events`
and `resource_watch_clock` tables. All of it is created by the baseline schema
migrations; there is no per-kind migration to apply.

**Upgrading from a release before the per-resource schema baseline requires a
fresh Scylla keyspace.** The migration history was consolidated and tables were
renamed, so an existing keyspace is refused at startup. Rolling back across that
release is not supported. Controllers re-list from a fresh bootstrap bookmark;
checkpoint cursors from the older release are rejected with `WATCH_EXPIRED`.

Roll out in this order:

1. Deploy every API replica. Exactly one healthy replica should report
   materializer leader `1`. Wait for a durable BOOKMARK and persisted CDC
   progress below 60 seconds; a fresh BOOKMARK alone does not certify CDC health.
2. Grant the controller identity `categoryTaxonomy.watch` alongside the existing
   `namespace.watch`, `repository.watch` and `product.watch` permissions.
   CategoryTaxonomy watches are authorized like every other kind.
3. Deploy the controllers. They wait for a journal bookmark before marking a
   kind synced, so they must not run against an API that predates this release.
4. Run a cross-replica probe: commit a change through API A and resume a watch
   on API B from an earlier cursor.

To take the journal out of service, deny watch ingress and disable
`watch.namespace.readers_enabled` first, then disable the materializer once
readers are drained. Do not drop CDC or journal tables while any issued cursor
could still be presented.

Journal signals have bounded labels only (`path` and `reason`; never a
resource name, UID, cursor, holder ID, or replica ID):

- `gitstore_resource_watch_materializer_leader` — alert if the fleet sum is
  zero for 30 seconds or above one for two lease TTLs.
- `gitstore_resource_watch_cdc_lag_seconds` and
  `gitstore_resource_watch_bookmark_age_seconds` — warn above 30 seconds and
  page above the 60-second readiness bound. Both report `+Inf` until their
  first durable observation; bookmark age advances only from an actual
  `BOOKMARK`, not ordinary journal activity.
- `gitstore_resource_watch_journal_oldest_sequence` and
  `gitstore_resource_watch_journal_high_water_sequence` — alert if high water
  stops advancing during acknowledged mutations or the retained span shrinks
  unexpectedly.
- `gitstore_resource_watch_subscribers{path="typed|generic"}` — capacity
  gauge; compare with the planned 1,000-subscriber envelope.
- `gitstore_resource_watch_expired_total{reason}` and
  `gitstore_resource_watch_overflow_total` — alert on any
  `JOURNAL_DISCONTINUITY`; warn on sustained overflow or expiry above 0.1% of
  subscription attempts.
- `gitstore_resource_watch_append_errors_total` — page on any sustained
  non-zero rate because acknowledged mutations may be awaiting CDC recovery.
- `gitstore_resource_watch_duplicates_total` — counts a new journal cursor
  delivered with a deduplication key already observed by this replica. Track
  its rate during recovery and rolling replacement; duplicates are safe but
  must remain visible rather than being mistaken for missing transitions.
- replay and delivery histograms — alert if 10,000-event replay p95 exceeds 5
  seconds, delivery p95 exceeds 1 second, or delivery p99 exceeds 3 seconds.

During replacement, the old leader may finish or lose its lease;
partition-local conditional writes stop a stale holder from
publishing/progressing. A replacement should acquire the
lease, resume durable CDC progress, write a BOOKMARK, and restore readiness in
30 seconds. Duplicates after append-before-progress recovery are safe, measured
by the duplicate counter and capacity client, and must be deduplicated by
cursor; missing sequences are not safe and fail closed.

Recovery by wire code:

- `WATCH_UNAVAILABLE/MATERIALIZER_NOT_READY`: retain the cursor, back off, and
  retry another ready replica. Check leader, CDC lag, bookmark age, and append
  errors.
- `WATCH_EXPIRED`: discard the cursor and repeat the documented
  bootstrap/list/drain algorithm. For `SUBSCRIBER_OVERFLOW`, also repair the
  slow consumer before reconnecting. For `JOURNAL_DISCONTINUITY`, page the
  datastore owner and preserve affected journal/CDC rows for diagnosis. A
  materializer that observes an actual CDC record behind the published
  frontier stops automatic leadership retries; repair the ordering state
  before restarting it. Empty CDC windows from newly discovered streams may
  advance only that stream's progress and do not constitute a discontinuity.
