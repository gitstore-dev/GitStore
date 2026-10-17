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

GraphQL Namespace and Repository admission can retain the caller's exact commit
when unrelated paths advance the shared authoring branch. The Git service must
prove that the caller's commit is an ancestor and that the selected manifest
remained unchanged throughout the intervening history. Equal bytes at the two
endpoints alone are insufficient: a changed, deleted-and-restored or diverged
path is superseded. Unavailable or budget-exhausted history proofs fail closed.
Admission rechecks this proof around conditional writes, without bypassing
ownership, deletion or resource-version checks.

Within the single Git service, File history reads and Git ref advertisements
wait for ref publication and repository creation or removal, not for a push's
pack upload or validation. Their read guards stay with accepted blocking work
even if the RPC is cancelled.
Complete pushes remain serialized per repository, and publication excludes
snapshot readers until its object promotion and ref transaction finish.

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

Missing Namespace lookups by either name or global ID return the structured
`NOT_FOUND` error code with a `NAMESPACE_NOT_FOUND` failure diagnostic.
The same envelope applies to typed lookups and generic `node` lookups across
resource kinds. Batch `nodes` lookups retain null entries for missing identities
and report errors at their array positions without discarding authorized
results. Storage failures and cancellation are not absence;
clients must not treat arbitrary GraphQL errors as permission to create or as
proof of successful deletion. Denied Namespace reads retain `FORBIDDEN`
without diagnostic details.

Post-receive delta snapshots read at most four manifest blobs concurrently at
each immutable commit. Paths are deduplicated and retain deterministic
processing order; full-tree compatibility reads remain sequential. All active
reads are drained before returning, and an unavailable, denied or cancelled
read rejects the snapshot rather than admitting a partially loaded batch.

Incremental Git fetches compare selected commit trees with a known client
snapshot instead of resending every unchanged blob. A shallow client still
has the complete tree of each commit it advertises; this does not imply that
it has that commit's parent history. Depth boundaries remain unchanged, and
initial clones without a known client snapshot still receive complete trees.
History traversal stops at known commit identities, not their timestamps, so
clock skew cannot discard required backdated commits.
New nested trees and blobs are included; submodule commit targets remain
external. Completed pack-generation logs report `pack_size_bytes` alongside
`duration_ms` so transport timing can be compared with the actual payload.
Ordinary and shallow fetches both enforce the configured platform pack-size
ceiling while encoding, including the pack header and trailing checksum but not
HTTP framing. The exact ceiling is inclusive. Encoding rejects an over-limit
write before appending it, and an oversized pack is returned as an error rather
than a partial stream. Each library entry batch is encoded and released before
requesting the next, rather than collecting every compressed batch first.
Generation also stops when the entries' minimum encoded size cannot fit.
The object count comes from the preceding expansion pass and is verified during
encoding. Missing counted objects, inconsistent counts, and out-of-order batches
reject the response rather than returning a malformed pack.
This does not bound history/tree traversal, object decoding, the size of an
individual entry batch, or the memory required by earlier pack-generation stages.
Sideband framing expands the encoded pack buffer in place rather than retaining
a second complete response copy. Negotiation bytes and framing remain outside
the configured pack-size ceiling. The allocation can still grow to fit that
framing; this is not a hard peak-memory bound.

Dropping a gRPC fetch response signals cancellation to its background pack
work. Queued generation checks the signal before opening the repository;
active generation checks it between history steps, at tree-change callbacks,
between library entry batches and at encoded writes. Tree changes stream
directly into the selected-object set rather than retaining a full change log.
A running lookup, unchanged-entry scan, sort or compression batch can finish
before observing cancellation: this is cooperative cancellation, not a hard
work or deadline bound. The repository read guard stays
with queued and active generation, but is released before response delivery.
A slow reader therefore cannot hold that guard through channel backpressure,
and a disconnected reader stops further chunk delivery.

Scylla File admission can coalesce up to four queued callbacks in one Namespace
under a single exclusive durable parent fence.
Catalog fence entry resolves the Namespace name to its UID. After the existing
queue grants access, a fresh authoritative reservation read checks publication,
deletion intent and state without loading the full Namespace envelope or issuing
a separate publication query. Its conditional reservation also compares that
publication marker: an intervening change loses the old condition and requires
a fresh check before any callback can run.
Staged or missing authoritative rows remain absent, including unpublished rows
with uninitialized fence fields; legacy null markers remain visible. While that
bounded queue still has work, a confirmed release can supply a candidate tuple
for the next conditional acquisition. That fresh transaction must match the
epoch, zero pending writers, deletion state, status and publication marker;
previous observations never authorize a callback themselves. A mismatch discards
the candidate and reloads authoritative state. Errors do not fall back or retry
an ambiguous acquisition. Candidates disappear with the queue entry, and
unconfirmed or counted-fallback releases cannot supply one. File snapshots and
post-write reads remain fresh and unchanged. Reservation
release still drains a confirmed writer even if publication subsequently changes,
without releasing a peer's remaining reservation. Ordinary Namespace API reads
retain their full envelopes and existing error behavior.
Before joining that fence, a callback containing two to four distinct File
creates or updates loads a fresh bounded name snapshot instead of separate
point reads. Its name locators and authoritative rows use at most two partition-local
queries, and each File is authorized against its own returned record. This
does not reuse earlier callback authorization facts. Missing positions remain
absent; storage failures and malformed snapshots fail explicitly, without
falling back to point reads or treating a failure as a create. Single-file and
other request shapes retain point reads. Same-commit no-op candidates still
refresh inside the fence; conflicts still reload and reauthorize the winner,
with unchanged CAS and retry limits.
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

Account for post-receive admission separately from the primary Git
acknowledgement. A visible projection can precede the callback's final ref
check, so neither the acknowledgement nor projection visibility proves callback
success. Join the workload trace ID to the owning `git.catalog.admission` span
and its `post_receive_admission_timing` event, checking the explicit `success`
field and any `admit_resources_error` event. An unset API callback span status
is not evidence of success; missing telemetry cannot establish absent work.
Report late callback failures separately from failed primary pushes, and retain
explicit repair or convergence evidence before claiming recovery.

For Scylla-backed runs, add `CAPACITY_PROMETHEUS_SCYLLA_TARGETS` with the
server metrics endpoints reachable from the scraper, usually port 9180.
The separate `gitstore-scylla-capacity` job retains server CAS latency,
contention and timeout histograms/counters, per-shard reactor utilization,
scheduler runtime/wait counters and transport pressure as five-second series.
Configured nodes require healthy, finite CAS-count and utilization evidence;
missing histories fail export rather than becoming zeros. Raw values retain
the server's native units and labels. These process/server observations
complement API profiles and driver spans; they do not identify the storage
execution time of an individual request.

Before a storage-backed run, check available space on both the Docker host
filesystem and the guest filesystem containing the retained volumes. A sparse
Docker disk can have ample free space inside the guest while its host filesystem
is exhausted. Retain both measurements with the run; guest free space alone
cannot rule out backing-storage failures.

Stop new load on storage I/O errors and retain the affected nodes' logs before
attempting recovery. Scylla can stop CQL, gossip and peer transport after an I/O
error while its process and metrics endpoint remain reachable. Container health
checks can also lag while their configured failure threshold is reached.
Require native transport and the expected membership view on every node,
datastore/watch readiness on both APIs, and credential readiness on both
controllers; a successful metrics scrape is insufficient.
Restore backing-storage headroom first, preserve the retained volumes, and
recover affected nodes in place with unchanged configurations and resource
limits. Verify quorum and application readiness again before resuming load.
Do not weaken consistency, remove data or add a Git-service instance to make
readiness pass. A small successful filesystem probe or restored readiness does
not establish the cause of an earlier stall or satisfy a workload latency gate.

The full observability stack also accepts OTLP/HTTP metrics on the collector's
port 4318 at `/v1/metrics`. Prometheus scrapes those metrics from the collector's
internal port 8889 under `gitstore-otlp-metrics-capacity`; port 8888 exposes only
the collector's own telemetry. Existing application `/metrics` scrapes remain
unchanged, so enabling this route does not move the Go/Rust exporters. Inactive
OTLP series expire from the exporter after five minutes without changing
Prometheus retention. The additional scrape endpoint is not published to the
host.

Trace export retries start at 250 ms and cap their backoff at one second so a
short Tempo rate-limit rejection does not stall both export consumers for
several seconds. The queue remains 128 batches with two consumers, a
three-second request timeout and a 30-second retry budget. This is not a
lossless-delivery guarantee: retain collector enqueue/refusal counters and
Tempo rejection history alongside application admitted/exported/failed
counters. Missing traces or ownership links cannot establish absent work.

`gitstore_git_rpc_verification_phase_seconds` separates signed Git RPC
verification capacity wait, blocking-pool wait, replay-lock wait, envelope
checks, nonce reservation, durable persistence and reservation completion.
Its only label is a fixed phase name. Capacity wait includes successful
acquisitions, rejected or timed-out acquisitions, and waits canceled before
verification starts. It ends before blocking-pool dispatch or durable nonce
work. Failed acquisitions emit a fixed-field `RPC verification capacity timing`
event with the elapsed wait and gRPC error code and message; they have no
verification-dispatch event. Canceled waits contribute a histogram sample but
do not emit a failure event. Do not interpret capacity-wait sample counts as
the number of authorized requests. Older Git-service builds recorded only
successful acquisitions, so their counts are not directly comparable.
These phases describe existing
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
Completed results are published and waiting verifiers notified before that
timing event is reported, so a slow log sink cannot retain the shared durability
gate. Reporting remains synchronous in the owning verification slot.
The fixed `directory_publish` and `directory_report` phases separate result
publication from timing-event reporting; neither replaces or skips a durability
barrier, changes its outcome, or increases the existing admission bounds.
Reconcile these counts with completed reservation events before claiming
physical request reduction; per-request persistence also includes inode fsync
and any wait for its covering barrier.
The `replay_inode` and `replay_directory` phases, also present as fixed fields
on reservation timing events, separate the mandatory inode fsync from the
complete directory wait. A shared barrier's physical duration is contained
inside participating requests' directory waits; do not add these overlapping
durations or compare their unlike sample counts as an end-to-end latency claim.

One already-admitted verifier performs expired-record maintenance at a time.
Clock-file fsync, its directory barrier and at most 256 expired-record unlinks
run outside the shared replay-state lock; other verifiers may persist fresh
nonces without waiting for that maintenance. Expired nonces remain reserved
until their clock floor is durable and each unlink succeeds. Pending nonce
writes are never reclaimed. Maintenance errors fail the owning request,
preserve unremoved records and allow a later request to retry maintenance.
The original replay-capacity bound remains fail-closed, and clock rollback
and envelope expiry are checked again before reservation after maintenance.
The fixed `replay_clock` and `replay_cleanup` phases measure physical
maintenance attempts inside `replay_reserve`; their sample counts are not
request counts. Reservation events include these durations and the bounded
number of completed unlinks. Maintenance remains synchronous within the
existing verification slot, not a background writer or detached task.

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
accepts only that exact receipt as its first fresh ref observation, without a
preliminary ref-only RPC or a redundant middle check. The final fresh ref check
after admission remains mandatory. A changed or deleted ref returns `Aborted`
without partial snapshots; the API then resolves the current tip through its
existing supersession and Namespace-convergence path. A failed proof consumes
the original bounded attempt even if the ref has moved back to the requested
commit. Storage, authorization, cancellation and unsupported-receipt failures
are returned, not replaced with a ref-only or individual-read fallback.
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

Stateless upload-pack negotiation ends an intermediate response with its
`NAK` packet, not an additional flush packet. Shallow boundary announcements
and the final sideband pack retain their own flush delimiters. An extra flush
after an intermediate `NAK` breaks repeated fetches with longer common history
(`expected ACK/NAK, got a flush packet`), even when initial shallow clones and
small fetches succeed. Native metadata/provenance capacity probes perform
these real fetch/rebase/push operations alongside the original sustained mutation
workload; their successful generation, selector and peer replay checks alone
do not establish a passing provenance-under-load gate.
The probe records the exact accepted Git revision and any different revisions
observed while waiting for its watch transition. A successful push alone does
not prove that admission and the durable watch preserved that revision.
Namespace admission preserves the accepted manifest's commit even when unrelated
paths advance the branch before its callback finishes. Both GraphQL writes and
raw Git callbacks use bounded ancestry and unchanged-path proofs rather than
substituting the latest global branch revision. Same-path changes, including
deletion followed by restoration, invalidate that proof; history-service failures
remain errors. A comment-only rewrite advances resource version without changing
generation, while replaying an already-admitted callback does not manufacture a
provenance update from an unrelated commit.

Native Git hook phases require a non-empty ref-update batch. An empty or
fully stale batch does not run validation or admission hooks, and does not
report `refs committed`. A zero-ref completion is not evidence of an accepted
push; check `refs_accepted`, the client's report-status and subsequent resource
provenance together.

The HTTP API decodes Git's `Content-Encoding: gzip` negotiation requests before
forwarding them to the signed upload-pack RPC. Both encoded and expanded bodies
are bounded to 1 MiB. Invalid gzip receives a Git-formatted HTTP 400 response,
oversized negotiation receives 413, and unsupported or chained encodings receive
415, without invoking Git. Plain/identity upload-pack and streamed receive-pack
retain their existing behavior. Apply the API change to each replica; a corrected
Git service alone cannot decode compressed requests forwarded by an older API.

After final Repository removal, its Relay `node` lookup returns `null` without
an error. Authorization returns that confirmed UID absence directly; it does
not reread the UID through an unguarded resolver. Missing parents, storage
failures and access denials remain errors. Typed Repository lookups retain
their explicit `NOT_FOUND` response.

Accepted schema validation can certify that no additional Product or
CategoryTaxonomy deletion snapshot check is needed for the receive. The Git
service then avoids transmitting complete resource trees for Repository-only
deletion, while retaining the schema callback's live authorization, authoring
target and catalog-membership checks. Product or CategoryTaxonomy deletion
still requires the proposed-tree check, including unchanged children that
have not yet been admitted. The certificate is receive-local and cleared
before validation; an older API omitting it retains the conservative full-tree
callout. The gRPC message-size limit is unchanged. Callback failure logs include
a fixed `rpc_code`, distinguishing message-size rejection from unavailable
storage or transport without requiring arbitrary error text.

Namespace completion reconciles projection restoration against a serial read of
the authoritative UID. A peer's committed deletion is idempotent success, while
a live resource-version conflict remains a conflict. The winning completion
also confirms UID-bound cleanup after committing its delete, so a losing peer
cannot leave a restored listing behind. Durable-watch processing repairs such
leftover projections only after confirming that the old authoritative UID is
absent; live Namespaces and same-name replacements are preserved. Storage
uncertainty remains an explicit error rather than permitting a deletion event
to bypass cleanup.

Repository lifecycle capacity probes also exercise Namespace deletion during
the unchanged sustained mutation workload. Both APIs must reject a Namespace
with an ordinary Repository without initiating a cascade. After explicit
ordinary-Repository removal, Product and CategoryTaxonomy fixtures in the system Repository independently block
Namespace deletion on both APIs without cascading or removing its committed
manifest. Explicit catalog removal and same-name recreation verify new UID
assignment and stale-completion rejection while preserving the replacement.
Missing Namespace, Repository, Product or CategoryTaxonomy completion IDs
must fail schema validation on both APIs without affecting replacements.
Product termination admission rebases controller-only resource-version conflicts
within a fixed eight-attempt bound. Each retry reauthorizes the current resource
and repeats the dependent check. Identity, desired-state or provenance changes,
deletion intents, storage failures and exhausted retries remain failures;
another replica's matching termination is idempotent.
After catalog cleanup, controllers must finalize the Namespace and its
empty system Repository within 60 seconds. Recreated names must have new IDs,
and stale completion must preserve the replacement on both APIs. Each
Namespace completion must overlap fresh workload acknowledgements; idle time
and drain cannot count as under-load coverage.

Namespace completion selects the System Repository UID before rechecking the
Namespace incarnation and resource version. A same-name replacement or changed
parent snapshot returns a conflict before any Repository lifecycle write.
Replacement after that check can invalidate only the captured Repository's
UID-bound update; it cannot redirect termination to the replacement's System
Repository. Conflict responses use the current Namespace snapshot rather
than the stale completion snapshot.

A lost manifest-removal response leaves the durable deletion intent available
to another API. Recovery retains the original guarded removal receipt even if
unrelated commits have advanced the authoring branch; it must not create a
second removal commit. Recovery advances the terminating resource version, so
controllers must reread it before completing deletion.

If repository storage was removed but its response was lost, authoritative
metadata and finalizers remain until a retry confirms removal through
idempotent success or a storage-not-found response. Stale completion versions
still conflict before retrying storage removal. Partial name-mapping cleanup
does not change the terminating Repository's UID: its controller can resume
using that UID. Namespace completion stays blocked while the authoritative
System Repository remains, even when its name mapping is already absent.

For a bounded signed-gRPC storage probe on an explicitly owned singleton:

```bash
make test TARGET=git-integration GIT_TEST_OWNED_DEPLOYMENT=1 \
  GIT_TEST_URI=127.0.0.1:50051 GIT_TEST_HMAC_SECRET_FILE=/private/test/grpc-secret
```

The probe provisions only fresh UUID repositories. Its client interceptor
discards one successful native deletion reply before the caller sees it.
An independent connection must recover the original file-removal receipt after
an unrelated commit without moving the branch; a restored file must reject the
stale receipt. Storage-deletion retry must remain idempotent, preserve the
deleted UID's tombstone and leave replacement-UID storage intact. Cleanup uses
an independent bounded context, including when the test context is cancelled.
Signing material is read exactly from the private input file, without trimming
or adding a newline, and never appears in command arguments or logs. The
fixture uses an explicit client-side test authorizer: it does not
establish deployed API RBAC, a network outage, server replacement, the
Scylla-backed deletion saga or sustained-load acceptance.

An unrelated Namespace finalizer blocks completion before the empty System
Repository is marked terminating or its storage is removed. Direct System
Repository completion also honors the Namespace blocker. Unrelated Repository
finalizers likewise preserve the Repository and Namespace; cleanup resumes
only after the responsible owner removes its finalizer.

Repository lifecycle capacity probes can additionally seed finalizer-held,
terminating catalog fixtures with `REPOSITORY_CAPACITY_VERIFY_FINALIZERS=1`.
This requires owned physical-storage verification and an explicit
`CAPACITY_SCYLLA_KEYSPACE` matching both APIs. Exact-UID/resource-version CQL
updates affect only newly created probe records; matching fixture metadata
is restored before normal Git deletion. Both APIs must preserve the held child
and reject parent deletion while offered mutations continue. These simulated
catalog states do not prove real child-deletion initiation. The same flag
separately adds only an unrelated finalizer to fresh ordinary Repository,
Namespace and system Repository probes, then initiates real API/controller
termination. Both APIs must reject held completion and preserve storage while
offered work continues. A terminating ordinary Repository must still block
Namespace deletion. Releasing only the owned marker preserves all other
finalizers and the real deletion timestamp and intent; normal controllers must
then complete cleanup within the unchanged deadline. Alpha/production require
the fixtures; diagnostics must opt in.
Repository and ProductVariant GraphQL metadata preserve the authoritative
`deletionTimestamp`, alongside finalizers, through their shared lookup, Node and
connection converters. A terminating resource must not appear active merely
because its schema metadata omitted the timestamp. Apply the corrected API to
both replicas before collecting cross-replica lifecycle evidence.
Category deletion uses a UID-bound transition and retries up to eight times
when controller status changes cause a version conflict. Retries preserve the
observed desired state and Git provenance, reauthorize the deletion and recheck
blocking children. A newer manifest or same-name replacement is not eligible
for this retry, and storage errors remain explicit.

The tagged Scylla datastore suite exercises both replacement points through
the actual completion service with separate datastore sessions. It verifies
the replacement's version, finalizers, nontermination and name mapping, and
that only the old Repository UID reaches storage deletion. It also checks
unrelated Namespace and System Repository finalizers through both completion
entry points, including cleanup after the finalizer owner releases the blocker.
Git transport is
modeled in this regression; it does not replace running-API fault scenarios
or physical-storage verification. RF3 on a single rack is replication evidence,
not rack-failure availability evidence.

With finalizer verification enabled, the lifecycle probe races Repository
creation on one API against Namespace deletion on the other, then reverses
the API roles. It checks actual acknowledgements, cross-replica projections,
the acknowledged child's Git manifest and UID-derived physical storage.
Both operations cannot win. An unrelated Namespace finalizer preserves the
incarnation for inspection, but does not prevent deletion initiation.
An acknowledged child must block deletion until explicitly removed; a deletion
winner must leave no visible child. Separate attempts after real Namespace
termination verify that late children are rejected on both APIs.
Each race requires continuing workload acknowledgements and normal controller
cleanup before same-name recreation. A rejected API authoring operation may
have committed its manifest before admission; rejection is not reported as
successful creation, and the system Repository's storage must still be removed
by normal Namespace cleanup.

Ordinary Repository probes separately push Product, ProductVariant, Collection,
File and CategoryTaxonomy manifests into fresh Repositories, one kind per UID.
This prevents stale membership from an earlier fixture masking another kind.
Both APIs must reject
Repository deletion while preserving the live Repository, child, committed
manifest and physical storage. The Variant's unresolved Product reference is
verified explicitly, avoiding a second Product masking its membership. The
File has an owned Git asset and no external-provider dependency. Explicit Git
removal must drain each child projection on both APIs before the next fixture
is introduced; per-kind empty-Repository completion and physical cleanup check the shared membership
drain. Each under-load blocker/cleanup interval requires fresh acknowledgements.

The same five kinds separately block Namespace deletion in fresh Namespaces,
one catalog kind per System Repository UID. Both APIs must return
`FAILED_PRECONDITION` with `NAMESPACE_NOT_EMPTY`, preserve Namespace finalizers
and the live System Repository/child, and retain the committed manifest and
storage. Explicit Git child removal must precede Namespace/System Repository
completion and physical cleanup. Fresh scopes prevent earlier membership from
masking another kind. Each complete blocker/cleanup interval must overlap fresh
acknowledgements inside the unchanged offered-load window.

The evidence includes Namespace and Product deletion outcome/rejection counters
and datastore creation/deletion for all five catalog kinds, emptiness,
termination and completion latency alongside controller progress.
Undefined histogram quantiles indicate no sampled work, not zero latency.
These bounded probes do not establish terminating blockers or
unrelated-finalizer coverage, worst-case membership partition health,
or alpha/production acceptance.

Set `REPOSITORY_CAPACITY_VERIFY_STORAGE=1` with the explicitly owned
`REPOSITORY_CAPACITY_PROJECT` to inspect physical Git cleanup read-only.
The lifecycle probe verifies live UID-bound storage before deletion, its
absence and retained tombstone after completion, and replacement storage
preservation after stale completion. Alpha/production require this inspection;
diagnostics without it cannot claim physical cleanup. Inspection never removes
files or changes the singleton Git process.

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
`FileConditionalQueue` measures a writer submission from admission until it is
selected for execution or drained as canceled. Closed or full-queue rejection
is recorded as an error; an already-canceled call that never attempts submission
has no queue sample. `FileConditionalSingle` measures actual one-row conditional
query attempts, including definite-conflict fallback after a batch miss. Both
use the requesting operation's trace context and metric exemplars, including
when the writer completes on another goroutine.
These distinguish waiting behind an earlier write from executing a query; they
do not introduce a gathering delay or change the existing sixteen-row,
payload, cancellation or reservation-draining bounds.
Each shared File reservation has a `datastore.FileMutationCohort` span.
Its physical Namespace reserve, mutation and release phases are direct children.
The span and each selected callback's request span have reciprocal trace links,
with bounded member indexes and a member count of one to four, including
callbacks selected during reservation acquisition. Follow a callback's link to
the exact cohort span, then its at-most-four member links to inspect physical
conditional queries reported in other callbacks' traces. Callback phases keep
their original request context and exemplars. These links establish actual
cohort membership; nearby timestamps alone do not. They do not establish which
individual rows were included in a conditional batch. Sampling or retention can
still leave a linked trace unavailable, and no trace IDs become metric labels.
These preserve the conservative index write and per-resource CAS; they add no
authorization shortcut. Membership samples
include conservative writes, readback repairs and obsolete-entry removals.
File updates still load the full initial authoritative envelope for version
checks and owner-reference handling. A callback updating two to four existing
Files may fetch those envelopes together, but only after holding the Namespace
reservation and waiting for earlier overlapping callbacks. It uses fresh UID
locators and exact authoritative row keys, not caller-provided timestamps,
name-only projections or pre-reservation snapshots.
Bulk File snapshots retain the 250-ID input limit and preserve input ordering,
independent duplicate results, and missing-row positions. Both UID locators and
exact authoritative row reads use at most sixteen keys per physical query.
Locator and authoritative batches from all Namespaces share one eight-reader
ceiling; there is no nested per-Namespace pool. Full, single-Namespace locator
batches start their authoritative reads without waiting for unrelated locator
batches. Partial and mixed-Namespace batches are regrouped as soon as all
locators finish, without waiting for unrelated authoritative reads to finish.
The same workers preserve query packing without extra readers or authoritative
queries. Cancellation joins those readers and
returns an error without exposing partial snapshots. The four-name snapshot
path retains its two physical reads. Neither path scans partitions or retains
authoritative facts across reservations.
Sampled finite GraphQL executions also include `graphql.execute`, covering
response execution and GraphQL data marshalling, but not HTTP encoding or writing.
File `node`/`nodes` preflight uses one `graphql.file_node_authorization` span
around its authoritative snapshots and policy checks. Bulk `nodes` hydration
uses one sibling `graphql.node_resolution` span, including its bounded worker
join and conversion. These are per-field phases, not per-File spans; they export
no documents, variables, identities or error messages and do not create traces
when request tracing is disabled or unsampled. Subscription payloads do not
receive the finite-response phase. Existing HTTP and datastore exemplars join
these spans through the same trace ID. Errors before an operation exists, such
as malformed JSON bodies, keep their normal client-error response without
inventing an execution span.
Compare nested intervals rather than summing overlapping phases. Time remaining
outside `graphql.execute` still includes authentication, parsing, validation,
HTTP encoding/writing and scheduling; it is not automatically database latency.
`FileMutationSnapshotRead` measures that complete bounded read with its request
trace and exemplars. Each UID's snapshot is single-use; subsequent mutations,
nested writes, and contexts used after reservation release read storage again.
Already-selected disjoint callbacks share their declared update UID reads inside
the same reservation: four three-File callbacks use one locator query and one
authoritative query rather than four independent pairs. The existing four-callback,
sixteen-key and eight-reader bounds remain. No gathering timer or extra arrival
barrier is introduced. A callback overlapping an earlier callback's names or
update UIDs keeps its independent read after its predecessor finishes. Nested
callbacks also read independently; shared results never cross reservations.
The first participating reader executes the shared query with the cohort's
existing all-callers cancellation context. Canceling one caller does not cancel
live peers; selected callers still join the read, and shared storage errors
propagate without read retries. Each callback's `FileMutationSnapshotRead` phase
includes its wait for that shared read, not an independent physical query.
The shared query retains at most sixteen result positions until its cohort
drains; this is bounded retention, not a claim of unchanged heap allocation.
The shared reservation retains at most sixteen snapshot slots, including
unfinished reads, and releases them when it closes. Missing rows remain missing;
snapshot read failures propagate to affected updates without point-read retries.
Within the held reservation, fresh Repository parent validation overlaps the
declared File snapshot read. Both reads drain before the callback proceeds;
either error remains attached to that callback's update facts rather than
poisoning another callback's shared snapshot. Successful parent validation uses
the existing reservation-scoped cache, not an authorization or cross-request
cache. Post-write readback, reader limits and lifecycle checks remain unchanged.
The `FileMutationSnapshotRead` phase includes both overlapped reads; subsequent
parent checks reuse the successful held-scope result. Do not add their timings
as if the reads were serial.
Creates, single updates and backends without this optimization keep their
existing path. Callback, conditional-batch, queue and payload bounds, parent
checks, authorization and reservation draining are unchanged.
Their post-write membership read fetches
only namespace, creation timestamp, UID, name and Repository UID from the same
authoritative row key. It remains a fresh storage read, including after a
failed CAS; it does not reuse the input envelope or cache the projection.
Concurrent deletion, changed membership, cancellation and storage errors retain
the existing repair and error behavior without reloading the File payload.
A before-read
includes its locator read, and the complete mutation includes all phases, so
do not add overlapping durations. Compare successful attempt counts with
complete phase count/sum and error deltas before proposing a write optimization.

The Repository workload's `workload-comparison.json` retains failed HTTP attempts
with offer sequence, endpoint index (zero for API A, one for API B), attempt
number, HTTP status and the final mutation outcome. A recovered attempt is not
a failed mutation. With `REPOSITORY_CAPACITY_TRACE_CORRELATION=1`, each Repository
HTTP attempt now also requests sampling through its own explicit W3C parent and
retains that trace ID on failure; the services' configured sampling is unchanged.
Only closed-vocabulary error codes, diagnostic reasons and message categories
are retained, never response messages, paths, request bodies or credentials.
Unknown values become `OTHER`; message categories are hints, not retry decisions.
Each failure retains at most four errors with four reasons each. An operation
keeps its first fifteen and latest failed attempts, up to sixteen; the load
retains at most 256 attempts. Truncation and omitted counts are explicit.
Classification, retry eligibility, deadlines and offered load are unchanged.

The File workload retains each push-worker slot until both APIs have verified
that update's exact sequence, identity and typed reference. The
bounded per-offer `timelines` match queue delay and offered-to-ACK time with
worker occupancy and that same update's verification. Worker occupancy includes
verification; ACK does not. A later offer can wait behind that occupied slot.
These prefix samples do not replace the complete workload counters or gates.
To join a File push to deployed server telemetry, explicitly set
`REPOSITORY_CAPACITY_TRACE_CORRELATION=1` on a File/combined comparison or the
full secret scenario. It creates a sampled W3C parent for each push, sends it
through command-only Git HTTP headers alongside authentication, and records
`traceCorrelation: true` and the bounded timelines' `traceID` values. The flag
defaults off and is rejected for Repository-only comparisons. It requests
sampling but cannot ensure backend retention or correlation by itself: inspect
the matching trace, logs and metric exemplars. It does not create client
queue/provider spans.
For each shared projection-read round, it also creates distinct W3C parents for
the two API requests. Each participating push timeline records that same
round's `projectionReadTraces`: the two `traceIDs` in API endpoint order,
`startedAt`, the complete two-replica wall-clock `duration`, each endpoint's
`requestDurations` including decoding and identity checks, and `failed` for
an interrupted read. This directly identifies which shared reads occupied a
push worker; a read can legitimately appear in multiple push timelines.
Up to thirty-two rounds are retained per timeline; additional rounds increment
`projectionReadTracesOmitted` without stopping verification. Trace evidence
is incomplete when that counter is nonzero or a backend did not retain a
recorded trace. These are explicit workload-to-read associations, not parent
spans or reciprocal server links to each original Git push.
Read counters and retained trace records are captured together, including
when a worker's deadline interrupts an outstanding round.
The offered rates, queue, deadlines and acceptance gates are unchanged.
For client-side attribution, add `REPOSITORY_CAPACITY_CLIENT_TIMING=1` to an
explicitly diagnostic File/combined comparison. It defaults off and is rejected
for Repository-only comparisons, full lifecycle scenarios and alpha/production.
The existing bounded timelines then include `clientGit` records for each local
`add`/`commit` and authenticated `push`: host `startedAt`, command `wall`,
`userCPU` and `systemCPU` (including children accounted by the operating system),
and the main Git Trace2 start/exit timestamps. HTTP pushes additionally retain
the remote-curl helper's command-name and exit timestamps as
`remoteHTTPStartedAt`/`remoteHTTPExitedAt`. These delimit helper execution,
not just network time; the helper includes packing, protocol and server waits.
Compare these timestamps with the correlated server HTTP spans to localize
gaps. Do not call wall-minus-CPU time disk or network time: scheduling and
other waits can contribute, and child CPU can overlap.

New captures additionally include `resourceUsage`: `minorFaults`, `majorFaults`,
`voluntaryContextSwitches` and `involuntaryContextSwitches`, taken from the same
OS process-exit accounting without an additional sampling process. Accounted
children are included; older evidence can omit this object. Zero counters are
explicit, while missing accounting invalidates the diagnostic without changing
the actual Git ACK. Minor faults do not necessarily involve disk I/O, and
context-switch counts alone cannot separate CPU contention from other waits.
Compare them with host/VM CPU and pressure observations rather than interpreting
them as per-command disk latency or an optimal worker-count measurement.

Trace2 events are consumed in memory, with a 16 KiB line bound and 128 KiB
trace budget per command. Only fixed command classes, timestamps, durations,
bounded resource counters and failure classes enter evidence; raw events, argv,
paths and configuration do not. Ordinary output retains its separate 128 KiB bound.
Unsupported, malformed, truncated or incomplete traces invalidate the diagnostic
without changing whether the Git command itself was acknowledged. At most
three commands per push and the existing 2,048 timeline prefix are retained.
Trace2 and CPU accounting add overhead, so compare against an uninstrumented
control rather than treating this diagnostic as an acceptance result.

Its
`projection_visibility` stage includes verifier scheduling and polling as well
as reads. `projection_read` reports the cumulative wall time of completed
two-replica read rounds per acknowledged push; the replicas are queried
concurrently, so this is not the sum of their HTTP durations. `projectionPolls`
counts participation in read rounds per push, not distinct HTTP requests.
In configured fault runs, transport interruptions, client timeouts and HTTP
502/503/504 without server backoff resume polling within the same original
30-second push deadline. Each round rereads both APIs; no successful peer
response is cached. `projectionInterruptedRounds` counts affected rounds per
push, not distinct failed HTTP requests. Those rounds remain included in
`projectionPolls` and read timing. Ordinary runs still fail on the first read
error. Authorization, quota, server backoff, malformed responses and identity
or cardinality failures remain terminal in every mode; a temporary failure on
one peer never masks a terminal error on the other. Write retries, queue and
read-concurrency bounds are unchanged.
These measurements overlap visibility time and must not be added to it.
Do not detach verification merely to shorten queue latency: a subsequent push
can overwrite the same File identities before their accepted update is checked.

Controller-restart comparison runs also retain identified before/after
controller snapshots. Exactly one process must change identity, obtain a fresh
credential and resume reconciliation; the retained peer must remain ready and
advance its reconciliation counter without resetting counters. A retained
peer does not need to renew an unexpired credential. These checks supplement
the controller's existing 60-second recovery limit and do not replace the
File ACK or projection-visibility gates.

For isolated cross-service deletion recovery, run the native Git integration
suite with `DATASTORE=scylla SCYLLA_TEST_ADDR=<host:port>`. It requires
`GIT_TEST_OWNED_DEPLOYMENT=1`, a credential-free `GIT_TEST_URI` and an existing
private `GIT_TEST_HMAC_SECRET_FILE`. Two independent in-process API services and
Scylla sessions finalize only freshly created test resources, using real signed
Git storage removal and recorded manifest-removal receipts. The suite covers a
discarded successful Git reply, partial lookup cleanup, failure before the
authoritative delete, a lost authoritative response and same-name replacement
with stale UID rejection. Scylla uses a new isolated test keyspace; native
storage is cleaned by exact generated UUID, retaining normal tombstones.
This verifies finalizer service logic, not deployed HTTP/RBAC, physical network
loss, process replacement or sustained-load acceptance. The API-side Git registry
uses the existing allow-all test provider; native RPC signatures remain enforced.

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
row CAS without acquiring the Namespace catalog reservation. They reuse the
fresh authoritative row read within that update rather than reading it twice;
the snapshot is not shared across requests. A concurrent status or deletion
transition still causes a version conflict. Deletion intent,
termination, path changes and writes to terminating repositories retain the
exclusive reservation and reload the current row inside it, including status
writes during completion cleanup.
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

A bounded three-node RF3 storage diagnostic populated the unchanged shared
membership schema with 10,245 synthetic rows across five kinds. It retained
initial and post-deletion SSTables and inspected their partition indexes and
data on all three replicas. The largest observed partition occupied 443,979
serialized **uncompressed** bytes across its SSTables, including obsolete
versions; this is not compressed on-disk size or a worst-case storage ceiling.
The corresponding warm-cache query traces from two coordinators demonstrate
why a returned-row limit is not a read-work bound:

| Hot-partition state | Rows processed per participating replica | Cells processed per participating replica | Returned rows |
| --- | ---: | ---: | ---: |
| 2,560 live members | 1 live | 3 live | 1 |
| 1 survivor after 2,559 row deletions | 1 live + 2,559 dead | 3 live + 7,677 dead | 1 |
| Empty after 2,560 row deletions | 2,560 dead | 7,680 dead | 0 |

The skewed fixtures deliberately placed rows in shard 31, exercising all 32
partition queries. RF3 reads used QUORUM and sometimes involved a third replica;
do not multiply by a presumed fixed two-replica cost. These are cache-backed
page-processing observations, not measured disk bytes, SSTable read
amplification, cold-cache latency, sustained churn or production percentiles.
Raw projection writes isolated storage behavior; they do not prove admission,
UID hash distribution or authoritative repair. The datastore regression
separately checks the real emptiness method against a mixed-kind last-shard
fixture before deletion, with a surviving member and after complete deletion.
Worst-case cardinality, field lengths, cold reads and sustained load still need
measurement against explicitly approved projection-specific ceilings.

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

The current Scylla Go driver defaults to token-aware routing, with round-robin
fallback when routing metadata is unavailable. Healthy, warmed-up conditional
queries keep a replica coordinator instead of shuffling replicas. Verify the
driver's actual default and observed coordinators before changing session
policy: older API comments describe round-robin as the default. The isolated
RF3 routing diagnostic compares the default, explicit token-aware and explicit
round-robin policies, plus the default routing policy with socket-write
coalescing disabled, for one-, four- and sixteen-row conditional writes.
It rotates case order and checks every final version/body. It also records
driver-attempt totals and request-stream totals from immediately before the
wire write until the response callback. Streams can include preparation
requests, so their counts need not equal query counts. These timings do not
isolate server execution or justify subtracting unlike latency percentiles.
Disabling coalescing is a diagnostic control, not a production setting.
The diagnostic requires
directly reachable peers, not an address translator that collapses all hosts
onto one contact point. These reduced write-only fixtures establish routing
behavior, not loaded acknowledgement performance or failover availability.

For explicitly correlated server timing in this isolated diagnostic, enable
`GITSTORE_TEST_SCYLLA_SERVER_TRACE=1`. It adds at most six traced conditional
requests after their cases' latency samples: one-, four- and sixteen-row
requests with default or disabled coalescing in the first round only. Each
probe joins its returned trace ID to the exact `system_traces.sessions` row,
with a bounded five-second publication wait, and reports server duration in
microseconds alongside that request's caller, driver and stream durations.
No query text or bound values are exported. The probes are separate from the
untraced latency population and change only isolated fixture rows. They do
not enable production server tracing, establish loaded server/network
causality, or replace the unchanged mixed-workload acceptance gate.

For a loaded, isolated-partition control, the same RF3 test accepts
`GITSTORE_TEST_SCYLLA_SERVER_TRACE_CONTROL_DIR` together with the tracing opt-in.
Use an absolute, private, test-owned directory mounted into the native test
container. This mode selects only the default four-row case, warms its session
and fixture before exclusively writing `ready`, then waits at most 120 seconds
for a regular `start` file containing `start` and a newline. Signal it only
after the owned mixed workload has started. It executes six traced requests
five seconds apart, retaining the existing five-second per-request bound and
checking the final resource versions and bodies. The other routing cases do
not run in this mode. These extra diagnostic writes target a separate fixture
partition, not admission resources: they measure loaded server behavior without
claiming the same contention as an admission partition or qualifying the mixed
workload for acceptance.

Namespace catalog reservations update internal fields on the watched
authoritative row. Those CDC records still advance durable source progress,
but unchanged public images produce no watch event. Byte-identical public
images are compared without JSON decoding or re-encoding; differently encoded
images retain semantic JSON comparison. This avoids local allocation work,
not the underlying reservation writes or server-side CDC cost.

Manifest admission reuses a request-local YAML tree for author-field checks
and kind detection instead of parsing that header twice. Typed resource
binding and Markdown body extraction still use the normal frontmatter
decoder. Malformed input, forbidden author fields and validation precedence
are unchanged; no parsed manifest is cached across requests.

Sampled File conditional writes also record a `scylla.exchange` span enclosing
their native query attempts. At most four native stream slots retain offsets
from the exchange start, with explicit reserved, started, completed, abandoned
and overflow counts. Unfinished streams use `-1` for finish and duration.
Callbacks only retain timestamps; they perform no queries or telemetry export.
For one successful native attempt with exactly one completed stream and no
overflow, discarded slots, retries or abandoned work, the offsets distinguish
time before stream start, stream lifetime and time after stream release.
One stream can carry a conditional
batch of several File rows; stream counts are not File counts. In the Go
driver, start precedes frame construction and write coalescing, while finish
is emitted during stream release in the request caller after response
delivery. The lifetime therefore includes client frame construction, write
queueing and reader/caller scheduling as well as network and server work.
It is neither raw socket timing nor an independent server/Paxos duration.
Prepare traffic and incomplete responses must not be collapsed into one
wire interval.

Sampled bulk File reads use the same bounded exchange tracing for UID and name
locators and authoritative rows. The fixed `db.collection.name` values
`files_by_uid`, `files_by_name` and `files_by_namespace` distinguish these
stages without exporting query text, bound values or resource identities.
Query attempts retain their exact exchange parent, so stream offsets can be
correlated with the corresponding read rather than a concurrent mutation.
Tracing does not add database queries or change the existing input, query-key
or reader limits.

During a Go execution capture, the existing SDK task for a sampled
`scylla.exchange` carries its exact OTLP trace and span IDs. A complete task
with both annotations can identify its caller goroutine and scheduling
transitions without matching unrelated operations by timestamps. Exclude
boundary-crossing tasks and annotations without a nonzero task ID. Caller
scheduling is not the same population as connection-reader scheduling or
remote server execution.
The `otel.trace_id` and `otel.span_id` annotations use hexadecimal IDs;
normalize base64-encoded binary IDs in retained OTLP JSON before comparing
them. Inspect blocked stacks and the goroutines that wake the caller, not
only its runnable delay. A wait in the driver's `writeCoalescer.writeContext`
is client-side write completion; the subsequent `Conn.exec` response wait
still includes reader, network and server work. Neither is independently
server execution time. Check exact stop-the-world overlap rather than
attributing a query to a process-wide GC summary, and do not interpret the
runtime's `Running` state as continuous on-CPU execution.

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
transfer. Depth traversal and both ordinary and shallow pack output respect the
configured pack size. Malformed packet framing and invalid depths fail
explicitly rather than falling back to full-history traversal. A clone failure before offered load is
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
unary `GetFile` and `GetFiles` authentication, Repository read-lock wait,
blocking-pool wait, and payload reads. Its only label, `phase`, has the fixed values
`authentication`, `repository_lock`, `blocking_queue`, and `payload`; no
resource or identity is included. Payload timing includes repository opening,
tree lookup and any requested unchanged-history proof, and records failed
reads too. These server phases do not include client transport or retry delay.
Compare their counts and sums with the full RPC histogram before attributing
slow reads to storage or increasing concurrency.
Sampled RPC spans also carry each completed phase as a numeric
`git.file_read.<phase>.seconds` attribute, using the same observation as the
histogram. Timers retain their originating span across blocking-worker scopes,
including error or cancellation cleanup; a phase that never starts has no
attribute. Use these per-request timings with the RPC's trace and exemplar to
distinguish lock or worker-queue wait from payload work. No new metric label or
request identifier is added.
Namespace watch provenance probes also log a fresh `trace_id` per push attempt
and a fixed `rejection_site`: `client_advertised_ref` means Git rejected the
advertised branch tip before sending its update; `remote_ref_update` means the
server rejected the update. `mixed` or `unclassified` cannot establish either
cause. These diagnostics inspect bounded command output without logging it.
One push command can make multiple Git HTTP requests sharing that trace ID.
Each bounded depth-one retry fetch also logs its own `trace_id` with
`fetch_duration`, followed by the local `prepare_duration`. Compare the fetch's
advertisement and upload-pack spans with the following push's advertisement
to locate the stale-ref window under concurrent authoring; push traces alone
cannot attribute fetch or local preparation time.
Native advertisement worker spans record `git.advertised_head_oid` from the
HEAD already being advertised. Fetch worker spans record the first validated
`git.fetch.want_oid` and `git.fetch.have_oid`, their counts, and `git.fetch.done`.
These identify the requested snapshot; they do not assume HEAD is the only
advertised branch. A signed receive-pack RPC records `git.push.ref_command_count`
and only its first command's validated `git.push.old_oid` and `git.push.new_oid`.
Its existing ref validation attaches `git.push.current_oid` to that same RPC
when the current target is an object ID. `git.push.current_lookup_succeeded=false`
means the lookup failed, not proof of absence; new-branch validation records
`git.push.current_lookup_attempted=false` without adding a lookup. Successful
CommitFile RPCs record `git.write.commit_oid` and, when present,
`git.write.parent_oid`, preserving the originating RPC across blocking work.
Each successful CommitFile also emits one `git_ref_published` log with
`git_commit_oid` and `git_parent_oid` (empty for a root commit). These bounded
log attributes retain publication evidence when the originating trace is not
sampled; a matching trace ID does not imply that its spans were exported.
Join exact IDs through their parent spans and timestamps to distinguish a
stale fetch snapshot from a later ref-update loss and to identify an observed
competing CommitFile writer. Only first-command/request IDs are retained,
without ref names, paths, raw packets, or additional Git/datastore reads.
Flush-only pushes have a zero command count and no submitted IDs; malformed
first-command IDs record `git.push.first_oid_valid=false` without copying
their input. No object ID becomes a metric label.
An upload that explicitly requests `thin-pack` on its first want can encode
one changed tree against a complete tree proven by a known client commit.
At most eight changed-tree pairs are considered; header checks admit only
4 KiB–1 MiB trees, and the selected pair adds at most two bounded tree reads.
The encoder copies a common prefix and suffix and permits at most 4 KiB of
literal bytes. Small, oversized, or widely changed trees retain ordinary
encoding, as do clients without `thin-pack` or a known commit. Storage errors
remain errors rather than being treated as an unavailable optimization.
`git.fetch.thin_pack` records the negotiated capability;
`git.pack.thin_tree_delta` records actual use, with bounded
`git.pack.thin_tree_target_bytes` and `git.pack.thin_tree_delta_bytes` when used.
These attributes describe encoding, not snapshot freshness. Shallow history,
ordinary object identity, cancellation, and the exact pack-byte ceiling remain
unchanged.
Scylla repository-path lookup still validates the forward mapping, current
authoritative repository, and reverse mapping on every call. After resolving
the repository UID, its authoritative and reverse reads overlap; both finish
before the lookup returns. This removes a serial database round trip without
retaining an authorization snapshot or suppressing projection-repair errors.
Git HTTP resolution uses the URL's Namespace name directly for this lookup;
the authorizer still loads the current repository and Namespace before asking
the configured policy provider. Those two authorization reads overlap using
the already-known repository UID and URL Namespace name, with at most one
additional worker per request. Both reads finish before any authorization
decision, including failure and cancellation. The current repository must
still match the URL's Namespace and repository name before either result can
authorize access. An invalidated path can therefore also incur one Namespace
read, but never policy evaluation or native dispatch. Resolution does not
separately hydrate the same Namespace. A repository moved or renamed between
resolution and authorization, or a genuinely missing Namespace, returns 404
before native dispatch. Namespace storage and cancellation errors remain
explicit server failures rather than being treated as absence. Push-policy
reads and the signed-RPC authorization recheck remain separate and fresh.
A client-rejected command can still send a flush-only receive-pack request, so
the presence of a receive-pack span alone does not prove that an update was
submitted. Empty ref-command requests hold a shared repository guard; actual
updates retain exclusive locking. Authentication, authorization, shallow
validation, hooks, and report-status handling remain unchanged.
Successful unary reads also return these four durations in bounded response
metadata. Each API exports them as
`gitstore_git_file_read_server_phase_seconds{phase="...",mode="..."}` and records
`gitstore_git_file_read_phase_reports_total{result="..."}` with the fixed
results `observed`, `missing`, `invalid`, and `rpc_error`. Missing or malformed
telemetry is counted explicitly without changing the read's content or error.
Compare phase samples only with successful RPCs; failed reads do not export
partial server timings. With `git_service.metrics.uri` configured,
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
The owned Go diagnostic captures each API's three-second execution trace first,
before its CPU profile and heap snapshots, to observe load-start scheduling
rather than a later steady window. Its bounded manifest records every attempted
capture, including failures, with UTC command/capture/write boundaries,
elapsed duration and retained bytes. Check those boundaries and the binary
trace before attributing a burst; they are not exact sample timestamps.
Capture failures invalidate the diagnostic, and profiler overhead prevents
using its latency results as unprofiled acceptance.

With `EVIDENCE_DIR` set to the closed diagnostic's evidence directory, inspect
the retained captures locally:

```bash
go tool trace -http=127.0.0.1:8089 "$EVIDENCE_DIR/profiles/api-a-execution.trace"
go tool pprof -http=127.0.0.1:8088 "$EVIDENCE_DIR/profiles/api-a-cpu.pprof"
```

Use the execution viewer's user tasks to find `scylla.exchange` and its trace
and span annotations; repeat with `api-b` for the peer API. The viewers read
retained files and do not require a running profiling listener. A trace that
covers all burst offers may still end before their queued work completes.
The subsequent CPU capture is a different time window, not a CPU profile of
that initial execution trace. Block and mutex profiles aggregate overlapping
goroutine waits, so their totals must not be treated as wall-clock latency.

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

Provisioning retries for existing storage return `AlreadyExists`; RPC histogram
completion counts therefore do not count newly created repositories. These
checks still verify the signed request, authorization, durable replay record
and deletion tombstone. They use the retained shared lifecycle gate without
excluding immutable readers. Actual creation rechecks absence and the tombstone
under the exclusive lifecycle and publication gates; deletion retains those
exclusive gates too. No cross-request existence cache or skipped storage check
is introduced. Compare read-lock wait with provisioning traffic before attributing
that wait to object decoding or repository opening.

A matched, unprofiled mixed-load diagnostic compared the exclusive and shared
existing-storage checks with 1,400 File offers, 1,400 Repository offers and the
same 32 File / 20 Repository workers. Both completed every ACK and all 2,800
attached two-API checks without failures, drops, missed schedules or projection
loss. Queue-inclusive File ACK p95 was 2.988s with the exclusive check and 3.088s
with the shared check: neither met the unchanged 2s target. Across equal
260-second exports and 5,738 reported reads per arm, aggregate read-lock wait
fell from 87.254s to 53.534s. Those windows also contain preparation and background
work, and the durations are request wall time, not CPU time. This single pair
does not establish repeatability or a causal regression, but it provides no ACK
improvement to promote. Keep end-to-end acceptance separate from a better
internal phase measurement.

Plural File node reads hydrate at most 250 IDs per backend call. UID locators
use at most eight concurrent queries containing at most sixteen distinct partition keys each;
the API never sends a 250-key cross-partition `IN` query. This reduces repeated
client/coordinator request setup, not the number of locator partitions read.
Canonical records are fetched by exact `(creation_timestamp, uid)` tuples within
each Namespace, with at most eight Namespace reads active. All locator reads
and authoritative reads share one pool of at most eight readers. A full
sixteen-key, single-Namespace locator batch can proceed directly to its
authoritative read. Remaining locator results are grouped after all locators
finish, without waiting for unrelated full-batch authoritative reads.
Neither lookup uses
filtering or full-partition scans. Duplicate positions receive
independent copies; stale locators remain absent and storage failures fail the
whole read. Authorization checks each record's stored owner and reuses that exact
snapshot for hydration, without a second read or cross-request cache. Existing
public request limits are unchanged; singular node reads remain point reads.
The native hydration diagnostic checks the physical query and returned-row
counts and maximum keys per query at the maximum request size. A 250-File,
single-Namespace request uses sixteen locator and sixteen authoritative queries,
returning 500 rows in total. The diagnostic compares against bounded point reads and
preserves cross-replica update, deletion/recreation and stale-locator readback.
Its isolated timings do not establish a combined-load acknowledgement benefit.

The native UID locator diagnostic compares batches of one, four, eight and
sixteen keys against the same eight-, sixteen- and sixty-four-key fixtures,
using at most eight readers. It rotates case order across six rounds and checks
every returned locator, the physical request count, completed driver streams
and zero retries. Smaller batches issue more requests; compare both elapsed
time and amplification before changing production packing. Optional
`GITSTORE_TEST_SCYLLA_SERVER_TRACE=1` captures two explicitly requested server
traces, for one- and sixteen-key reads. Isolated locator timings and these two
server traces do not identify the cause of a mixed-workload tail.

The same diagnostic includes a read-coalescing comparison for locator reads and
exact Namespace tuple reads with a verified 2 KiB body. It holds sixteen-key
packing, the eight-reader maximum, consistency and fixture identities constant,
comparing separate sessions with the driver's default 200-microsecond write
coalescing and with coalescing disabled. Six counterbalanced rounds retain all
64 read-wave durations per case, exact query/row counts and completed streams.
Each sixty-four-key wave uses four physical queries; its sample percentiles are
not individual-query or mixed-workload acknowledgement percentiles.
The API datastore disables this client-side flush timer for latency-sensitive
admission reads and writes. This does not change query packing, conditional
write semantics, consistency, deadlines or retry behavior. It may increase
socket write frequency, so evaluate CPU, allocation, queue and database load
alongside acknowledgement latency in the original combined workload; the
isolated comparison alone does not establish capacity acceptance.

Seeded File discovery retries only explicit `NOT_FOUND` errors with a
`FILE_NOT_FOUND` failure diagnostic at a requested alias whose value is null.
Other GraphQL failures, malformed responses and mismatched projections fail
immediately. This bounded visibility retry does not apply to mutations.

Typed Product, ProductVariant, Category and Collection reads preserve the same
absence-versus-failure distinction as generic Node reads, for both ID and
namespace/name selectors. Confirmed datastore absence at the resolver returns
null. Cancellation, deadline and storage failures return an explicit sanitized
GraphQL error rather than a successful-looking null; their underlying cause
remains available to server-side error classification. This does not bypass
authorization or change intentional concealment decisions or existing
authorization-layer not-found responses.

Category.products query diagnostics use acknowledged sparse, evenly distributed
and single-shard-hot Product fixtures. They compare every forward/backward
window, including timestamp ties, against an independent membership oracle
before and after category moves, same-name/new-UID replacement and deletion.
Stale membership retries cannot restore deleted UIDs or superseded category
membership. The evidence separates the 32 shard reads, readiness-marker read,
projection rows, UID lookups and authoritative hydration instead of treating a
page as one datastore request. Clean pages hydrate only their live page and
lookahead; empty membership performs no Product hydration. These isolated
two-client checks share one translated coordinator and do not establish live
API replica correctness, sustained-load latency, rack-failure availability or
approved storage ceilings.
The Category hierarchy load profile additionally seeds acknowledged leaf,
distributed and anchor Product fixtures, then sustains owned Product updates
and category reassignments while the subtree moves. An independent ledger
checks exact forward/backward windows at the leaf, root, anchor and an empty
control root on both APIs, including current Ready generations and resolved
category paths. Its summary must account for every declared Product update
and reassignment, with no failed or missed schedules. Page and convergence
latencies are reported separately, alongside poll counts and complete oracle
verification time. Independent first-page and phase reads are paired across
the two APIs, with at most two concurrent requests; each window's continuation
pages remain cursor-ordered. An incomplete poll stops after its pending pair.
Acceptance still requires every connection and a stable root phase within the
original absolute deadline, including prefetched responses.
Integrated faults wait for the first scheduled load callback after fixture
seeding and setup verification. Their delay is relative to the native scenario
start; a runner-start timer can otherwise restart a replica during a long seed
phase without exercising any sustained load. The runner retains a run-matched
window and millisecond injection timestamps, and rejects injections that begin
or finish outside it. Exact Compose ownership is checked when a project is
selected. Timing alone does not prove recovery or replica correctness.
The predicted maximum FNV-1a shard
membership is a live-row estimate, not physical partition-size or read-work
evidence. Use the API/Scylla observations and approved storage ceilings for
those decisions rather than treating this ledger or HTTP latency as a proxy.
The application datastore wrapper forwards both membership replacement and
connection reads, recording `ReplaceCategoryProductMembership` and
`ListCategoryProducts` operation durations, failures and sampled spans. With
Prometheus observability enabled, the Category harness exports both APIs'
five-second histories, mutation outcomes and p95/p99 estimates, Product and
membership operation samples/errors, and shared resource-watch phase estimates.
Configured controller histories retain recovery and reconciliation alongside
these observations. The operation histograms do not count physical shard
queries or hydrated rows; shared watch estimates are not Category-only latency.
Product controller list and watch reads retain the complete resolved category
path. Omitting it makes unchanged Products appear unresolved and causes repeated
status writes and membership replacements after load has stopped. Healthy
resolved Products settle without writes; a changed category path still triggers
status and membership reconciliation. Category path changes request durable,
indexed fan-out to directly referencing Products even when the Category
generation has not changed, as during a descendant cascade. The dependency
version excludes child/product counts and unrelated conditions to avoid
reconciling every Product after count-only updates. Invalid paths fail
explicitly rather than advancing the dependency checkpoint. Check idle operation
rates as well as loaded latency when diagnosing controller contention.
Product desired-state admission retries a controller status-write conflict at
most eight times. Each retry reloads the same UID and repeats admission policy
validation. Identity, provenance, desired state and creation metadata must
remain unchanged; a competing desired-state commit, termination, storage
failure or cancellation is not silently rebased. Retries do not turn missed
load schedules or incomplete acknowledgements into successful evidence.
The durable Category.products readiness marker is checked on every connection
read, including already-warmed clients. A false or missing marker blocks the
read with projection unreadiness. Storage errors remain explicit failures,
not confirmed unreadiness or successful empty pages; cancellation likewise
retains its cause. Reads recover after the marker and storage path are restored.

Category.products stale-projection scanning is bounded to 256 shard-projection
fetches and 4,096 UID hydration attempts. This is not a 256-total-query bound:
each hydration may require a UID lookup plus an authoritative Product read.
Exhausting either limit returns projection unreadiness without a truncated
page, including when a live member was already hydrated. Native diagnostics
exercise missing UIDs and stale creation keys in a hot partition, check exact
physical-query and fetched-row counts, and require unaffected categories to
remain readable. Duplicate live membership encountered while merging shards
also returns projection unreadiness, never duplicate members or a false
next-page flag. Duplicate tracking retains only the live page and lookahead;
stale creation keys do not consume that tracking state.
Targeted fixture correction and membership replay must restore
exact forward/backward results. These checks do not approve storage ceilings
or substitute for sustained deployed-load and coordinated repair evidence.

Product, ProductVariant, CategoryTaxonomy, Collection and File deletion apply
their authoritative resource-version CAS before touching lookup or relation
projections. This includes ordinary datastore deletion and Category completion:
a version change committed by another writer after the initial read cannot
temporarily delete and restore the surviving resource's lookup rows. They use
the shared bounded projection recovery path: cleanup failures do not disappear
or prevent attempts to clean the remaining projections, and committed deletion
is never compensated by restoring a stale resource. A stale version leaves
lookup rows and membership unchanged. Partial cleanup returns a repair-required
error and retains the conservative Namespace fence, blocking admission until quiescent
projection repair verifies cleanup and releases it. Repository membership is
cleared only after confirmed authoritative absence; a same-name replacement
remains a blocker, and an old repair plan cannot delete its new-UID reservation.

ServiceAccount deletion reports partial projection cleanup as a failure even
when its authoritative enrollment has already been removed. Listing cleanup
failure does not skip the conditional old-UID routing release; all cleanup
causes remain available to server-side classification. Deleted enrollments
cannot authenticate through leftover lookup or listing rows. The bounded
projection audit and repair remove those old-UID rows without deleting a
same-name replacement. Inspect and repair the old identity rather than blindly
repeating a name-based delete after re-enrollment.
The API cancels that old UID's local connections after successful deletion or,
on a cleanup error, only after a fresh UID lookup confirms absence. Conflict or
unavailable confirmation does not authorize connection cancellation, and the
replacement UID is never selected by name for revocation. This local registry
does not provide cross-replica cancellation of already-open connections.
GraphQL deletion failures are sanitized while retaining their underlying causes.

Companion Git-push capacity evidence retains the complete declared schedule,
not just delivered timer events. Missed or canceled offers cannot disappear
behind a successful push count. Acknowledgement latency includes schedule
lateness; worker completion and cancellation cleanup must satisfy the existing
30-second drain bound. Evidence is written only after accepted workers finish,
and canceled, incomplete or over-bound schedules cannot pass the verifier.

Namespace admission confirmations require HTTP success, an error-free GraphQL
data envelope and a nonempty Namespace ID. Partial data returned with errors
does not prove acknowledgement or cross-replica visibility. Every visibility
read must return the same acknowledged Namespace ID; a same-name resource with
a different ID on another replica is not convergence. The verifier
balances offered iterations with recorded outcomes, checks that admissions
equal successful outcomes and requires one visibility sample per admission.
Thrown requests, missing observations, incomplete check counts and dropped
iterations fail verification; the existing HTTP error-rate and inclusive
visibility bounds remain unchanged.
Only a single bounded JSON summary in a non-symlink regular file is accepted.
The verifier also requires complete declared sustained and burst delivery,
independently per scenario and in aggregate. Planned offers are derived with
exact integer arithmetic from k6's effective normalized duration and arrival
rate, including compound and fractional durations. Missing burst metrics,
partial execution segments, late or dropped offers and incomplete schedules
fail even when every recorded mutation succeeds. Lateness uses native scenario
progress, not wall-clock millisecond rounding. At most one k6 stop-timer callback
at each exclusive endpoint is recorded separately without offering HTTP work.
This establishes SDK schedule accounting, not successful sustained delivery
against deployed API replicas or capacity acceptance.

Category capacity acknowledgements require successful, error-free responses
carrying the expected name, title and commit revision, including after conflict
confirmation. Batch seeding verifies every response before counting admitted
nodes. Convergence cannot use partial error data or a different category's path;
a valid zero-millisecond sample remains distinct from timeout. Filtered reads
reject malformed or duplicate identities and incomplete pagination metadata.
Product capacity peer reads likewise require the acknowledged UID. Every
offered Product iteration must finish its admission and deletion-race checks,
with the complete check count and no dropped iterations. Omitted and empty
GraphQL error arrays are both valid; retry classification uses uniform error
codes, never message text. Existing workload and latency limits are unchanged.

Category hierarchy capacity evidence likewise accounts for the full declared
mutation and filtered-read schedules. Planned counts must equal each rate times
the declared duration, every offer must have an outcome, and failed reads,
dropped iterations or offers at least one arrival interval late fail verification.
Filtered-read latency samples must cover every completed read. Offers at or
after the load endpoint cannot start a request, and exceptions cannot disappear
from the offered count. The load window comes from the mutation scenario's
actual start and declared duration; cascade convergence during teardown cannot
inflate overlap with companion pushes. Acceptance retains the minimum
20 mutations/s for 300 seconds, the existing latency thresholds and worker
bounds. This accounting supports a single k6 process, not segmented runs.
The single exclusive-endpoint callback that k6 may deliver while its stop timer
races is counted separately and sends no request. It cannot replace a planned
offer or hide a late scheduled request; more than one such callback fails
verification.
Shared k6 integer settings reject truncated or inexact values while preserving
existing defaults and leading-zero decimal values.

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
Within an active fence scope, queued File projection readbacks can share one
fresh, partition-local query for up to sixteen exact clustering keys. The reader
drains only requests already queued; it adds no collection timer or arrival
barrier, and never caches results for later reads. A missing row remains absent,
and query or identity errors remain explicit. Canceling one caller does not
cancel a live peer's shared query; all accepted reads join physical work before
the fence can be released. Reads outside an active scope retain point queries.
`FileProjectionReadQueue` measures each caller's queue residence separately
from `FileProjectionReadBatch1` through `FileProjectionReadBatch16`, which
measure each executed shared read once by its exact requested key count.
These read durations include driver work and result validation, not just
server execution. `NamespaceFenceReserveRead` and `NamespaceFenceReserveCAS`
separate fresh fence reads from conditional acquisition. Retry delays appear
as `NamespaceFenceReserveBusyWait` when another reservation was observed or
`NamespaceFenceReserveConflictWait` when a conditional acquisition did not
apply. These fixed operation names use the existing datastore histograms,
sampled spans and exemplars; they introduce no resource-identity labels.
With an active sampled local parent, `scylla.query` and `scylla.batch` client
spans report individual driver execution attempts. They include the
driver-selected host address and coordinator ID, zero-based retry count, and
returned-row or batch-statement count. Host metadata describes the driver's
selected host, not independent server-side proof of routing through an address
translator or proxy. `gitstore.db.attempt.duration_ns` retains the driver's
measured elapsed time separately from exported wall-clock span timestamps.
These client-observed attempts do not isolate server execution or connection
acquisition and do not cover the whole enclosing datastore operation. A
conditional statement that did not apply is not necessarily a driver error.
Query text, bound values and driver error messages are not exported; errors
retain their type and an error span status. Unsampled, remote-only and completed
parents do not generate these spans, so subscription polling does not extend a
completed request trace. This instrumentation does not enable server-side CQL
tracing or change retry, consistency, deadline, mutation or resource limits.
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
