# SARIF Export and Native Notes Projection (Proposed)

## Bounded SARIF 2.1.0

exportAdmissionReports accepts 1–16 unique terminal report IDs and a caller idempotency key<=128 bytes. It reuses admissionReport.read and current diagnostic disclosure on every requested report and is audited under the owning coverage policy. Compare the full ordered request hash on duplicate keys; return the same export identity/immutable snapshot refs. Each page returns exactly one complete SARIF document/run for one terminal report, max 8 MiB encoded document. Max 16 pages/128 MiB per export, <=2 concurrent exports/API and<=8 queued, per-page request deadline 5 s, cursor lifetime<=15 min and no later than earliest report/snapshot expiry. No asynchronous public download URL, JSON fragment stream or unbounded archive. Each continuation repeats current scope/report-read/diagnostic disclosure checks; cursor does not confer authority. Repeated key+cursor reads the same document or explicit expiry; it does not create another export operation/audit chain.

Bind export to immutable terminal finding snapshot digests. Running reports return REPORT_NOT_COMPLETED; unknown/expired input fails under normal disclosure policy. Validate encoded SARIF size separately from the1-MiB diagnostic storage limit because JSON escaping/structure can expand bytes. Oversize/unavailable data is explicit failure; never silently truncate again or fabricate findings. This8-MiB output ceiling is a producer conformance bound tested with maximum-size escaped findings; an unsupported input cannot reach successful export readiness.

| Source fact | SARIF projection |
| --- | --- |
| Report producer name/version | run.tool.driver.name/version; no unimplemented hook resource required. |
| Canonical reason | result.ruleId, with bounded deterministic rule descriptors where emitted. |
| FAILURE / WARNING / NOTICE | result.level error / warning / note. |
| Message | result.message.text; no Markdown/HTML template expansion or source snippet. |
| Repository-relative file | escaped relative artifactLocation.uri after traversal/absolute/control validation. |
| Optional validated region | start/end lines/columns,1-based Unicode code points; run.columnKind=unicodeCodePoints and exclusive endColumn. No invented line/column when unknown. |
| Verified submitted commit/ref | run.versionControlProvenance revisionId/branch; deletion identifies previous commit/ref and explicit deletion properties rather than inventing a new revision. |
| Report/execution/attempt/digest/counts | Versioned properties.gitstore: safe provenance, known/retained/omitted counts and incomplete flag. No credentials, private source bytes or recovery tokens. |

Set version 2.1.0 and $schema=https://docs.oasis-open.org/sarif/sarif/v2.1.0/os/schemas/sarif-schema-2.1.0.json. One run/report attempt. Preserve failure conclusion and omissions even when results retain only a subset. The export is a view of safe retained diagnostics; it does not restore deliberately redacted/expired findings or claim static-analysis source coverage beyond admission's actual scope.

Sources: [OASIS SARIF 2.1.0 plus Errata 01](https://docs.oasis-open.org/sarif/sarif/v2.1.0/sarif-v2.1.0.html). Contract fixtures validate against the official pinned schema during implementation, plus independent editor/tool import for actual region/provenance interpretation.

## Notes are a repository-wide derived view

API-internal bounded projector reads report projection intents, not a controller-private datastore reader or new GraphQL execution commands. Reuse native Git/proto/security helpers. Add the narrow WriteAdmissionNote RPC from git-stream.proto with receiver/method/repository binding and finite canonical repository.admissionNotes.write action. Endpoint accepts only typed safe entries, a target commit, expected notes ref OID, projection epoch/revision and idempotency key. It cannot accept arbitrary Git refs, arbitrary note bodies, caller-controlled report URLs or diagnostic text.

Target ref is fixed refs/notes/gitstore/admission. Deny ordinary writes to this exact ref and descendants on receive-pack and every legacy/native write/delete/tag path; do not accidentally reserve unrelated user refs/notes/* without an owned reason. Protect private control refs separately. Admission and spec 074/#139 default business extraction ignore internal notes/control updates, including their recovery and deletion. Git read visibility remains repository-authorized; notes are not hidden from authorized repository readers.

Because report permissions can be narrower than repository read, require an explicit repository-wide safe-summary disclosure grant/configuration before publishing entries. An operator/producer's broad read permission is not proof that all repository readers may see an outcome/count/report identifier. Default omit unauthorized entries. Approved initial entry contains ref, registered producer name/version, attempt, terminal conclusion and report ID only; no free-text summary, resource name/path, finding counts/message, actor or token. If that fixed metadata is not authorized for every repository reader, do not publish it. Report links still require current report authority; URLs are rendered from trusted deployment configuration, not user input. Report/SARIF reads and notes-writing/disclosure permissions remain separate; SARIF does not add an export verb or permission.

Bound each commit note to16 KiB/64 safe entries, deterministic sorted ref/producer/attempt/report order, preserving distinct report attempts until the note bound. If more entries exist, include deterministic latest-per-ref/producer entries and bounded omission count; notes do not promise full report history. Report remains canonical. Git notes use ordinary notes-tree fan-out/native gix tree editing; update only bounded changed paths, never enumerate every repository report or whole notes tree in memory. Max 32 note writes/batch, <=4 concurrent/API, <=64 queued hints/repository; durable desired commit-page intents are enumerable. Rebuild pages<=100 reports and<=1 MiB, retain a cursor and yield between pages.

## Target-checked projection ordering

API schedules monotonic per-repository projection ordinals and immutable render intents (report snapshot/expiry/disclosure revision digest). Native serializes under the existing repository mutation gate, checks expected notes OID, current trusted projection epoch/ordinal and idempotency content, then builds/publishes one notes commit. Record repository UID/epoch/ordinal/render digest in system-owned metadata of that same notes commit; this single-ref CAS is the target authority, not an independent unfenced control-ref update. A matching duplicate returns the accepted OID; conflicting key/digest or lower revision fails. Epoch mismatch needs explicit verified operator recovery; do not reset a counter on restart and overwrite newer state.

An old projector cannot fetch a newer ref and resubmit old payload under the same/another revision. Repair recomputes current authorized render against retained reports and acquires a new API intent/ordinal; the target guard still rejects stale intents. Reserve ordinal before publication through durable intent; crash repair verifies the exact current notes commit or replays with matching receipt, never a fictitious rollback. Terminal report changes/expiry/disclosure updates schedule repair; private Git history may retain prior note commits, so removing an entry does not prove physical erasure. Repository deletion/recreation is UID-bound and old intents cannot write to the new repository.

The notes RPC never triggers admission itself and never changes resource acceptance/reports. Automatic projection retry<=3/30 s with persisted backoff thereafter and explicit degraded lag. Source/repository retirement records cancellation of outstanding projection obligations before cleanup. Rebuild/rollback keeps all authoritative report/work state and respects singleton Git/non-overlap.

The [SARIF fixture](fixtures/admission-report.sarif.json) is illustrative static contract data; its example rule/field are not shipped Product validation behavior.
