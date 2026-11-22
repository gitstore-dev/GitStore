# Proposed Admission Report and Push Workflows

These are design examples for spec 075, not shipped endpoints/options. Implement the source/effect prerequisites and enable compatible versions first. API/Git source handoff depends on spec 074/#139's owned slice; Event/Audit/Svix delivery is not required to observe reports. Examples use current Admin GraphQL HTTP/WS transport, canonical resource envelope/status and existing errors/extensions.

## Inspect retained admission results

Use the real report ID supplied by an authorized correlated response or negotiated Git output. IDs are not credentials. The nested findings are a bounded value connection from the report's pinned snapshot.

```graphql
query InspectAdmission($id: ID!) {
  admissionReport(id: $id) {
    id apiVersion kind
    metadata { name namespace uid resourceVersion generation creationTimestamp }
    spec {
      executionId batchId attempt phase
      repositoryRef { kind name namespace uid }
      producer { name version }
      target { origin ref commit previousCommit sourceCursor inputDigest }
    }
    status {
      observedGeneration lastAppliedRevision
      conditions { type status reason message }
      resolved {
        state conclusion observedAt summary expiresAt diagnosticsSnapshot
        counts { planned accepted noOp denied failed skipped unresolved diagnosticsKnown diagnosticsRetained diagnosticsOmitted diagnosticsIncomplete }
        diagnostics(first: 20) {
          edges { cursor node { reason message level file field region { startLine startColumn endLine endColumn } } }
          pageInfo { hasNextPage endCursor }
        }
      }
    }
  }
}
```

QUEUED/IN_PROGRESS with absent conclusion is not success. COMPLETED+FAILURE can contain only retained warnings if failure messages were omitted; counts/conclusion still reflect known failures. A deletion has previousCommit and no new commit. Newer commits/current Ready conditions do not satisfy an older report. Expired input can prevent rerun while a90-day report remains readable. Findings may require narrower current permission than other repository data.

```graphql
query RecentAdmission($namespace: String!, $after: String) {
  admissionReports(namespace: $namespace, first: 20, after: $after) {
    edges { cursor node { id spec { executionId attempt } status { resolved { state conclusion summary } } } }
    pageInfo { hasNextPage endCursor }
  }
}
```

Initial blank-query list is a fixed24-hour window pinned by first cursor. Exact repository/revision recovery uses the shared query subset: spec.repositoryRef.uid:"<repository-uid>" spec.target.commit:"<full-oid>" (optional spec.target.ref). That indexed route covers retained90-day history and is required for recovery after quiet/non-sideband pushes; unsupported advanced terms fail explicitly. Known retained IDs remain inspectable independently of search. Cursors are not portable across namespace/principal/predicate/snapshot; do not fetch all rows or infer exact totals.

```graphql
subscription AdmissionChanges($namespace: String!, $resourceVersion: String) {
  watchAdmissionReports(namespace: $namespace, resourceVersion: $resourceVersion) {
    type name namespace resourceVersion
    report { id status { resolved { state conclusion observedAt diagnosticsSnapshot } } }
  }
}
```

Persist resourceVersion only after applying an event. Bookmarks/deletions have no report. Journal expiry/gaps use existing errors; relisting restores current report views, not missing historical admission occurrences. Nested reads from old snapshots either resolve the pinned immutable findings or explicitly return SNAPSHOT_EXPIRED.

## Git and GraphQL observation

Under the proposed enabled sideband policy an ordinary Git push may display:

```text
remote: Git refs accepted; admission report <id>
remote: Admission in progress
remote: products/coat.md: spec.taxCode requires a value
remote: Admission failed; committed refs remain accepted
```

Text is illustrative, not a machine parser contract or a newly implemented validation rule. Git status remains about actual refs; a CLI's separately defined admission wait can use report conclusion. After complete upload/publication, the API streams negotiated progress and waits for admission before normally finishing the push. Healthy work is not automatically detached after60 seconds. Quiet and non-sideband also wait, with optional output suppressed or unavailable. Disconnect, explicit detach or operational transport/authentication failure ends observation only; accepted work keeps running. Configured execution/queue/retry limits produce explicit timeout/unresolved results, and proxy limits must cover that supported budget. No new git push option is claimed here. Report lookup supplies durable machine results. Work survives detachment only after verified durable acceptance; incomplete uploads and uncertain publication are distinct.

Existing GraphQL mutations keep their current synchronous result/error behavior. extensions.admission gains reportId/executionId/attempt when a concrete correlated outcome exists, including diagnostics:[] on explicitly correlated clean success. It retains response path/commit/diagnostics names and canonical reason/message/level/file/field. An unchanged legacy nil-warning collector call does not suddenly emit an empty entry. See [diagnostics contract](contracts/diagnostics-operations.md) for example JSON.

## Explicit rerun and export

Rerun requires admissionReport.create plus the original target authority and uses original retained input under current authorization, preserves prior reports and verifies/reuses already applied effects. It is not a request to repeat arbitrary business effects or silently use a newer policy. One active attempt/root; repeat the exact caller key to recover acceptance, never invent a new key because a response was lost.

```graphql
mutation RerunAdmission($input: RerunAdmissionReportInput!) {
  rerunAdmissionReport(input: $input) {
    report { id spec { executionId attempt previousAttemptRef { uid name } } status { resolved { state conclusion } } }
  }
}
```

```json
{
  "input": {
    "reportId": "<authorized-retained-report-relay-id>",
    "idempotencyKey": "operator-rerun-2026-11-22-001"
  }
}
```

REPORT_INPUT_EXPIRED, EFFECT_UNCERTAIN, ADMISSION_PENDING, audit unavailability or unsupported producer guarantee are explicit errors/conditions; no fresh effects are inferred from absent receipts. A successful command returns a real queued report, not successful admission.

```graphql
mutation ExportAdmission($input: ExportAdmissionReportsInput!) {
  exportAdmissionReports(input: $input) { exportId document nextCursor }
}
```

Pass1–16 terminal reportIds and idempotencyKey; continuation repeats them with after=nextCursor. Each document is a complete bounded SARIF 2.1.0 log with one run; each page reauthorizes using admissionReport.read and current diagnostic disclosure and expires within 15 min/report retention. There is no additional export permission or verb. These are mutation Payloads, not a query input wrapper. No public artifact URL or unbounded export. Protected notes projection is an API-internal derived operation, not a user endpoint; report-only findings never appear in repository-fetchable notes.

## Implementation and verification order

Follow [plan.md](plan.md) US1–US5 slices, failing contracts first, forward migrations/shared generation and existing Make check/test/capacity/chaos families. Use fixture-backed reports for independent observer/export work. Do not enable restart-safe post-commit claims before source 074 handoff and producer target-effect verifiers pass; do not run two callback/source engines. Full normal make pr-ready and deployed source/replica/proxy evidence are distinct gates. Planning does not execute runtime faults or claim acceptance.
