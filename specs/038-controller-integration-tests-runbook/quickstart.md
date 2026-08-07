# Quickstart: Controller Integration Tests + Operations Runbook (spec 038)

## Prerequisites

- `gitstore-controller-manager` built: `go build ./...` from `gitstore-controller-manager/`
- Familiarity with the existing contract test fakes: `tests/contract/listwatch_resume_test.go` (`fakeListWatcher`), `tests/contract/retry_quarantine_test.go` (always-failing reconciler), `tests/contract/health_test.go` (`prometheus/client_golang/prometheus/testutil` metric assertions)

## Running the new integration suite

```bash
cd gitstore-controller-manager
go test ./tests/integration/... -v -race
```

This package is included automatically by `make test` (which runs `go test ./...` for the controller-manager module) and by CI — no separate target is added.

## Writing a new integration scenario

1. Pick the scenario file matching the user story (see [contracts/integration-test-scenarios.md](./contracts/integration-test-scenarios.md)) — e.g. a new retry-edge-case test goes in `reconcile_retry_resume_test.go`.
2. Construct a real `manager.Manager` via `manager.New()`, register a fake `Reconciler` that returns the exact `types.ReconcileResult` sequence your scenario needs (`Success`, `TransientFailure`, `TerminalFailure`, `RequeueAfter`).
3. If the scenario involves checkpointing or watch behavior, also construct a real `checkpoint.MemoryStore` (or `FilesystemStore` over `t.TempDir()`) and a `listwatch.Runner[T]` with a fake `ListWatcher[T]` — copy the fake shape from `tests/contract/listwatch_resume_test.go`, do not invent a new one.
4. Assert only on external state: `Manager.AllPoisonItems()`, `Manager.QuarantineStore(kind)`, resource status returned from your fake `status.StatusClient`, and `health` package metrics via `testutil.ToFloat64(...)` / `testutil.GatherAndCompare(...)`.
5. Run with `-race`; the manager dispatches on goroutines, so races are the most likely failure class.

## Following a runbook

Each runbook in `docs/runbooks/` follows: **Symptom** (what you're paged for) → **Diagnostic Steps** (exact metric/log/API to check, from [contracts/runbook-signal-contract.md](./contracts/runbook-signal-contract.md)) → **Recovery Actions** → **Verification** (how to confirm the fix worked). Start with the Symptom section that matches what you observed (dashboard alert, log line, or ticket description), and work top-to-bottom — do not skip Diagnostic Steps even if the Recovery Action seems obvious, since the same symptom can have more than one root cause (see spec Edge Cases: distinguishing a genuinely poisoned item from a longer transient outage).

## Verifying a runbook's signals are real (before merging a runbook change)

```bash
cd gitstore-controller-manager
go test ./tests/integration/... -run TestObservability -v
```

Every row in [contracts/runbook-signal-contract.md](./contracts/runbook-signal-contract.md) must have a passing entry in this test run before the corresponding runbook prose is committed.
