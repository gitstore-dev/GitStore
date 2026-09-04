# GitStore capacity profiles

Capacity scenarios use k6 or focused Go harnesses for reproducible offered
load and threshold decisions. Run them only through the root Makefile:

```bash
make capacity TARGET=api PROFILE=readiness MODE=diagnostic \
  CAPACITY_BASE_URL=http://localhost:4000
```

Valid target/profile pairs are `api/readiness`, `namespace/admission`,
`namespace/validation`, `namespace/watch`, `namespace/recovery`, and
`scylla/soak`. Admission is deployed k6 load; validation is the in-process
two-replica soak. `CAPACITY_PROFILE` is now only an internal k6 dispatch detail.
The dispatcher is the only public capacity entry point.

Modes classify acceptance independently of workload. `diagnostic` records
results but can never set `passed: true`. `alpha` keeps correctness, error,
recovery, CPU, and memory requirements hard, enforces Namespace visibility p95
≤2s and p99 ≤3s, and warns above the unchanged 1s production p95 target.
`production` enforces visibility p95 ≤1s and p99 ≤3s. Require at least five
clean fixed-topology 10-minute repetitions before proposing another threshold
change.

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
fails on OOM, restart, disappearance, or stopped state. Namespace admission
alpha and production modes additionally require:

```bash
CAPACITY_RUNTIME_MEMORY_BYTES=17179869184
CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=<explicit container limit>
CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated # or password-authenticated
CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3
```

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
results there. Alpha/production profiles other than the non-mutating
`api-readiness` smoke test fail closed when their verifier is absent or not
executable.

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
  CAPACITY_OBSERVABILITY=prometheus
```

PromQL snapshots for admission, CDC discovery, journal materialization, and
subscriber delivery are stored in the evidence bundle. The API `git_commit`
stage isolates the Git-service boundary; finer Git marker-lock and reference
retry timing is emitted as bounded structured Git-service log fields without
reintroducing the removed Axum HTTP stack.
The Namespace watch harness seeds a bounded pool of 50 resources by default and
applies uniquely tagged updates to that pool. Override
`NAMESPACE_WATCH_CAPACITY_RESOURCE_POOL` only when resource cardinality is an
explicit experiment dimension; subscriber count, transition rate, bursts,
replay size, and duration remain independent acceptance dimensions.
The exporter defaults `CAPACITY_PROMETHEUS_LOOKBACK` to `90m`, covering the
load and stabilization windows instead of sampling only the quiet tail. Set a
longer Prometheus duration when the complete run exceeds 90 minutes.

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
  GraphQL errors, or latency/error threshold violations. It is the offered-load
  component of Namespace capacity validation; the existing Go watch verifier
  remains authoritative for replay, completeness, duplicates, and cursor
  recovery until its external-load mode is completed. Consequently, this
  profile cannot produce passing gate evidence until that verifier is wired in;
  use `MODE=diagnostic` for offered-load-only investigation.

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
