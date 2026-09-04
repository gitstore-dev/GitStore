# Production Readiness Testing Patterns

The constitution's Principle 4 ("Production Readiness") is non-negotiable:
load/soak, failover, rolling-upgrade, and security/capacity runbook
validation must exist before a feature affecting `gitstore-api`,
`gitstore-controller-manager`, or `gitstore-git-service` is considered
production-ready (`.specify/memory/constitution.md`, Quality Gates: "Core-service
changes document and test behavior with multiple replicas," "Load-bearing
changes meet declared capacity and sustained-load objectives," "Protected
operations include authentication and authorization tests," "Contract
changes document compatibility, rollout, and rollback").

Every spec so far (046, 048, 052, ...) has independently reinvented the same
four structural test patterns to satisfy that gate. There is no missing code
infrastructure here — `go test`, build tags, and env-var gating are enough —
what was missing was a written convention. This page is that convention. A
spec's `tasks.md` "production readiness" phase should **reference this
runbook** (`docs/runbooks/production-readiness-testing.md`) rather than
re-deriving the approach, and should link to the concrete test file(s) it
added for each applicable pattern.

Not every pattern applies to every spec. A leaf, read-only resource with no
concurrent writers and no schema evolution may only need pattern 4. Judge
applicability the same way `feedback_reconciler_fanin_check` judges
reconciler fan-in: check what actually changed, don't skip a pattern just
because the resource "looks simple."

## 1. Multi-replica correctness

**What it verifies:** that running the same reconciler/handler logic as more
than one independent instance — modeling multiple controller-manager
replicas, or the same replica before and after a restart — never double-applies
a side effect, never leaves work permanently stuck, and resolves conflicts
safely. This is **not** literal multi-process or multi-container testing.
The pattern is: build an in-process fake/mock of whatever external client the
reconciler calls, guard its internal counters with a `sync.Mutex`, then drive
two or more independent reconciler/manager instances against the *same* fake
and assert on the fake's counters afterward.

**Correction (verified 2026-08-29 against a review finding):** an earlier
version of this section implied that a goroutine-driven shape in this repo
demonstrates true concurrent-replica interleaving. It does not, and after
checking every `go func` call site under
`gitstore-controller-manager/tests/integration/`
(`reconcile_retry_resume_test.go`, `product_category_count_test.go`,
`disconnect_reconnect_test.go`, `status_conflict_test.go`,
`observability_test.go`, `poison_item_test.go`), **no test in this codebase
currently exercises two reconciler/manager instances processing the same
work item at overlapping wall-clock time.** Every existing example is one of
two honest shapes, both of which model "two replicas" as strictly
*sequential* generations, not a race:

- **Sequential dual-instance (idempotency across restart), no goroutines
  needed.** Worked example:
  `gitstore-controller-manager/tests/integration/categorytaxonomy_deletion_test.go`,
  `TestIntegration_CategoryDeletionResumesAfterControllerRestart`. It builds a
  `restartDeletionClient` fake with a `sync.Mutex`-guarded `decouples`/
  `completions` counter pair and a `remainingPage bool` that simulates one
  bounded continuation still outstanding. A `firstController` reconciler
  instance reconciles once and is asserted to leave a `types.RequeueAfter`
  (the bounded page isn't drained yet); `Reconcile` returns before the second
  instance is even constructed. A **second, independently constructed**
  reconciler instance (`secondController`, same shared fake) is then
  reconciled and asserted to reach `types.Success`, with the fake's counters
  proving the drain-then-complete sequence ran exactly once end-to-end
  (`decouples == 2`, `completions == 1`). A final reconcile after the record
  is deleted from the cache asserts a `types.TerminalFailure` and that
  `completions` is still `1` — a stale requeue after completion must not run
  `CompleteDeletion` a second time. There are no `go func` goroutines in this
  test at all.
- **Real Runner+Manager pair, torn down before the "second replica" starts.**
  Worked example:
  `gitstore-controller-manager/tests/integration/reconcile_retry_resume_test.go`,
  `TestIntegration_Restart_ResumesFromCheckpoint_NoLostOrDuplicateWork` (the
  same restart-teardown shape also appears in
  `product_category_count_test.go`'s
  `TestIntegration_ProductCategoryCount_SurvivesRunnerRestart`). Run 1 starts
  a real `listwatch.Runner` and `manager.Manager` each in their own goroutine
  (`go func() { runnerDone1 <- runner1.Run(ctx1) }()` /
  `go func() { mgrDone1 <- mgr1.Start(ctx1) }()`) — those goroutines exist so
  the Runner and Manager can run their own blocking loops *within one
  generation*, not so that generation 1 and generation 2 overlap. Run 1
  reconciles one item to success, then is **fully torn down and drained**
  before generation 2 is even constructed: `cancel1(); <-runnerDone1;
  <-mgrDone1` all execute, and only afterward does the test build `runner2`/
  `mgr2` against the same `checkpoint.Store`. So this is "restart," not
  "concurrency" — it proves no-lost/no-duplicate-work across a clean
  sequential handoff, not safety under two replicas racing the same key at
  the same time.

**Required bar for "multi-replica correctness" — the sequential shapes above
do not satisfy it on their own.** A production-readiness task titled
"multi-replica correctness" (or equivalent constitution language about
"behavior with multiple replicas") means two replicas may reconcile the same
key *simultaneously*, not one-after-another. Neither shape above proves
that, because in both, one reconciler/Runner+Manager instance's `Reconcile`
call (or its full teardown) completes before the second instance is even
constructed — there is no overlap. **A test only satisfies the
multi-replica-correctness gate if it forces genuine overlap:**

1. Two reconciler/handler instances, each driven from its own goroutine.
2. An explicit synchronization barrier between them — e.g. both goroutines
   send on (or receive from) a shared unbuffered channel, or both block on a
   `sync.WaitGroup.Wait()` released only once both have called `Done()` on
   arrival — so that neither instance's call into the datastore/fake
   proceeds until *both* are guaranteed to be mid-flight at the same time.
   Without this barrier, Go's scheduler gives no guarantee two goroutines
   actually overlap; "I used `go func()` twice" is not sufficient by itself.
3. Both instances then call `Reconcile` (or attempt the same conflicting
   mutation) while genuinely interleaved, against one shared mutex-guarded
   fake or a real datastore's actual optimistic-concurrency path
   (`resourceVersion` CAS or equivalent).
4. The assertion is on the *outcome of that conflict*, not just "both
   finished": exactly one side must win the CAS and the other must observe
   a clean, well-typed conflict/retry (`ErrConflict` or equivalent) — not
   corrupted merged state, not two winners, not a silently duplicated side
   effect (e.g. `CompleteDeletion` called twice, a decouple counter
   double-incremented).

**No test in this codebase today meets this bar.** Both shapes documented
above (the sequential dual-instance fake and the real Runner+Manager
restart test) are restart-safety tests, not concurrency tests, and are
**not** an acceptable substitute when a spec's task is specifically
"multi-replica correctness" — don't check that task off by pointing at
either of them. They remain useful and worth keeping for what they actually
prove (idempotency across a restart), but a new resource's
multi-replica-correctness task requires writing the barrier-synchronized
test described above from scratch; nothing in this repo can be copied
as-is for the overlap requirement.

**How to apply this to a new resource:**

1. Identify every external client/side effect the reconciler under test
   calls (a status client, a decouple/mutation RPC, a completion call).
   Build a fake with `sync.Mutex`-guarded counters for each, or use the
   real datastore's conflict path if you're testing at that layer.
2. For restart-safety (necessary but not sufficient on its own): pick the
   sequential-dual-instance or real-Runner/Manager-restart shape above
   based on whether you're also validating checkpoint/dispatch machinery.
   Tear the first instance down fully before constructing the second, and
   say explicitly in the test's doc comment that this proves restart-safety,
   not concurrent-replica safety — don't let the naming imply more than the
   test proves.
3. Assert exact counts on the fake after the second instance runs in the
   restart-safety test — "ran exactly once," "did not re-run a completed
   step," "a stale reconcile after completion is a terminal no-op."
4. **Required, separately, for the multi-replica-correctness gate itself:**
   write a new test with two goroutines synchronized through an explicit
   barrier (a channel or `WaitGroup`) so both reconciler/handler instances'
   mutation calls genuinely interleave against the same fake or real
   datastore CAS path. Assert exactly one clean winner and one clean
   conflict/retry — never corrupted state or a duplicated side effect. Do
   not substitute a sequential test for this step and mark the task done;
   as of this writing, no worked example of this shape exists anywhere in
   this codebase, so you are writing the first one, not adapting an
   existing pattern.

## 2. Capacity/soak testing

### Repository-wide capacity and fault harness

New load-bearing specifications MUST use the repository-wide harness instead
of adding another in-process load scheduler:

```bash
make capacity TARGET=api PROFILE=readiness MODE=diagnostic \
  CAPACITY_ENV_FILE=/absolute/path/to/non-committed-capacity.env
```

The runner uses the pinned `grafana/k6:2.1.0` image unless a local `k6` binary
or `K6_BIN` is supplied. Profiles live under `tests/capacity/profiles/`, shared
JavaScript modules live under `tests/capacity/lib/`, and every invocation writes
an evidence bundle under
`.gitstore/capacity/<target>/<profile>/<mode>/<run-id>/`. The bundle
contains the Git revision and dirty state, tool identity, k6 log, structured
summary, timestamps, exit code, and pass/fail status. Tokens belong in the
untracked environment file and MUST NOT be copied into evidence.

Valid target/profile pairs are `api/readiness`, `namespace/admission`,
`namespace/validation`, `namespace/watch`, `namespace/recovery`, and
`scylla/soak`. Admission is deployed k6 load; validation is the in-process
two-replica soak. Diagnostic mode
cannot produce passing evidence. Alpha mode is an explicitly provisional local
environment gate: Namespace-watch visibility p95 must be ≤2 seconds and p99
≤3 seconds, with a warning above the unchanged 1-second production p95 target.
Production mode keeps p95 ≤1 second and p99 ≤3 seconds. Correctness, errors,
recovery, CPU, and retained-memory requirements remain hard in every mode.

Set `CAPACITY_OBSERVABILITY=prometheus` on `make capacity` when phase
attribution is required. The dispatcher starts and removes the optional scraper
and retains SLO-focused API admission, CDC/materializer, and delivery queries
in the evidence bundle. Set `CAPACITY_PROMETHEUS_TARGETS` to the comma-separated
`host:port` addresses of the deployed API replicas as they are reachable from
the Prometheus container. Each run uses an ephemeral TSDB and derives its
default query window from the evidence start time, preventing samples from a
previous experiment from contaminating the result. The
API `git_commit` stage is the Prometheus boundary for Git-service latency;
advisory-lock waits and optimistic-reference retries remain structured Rust
log fields because the legacy Axum metrics surface has been removed. Advisory
lock ownership is released by the OS on process termination, so a stale lock
file does not block replacement replicas.
Query requirements follow the selected profile: `api/readiness` requires only
healthy scrape targets, Namespace admission adds admission/datastore signals,
and watch/recovery add CDC, materializer, and delivery signals. A missing lazy
datastore-error series is recorded as zero rather than treated as scrape loss.
Namespace watch capacity uses a bounded 50-resource update pool by default, so
the watch gate measures transition delivery instead of unbounded Git-tree
growth. Change `NAMESPACE_WATCH_CAPACITY_RESOURCE_POOL` only for a declared
resource-cardinality experiment; it does not replace the 1,000-subscriber or
sustained/burst transition targets.
Alpha and production watch evidence requires a run of at least 60 minutes,
1,000 subscribers, 10,000 replay events, bursts of at least 100 transitions,
and a burst interval shorter than the run. Use diagnostic mode for smaller
experiments.

A k6 pass proves only that the declared offered load and metric thresholds
passed. Each feature profile MUST pair it with a domain correctness verifier
when correctness cannot be expressed by k6 checks—for example exact watch
event coverage, cursor replay, ordering, datastore row-count proof, or
controller convergence. A production-readiness claim requires all of:

1. validated production-like topology and dataset scale;
2. zero unintended k6 dropped iterations;
3. passing latency, throughput, and error-rate thresholds;
4. passing feature-specific correctness verification; and
5. passing declared fault/recovery experiments during active load.

The environment verifier MUST also prove that every datastore node stayed up
for the full measurement window: no OOM kill, unexpected restart, or membership
loss. A load result collected across an undeclared datastore restart is failure
evidence, not a basis for relaxing application timeouts, buffers, or retry
defaults. Record per-node memory limits, CPU/shard allocation, restart counts,
and OOM state before and after the run.
For Docker-backed runs, provide `CAPACITY_DATASTORE_CONTAINERS`; the repository
runner snapshots sanitized container state and enforces this invariant. The
Namespace admission gate also requires the runtime memory allocation, explicit
per-node Scylla memory, and authentication mode to be declared rather than
inferred from a developer machine. The snapshot parses each running Scylla
container's command and rejects a runtime `--smp` value that differs from the
config manifest. It also runs `nodetool status` through every declared
container and requires the full declared set to report `UN` before and after
load. Container IDs and normalized names must be unique, and the same IDs must
remain present across both snapshots; aliases of one container cannot satisfy
the declared node count.
API readiness alpha/production runs require config and environment manifests,
at least two declared API replicas, a release API build, and explicit replica
and host-memory values matching those manifests. Provide every replica in
`CAPACITY_API_ENDPOINTS` and its corresponding running container in the same
position in `CAPACITY_API_CONTAINERS`. Preflight requires unique container IDs
and matches each external `/metrics` process start time to the internal
container scrape. Legitimate same-tick process starts therefore remain valid,
while aliases of one endpoint cannot satisfy the topology.
Namespace recovery requires a fresh
`NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE`; the deployment harness replaces the
selected endpoint when the file appears. The verifier records an actual
outage, requires a changed `process_start_time_seconds`, and resumes delivery
from the pre-replacement cursor.
Namespace watch and recovery runs must also provide every declared API replica
in `CAPACITY_API_ENDPOINTS`. Preflight scrapes and retains each process start
time, maps each endpoint to the corresponding unique container ID, and requires
the two watch endpoints to belong to that verified live set.
Set `CAPACITY_API_CONTAINERS` and `CAPACITY_GIT_SERVICE_CONTAINER` to the
corresponding running containers. Preflight rejects mutable image tags,
requires OCI revision labels matching the tested Git checkout and the release
executables from the repository Dockerfiles, and maps the externally scraped
API process identities to those containers.
The dispatcher enforces the same manifest preflight for Go-based capacity
profiles. It records their focused Go test as the domain verifier and, for
deployed Scylla-backed profiles, refuses alpha or production evidence without
the before/after datastore checks.

Capacity preflight MUST identify optimized/release service artifacts. Debug
builds are useful for functional diagnosis but cannot produce capacity evidence;
their lock, allocator, and instrumentation costs are not representative.

Capacity evidence is also the basis for application defaults. Every gate run
attaches a sanitized effective-config manifest and an environment manifest.
When tuning defaults, keep workload/topology/hardware constant, vary one related
configuration group, repeat each candidate at least three times, and select the
smallest value that meets latency/error/recovery objectives with headroom and
bounded overload behavior. Compare CPU, retained memory, goroutines, queue
depth/backpressure, and datastore pressure as well as throughput. A single
passing run MUST NOT automatically rewrite configuration defaults.

When a run fails because the declared environment exhausted memory or CPU,
repeat it with sufficient fixed datastore resources before varying application
configuration. Do not tune application defaults to conceal an undersized or
unstable test cluster.

Container fault injection uses pinned Pumba profiles:

```bash
make chaos CHAOS_PROFILE=api-restart \
  CHAOS_TARGET=gitstore-capacity-api-a \
  CHAOS_CONFIRM=1
```

Profiles live under `tests/chaos/profiles/`, and evidence is written under
`.gitstore/chaos/<profile>/<run-id>/`. The wrapper accepts one explicit
container name beginning with `gitstore-`, verifies that it exists, and refuses
to run without `CHAOS_CONFIRM=1`. Start with lifecycle faults (`restart` and
bounded `pause`). Network loss/latency and CPU/memory pressure MAY be added as
new reviewed profiles when their rollback behavior is proven on every supported
developer runtime. Never point the wrapper at a shared or production container.

The constitution intentionally specifies outcomes rather than k6 or Pumba.
Tool pins and operating instructions belong here and in the root `Makefile`, so
the implementation can evolve without a governance amendment.

**What it verifies:** real load and soak behavior against a live backend at
production scale — dataset size, sustained throughput, and (where the test
measures them) error rate and partition-size ceilings — deliberately kept out
of normal CI because it needs a preloaded multi-million-row dataset and can
run for hours.

Worked example: `gitstore-api/internal/datastore/scylla/capacity_test.go`.
Structure to copy:

- `//go:build scylla` build tag, same as the rest of the Scylla-tagged test
  suite — never runs in a default `go test ./...`.
- `TestScyllaCapacity` itself is gated behind **two** independent opt-ins:
  `SCYLLA_TEST_ADDR` (a real reachable cluster) and
  `GITSTORE_SCYLLA_CAPACITY_RUN=1` (an explicit "yes, run the expensive one"
  flag) — both must be set, so a stray env var from another test target can't
  accidentally trigger it.
- A hard-floor assertion on the intended scale:
  `GITSTORE_SCYLLA_CAPACITY_PRODUCTS` must be at least 5,000,000 (matching
  the constitution's stated minimum catalogue-size capacity target) and
  `GITSTORE_SCYLLA_CAPACITY_CONCURRENCY` must be at least 2, enforced with
  `t.Fatalf` before any real work starts — a misconfigured capacity run fails
  fast and loud instead of silently testing at toy scale.
- Two independent `gocql.Session`s (`sessions[]`), not one — capacity/soak is
  explicitly about validating **two independent clients** hitting the backend
  concurrently, mirroring what two API/controller replicas would do in
  production.
- `assertPartitionSizes` queries `system.large_partitions` and fails if any
  partition exceeds the 100 MiB hard ceiling or (unless
  `GITSTORE_SCYLLA_CAPACITY_ALLOW_HOT_PARTITIONS=1`) the 10 MiB hot-partition
  target — this is the "saturation point" check for this resource's access
  pattern.
- `assertBoundedPage` proves paginated queries never exceed the configured
  page size regardless of dataset size.
- `runMutationLoad` drives `cfg.Concurrency` goroutines through
  `mutations` reserve/release CAS cycles split round-robin across the two
  sessions, using `atomic.Int64` for the completed counter and a
  `sync.Mutex`-guarded `firstErr` with context cancellation on first failure
  — this is the sustained-load/throughput measurement; `t.Logf` records
  count, wall-clock duration, and client count so `go test -v` output is the
  durable record of a capacity run.
- `runSoak`, gated on `GITSTORE_SCYLLA_CAPACITY_SOAK_DURATION > 0`, repeats
  `runMutationLoad` in a loop until a deadline and asserts at least one batch
  completed — the actual "soak" (sustained, hours-long) half of the test,
  opt-in on top of the opt-in.

Diagnostic invocation is via the canonical capacity dispatcher, never ad hoc:

```bash
make capacity TARGET=scylla PROFILE=soak MODE=diagnostic
```

**Known limitation, requires a code change, not a docs workaround — read
before trusting a "PASS":** `TestScyllaCapacity` does **not** independently
confirm that the claimed dataset scale was actually loaded, and — checked
again directly against a review finding — there is currently **no
remediation available from documentation alone**, because the test itself
never surfaces a real count anywhere:

- `assertPartitionSizes` iterates `system.large_partitions`, increments a
  `seen` counter per matching row, and only calls `t.Errorf` for rows it
  actually scans. Zero recorded large partitions means `seen` stays `0`, no
  `t.Errorf` fires, and the function returns cleanly, logging only
  `"checked 0 recorded large partitions for keyspace ..."`.
- `assertQueryBound` (called by `assertBoundedPage`) only fails with
  `t.Fatalf` if `rows > limit`; zero rows satisfies `rows > limit` as
  `false`, so an empty table passes identically to a correctly paginated
  multi-million-row table.
- `runMutationLoad`'s `completed == mutations` assertion is real work (CAS
  reserve/release cycles against `namespace_mappings`), but it is
  **unrelated to the configured `GITSTORE_SCYLLA_CAPACITY_PRODUCTS` scale**.
- **Critically, `TestScyllaCapacity` has no preload phase of its own at
  all.** Read `loadCapacityConfig`/`TestScyllaCapacity` again: the only
  reference to loading data is `t.Skip("set GITSTORE_SCYLLA_CAPACITY_RUN=1
  after preloading the capacity dataset")` — preloading is an entirely
  out-of-band, manual/external step that happens *before* `go test` is
  even invoked. The `Products < 5_000_000` check only validates the **env
  var value itself**, and nothing in the Go test file ever counts rows in a
  Product (or equivalent) table, logs that count, or compares it against
  the configured target. A prior version of this runbook suggested checking
  `gitctl scylla-projection-audit`'s output as a substitute — that does not
  work either: `ProjectionRepairService.Audit` produces `findings`/`actions`
  for authoritative-vs-projection *consistency* drift, and returns an empty
  result for both a toy dataset and a correctly-loaded 5-million-row
  dataset alike, because it was never designed to count rows. There is
  **no existing signal anywhere in this repo — test output, audit tool, or
  otherwise — that an operator can check to confirm the claimed scale was
  actually loaded.**

**Practical consequence:** the canonical dispatcher now fails closed when
`scylla/soak` is requested in alpha or production mode; the underlying Go test
can still pass against a nearly-empty Scylla cluster and therefore remains
diagnostic-only. This is a **tracked follow-up
requiring a code change to `TestScyllaCapacity` itself**, not something a
runbook can work around after the fact:

1. **Required code fix (not yet done):** add an explicit preload-verification
   step to `TestScyllaCapacity` — e.g. a bounded/approximate row count on
   the relevant Product (or equivalent) table(s) (exact `COUNT(*)` over
   millions of Scylla rows is prohibitively expensive; use a
   token-range-sampled estimate, a tracked metadata/counter row maintained
   by the preload step, or `system.size_estimates`) — that logs the
   observed count and fails the test if it falls materially short of
   `cfg.Products`. Until this exists, treat this as an open item for
   whichever spec next touches `gitstore-api/internal/datastore/scylla/capacity_test.go`.
2. Until that code fix lands, `make capacity TARGET=scylla PROFILE=soak
   MODE=production` intentionally refuses to produce passing evidence. The
   only currently-available diagnostic mitigation is fully manual and
   out-of-band: independently query the preloaded table's row count via
   `cqlsh` (or equivalent) immediately before invoking `go test`, and record
   that number yourself alongside the test's `t.Logf` output — the test
   cannot corroborate it for you.
3. If your new resource's capacity test is written from scratch rather than
   copying `capacity_test.go`, build the row-count assertion in from the
   start rather than inheriting this gap.

**Where results get recorded:** new tests use the structured evidence bundle
created by `make capacity`. Existing Go-only gates may retain `go test -v`
output during migration, but MUST store it alongside equivalent metadata and
MUST NOT claim that a bare `PASS` proves the offered workload or dataset scale.
Specs reference the evidence bundle and summarize its thresholds and verifier
result; evidence containing secrets is never committed.

**How to apply this to a new resource:**

1. Add a `//go:build scylla`-tagged test file under
   `gitstore-api/internal/datastore/scylla/`.
2. Gate the expensive test behind a dedicated `GITSTORE_SCYLLA_CAPACITY_RUN`-style
   flag plus the shared `SCYLLA_TEST_ADDR`/`GITSTORE_TEST_SCYLLA_ADDR`
   address var — never let it run just because Scylla is reachable.
3. Assert the declared minimum scale (`t.Fatalf` if the configured dataset
   size is below the constitution's stated capacity target) before doing any
   real work.
4. Use at least two independent client sessions/connections driving
   concurrent goroutines, with `atomic` counters or a mutex-guarded error
   slot, to model multiple production clients.
5. Check partition-size ceilings and bounded-page invariants relevant to your
   access pattern, not just raw throughput.
6. Add a soak variant gated on its own duration env var, looping the same
   load function until a deadline.
7. Add the scenario to the root capacity dispatcher's validated target/profile
   matrix (never document a bare `go test` invocation as the primary entry
   point) and document required variables in the root `Makefile`.
8. **Required, not optional:** include an explicit preload-verification
   assertion — a bounded/approximate row count on the resource's table(s),
   logged and compared against the configured scale env var — before
   relying on partition/page/mutation checks. `TestScyllaCapacity` itself
   does not do this (see the "Known limitation" callout above); do not
   copy its zero-row-accepting `assertPartitionSizes`/`assertBoundedPage`
   shape, or its total absence of a preload-count check, into a new
   resource's capacity test without deciding that gap is acceptable — the
   default should be to close it, not inherit it.

## 3. Rolling-upgrade compatibility

**What it verifies:** a record written under the *old* shape (before a field
or behavior was added) remains fully readable and functionally correct after
the change ships, with no explicit migration step required — because
`gitstore-api` runs multiple replicas during a rolling upgrade, and old-schema
records can be read by new-schema code (and vice versa, transiently) at any
point.

Worked example (memdb, in-process authoritative store):
`gitstore-api/internal/datastore/memdb/owner_references_test.go`,
`TestOwnerReferenceProjectionCapsProductPagesAndIgnoresLegacyRecords`. The
load-bearing part is this block:

```go
// A pre-ownerReferences record from a rolling upgrade remains readable and
// does not create a false blocking dependent.
require.NoError(t, store.CreateCategoryTaxonomy(ctx, &datastore.CategoryTaxonomy{
	UID: "00000000-0000-0000-0000-000000000101", Namespace: scope.Namespace,
	RepositoryID: scope.RepositoryID, Name: "legacy", ResourceVersion: "1",
	CreationTimestamp: time.Now(),
}))
blocking, err := owners.HasBlockingOwnerDependents(ctx, scope, parentUID)
require.NoError(t, err)
assert.False(t, blocking)
```

It constructs a `CategoryTaxonomy` with **no `OwnerReferences` field set at
all** — exactly what a record written before spec 041/052's owner-reference
feature shipped would look like on disk — and asserts that querying it
through the *new* `HasBlockingOwnerDependents` API returns `false` (not an
error, and critically not a false-positive block) rather than panicking on a
nil field or misinterpreting the absence as blocking. The same test file's
first test (`TestOwnerReferenceProjection...`) also directly exercises
`ListNonBlockingProductOwnerDependents`'s page-size cap against
`datastore.MaxOwnerDependentPageSize`, so pagination bounds under real data
are covered in the same style.

**Scylla-backend equivalent exists too** — spec 052's task T037 asked for a
memdb/Scylla pair, and it's there:
`gitstore-api/internal/datastore/scylla/owner_references_test.go`,
`TestDecodeOwnerReferencesSupportsLegacyAndAdditiveRecords`:

```go
references, err := decodeOwnerReferences(nil)
require.NoError(t, err)
assert.Nil(t, references)

raw, err := json.Marshal([]catalog.OwnerReference{{UID: "owner", Kind: "CategoryTaxonomy"}})
require.NoError(t, err)
references, err = decodeOwnerReferences(raw)
require.NoError(t, err)
require.Len(t, references, 1)
assert.False(t, references[0].BlockOwnerDeletion, "legacy omitted flag defaults to non-blocking")
```

This is the Scylla-side shape of the same invariant: `nil` stored JSON (a row
that predates the column ever being written) decodes to `nil` with no error,
and JSON that omits the newer `BlockOwnerDeletion` field decodes with that
field defaulting to its safe (non-blocking) zero value rather than erroring
on an unrecognized/missing key. The same file's
`TestOwnerReferenceProjectionFailureIsRetriedAsRollForwardRecovery` is a
related-but-distinct pattern worth reusing separately: it injects a failure
*before* a specific named mutation step (`converge-owner-references`) via a
`newTestFailureInjector`, then re-runs the same executor and asserts the step
that already succeeded (`update-authoritative`) is not re-applied while the
step that failed is retried exactly once — useful for validating your own
multi-step Scylla mutation executor's retry/roll-forward safety alongside
rolling-upgrade read compatibility.

**This pattern is one-directional today, and that's a real gap, not just a
documentation nit.** Every worked example above tests *new* code reading an
*old*-shaped fixture (a record created before the field existed). A rolling
upgrade also runs the **reverse** direction: an old replica's binary — with
an older, narrower struct/deserializer — reads a record a *new* replica
already wrote with the new field populated, and may then perform a
read-modify-write of its own (e.g. updating some unrelated field) using its
old, narrower in-memory representation. If that old code's write path
round-trips through a typed struct that doesn't know about the new field,
the old replica's write can silently drop/clobber the new field — a
"deserialize into old shape, mutate, re-serialize" bug that a
new-reads-old-fixture test can never catch, because it never exercises old
code at all.

**Checked directly, and confirmed: no test in this codebase covers this
direction.** Searched for any "old code encounters new-shaped record,"
"backward compatibility," "forward compatibility," "unknown field," or
round-trip-preservation test across `gitstore-api` and
`gitstore-controller-manager` — nothing exists. `datastore.CategoryTaxonomy`
stores `OwnerReferences json.RawMessage` as its own dedicated Go struct
field (not embedded inside a shared JSON blob that an old struct might
partially decode), so a same-binary read-modify-write is safe by
construction *inside this one API process* — but that says nothing about
what an actually-older API **binary** (running as a separate replica during
a rolling upgrade against the same Scylla-backed row) would do with a
column it was compiled without knowledge of. No test in this repo exercises
two different code versions against the same row at all, so this is
genuinely unverified, not verified-and-safe.

**This is a required second checklist item, symmetric to the first, not an
optional extra** — see the checklist below. As of this writing, there is no
worked example to cite for it in this codebase; the first spec that adds
one should update this section to point at it.

**How to apply this to a new resource:**

1. For each backend you support (memdb and Scylla), write a test that
   constructs a row/record using only the fields that existed *before* your
   change — literally omit the new field from the struct literal (memdb) or
   marshal JSON that omits the new key (Scylla).
2. Read that old-shape record back through the *new* code path (the new
   query method, the new decode function) and assert it returns a safe
   default for the missing field — not an error, not a panic, not a
   false-positive on whatever the new field gates.
3. If the new field changes cardinality/paging behavior, add a page-size-cap
   assertion in the same test file against the same "old + new" mixed
   dataset, matching `MaxOwnerDependentPageSize`-style constants already
   defined on `datastore`.
4. If your change introduces a multi-step mutation (authoritative write +
   projection write), add a roll-forward-safety test in the same style as
   `TestOwnerReferenceProjectionFailureIsRetriedAsRollForwardRecovery`,
   injecting a failure at a named step and asserting no step re-runs after
   it already succeeded.
5. **Required, symmetric to step 1, not optional:** add the reverse-direction
   test. Construct a record using the *new*-shaped fixture (new field
   populated), decode it through your change's *old*-shaped
   struct/deserializer (the type as it existed before your change — you may
   need to keep or reconstruct that older type in the test itself), perform
   whatever read-modify-write the old code would actually do, and assert the
   new field survives that round trip unchanged in the underlying
   record/column rather than being silently dropped or nulled out. If your
   storage layer makes this safe by construction (e.g. a CQL `UPDATE`
   statement that only ever names the columns it knows about, never a
   full-row `INSERT`/replace), write a test that proves *that* specific
   claim — e.g. assert the generated CQL statement or executed query is a
   column-scoped `UPDATE`, not a full-row replace — rather than asserting
   nothing and relying on the claim being true.

## 4. Namespace-isolation / security-boundary testing

**What it verifies:** that operations and reads scoped to namespace/tenant A
cannot see or affect namespace/tenant B's resources, at both the resolver
(unit) level and the full GraphQL (integration) level, and that
authorization decisions run *before* any mutation or read side effect.

Two worked examples exist for this, at two different layers, and each covers
something the other doesn't — the concern from the task brief that this
pattern might have no real example turned out to be unfounded, but (per a
review finding below) neither example alone is a complete cross-user-read
proof:

- **Resolver-level (unit), fake `AuthZProvider`, includes explicit
  cross-tenant read attempts by ID/path/node:**
  `gitstore-api/internal/graph/resolver/repository_authorization_test.go`,
  `TestRepositoryResolversDenyCrossTenantAccessBeforeMutationOrRead`. It
  builds a `tenantOwnershipAuthz` fake implementing the real
  `auth.AuthZProvider` interface, guarded by a `sync.Mutex`-protected
  `calls []repositoryAuthzCall` slice, whose `Authorize` denies whenever
  `principal.Subject != resource.OwnerSub`. A table of every repository
  mutation and read path — `create`, `rename`, `transfer`, `delete`, and
  critically `read by ID`, `read by namespace path`, `read through node`,
  and `list namespace repositories` — is run as principal `bob` explicitly
  requesting resources owned by `alice` (by node ID, by `{namespace, name}`
  path, and via the Relay `node` field, not just via a list), asserting
  every single one returns exactly `"input: permission denied: cross-tenant
  access denied"` and that the authorizer was called with the correct
  `action` string, `resource.Kind`, `resource.OwnerSub`, and (for transfer)
  both source and target namespace attrs — proving the authorization check
  runs, and runs with correct context, on every mutation/read entry point
  including direct-by-ID/by-path cross-tenant reads, not just list
  filtering. The companion `TestRepositoryResolverUsesOwnActionForTenantOwner`
  asserts the *positive* case: the same principal reading their own resource
  is authorized under a distinctly-named `*.own` action rather than `*.any`,
  confirming the two authorization tiers are wired correctly rather than one
  swallowing the other. The one caveat: this test substitutes a fake
  `tenantOwnershipAuthz` authorizer for whatever `AuthZProvider` is actually
  configured in production, so it proves the resolver *calls* the configured
  authorizer correctly and denies before reading — not that the real
  production policy itself is what denies it end-to-end.
- **Full-stack integration (real GraphQL, two real users):**
  `tests/integration/authz_repository_contract_test.go`,
  `TestRepositoryAuthorization_TwoUserNamespaceIsolation`. Two users
  (the real `static-users` identities `alice` and `bob`) each create their own namespace and
  repository through real GraphQL mutations against the running harness.
  Alice's attempt to `deleteNamespace` on Bob's namespace is asserted to
  fail with `"permission denied: resource belongs to another user"`. Each
  user then lists `repositories(namespace: ...)` scoped to **their own**
  namespace, and `assertRepositoryNamespaceIsolation` asserts every returned
  repository belongs to the expected namespace, that the other user's
  repository name never appears, and that `createdBy` matches the expected
  actor.

  **Correction (verified 2026-08-29 against a review finding):** this test
  does **not** cover the cross-user *read* case: neither Alice nor Bob ever
  issues a scoped read/get for the *other* user's specific repository (by
  ID, or by namespace path naming the other user's namespace/repository).
  Both users only ever list their own namespace and assert the other user's
  name is absent from *that* list. A wiring bug that filters same-namespace
  lists correctly and denies `deleteNamespace` correctly, but that fails
  open on an explicit cross-user `repository(namespaceAndPath: ...)` or
  `node(id: ...)` lookup naming the other user's resource directly, would
  pass this test unchanged. The resolver-level unit test above *does* cover
  that shape (its `"read by ID"`/`"read by namespace path"`/`"read through
  node"` table cases), but only against a fake authorizer substituted in for
  the real production `AuthZProvider` — it proves the resolver's dispatch
  order (deny-before-read), not that the real, wired-in-production
  authorization stack denies a cross-user read end-to-end through a live
  GraphQL request. As of this writing there is no full-stack integration
  test that issues an explicit cross-user scoped-read-by-ID/by-path request
  through real GraphQL and asserts denial or not-found. Treat this as a
  real, currently-open gap — see the checklist item below.

**Secret-reference (`SecretRef`-style) leak checks:** the task brief asked
specifically to check whether cross-namespace leakage of secret-reference
fields is covered.

**Correction (verified 2026-08-29 against a review finding):** an earlier
version of this section incorrectly claimed no `SecretRef`-style field
exists in this codebase. That was wrong — the earlier grep was scoped
incorrectly and returned a false negative. `SecretRef`/`CredentialsRef`
genuinely exist today: `gitstore-api/internal/catalog/file.go` defines
`FileSourceDefinition.CredentialsRef *SecretRef` (line 28) and the
`SecretRef{Kind, Name, Key, Namespace}` struct (line 36), and
`FileSpec.Validate` (lines 82-84) already enforces a namespace-isolation
rule for it:

```go
if s.Source.CredentialsRef != nil && s.Source.CredentialsRef.Namespace != "" &&
	s.Source.CredentialsRef.Namespace != resourceNamespace {
	return fmt.Errorf("validate: spec.source.credentialsRef.namespace must match the resource namespace")
}
```

`shared/schemas/file.graphqls` exposes the same `SecretRef` type over
GraphQL (`credentialsRef: SecretRef` on `FileSource`), so it is reachable
through reads, not just admission.

**Correction (verified 2026-08-29 against a second review finding): the
admission-reject path IS already covered — an earlier version of this
section overclaimed a total gap.** Two levels of testing exist:

- `gitstore-api/internal/catalog/file_test.go`'s
  `TestFileSpecSourceValidation` covers only the **accept** path — a
  `CredentialsRef` whose `Namespace` matches the resource's own namespace
  validates successfully.
- `gitstore-api/internal/cataloggrpc/server_test.go`'s
  `TestValidateResources_FileAggregatesVariantAndCredentialsErrors`
  (around line 668) **does** cover the reject path, at the real gRPC
  admission boundary: it submits a `File` whose
  `spec.source.credentialsRef.namespace: other` doesn't match the
  resource's own namespace (`gitstore`) through `srv.ValidateResources`,
  asserts `resp.Accepted` is `false`, and asserts one of the aggregated
  error messages contains `"credentialsRef.namespace"` — proving a
  cross-namespace `SecretRef` reference is rejected at the point a File
  resource is admitted via a real push, not just at the narrower
  `FileSpec.Validate` unit level.

**What is genuinely still untested is narrower than "SecretRef leak checks"
as a whole: it's specifically the *post-admission, authenticated
status/watch/read* boundary.** `TestValidateResources_...` proves a
cross-namespace `SecretRef` is rejected *before* a File is ever admitted —
but it says nothing about what happens if a File carrying a cross-namespace
`SecretRef` somehow already exists in the datastore (e.g. a record written
before this validation rule shipped, written directly against the
datastore in a test/migration, or admitted under some other path that
doesn't run this check). No test in this repo exercises: a File with a
`CredentialsRef.Namespace` that differs from the File's own namespace, read
back through `gitstore-api/internal/graph` (a GraphQL query/resolver), a
status update, or a watch subscription — asserting that field's exposure
still respects namespace boundaries (or is redacted/denied) for a caller
authenticated as a different tenant. This is genuinely open — not covered by
`authz_repository_contract_test.go` (that test doesn't touch File at all)
and not covered by `repository_authorization_test.go` (Repository-specific).
It matches spec 051's own tracking: `specs/051-file-resource-contract/tasks.md`
T041 ("Add authenticated namespace-isolation and SecretRef boundary tests
for File admission/status/watch paths") is unchecked as of this writing and
explicitly annotated `Blocked: existing auth integration covers
repository/namespace authorization, but has no File admission/status/watch
endpoint harness; SecretRef validation is covered by runnable unit tests
only` — note that annotation itself was written before/independent of the
`TestValidateResources_FileAggregatesVariantAndCredentialsErrors` admission
coverage identified above, so its "covered by runnable unit tests only"
framing undersells the admission-level coverage that already exists; what
it correctly flags as missing is the status/watch/read boundary. Another
agent may be completing T041 in a parallel worktree as part of spec 051 —
if so, **point this section at whatever File-specific status/watch
boundary test lands from that work instead of re-deriving one**, following
the same worked-example format as patterns 1-3 above.

**How to apply this to a new resource:**

1. Add a resolver-level unit test with a fake `AuthZProvider` (implement the
   real `auth.AuthZProvider` interface) that denies whenever the caller's
   subject doesn't match the resource's owner. Table-drive it across every
   mutation and read entry point your resolver exposes — not just one
   representative path — and assert the exact action string and resource
   attrs passed to the authorizer for each.
2. Add the positive-case companion test proving same-owner access uses the
   `*.own` action tier, not `*.any`.
3. Add (or extend) a full-stack integration test with two real users each
   owning their own namespace/resource, asserting: (a) a cross-user mutation
   is denied with the expected error text, (b) a same-user list/read query
   never returns the other user's resource by name or attribute, **and (c)
   an explicit negative test where user A issues a scoped read/get naming
   user B's specific object (by ID, or by `{namespace, name}` path) and
   expects denial or not-found** — do not rely on (b)'s list-filtering
   assertion to imply (c); as this runbook itself had to correct, a
   same-namespace list filter passing correctly does not prove a
   direct-by-ID/by-path cross-namespace read is blocked, and
   `authz_repository_contract_test.go` is a concrete example of a test that
   covers (a) and (b) but not (c).
4. If your resource introduces any field that references a secret,
   credential, or other sensitive external identifier (a `SecretRef`-style
   field): add tests for **both** the unit-level accept path (matching
   namespace validates) **and** the reject path at the real admission
   boundary (a cross-namespace reference is rejected via the actual gRPC
   `ValidateResources`/admission call, not just the narrower `Validate()`
   unit method) — follow
   `gitstore-api/internal/cataloggrpc/server_test.go`'s
   `TestValidateResources_FileAggregatesVariantAndCredentialsErrors` for the
   admission-reject shape. Then — separately, and this is the part most
   likely to be missing — add the cross-namespace-read negative test from
   step 3 specifically for that field's *post-admission* exposure (a status
   query, a watch subscription, and a GraphQL read/resolver path), covering
   the case where a cross-namespace reference already exists in the
   datastore rather than being caught at admission time. Don't assume the
   admission-reject test already covers this; as of this writing in this
   codebase, it doesn't.

## Related

- `.specify/memory/constitution.md` — Principle 4 ("Production Readiness") and
  the Quality Gates it drives.
- [CategoryTaxonomy foreground deletion](categorytaxonomy-deletion.md) — the
  operational runbook for the resource pattern 1's worked example is drawn
  from.
- [Scylla projection repair and capacity validation](scylla-projection-repair.md)
- `gitstore-controller-manager/tests/integration/categorytaxonomy_deletion_test.go`
- `gitstore-controller-manager/tests/integration/reconcile_retry_resume_test.go`
- `gitstore-api/internal/datastore/scylla/capacity_test.go`
- `gitstore-api/internal/datastore/memdb/owner_references_test.go`
- `gitstore-api/internal/datastore/scylla/owner_references_test.go`
- `gitstore-api/internal/graph/resolver/repository_authorization_test.go`
- `tests/integration/authz_repository_contract_test.go`
- `gitstore-api/internal/cataloggrpc/server_test.go` (`TestValidateResources_FileAggregatesVariantAndCredentialsErrors`)
- `gitstore-api/internal/catalog/file.go`, `gitstore-api/internal/catalog/file_test.go`
- `specs/051-file-resource-contract/tasks.md` (T041)
