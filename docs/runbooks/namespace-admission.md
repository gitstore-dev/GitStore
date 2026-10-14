# Namespace admission operations

## Scope

This runbook covers Namespace structural validation, stateful policy admission,
GraphQL create/update/delete behavior, and the server-first activation of
`DeleteNamespacePayload.outcome`. Authorization must complete before validation,
policy, lifecycle, or blocker details are returned.

Metadata-only label and annotation updates advance `resourceVersion` without
advancing `generation`. Filtered Namespace watches report selector entry as
`ADDED`, an in-selector update as `MODIFIED`, and selector exit as `DELETED`.
Typed deletion events carry the name and journal cursor, not a Namespace
payload. Resume with the event's opaque journal `resourceVersion`, not
`namespace.metadata.resourceVersion`; another API replica replays the retained
metadata and selector transitions from that cursor.

Repository watches follow the same selector and payload-free deletion contract
within their namespace. Rewriting a manifest's YAML comments or formatting
without changing its parsed metadata, specification or body still advances Git
provenance and `resourceVersion`, but not `generation`. Pre-receive authorization
requires the resource's update permission for that rewrite; post-receive
admission delivers it through the durable watch. Unchanged, unrelated manifests
are not rewritten simply because their repository's branch advances.

Periodic controller resync starts at the configured interval but distributes
reconciliation across that interval rather than enqueueing the entire retained
population at once. The disk-backed controller first counts local keys in
bounded pages, then persists its sweep cursor and next eligible page time.
This adds a bounded local counting pass, not API or Scylla queries. A process
replacement resumes the sweep without replaying accumulated downtime;
snapshot generation replacement restarts counting. Watch events and dependency
fanout remain immediate, while periodic work preserves existing retries and
quarantine. A ticker arriving before a sweep finishes retains one coalesced
next-sweep obligation, instead of silently skipping an entire interval.
This is eventual external-state rechecking, not a guarantee that
every resource is verified at each ticker boundary. Cold list/watch recovery
is unchanged. Include periodic sweep windows when measuring sustained load;
quiet windows between sweeps do not establish combined-load capacity.

Recent Repository storage verification is bound to the immutable UID, not just
its namespace/name. Recreating a name cannot inherit the previous incarnation's
verified state. The bounded observation cache is empty after process replacement;
failed verification is not cached.

Post-receive delta snapshots read at most four manifest blobs concurrently at
each immutable commit. Paths are deduplicated and retain deterministic
processing order; full-tree compatibility reads remain sequential. All active
reads are drained before returning, and an unavailable, denied or cancelled
read rejects the snapshot rather than admitting a partially loaded batch.

Scylla File admission can coalesce up to four queued callbacks in one Namespace
under a single exclusive durable parent fence.
After that reservation is confirmed, the cohort checks the consecutive queue
again before starting mutations. Arrivals during the reservation RPC can join
the same fence, up to the unchanged four-callback limit and only before the
first lifecycle barrier. A fully canceled or unconfirmed reservation selects
no additional callbacks.
Each callback contains at most four distinct File creates or updates, so a
cohort runs at most sixteen File mutations concurrently. Callbacks retain their
own Repository identity and immutable commit. Callbacks with overlapping File
names wait for earlier overlapping callbacks to finish, while disjoint callbacks
may execute concurrently. Overlaps preserve FIFO mutation order without another
durable reservation; an ambiguous predecessor blocks overlapping successors
for repair. Deletions, other resource kinds and parent lifecycle writes remain
FIFO reservation barriers. No artificial
collection delay is added when the queue is empty. Callbacks retain their own actor, immutable
commit, cancellation and resource CAS; one ordinary failure does not reject
unrelated callbacks. All active mutations drain before the shared fence is
released. Ambiguous mutation or fence-release failures retain the reservation
for repair and fail every affected callback, including otherwise successful
ones. This is bounded scheduling, not an atomic multi-resource transaction.

For admission latency diagnosis, scrape both APIs. The
`gitstore_catalog_grpc_server_handling_seconds` histogram covers complete
catalog callback execution, including its authorization and datastore work.
`grpc_client_handling_seconds` covers the API's existing Git RPC calls, such as
`CommitFile`, `GetFile` and `ListFiles`. The existing interceptor records stream
completion only on EOF or error; successful client-streaming `ReceivePack`
returns its single response without EOF and is not a complete duration sample.
Use the workload's Git HTTP timing for acknowledgement latency. Labels are fixed RPC service, method
and call type, not resource identities or credentials. Compare exact-window
count/sum deltas with the datastore phase measurements; containing and child
durations overlap, and histogram bucket bounds are not exact percentiles.

Git smart HTTP emits one fixed-field `git_http_request_timing` event per
completed request, including requests rejected before a backend handler runs.
It records a fixed operation name, HTTP status and five disjoint boundary
durations: authentication, verified repository-path resolution, tenant
authorization, push-context preparation and the handler. Unreached stages
are zero; their sum equals `total_ms`. Timing starts after request-ID insertion
and ends when the middleware chain returns. Events contain no paths, resource
identities, credentials, bodies or arbitrary error strings.
The handler includes request-body consumption, signed backend RPC work and
response writing; it is not a pure Git-service duration. Client startup,
pack preparation and work before the request enters the router are excluded.
Compare complete advertisement and receive counts with acknowledged pushes
before attributing an advertisement-to-receive gap to authentication or
transport. These events do not remove mapping integrity checks, fresh
authorization or push-policy reads.
The same boundaries are exported as
`gitstore_git_http_stage_duration_seconds`, labelled only by fixed operation,
phase and outcome values. Rejected and failed requests have distinct outcomes;
unreached phases do not contribute synthetic zero-duration histogram samples.
The `total` phase contains the other phases and must not be added to them.
For example, query per-replica middleware p95 with:

```promql
histogram_quantile(0.95,
  sum by (le, instance, operation, phase, outcome) (
    rate(gitstore_git_http_stage_duration_seconds_bucket[5m])
  )
)
```

Histogram percentiles are bucket estimates. Use exact before/load-end counter
deltas and matched request logs for complete push accounting; Prometheus
`increase` extrapolates between scrapes and is not an exact integer job count.
With Prometheus observability enabled, the lifecycle harness retains five-second
API and controller series plus the queried phase results in its evidence.
This preserves the measurement history after the managed scraper is stopped.

`gitstore_git_rpc_verification_phase_seconds` separates signed Git RPC
verification capacity wait, blocking-pool wait, replay-lock wait, envelope
checks, nonce reservation, durable persistence and reservation completion.
Its only label is a fixed phase name. These phases describe existing
verification work; they do not relax signatures, expiry, replay rejection,
durability or bounded admission. Compare complete-window counts before
attributing authentication latency to cryptography or retained replay storage.
The same durations appear in fixed-field `RPC verification dispatch timing`
and `RPC replay reservation timing` logs. Deployments without a Git-service
Prometheus scrape surface must collect these completed-request events; cgroup
CPU/RSS snapshots do not contain the verification histograms.

Signed RPCs retain an exclusive nonce record and an inode fsync for each
request. After that fsync completes, at most 32 verification requests can
share an already-ready directory fsync. The first ready request starts its
barrier immediately; arrivals during a barrier require a later barrier.
No request is authorized until its own covering barrier succeeds. A failed
barrier fails every covered request, without relaxing replay rejection or
allowing uncertain records to be retried as new nonces. There is no collection
timer, background writer or increase in verification concurrency. The
`directory_barrier` verification phase counts physical nonce-directory fsync
attempts, not individual requests or separate clock-cleanup barriers.
Fixed-field `RPC replay directory barrier timing`
events report the same barrier duration, success and covered-request count.
Reconcile these counts with completed reservation events before claiming
physical request reduction; per-request persistence also includes inode fsync
and any wait for its covering barrier.
The `replay_inode` and `replay_directory` phases, also present as fixed fields
on reservation timing events, separate the mandatory inode fsync from the
complete directory wait. A shared barrier's physical duration is contained
inside participating requests' directory waits; do not add these overlapping
durations or compare their unlike sample counts as an end-to-end latency claim.

Small admission deltas containing two to four distinct paths use one signed
`GetFiles` request for both old and new immutable commits when both commits
exist. The optional second commit is signed, and both snapshots must return
their exact commit receipts and ordered results. The two trees are read
sequentially under one owned repository read guard, which remains held while
blocking work drains after caller cancellation. The RPC preserves path order
and cardinality, reports missing paths
explicitly and fails on missing repositories/commits, non-file entries or
storage errors. Each blob is bounded to 4 MiB and the complete encoded response,
including both snapshots and protobuf overhead, to 16 MiB. This combined ceiling
is tighter than two independent 16 MiB replies; an oversized pair fails
explicitly rather than splitting into additional reads. Zero-commit, single-path
and larger/full-tree shapes retain their existing ordinary reads.
Authentication, repository
read authorization and the existing admission/ref/CAS checks remain required.
History proofs continue to use `GetFile`. No cache or wider read fanout is
introduced. Deploy the Git service supporting paired `GetFiles` before rolling
API replicas; missing second receipts or an unsupported RPC fail explicitly
without an older-server or individual-read fallback.
For ordinary paired deltas, the signed request also names the current ref.
After reading both snapshots, the Git service resolves that ref under the same
owned guard and requires its tip to equal the new immutable commit. The API
accepts only that exact receipt in place of the middle ref-only RPC; its initial
and final ref checks remain. A changed or deleted ref returns `Aborted` without
partial snapshots and enters the existing bounded supersession retry.
Namespace-only convergence and unpaired reads retain the middle check.
Missing or mismatched current-ref receipts fail explicitly, including during
mixed-version deployment. The receipt also counts toward the same 16 MiB reply
ceiling; deploy the new Git service before rolling API replicas.
The existing read-phase histograms use the fixed `batch_content` mode for
these calls. Reconcile successful `GetFile` and `GetFiles` counts with complete
phase reports before comparing per-RPC latency or physical request totals.

Each current-ref check for a committed Repository create/update uses one signed
`GetFile` history proof. The Git service resolves the named ref, proves that no
intervening commit changed the path and returns its bytes under one repository
read lock. An explicit signed `allow_ancestor_receipt` request restores the
ref-only fast path when the tip is exactly the caller's authoring commit.
Its `at_ancestor` response confirms only that ref identity, not file existence;
content, blob identity and size are empty. Advanced refs still require the
complete bounded history proof and byte-identical committed content.
An absent or malformed requested receipt fails explicitly; there is no
old-server or second-RPC fallback. Deploy the supporting Git service before
rolling API replicas. Ordinary file reads do not request or accept receipts.
Admission still checks every required boundary; it does not cache
or reuse a previous check. A missing ref aborts the proof, while unavailable
repositories, corrupt refs and storage failures remain explicit errors. The
blocking read retains its lock if the requesting task is cancelled.

Blocking Git writes also retain their writer lock until publication finishes,
even when the caller disconnects. A successful receive dispatches its
asynchronous post-receive callback from the publication job, rather than from
the cancelled caller. Failed publication does not dispatch a callback.
This protects task cancellation; it does not make callbacks durable across
a Git-service process crash.

Shallow Git clients can push or delete refs when their declared shallow
boundaries already exist as commits in the complete live repository.
The API preserves these boundaries in the signed first receive chunk;
the Git service checks them under the repository writer lock before staging.
Unknown boundaries, non-commit objects, duplicates and malformed IDs fail
explicitly. Importing a shallow repository or changing server shallow
boundaries is unsupported. Command headers are bounded to 4 MiB and 4,096
distinct shallow commits. Deploy the supporting Git service before rolling
API replicas; the ordinary non-shallow push path is unchanged.

The lifecycle capacity gate's raw-push/controller probes clone only the current
tree at depth one, as the sustained push workers already do; they do not need
the deployment's accumulated authoring history. Ordinary integration clones
and full-history Git transport tests remain unchanged. The shared Git runner
enforces its 30-second operation deadline against the owned process group,
including transport children, with a bounded output-pipe drain. A timed-out
child cannot keep downloading after its caller has returned.

After final Repository removal, its Relay `node` lookup returns `null` without
an error. Authorization returns that confirmed UID absence directly; it does
not reread the UID through an unguarded resolver. Missing parents, storage
failures and access denials remain errors. Typed Repository lookups retain
their explicit `NOT_FOUND` response.

Authorization for a delta of two to four Files in one namespace loads fresh
resource facts with two sequential partition-local reads: bounded name
locators, then the exact authoritative row keys. A locator alone is not
resource data. Input positions, duplicate names and explicit absence are
preserved; storage errors, cancellation or mismatched identities fail the
whole read without a point-read fallback. Every File still receives its own
live semantic authorization check, including ownership transfer. Larger,
mixed-kind and cross-namespace deltas retain individual reads. This introduces
neither cross-request caching nor additional query concurrency.
The datastore histogram records these calls as `GetFileSnapshotsByName`.

Namespace policy evaluation uses the already parsed old and proposed trees to
determine whether a Namespace is present. Receives containing no Namespace
resources avoid reparsing their blobs for Namespace policy discovery. Namespace deletions and
unchanged Namespace entries still require the existing policy and dependency
checks, including when another tree contains only catalog resources. Structural
validation and live semantic authorization remain mandatory for every receive.
Immutable-field validation parses each old and proposed blob once and retains
separate path and identity indexes. Duplicate identities at different paths
still participate in path-specific immutability checks; all Namespace,
Repository, CategoryTaxonomy and File constraints remain enforced. Parsed
entries are request-local and never cache datastore or authorization facts.

The same datastore histogram exposes fixed catalog write phases:
`CatalogParentCheck`, `CatalogProjectionReadBefore`, `CatalogMembershipWrite`
and `CatalogProjectionReadAfter`. File updates additionally report
`FileUIDLocatorRead`, `FileAuthoritativeCAS` and `FileOwnerReferenceSync`.
Physical conditional batches report `FileConditionalBatch2`,
`FileConditionalBatch3` through `FileConditionalBatch16`; their counts describe
attempted batch sizes, while `FileAuthoritativeCAS` still measures every
individual row attempt, including queueing and any definite-conflict fallback.
These preserve the ordinary reads, conservative index write and per-resource
CAS; they add no cache, batching or authorization shortcut. Membership samples
include conservative writes, readback repairs and obsolete-entry removals.
A before-read
includes its locator read, and the complete mutation includes all phases, so
do not add overlapping durations. Compare successful attempt counts with
complete phase count/sum and error deltas before proposing a write optimization.

The File workload retains each push-worker slot until both APIs have verified
that update's exact sequence, identity and typed reference. The
bounded per-offer `timelines` match queue delay and offered-to-ACK time with
worker occupancy and that same update's verification. Worker occupancy includes
verification; ACK does not. A later offer can wait behind that occupied slot.
These prefix samples do not replace the complete workload counters or gates.
Its
`projection_visibility` stage includes verifier scheduling and polling as well
as reads. `projection_read` reports the cumulative wall time of completed
two-replica read rounds per acknowledged push; the replicas are queried
concurrently, so this is not the sum of their HTTP durations. `projectionPolls`
counts participation in read rounds per push, not distinct HTTP requests.
These measurements overlap visibility time and must not be added to it.
Do not detach verification merely to shorten queue latency: a subsequent push
can overwrite the same File identities before their accepted update is checked.

File node authorization runs at most eight authoritative point reads
concurrently. Scylla resolves each UID locator and reads its exact
`(namespace, creation_timestamp, uid)` canonical key, without a partition scan
or filtering. All reads drain before returning. Missing rows retain their
positional absence, while storage failures, cancellation or wrong identities fail
authorization before response hydration. Hydration reuses those exact
authorized snapshots, not a cache or a fresh record with different ownership.
These logical key bounds do not establish a bound on SSTable or tombstone work;
measure the complete request path under the intended load and partition skew.

To distinguish Git RPC transport time from signed-request verification, enable
the Git service's `RUST_LOG=info,gitstore::auth::interceptor=debug` tracing filter.
`RPC verification dispatch timing` reports capacity-slot wait, blocking-pool
dispatch wait and total verification time. `RPC replay reservation timing`
reports replay-lock wait, envelope checking, serialized reservation,
durable nonce persistence and reservation-completion wait.
Fields use milliseconds and the fixed RPC method; no nonce, signature,
credential, payload or resource identity is logged. These are per-request
diagnostics, not capacity acceptance metrics. Retain an exact measurement
window, account for tracing overhead and keep durable replay protection enabled.

Signed Git RPC verification keeps nonce uniqueness, clock checks and expiration
cleanup serialized, but drains each nonce's file and directory synchronization
outside that lock. The existing 32 verification slots bound concurrent durable
writes. Pending nonces remain spent and cannot be reclaimed, even after expiry,
until persistence finishes. Cancellation after dispatch still drains the write;
durability failures reject the request and retain replay exclusion. No handler
effects run before successful persistence. This does not remove the singleton
Git requirement or establish zero-downtime replacement.

Expiration cleanup reuses a durable clock floor and advances it at most once
per 250 milliseconds while expired records exist. Only nonpending records at
or below that persisted floor can be removed; later-expiring records remain
spent until a later cleanup. Signature expiry checks and in-process clock
rollback rejection remain exact, not rounded. The 100,000-record ceiling still
fails closed, and every removal is covered by a persisted rollback barrier.

The API retries only side-effect-free unary Git reads (`GetFile`, `GetFiles`, `ListFiles`,
`InfoRefs`, `ListTags` and `GetLatestTag`) after `UNAVAILABLE`, at most four
attempts with 25, 50 and 100 millisecond waits. Each attempt carries a freshly
signed single-use nonce and retains the original actor and request binding;
partial responses are cleared before retry. Caller cancellation stops the
wait. Authorization failures, absence, invalid input, saturation and other
status codes are not retried. Writes and streams are never retried by this
path, and exhaustion returns the failure. This allows a brief unavailable
read to recover without accepting requests during a clock rollback or
weakening durable replay protection.

## Repository lifecycle fence

### Git-backed deletion safety

Use matching current Git service, API, and controller builds. GitStore is under
active development; mixed-version writers and upgrade compatibility with older
binaries are not supported. Concurrent current-version API/controller replicas
and restart recovery must remain correct. Keep exactly one active Git service,
with retained storage and non-overlapping replacement.

All implemented `complete*Deletion` inputs require the resource's GraphQL
`id: ID!`, its current `resourceVersion`, and its existing name/namespace scope.
A conflict requires a fresh resource read, never retrying stale work against a
same-name replacement. The check applies equally to Namespace, Repository,
Product, and CategoryTaxonomy.
An error-free empty Namespace completion payload means the resource is already
absent and is successful on retry; transport, authorization, and conflict errors
remain failures.

An empty, verified system repository does not block Namespace initiation.
Contained resources, including Files, do, as do all ordinary repositories
(including terminating ones). Completion cleans up system storage by UID before
releasing the Namespace. Neither path cascades resources or ordinary repositories.

### Repository membership index

Repository emptiness uses one polymorphic `resources_by_repository` table:
`PRIMARY KEY ((repository_id, shard), kind, uid)`. A new kind adds rows, not a new
table or another per-kind query. Product, ProductVariant, CategoryTaxonomy,
Collection, and File use the same membership-write and deletion-fence protocol.
Both datastores reject their creates and updates when the referenced repository
UUID has no live repository row.
Namespace/Repository child checks continue using their existing indexed
relationships; no parallel per-kind membership tables are needed.

Membership is distinct from optional semantic owner references: a Product
without a Category owner still occupies its repository, and a nonblocking owner
reference must not permit deletion of its backing repository. Membership cannot
be removed by editing `ownerReferences`. The existing owner-dependent index
continues to answer its different, owner-specific queries.

An emptiness check performs at most 32 partition-key lookups with `LIMIT 1`,
short-circuiting on the first member. It never enumerates kinds, uses
`ALLOW FILTERING`, or performs a live count. Membership is written conservatively
before authoritative admission and removed only after authoritative removal is
confirmed; errors cannot be interpreted as emptiness. The shared fence excludes
admission while the parent transitions to deletion.

Active same-name, same-namespace Repository updates use their expected-version
row CAS without acquiring the Namespace catalog reservation. A concurrent
deletion transition still causes a version conflict. Deletion intent,
termination, path changes and writes to terminating repositories retain the
exclusive reservation, including status writes during completion cleanup.
Catalog membership writes still use that reservation; this distinction does
not establish combined-load capacity or remove the need to measure contention.

Within a held active Namespace reservation, nested catalog writes share successful
Repository eligibility checks for at most four distinct parents. The reservation
excludes parent deletion and ownership transitions until the writes drain; the
checked result is discarded when the reservation ends. Additional parents use
normal reads. Failed checks are not retained, waiting callers preserve their own
cancellation and deadlines, and authorization and authoritative resource-version
checks still run independently for every mutation. This avoids repeated parent
reads without adding a cross-request cache or increasing write concurrency.

These are separate bounds: table count and query fan-out are independent of
kind count, but index storage necessarily grows with resource count. Fixed hash
sharding spreads rows; it does **not** impose a hard partition-size limit, and
`LIMIT 1` bounds returned rows, not tombstone or SSTable read work. Request
deadlines and datastore errors fail closed. Capacity acceptance must measure
worst-case partition size, skew, churn/tombstones, and empty-check latency.
Do not claim unlimited-cardinality performance or introduce an undocumented
resource quota. More buckets or adaptive partitioning require measured evidence.

Conditional writes have a separate contention bound. In the namespaced catalog
tables, the Namespace is the partition key: different resource UIDs in that
Namespace do not give their resource-version CAS operations independent Paxos
partitions. Changing read fan-out or adding a list index does not remove that
write contention. Compare same-partition and distributed-partition conditional
writes at the concurrency actually used by the mutation path, not just at the
offered worker count. Each File callback permits at most four concurrent writes;
a shared reserved cohort permits at most sixteen. Higher-concurrency native experiments can identify
the mechanism, but do not establish the cause of a combined-load latency failure
or justify changing the authoritative layout without migration and bounded-query
evidence.

File updates retain the same expected-resource-version CAS and immutable
authoritative key, but send CQL `UNSET` for unchanged columns from the fresh
authoritative preimage. Explicit removals are still written. Resource version
and logically absent JSON remain explicit writes, preserving normalization of
CQL null to empty JSON text. This reduces redundant cell and tombstone writes;
it does not split a hot Paxos partition or establish an acknowledgement-latency
improvement. Full CDC preimages/postimages, membership repair and owner-reference
synchronization remain unchanged.

The native File conditional-batch diagnostic compares one-row writes with
four-row and eight-row same-partition batches, and includes unbatched controls
at the same in-flight request count. It checks every final resource version and
body; a separate conflict test verifies that one stale condition rejects all
rows in the batch. The four-row and sixteen-row fixtures cover the current callback and cohort
bounds. These are isolated write-cost measurements, not deployed acceptance:
they exclude authorization, membership projections, reservation queueing and
watch delivery. An atomic batch must not turn one caller's stale version into
another caller's failure; independent retry, cancellation and repair semantics
must be preserved in the admission path.

GraphQL `repository` reads hydrate from the exact Repository and Namespace records
used for that field's successful policy decision. The records are scoped to one
field and exact UID or namespace/name selector; sibling fields and later requests
perform fresh lookups. This avoids repeating routing and authoritative reads after
authorization, and prevents returning a different owner or version than the one
checked. Mutations, lifecycle fences, mapping validation and policy checks remain
unchanged. Canceled reads and malformed stored records still fail explicitly.

Within a confirmed catalog reservation, File updates drain already-ready distinct
File UIDs into same-partition batches of at most sixteen small rows, reusing the
existing sixteen-entry writer queue and sixteen-mutation reservation bound.
There is no gathering delay. Groups larger than four require a combined value budget of
at most 64 KiB, including conservative per-value overhead; larger values retain
the four-row maximum. Each row retains its original
expected resource version, immutable key, values and actor. Repeated UIDs remain
ordered in separate requests. A definite all-or-none condition miss retries
each row independently, so a stale row does not reject unrelated valid rows.
Transport and iterator-close errors do not trigger that fallback or blind
batch retries. One caller's cancellation does not cancel live peers; already
canceled queued rows do not write, and accepted physical work drains before
the reservation is released. Full-image CDC, conservative membership and
owner-reference synchronization remain required. Fewer physical CAS requests
alone do not establish a loaded acknowledgement-latency improvement.

Catalog CDC readers pair every preimage and postimage in a multi-row frame by
immutable resource identity. All rows at the same source position enter the
sequencer together; source progress advances only after the whole frame is
materialized. Multi-row deduplication keys include the UID, and single-row
keys remain unchanged. Malformed, repeated or oversized images fail closed.
The pending-work ceiling counts resources, not just frames.

When upgrading from a reader that supports only single-row frames, pause File
mutations until every API replica has the new reader. Replacing replicas
serially is safe for reads and single-row writers, but an old reader must not
consume new physical multi-row batches. Resume File mutations only after all
replicas are ready; this is an explicit upgrade write pause, not a claim of
zero-downtime mixed-reader compatibility.

Exclusive catalog reservations retain their successfully acquired Namespace
epoch. Release attempts the same conditional decrement against that epoch and
the single pending reservation without first rereading the fence. If a concurrent
Repository creation changed the fence, a definite non-applied result uses the
existing fresh-read release and preserves the other writer's reservation.
The first release attempt disables retries: an uncertain outcome cannot be
hidden by a later non-applied result. Errors retain the reservation for explicit
repair; caller cancellation still uses bounded uncanceled cleanup.

The design follows Cassandra's
[partition sizing and bucketing guidance](https://github.com/apache/cassandra/blob/trunk/doc/modules/cassandra/pages/developing/data-modeling/data-modeling_refining.adoc)
and [CQL key restrictions, limits, and filtering semantics](https://github.com/apache/cassandra/blob/trunk/doc/modules/cassandra/pages/developing/cql/dml.adoc).
ScyllaDB's engineering discussion of
[hot partitions and goodput](https://www.scylladb.com/2024/04/17/per-partition-query-rate-limiting/)
explains why a single large, hot parent partition is not an adequate alternative.
A native counter or asynchronously materialized total is not deletion proof:
an ambiguous update or lagging projection must never authorize storage removal.

The schema contains one shared membership table and its per-repository
readiness/tombstone metadata, not separate membership tables per resource kind.
Released migration history remains immutable and non-destructive.

Git smart-HTTP shallow clones and absolute-depth fetches negotiate
`shallow`/`unshallow` boundaries before the acknowledgement and pack stream.
The pack contains only the requested depth's commits and their trees and blobs;
reporting a shallow boundary while sending the entire history is not bounded
transfer. Depth traversal and shallow pack output respect the configured pack
size. Malformed packet framing and invalid depths fail explicitly rather than
falling back to full-history traversal. A clone failure before offered load is
a failed prerequisite, not acknowledgement-latency evidence.

### Repairing incomplete projections

Missing membership readiness fails closed. Fresh repositories initialize it
when their empty authoritative record is created. For retained development data
or a damaged projection, stop relevant mutations and controller writes while
running the existing maintenance commands from `gitstore-api/`:

```bash
go run ./cmd/gitctl scylla-projection-audit --hosts "$HOSTS" --keyspace "$KEYSPACE"
go run ./cmd/gitctl scylla-projection-repair --dry-run --hosts "$HOSTS" --keyspace "$KEYSPACE"
go run ./cmd/gitctl scylla-projection-repair --confirm --hosts "$HOSTS" --keyspace "$KEYSPACE"
go run ./cmd/gitctl scylla-projection-audit --hosts "$HOSTS" --keyspace "$KEYSPACE"
```

Supply database passwords through `GITSTORE_API__DATASTORE__SCYLLA__PASSWORD`.
The repair commands cover all projections, not just repository membership.
Repair publishes readiness only after complete verification;
an interrupted repair can be retried with a fresh audit and confirmed repair.
Readiness is not a detector of subsequent manual index corruption.
Resume writers only after the final audit is clean.

`DeletionPending=True` records recoverable mutation work before the Git call.
Controllers resume that work after API replacement. `SUPERSEDED` means the
manifest content changed: do not overwrite it or clear the operation blindly.
`DELETION_REPAIR_REQUIRED` blocks termination without removal evidence.
Inspect retained manifests and terminating rows, compare
their UID, admitted path/ref/revision and current tree, and resolve ambiguous
ownership before allowing cleanup. A missing file alone is not proof of a
successful authorized mutation.
For `GetFile` and `GetFileStream`, `NOT_FOUND` means the path is absent from a
readable tree.
A missing authoring repository/ref or a path occupied by a non-file entry returns
`FAILED_PRECONDITION`. Unavailable or unreadable authoring storage blocks both
Namespace and Repository completion even when a prior removal commit is recorded.
Product and Category creation may initialize an unborn branch through the Git
commit operation, which still requires existing repository storage. This
write-only handling does not apply to updates or deletion verification.

The Git service emits `post_receive_dispatch_timing` with `dispatch_us` for the
async hook's scheduling delay. Each admission callback emits
`post_receive_admission_timing` with `dispatch_us`, `repository_open_us`,
`diff_us`, `callback_us` and `success`. Durations are integer microseconds;
callback time includes signing, client queueing, transport and API processing.
These timing events contain no actor, repository, ref or path fields. Repository
opening is shared by a multi-ref hook invocation and repeated in each matching
callback's sample, so it must not be summed as independent work. Correlate
samples with the exact load window and callback outcomes. The existing
post-receive `hook_phase_complete` duration covers scheduling only, not
asynchronous admission completion.

Capacity collectors bound complete metrics responses at 8 MiB, including
internal container probes used to verify process identity. Metrics do not share
the smaller 128 KiB command-output capture used for Git diagnostics. Overflow
fails the collection explicitly; a truncated response cannot prove identity or
capacity. Command failures distinguish sanitized exit, startup and output-limit
errors without exposing command arguments or captured output.

The Git service's `gitstore_git_file_read_phase_seconds` histogram separates
unary `GetFile` authentication, Repository read-lock wait, blocking-pool wait,
and payload reads. Its only label, `phase`, has the fixed values
`authentication`, `repository_lock`, `blocking_queue`, and `payload`; no
resource or identity is included. Payload timing includes repository opening,
tree lookup and any requested unchanged-history proof, and records failed
reads too. These server phases do not include client transport or retry delay.
Compare their counts and sums with the full RPC histogram before attributing
slow reads to storage or increasing concurrency.
Successful unary reads also return these four durations in bounded response
metadata. Each API exports them as
`gitstore_git_file_read_server_phase_seconds{phase="...",mode="..."}` and records
`gitstore_git_file_read_phase_reports_total{result="..."}` with the fixed
results `observed`, `missing`, `invalid`, and `rpc_error`. Missing or malformed
telemetry is counted explicitly without changing the read's content or error.
Compare phase samples only with successful RPCs; failed reads do not export
partial server timings. With `git_service.metrics.listen_addr` configured,
the native Git `/metrics` listener exports these histograms and signed-RPC
verification phases directly. `gitstore_git_blocking_pending`,
`gitstore_git_blocking_active` and `gitstore_git_blocking_queue_seconds`
distinguish queued from executing owned jobs; cancellation does not remove
executing work from the gauge. Tokio live-task and global runnable-queue gauges
are sampled at scrape time. Use the API-exported read histograms when the
native listener is disabled.

Use the [telemetry and profiling configuration](../configuration.md#telemetry-and-api-profiling)
for a local capacity observation stack. HTTP phase, signed RPC and blocking-job
spans preserve W3C parent context; asynchronous post-receive dispatch/callback
tasks retain their originating trace without changing publication or signing.
Trace propagation is not authorization, and it never carries baggage or bodies.
Use matched phase samples, complete native metric histories and live profiles
before selecting an optimization. CPU samples alone do not measure storage,
lock or Tokio scheduling waits. Native `perf` availability/permissions must be
recorded; do not silently elevate container privileges to obtain a profile.
The fixed modes `content` and `history_proof` distinguish ordinary blob reads
from reads that also prove the path was unchanged across bounded commit
history. No path, commit, ref or Repository identifier becomes a metric label.
The API also exports the aggregate `history_commit` and `history_tree` phases
from successful reads. They measure commit-object lookup and tree/path lookup
within the proof, not additional independent RPC time; ordinary reads report
zero for both. When the requested ancestor is the resolved tip, the target
commit, tree and path have already been validated for a full read: the proof
visits one commit and reports zero additional history lookup time. An explicitly
requested ref-identity receipt instead visits the resolved commit without
reading its tree or blob; those subphases also report zero.
Tree lookup validates every
encoded entry, including trailing entries after a matching or missing path,
without retaining a decoded entry list.
`gitstore_git_file_read_history_commits` counts visited commits
only for successful proofs, bounded by the unchanged 4,097-commit walk limit.
Compare these subphases with payload time and proof depth before changing
decoding, caching or history traversal. All durations and the work count are
validated together; partial or invalid reports record no samples.
Failed verification dispatch events include the gRPC status code and the
verifier's fixed failure message, distinguishing clock rollback from replay
storage or durability failures without logging signed envelopes or nonces.
The fixed `repository_open`, `revision`, `target_tree`, and `blob` subphases
cover opening the repository, resolving/loading the target commit, its initial
tree/path lookup, and loading/copying file bytes. These and the two history
subphases are contained within payload time. They distinguish an expensive
revision lookup from an expensive history walk; do not add their overlapping
histograms to the payload histogram.
Reads targeting a full immutable object ID load that object directly, without
general revision-prefix disambiguation. Named refs and annotated tags still
resolve normally. Missing targets, non-commit targets and unreadable object
storage retain their error behavior; the fast path adds no cross-request cache.

Plural File node reads hydrate at most 250 IDs per backend call. UID locators
use sequential queries containing at most sixteen distinct partition keys each;
the API never sends a 250-key cross-partition `IN` query. This reduces repeated
client/coordinator request setup, not the number of locator partitions read.
Canonical records are fetched by exact `(creation_timestamp, uid)` tuples within
each Namespace, with at most eight Namespace reads active. Neither lookup uses
filtering or full-partition scans. Duplicate positions receive
independent copies; stale locators remain absent and storage failures fail the
whole read. Authorization checks each record's stored owner and reuses that exact
snapshot for hydration, without a second read or cross-request cache. Existing
public request limits are unchanged; singular node reads remain point reads.
The native hydration diagnostic checks the physical query and returned-row
counts at the maximum request size, compares against bounded point reads, and
preserves cross-replica update, deletion/recreation and stale-locator readback.
Its isolated timings do not establish a combined-load acknowledgement benefit.

The local RBAC evaluator uses immutable policy snapshots and bounded candidate
sets. Overlapping subject/group bindings evaluate each shared role once per
request and cluster/namespaced provenance, after checking the binding's scope.
This preserves the first matching binding's diagnostics and cluster transfer
overrides without repeatedly walking already-evaluated rules. The three supported
attribute values are decoded once per decision, preserving missing, invalid and
oversized-value denial rather than repeating map lookups for every rule.
Reloads publish complete snapshots; decisions perform no policy-file, datastore
or network I/O.

Catalog admission retries transient Namespace fence contention within a bounded
request budget rather than immediately rejecting a committed push. Retries use
jitter between 1 ms and the exponentially increasing 10-100 ms backoff ceiling;
the five-second reservation budget is unchanged.
Namespace rows must have initialized child-fence counters and an explicit watch
commit marker. Null counters block lifecycle writes with a repair-required
error; CDC records without the marker or its staged creation transition block
watch publication with a discontinuity error. Older-binary creation protocols
are not supported. Projection repair uses the Product's recorded resolved
category path without expanding it through another resource's current path.
Each API bounds local reservation holders and waiters to 256, ordered by
Namespace UID. Idle queue keys are removed. This queue holds no authoritative
state: a writer still acquires the Scylla fence, and local queue wait and
distributed acquisition share the same five-second budget.
Two-to-four File creates or updates in one push and one namespace share one
exclusive Namespace reservation, with at most four independent File writes in
flight. The held reservation proves that the same Namespace remains active
throughout that batch, avoiding another Namespace-name lookup for every File.
Each File still freshly reads and validates its Repository parent, writes
conservative membership, and uses its own resource-version CAS. A different
datastore instance, Namespace or expired batch context cannot reuse that proof;
ordinary writes retain their fresh Namespace check. Namespace deletion intent
and termination cannot start while the reservation is held. No parent record
is cached across batches or requests.
File updates reuse the authoritative envelope read for the mutation's initial
membership snapshot when checking immutable identity, expected version and old
owner references. This is a local value within one mutation, not a cached parent
or resource record. The final authoritative readback and membership repair
remain in place, including after conflicts.
Readback uses the immutable namespace/creation-time/UID key obtained from that
initial authoritative envelope, avoiding another UID-index lookup while still
reading the current authoritative row.
When a prior envelope exists, conservative membership includes its name and
creation time before the write. If authoritative readback confirms the exact
same membership values, admission does not repeat that identical index write.
Missing or changed values, creates, deletion cleanup and repair errors retain
their normal reconciliation paths.
Eligible File batches perform at most four lookup/authorization preparations
before taking the reservation. A changed-commit envelope must still pass the
authoritative version CAS; collisions reload and reauthorize under the
reservation. Same-commit envelopes are looked up again under the reservation
before they can take a no-op path. Preparation is not proof of current storage
or permission to bypass parent/lifecycle checks.
Lifecycle writes cannot share an active admission batch. This is not an
atomic multi-resource transaction: another File can commit even if one fails.
Admission waits for every write and reports batch errors,
and ambiguous projection outcomes retain the reservation for repair. Larger,
mixed-kind and deletion batches retain individual reservations. Batch contexts
cannot bypass a different datastore replica's reservation or outlive the batch.

The existing `gitstore_datastore_operation_duration_seconds` histogram separates
`NamespaceFenceLookup`, `NamespaceFenceQueue`, `NamespaceFenceReserve`,
`NamespaceFenceMutation`, and `NamespaceFenceRelease` operations. Samples retain
only the bounded operation and backend labels, never namespace names or UIDs.
Queue timing covers the local admission queue; reserve timing covers the
distributed fence and its retries. Mutation timing excludes reservation and
release, but includes child admission work. Release is sampled only when
attempted: repair-required mutations deliberately retain the fence. Namespace
Repository creation also reports reserve, mutation and release phases.
Compare counter and histogram differences at the same load boundaries on each
API; phase percentiles cannot be added to reconstruct end-to-end percentiles.
Inner File timings overlap the containing batch's mutation phase.
The existing `gitstore_namespace_admission_stage_duration_seconds` histogram
also separates catalog callback authentication, namespace lookup, mutable-ref
resolution, immutable snapshot loading and application with the fixed stages
`catalog_callback_auth`, `catalog_namespace_lookup`, `catalog_resolve_ref`,
`catalog_snapshot_read` and `catalog_apply`. Snapshot loading includes parsing;
ref resolution is observed for both push and synchronous GraphQL admission.
Failed attempts are timed too, and retries can produce several stage samples
for one callback. Application includes the containing mutation batch and its
queue, so these overlapping histograms must not be added together.
The API and standalone CDC module use the published Scylla Go driver's
`v1.18.1` patch, which synchronizes connection-picker state during concurrent
pool filling and session shutdown. Do not suppress shutdown race reports or
replace the published driver with a private source copy.

Post-receive admission with declared changed paths reads only those exact paths
at each immutable commit, deduplicating paths without enumerating the repository
tree. A path absent from the old or new readable tree is valid delta evidence.
Unreadable repositories/refs, permission failures and transport errors fail
admission explicitly instead of becoming an empty successful result. Calls
without a changed-path set still enumerate the tree; failure to read a listed
path is not evidence of deletion.
Mutable-ref lookup distinguishes an absent or superseded ref from a failed
lookup, including before writes, during conflict retries and after writes.
Cancellation and infrastructure failures are reported rather than counted as
successful stale admissions. Synchronous committed admission preserves the
failure cause and records failed ref repair separately from supersession.
Missing Git dependencies or unresolved repository/namespace ownership also
fail explicitly. Category sibling operation discovery uses authoritative
lookup results; an unreadable sibling is not assumed to be a create.

A confirmed failed repository cleanup restores both name mappings and list projections
before releasing its fence. Controllers can also resume a known terminating UID
when its name mapping is missing; a different name, version, or UID is rejected.
If restoration itself fails, the fence remains held for projection repair.

Keep the Git service's UID tombstones permanently and include its private
operation refs in repository backups. They fence stale provisioning and prove
uncertain deletion outcomes. A prepared receipt can resume only while its durable
preparation marker and original expected tip still prove it was not published.
Unprovable interrupted publication, restored files, legacy unmarked receipts, and
ancestry exceeding the bounded recovery walk fail closed and require operator
reconciliation; retrying an arbitrary absent path is not sufficient.

For a terminating resource with a retained manifest, use an authenticated
Git checkout of the canonical authoring repository as the maintenance surface.
Record the GraphQL `metadata.uid` and `resourceVersion`, fetch `main`, and compare
the exact file against its admitted revision. Re-read the resource to confirm
the same UID/version, remove only that file, commit, and push with an explicit
`--force-with-lease=refs/heads/main:<reviewed-head>` precondition. Admission
records removal evidence for that terminating UID; controller completion can
then proceed. A changed manifest is rejected rather than silently discarded.
Do not fabricate a removal for an absent row, restore a manifest just to delete
it again, or clear finalizers directly. Retained files with missing records and
unverifiable provenance require manual ownership reconciliation while writers
remain quiesced.

The repository lifecycle fence (Scylla LWT columns on the Namespace row that
serialize namespace deletion against in-flight repository creation) is always
enabled on every backend; it is not configurable and has no rollout gate.
`deleteNamespace`, `completeNamespaceDeletion`, and `createRepository` always
enforce it.

## Stable response codes

Namespace mutations use the shared mutation error envelope: `extensions.code`,
plus `diagnostics[]` (each with a stable `reason`), and for
`ADMISSION_REJECTED` only, `phase` (`PRE_RECEIVE` or `POST_RECEIVE`) and, for
`POST_RECEIVE`, `commit`. No other keys are returned. Clients branch on `code`
first and on `diagnostics[].reason` for detail. Reason values are unchanged
from earlier releases.

Repository GraphQL mutations also preserve the committed-admission envelope,
including `CONFLICT` with `RESOURCE_VERSION_CONFLICT` or `SUPERSEDED`.
A superseded authoring commit is not a transient HTTP failure and must not be
blindly retried. Read current resource and authoring state before deciding
whether a new mutation is still appropriate.

For Repository create/update mutations, an unrelated commit no longer rejects
the request merely because it advanced the shared authoring branch. Admission
proves that the requested path stayed unchanged along the intervening
single-parent history, preserves the original commit and actor, and repeats
that check before each row CAS retry and after admission. A changed, removed,
or changed-then-restored path remains superseded. Proof does not cross merges
or unrelated history and is bounded to 4,096 commits and one second per Git
read; an unavailable or unverified proof fails explicitly. This does not change
deletion admission or authorize another writer's manifest.

| Code | Reasons |
|---|---|
| `ADMISSION_REJECTED` | `INVALID_ENVELOPE`, `INVALID_IDENTIFIER`, `RESERVED_IDENTIFIER`, `INVALID_TIER`, `INVALID_AUTHORING_TARGET`, `DUPLICATE_IDENTITY`, `IMMUTABLE_NAME`, `TIER_DEMOTION` |
| `FAILED_PRECONDITION` | `BOOTSTRAP_NAMESPACE`, `NAMESPACE_TERMINATING`, and for deletion `NAMESPACE_NOT_EMPTY` |
| `ALREADY_EXISTS` | `NAMESPACE_ALREADY_EXISTS` |
| `NOT_FOUND` | `NAMESPACE_NOT_FOUND` |
| `CONFLICT` | `RESOURCE_VERSION_CONFLICT`, `SUPERSEDED` |

### Migrating from the earlier codes

| Earlier `code` | Earlier extensions | Now |
|---|---|---|
| `NAMESPACE_STRUCTURAL_VALIDATION_FAILED` | `phase: STRUCTURAL`, `reason` | `ADMISSION_REJECTED`; same reason in `diagnostics[0].reason` |
| `NAMESPACE_IMMUTABLE_FIELD` | `phase: STRUCTURAL`, `reason` | `ADMISSION_REJECTED`, reason `IMMUTABLE_NAME` |
| `NAMESPACE_POLICY_REJECTED` | `phase: POLICY`, `reason` | By reason: `TIER_DEMOTION` is `ADMISSION_REJECTED`; `BOOTSTRAP_NAMESPACE` and `NAMESPACE_TERMINATING` are `FAILED_PRECONDITION`; `NAMESPACE_ALREADY_EXISTS` is `ALREADY_EXISTS`; `NAMESPACE_NOT_FOUND` is `NOT_FOUND` |
| `NAMESPACE_CONFLICT` | `phase: POLICY`, `reason` | `CONFLICT`, reason `RESOURCE_VERSION_CONFLICT` or `SUPERSEDED` |
| `NOT_FOUND` | `phase: POLICY`, `reason` | `NOT_FOUND`, reason `NAMESPACE_NOT_FOUND` |
| `NAMESPACE_DELETION_BLOCKED` | `reasons[]` | `FAILED_PRECONDITION`, one diagnostic per blocker |
| `RESOURCE_VERSION_CONFLICT` (status writes and `complete*Deletion`, all kinds) | `resourceVersion` | `CONFLICT`, reason `RESOURCE_VERSION_CONFLICT`; the current version is in the diagnostic `message` (`current resourceVersion is N`), not a `resourceVersion` key |

The `phase` values `STRUCTURAL` and `POLICY` and the top-level `reason` and
`reasons` keys no longer appear in error extensions. Update client and alert
matchers that used them.

Deletion blockers are returned as one diagnostic each, ordered
`BOOTSTRAP_NAMESPACE`, then `NAMESPACE_NOT_EMPTY`. Successful deletion returns
`TERMINATION_STARTED` or the idempotent no-write result `ALREADY_TERMINATING`,
including when another replica wins a concurrent deletion request.

Controllers accept both the earlier `RESOURCE_VERSION_CONFLICT` code and
`CONFLICT` and treat either as "re-read and retry", so the API and controller
manager can be upgraded in either order for the error-code change alone.

## Metrics

Monitor:

- `gitstore_namespace_validation_rejections_total{code,reason}`
- `gitstore_namespace_validation_duration_seconds{stage}`
- `gitstore_namespace_deletion_rejections_total{reason}`
- `gitstore_namespace_deletion_outcomes_total{outcome}`
- `gitstore_admission_rejections_total{kind,phase}` (manifests rejected by
  admission; emitted for `CategoryTaxonomy/POST_RECEIVE`,
  `Product/PRE_RECEIVE`, and `Product/POST_RECEIVE`, not for Namespace)

The `phase` label on the two `validation` series was replaced: rejections are
now labelled by `code` (keeping `reason`), and duration by `stage`
(`STRUCTURAL` or `POLICY`). Dashboards and alerts that selected on
`phase="STRUCTURAL"` or `phase="POLICY"` must be rewritten, for example
`stage="POLICY"` for latency or `code="ADMISSION_REJECTED"` for rejections.

Alert on a sustained increase in `reason="RESOURCE_VERSION_CONFLICT"` or
`code="CONFLICT"`, `NAMESPACE_TERMINATING`, or internal GraphQL/gRPC errors.
Correlate policy rejections with deployments and client changes; do not treat
expected user rejections as server failures.

## Capacity and saturation

The Repository lifecycle gate (`make capacity TARGET=repository PROFILE=lifecycle
MODE=production`) includes a bounded mutation/push deletion probe against the two
API replicas and active controllers. It fetches the authoring repository to prove
manifest removal, waits for final resource deletion, recreates the same name with
a new UID, and rejects completion carrying the old UID. It requires the existing
`GIT_URL` and an identity authorized for deletion and controller completion.
Local service tests do not replace this deployed evidence.

Run the opt-in production harness:

```bash
make capacity TARGET=namespace PROFILE=validation MODE=production
```

It uses two independent API helper processes, replaces one replica during the
run, and enforces:

- 500 files/request with at most 50 Namespace manifests;
- 10 requests/second at concurrency 20 for at least 30 minutes;
- p95 at most 100 ms and p99 at most 250 ms;
- internal errors below 0.1% and zero incorrect decisions;
- replacement recovery within 30 seconds;
- CPU below 80%, retained-memory growth below 10%, and post-soak goroutines
  within 5% of baseline for each replica process.

Treat p95 above 80 ms, p99 above 200 ms, CPU above 70%, retained-memory growth
above 8%, goroutine drift above 4%, or recovery above 20 seconds as warning
signals. The enforced limits are critical saturation thresholds. Do not raise
concurrency or request-size limits to mask saturation; first inspect parse
latency, policy datastore latency, CPU, garbage collection, and conflict rate.

## Structured logs

Namespace logs use bounded fields:

- `operation`
- `phase`
- `reason` or `reasons`
- `namespace`
- `outcome`
- `blocker_count`
- `conflict`
- `existing`
- `attempts`

Logs must not contain manifest bodies, authorization headers, credentials,
tokens, or internal datastore identifiers. Use the Namespace name, stable
reason, operation, and request ID to correlate a rejected request.

## Troubleshooting

### Structural rejection unexpectedly includes policy details

Confirm the response contains only structural errors. Structural failures,
including same-path `metadata.name` changes, short-circuit policy evaluation.
Check for mixed binaries that predate the ordered validation pipeline.

### Update rejected with `TIER_DEMOTION`

Read the durable Namespace tier. `ORGANIZATION` to `USER` is not allowed.
Submit a non-demoting update or create a migration plan; do not edit the
datastore directly.

### Update rejected with `NAMESPACE_TERMINATING`

The response code is `FAILED_PRECONDITION` with reason `NAMESPACE_TERMINATING`.

The Namespace already has a deletion timestamp. Stop retries that modify its
spec and either let foreground deletion complete or resolve its deletion
blockers.

### Deletion returns both blockers

For a bootstrap Namespace containing repositories, both
`BOOTSTRAP_NAMESPACE` and `NAMESPACE_NOT_EMPTY` are expected. Bootstrap
Namespaces are system-managed and cannot be deleted. For a non-bootstrap
Namespace, remove or transfer all repositories, then retry deletion.

### Deletion remains terminating

1. Query the Namespace and verify the foreground-deletion finalizer and
   deletion timestamp.
2. Verify `HasRepositories` is false after repository transfer/deletion.
3. Check controller health and completion retries.
4. Investigate resource-version conflicts; completion must reload and retry
   rather than remove the finalizer by hand.
5. A repeated user deletion returning `ALREADY_TERMINATING` is healthy and must
   not advance `resourceVersion`.
6. Ordinary repository create/transfer fence cleanup uses a bounded context
   detached from request cancellation. Repair-required writes intentionally
   retain the fence until a quiesced `scylla-projection-repair --confirm` run
   completes projection repair, verifies a clean audit, and clears the retained
   reservation.

### High conflict or latency rate

Confirm all replicas use the same durable datastore and authoritative Git ref,
then inspect policy-read latency and conditional-write conflicts. During a
rollout, keep clients on legacy selections until schema convergence. Run the
focused replica, rollout, authorization, and capacity-threshold tests before
resuming deployment.

When Git HTTP requests fail, correlate their errors with the Git RPC
verification boundary before tuning datastore queries. Signature verification
keeps 32 active slots and at most 256 waiting requests, with an additional
128 MiB aggregate encoded-request budget before any queued body is copied.
Waiters have a one-second budget and are canceled with their caller.
Queue overflow or timeout fails
explicitly without consuming the request nonce. Once verification starts, its
slot stays held through the durable replay reservation, even if the caller
disconnects. No live nonce is evicted and no replay or durability check is skipped.
