# Implementation Plan: Phased Events, Audit and Reliable Delivery

**Branch**: `codex/074-phased-events-delivery` | **Date**: 2026-11-21 | **Spec**: [spec.md](spec.md)

**Input**: `specs/074-phased-events-delivery/spec.md`, baseline `f8bd3bca`.

## Summary

Implement five independently reviewable paths under existing issue ownership: best-effort operational Event, independently governed AuditLog, #139 retained Git source, immutable OutboxEvent materialization, and signed WebhookDelivery. Runtime resources reuse ObjectMeta and the shared status/Condition/resolved shape. Git-source acceptance, admission execution, routing and receiver outcome remain different progress boundaries.

API owns datastore writes, source ingestion, routing and asynchronous delivery-provider handoff/status reconciliation. An internal DeliveryProvider interface has a bounded local dev implementation and a Svix production implementation; Svix owns accepted-message queueing, sending, signing, retries and redelivery. Controller-manager has no webhook sender or execution commands. Singleton Git owns a retained private notification log outside removable bare repositories. No new GitStore service or general event broker; the production delivery dependency is explicit. These are proposed contracts only.

## Technical Context

**Language/Version**: Go 1.27.2; Rust 1.99.1/edition 2021, pinned in current manifests.

**Primary Dependencies**: Existing gqlgen 0.17.90/gqlparser, gocql 1.17/gocqlx 3.0.4/memdb, UUID, CEL 0.29, Viper, shared secretmaterial/observability; Rust gix 0.90, Tonic 0.14.6, prost 0.14.3, Tokio, SHA-256/HMAC. Go standard HTTP/TLS/crypto for dev delivery; Svix service and official Go SDK or bounded HTTP client for production. Pin/qualify the adapter dependency and service contract during implementation. No new general broker/parser/embedded database.

**Storage**: Existing Scylla/memdb backend-neutral contracts plus forward query-first migrations; same-table/partition conditional batches only where specified. Git source uses retained framed protobuf segments/manifest with checksum and explicit flush/recovery, not Scylla or API private-table access. Existing controller disk-v2 queues/checkpoints retain ownership.

**Testing**: Go/Rust failing contract/unit/crash tests before implementation; merged GraphQL/proto validation, memdb/Scylla parity, common dev/Svix envelope/signature fixtures, Svix handoff/status/replay conformance and controlled external-service failure fixtures. Two-API delivery replacement/rolling tests and two-controller reconciliation coexistence use existing capacity/chaos families. Local dev delivery does not qualify distributed production behavior. No runtime qualification is claimed by this plan.

**Target Platform**: Existing Linux core deployments and macOS development; retained storage and fsync/rename behavior tested on supported filesystems.

**Project Type**: Three existing services and shared contracts; no browser/UI implementation.

**Performance Goals**: Spec PR-003–007. 30-minute phase and combined runs: 500 offered Event calls/s (125 exact retries, 375 new observations), 100 audited operations/s, 100 eligible resource transitions/s plus 10 Git facts/s (110 total source facts), 100 delivery attempts/s and 10 committed Git ref mutations/s. Baseline >=5M products/10K categories. Acceptance p95/p99 <=250 ms/1 s, source-to-fact <=1 s/3 s, healthy eligible receiver receipt <=2 s/5 s; unexpected healthy errors <1%. Existing stricter gates remain.

**Constraints**: [Operations](contracts/operations.md), [storage/recovery](contracts/storage-recovery.md), [retention](contracts/retention.md), [Git source](contracts/git-source.md), [delivery wire](contracts/delivery-wire.md), [provider interface](contracts/delivery-provider.md). No audit guarantee from logs, no occurrence recovery from current-state relist, no post-commit rejection, no exactly-once HTTP effects.

**Scale/Scope**: Event/Audit/outbox/delivery are authorized management/service resources. Storefront gets no internal history or watch access by implication. No new AdmissionReport engine, identity email sender, recommendation ingestion, lifecycle-hook platform or Git replication. Configuration/subject/report/SecretRef contracts retain their owners.

**Replica/Scaling Model**: >=2 APIs/>=2 controllers for coexistence; API per-key CAS/fences protect source/routing/provider handoff and normalized status. Stable submission keys and provider reconciliation handle ambiguous acceptance; provider receipts bind runs/destinations and prevent stale status regression. Svix owns distributed HTTP delivery. No resource_watch_clock lease reuse. Dev is process-local and disallowed in production. One Git process retains repositories/source segments without overlapping replacement. Activate only on compatible API/provider contract versions; replacements preserve accepted provider bindings.

**Authentication/Authorization**: Pluggable AuthN/AuthZ/UserDir and generated canonical action catalog; cluster ServiceAccount subjects remain `system:serviceaccount:<name>`. Operator-only AuditLog in v1. Recorder provenance is registered to principal/scope; target references do not grant access. Git source read uses receiver/method-bound signed RPC. Provider calls are API-internal with operator-bound credentials/SecretRefs and isolated tenant/destination mappings. Public inspection/replay retain owning permissions; no webhook worker GraphQL actions or schema hiding. Existing errors/diagnostics retained.

**Load/Backpressure Model**: Event body <=4 KiB, full record <=16 KiB, integration data <=64 KiB/full wire <=80 KiB; pages <=100. Event producer buffer <=1,000/3 attempts within 30 s, 100 calls/s/principal (burst 200), 1,000/s/namespace; 8 qualification producers across two namespaces. New-observation mix: 90% over 64 stable series, 10% distinct series; producer rotates stable series names every 15 minutes. Series <=10,000 observations/24 h; live TTL 15 min–24 h/default 1 h. Two APIs each permit 64 concurrent Event/audit writes with 256 queued/1 s deadline. Source batches <=128/1 MiB, router pages <=32 destinations, <=64 active subscriptions/namespace, filters <=64 AST nodes/1,000 CEL runtime cost. Provider API work <=16 concurrent calls/API, queue <=256, <=5 s call deadline, <=100 status records/1 MiB per page and <=256 due candidates/scan; persisted due state/backoff, no unbounded retries. Handoff ownership lease 30 s/renew 10 s guards local state, not remote effects. Dev sender <=8 workers/2 per destination, <=256 queue, <=5 s attempt/2 s HTTP, <=8 KiB response, <=12 attempts/24 h, full jitter 5 s to 1 h. Svix retry/age/rate/concurrency/replay limits are validated versioned profile values, not dev defaults. See capacity contract for finite storage and drain bounds.

**Capacity Profile**: Extend existing api/endpoint-split, namespace/watch, repository/lifecycle runners through existing selectors; new event probes are proposed. Qualification allocation: two APIs 4 vCPU/4 GiB each, two controllers 4 vCPU/4 GiB each, singleton Git 4 vCPU/8 GiB with >=64 GiB dedicated source-log budget plus repository volume, three Scylla nodes 16 vCPU/64 GiB/6 TiB each (RF3); owned receiver fixture reports resources/latency. Production evidence also records the actual Svix service/version/account allocation, quotas, policy revision, custody/retention and provider-call latency; local dev cannot substitute. Record existing catalogue storage separately. Normal fixture: Event body 256 B/full summary <=1 KiB; audit record <=1 KiB (up to two records/operation); fact/wire <=2 KiB. Retention occupancy estimates include CDC pre/postimages, journal, 24-hour receipts/markers, 30-day audit and seven-day terminal replay; max-size probes are separately attributed. Startup validates assigned storage quotas fit provisioned capacity with >=20% headroom; runtime quotas apply explicit backpressure when a maximum-size mix would exhaust retained space. These allocations are proposed acceptance fixtures, not measured sizing advice.

**Fault Profile**: Existing owned api-restart/controller-restart/journal-interruption, plus bounded receiver/storage failure and non-overlapping Git replacement crash cuts within existing harnesses. Recover healthy processing <=60 s after readiness and stricter existing deadlines; overload <=30 s. For controlled five-minute outages cap backlog at 33,000 facts/30,000 deliveries, then prove >=220 facts/s versus 110/s offers and >=200 deliveries/s versus 100/s offers, draining <=300 s after readiness. Cleanup lag <=60 s when healthy. No live faults during planning.

## Constitution Check

Pre-design gates passed as obligations; post-design review confirms no waiver is required:

| Gate | Contract and evidence |
| --- | --- |
| I Test-first | Crash/race/auth/expiry/signature/source fixtures precede each runtime slice. |
| II Contract-first | Shared SDL, Git RPC additions, persistence states, wire/security and retention are specified before implementation. |
| III Versioning | Additive inactive contracts; explicit source/cursor/schema versions, unknown-version failures and guarded activation/rollback. |
| IV Observability | Independent loss, audit coverage, lag/gap, routing/backlog, attempt and cleanup signals; private/high-cardinality data excluded. |
| V Stories | US1 Event; US2 Audit; US3 #139; US4 Outbox; US5 Delivery, each independently tested with fixtures for prerequisites. |
| VI Independent delivery | Deploy API storage/provider adapters before activation; verify provider profiles and version floors. Drain incompatible replicas; preserve accepted bindings/work. Git replacement is non-overlapping. |
| VII Simplicity | Existing services/datastore/secret resolver; provider abstraction delegates production delivery to Svix instead of building a distributed sender. Private Git log preserves single-writer source occurrences. |
| VIII Replicas | Guards at target state rows, separately keyed ownership, two readers/workers and stale-fence/crash tests. No source lease reuse or Git HA claim. |
| IX Isolation | Producer binding, namespace UID references, operator audit/export, source/export authority and current delivery revocation. |
| X Capacity | 5M retained baseline, explicit offered mix/fan-out/storage/durations, full healthy/overload/recovery correctness. |

ADR lifecycle is an explicit implementation deliverable. Amend design 039 for split progress, epoch identities, API status and terminal delivery authority. In the implementing PR, evaluate ADR 0018's core target-checked ownership/fencing decision: if brought into force, change to `Accepted (YYYY-MM-DD)` and update its index using actual implementation/date evidence; otherwise retain Proposed and document the remaining core gap. Do the same explicit core-decision check for ADR 0015, which this event-only work does not implement in full. Do not accept all lifecycle hooks, App installation or field ownership by association. Close spec 074 only after its required slices/evidence are complete; individual child issues close on their own verified behavior.

## Project Structure

### Documentation (this feature)

```text
specs/074-phased-events-delivery/
  spec.md / plan.md / research.md / data-model.md / quickstart.md
  checklists/requirements.md
  contracts/
    admin.graphqls / operations.md / storage-recovery.md
    git-source.md / git-notifications.proto / delivery-wire.md / delivery-provider.md
    retention.md / capacity-acceptance.md
```

### Source Code (repository root)

```text
shared/schemas/admin/                     # canonical resource/envelope/SecretRef inputs
shared/schemas/authorization-actions.json # source-owned action vocabulary
shared/proto/gitstore/git/v1/             # additive Git source RPC/messages
gitstore-api/internal/events/             # proposed recording/aggregation/expiry owner
gitstore-api/internal/audit/              # proposed trusted capture/recovery owner
gitstore-api/internal/integrations/       # proposed source/materializer/router/provider handoff
gitstore-api/internal/integrations/delivery/ # proposed provider interface + dev/Svix adapters
gitstore-api/internal/admin/graph/        # owning schema/models/resolvers
gitstore-api/internal/datastore/          # backend-neutral contracts and implementations
gitstore-api/internal/watchjournal/       # existing shared source/replay authority
gitstore-git-service/src/git/notifications/ # proposed retained source + mutation gate
tests/integration/ / tests/capacity/ / tests/chaos/
docs/implementation/039-resource-lifecycle-hooks.md
docs/ADRs/ / docs/storage-groups/
```

**Structure Decision**: Proposed packages follow service ownership; controllers never use API tables. Reuse shared source generation, metadata/reference/status, watch CDC sequencing and HTTP/secret clients. Delivery follows the existing provider pattern with internal Go operations; public GraphQL covers management only. No duplicate resource metadata or free-standing bus.

## Phase 0 — Research

[research.md](research.md) resolves metadata/reference amendments, exact observation counting, retention/privacy, audit coverage, Git commit/replay, per-key state, activation and wire signing. Source limitations are recorded rather than assumed implemented.

## Phase 1 — Design

[data-model.md](data-model.md) and [contracts](contracts/) define entities, mutation boundaries, storage/query paths, public/service interfaces, retention and fault oracles. [quickstart.md](quickstart.md) gives proposed caller/receiver workflows. Agent context points to this plan through canonical AGENTS/CLAUDE linkage. No unresolved clarification blocks task generation.

## Phase 2 — Implementation sequence (tasks generated separately)

1. **Foundations**: failing contract/auth/error/metadata/reference fixtures, action catalog additions, forward migrations and bounded backend interfaces; update generated artifacts together. Shared query grammar is reused when available; v1 supports blank queries plus bounded default windows until that parser subset is implemented, with explicit unsupported-query errors.
2. **US1 / #503**: recording receipts/series CAS, summary-only CDC adapter, safe list/watch, expiry/quota/retention policy and management docs. Disable/throttle diagnostics without changing primary correctness.
3. **US2 / #504**: trusted capture, independent retention/operator access, required operation-intent/recovery integration and bounded async coverage; no public audit append or generic audit watch.
4. **US3 / #139**: source log/mutation gate across every writer, additive signed RPC/bounded ref listing, API durable work ingestion/recovery and #501 handoff. Existing direct callback and new consumer must share one idempotent accepted-work identity before activation; never run two independent admission executions. Audit/report coverage integration remains explicit.
5. **US4 / #505**: versioned historical source facts, gap control rows, deterministic outbox/routing manifests, source-position activation and enumerable delivery handoff intents. Resource source can ship before Git adapter. No provider/HTTP completion claim.
6. **US5 / #506**: internal DeliveryProvider interface/registry, dev adapter and Svix production adapter; common envelope/signature fixtures, idempotent destination/submission, bounded status reconciliation, policy capability checks, terminal/replay and disable handling. Remove worker GraphQL commands/types/actions; no controller sender. Test response-loss/idempotency expiry, immutable destinations, provider outages and two-API replacement. Extend Git-backed registration admission; do not build a parallel configuration store or Svix retry scheduler.
7. **Qualification/close-out**: full normal checks, two-replica/rolling/crash/load/retention oracles, design/storage/operator docs and explicit ADR status/index tasks based on implemented core decisions. Preserve data and pending work during rollback. No tasks.md or implementation created by this command.

## Complexity Tracking

No constitution violations. Added mechanisms are necessary contractual costs: same-partition applied markers prevent duplicate counts; separate durable intents recover cross-partition writes; private retained Git segments preserve occurrences absent from ref snapshots; named control rows prevent stale worker writes; pinned revision/manifests prevent retry retargeting. A process-local cache, one cursor or assumed global transaction cannot replace them safely.
