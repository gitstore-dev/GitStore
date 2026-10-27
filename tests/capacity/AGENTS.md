# Capacity, profiling, chaos and owned-deployment instructions

Read these for capacity work anywhere in the repository, including Go
drivers under `../integration/`, helpers under `../../scripts/`, and Compose
or Makefile capacity wiring. The workload/evidence reference is
[README.md](README.md); do not copy this command catalogue into root instructions.

## Public interface and acceptance

- The only public interface is
  `make capacity TARGET=<api|namespace|repository|category|scylla> PROFILE=<scenario> MODE=<diagnostic|alpha|production>`.
  Valid pairs are `api/readiness`, `api/authorization`, `namespace/admission`,
  `namespace/validation`, `namespace/watch`, `namespace/recovery`,
  `repository/lifecycle`, `category/hierarchy` and `scylla/soak`.
- Repository lifecycle is the real two-API/two-controller Git-admission,
  durable-watch, load, overflow, rolling-replacement and recovery gate.
  Exactly one Git process is active; replacement must not overlap.
- Diagnostic evidence cannot pass acceptance gates. Alpha watch visibility
  enforces p95 <=2s and warns above the unchanged 1s production target.
  Production requires p95 <=1s and p99 <=3s.
- Category hierarchy uses a 10,000-descendant subtree, mutations, filtered
  pages and re-parent cascade, with the existing SC-007/SC-009 verifier.
  It needs the companion Git push driver described in the README.
- Keep full durations, bounded work, retry/deadline behavior and resource
  gates. Instrumented or partial diagnostics do not replace unprofiled
  production acceptance.

## Owned stack and storage safety

- `make capacity TARGET=repository PROFILE=lifecycle MODE=diagnostic CAPACITY_STACK_ACTION=<up|resume|wait|token|verify-owned|down|config>`
  manages an explicitly named owned two-API/two-controller/singleton-Git/
  three-Scylla deployment through the existing stack helper.
- `up` refuses existing project containers/volumes. `resume` only completes
  owned secret startup interrupted before private fixture provisioning.
  `down` stops only verified owned services and retains volumes.
- Native sizing uses existing selectors: `SCYLLA_CLUSTER_SMP` defaults to
  `1`; `SCYLLA_TABLETS_PER_SHARD_GOAL` defaults to `100`.
  Fresh owned startup accepts `CAPACITY_SCYLLA_PRODUCT_MIN_TABLETS`
  (default `0`, unchanged) as a Product authority hint. Requested budgets
  are not achieved allocations or CDC stream counts. Measure the actual
  topology and retain the same settings for later lifecycle commands.
- Add `REPOSITORY_CAPACITY_VERIFY_STORAGE=1 REPOSITORY_CAPACITY_PROJECT=<owned-project>`
  for read-only UID-bound Git directory/tombstone checks through the owned
  singleton. Alpha/production default this on and reject disabling it.
  Preserve files, receipts, tombstones and volumes; inspection does not
  replace deletion, fault or capacity gates.
- Add `REPOSITORY_CAPACITY_VERIFY_FINALIZERS=1 CAPACITY_SCYLLA_KEYSPACE=<owned-keyspace>`
  alongside physical-storage checks. Alpha/production require this and
  default it on. Exercise all five catalog kinds as simulated terminating
  blockers, plus real API/controller Namespace, system Repository and
  ordinary Repository termination held by an unrelated finalizer.
- Finalizer probes use bounded exact-UID/resource-version CQL CAS against
  newly created records and verify both APIs use the selected keyspace.
  Restore catalog simulations before real Git removal. Parent probes
  add/remove only their own finalizer, without changing deletion intent,
  timestamp or generation. Both APIs must preserve held storage and reject
  completion; terminating ordinary Repositories still block Namespace
  deletion. These probes do not establish real catalog-child deletion
  initiation or production acceptance.

## Repository/File diagnostics and connected secret scenario

- `make capacity TARGET=repository PROFILE=lifecycle MODE=diagnostic REPOSITORY_CAPACITY_WORKLOAD=<repository|file|combined> REPOSITORY_CAPACITY_SKIP_REPLACEMENT=1`
  compares controlled offered loads with equally seeded Repository/File
  pools on a provisioned owned deployment. Use private run-matched fixtures
  and endpoint/token/Git settings. This mode has no full secret scenario,
  replay, scheduled faults or alpha/production acceptance.
- `REPOSITORY_CAPACITY_SECRET_SCENARIO=1 make capacity TARGET=repository PROFILE=lifecycle MODE=<mode>`
  runs the connected File/fault/resource scenario only with `CHAOS_CONFIRM=1`,
  provisioned owned fixtures and an acknowledged Product manifest.
  The finalizer hashes/scans all closed artifacts. Repository-only runs
  and local fixtures cannot substitute for full production runs.
- Required selectors include `REPOSITORY_CAPACITY_SECRET_DATASET_MANIFEST`
  (absolute acknowledged Product JSONL path),
  `REPOSITORY_CAPACITY_SECRET_DATASET_NAMESPACE`
  (defaults to `REPOSITORY_CAPACITY_NAMESPACE`) and
  `REPOSITORY_CAPACITY_SECRET_DATASET_PAGE_SIZE` (default/maximum `250`).
  Also require `REPOSITORY_CAPACITY_SECRET_OWNED_DEPLOYMENT=1` and a private
  provisioned `REPOSITORY_CAPACITY_SECRET_FIXTURE_DIR` matching `CAPACITY_RUN_ID`.
  Follow the README's manifest, admission/readiness and evidence contracts.
- Add `REPOSITORY_CAPACITY_TRACE_CORRELATION=1` to File/combined comparisons
  or full secret scenarios for a fresh sampled W3C parent per File push
  and bounded timeline `traceID`s. Default is off; Repository-only runs
  reject it. Keep headers command-scoped with bearer authentication.
  It correlates instrumented Git HTTP/admission/RPC spans, not client
  queue/provider work or shared projection batches. Verify actual deployed
  trace/log/exemplar joins.
- Add `REPOSITORY_CAPACITY_CLIENT_TIMING=1` only to diagnostic File/combined
  comparisons for bounded command wall/CPU and in-memory Trace2 startup/
  HTTP-helper timestamps in existing timelines. Default is off; full
  scenarios, Repository-only and alpha/production reject it. Never export
  raw trace events, argv, paths or configuration. This adds overhead, not
  a new acceptance mode or altered workers/checks/deadlines.

## Metrics, traces and profiling

- `CAPACITY_OBSERVABILITY=prometheus CAPACITY_PROMETHEUS_TARGETS=<host:port,...>`
  enables the dispatcher's isolated scraper and current-run API phase
  queries in the evidence bundle.
- Add `CAPACITY_PROMETHEUS_CONTROLLER_TARGETS=<host:port,...>` to retain
  per-kind recovery, reconciliation, stall, queue, credential and process
  identity histories. Secret capacity waits for list/watch recovery before
  baseline and requires both controllers' `ready` health before load and
  acceptance.
- Add `CAPACITY_PROMETHEUS_SCYLLA_TARGETS=<host:9180,...>` for server CAS,
  contention/timeouts, per-shard utilization/scheduling and transport.
  Every configured node must supply finite CAS-count/utilization history
  and an up sample; missing history fails export. Preserve server-native
  units; these are not individual request execution times.
- `CAPACITY_OBSERVABILITY=full make capacity TARGET=repository PROFILE=lifecycle MODE=diagnostic ...`
  reuses `compose.capacity.yml` for local Prometheus/OTLP/Tempo/Loki,
  bounded trace/phase-log exports and retained histories.
- Configure `CAPACITY_OTLP_URI`, `CAPACITY_TRACE_SAMPLE_RATIO`,
  `CAPACITY_API_PROFILE_URI` and `CAPACITY_GIT_METRICS_URI` before controlled
  replacement. Core telemetry uses typed `observability.otlp.uri`.
  Profiling/metrics values are full HTTP URIs, such as
  `http://127.0.0.1:6060` or `http://0.0.0.0:50052`.
- `CAPACITY_PROMETHEUS_GIT_TARGETS=capacity-git-service:50052` adds native
  metrics. `CAPACITY_OBSERVABILITY_KEEP=1` retains collectors for later
  queries. OTLP/HTTP metrics share collector port 4318 and Prometheus
  scrapes internal port 8889 as `gitstore-otlp-metrics-capacity`; existing
  direct scrapes and collector self-telemetry on port 8888 are unchanged.
- `CAPACITY_PROFILING=go CAPACITY_PROFILE_SECONDS=30 make capacity TARGET=repository PROFILE=lifecycle MODE=diagnostic ...`
  captures bounded CPU/heap/allocation/block/mutex/goroutine/execution
  profiles on both owned APIs during actual load. Use only the configured
  loopback profiler (default capture port 6060), never public profile
  ports. Profiles are private diagnostics and may contain runtime data;
  they are never acceptance evidence. Probe native perf without privilege
  escalation.
- `CAPACITY_PROFILE_NATIVE_SAMPLES=16` samples at most sixteen File
  conditional exchanges per API execution capture (default `0`, maximum
  `64`). Only sampled File exchanges consume the loopback `sample_limit`
  budget; retain at most four native trace IDs per exchange, with retries
  unchanged. `scylla.server_trace` events identify exact native sessions;
  admission never reads trace tables. Native tracing adds overhead and
  cannot provide unprofiled acceptance.

## Authorization capacity

- `make capacity TARGET=api PROFILE=authorization MODE=<diagnostic|alpha|production>`
  measures bounded per-case latency at maximum policy dimensions and
  concurrency 1/8/32, then concurrent valid/rejected reloads for 30 minutes
  with 32 decision workers. Export the evaluator call-graph dependency
  audit, latency histograms and post-GC reload heap evidence.
- `AUTHZ_CAPACITY_WARMUP`, `AUTHZ_CAPACITY_DURATION` and
  `AUTHZ_CAPACITY_RELOAD_DURATION` may shorten diagnostics only.
  Alpha/production require clean unchanged source, matching runtime
  manifests, per-case p99 <1ms, full durations and post-GC heap <=110%.
  This evaluator-only gate does not replace deployed readiness, replica
  semantics or lifecycle gates.

## Chaos

- `make chaos CHAOS_PROFILE=api-invalid-policy-reload CHAOS_TARGET=<container> CHAOS_COMPOSE_PROJECT=<owned-project> CHAOS_POLICY_DIR=<absolute-private-directory> CHAOS_CONFIRM=1`
  rejects malformed policy on one explicitly owned API, then restores
  the original file and active snapshot. Require a dedicated read-only
  directory bind and matching `owned-policy.json`; see `../chaos/README.md`.
- `CAPACITY_CHAOS_PROFILE=api-invalid-policy-reload` with
  `CAPACITY_CHAOS_POLICY_DIR` and the existing fault selectors runs inside
  a declared k6 or Go Repository lifecycle load window. Reject other Go
  capacity profiles and overlap with the full secret scenario's managed
  faults.
