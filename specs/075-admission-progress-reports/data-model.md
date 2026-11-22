# Data Model and Ownership (Proposed)

All public records use the canonical resource envelope. AdmissionReport is namespaced and datastore-only: id/apiVersion/kind/metadata/spec/status/optional body. Real provisioned admission service identity owns its ObjectMeta; authenticated initiating actor is retained private evidence, not fabricated ownership. Generation is1; spec/body are immutable. Nullable observedGeneration/lastAppliedRevision are not invented desired-state reconciliation. Conditions report ExecutionAvailable/ResultsComplete as appropriate, not catalog Ready.

## Identities and entities

| Entity | Identity and immutable content | Mutable authority / lifecycle |
| --- | --- | --- |
| Admission source receipt | Existing #139/spec 074 source repositoryUID/epoch/sequence/mutation and actual ref old/new object IDs, plus content digest. | Verified publication evidence; not proof admission succeeded. Callback/write result/source ingestion converge here. |
| Command root | Source receipt + producer/version + phase + scope digest; reconciliation-only origin explicitly lacks a new source transition. Original actor/service/accepted authorization policy and immutable input manifest. | PREPARING -> ACCEPTED -> RUNNING -> RESOLVED or PAUSED. Source acknowledgment waits for sealed recoverable leaf obligations, not terminal execution. |
| Semantic leaf / effect | Source receipt + producer/version/phase + canonical path/target incarnation + derived operation + old/new content digest, independent of root/entry transport. | Owner-controlled prepare/apply/verify receipt. Shared by overlapping GraphQL subset/full source roots. Recovery never infers an old effect merely from current content/absence. |
| Attempt / AdmissionReport | Root execution ID + positive ordinal; deterministic report UID/name, previousAttemptRef, phase/producer/target. | QUEUED -> IN_PROGRESS -> COMPLETED with established conclusion; unresolved work stays nonterminal with explicit conditions. Terminal outcome/findings immutable; later metadata cleanup only. |
| Diagnostic snapshot | Report UID + snapshot ordinal/digest, deterministic ordered manifest of immutable finding pages. | Published snapshot never changes. Only current and one previous active snapshot retained while running; terminal snapshot retained with report. Older cursors fail SNAPSHOT_EXPIRED explicitly. |
| Contribution | Report/root + semantic leaf ID + attempt/reason. | Same-partition applied marker with summary counts; duplicate source/worker retries cannot double-count. |
| Batch correlation | Git operation/mutation + bounded per-ref root identities and applicability. | Sealed obligations and asynchronous summary contributions; no public PushReport or live related-resource aggregate. |
| Observer | Authorized repository/namespace + operation/batch/reports, finite policy expiry. | Ephemeral bounded buffer; disconnect ends observation only. Not execution ownership or history authority. |
| Export / notes revision | Export request digest/ordered report IDs/expiry; repository notes desired snapshot ordinal. | Bounded authorized projections with explicit expiry/repair. Never execution truth. |

Opaque runtime IDs use existing UUID/Relay conventions. Deterministic new command/report/effect IDs derive from versioned domain-separated canonical identity bytes (reuse spec 074's deterministic UUID helper when available); persist full SHA-256 identity/content digests and reject collisions or conflicting duplicates. No sequence-only event identity, commit-only aliasing, or IDs as permissions. CatalogObjectReference carries real UID/incarnation; no AdmissionReportMeta or alternate reference model.

## Report target and attempt semantics

POST_RECEIVE denotes actual post-commit execution even when entered through GraphQL. COMMITTED_TRANSITION targets carry public source cursor/actual old/new commits; native mutation identity remains private source/recovery evidence and is not exposed as AdmissionReportTarget.mutationId. Public batchId/executionId/attempt/revision provide correlation; deletion has no new commit and preserves previousCommit plus exact ref. RECONCILIATION targets explicitly represent an existing-revision no-write/no-op request and cannot claim a new Git occurrence. Current feature does not persist rejected pre-receive proposals as accepted PostReceive reports.

Execution/attempt IDs stay stable when a worker resumes incomplete units. Up to3 automatic attempts/root for verified transient infrastructure failures; previous terminal attempts remain visible. Domain DENIED is terminal FAILURE, not automatic retry. Requesting another attempt requires admissionReport.create plus original target authority; SARIF uses existing report/diagnostic read permissions. Explicit rerun allocates another ordinal under a scoped idempotency key (<=128 bytes), max 10 total attempts/root including automatic attempts. It reuses original immutable input and current authorization; already-applied leaf receipts are verified/no-op rather than executed again. Changing policy/producer/version or requesting external business reprocessing needs a separately accepted execution contract and is not hidden inside rerun.

Completed SUCCESS requires every required unit to be accepted/no-op and complete findings. FAILURE reflects known denial/failure regardless of omitted messages; unresolved effect ambiguity prevents terminal conclusion. SKIPPED is for proven no-longer-applicable/superseded units without unresolved effects; NEUTRAL is a completed applicable producer with explicitly informational/no-work semantics. TIMED_OUT requires deadline exhaustion and verified disposition of every unit/effect; otherwise state remains IN_PROGRESS with ExecutionAvailable=False/EffectUncertain. No cancellation result is invented to fit an existing enum.

## Diagnostic storage and watch projection

Canonical Diagnostic has reason/message/level/file/field and optional region; GraphQL values mirror it. Message <=4 KiB, encoded finding <=8 KiB, <=1,000 retained findings and <=1 MiB encoded findings/report attempt. Validate reason<=128 bytes, file/field<=2 KiB each and complete record bound. Unresolved unit count includes all planned units without verified terminal disposition, including queued/in-progress/ambiguous work; terminal completion requires zero unresolved units. Omitted counts can exceed retained count; incomplete enumeration is separate. Do not treat omitted secrets as retained exportable findings.

Deterministic order is sealed manifest leaf ordinal, producer finding ordinal. Persist each leaf's sanitized finding digest/result before contribution. Store immutable pages <=64 KiB and <=16 findings/page, capped by total1 MiB and <=64 pages/snapshot. Running snapshots publish at most once/s; pages may be shared, published bytes never edited. Seal final manifest before publishing terminal summary. An older watch summary can resolve only its pinned snapshot, or return explicit expiry; never combine it with newest findings under the old cursor.

Report authoritative summary/control/marker rows share one report-UID partition/table with explicit row discriminators. Full-image CDC is enabled there; a dedicated catalog adapter projects summary rows only into existing resource_watch journal. Diagnostic pages are in a separate immutable table without CDC; the summary carries a pinned snapshot/manifest digest and counts. Summary history never contains1 MiB findings per update. A report watch cannot expose findings without current nested-field authorization. Generic DELETED/BOOKMARK objects remain null; expiry-aware access suppresses deleted/expired report snapshots consistently. No new journal runner/bus.

## Query-first persistence families

These are proposed forward migrations, not deployed tables. Every cross-partition mutation has an enumerable durable intent and replay verifier; no global transaction is assumed.

| Family | Partition / bounded lookup | Authority |
| --- | --- | --- |
| admission_commands_by_id | Root ID; compact control and paged manifest refs. | Input hash, immutable source/security policy, sealed acceptance and version/fence. |
| admission_recovery_by_bucket | Minute/shard, due time/command ID; <=256 candidates/scan. | Enumerable preparation before canonical command; index is not authority. |
| admission_inputs_by_command | Command UID/page ordinal, <=1 MiB/page. | Original manifests/source evidence <=64 MiB/push; shared leaf blobs may be reused by digest. |
| admission_effects_by_id | Semantic leaf identity, result/target receipt projection. | Locates producer-owned authoritative effect proof; this separate row alone cannot make target application atomic. |
| admission_reports_by_uid | Report UID, summary/control/contribution discriminators; <=5,000 unit contributions plus compact summary. | Same-partition guarded counts/snapshot/status transitions; specialized CDC summary adapter. |
| admission_findings_by_snapshot | Report UID/snapshot/page; immutable page bytes and digest. | Bounded pinned finding snapshot, authoritative for nested reads/export. |
| admission_reports_by_namespace_time / admission_reports_by_execution | Namespace UID/day/shard + timestamp/UID; execution UID + ordinal<=10. | Narrow recoverable projections, authorized fixed-window cursors. |
| admission_reports_by_repository_revision | Namespace UID/repository UID/full target or deletion-previous OID/ref-key/day/segment; bounded segment manifest. | Exact revision recovery query; maintain both all-ref and exact-ref narrow projections,<=10K rows/segment and stable creation-time/UID cursors; no historical report scan. |
| admission_batches_by_id | Batch UID; <=32 ref summaries and paged root refs. | Sealed expected obligations and idempotent completion contributions; no live resolver count. |
| admission_exports_by_key | Principal/scope/idempotency digest; max 16 report refs and15-min expiry. | Reproducible bounded SARIF continuation, not a public object URL. |
| admission_notes_by_repository | Repository UID compact desired ordinal/current intent; <=64 queued commit projections. | API scheduling/provenance only; native note commit ordinal/ref CAS guards target updates. |

Time/due/list partitions have <=10,000 rows/segment and64 shards; rotate segments through recoverable pointers. Report unit markers bound by5,000 selected manifests, not an ever-growing execution log. Store completed report 90 d; original terminal input/effect recovery receipts at least 7 d (longer while required pending roots reference them). Source acknowledgment after acceptance requires source-independent recovery; pending data has no blind TTL. After receipt horizon, old roots are closed and cannot be reaccepted/rerun as fresh work. Notes/export retention does not authorize hidden longer access. See execution-recovery.md for control ordering and capacity-acceptance.md for allocated bytes/retention duty cycle.
