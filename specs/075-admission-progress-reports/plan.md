# Implementation Plan: Admission Progress and Durable Reports

**Branch**: `codex/075-admission-progress-reports` | **Date**: 2026-11-22 | **Spec**: [spec.md](spec.md)

**Input**: Spec075/#501, requirements commit 982502f8, runtime baseline f8bd3bca. Spec074's #139 source/handoff is a proposed dependency in draft PR602, not implemented source infrastructure on this branch.

## Summary

Implement API-owned accepted admission execution and attempt-scoped AdmissionReport around the existing CommittedManifestAdmitter/catalog rules. Git callback, retained committed-source ingestion and synchronous GraphQL writes converge on immutable source/input/producer identity and shared semantic leaf effect receipts. Recovery depends on target-owned guarded effects and verifiers, not work leases or a nil callback error. Reports summarize execution, not ref acceptance or controller readiness.

Add a typed bidirectional ReceivePackStream RPC without changing legacy cardinality. Git returns publication truth; API encodes negotiated Git status/progress and waits for its own durable admission reports after upload/publication before normally finishing the push. Post-receive is synchronous from the push client’s perspective; accepted execution retains an independent recoverable lifetime. Disconnect/explicit detach or operational transport/authentication limits end observation only, with no separate short default observation timer. Shared diagnostics extend current errors/extensions.admission, including explicit clean-success correlation. Bounded authorized SARIF export and an API-internal native notes projector complete the final slice. No second API/controller transport, execution worker GraphQL surface, new core service or message broker.

## Technical Context

**Language/Version**: Go 1.27.2; Rust 1.99.1/edition2021, verified against manifests/toolchain.

**Primary Dependencies**: Existing gqlgen 0.17.90/gqlparser, protobuf/gRPC Go, UUID, CEL 0.29 (shared query owner only), Viper, shared observability/secretmaterial, gocqlx 3.0.4/memdb 1.3.5 and Scylla driver replacement1.18.0. Rust native gix0.90, Tonic 0.14.6/prost 0.14.3, Tokio and existing HMAC/SHA-256 signing. Use standard HTTP/JSON SARIF rendering and existing Buf generation; no subprocess Git, new queue database/parser/SDK or alternative diagnostics package.

**Storage**: Backend-neutral API interfaces, memdb contract backend, forward Scylla query-first migrations. Authoritative report summary/control/markers share a bounded report-UID partition with full-image CDC and summary discriminator. Immutable finding/input pages have explicit digests/retention; existing journal owns report watches. Producer-owned target rows/tombstones/receipts must implement atomic effect proof at their real authority; no generic cross-partition transaction. Git source remains retained singleton storage under spec 074, not a new spec 075 log.

**Testing**: Failing contract/unit/crash/replica tests before runtime slices; merged SDL and proposed RPC compilation, golden error/extension/SARIF/sideband fixtures, backend parity and negative source/target-fence tests. Real Git clients/direct+supported proxy for streaming, cancellation and quiet/no-sideband. Two APIs/two controllers coexistence and rolling tests, one Git replacement, existing capacity/chaos runners. Planning checks do not qualify runtime behavior.

**Target Platform**: Existing Linux core deployments and macOS development; native Git retained storage. No browser/UI or CLI implementation here; CLI compatibility evidence consumes the versioned contracts.

**Project Type**: Existing API/Git services and shared contracts; controller transport remains GraphQL HTTP/WS with ordinary reconciliation coexistence.

**Performance Goals**: SpecPR003–007 and [capacity acceptance](contracts/capacity-acceptance.md).30-minute healthy/phase+combined runs, >=5M products/10K categories/100K baseline attempts;10 committed transitions/s and100 unique manifest units/s with declared native/GraphQL mix;100 observer concurrency and separate5,000-manifest probes. Report lookup/list p95/p99<=250 ms/1 s, durable progress visibility<=1 s/3 s, accepted identity<=2 s, unexpected healthy errors<1%; existing stricter push/reconciliation gates remain.

**Constraints**: Publication/work/execution/observation/readiness remain distinct. No report success inferred from current empty callback responses, no universal exactly-once effects, no cancelled observer owning execution lifetime, no current-state ref polling as historical proof. Preserve original input, UID/epoch identity and producer-specific authorization, including already-authorized committed RBAC projection. Preserve user-approved GitStore metadata/status/diagnostics and spec 073 query direction without requiring an unimplemented search engine.

**Scale/Scope**: Report/execution, shared diagnostics, attached Smart HTTP observation, bounded SARIF and protected rebuildable notes. Initial report phase POST_RECEIVE and built-in producer only; future hook platform/PreReceive reports require their own contract. No SSH/protocol-v2 fetch, controller-ready wait execution, full lifecycle platform, Event/Audit/Svix dependency or Git HA. Spec074/#139 handoff and necessary source receipts are explicit prerequisite work, not all of074.

**Replica/Scaling Model**: >=2 APIs/two controllers for coexistence; persisted command CAS/fences, target-owned effect authority and idempotent contribution/snapshot commits. Notes target checks its own expected-ref/projection revision. No resource_watch_clock generic lease reuse. One active Git on retained repository/source storage, non-overlapping replacement. New-contract work runs only on capable API/native versions; rollback preserves accepted work/input/receipt bindings.

**Authentication/Authorization**: Existing pluggable AuthN/AuthZ and finite generated actions; signed receiver/method/request-bound Git RPC. Authenticated service and initiator evidence stay distinct from author strings. Current report/nested-finding/notes disclosure checks; requesting another attempt uses admissionReport.create plus original target authority, and SARIF reuses admissionReport.read/diagnostic disclosure without new authorization verbs; producer-owned recovery policy preserves RBAC accepted proof without granting blanket historical actor authority. API internal notes requires repository-wide safe metadata grant and narrow native action. No Storefront mounting or worker SDL hiding.

**Load/Backpressure Model**:32 refs/push, refname<=1 KiB, pack qualification<=512 MiB,5,000 manifests/64 MiB selected input total/push and256 KiB/file. Root lease30 s/renew10 s;16 executing roots/API,32 leaf operations/API,256 queued hints; <=5 s leaf call,3 safe transient retries/30 s,300 s attempt deadline,3 automatic/10 total attempts andone active/root. Persistent pending500K units/16 GiB selected inputs fleet-wide. Findings<=1K/4-KiB message/8-KiB encoded item/1-MiB total; immutable pages<=64 KiB/16 entries. Summary at most1/s.64 observers/API,64-KiB pending/256-KiB total,50 diagnostic lines,<=10 progress updates/s, heartbeat<=1/5 s;pushes wait for admission by default, including quiet/non-sideband, without a separate observation cutoff; explicit detach/transport failure preserves accepted work. Transport/proxy budgets cover the configured end-to-end admission budget. Optional output exhaustion suppresses output rather than ending the wait. Export<=16 terminal reports,one complete<=8-MiB SARIF document/page,15-min cursor. Notes<=16 KiB/64 safe entries/commit. Detailed limits and high-water policies are normative in contracts.

**Capacity Profile**: Extend api/endpoint-split, namespace/watch and repository/lifecycle selectors in current Make families. Proposed fixture: two APIs4 vCPU/4 GiB, two controllers4 vCPU/4 GiB, singleton Git 4 vCPU/8 GiB, three Scylla 16 vCPU/64 GiB/6 TiB/RF3, retained source 64-GiB allocation plus repositories. Record actual resources, direct/proxy path and90-day retention duty cycle;100K baseline is not total steady occupancy. Logical new quotas: summaries/indexes256 GiB, diagnostics256 GiB, inputs512 GiB, receipts128 GiB, controls64 GiB; account RF3/CDC/journal/indexes separately and>=20% headroom. No deployed qualification performed.

**Fault Profile**: Existing owned api-restart/controller-restart/journal-interruption plus precise source/producer/proxy/socket barriers and non-overlapping Git replacement. Healthy recovery<=60 s after dependencies ready; after 5-minute/30K-unit outage prove>=200 units/s against100/s offers and drain<=300 s. Twice-load5-minute overload restores healthy acceptance/freshness<=30 s after removal, with separately measured residual drain. Preserve all accepted work, retained baseline and private runtime evidence.

## Constitution Check

Pre-design gates pass as explicit implementation obligations. Post-design review finds no waiver or unresolved contract choice:

| Gate | Designed obligation |
| --- | --- |
| I Test-first | Source/target receipt crash cuts, wire bytes, extensions and replica negatives precede each runtime slice. |
| II Contract-first | Proposed SDL, typed Git RPC, execution/target-owner primitives, projections/auth/error/retention defined before implementation. |
| III Versioning | Additive RPC/schema; canonical source receipt convergence; explicit feature/version floors and safe pre-upload selection/rollback. |
| IV Observability | Separate queue/effect/report/observer/notes/expiry signals; no raw finding/input/secret or high-cardinality metrics. |
| V User journeys | US1 durability, US2 reports, US3 Git observation, US4 GraphQL compatibility, US5 export/notes; fixture-backed independent slices. |
| VI Independent delivery | Stage storage/guards/source and readers before observation/export activation; old API routing never silently acquires new guarantees. Git non-overlap. |
| VII Simplicity | Reuse engine, services, journal, signing and native Git. Separate durable intents/receipts justify crash correctness; no broker or second worker transport. |
| VIII Replicas | Owner checks at target writes plus shared leaf receipts, conditional contribution counts, stale report/notes guards; two-API/rolling proofs required. |
| IX Isolation | Producer policy/provenance, current observer/export authority, namespace UID and repo-wide-safe notes grants. |
| X Capacity | Retained catalogue/report baseline,90-day duty cycle, normal/max-size mix, finite quotas/limits and full healthy/overload/recovery evidence. |

Current code lacks producer-universal target fencing/effect receipts and durable callback results; this plan names them as prerequisites and tests, not evidence of readiness. ADR0015 remains Proposed unless its core lifecycle decision actually ships; reporting alone does not accept hook registry/policy/platform. ADR0018 likewise requires its full core ownership/fencing decision, not just a report lease. Implementing PRs MUST include an explicit core-decision assessment and, if implemented/in force, change status to Accepted(actual permitted date) and update docs/ADRs/README.md in that PR; otherwise retain Proposed and document remaining gaps. Do not predate acceptance or close #501/DEP-016 on design artifacts.

## Project Structure

### Documentation (this feature)

```text
specs/075-admission-progress-reports/
  spec.md / plan.md / research.md / data-model.md / quickstart.md
  checklists/requirements.md
  contracts/
    admin.graphqls / git-stream.proto / catalog-accept.proto
    execution-recovery.md / transport.md / diagnostics-operations.md
    export-notes.md / capacity-acceptance.md
    fixtures/admission-report.sarif.json
```

### Source Code (repository root)

```text
gitstore-api/internal/admission/            # canonical diagnostics / shared engine contracts
gitstore-api/internal/admissionreport/      # existing extension + proposed durable owner subpackages
gitstore-api/internal/cataloggrpc/          # typed decisions / producer-owned effect integration
gitstore-api/internal/datastore/            # backend-neutral records + memdb/Scylla commit/verifiers
gitstore-api/internal/watchjournal/         # existing replay/CDC sequencing, no new bus
gitstore-api/internal/gitclient/            # signed typed stream / source receipts / notes client
gitstore-api/internal/githttp/              # negotiation / pkt-line encoder / bounded observer
gitstore-api/internal/admin/graph/          # authorized report/diagnostic/rerun/export resolvers
shared/schemas/admin/ / shared/schemas/authorization-actions.json
shared/proto/gitstore/{git,catalog}/v1/     # additive stream, acceptance, receipt and notes contracts
gitstore-git-service/src/grpc/ / src/git/    # native typed publication / protected notes target
tests/integration/ / tests/capacity/ / tests/chaos/
docs/implementation/039-resource-lifecycle-hooks.md / docs/ADRs/
```

**Structure Decision**: Extend existing canonical owners; internal admissionreport subpackages may separate persistence/execution from request collector without renaming its extension contract. API-internal notes and observation avoid new controller worker API. Controller-manager needs no report executor/table reader; its actual reconciliation paths still participate in coexistence/rolling evidence. Plans/examples are proposals, not shipped endpoint availability.

## Phase 0 — Research

[research.md](research.md) consolidates three source/protocol research tracks. Resolved choices: typed per-entry outcomes, shared source/leaf identity, target-owned atomic guards/verifiers, producer-specific authorization, attempt/snapshot records, additive bidi transport, truthful finite observation, safe export/notes, finite storage/retention. Primary references are pinned in their contracts. No NEEDS CLARIFICATION items remain.

## Phase 1 — Design

[data-model.md](data-model.md) defines identity/state and query-first authority. [execution recovery](contracts/execution-recovery.md) defines acceptance/effect/terminal ordering; [Admin SDL](contracts/admin.graphqls), [Git messages](contracts/git-stream.proto), [transport](contracts/transport.md), [diagnostics/operations](contracts/diagnostics-operations.md), [export/notes](contracts/export-notes.md) and [acceptance](contracts/capacity-acceptance.md) define external/owner contracts. [quickstart.md](quickstart.md) demonstrates proposed observations and mutations. Canonical agent-context reference points here; proposed schemas are not mounted/generated runtime code.

## Phase 2 — Implementation sequence (tasks generated separately)

1. **Foundations/US1**: failing source/identity/effect/metadata/error fixtures, action vocabulary, forward query-first migrations/interfaces; converge spec 074 source receipt/command handshake and additive Git write/callback result fields. Preserve verified actor/service distinction and finite pre-publication source/input budgets.
2. **US1 durable execution**: typed batch decisions retaining all outcomes/dependencies; enumerable immutable input/work; shared leaf effect ownership/verifiers for every enabled create/update/delete/RBAC path; contribution/attempt recovery and closed-key horizons. Prove target stale-write and unknown-effect behavior in both backends before producer activation. No dual legacy/source executor.
3. **US2/US4 reports+GraphQL**: canonical report/snapshot resource, summary CDC adapter and typed/generic journal integration, guarded terminal counts/snapshots, query/cursor/auth/expiry and the owning shared exact repository/revision query subset; explicit correlated clean-success collector and unchanged synchronous mutation result/error behavior. Fixtures can drive accepted work before source-driven production activation.
4. **US3 Git observation**: additive typed bidi RPC/stubs, capability preservation/advertisement repair, one API wire encoder, complete-upload boundary, API report observer, wait-until-admission completion across negotiated/non-sideband/quiet modes, explicit detach/execution-timeout/revocation policies, supported proxy tests and real Git bytes. Feature selection before sending data; no uncertain push fallback. DEP-016 requires this end-to-end evidence.
5. **US5 export+notes**: bounded terminal SARIF/schema fixtures, cursor/audit controls, API projection intents, exact protected notes writer/read disclosure and native target ordinal/ref CAS; bounded repair/expiry and recursion exclusions. No source-of-truth shift or new worker SDL.
6. **Qualification/lifecycle**: full normal checks, two-replica/rolling/auth/retention/source+effect crash proofs, complete healthy/overload/recovery/proxy runs, design039/storage/source/operator docs and explicit ADR status/index assessment. Preserve pending inputs/source/reports during rollback. Generate tasks separately; do not create tasks.md or runtime code in planning.

## Complexity Tracking

No constitution violation is proposed. Durable preparation, shared leaf receipts, target guards and sealed report snapshots are contractual costs of independent execution/recovery and overlapping entry paths. A detached task, one work lease, current-content comparison or report-only table cannot replace them. API notes projection and typed publication avoid adding services/transports; producer readiness gates prevent unbuilt primitives from masquerading as implemented guarantees.
