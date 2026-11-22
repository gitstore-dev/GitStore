# Diagnostics, Queries and Authorization (Proposed)

## Resource/query contract

admin.graphqls is an additive overlay against canonical common/Admin SDL. AdmissionReport uses Node/ObjectMeta/spec/status and optional body; metadata/status/reference definitions are never copied. Internal command/effect/observer DTOs are not public resources or worker mutations. POST_RECEIVE is the only initial persisted report phase; rejected pre-receive diagnostics retain their existing error path. Later phases extend the owner contract explicitly.

admissionReport(id:) and generic node(s) apply current report/repository disclosure checks before revealing existence. admissionReports(namespace:, first/after/last/before/query) defaults first 50/max 100 with stable creation-time/UID order, canonical signed cursor binding namespace UID/principal authorization context/predicate/first-page window. Blank-query list window is the24 h ending at first-page time. To support quiet/non-sideband/closed-client recovery without scanning, the initial report lookup subset MUST also implement exact positive conjunctions on spec.repositoryRef.uid plus spec.target.commit (or previousCommit for deletion), with optional spec.target.ref, through spec 073's owning parser/registry and indexed plan. Example query: spec.repositoryRef.uid:"<repository-uid>" spec.target.commit:"<full-oid>". No local competing parser, ranking or search engine is needed. Unimplemented terms/combinations fail existing unsupported-query diagnostics; streaming/DEP-016 recovery readiness requires this exact subset. The exact-revision route spans retained90-day history, pinned to first-page creation-time high water, using bounded segment metadata/index pages; each page scans<=256 candidates/8 index partitions and may return an empty page with continuation. Namespace/principal/repository checks apply before disclosure. No exact totalCount, query input/Payload or fetch-all sorting.

Nested status.resolved.diagnostics is an AdmissionDiagnosticConnection with first/after/last/before/query, default 50/max 100 and ordinal order; no namespace arg because it is a value collection under one authorized report. A cursor binds report UID/snapshot digest/predicate/authorization and finite retention. Query initially blank-only. Pin one snapshot for a response; reject mismatched/expired snapshot cursors rather than mix newest findings. Every nested read repeats current authorization, including reads from a historical watch object. Diagnostic values keep AdmissionDiagnosticConnection pagination without a synthetic global Node identity/resource envelope. The edge node field is a value type; it does not require the Node interface.

watchAdmissionReports(namespace, resourceVersion, query) and generic watchResources(AdmissionReport) use existing journal/bookmark/gap semantics and the same report-summary source. Register report summary source/row discriminator in RunCatalogCDC, generic kind dispatch and node resolution. No new journal runner or process-local fan-out. Generic watch currently uses selector; spec 073 owns its query migration. Do not redeclare generic watch in this overlay; integrate that canonical change once. New typed report watch follows query convention, rejecting unsupported terms. Fleet watch requires explicit cluster authority; missing namespace is not implicit global access. DELETED/BOOKMARK report/object is null, including retained replay; expired report content is not revived by historical snapshot reads.

## Shared diagnostic projection

Extend admission.Diagnostic and every existing owning renderer together: reason/message/level/file/field, optional validated region. Level remains FAILURE/WARNING/NOTICE. GraphQL fields mirror these names; errors preserve code/diagnostics/phase/commit and current sanitized FORBIDDEN behavior. No new query-specific extensions or invented title/severity parallel contract. Region uses1-based lines and Unicode-code-point columns with exclusive endColumn; absent/invalid locations are omitted, not synthesized from byte offsets. Keep filenames repository-relative, reject traversal/absolute paths and redact secret-bearing content before storage/output. Do not expose source snippets or arbitrary stack traces.

Existing extensions.admission remains an array of per-mutation-field entries. Preserve response path aliases/list indices, optional commit and diagnostics. Add optional executionId, reportId and attempt, through an explicit correlated-result collector path (including clean success with diagnostics:[]). Existing Report(ctx, commit, nil) remains a no-op unless a real correlated result was recorded, avoiding accidental response expansion for unrelated calls. Exact serialized shape, casing and optional omissions have golden fixtures shared with persisted diagnostic/SARIF/Git projections.

Example proposed compatible entry:

```json
{
  "path": ["updateProduct"],
  "commit": "0123456789abcdef0123456789abcdef01234567",
  "executionId": "<execution-id>",
  "reportId": "<report-relay-id>",
  "attempt": 1,
  "diagnostics": [
    {"reason": "TAX_CODE_MISSING", "message": "Taxable variants require spec.taxCode", "level": "WARNING", "file": "products/coat.md", "field": "spec.taxCode"}
  ]
}
```

The warning is illustrative, not a newly implemented Product rule. GraphQL response path is not manifest field/file. Streamed human diagnostics have separate output omission counts; neither stream loss nor finding truncation changes a known failed outcome into success. Clean successful mutation responses retain resource/Payload completion semantics; correlation is additive. If durable acceptance is uncertain, existing owner errors carry safe commit/report correlation when proven, never a false rollback or success-shaped placeholder resource.

## Proposed finite action additions

| Action | Scope / enforcement |
| --- | --- |
| admissionReport.read/list/watch | Namespace plus source repository/report disclosure; nested diagnostics, SARIF export and generic node/watch cannot bypass it. Export uses read on each report with its diagnostic disclosure checks; no additional export permission. Fleet watch separately cluster-authorized. |
| admissionReport.create | Namespace/target; authorizes requesting another attempt through the typed rerun command, plus current original-source/producer operation authority and required audit coverage. It does not permit caller-authored report results. |
| repository.admissionNotes.write | Service-only target-bound native notes projection and explicit repository-wide safe-summary disclosure grant. No arbitrary ref/content writes. |

Add vocabulary in shared/schemas/authorization-actions.json with generated Go/Rust provider coverage. Source receipt recovery and existing repository.contents.* authority retain their owners. Reuse create/read verbs for attempts and representations; GraphQL rerun/export operation names are not new authorization verbs. No admission worker claim/complete action. Service subjects remain cluster-scoped system:serviceaccount:<name>, with namespace reach via bindings. Validate report collector/producer identity before feature readiness, not fabricated actor metadata.

## Errors, retention and configuration

Use existing admission.Error.ToGQLError codes and diagnostics; sanitized FORBIDDEN is unchanged. BAD_USER_INPUT covers INVALID_FIELD/INVALID_ENVELOPE/UNSUPPORTED_CAPABILITY/cursor input. CONFLICT covers request hash/version/stale target fence. FAILED_PRECONDITION covers ADMISSION_PENDING, EFFECT_UNCERTAIN, SOURCE_GAP, REPORT_EXPIRED, REPORT_INPUT_EXPIRED, SNAPSHOT_EXPIRED, REPORT_NOT_COMPLETED, AUDIT_UNAVAILABLE and CONFIGURATION_UNAVAILABLE. Preserve owning watch expired/gap errors. An absent unknown ID does not leak whether a protected report once existed; retained authorized expiry evidence permits explicit expiry.

Proposed typed settings belong under API admission recording/execution/observation/export/notes and Git transport/source ownership, using existing Viper/environment conventions. Exact keys/default decoding fixtures are implementation work. Namespace policy cannot enlarge operator bounds or choose another privileged producer. Independent feature flags gate durable execution, report reads/watch, streaming and export/notes. Startup rejects unbounded pack/file policies for durable mode, unknown producer recovery policies, absent source/effect primitives, invalid principal/secret bindings and quotas larger than allocated storage. Legacy mode cannot inherit durable readiness by sharing a binary.

Terminal report/snapshot 90-day visibility is distinct from protected14-day summary CDC/7-day default journal copies and Git notes history. Original rerun input/effect proof retained at least 7 days after terminal and while pending obligations reference it. Explicit cleanup uses bounded due pages<=100 and expected UID/version/deadline guards; report deletion emits journal DELETED and findings cleanup follows recoverable intent. Do not delete a snapshot still needed by an accepted export inside its bounded lifetime; export expiry is at most15 min and never exceeds report/snapshot expiry. Short physical-erasure policies unsupported by these copies fail configuration, rather than imply90-day or7-day cleanup proves erasure everywhere.
