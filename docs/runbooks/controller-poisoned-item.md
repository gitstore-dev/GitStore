# Runbook: Poisoned (Quarantined) Item

## Symptom

A specific resource repeatedly fails reconciliation. Either it exhausted its retry budget (transient failures past `MaxAttempts`) or failed with an unrecoverable error (`TerminalFailure`), and the controller manager has quarantined it rather than retrying forever.

## Diagnostic Steps

1. Check whether the affected kind has any quarantined items, and how many:

   ```promql
   gitstore_controller_poison_items_total{kind="<Kind>"}
   ```

2. List the quarantined items for that kind (or across all kinds) via the poison-item HTTP API exposed by the controller manager (`cmd/controller/main.go`):

   ```bash
   curl http://<controller-manager-host>:<port>/controller/v1/poison/<Kind>
   # or, across every registered kind:
   curl http://<controller-manager-host>:<port>/controller/v1/poison/_all
   ```

   Each entry includes the resource's `Key` (kind/namespace/name), its `Attempts` count, and `LastError` — the error message from the final failed reconcile attempt.

   Responses remain JSON arrays but are bounded to 256 entries. Use `?limit=N`
   (1–256) to request a smaller page. If the response has an `X-Next-Cursor`
   header, pass that opaque value as `?after=<cursor>` on the next request.
   Follow all pages for a complete inventory; an empty final page is valid.
   Storage failures return HTTP 503, not an empty successful array.

3. Search controller-manager logs for the offending resource's namespace/name for the quarantine log line (`"terminal reconcile failure — quarantining immediately"` for an immediate `TerminalFailure`, or `"reconciler quarantined after exhausting retries"` for exhausted transient retries). The log line includes the same error detail as `LastError` above. If the reconciler panicked, a separate `"reconciler panic recovered"` log line (logged just before the quarantine line) carries the full stack trace.

4. Using `LastError` and the log context, determine **why** the reconciler is failing:
   - **Bad/invalid resource data** (e.g. a malformed reference, a constraint violation) — the item is genuinely poisoned; it will keep failing until the underlying data is fixed.
   - **A dependent service being down or slow** (e.g. a downstream API timeout) — this may look identical to a poisoned item if the outage outlasted the retry budget, but is not permanently broken; distinguishing this from genuinely bad data is judgment-based — check whether the same error is affecting *many* items across kinds simultaneously (points to an outage) versus just this one resource (points to bad data).
   - **`LastError` is exactly `"<kind>: <namespace>/<name> not found in cache"`** — this is a reconciler bug, not poisoned data. A queued key legitimately outlives its object after watch replay, deletion, or a checkpointed controller restart; every `Reconcile` implementation must treat a cache-miss as already-reconciled (`types.ResultOK()`), not as a `TerminalFailure`. `Repository` and `Product` follow this convention; `CategoryTaxonomy` and `Namespace` did not, and quarantined every stale requeue as a result, until this was fixed. Requeuing will not help — if you see this error again for any kind, it means that kind's `Reconcile` has the same bug; fix the cache-miss branch rather than repeatedly requeuing.

## Recovery Actions

Quarantine and retry deadlines survive controller replacement on the same
per-replica checkpoint volume. Periodic resync does not clear them. A newer
resource/dependency change can enqueue fresh work; completion from an older
attempt cannot clear that newer obligation. Stored `LastError` text is capped
at 2,048 bytes plus an explicit truncation suffix; use logs for longer detail.

- **If the root cause was bad resource data**: fix the underlying resource (correct the data via the normal push/API path), then requeue it:

  ```bash
  curl -X POST http://<controller-manager-host>:<port>/controller/v1/poison/<namespace>/<Kind>/<name>/requeue
  ```

  A `204 No Content` response confirms the item was removed from quarantine and re-enqueued with a fresh retry budget.

  For cluster-scoped Namespace keys, omit the namespace segment:
  `POST /controller/v1/poison/Namespace/<name>/requeue`.

- **If the root cause was a transient outage that has since resolved**: requeuing (same endpoint) is sufficient — no data fix needed, since the reconciler should now succeed on retry.

- **If the root cause is still present** (data not yet fixed, or the outage ongoing): do **not** requeue yet — requeuing an item whose root cause persists will simply re-fail and re-quarantine after consuming another retry budget, adding noise without progress. Document the decision to leave it quarantined (e.g. in the related incident/ticket) until the root cause is addressed.

- **If many items across multiple kinds are simultaneously poisoned with the same `LastError`**: treat this as a single upstream incident, not N independent poisoned items — fix the shared root cause first, then requeue affected items in bulk (there is currently no bulk-requeue endpoint; each item must be requeued individually via the endpoint above).

## Verification

- The requeued item no longer appears in `GET /controller/v1/poison/<Kind>` or `_all`.
- `gitstore_controller_poison_items_total{kind}` decrements for the affected kind.
- `rate(gitstore_controller_reconcile_total{kind,result="success"}[5m])` shows a success for the requeued item's kind following the requeue.
