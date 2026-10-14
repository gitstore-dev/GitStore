# GitStore capacity profiles

Capacity scenarios use k6 or focused Go harnesses for reproducible offered
load and threshold decisions. Run them only through the root Makefile:

```bash
make capacity TARGET=api PROFILE=readiness MODE=diagnostic \
  CAPACITY_BASE_URL=http://localhost:4000
```

Valid target/profile pairs are `api/readiness`, `api/authorization`, `namespace/admission`,
`namespace/validation`, `namespace/watch`, `namespace/recovery`,
`repository/lifecycle`, and `scylla/soak`. Admission is deployed k6 load; validation is the in-process
two-replica soak. `CAPACITY_PROFILE` is now only an internal k6 dispatch detail.
The dispatcher is the only public capacity entry point.

## Local telemetry and live profiling

`CAPACITY_OBSERVABILITY=full` extends the existing Prometheus selector with an
OTLP HTTP collector, Tempo and Loki in `compose.capacity.yml`. Core processes
must already have their typed telemetry configuration enabled; collector startup
does not restart or reconfigure them. On the capacity network, use
`CAPACITY_OTLP_ENDPOINT=http://capacity-otel-collector:4318` and optionally
`CAPACITY_GIT_METRICS_ADDR=0.0.0.0:50052` before controlled process replacement.
Set `CAPACITY_PROMETHEUS_GIT_TARGETS=capacity-git-service:50052` to scrape native
RPC, read, blocking-job and Tokio queue/task metrics. Trace sampling defaults to
0.05; `CAPACITY_TRACE_SAMPLE_RATIO` changes the existing typed sampling setting.
Native RPC completion metrics use fixed method/status labels, retain streaming
frames and trailers, and finish on final status or transport cancellation.
Full native exports require counter and histogram increments during the run;
historical positive values alone cannot prove live instrumentation. Exports
include method latency and completion/non-OK status queries. `AlreadyExists`
is an expected provisioning result, not evidence of lost workload.
Cancellation does not mean an owned blocking job has drained; retain its
separate active/pending observations.

File workers seed their exact assigned paths once, then use one path-scoped
Git commit command for local preparation. Unoffered staged paths are excluded.
Preparation remains inside offered-to-ACK and worker-occupation measurements;
real authenticated pushes and verification through both APIs are unchanged.

Collectors expose loopback-only host ports and have fixed memory/CPU, ingestion,
queue and retention bounds. `CAPACITY_OBSERVABILITY_KEEP=1` keeps them running
for later queries; cleanup otherwise removes their containers but retains named
data volumes. Tempo's Go memory limit reserves headroom inside its unchanged
384 MiB container limit. Full exports include native/collector histories, bounded
trace details restricted to current-run storage blocks, and selected phase logs.
Missing stores/targets, empty results, failed
requests and export-limit truncation explicitly fail collection. Failed artifacts
remain diagnostic evidence, not a successful capacity gate.
Collection allows 15 seconds for SDK/collector batches and idle-trace indexing
after the workload; this settling interval is not offered load or a latency sample.

For an owned Repository workload diagnostic, enable the API loopback profiler
with `CAPACITY_API_PROFILE_ADDR=127.0.0.1:6060`, then run with
`CAPACITY_PROFILING=go CAPACITY_PROFILE_SECONDS=30`. Captures begin at actual
offered load after fixture preparation/stabilization and cover both verified API
containers. The harness captures CPU, heap, allocations, block/mutex, goroutines
and execution trace, with bounded durations and 32 MiB per artifact. Capture
failures invalidate the diagnostic. Profiles are private binary runtime material;
do not publish them raw. Profiling/sampling overhead makes these diagnostic
runs unsuitable for unprofiled acceptance.

API and Git telemetry accounting distinguishes admitted, exported, failed and
truncated records. Both bound exported string log bodies to 4096 UTF-8 bytes.
The live admitted/exported difference includes pending batches; only interpret
loss after bounded flush/drain, subtracting records counted as failed exports.
Native Rust attribute-value bounds are not equivalent to the API's attribute
limits; do not infer complete payload-bound parity from the log-body limit.
Exports retain API process start times and native Git telemetry counters.
Account for process replacement and counter resets; raw final-minus-initial
counter values can be negative or misleading around a rolling replacement.
Preserve collector refused/send-failed
series alongside traces. Native Rust runtime observations are not native CPU
samples: record `perf`/symbols/permissions availability explicitly and never
silently elevate privileges. See [configuration](../../docs/configuration.md#telemetry-and-api-profiling)
for typed settings and listener restrictions.

## Authorization evaluator diagnostics

`make capacity TARGET=api PROFILE=authorization MODE=diagnostic` runs the
maximum policy fixture (200 roles, 1,000 bindings, 2,000 rules and 50 overlapping
groups). It samples each named allow/deny case independently at concurrency
1, 8 and 32, with 60 seconds of warm-up and 10 minutes of measurement each.
The subsequent 30-minute reload soak uses 32 decision workers, alternates two
valid policies and rejects malformed, legacy-version and oversized policies.
Decisions must match a complete published snapshot; a retained old snapshot
must remain unchanged. Retained heap is measured after GC with the same
retained policy inputs and snapshots on both sides.

Evidence includes named latency histograms and runtime/policy dimensions,
reload counts, semantic errors and heap measurements, and an evaluator
call-graph audit that rejects unreviewed function calls and file/datastore/network
dependencies. The audit explicitly permits the existing decision timestamp
clock read; it is a source dependency proof, not a syscall trace.
`AUTHZ_CAPACITY_WARMUP`, `AUTHZ_CAPACITY_DURATION` and
`AUTHZ_CAPACITY_RELOAD_DURATION` may shorten diagnostics only.
Diagnostic success never establishes acceptance. Alpha/production require a
clean unchanged verifier revision and runtime/configuration manifests. Every
case needs at least 1,000 samples and p99 below 1ms; shortened durations,
missing artifacts, inconsistent digests, incomplete reload accounting and
post-GC heap above 110% fail the gate. The environment manifest runtime,
architecture and CPU count must match the observed evaluator process.
Evaluator measurements do not replace deployed readiness, replica semantics
or lifecycle gates.

## Common resource and evidence contract

Every profile uses the same accounting stages: scheduled offer, queue admission,
execution attempt, authoritative acknowledgement, and per-replica projection
visibility. Report these separately; stage failures can overlap and must not be
summed as distinct failed jobs. A successful command, empty error log, page-size
limit or fixed shard count is not capacity evidence.

The File component records queue wait, local manifest/commit preparation, Git
HTTP push and per-batch projection visibility as separate sampled latency
stages. Git HTTP includes server admission; it does not isolate admission time.
Its scheduled offers, queue admissions and execution attempts are explicit
counters. Fixed failure classes distinguish local Git, non-fast-forward,
authentication, quota, remote rejection, transport, deadline and output-limit
failures without retaining response bodies, credentials or command output.
Projection and credential-resolution failures remain separate overlapping
observations, not additional failed jobs.

Live File projection checks share a bounded verifier rather than independently
polling from every push worker. At most 32 batches wait at once; every 100 ms it
combines their distinct File IDs into one `nodes` query per API, capped at 250
IDs. The two replica queries run concurrently, and verification waits for both
responses. Every File still has to match its own acknowledged sequence and identity
on both replicas within the original request deadline. Missing peer data stays
pending; HTTP quota, authorization, malformed response and identity errors fail
explicitly. This changes neither ingress quotas nor visibility thresholds.

`projection_read` records each push's cumulative time in those two-replica
read rounds; `projection_read_peak` records its slowest individual round.
Each stage has one sample per acknowledged push. Peak-read percentiles must
not exceed the corresponding cumulative-read percentiles. Neither stage is
disjoint from projection visibility, so do not add their latencies together or
interpret cumulative read time as one HTTP request's latency.

File evidence also retains matched `timelines` for the first 2,048 scheduled
offers, with the bound declared as `timelineLimit`. Each row identifies its
sequence, burst, worker and offer time, and records queue wait, offered-to-ACK
time, worker occupancy and the same push's preparation, HTTP and verification
times. Worker occupancy excludes queue wait but includes complete two-replica
verification; ACK excludes its own later verification. Use matched rows to
investigate publisher occupancy rather than subtracting unrelated percentiles.
These rows are a bounded prefix, not complete production-run timing evidence.

Repository observations separate scheduled offers, acknowledgements, failures,
queue drops, HTTP attempts and retries. Latency stages distinguish queue wait,
the per-resource client lock, the complete mutation (including retry delays),
and each HTTP attempt. The scheduler accounts for every declared interval
through the nominal end, catches up rather than losing ticker notifications,
and records offers delayed by at least one interval. A 30-second drain deadline
cancels remaining requests and fails the run. Only transient HTTP transport,
quota and version-conflict outcomes retry within the existing request deadline;
malformed or unconfirmed success payloads, authorization/policy failures and
superseded commits fail explicitly. Initial Namespace provisioning waits for
current-generation admission, system-repository and ready conditions on both
API replicas rather than retrying arbitrary Repository creation failures.
The created Repository pool must then expose current-generation admission,
storage-provisioned and ready conditions on both replicas before File authoring.

For controlled workload comparisons, use the existing lifecycle dispatcher:

```bash
make capacity TARGET=repository PROFILE=lifecycle MODE=diagnostic \
  REPOSITORY_CAPACITY_WORKLOAD=file \
  REPOSITORY_CAPACITY_SKIP_REPLACEMENT=1 \
  REPOSITORY_CAPACITY_DURATION=2m
```

Set the selector to `repository`, `file` or `combined`. All three seed the
same Repository/File pools and initial controller work before stabilization,
then run only the selected offered load. They require a provisioned owned
deployment, run-matched private fixture directory and the existing endpoint,
token and Git URL settings. The runner matches endpoint process identities
against the owned containers and refuses concurrent runs against that fixture.
Keep the full secret-scenario flag off: comparisons do not execute its scheduled
faults, dataset acceptance, replay or replacement gates, and cannot produce
alpha/production acceptance. They retain component File evidence and five-process
before/load-end/stabilized resource samples in `workload-comparison.json`.
Run comparisons on equally provisioned fresh fixtures; the seed counts do not
establish the cardinality of an existing deployment.

Owned diagnostic stack lifecycle is also available through this command family:
`make capacity TARGET=repository PROFILE=lifecycle MODE=diagnostic
CAPACITY_STACK_ACTION=up` (or `wait`, `token`, `verify-owned`, `down`, `config`).
`resume` only completes a verified owned secret startup interrupted before
private fixture provisioning; it does not overwrite existing fixture records.
Keep the project, state directory, run ID, private fixture and port/keyspace
settings consistent across actions. Startup refuses existing project data;
shutdown verifies ownership and retains volumes. Provisioning the existing
secret fixtures requires its explicit confirmation settings; selecting a
diagnostic workload does not itself authorize scheduled faults.

The common correctness matrix covers Namespace, Repository, Product,
ProductVariant, CategoryTaxonomy, Collection, File and ServiceAccount wherever
each kind supports an operation. Equivalent operations must agree on UID-bound
incarnation checks, atomic expected-resource-version updates, generation changes,
status/finalizer preservation, authorization outcomes, projection readiness and
repair. Cluster scope, Git authoring and supported controller operations remain
intentional kind-specific differences. Do not add unsupported operations solely
to make the matrix rectangular.

Record resolved fixture cardinality, workload mix, payload bytes, rates, queue
and worker bounds, warm/cold status, policy/fixture digests, runtime allocation,
image revision and distinct process identities before load. Expected denial
probes pass only on their exact declared outcome; transport or unrelated domain
errors are not expected denials. Missing observations, unsampled cases, unfinished
faults, dropped/late/undrained work and lost acknowledged projections cannot pass.
Keep diagnostic evidence distinct from alpha/production acceptance.

API readiness now runs the unchanged health/readiness bounds independently on
every `CAPACITY_API_ENDPOINTS` entry. Alpha/production use 10 iterations/s per
API for at least 10 minutes with 10 preallocated / 100 maximum VUs per API.
Supply `CAPACITY_TOKEN_FILE` for an identity allowed to list Namespaces. Each
iteration performs one bounded Namespace list with that credential and one
expected denial, requiring the exact response shape/error class. The denied
identity is anonymous unless `CAPACITY_DENIED_TOKEN_FILE` selects a separate
valid, unprivileged identity; `CAPACITY_DENIAL_CODE` defaults to `FORBIDDEN`.
Expected denials are measured separately from readiness HTTP failures.
Both APIs must expose matching loaded policy/provider/catalog metadata, unchanged
throughout the run. The existing shared evidence validator checks the sanitized
`authorization-probes.json`, every replica's complete allow/deny accounting,
duration and verified preflight identity. Missing or unsampled probes cannot
pass a gate. A diagnostic without a token still runs health/readiness only and
does not prove authorization. These baseline probes do not substitute for the
full domain, ownership, ServiceAccount or fault matrix.
The runner rejects HTTP debug logging and incompatible k6 summary settings
instead of risking credential disclosure or unverifiable evidence.
Domain verifiers read the flat metrics written by native k6 `--summary-export`:
counters expose `count`, rates expose `value`, and trends expose percentile keys.
The runner explicitly exports p95, p99 and trend sample counts on every k6 path.
The Namespace admission verifier requires sampled check and failure-rate
observations and one visibility sample per admitted Namespace; missing metrics
cannot be treated as successful checks.
Namespace admission and watch workloads create generated `ORGANIZATION`
namespaces and preserve that tier during updates. `USER` namespaces are bound
to a human's identity and cannot serve as a pool of generated workload names.

Scylla query evidence must cover real datastore access paths, partition and
clustering restrictions, shard merging and hydration, read/write amplification,
tombstones, and projection repair under sparse, hot/skewed and churning fixtures.
Inspect actual partitions rather than interpreting an empty
`system.large_partitions` report as proof of small partitions. Preserve the
250-row connection maximum and existing workload-specific latency limits.
Record approved projection-specific ceilings before declaring acceptance.

The managed capacity stack uses project-scoped container names. Existing
container names are not proof of ownership or disposable data: inspect writable
layers, mounts and keyspaces before cleanup. Retain run volumes and keyspaces
when stopping a stack; deleting identified unused data requires explicit
approval. No legacy compatibility or old-binary rollout is part of these
actively developed alpha workloads.

Managed startup writes a private `owned-stack.json` containing project, run,
revision, keyspace and host ports before creating services. Each container carries
the same run label. Wait, token, alpha, replacement and shutdown actions verify
the manifest; lifecycle actions reject any container carrying a different run.
Keep the original state directory and settings for later lifecycle calls.
The manifest and volumes are retained after shutdown; a fresh run needs a fresh
state directory and project. A missing manifest is not permission to operate on
similarly named containers.

Scylla soak page checks now call the real datastore: global and namespace
Repository pages include bucket reads and authoritative hydration; Product pages
read their authoritative namespace table. Forward/backward windows must agree.
Per-page query evidence reports attempts, returned driver rows, retries, tables
and latency without bound values. These are not SSTable/tombstone counters or
partition-size proof, and small regression fixtures do not certify capacity.

Datastore container snapshots capture host/container epoch seconds before
and after load. They reject clock disagreement outside the one-second precision
of `date`, backward host time, or samples taking longer than one second.
This is a conservative environment validity guard: skew can invalidate CDC
timestamps and last-write-wins results. It does not alter clocks or establish
an application latency guarantee. Correct the environment before rerunning;
do not treat skewed-run failures as clean query-performance evidence.

Spec 063's `REPOSITORY_CAPACITY_SECRET_SCENARIO=1` selects the connected File-push,
owned-record fault, resource and evidence collector. It requires explicit
`CHAOS_CONFIRM=1`, provisioned test-owned fixtures and an acknowledged Product
manifest before running; the old Repository-only workload cannot establish
secret-scenario acceptance. The full deployed capacity run has not been certified.
`make test TARGET=secret-integration` provides separate functional bootstrap/rotation
acceptance, not capacity certification. See the
[rotation runbook](../../docs/runbooks/secret-material-rotation.md) and
[capacity contract](../../specs/063-implement-secret-adrs/contracts/capacity-and-rollout.md).
The secret topology retains exactly one active Git service in every mode;
repository sharding and multi-Git operation are not supported.

`make test` runs the secret production-contract assertion matrix in
`tests/integration/secret_capacity_contract_test.go`. It covers exact offered
load, burst timing/drain, bounded client/provider work, offline titled-Product
count proof, all latency/resource thresholds and scheduled fault/recovery
requirements. The assertions require offline pages of at most 1,000 rows. Synthetic
observation fixtures cannot certify a deployment; only a complete measured
run can produce a passing production bundle.

The guarded File-load path now reuses the Repository lifecycle runner: it seeds
100 typed Files before baseline, uses 32 disjoint authoring worktrees/repositories
on the one Git service to avoid client-side non-fast-forward contention, and
offers real pushes through a queue capped at 256. Missed schedules and drops are
counted rather than hidden by ticker coalescing or retries. Every acknowledged
batch is checked through both APIs' existing `nodes` query. Initial IDs come
from persisted `file(namespace:, name:)` lookups of the 100 known fixture names
on each API: ten aliases per request, a five-second request timeout, and a
30-second bootstrap deadline. This is not an inventory scan or a subscription
snapshot; subscribing after seed pushes cannot discover already-existing Files.
Missing, duplicate or invalid projections fail closed, and transport/HTTP/GraphQL
failures are classified without logging response bodies or credentials.
Local resolution and explicit
32-caller/16-slot contention/deadline/authorization probes use test-owned regular
provider files. No File payload operation is performed.

`secret/file-workload.json` is a **component observation**, written atomically
even when the enclosing workload is canceled. It records actual counts, timings
and denials, distinguishes nominal duration from elapsed offering time, and has
no `passed` field. It cannot replace `secret-evidence.json`, dataset verification,
resource measurements or the scheduled fault proofs. The dispatcher assembles
the final bundle only after the verifier, captured logs and postflight finish.
The local checks use real bare-Git pushes and regular-file providers; they do not
claim a deployed API/Git/controller run.

Before pool creation or offered load, the guarded path verifies an acknowledged
Product JSONL manifest against bounded `products` pagination on **both APIs**.
`REPOSITORY_CAPACITY_SECRET_DATASET_MANIFEST` must be an absolute regular-file
path; `REPOSITORY_CAPACITY_SECRET_DATASET_NAMESPACE` defaults to
`REPOSITORY_CAPACITY_NAMESPACE`, and
`REPOSITORY_CAPACITY_SECRET_DATASET_PAGE_SIZE` defaults to 250 (maximum 250).
Each line contains exactly `namespace`, `name`, `title`, `revision` (the exact
API value: `<branch>@sha1:<40 lowercase hex characters>`, or a bare SHA-1 for
preloaded fixtures) and `acknowledged: true`. Names must be strictly sorted
and unique. Limits are 4 KiB/line, 8 GiB/file, 10 million rows and 30 minutes for
the complete verification. Production rejects fewer than five million rows
before requesting pages. No aggregate query or Scylla counter is used.

`secret/dataset.json` contains counts, per-replica page observations, manifest
SHA-256 and an order-independent row-content digest, not the manifest or Product
contents. Count plus digest comparison rejects missing, duplicated or changed
namespace/name/title/revision rows without retaining millions of names.
`secret/controllers-before-load.json` and `secret/controllers-after-load.json`
record process-local exchange/reconciliation counters and resource snapshots.
The observer brackets readiness with identity-checked metrics scrapes, bounds
responses to 2 MiB and each sample to five seconds, and rejects malformed,
missing, duplicate, nonfinite or regressing required metrics. HTTP 503 with
explicit exhausted credential readiness is observable, never healthy.

These component snapshots have no `passed` field and cannot replace sustained
resource sampling or establish a production dataset run. The guarded fault
driver now schedules owned record withdrawal/restoration at minute 15, key
overlap/retirement at minute 30, and an explicitly confirmed root chaos restart
at minute 45. It observes fresh authentication and reconciliation within the
recovery budget and restores withdrawn records on cancellation.

Owned provisioning reuses `compose.capacity.yml`: the stack owner selects each
controller's config and read-only provider source with
`CAPACITY_CONTROLLER_{A,B}_CONFIG_FILE` and
`CAPACITY_CONTROLLER_{A,B}_SECRET_SOURCE`. Normal runs retain their existing
shared-key defaults. Secret runs keep the shared nonsecret UID volume and
separate checkpoint volumes, but neither controller mounts the other's private
provider directory. The existing API command clamps token TTL only when the
scenario flag is set. No secret-specific Compose overlay or public Make target
is needed. Missing ownership/fault prerequisites are rejected before startup;
missing observations prevent final bundle acceptance.

On macOS, the owned-mount verifier accepts Docker Desktop's mixed bind-source
representations (`/host_mnt/Users/...` for a directory and `/Users/...` for a
file) only after confirming a local Unix-socket Docker endpoint and the daemon's
`Docker Desktop` operating-system identity. `DOCKER_CONTEXT` takes precedence
over `DOCKER_HOST`. Translated and native bind sources must resolve on the host;
the same exact per-controller paths, read-only flags, destinations and four-mount
limit still apply. Linux and remote endpoints receive no Desktop translation.
Mounts exposing the fixture root, its ancestors or another controller's files
remain invalid, including mounts on APIs or the singleton Git service.

`make test` also runs these contracts under the race detector,
including JSON/file-based negative fixtures. The bundle loader in
`tests/integration/secret_capacity_contract_test.go` requires a closed
`secret-evidence.json` envelope with `schemaVersion: 1`, `runID`, `gitRevision`,
`observations` and `artifacts`. Every observation field is required, including
zero error/drop counters; durations use integer nanoseconds. Unknown, duplicate,
case-folded, null or omitted fields fail. The caller must supply the expected run
and revision from independent provenance.
`duration` is the nominal hour-long schedule; `offeredDuration` is measured and
must cover that schedule without drifting by more than one 100 ms cadence.

Each artifact declares a relative `path`, `kind` (`log`, `metrics`, `trace` or
`summary`), `processID`, byte count and SHA-256. Logs and metrics are required for
each measured process and both replacement processes (API B and controller A). Unlisted, missing,
modified, duplicate, symlink or nonregular artifacts fail. The bounded scanner
checks actual files, including the envelope, for private-key/token patterns,
provider records, raw controller configuration/environment and caller-supplied
private markers (including encoded forms). It never echoes offending content.
Bounds are 2 MiB per JSON envelope, 512 files, 512 MiB per artifact, 4 GiB
total, 1,024 directory entries and 16 directory levels. The hour-long production
workload can exceed 128 MiB of normal authorization/admission logs per API;
the larger finite disk budget preserves complete logs instead of truncating or
sampling them. Hashing and leakage scanning remain streaming with bounded
buffers; this does not increase service memory limits or relax latency gates.

These helpers validate a **completed immutable collection**, not a live log.
Setup checks every seeded File through typed lookups and generic `nodes` on both
APIs before starting offered load, so a missing read surface fails during setup.
They do not authenticate the origin of observations, collect deployment
telemetry, or replace the existing release-image/Scylla/lifecycle preflights.
The connected collector captures original logs before API removal, bounded
replacement logs, process-identified Go metrics and singleton Git cgroup-v2 CPU/
proc RSS counters. CPU accounting splits at replacement; both RSS segments use
the original warmed baseline. Source revision/state must remain unchanged through
finalization. Secret artifacts cannot enter the old Repository-only path.

Internal owned-container metrics probes retain the same 8 MiB response bound as
HTTP metrics scrapes, separately from the 128 KiB Git/other command-output bound.
Overflow fails explicitly before a truncated payload can establish process
identity. Command failures report sanitized exit/start categories without
including arguments or captured output; caller cancellation remains distinct.

For an explicitly owned deployment, use the existing stack lifecycle with the
scenario flag and `CHAOS_CONFIRM=1`. It stages per-controller read-only provider/
config mounts in `compose.capacity.yml`; no extra Compose overlay is needed.
The managed alpha run defaults to 60m load, 32 authoring repositories, 5m baseline
and 10m stabilization. Direct runs additionally require
`REPOSITORY_CAPACITY_SECRET_OWNED_DEPLOYMENT=1`,
`REPOSITORY_CAPACITY_SECRET_FIXTURE_DIR` and a matching `CAPACITY_RUN_ID`.
Private fixture records/markers stay outside evidence and are removed by the
owned stack cleanup; never target an operator deployment. Completed evidence is
under `${CAPACITY_EVIDENCE_DIR}/repository/lifecycle/<mode>/<run-id>/`, defaulting
to `.gitstore/capacity`, with root `secret-evidence.json`. A failed finalizer
removes that envelope and marks `metadata.json` failed.

Prebuilt production images can be selected with `CAPACITY_API_IMAGE`,
`CAPACITY_CONTROLLER_IMAGE` and `CAPACITY_GIT_SERVICE_IMAGE` (digest-pinned
references). The existing stack and replacement watcher reuse these references
without rebuilding them; unspecified images retain the normal local build tags.

Modes classify acceptance independently of workload. `diagnostic` records
results but can never set `passed: true`. `alpha` keeps correctness, error,
recovery, CPU, and memory requirements hard, enforces Namespace visibility p95
≤2s and p99 ≤3s, and warns above the unchanged 1s production p95 target.
`production` enforces visibility p95 ≤1s and p99 ≤3s. Require at least five
clean fixed-topology 10-minute repetitions before proposing another threshold
change.

Repository lifecycle has separate minimum scales for the two evidence tiers:

| Mode         | Duration | Transition interval | Subscribers | Replay events | Replay samples | Resources | Overflow transitions | Burst | Visibility       | API CPU | API RSS                          |
|--------------|---------:|--------------------:|------------:|--------------:|---------------:|----------:|---------------------:|------:|------------------|--------:|----------------------------------|
| `alpha`      |      10m |               500ms |         100 |         1,000 |              5 |        20 |                  256 |    20 | p95 ≤2s, p99 ≤3s |    <80% | <75% growth and <256 MiB per API |
| `production` |      60m |               100ms |       1,000 |        10,000 |             20 |        50 |                1,000 |   100 | p95 ≤1s, p99 ≤3s |    <80% | <10% growth                      |

Both modes require two real API processes, two real controller-manager
processes, shared durable storage, an observed replacement of the selected API
process, cursor-resumed recovery, and zero event loss. Alpha is intentionally
laptop-safe; passing it does not claim the production soak passed.
Alpha's wider relative RSS allowance accounts for Go/runtime cache warm-up in
the cold-start 10-minute run, but the simultaneous 256 MiB per-API ceiling
keeps it bounded. Production retains the stricter less-than-10% RSS-growth
gate. The less-than-80% normalized CPU gate is unchanged in both modes.

The default runner uses the pinned container image declared in the Makefile.
Set `K6_BIN` to an explicit executable when container networking is unsuitable,
or `CAPACITY_ENV_FILE` to an absolute, untracked environment file. Never put
tokens in profile source or committed evidence.
For a file containing only a bearer token, use `CAPACITY_TOKEN_FILE`; the
runner exports it without printing or recording the value.

Alpha and production runs also supply sanitized JSON through `CAPACITY_CONFIG_MANIFEST` and
`CAPACITY_ENVIRONMENT_MANIFEST`. The runner canonicalizes and hashes both into
the evidence bundle. The config manifest records effective non-secret knobs,
not merely source-file defaults; the environment manifest records CPU, memory,
architecture, runtime, replica counts, datastore topology, and optimized build
identity. See `examples/` for the minimum shape. Do not include credentials,
secret references whose names are sensitive, or raw environment dumps.
For datastore-backed profiles, record per-node memory limits, CPU/shards,
restart counts, OOM state, and authentication mode. A gate fails if a node is
OOM-killed, unexpectedly restarts, or leaves cluster membership during load.
Set `CAPACITY_DATASTORE_CONTAINERS` to the comma-separated container names. The
runner captures sanitized `docker inspect` state before and after load and
fails on OOM, restart, disappearance, stopped state, incomplete `nodetool
status` membership, or a runtime `--smp` value that differs from the config
manifest. Namespace admission
alpha and production modes additionally require:

```bash
CAPACITY_RUNTIME_MEMORY_BYTES=17179869184
CAPACITY_API_BUILD=release
CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=<explicit container limit>
CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated # or password-authenticated
CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3
```

The same manifest contract applies to the Go-based `validation`, `watch`,
`recovery`, Repository `lifecycle`, and `soak` profiles. Their focused Go test is recorded as the
domain verifier; deployed Scylla-backed profiles additionally require the
before/after container-health evidence above.
Non-diagnostic `watch` and `recovery` runs also require
`CAPACITY_API_ENDPOINTS` containing every declared replica. Preflight retains
each endpoint's `process_start_time_seconds`, maps it positionally to the
corresponding unique container ID and `gitstore_api_process_instance_info`
UUID in `CAPACITY_API_CONTAINERS`, and requires `NAMESPACE_WATCH_API_A` and
`NAMESPACE_WATCH_API_B` to be members of that verified set.
Provide the matching running containers in `CAPACITY_API_CONTAINERS` and
`CAPACITY_GIT_SERVICE_CONTAINER`. Preflight requires digest-pinned images with
an OCI revision label matching the tested checkout, verifies the expected
release executables, and maps the endpoint process identities back to the
inspected API containers.
Recovery also requires `NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE`. The external
harness must replace the selected endpoint when that file appears; the probe
requires an observed outage, a changed `process_start_time_seconds`, and
cursor-resumed delivery through the replacement before it passes.

Non-diagnostic API readiness also requires both manifests plus
`CAPACITY_API_REPLICAS`, `CAPACITY_API_BUILD=release`,
`CAPACITY_RUNTIME_MEMORY_BYTES`, `CAPACITY_BASE_URL`, and a comma-separated
`CAPACITY_API_ENDPOINTS` containing every replica. Provide the corresponding
containers in the same order through `CAPACITY_API_CONTAINERS`. The preflight
requires unique live container IDs and matches every external endpoint to its
container's collision-safe process-instance UUID. Process start time remains
available for replacement proof, while endpoint aliases fail even when
legitimate replicas start in the same kernel tick.
`CAPACITY_BASE_URL` must be one of those verified endpoints. Every live
container must use the release executable and an OCI revision matching the
tested checkout. Production requires a digest-pinned image; alpha may use a
locally built exact-revision release image because the evidence records and
verifies its immutable image ID.
For `namespace/watch`, `namespace/recovery`, and `repository/lifecycle`, the
dispatcher repeats this identity and artifact inspection after the workload. A
replacement using a different image, executable, or revision therefore makes
the evidence fail and is retained in `postflight-environment.json` for audit.
The preflight and postflight identities must show that exactly the selected
Namespace or Repository replacement endpoint changed while every other replica
stayed up unchanged.

Alpha and production evidence requires a clean Git checkout so the recorded
`gitRevision` identifies the exact verifier and gate scripts that produced it.
Recovery additionally requires at least 1,000 overflow transitions, and its
terminal-error read is capped at 60 seconds so a misconfigured deployment fails
promptly instead of waiting for the overall Go test timeout.

`repository/lifecycle` is the end-to-end spec-058 gate. It alternates
Git-delegating Repository mutations across two API endpoints, verifies durable
watch delivery and reads through both replicas, requires both independent
controller-manager endpoints to reconcile without poison items, measures
replay, holds live subscribers during sustained load and bursts, forces
slow-consumer overflow, and coordinates one real API process
replacement with cursor-resumed recovery. Configure it with the
`REPOSITORY_API_*`, `REPOSITORY_CONTROLLER_*`, `REPOSITORY_TOKEN[_FILE]`, and
`REPOSITORY_CAPACITY_*` variables shown by `make help`. Diagnostic mode permits
smaller experiments but never produces passing gate evidence.

The managed alpha topology contains two APIs, one singleton Git service, two
active-active controllers, and three Scylla nodes. The shared Git volume is not
sharded or replicated, so this is not a Git-service HA test. Controllers do
not acquire an API lease or fencing token; their concurrency contract is
level-triggered idempotency plus optimistic resource-version checks. The
Scylla-backed lease/fencing signals in this profile belong only to CDC journal
materialization. Alpha requires 256 overflow transitions, which exceeds the
two 64-event buffering stages; production retains its 1,000-transition floor.
`REPOSITORY_CAPACITY_OVERFLOW_BACKPRESSURE_WAIT` defaults to 31 seconds so the
probe does not read until the configured 30-second backpressure bound expires.

For a complete isolated laptop alpha deployment, use the canonical capacity
interface rather than assembling those variables manually:

```bash
make capacity TARGET=repository PROFILE=lifecycle MODE=alpha
```

The command manages stack startup, readiness, token bootstrap, API-B
replacement, evidence capture, and cleanup. Production mode does not create a
local stack; point it at an externally managed topology with the documented
`REPOSITORY_*` and `CAPACITY_*` variables. The managed alpha topology is
defined in the shared `compose.capacity.yml` capacity overlay.

When a load balancer or local container runtime buffers WebSocket traffic,
point `REPOSITORY_OVERFLOW_API` at a reader-only API replica on the same
datastore and journal. Configure only that chaos endpoint with
`api.watch.journal.subscriber.buffer=1` and
`api.watch.journal.subscriber.backpressure=1ms`; the two measured API
replicas must retain their production settings.

The checked-in three-node profile defaults `SCYLLA_CLUSTER_MEMORY_LIMIT` to
`3g` per node. Override it explicitly when testing another resource tier and
set `CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE` to the corresponding byte value.

## Adding a profile

1. Add `profiles/<name>.js`; import reusable helpers from `lib/`.
2. Use an arrival-rate executor for a throughput contract. Treat
   `dropped_iterations` as a failing threshold unless the specification
   explicitly defines overload shedding as the expected result.
3. Tag requests by stable operation name and define p95, p99, error-rate, and
   correctness-check thresholds.
4. Fail fast when required topology, dataset, or credential inputs are absent.
5. Add a domain verifier for invariants k6 cannot prove, and store its output
   in the same evidence directory identified by `CAPACITY_EVIDENCE_DIR`.
6. Run declared `make chaos` profiles while the load is active and require the
   recovery verifier to pass.

An executable `preflight/<profile>.sh` runs before k6 and must fail when the
declared topology or dataset scale is absent. An executable
`verifiers/<profile>.sh` runs after k6 and decides domain correctness. Both
receive the evidence directory as their first argument and store structured
results there. Every alpha/production k6 profile requires an environment
preflight. Profiles other than the non-mutating `api-readiness` smoke test also
fail closed when their domain verifier is absent or not executable.

To inject a reviewed fault during load, configure the integrated runner:

```bash
make capacity TARGET=namespace PROFILE=admission MODE=production \
  CAPACITY_CHAOS_PROFILE=api-restart \
  CAPACITY_CHAOS_TARGET=gitstore-capacity-api-a \
  CAPACITY_CHAOS_DELAY=30m \
  CAPACITY_CHAOS_CONFIRM=1
```

The overall run fails if k6 thresholds, fault injection, or the domain verifier
fails. Pumba's successful exit means only that the fault was injected; recovery
is always a verifier responsibility.

A harness exit code alone is not a production capacity pass. Evidence is stored
under `.gitstore/capacity/<target>/<profile>/<mode>/<run-id>/` and must also
identify the deployed revision, topology, dataset scale, fault schedule, and
domain-verifier result.

For internal phase evidence, start the optional API scraper and pass its URL:

```bash
make capacity TARGET=namespace PROFILE=watch MODE=alpha \
  CAPACITY_OBSERVABILITY=prometheus \
  CAPACITY_PROMETHEUS_TARGETS=api-a.internal:4000,api-b.internal:4000
```

PromQL snapshots are selected by profile: readiness records scrape health,
admission adds admission/datastore signals, and watch/recovery add CDC,
materialization, and delivery signals. The API `git_commit`
stage isolates the Git-service boundary; finer Git advisory-lock and reference
retry timing is emitted as bounded structured Git-service log fields without
reintroducing the removed Axum HTTP stack. The repository lock is OS-owned and
automatically released when a Git-service process exits or crashes.
The Namespace watch harness seeds a bounded pool of 50 resources by default and
applies uniquely tagged updates to that pool. Override
`NAMESPACE_WATCH_CAPACITY_RESOURCE_POOL` only when resource cardinality is an
explicit experiment dimension; subscriber count, transition rate, bursts,
replay size, and duration remain independent acceptance dimensions.
Alpha and production watch evidence enforces at least 60 minutes, 1,000
subscribers, 10,000 replay events, 100 transitions per burst, and a burst
interval shorter than the run. Smaller experiments must use diagnostic mode.
The dispatcher writes those configurable scrape targets into the evidence
bundle and gives every run an ephemeral Prometheus TSDB. The exporter derives
its default PromQL lookback from the recorded run start time plus a small
scrape-boundary allowance. `CAPACITY_PROMETHEUS_LOOKBACK` remains an explicit
override for unusual collection windows, but it cannot reach a prior run's
isolated TSDB.

For Repository/secret recovery investigations, also pass
`CAPACITY_PROMETHEUS_CONTROLLER_TARGETS=host.docker.internal:5001,host.docker.internal:5002`
(or the two controller endpoints reachable from the scraper). Keep
`CAPACITY_OBSERVABILITY=prometheus` and the API targets above. Controllers use
the separate `gitstore-controller-capacity` job. The exporter saves five-second
range samples for controller identity, credentials, reconciliation, recovery,
stall, queue and resource metrics to `prometheus/controller-series.json`,
including on a failed gate, before the ephemeral scraper is removed. Missing
controller history fails export rather than generating zero-valued evidence.

Controller snapshots also retain per-kind
`gitstore_controller_dependency_deferrals_total` counters, separated into
`api`, `credentials` and `rate_limit` causes. These are deferral attempts, not
unique pending items or durable acknowledgements. An API-outage recovery claim
needs actual deferrals, zero new quarantine and subsequent successful work;
endpoint readiness alone is insufficient. Compare counter changes only within
the same recorded process identity, and retain restart boundaries in the time
series. An absent lazy counter series provides no positive deferral evidence.

Secret-scenario setup waits up to ten minutes for healthy, authenticated
controllers to finish list/watch recovery before collecting the baseline.
`secret/controllers-recovery.json` retains the latest warmup observation;
before/after-load snapshots retain per-kind health and recovery details before
asserting readiness. Progressing recovery is not permission to start load, and
neither warmup nor metrics export substitutes for a complete production gate.

## Choosing application defaults

Default changes require a configuration matrix, not one successful run. Hold
the workload, dataset, topology, and hardware manifest constant; vary one
bounded group of related knobs; run at least three repetitions per candidate;
and compare throughput, p95/p99, errors, recovery, CPU, retained memory,
goroutines, queue depth, and datastore pressure. Select the smallest setting
that meets the envelope with documented headroom and safe overload behavior.
Record the chosen evidence run IDs beside the config change. Never promote a
diagnostic run, a debug build, or a fault run without successful rollback into
a default.

## Included profiles

- `api-readiness` is a non-mutating runner smoke test.
- `namespace-admission` drives unique Namespace creates at a fixed arrival
  rate, alternates across two replicas, and fails on dropped iterations,
  GraphQL errors, or latency/error threshold violations. Each acknowledged
  Namespace is queried through every declared replica, and the domain verifier
  requires nonzero admissions, zero failed checks, and cross-replica visibility
  at p95 <= 1 second and p99 <= 3 seconds. The Go watch verifier remains
  authoritative for replay, duplicates, cursor recovery, and replacement.

- `category-hierarchy` (`make capacity TARGET=category PROFILE=hierarchy
  MODE=<mode>`, spec 057) seeds a deterministic fan-out subtree of
  `CAPACITY_CATEGORY_SUBTREE_SIZE` (default and alpha/production minimum 10,000)
  descendants under one root, then sustains `createCategory`/`updateCategory`
  at `CAPACITY_RATE` (default and minimum 20/s, one namespace,
  `CAPACITY_CATEGORY_NAMESPACE`) alternating across both APIs, while paging
  `categories(filter:{descendantOf,...}, first:100)`. Midway
  (`CAPACITY_CATEGORY_REPARENT_AFTER_SECONDS`) the subtree root is re-parented
  and cascade convergence is recorded as `category_cascade_convergence_ms`
  (reported, not gated). Thresholds for alpha and production alike: mutation
  p95 <= 750ms / p99 <= 2s; filtered page p95 <= 150ms / p99 <= 500ms, including
  during the cascade. Other knobs: `CAPACITY_CATEGORY_FANOUT`,
  `CAPACITY_CATEGORY_DURATION_SECONDS` (min 300 for gates),
  `CAPACITY_CATEGORY_READ_RATE`, `CAPACITY_CATEGORY_CONVERGENCE_TIMEOUT_SECONDS`.
  - **SC-009**: teardown waits for the cascade, walks every category's
    `status.resolved.path`, and compares the walk with seven filtered result
    sets (including `maxDepth`, `includeSelf` and an unknown root) fully paged
    on each API replica; any missing or extra name fails.
  - **SC-007**: each pool record (a created category or a seed updated once) is
    touched by exactly one mutation. Each acknowledged payload must carry its own
    name and title and a `metadata.revision` of the form `<branch>@sha1:<commit>` (40-64 hex commit); at teardown
    the commits extracted from the stored revisions of all pool records must be pairwise distinct. This
    needs `ObjectMeta.revision` populated on `Category`.
  - **Concurrent pushes**: k6 cannot push, so the companion push driver
    (`TestCapacityGitPushDriver` in `tests/integration/capacity_push_driver_test.go`,
    enabled with `CAPACITY_PUSH_DRIVER=1`; see the file header for its
    `CAPACITY_PUSH_*` settings) pushes commits to the same `gitstore-system`
    repository for the duration of the load and writes `{"repository":"gitstore-system","pushes":N,"failures":0,
    "firstPushMs":<epoch ms>,"lastPushMs":<epoch ms>}`. Pass its path as
    `CAPACITY_CATEGORY_PUSH_EVIDENCE`; alpha/production require it and the
    verifier requires the push window to cover at least half of the load window.
  - **Faults**: reuse `tests/chaos/profiles/controller-restart.json` and
    `api-restart.json` via `CAPACITY_CHAOS_PROFILE` (one per run) with
    `CAPACITY_CHAOS_DELAY` set inside the load window (for example, the delay
    that lands in the cascade). After recovery run `gitctl scylla-projection-audit`
    and pass the output as `CAPACITY_CATEGORY_AUDIT_FILE`; alpha/production
    require zero `findings`.

Alpha and production modes refuse to run unless evidence
declares at least two API replicas, three Scylla nodes with at least two shards
each, and a release Git-service build. Use `MODE=diagnostic` only for
short bottleneck discovery; diagnostic results can never be cited as a capacity
pass.

## Namespace staged progression

Keep topology, binaries, manifests, and application configuration fixed while
progressing. Restart with a fresh keyspace and Git data directory after a
failed stage.

1. Run 10 transitions/s for 10 minutes without subscribers using
   `TARGET=namespace PROFILE=admission MODE=diagnostic`.
2. Run the deployment verifier for 10 minutes with 1,000 subscribers and set
   the burst interval longer than the run so no burst occurs. Set
   `NAMESPACE_WATCH_CAPACITY_SKIP_REPLACEMENT=1` for this diagnostic stage.
3. Repeat the 10-minute verifier with the one-minute 100-transition burst.
4. Run the full 60-minute verifier with the midpoint API restart.

Do not lower the 1,000-subscriber acceptance target after a datastore resource
failure. Test 2,500, 5,000, and 10,000 subscribers separately as connection
scale tiers only after the required gate passes.
