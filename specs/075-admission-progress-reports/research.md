# Research: Admission Progress and Durable Reports

**Date**: 2026-11-22. Runtime baseline f8bd3bca; requirements saved at 982502f8. Decisions are proposed contracts, not implemented behavior. Three read-only research tracks examined execution/recovery, receive-pack streaming and report/diagnostic/export contracts.

## R1 — Preserve the shared executor, replace callback success inference

**Decision**: API-owned durable execution wraps the existing CommittedManifestAdmitter/catalog admission rules. Introduce a typed internal batch result retaining every accepted/no-op/denied/failed entry, dependency ordering and incomplete work. Never infer report success from an empty callback response.

**Rationale**: `internal/admission/committed_manifest.go` defines the shared interface; GraphQL `internal/admin/graph/resolver/service.go:249–294` uses it. `internal/cataloggrpc/server.go:1259–1382` has early empty-success returns on unavailable dependencies, resolution/stale checks and convergence exhaustion. `applyResourceOperations` currently discards most decisions. The short repair loop in `cataloggrpc/committed_manifest.go:225–278` is not a durable queue.

**Alternatives considered**: Another admission engine drifts. Treating callback completion as semantic success fabricates results. Executing every manifest as an independent singleton loses push-set/category dependency semantics.

## R2 — Source receipts and shared semantic effects

**Decision**: Converge callbacks, retained #139 ingestion and applicable GraphQL writes on the source receipt proposed by spec 074: repository UID/epoch/sequence, mutation identity and actual ref transition. Add that receipt additively to owned write results/callback context, with durable operation lookup for response loss. Root scopes may differ; semantic leaf identity does not include entry transport/root scope. Store original input and a sealed dependency manifest before acknowledging source acceptance. Native mutation identity remains internal; omit public mutationId and retain batch/execution/attempt/revision correlation.

**Rationale**: Current write responses and `gitclient/write.go` return SHA/branch only. A GraphQL path-subset root and later full-ref source root must share leaf effect receipts. A->B->A is not one occurrence. Equal-content GraphQL no-write paths cannot invent a source event. `system:api` backing-write identity and authenticated initiator are distinct; Git AuthorName is not actor proof. Read spec 074 contracts at draft PR602/branch codex/074-phased-events-delivery; do not copy its source-log ownership into this feature.

**Alternatives considered**: Commit/ref/hook alone aliases transitions. Root-only deduplication double-applies overlapping paths. Current-ref lookups cannot reconstruct historical admission input.

## R3 — Fencing belongs at the effect owner

**Decision**: Require producer-owned Prepare/ApplyIfExecutionCurrent/VerifyEffect primitives before durable producer readiness. Target guard, immutable effect receipt and authoritative change must share that owner's atomic authority. Checkpoints/leases coordinate work but cannot substitute for target checks. Deletion requires UID-bound tombstone/effect evidence; absence is not proof. Unknown effects remain paused/unresolved and are not automatically rerun.

**Rationale**: `cataloggrpc/server.go:1171–1193,2779` checks Git before applying, while Scylla Product UPDATE (`datastore/scylla/backend.go:2263–2282`) checks resource_version only. A stalled worker can cross that gap. Existing `scylla/recovery.go` and backend mutation/index repair are reusable but not universal effect idempotency. [Scylla conditional batch rules](https://gocql-driver.docs.scylladb.com/stable/statements/lightweight-transactions.html) constrain all statements to the same table/partition; separate work and target partitions are not one transaction.

**Alternatives considered**: A separate effect receipt after mutation loses evidence on crash. A work lease checked before a target write cannot fence it. Blanket exactly-once effects are unsupported. Each kind's existing create/name-claim, update and deletion authority needs its own verified implementation, not a fictitious universal CQL batch.

## R4 — Producer-owned authorization, independent observation

**Decision**: Record immutable authorization/provenance evidence at acceptance and use explicit producer recovery policy. Ordinary catalog recovery applies its current owner checks; already-authorized committed RBAC projection completes under its accepted proof and is not re-vetoed by later actor revocation. Observation, export, notes disclosure and explicit rerun always require current authority. No historical snapshot grants indefinite arbitrary execution/export.

**Rationale**: `cataloggrpc/rbac.go:29–35,85–147`, `admission/rbac_projection.go:16–22` and `cataloggrpc/context.go` separate committed RBAC projection from ordinary catalog checks. Conflating the two would change existing security behavior.

**Alternatives considered**: Blanket reauthorization can strand an authorized committed RBAC candidate. Blanket reuse of historical actor permissions bypasses revocation for unrelated operations.

## R5 — Attempt reports and sealed diagnostic snapshots

**Decision**: AdmissionReport is a datastore-only Node using canonical ObjectMeta/spec/status/Condition/resolved/body. One report per root/producer/attempt; terminal attempts are immutable. Findings are bounded separately paged immutable snapshots. The report row holds summary/counts/snapshot identity, not megabyte findings in each CDC image. Source replay and automatic resumption share the accepted attempt; an explicit rerun has a new run identity/attempt and audit provenance, not permission to duplicate already-applied effects.

**Rationale**: Shared owners are `shared/schemas/admin/schema.graphqls`, `repository.graphqls`, `product.graphqls` and common/node.graphqls. Generic `WatchEvent.object` remains JSON and DELETED/BOOKMARK have no object. Register report summary rows through existing catalog CDC/journal; no report event bus. Terminal status follows sealed findings and verified child completion, with idempotent contribution markers rather than live aggregate queries.

**Alternatives considered**: Replacing annotations on retry destroys prior evidence. A 1,000 x64-KiB message ceiling allows roughly 64 MiB/report. An unbounded nested list or full diagnostics in every watch payload defeats count/byte budgets.

## R6 — Extend existing diagnostics, preserve response shape

**Decision**: Extend canonical admission.Diagnostic additively with optional validated region. Keep reason/message/level/file/field and existing error extension vocabulary. Explicitly record correlated clean-success entries through a new collector method; do not change the old nil-diagnostics no-op implicitly. Add executionId/attempt/reportId correlation beside path/commit/diagnostics in extensions.admission.

**Rationale**: `admission/error.go:26–43,131,165` owns levels and shared formatting. `admissionreport/extension.go:52,75–95` skips empty findings and renders response paths separately. No current GraphQL Diagnostic type exists. Quiet/non-sideband recovery additionally requires a bounded exact repository/revision lookup subset in the owning shared query parser/registry, rather than a competing commit filter or90-day scan. Findings remain values in AdmissionDiagnosticConnection, not globally addressable Node resources. Attempt requests use admissionReport.create; SARIF reuses admissionReport.read and diagnostic disclosure. Neither requires a new authorization verb.

**Alternatives considered**: New severity/title keys compete with canonical reason/level. Making every current Report(nil) call emit changes unrelated responses. Human sideband text cannot serve as the machine contract.

## R7 — Typed additive Git streaming and API observation

**Decision**: Add ReceivePackStream bidi RPC without changing legacy ReceivePack cardinality. Begin pins signed repository/context, operation identity, refs and negotiated capabilities; Data <=64 KiB; End and half-close authorize finalization. Git emits typed publication truth. API alone encodes new-path pkt-lines and observes its own durable admission reports after publication; Git does not call back to watch them. Finish consuming HTTP upload before flushing the response.

**Rationale**: `gitclient/grpc_client.go:125,229` currently buffers final response and discards capabilities; `githttp/handler.go:146` uses c.Data. `grpc/server.rs:2820–2878` currently sideband-wraps status unconditionally. `pack_server.rs:22` advertises atomic/quiet without forwarding capability selection. Preserve current release of repository guards before post-receive work (`server.rs:2624`), and qualify/remove unsupported advertisement rather than silently honor atomic by partial publication.

**Alternatives considered**: Changing existing response cardinality breaks rolling compatibility. API->Git->API report polling adds transport/ownership complexity. Fallback after upload may duplicate a push. Flushing before consuming HTTP/1 request would require extra full-duplex guarantees; this feature does not need them.

## R8 — Truthful bounded observation and compatibility

**Decision**: Pushes wait for required post-receive admission outcomes by default while negotiated sideband streams progress. Quiet and non-sideband retain waiting semantics with their output restrictions. No separate short default observation timer. Explicit detach/disconnect/operational transport or authentication failure ends only observation; accepted execution continues. Finite queue/execution/retry budgets and corresponding transport/proxy limits govern timeout or explicit unresolved results, never fabricated completion. At admission outcome (or explicitly reported timeout/unresolved disposition), emit actual ref status/flush; admission failure never turns committed refs into ng or a channel3 fatal. Custom push options remain unavailable until genuinely negotiated/parsed/signed.

**Rationale**: Primary contracts: [Git capabilities](https://git-scm.com/docs/gitprotocol-capabilities), [report-status](https://git-scm.com/docs/gitprotocol-pack#_report_status), [Smart HTTP](https://git-scm.com/docs/gitprotocol-http). Channel1 contains inner report-status pkt-lines, including its flush, inside valid outer sideband packets; channel2 is human progress. Proxy idle/write timeouts must support quiet intervals as well as heartbeats. A closed Git response is not resumable history.

**Alternatives considered**: Automatically detaching at60 seconds or on non-sideband changes the intended synchronous post-receive experience. Non-sideband/quiet cannot promise progress text, but still wait. Invented push options or always-sideband framing violate negotiation. New RPC fallback is permitted only after pre-upload capability/version selection, never after uncertain publication.

## R9 — Bounded export and safe native notes

**Decision**: SARIF 2.1.0 plus Errata 01, one run per report attempt, complete independently valid bounded documents through authorized GraphQL. Map reason to ruleId; FAILURE/WARNING/NOTICE to error/warning/note; validated Unicode-code-point locations with exclusive end column. API-internal notes projector uses a narrowly signed native Git command with expected-ref CAS and a report-revision fence. Notes contain only fixed safe repo-reader facts; diagnostic bodies remain behind report permissions. Protect the reserved notes namespace on every writer and exclude it from source/admission business extraction.

**Rationale**: [OASIS SARIF normative text](https://docs.oasis-open.org/sarif/sarif/v2.1.0/sarif-v2.1.0.html). Current Git guards hide/reject refs/gitstore only (`grpc/server.rs:2338`, `pack_server.rs:193`); notes protection/RPC/SARIF are new work. API projection avoids new controller worker GraphQL commands. Git notes are fetchable by repository readers and cannot implement narrower report authorization.

**Alternatives considered**: A new unauthenticated-looking download URL adds surface. Unbounded export is unsafe; JSON fragments are not valid SARIF. Current-ref CAS alone can still allow an older projector to overwrite a newer derived snapshot; target revision guarding is required.

## R10 — Retention, bounds, capacity and lifecycle

**Decision**: Retain terminal report 90 d, original immutable rerun inputs/effect receipts7 d after terminal (pending pinned), report snapshots within report lifetime, finite cleanup/queue budgets. Snapshot/root rerun expires explicitly when input is unavailable. Use existing Make capacity/chaos families, replica/fault/rolling fixtures and normal checks; ADR0015/0018 remain Proposed until their core actually ships, with status/index assessment in implementing tasks.

**Rationale**: Live report retention does not erase14-day CDC/7-day journal copies or Git notes history. A90-day workload at 10 reports/s is77.76M reports, not merely the100K baseline; sizing must include retained duty cycle, input bytes, snapshots, indexes/CDC and replication. See capacity-acceptance.md. No runtime qualification is claimed.

**Alternatives considered**: Reachability-driven early expiry adds historical ancestry scans. Purging pending inputs or treating missing effect receipts as permission to reapply loses correctness. Accepting an entire lifecycle ADR when only reporting ships overstates implemented scope.
