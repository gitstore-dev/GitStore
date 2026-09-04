# Quickstart: Namespace Watch Contract

## Test-first implementation sequence

1. Add failing schema tests for `watchNamespaces` and
   `NamespaceWatchEvent`.
2. Add failing authorization tests proving typed and generic Namespace watches
   require `namespace.watch` before cursor validation.
3. Add failing journal cursor/replay/expiry/overflow tests against the
   backend-neutral `WatchJournal` contract.
4. Add failing Scylla migration and CDC integration tests proving successful
   base writes have CDC rows and rejected/conflicting/no-op spec-047 operations
   do not create data events.
5. Add failing materializer tests for CDC classification, append-before-progress,
   duplicate recovery, lease fencing, journal bookmarks, and bounded batches.
6. Add failing resolver tests for typed payloads and identical generic/typed
   cursor streams.
7. Add failing two-replica transport tests: mutate through replica A, watch and
   resume through B, replace A, then reverse roles.
8. Implement migration 006, journal backends, materializer, resolver, AuthZ,
   metrics/readiness, and docs in that order.
9. Run the 60-minute sustained envelope and rolling replacement probe.
10. Run `make pr-ready`.

## Focused schema and resolver checks

```bash
cd gitstore-api
go generate ./...
go test -count=1 ./internal/graph/resolver ./internal/middleware/security
```

Expected contract coverage:

- `watchNamespaces` has no namespace argument;
- event field `namespace` is typed and nullable;
- generic Namespace path remains available;
- both paths use `namespace.watch`;
- denied callers receive `FORBIDDEN` before cursor errors;
- ADDED/MODIFIED contain full spec-047 Namespace state;
- DELETED/BOOKMARK contain no resource payload.

## Backend-neutral journal checks

```bash
cd gitstore-api
go test -count=1 ./internal/watchjournal/...
```

Cover:

- opaque cursor parsing and epoch mismatch;
- bootstrap high-water capture;
- resume strictly after cursor;
- seven-day/event-count expiry;
- 256-event batches and 100,000-event replay cap;
- subscriber overflow -> `WATCH_EXPIRED`;
- durable advanced BOOKMARK;
- identical typed/generic order;
- duplicate events accepted.

## Scylla CDC and materializer checks

Start the repository's Scylla test services, then:

```bash
make test-scylla-hardening
make test-scylla-integration SCYLLA_TEST_ADDR=127.0.0.1:9042
```

Required assertions:

- migration 006 enables full preimage/postimage CDC with 14-day TTL;
- CDC log and base writes share acknowledged success;
- successful create/update/status/finalizer/final-delete classify correctly;
- repeated `ALREADY_TERMINATING`, conflicts, and rejected admission do not
  create false data events;
- progress advances only after journal append;
- crash after append produces a safe duplicate;
- stale lease token cannot advance progress;
- migration 006 remains in supported rollback artifacts.

## Cross-replica GraphQL scenario

1. Start shared Scylla plus API replicas A and B.
2. Open typed watch on A using the bootstrap sentinel.
3. Receive BOOKMARK C, keep the socket open, and list all Namespaces through B.
4. Create through B, update through A, start termination through B, clear the
   finalizer/status through A, and complete deletion through B.
5. Verify ordered ADDED/MODIFIED/DELETED events on A with complete/null payload
   rules.
6. Persist the last cursor, close A, and resume through B.
7. Replace B under continued mutations and resume through the replacement.
8. Force retention expiry and subscriber overflow; verify `WATCH_EXPIRED`.
9. Repeat through `watchResources(kind: "Namespace")` and compare cursors.

## Consumer example

Bootstrap subscription:

```graphql
subscription BootstrapNamespaces {
  watchNamespaces(resourceVersion: "__namespace_watch_bootstrap__") {
    type
    name
    resourceVersion
    namespace {
      apiVersion
      kind
      metadata {
        name
        uid
        resourceVersion
        generation
        finalizers
      }
      spec {
        title
        tier
      }
      status {
        observedGeneration
        lastAppliedRevision
        conditions {
          type
          status
          reason
          message
        }
      }
      body
    }
  }
}
```

After receiving the first BOOKMARK cursor, keep the subscription open and page
the current snapshot:

```graphql
query ListNamespaces($first: Int, $after: String) {
  namespaces(first: $first, after: $after) {
    edges {
      cursor
      node {
        apiVersion
        kind
        metadata {
          name
          uid
          resourceVersion
          generation
          finalizers
        }
        spec {
          title
          tier
        }
        status {
          observedGeneration
          lastAppliedRevision
          conditions {
            type
            status
            reason
            message
          }
        }
        body
      }
    }
    pageInfo {
      hasNextPage
      endCursor
    }
  }
}
```

Resume after restart:

```graphql
subscription ResumeNamespaces($cursor: String!) {
  watchNamespaces(resourceVersion: $cursor) {
    type
    name
    resourceVersion
    namespace {
      metadata {
        name
        uid
        resourceVersion
        generation
        finalizers
      }
      spec {
        title
        tier
      }
      status {
        observedGeneration
        conditions {
          type
          status
          reason
        }
      }
    }
  }
}
```

On `extensions.code == "WATCH_EXPIRED"`, discard the cursor and repeat
bootstrap/list. On `WATCH_UNAVAILABLE`, retry with bounded backoff without
trusting the old cache as current.

## Capacity and recovery validation

Run for 60 minutes with:

- two API replicas;
- 10 sustained Namespace transitions/second;
- 100 transitions/second bursts;
- 1,000 concurrent subscriptions split across replicas;
- one rolling replica replacement;
- forced CDC reader restart and lease handoff;
- 10,000-event replay probes.

Pass thresholds:

- event visibility p95 ≤1 second and p99 ≤3 seconds;
- replay of 10,000 events p95 ≤5 seconds;
- internal errors <0.1%;
- zero missing acknowledged transitions;
- recovery within 30 seconds;
- CPU <80% per replica;
- retained-memory growth <10%;
- no normal stream closure after overflow/discontinuity.

These remain the production objectives. Early-alpha local-environment runs use
`MODE=alpha`, with a provisional hard visibility ceiling of p95 ≤2
seconds and p99 ≤3 seconds and a warning whenever p95 exceeds the unchanged
1-second production target. Diagnostic runs never produce passing gate
evidence. Correctness, error, recovery, CPU, and retained-memory checks remain
hard in every mode. Require five clean fixed-topology 10-minute repetitions
before reconsidering either latency policy.

Run offered load through the repository harness and attach sanitized effective
configuration/environment manifests:

```bash
make capacity TARGET=namespace PROFILE=admission MODE=alpha \
  CAPACITY_API_A=http://api-a:4000/graphql \
  CAPACITY_API_B=http://api-b:4000/graphql \
  CAPACITY_TOKEN_FILE=/absolute/path/to/token \
  CAPACITY_CONFIG_MANIFEST=/absolute/path/to/effective-config.json \
  CAPACITY_ENVIRONMENT_MANIFEST=/absolute/path/to/environment.json \
  CAPACITY_API_REPLICAS=2 \
  CAPACITY_SCYLLA_NODES=3 \
  CAPACITY_SCYLLA_SMP=2 \
  CAPACITY_GIT_SERVICE_BUILD=release
```

### Current gate status

The production capacity gate remains **not passed**. Five fixed-topology
10-minute **alpha** repetitions passed on 2026-09-04 after the fail-closed CDC
frontier and retained-batch-memory fixes. Every qualifying bundle records the
same dirty source-state fingerprint
`8e6e6b5f9c82489c0e1d1965680027c8cf7e20c17e26ba3ef30a70be05e9aeba`:

| Run | Transitions | Visibility p95 / p99 | Replay p95 | CPU A / B | Retained RSS A / B |
|---|---:|---:|---:|---:|---:|
| `alpha-final-01-20260904T0709Z` | 6,900 / 6,900 | 1.438s / 1.638s | 302ms | 3.21% / 2.91% | 5.48% / 5.17% |
| `alpha-final-02-20260904T0738Z` | 6,900 / 6,900 | 1.600s / 2.100s | 185ms | 3.18% / 2.83% | 6.29% / 3.91% |
| `alpha-final-03-20260904T0807Z` | 6,899 / 6,899 | 1.529s / 1.985s | 189ms | 2.88% / 3.15% | 4.03% / 0.99% |
| `alpha-final-05-20260904T0908Z` | 6,899 / 6,899 | 1.493s / 1.897s | 140ms | 3.25% / 3.10% | 2.76% / 4.47% |
| `alpha-final-06-20260904T0936Z` | 6,899 / 6,899 | 1.506s / 1.953s | 161ms | 3.04% / 2.89% | 5.68% / 5.88% |

All five qualifying runs used 1,000 subscribers and 1,000 replay-preparation
events, reported zero failures, backpressure, missing acknowledged transitions,
duplicates, or drain timeouts, and stayed below the alpha CPU/RSS and p95 ≤2s /
p99 ≤3s limits. A separate preserved attempt,
`alpha-final-04-20260904T0837Z`, failed retained RSS on API B at 12.20% and is
not counted. These results satisfy the provisional alpha repetition rule only:
every qualifying p95 remains above the production 1-second target, and the
60-minute midpoint-replacement gate was not run.

Earlier diagnostic runs found:

- the best clean 10-minute two-replica/1,000-subscriber stage prepared 1,000
  replay events in 15.143s, caught the journal up in 630ms, admitted and
  delivered all 6,899 transitions without failures, backpressure, missing
  events, or duplicates, and stayed below CPU/RSS limits; visibility was p50
  655ms, p95 1.448s, and p99 1.908s, so only the production p95 ≤1s assertion
  failed;

- the former in-process load scheduler dropped 34,481 of 41,899 scheduled
  transitions and admitted only 4,101 of 7,418 attempted transitions;
- its external replacement watcher lacked Docker permission, so the recorded
  replacement failure was an orchestration failure rather than valid recovery
  evidence;
- the reusable k6 profile passed a three-second 1 transition/s smoke with zero
  drops and approximately 474 ms p95 mutation latency;
- a 30-second 10 transitions/s diagnostic exhausted 120 VUs, dropped 181
  iterations, failed all 120 issued mutations, and measured approximately
  48.5 s p95; and
- that environment used `target/debug/git-service`, while every GraphQL
  Namespace mutation takes the per-repository write lock to commit into the
  shared system repository. Debug-artifact results cannot establish production
  capacity.

The 2026-09-01 clean release-artifact attempt prepared 10,000 acknowledged
transitions in 3m19.7s and caught the durable journal up in 1.04s. A short
10 transitions/s gate smoke admitted 100/100 with zero conflicts, errors, or
drops (approximately 46 ms p95 and 52 ms p99). The full 60-minute run still
failed: of 41,899 offered transitions, 32,814 entered the bounded queue,
31,550 were admitted and acknowledged, 1,264 attempts failed, and 9,085 were
backpressured. Pumba successfully restarted API A; its 500 subscribers resumed,
it reacquired the materializer lease, and observed CDC lag returned below one
second. Five minutes later the three-node local Scylla cluster suffered gossip
timeouts and connection resets; node 3 was OOM-killed and restarted. This is
environment-capacity failure evidence, not support for increasing application
timeouts or buffers.

Before rerunning the unchanged 60-minute workload, provision fixed and
explicitly recorded per-node Scylla memory/CPU, require zero OOM kills and
unexpected datastore restarts, retain release artifacts and the clean isolated
keyspace/Git directory, and repeat the short stepped preflight. Only after that
environment passes should a three-repetition, one-variable-at-a-time matrix be
used to choose application defaults. Only a clean run satisfying every
threshold may complete task T061.

## Documentation and final gate

Update:

- `docs/namespace/namespace-watch.md`;
- `docs/configuration.md`;
- the controller watch/status runbook with Namespace-specific expiry,
  materializer lag, and recovery procedures;
- deployment/migration guidance for migration 006 and mixed-version deny.

Then run:

```bash
make pr-ready
```

## Validation evidence (2026-08-30)

Completed:

- focused schema, authorization, resolver, journal, memdb, and Scylla-hardening
  suites;
- tagged Scylla datastore integration, including the official
  `scylla-cdc-go` reader materializing an acknowledged Namespace mutation;
- the deployment-shaped two-replica recovery gate on a fresh Scylla keyspace:
  migration 006 completed in 887 ms with its corrected physical schema, all
  three probes passed in 5.702 seconds (6.13 seconds wall), cross-replica event
  visibility was 872.7 ms, fenced lease handoff advanced token 1 to 2 in about
  4.975 seconds, and the replacement replica became ready in 48 ms;
- the original in-process `internal/watchjournal` capacity smoke and 60-minute
  run completed, but review correctly determined that they did not exercise
  deployed GraphQL/WebSocket transports, two API processes, or Scylla and
  therefore do **not** count as PR-003/T061 evidence;
- the replacement gate is deployment-driven from
  `tests/integration/namespace_watch_capacity_test.go`: it requires two
  distinct API URLs, a replacement endpoint naming one of them, a trigger file
  watched by the deployment harness, and an authenticated token;
  creates acknowledged Namespace transitions through both GraphQL endpoints;
  splits real WebSocket subscriptions across both replicas; replays 10,000
  retained events through GraphQL; checks sustained and burst scheduling,
  missing transitions, stream errors, p95/p99 visibility, replacement recovery,
  requires the triggered endpoint to become unavailable and return with a new
  `process_start_time_seconds`, resumes a peer-issued cursor through that new
  process, reconnects replacement-disrupted subscribers from each socket's last
  observed cursor while preserving GraphQL terminal errors, and enforces
  GOMAXPROCS-normalized per-process CPU and resident memory from `/metrics` (a
  short smoke may explicitly relax only the CPU/resident assertions);
- the focused integration test compile and `go vet ./...` passed;
- a short deployed smoke against two API containers and a fresh shared Scylla
  keyspace acknowledged 50 Namespace mutations split across both GraphQL
  endpoints, then failed its bounded replay with
  `WATCH_UNAVAILABLE/MATERIALIZER_NOT_READY`; both `/health` and `/ready`
  subsequently reported healthy, but the authoritative Namespace rows were
  `watch_committed=true` while the corresponding journal window contained only
  `BOOKMARK` records. No replacement was triggered and this failed smoke is not
  T061 evidence;
- after commit `4e7a38f` corrected false-to-true commit promotion, a 12-second
  functional deployment smoke with 20 subscribers and 50-event replay reached
  the final latency assertion after passing real API-A outage/restart identity,
  cursor-resumed subscriber failover, zero-missing delivery, replay, mutation
  error, and recovery assertions. Its p95 was 5.47 seconds because the forced
  replacement and lease-handoff interval dominated the intentionally tiny
  sample; this is functional smoke evidence only, and the production latency
  threshold remains unchanged for the 60-minute gate;
- the first production-size attempt stopped after 970.81 seconds at the end of
  replay preparation: 9,999 of 10,000 distinct GraphQL Namespace creates were
  acknowledged, while transition 109 returned the spec-047 retryable
  `NAMESPACE_CONFLICT` / `RESOURCE_VERSION_CONFLICT`. Replay, the 60-minute
  soak, and replacement did not start. The deployment harness alternates
  replicas with capped exponential backoff, confirms ambiguous commit outcomes
  by querying both replicas, and still fails permanent GraphQL errors
  immediately; focused tests cover peer conflict retry, permanent-error
  rejection, and ambiguous-success read-back;
- the next production-size attempt ran the `cdc6711` hot-path binaries for
  3,075.40 seconds, then stopped during replay preparation when transition
  3,382 exhausted all six alternating-replica attempts with the same retryable
  `NAMESPACE_CONFLICT` / `RESOURCE_VERSION_CONFLICT`. Both replicas remained
  ready, the journal high-water reached 21,400, leader CDC lag was about 4.3 ms,
  append errors remained zero, and `replay_events_total` remained zero. The
  10,000-event replay, 1,000-subscriber soak, replacement, and CPU/RSS gates did
  not start, so this is failure evidence rather than T061 completion;
- a fresh-keyspace `23bc618` attempt proved single-writer seeding avoids those
  conflicts but was stopped after 374 seconds because it produced only 228 seed
  events (about 36.6/minute), implying more than four hours before the gate
  could complete. Both replicas remained ready, CDC lag was about 2.3 ms,
  append errors and replay remained zero, and no subscribers, soak, replacement,
  or T061 measurement started. Parallel seeding is restored with a two-minute
  per-transition retry window and 500 ms capped backoff; measured-load
  thresholds and the 20-worker sustained/burst phase are unchanged;
- the fresh-keyspace `d7dac82` attempt acknowledged all 10,000 GraphQL creates
  in 15m50.95s, then failed before measured replay because only 7,292 journal
  events had been materialized and the 2,710-event backlog caused
  `WATCH_UNAVAILABLE/MATERIALIZER_NOT_READY`. No soak, replacement, or T061
  measurement started. The gate now waits up to a separately bounded 15-minute
  setup interval for those exact 10,000 names to become durably observable
  before starting the unchanged 5-second replay measurement. The Scylla hot
  path also avoids repeating clock initialization for every event and leaves
  checkpoint ownership with the CDC sequencer, removing two redundant fenced
  writes without weakening append-before-progress or published-frontier
  ordering. A fresh production-size rerun remains required;
- the 2026-09-01 staged rerun used Docker Desktop with 17,775,386,624 bytes,
  three fresh Scylla 2026.1 nodes at 3,221,225,472 bytes and two shards each,
  RF=3, two exact-`c3f4f4f` API replicas, and the exact-head release Git
  service. The 10-minute offered-load diagnostic acknowledged 6,001 Namespace
  creates at 10/s with zero GraphQL or HTTP errors, zero dropped iterations,
  mutation p99 71.12 ms, and stable Scylla container verification. Two earlier
  setup-only attempts were retained as failure evidence (an expired token and
  base URL supplied where the k6 profile required `/graphql`); neither offered
  load or mutated the datastore;
- the following 10-minute/1,000-subscriber diagnostic stopped before opening
  subscribers or measuring replay. It acknowledged all 10,000 setup creates
  through both replicas in 6m09.09s, but the journal high-water stalled near
  11,209, CDC lag continued increasing, and none of the 10,000 run-specific
  transitions became replay-visible within the separate 15-minute catch-up
  bound. The terminal error was
  `WATCH_UNAVAILABLE/MATERIALIZER_NOT_READY`; append errors remained zero and
  all three Scylla nodes finished running with zero OOM kills or restarts.
  Post-run durable-state inspection found the published frontier fixed at
  16:54:42Z and stale-lease termination on both replicas. Lease renewal,
  journal high-water publication, the global frontier, and per-stream CDC
  checkpoints all issue LWTs in the same `namespace_watch_clock` partition;
  catch-up traffic starved the 30-second lease renewal and fenced each leader
  before the backlog drained.
  Burst and full replacement stages were not run, so this is failure evidence
  and not a capacity pass;
- after batching idempotent journal appends ahead of one checkpoint per stream
  and one global-frontier publication per CDC batch, a fresh RF=3 rerun
  acknowledged all 10,000 setup transitions in 3m51.82s and made the complete
  run-specific journal window replay-visible 748ms later. The subsequent
  10-minute diagnostic admitted and acknowledged 6,000/6,000 transitions with
  zero failures, backpressure, missing events, or duplicate deliveries while
  serving 1,000 subscribers. The materializer retained leadership, reported
  zero append errors and sub-second leader lag at completion, and all three
  Scylla containers passed the no-OOM/no-restart stability comparison. This
  confirms the stale-lease catch-up regression is fixed under the staged
  workload;
- that batching-fix diagnostic still failed the independent retained-memory
  threshold: API A grew 38.06% against the required less-than-10% bound. Burst
  and full replacement stages were therefore not run. This remains partial
  failure evidence rather than a production capacity pass;
- follow-up runtime metrics showed that API A's RSS fell from 158.1 MB at the
  immediate post-load sample to 83.6 MB in the same process, with 22.2 MB of
  live Go heap, 102.3 MB released to the operating system, zero restarts, and
  zero OOM kills. The deployment harness now measures retained memory as the
  lowest RSS from a bounded five-minute post-load stabilization window while
  preserving the immediate load-end CPU counter and elapsed time; focused
  tests cover the RSS floor, allow sub-second process-start collector jitter,
  and reject real process identity changes. CPU/RSS evidence is asserted before
  fail-fast latency checks so independent gate results are not lost;
- a fresh-keyspace validation of that harness change again acknowledged all
  10,000 setup transitions (4m20.64s) and caught the durable journal up in
  641ms, but failed independent load gates before its memory assertion: 78 of
  5,998 mutation attempts failed and visibility p95 was 2.714s. API logs tied
  the failures to a transient Scylla quorum loss (`SERIAL` required two live
  replicas but observed one); all three containers retained zero restarts and
  OOM kills and later reported Up/Normal. This is datastore-environment failure
  evidence, so burst and full replacement stages remain blocked;
- the transient quorum loss was simultaneous across all three nodes: each
  gossip failure detector marked both peers down for roughly 0.1-1.4 seconds,
  then restored them without a container restart. The supposedly fresh-keyspace
  run had reused Scylla volumes and was concurrently performing tablet cleanup
  for the previous keyspace, with roughly 8.5 GiB block I/O per node. Repeating
  on empty volumes removed the quorum failure and passed the 10-minute
  functional workload: 5,999/5,999 acknowledged, zero errors/backpressure/
  missing/duplicates, visibility p95 841ms and p99 936ms, replay p95 2.302s,
  sub-second CDC lag, and zero append errors. Metrics were skipped in that
  repetition because exact floating-point process-start comparison rejected
  collector jitter, which the harness now tolerates;
- a required-metrics empty-volume repetition passed 6,000/6,000 functional
  transitions with zero errors or backpressure but proved two minutes was too
  short for Go scavenging: API A reported 47.18% RSS growth at the assertion,
  then fell from 161.1 MB to 95.6 MB without restart one additional idle minute
  later as released heap rose to 70.3 MB. The stabilization bound is therefore
  five minutes. A following empty-volume repetition was not a stage pass: it
  acknowledged 5,837/5,837 attempted mutations with zero mutation errors but
  backpressured 163 of 6,000 produced transitions and measured 1.705s visibility
  p95. Scylla again had zero OOM kills or restarts. Stage 2 is not yet
  repeatably passing, so burst and full replacement stages remain blocked;
- a later full-duration attempt is retained as invalid environment evidence:
  only 116 MiB remained on the Docker disk, 6,598 of 41,897 produced
  transitions were backpressured, and 1,277 mutation attempts failed. Its API
  A replacement-memory comparison also used the replacement process's
  immediate ready-state RSS instead of the warmed pre-load baseline, producing
  a false 208% growth result. The harness now applies the same warmed baseline
  to both process segments and has focused coverage for that calculation;
- cross-replica GraphQL writes then exposed two independent Git ordering
  defects. Namespace admission treated every intervening disjoint manifest
  commit as superseding the requested commit, and `CommitFile` relied only on
  a process-local repository lock. Admission now accepts a newer head when the
  target path still contains the committed bytes. The Git service now holds a
  repository-local marker lock across `CommitFile` so separate processes
  sharing a bare repository cannot lose sibling updates, and it retries
  bounded optimistic-reference conflicts from writers outside that convention
  after reopening at the latest head. A two-service-instance regression test,
  stress-repeated ten times, verifies that all concurrent disjoint files
  survive. Successful per-RPC authorization logging was moved to debug to keep
  the storage hot path bounded;
- after reclaiming the failed-run data and recreating all three Scylla volumes
  plus the Git directory, a 10-minute/1,000-subscriber required-metrics stage
  prepared 1,000 replay transitions in 15.14s and caught the journal up in
  630ms. It admitted and acknowledged 6,899/6,899 loaded transitions with zero
  errors, backpressure, missing events, duplicate delivery, or drain timeout.
  Normalized CPU was 4.40%/3.39% and retained RSS growth after five minutes of
  stabilization was 4.86%/3.87%. The stage still failed only the visibility
  SLO: p50 655ms, p95 1.448s, p99 1.908s, maximum 2.406s;
- the first complete 60-minute deployment run exposed a harness-cardinality
  defect: creating a new Namespace for every transition continuously enlarged
  the Git tree and measured resource creation rather than watch delivery. It
  still exercised replacement and resource gates, but only 16,390 of 41,900
  produced transitions were acknowledged before the bounded drain expired.
  The harness now seeds a bounded Namespace pool and applies uniquely tagged
  updates to it for replay, sustained traffic, bursts, and recovery. The
  default is 50 resources, making resource cardinality explicit and bounded
  while leaving the 1,000-subscriber, transition rate, burst, replay, and
  duration requirements unchanged;
- controlled bounded-pool preflights isolated the CDC confidence window and
  workload cardinality. A fresh 50-resource, 1,000-subscriber, two-minute run
  at an explicit 250ms confidence window acknowledged 1,300/1,300 transitions
  with zero failures, backpressure, missing events, or duplicates; visibility
  was p95 899.985ms and p99 1.085108s, normalized CPU 4.48%/4.26%, and retained
  RSS growth 0.54%/3.42%. The checked-in 500ms confidence default is retained:
  one short pass is not the required repeated fixed-topology matrix for an
  application-default change;
- production evidence
  `production-pool50-60m-20260904` ran two API processes against a fresh
  three-node Scylla 2026.1 cluster, a release Git-service image, a 250ms
  explicitly configured confidence window, 1,000 WebSocket subscribers, a
  10,000-event replay, 60 minutes at 10 transitions/second with 100-transition
  bursts, and a real API-A midpoint restart. Replay preparation completed in
  3m02.292s and journal catch-up in 544ms. All 41,900 produced transitions were
  enqueued, attempted, admitted, and acknowledged with zero backpressure,
  failures, drain timeout, missing events, or duplicate deliveries. API-A
  returned with a new process identity in 3.002s. Normalized CPU was 5.70% and
  5.40%; retained RSS growth was 7.01% and 4.17%. Visibility p99 passed at
  1.739540s, but p95 was 1.308777s and failed the unchanged 1s production SLO.
  Prometheus phase evidence was exported successfully. T061 therefore remains
  intentionally open; neither the threshold nor the task is weakened to turn
  this otherwise-clean failure evidence into a pass;
- `make pr-ready` across Go, Rust, static analysis, formatting, and license
  checks;
- `graphify update .` followed by a query that surfaced the Namespace CDC
  progress, materializer, journal cursor, readiness, and typed/generic watch
  nodes.

Commands exercised:

```bash
make test-scylla-hardening
make test-scylla-integration SCYLLA_TEST_ADDR=127.0.0.1:9042
NAMESPACE_WATCH_API_A=http://127.0.0.1:4100 \
NAMESPACE_WATCH_API_B=http://127.0.0.1:4101 \
NAMESPACE_WATCH_API_REPLACEMENT=http://127.0.0.1:4100 \
NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE=/private/tmp/gitstore-watch-smoke-replace \
NAMESPACE_WATCH_TOKEN="$TOKEN" \
  make capacity TARGET=namespace PROFILE=recovery MODE=diagnostic
NAMESPACE_WATCH_API_A=http://127.0.0.1:4100 \
NAMESPACE_WATCH_API_B=http://127.0.0.1:4101 \
NAMESPACE_WATCH_API_REPLACEMENT=http://127.0.0.1:4100 \
NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE=/private/tmp/gitstore-watch-capacity-smoke-replace \
NAMESPACE_WATCH_TOKEN="$TOKEN" \
  make capacity TARGET=namespace PROFILE=watch MODE=diagnostic \
    NAMESPACE_WATCH_CAPACITY_DURATION=12s \
    NAMESPACE_WATCH_CAPACITY_SUBSCRIBERS=20 \
    NAMESPACE_WATCH_CAPACITY_REPLAY_EVENTS=50 \
    NAMESPACE_WATCH_CAPACITY_REPLAY_SAMPLES=3 \
    NAMESPACE_WATCH_CAPACITY_REPLACEMENT_DELAY=5s \
    NAMESPACE_WATCH_CAPACITY_BURST_INTERVAL=2s \
    NAMESPACE_WATCH_CAPACITY_BURST_SIZE=10
STATICCHECK_CACHE=/private/tmp/gitstore-050-staticcheck \
  GOCACHE=/private/tmp/gitstore-050-gocache make pr-ready
graphify update .
graphify query "How does Namespace CDC flow through the durable watch journal materializer to typed and generic GraphQL subscriptions, including readiness and cursors?"
```

Repeat the still-pending PR-003/T061 gate after reducing measured p95 below one
second without weakening correctness or the threshold. Use two distinct API
processes backed by the same fresh Scylla deployment. The deployment harness
must watch `NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE` and restart the process
addressed by `NAMESPACE_WATCH_API_REPLACEMENT` in place after the test writes
that file. The gate must observe both the outage and a changed
`process_start_time_seconds`; merely pointing at an already-live endpoint is
not replacement evidence:

```bash
NAMESPACE_WATCH_API_A=http://api-a:4000 \
NAMESPACE_WATCH_API_B=http://api-b:4000 \
NAMESPACE_WATCH_API_REPLACEMENT=http://api-a:4000 \
NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE=/var/run/gitstore/watch-capacity-replace \
NAMESPACE_WATCH_TOKEN="$TOKEN" \
NAMESPACE_WATCH_CAPACITY_REPLAY_CATCHUP_TIMEOUT=15m \
NAMESPACE_WATCH_CAPACITY_RESOURCE_POOL=50 \
  make capacity TARGET=namespace PROFILE=watch MODE=production
```
