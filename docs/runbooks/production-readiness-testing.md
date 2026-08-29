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

**Known gap, stated honestly rather than papered over:** if a spec's
production-readiness requirement is specifically "two replicas may reconcile
the same key *simultaneously* and must not double-apply a side effect or
corrupt a conflicting write," neither worked example above proves that,
because neither one lets two reconciler instances overlap in time. Writing
that test requires new synchronization that doesn't exist yet in this repo's
test helpers: e.g. two goroutines started together, each blocked on a shared
channel/barrier until both are ready, then released to call `Reconcile` (or
attempt a conflicting mutation) at the same instant against one shared
mutex-guarded fake or datastore, asserting exactly one side "wins" a
CAS/resourceVersion check and the other retries safely rather than
corrupting state. Until a spec actually needs and writes that test, don't
cite this runbook as if the pattern already has a true-concurrency worked
example — it doesn't.

Use the sequential dual-instance shape when the reconciler-under-test is
simple enough that constructing two instances and calling `Reconcile`
directly is enough to prove idempotency across a restart. Reach for the real
Runner/Manager restart shape only when the checkpoint/dispatch machinery
itself is part of what you're validating (e.g. a new `ListWatcher`
implementation). Reach for the (currently unwritten) true-concurrency shape
above only when the invariant you need is specifically about simultaneous
overlap, not restart-safety.

**How to apply this to a new resource:**

1. Identify every external client/side effect the reconciler under test
   calls (a status client, a decouple/mutation RPC, a completion call).
   Build a fake with `sync.Mutex`-guarded counters for each.
2. Pick the sequential-dual-instance or real-Runner/Manager-restart shape
   above based on whether you're also validating checkpoint/dispatch
   machinery. Both are restart-safety tests, not concurrency tests.
3. Construct at least two independent reconciler (or Runner+Manager)
   instances sharing the same fake/store, and force one to leave the work
   item mid-progress (a bounded continuation, an in-flight item at teardown).
   Tear the first instance down fully before constructing the second — that
   sequencing is what makes the test about restart-safety, and you should
   say so in the test's doc comment rather than implying concurrency.
4. Assert exact counts on the fake after the second instance runs — not just
   "eventually succeeded," but "ran exactly once," "did not re-run a
   completed step," and "a stale reconcile after completion is a terminal
   no-op."
5. If — and only if — your spec's invariant is specifically about
   *simultaneous* overlap (not restart), do not reuse the shapes above
   unmodified. Write a new test with an explicit synchronization barrier
   (e.g. two goroutines released together via a shared channel) so both
   reconciler instances genuinely race the same key, and say in this
   runbook (via a follow-up edit) once such a worked example exists — as of
   this writing, it does not.

## 2. Capacity/soak testing

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

Invocation is via a dedicated `make` target, never ad hoc — root `Makefile`
around line 163:

```makefile
test-scylla-capacity: ## Run the opt-in Scylla capacity and soak test.
	@cd "$(API_DIR)" && GITSTORE_TEST_SCYLLA_ADDR="$(SCYLLA_TEST_ADDR)" \
		GITSTORE_SCYLLA_CAPACITY_PRODUCTS="$(SCYLLA_CAPACITY_PRODUCTS)" \
		GITSTORE_SCYLLA_CAPACITY_CONCURRENCY="$(SCYLLA_CAPACITY_CONCURRENCY)" \
		GITSTORE_SCYLLA_CAPACITY_SOAK_DURATION="$(SCYLLA_CAPACITY_DURATION)" \
		GITSTORE_SCYLLA_CAPACITY_RUN=1 \
		go test -tags scylla -count=1 -timeout 0 -run TestScyllaCapacity ./internal/datastore/scylla/...
```

**Verification gap — read before trusting a "PASS":** `TestScyllaCapacity`
does **not** independently confirm that the claimed dataset scale was
actually loaded before it runs. Read `assertPartitionSizes` and
`assertBoundedPage`/`assertQueryBound` directly: both accept a zero-row
result without failing.

- `assertPartitionSizes` iterates `system.large_partitions`, increments a
  `seen` counter per matching row, and only calls `t.Errorf` for rows it
  actually scans. If the keyspace has zero recorded large partitions (an
  empty or barely-populated cluster), `seen` stays `0`, no `t.Errorf` ever
  fires, and the function returns cleanly — it only logs `"checked 0
  recorded large partitions for keyspace ..."`, it does not fail.
- `assertQueryBound` (called by `assertBoundedPage`) counts rows scanned
  from a `LIMIT`-bounded query and only fails with `t.Fatalf` if
  `rows > limit`. Zero rows satisfies `rows > limit` as `false`, so an empty
  table passes the "bounded page" check identically to a correctly
  paginated multi-million-row table.
- `runMutationLoad`'s `completed == mutations` assertion is real work (CAS
  reserve/release cycles against `namespace_mappings`), but it is
  **unrelated to the configured `GITSTORE_SCYLLA_CAPACITY_PRODUCTS` scale** —
  the test never counts rows in a Product (or equivalent) table and never
  compares that count against the configured target. The `Products < 5_000_000`
  check in `loadCapacityConfig`/`TestScyllaCapacity` only validates the
  *env var value itself* before the run starts; nothing later in the test
  verifies that many rows actually exist in the cluster.

**Practical consequence:** it is possible to run `make test-scylla-capacity`
against a nearly-empty Scylla cluster, get a clean `PASS`, and mistakenly
report it as a validated 5-million-row capacity/soak result. Before trusting
a capacity run's result:

1. Do not treat `go test ... PASS` alone as evidence of anything. Read the
   test's own `t.Logf` output for `"checked %d recorded large partitions"`
   and `"%s bounded page returned %d rows"` and confirm those counts are
   nonzero and plausible for the configured scale — a `0` in either is a
   silent no-op, not a validated result.
2. Independently confirm the preload actually happened at the configured
   `GITSTORE_SCYLLA_CAPACITY_PRODUCTS` scale before running the test at all
   — e.g. via `cqlsh` row counts on the relevant table(s), or
   `gitctl scylla-projection-audit` (see "Projection audit, repair, and
   capacity validation" in `docs/developer-guide.md`) — since the test
   itself will not catch a preload that silently loaded far fewer rows than
   claimed.
3. If your new resource's capacity test should actually enforce this rather
   than rely on manual verification, add an explicit row-count assertion
   against the configured `*_PRODUCTS`-equivalent env var before running the
   partition/page/mutation checks — don't inherit `TestScyllaCapacity`'s gap
   silently.

**Where results get recorded:** there is no separate results file or
dashboard convention yet. The established practice (see `capacity_test.go`'s
own `t.Logf` calls, and `docs/developer-guide.md`'s "Projection audit,
repair, and capacity validation" section) is `t.Logf`/`go test -v` output as
the record, captured by whoever runs it — `specs/048-scylla-query-design`'s
own `quickstart.md` documents the *test scenarios* (cross-bucket pagination
correctness, etc.) but does not itself define a structured results-recording
format. If your spec's capacity run needs durable evidence beyond captured
test-log output (e.g. for a PR description or an ADR), redirect
`go test -v`'s stdout to a file under the spec's `specs/<NNN>/` directory and
reference it from the spec's `tasks.md` — don't invent a new dashboard.

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
7. Add a dedicated `make test-scylla-<resource>-capacity`-style target to the
   root `Makefile` (never document a bare `go test` invocation as the primary
   entry point) and document required env vars in this repo's `Makefile` per
   the "Development Guidelines" rule that root `Makefile` is canonical.
8. Make the test itself assert nonzero, scale-matching results wherever
   possible (an explicit row-count check against the configured products/
   scale env var), and where that's not practical, log counts loudly enough
   (`t.Logf`) that a human reviewing output — not just the `PASS`/`FAIL`
   line — can catch a silently-empty preload. Do not copy
   `TestScyllaCapacity`'s zero-row-accepting `assertPartitionSizes`/
   `assertBoundedPage` shape without deciding whether that gap is acceptable
   for your resource.

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
  (`test-user:alice`, `test-user:bob`) each create their own namespace and
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

**What's actually tested today is thinner than a full worked example.** The
only existing test touching this is
`gitstore-api/internal/catalog/file_test.go`'s
`TestFileSpecSourceValidation`, and it only exercises the **accept** path —
a `CredentialsRef` whose `Namespace` matches the resource's own namespace
validates successfully. There is no test anywhere in the repo (verified via
`grep -rn "CredentialsRef.Namespace\|credentialsRef.namespace must match"`
across `*_test.go`) that exercises the **reject** path — a `CredentialsRef`
naming a *different* namespace — nor any test at the
`gitstore-api/internal/cataloggrpc`, `gitstore-api/internal/auth`, or
`gitstore-api/internal/graph` layer that checks a `SecretRef` doesn't leak
across a real cross-namespace admission/status/watch/read boundary. This
matches spec 051's own tracking: `specs/051-file-resource-contract/tasks.md`
T041 ("Add authenticated namespace-isolation and SecretRef boundary tests
for File admission/status/watch paths") is unchecked as of this writing and
explicitly annotated `Blocked: existing auth integration covers
repository/namespace authorization, but has no File admission/status/watch
endpoint harness; SecretRef validation is covered by runnable unit tests
only.` Another agent may be completing T041 in a parallel worktree as part
of spec 051 — if so, **point this section at whatever File-specific
`SecretRef` boundary test lands from that work instead of re-deriving one**,
following the same worked-example format as patterns 1-3 above. Until that
lands, record this honestly as a genuinely open gap rather than citing
`TestFileSpecSourceValidation`'s accept-path-only coverage as if it were a
complete worked example.

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
   field): add a unit test for the **reject** path (a reference naming a
   different namespace than the resource's own is rejected at validation),
   not just the accept path — `gitstore-api/internal/catalog/file_test.go`'s
   `TestFileSpecSourceValidation` is a cautionary example of covering only
   the accept path. Then add the same cross-namespace-read negative test
   from step 3 specifically for that field's exposure (admission, status,
   watch, and GraphQL read paths) — don't assume a generic isolation test
   written for other fields already covers it.

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
