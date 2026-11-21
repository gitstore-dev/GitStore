# RBAC Snapshot and Provider Contract

## Model v1 and provider independence

The input model is the immutable RBAC snapshot described in
[data-model.md](../data-model.md), not Rego source. One snapshot is one
tier; a request pins a cluster snapshot and, when needed, one Namespace UID
snapshot. The model includes resolved role links, lifecycle suppression,
built-ins and finite catalog identity. Translation never queries a UserDir
or datastore during ordinary authorization.

`AuthZProvider.Authorize` remains the request-decision boundary.
Add optional bounded capabilities for authoritative pinned-model evaluation,
conservative coverage and snapshot/readiness diagnostics; existing providers
must continue to work without implementing them. Dynamic-write validation
must fail or require explicit privileges when a capability is unavailable,
not guess a proof.

The OPA provider compiles only fixed release policy and exposes named
decision/coverage queries. Wildcard expansion, subjects, conditions,
ownership precedence, trusted masters and invalid-action checks must match
the spec 064 provider-neutral decision fixtures. Known snapshot
unavailability is checked before any trusted-identity allow fast path.

### Verified fixed-engine boundary

`internal/auth/provider/opa` prepares the embedded decision query once,
then evaluates private immutable AST translations of selected tiers.
Translation indexes subjects to binding UIDs and roles by UID/action;
the decision path performs no datastore, UserDir or network access.
Shared static-provider helpers validate principal subjects and transfer
targets without changing static authorization semantics.

The unchanged 55-case neutral fixture passes, alongside focused tests for
trusted profile usernames, terminating roles/bindings/namespaces, unresolved
and recreated identities, malformed/undefined/error output, cancellation,
deterministic translation and concurrent pinned evaluations. Static fixture
role identifiers are lowercased only when adapting them to Git resource
names; rules, requests and expected decisions remain unchanged. Compilation
rejects network, clock, random, UUID-generation and runtime-inspection builtins.
Translation reserves a conservative serialized expansion bound before adding
action/index entries. Publication and authoritative pins share the provider's
aggregate translated-data budget; retired publications retain their reservation
until their final reader closes. Rebuilds reserve scratch before scanning.
Authoritative validation reserves a full tier allowance for each selected
tier before source I/O, retains it for the capture's lifetime and reclaims it
on failure, closure or cancellation. Saturated captures do not begin scans.
These accounting tests do not establish measured heap/RSS ceilings.

Verification:

```sh
GOWORK=off go -C gitstore-api test -race ./internal/auth/... ./internal/rbac ./internal/app ./internal/config -count=1
make lint
```

Authoritative snapshot acquisition and trusted release composition are covered
by the foundation join in [admission-storage.md](admission-storage.md).
The concrete origin reader, publication budget and shared-journal manager now
have focused race coverage. An unregistered application runtime composes the
real snapshot reader, provider, manager and readiness handler. Its memdb test
uses constructed origin and frontier records, not deployed delivery.
Production provider construction, bootstrap and cutover remain pending:
selecting `opa` still fails startup rather than serving incomplete state.

Optional capabilities now let the selected provider pin authoritative
model-v1 tier DTOs, expose conservative coverage, and report no-I/O readiness
metadata without changing `AuthZProvider.Authorize`. Unsupported providers
return unsupported, not guessed coverage or an ordinary-cache fallback.
Registry reload does not switch an already-pinned evaluation to a different
provider. The decision logger preserves pinning, closure, coverage and readiness
capabilities and logs decisions from the pinned evaluator itself.

## Data publication

Subject and group indexes restrict candidate bindings to the relevant
cluster/namespace tiers. Data, revision/digests and readiness are one
immutable publication unit. No decision may log a later snapshot identity
than the data actually evaluated.

Use prepared Rego policy with immutable selected data. If OPA in-memory
storage is used instead, every query pins the read transaction; updates and
logged revisions must belong to that same transaction. Do not rebuild policy
source for each resource update or compile per request.

### Verified journal transport boundary

The four RBAC kinds share the existing journal and cursor space. Memdb
publishes each committed candidate's resource changes followed by its tier
revision without interleaving another candidate; rejected writes and exact
replays publish nothing. Final deletion retains envelope identity and UID,
not the removed spec. Selectors extract labels from nested envelope metadata.

Scylla registers one discriminated `RBAC` source for
`rbac_resources_by_scope`, not four readers or another CDC daemon. It
validates complete same-tier pre/postimage frames, uses stable per-entry
deduplication keys, and checkpoints through the existing shared sequencer
only after publishing the frame. External receives permit at most 256 changed
resources; committed catch-up can publish a complete bounded tier plus one
tier revision in a frame. Other catalog sources keep their existing
frame bounds. A delta without full images is a discontinuity, not an empty
successful poll. Same-revision repair-only sentinel frames advance source
progress without duplicating resource changes.

Raw journal readers receive private `RBACTier` progress with tier UID and
revision. Public subscribers receive only payload-free opaque BOOKMARKs at
those cursor positions. Resource namespace names are not sufficient to attach
events to a recreated namespace: snapshot consumers must validate the tier UID.
Tier revisions are not quiet-source heartbeat/freshness evidence.

Verification includes concurrent memdb publication, selector/deletion identity,
private-progress bookmark projection, reordered/corrupt/replayed frame tests,
and `TestRBACNativeJournalCDC` against a task-owned Scylla 2026.1 fixture. The
native contract delivers all four kinds, status changes written while the
reader is stopped, a 257-resource catch-up, final deletion and tier
progress after restarting the same source. The complete native datastore
gate, shared journal/datastore/app race suites and repository lint pass:

```sh
make test TARGET=datastore
GOFLAGS=-p=1 make test TARGET=datastore DATASTORE=scylla SCYLLA_TEST_ADDR=<owned-address>
go -C gitstore-api test -race ./internal/watchjournal ./internal/datastore/... ./tests/contract/datastore ./internal/app -count=1
make lint
```

This verifies storage-to-journal transport on a one-shard, 1 GiB fixture,
not serving authorization, snapshot/watch bootstrap, cross-replica propagation,
rolling upgrades or sustained-load acceptance. Those runtime-manager and
deployed gates remain pending; dynamic writes and production OPA activation
remain disabled.

### Verified unregistered runtime boundary

The manager keeps the shared journal live while at most four workers scan,
retains newer dirty generations, and installs only complete publications caught
up to the post-scan barrier. Namespace demand is asynchronous, bounded and
coalesced with the 1,024-entry dirty queue. Configured lower worker, queue and
timeout bounds are honored by the manager. The unregistered composition applies
the configured source-staleness, retry/deadline, serialized-tier, aggregate
translated-data and per-rebuild reservation limits. Authoritative capture shares
the runtime scratch budget; at least two tier reservations fit even with one
configured worker. Lower serialized limits are not measured physical-allocation
ceilings.

Readiness requires separate epoch-bound, fully published read frontiers from
Namespace, Repository, RBAC and ServiceAccount sources. Each frontier is the
minimum across that source's active streams. Cached checks expire synchronously
before trusted fast paths, even if manager polling is delayed. Native Scylla
coverage verifies persistence of the RBAC-specific frontier. Memdb preserves
the same source-qualified checkpoint keys.

Progress-only idle checkpoint batches use a 500 ms coalescing interval between
successful flushes. Resource changes retain the 25 ms flush cadence and retiring streams
drain immediately. Event publication, frontier publication, stream checkpoints
and source progress keep their existing ordering and lease fencing; failed
publication never advances source freshness.

Optional canonical-origin checks reuse the same bounded workers and verify live
Namespace/repository/ref identity without rereading unchanged manifests. A changed
or unavailable origin invalidates the tier and schedules complete repair/resync.
Cached origin proofs expire before ordinary decisions and trusted fast paths if
background checking stalls. Generation changes prevent an in-flight check from
renewing a superseded publication. Scheduling alternates queued rebuilds and
oldest-due origin checks, so a sustained dirty backlog cannot indefinitely defer
the latter and due checks cannot monopolize rebuild dispatch. A regression with
one worker and 64 failing namespace rebuilds proves real cluster-origin renewals
continue without extending proof lifetime; blocked I/O and overload still expire
proofs and fail closed. The unregistered runtime can compose the
canonical repair callback before rebuilding; production composition remains gated.

The runtime shares a fixed 128-entry FIFO cache of completely verified immutable
Git tree digests. Its key includes Namespace UID/lifecycle, canonical repository,
ref, resolved commit and scan-byte limit, but excludes mutable Namespace
resourceVersion. Every hit rereads current live facts and returns the current
resourceVersion. New commits and scopes undergo the complete bounded scan;
failed scans do not populate the cache. This is not an authorization cache or a
reuse of source-freshness proofs. Regression coverage checks changed versions,
refs, malformed manifests, UID replacement and Namespace deletion.

Focused tests cover lost epochs/cursors, quiet sources, failed complete resync,
partial snapshots, saturation, cancellation, namespace termination/name reuse,
stale in-flight UID scans and canonical Repository changes. OPA watch guards
reauthorize before events and every second while idle or backpressured, with
bounded concurrent checks and refreshed directory membership/lifecycle facts.
ServiceAccount watches also re-read the account lifecycle and immutable UID,
even when the selected UserDir does not support ServiceAccounts.
Static-provider streams are unchanged. Decision logs record only the exact
evaluated vector; SDK labels remain finite.
Provider reload rejects transitions into or out of OPA; controlled restart with
write freeze and traffic drain is required. Static same-provider semantics remain
unchanged.

```sh
go -C gitstore-api test -race ./internal/rbac ./internal/auth/... ./internal/watchjournal ./internal/datastore/memdb ./internal/admin/graph/resolver ./internal/middleware/security ./internal/app -count=1
make lint
```

These are constructed runtime and native-source proofs, not two-API delivery,
production readiness, physical-memory, rolling-upgrade or capacity acceptance.
The corresponding deployed gates remain open.

## Readiness state machine

```text
Uninitialized -> Loading -> CatchingUp -> Ready
                                ^         |
                                |         v
                              Resync <- Unavailable
```

- Cluster Uninitialized/Loading/CatchingUp: API authorization readiness is
  false. Operational health and bounded internal bootstrap may run.
- Namespace not loaded: that namespace's authorization remains unavailable;
  it cannot be treated as an empty grant set or inferred nonexistent.
- Ready requires a complete stable snapshot, valid catalog/built-in digests,
  verified gap-free watch position and fresh source progress.
- A quiet tier does not become stale because no role changed. Measure source
  heartbeat/progress and journal lag, not time since the last resource event.
- Known lag beyond 5 s, lost cursor/epoch, overflow, rebuild error, partial
  state or namespace UID mismatch invalidates the affected tier.
- Recovery captures a new complete snapshot and watch position. Reconnecting
  a socket alone does not restore readiness.

Global readiness reflects the cluster prerequisite. Per-tier availability
is tracked separately so one failed namespace does not falsely make another
ready or require unlimited global scans. Requests involving an unavailable
tier deny with a stable retryable authorization-unavailable error.

## Bounds

Defaults: four global rebuild workers, one rebuild per tier, bounded
1,024-entry coalesced dirty-tier queue, 1,000-resource pages and 64 MiB
serialized tier cap. Exceeding capacity invalidates affected tier readiness
and schedules bounded full resync, never unbounded buffering.

Use an aggregate translated-data budget of 512 MiB per API and a 64 MiB
bounded scan/translation working budget per rebuild. Reserve budget before
allocating; account for replacement plus pinned readers, not only the new
snapshot. Reject or fail closed rather than exceed the budget. The
acceptance dataset must fit these ceilings; if measurement disproves them,
review the declared envelope rather than weaken it silently.

Eval concurrency uses the existing bounded request admission controls and
request context/deadline. Validation timeout is 30 s; snapshot read retries
are at most three per attempt. Controller retries reuse durable backoff,
not unbounded immediate loops.

## Authoritative validation and access reviews

Capture each touched tier from current Git/projection state at validation
start; pin all data and identities for that attempt. Never silently reuse
the ordinary watch cache. Each tier capture is current at its own read;
the vector is not a global transaction.

A revocation after capture may commit before the authorized operation.
This explicit chosen race is distinct from allowing on a known-stale cache.
New Git candidates and retries after conflict capture new snapshots.

Coverage proof only accepts unconditional covering authority at sufficient
scope with resource-name supersets for every finite expanded action, or
explicit bind/escalate. Candidate conditional restrictions may narrow an
unconditionally covered grant; caller-dependent predicates never prove
coverage for another subject.

## Subscriptions

Existing long-lived subscriptions pin identity, not perpetual permission.
RBAC cluster/tier changes, namespace termination and UserDir membership or
lifecycle changes trigger reauthorization within the declared bounds.
On unavailable snapshots, suspend/close through existing retryable protocol
behavior; never deliver events using a revoked grant while waiting for resync.
Bound subscription recheck fan-out and expose saturation.

## Observability

Reuse bounded `DecisionLogger`, exact policy identity, optional registry
snapshot metadata and shared SDK instrumentation. Decision metadata must
include the evaluated tier revision vector, catalog and built-in identities;
capture it with the decision rather than inspecting current provider state
afterward. Preserve existing latency/action/outcome telemetry.

Add SDK counters/histograms for snapshot rebuilds, source progress age,
fail-closed reasons, queue saturation and recovery. Metric labels are
finite outcome/reason/tier-class values; namespace IDs/names, resource IDs,
subjects, digests and revision values are never labels. Individual tier
lag is accessible only through bounded authorized diagnostics/logs.

No raw provider errors, policy bodies, credential contents or full subject
membership arrays in public logs. Diagnostics must remain valid during
provider swap, shutdown and rejected reloads.
