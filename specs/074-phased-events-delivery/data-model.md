# Data Model and State Ownership (Proposed)

All public stored resources reuse canonical ObjectMeta. Real system ownership is captured at creation: Event owner is the authenticated producer; audit/outbox/delivery owners are configured provisioned service identities validated before readiness. Actor/initiator evidence is separate from metadata ownership. Namespace scope binds its UID/incarnation internally, not only its slug. No fabricated metadata for anonymous actors; the system collector owns their audit records.

## Resource shapes

| Resource | Immutable spec/body | Mutable observation/progress |
| --- | --- | --- |
| Event | type, reason/action, regarding/related/report CatalogObjectReference, registered reportingController/instance, producer eventTime, bounded body. | status.resolved.series count/lastObservedTime, accepted first time, expiresAt/policy revision, closed state. Shared status fields; generation 1, resourceVersion advances. |
| AuditLog | server-captured stage/outcome, operation/request IDs, verified actor/provider/service/initiator facts, canonical action, safe optional resourceRef, received/occurred time, reason and optional redacted body. | None. Distinct immutable Intent/Completion/Uncertain records; no public mutation/status/watch. |
| OutboxEvent | source identity/cursor/version, semantic event type/extraction index, occurredAt, compatible resourceRef, immutable safe data and exact serialized CloudEvent bytes/digest. | status.resolved materialization/routing phase and pinned manifest progress. No retry result in immutable fact. |
| EventSubscription | Git-backed namespace desired configuration: selected source domains/kinds/types, bounded CEL filter, HTTP sink, canonical SecretRef, provider policy profile and body. | status.resolved activation/source bounds, Ready/Degraded conditions, API-materialized backlog summaries and current revision. |
| WebhookDelivery | eventRef, subscriptionRef, activation revision, pinned destination/configuration digest and ordering identity. | status.resolved phase, nullable observed attempt/scheduling facts and bounded safe outcome; private provider receipts, expected-version guards and replay rows preserve provenance. |

ObjectMetaInput.name is a real caller-selected series/configuration name, not an optional generateName surrogate. Recording rejects resourceVersion/forbidden scope or metadata fields. CatalogObjectReferenceInput gains optional UID in its canonical owner; recorder requires it for retained regarding/related references and treats deleted references as authorized producer assertions. No EventReference. Existing GraphQL SecretRef gains one canonical paired SecretRefInput for authored subscriptions; shared/secretmaterial validation remains authoritative.

## Internal entities and transitions

### ObservationReceipt and EventSeries

A namespace-UID/name claim binds exactly one producerBindingUID and seriesUID/fingerprint. Other producers using that canonical name conflict; EventBy.namespacePath never needs a producer selector. Keep the claim through every receipt/intent recovery horizon.

Receipt key: `(namespaceUID, producerBindingUID, SHA256(idempotencyKey))`; value binds canonical spec/body hash, fingerprint, selected series UID, server times and state. Do not log/store raw caller keys outside the protected bounded receipt where unnecessary. Series fingerprint includes namespace UID, chosen series name, regarding UID, logical producer, reason/action/type, report/correlation and body digest; reportingInstance is informational and excluded, so replicas of the same producer can aggregate. First recorded instance stays in immutable spec; later instances and producer eventTime are retained only in bounded receipt provenance; stored spec keeps the first values.

Receipt: PREPARED -> APPLYING -> APPLIED, or REJECTED/CLOSED before acceptance. Before PREPARED, persist an enumerable recovery intent; orphan intents are safe to remove after verifying no receipt. A normal recordEvent response is returned only for APPLIED. Timeout after preparation is RECORDING_PENDING/unknown outcome; repeating the same key returns the stable result. APPLIED changes exactly one series count once. No successful acknowledgement depends on an unpersisted secondary queue.

The series partition contains a persistent lifecycle/control row, summary row and applied-marker rows. Control is initialized once through the namespace/name claim and can never be recreated for the same series after closure. Every append batch checks/updates OPEN control at its expected version alongside summary and marker; absence of summary alone never permits initialization. Conditional batch inserts marker IF NOT EXISTS and changes count/version/expiry IF expected version, all in the **same table and partition**. Initial batch inserts summary and marker conditionally. No counter columns; compute the new Long count/version from loaded state. At 10,000 observations or 24-hour maximum lifetime, reject new observations explicitly; producer chooses a new series name/key. Existing retries still resolve. Refreshed expiry is min(last accepted time + effective TTL, first accepted time +24 h). Expiry atomically closes lifecycle control and deletes summary in one same-partition conditional batch under version/deadline guards; control/name claim/markers outlive every recoverable receipt. An absent summary is not a fresh-series signal. After terminal receipt expiry the same key cannot establish another accepted old observation through an existing pending intent; stale intents are terminal and never reopened.

### AuditEvidence and AuditOperation

Audit UID binds operation/stage/outcome occurrence. A required operation first persists an enumerable recovery command and immutable Intent, then performs its owned conditional side effect, then appends distinct Completion or verified Uncertain evidence. Duplicate command IDs compare canonical intent digest. No transaction across audit table, resource partition or Git is assumed. If the owning side effect cannot be recovered, that coverage class is unavailable; it cannot be labeled REQUIRED_PRE merely by adding a pre-log. Async captures have finite memory/age, explicit counters and no durable claim before append.

### GitMutation and GitSourcePosition

Git owns per-repository incarnation/epoch/sequence and private intent/completion segments. Mutation ID groups actual per-ref outcomes; it is not a resourceVersion or proof of all-or-nothing publication. State: PREPARED -> per-ref verified COMMITTED/NOT_PUBLISHED, or UNCERTAIN/UNREADY. Only complete verified source frames advance published high water. Consumers bind repository UID/epoch and durably accept work before checkpoint; source deletion/history loss has explicit tombstone/gap handling. See [git-source.md](contracts/git-source.md).

### SourceFact, RoutingManifest and ActivationInterval

Canonical identity bytes bind source instance UID, epoch/position, semantic type and extraction index. New runtime fact/delivery UIDs use one shared deterministic UUIDv8 helper from the first 128 bits of domain-separated SHA-256 with UUID version/variant bits; store the full identity digest and reject collisions/content mismatch. UID opacity grants no access.

Fact: CREATING -> FACT_DURABLE -> ROUTING -> ROUTED (or explicit GAP/UNSUPPORTED). Create-if-absent compares the canonical immutable spec/body/source digest, excluding newly computed managed creation timestamps/owner values; an existing fact returns its original metadata and exact bytes. Replays never rewrite stored managed metadata. Routing manifest pins every `(subscriptionUID, activationRevision)` and has deterministic page identities; pages are replayable, not a transient iterator. Source checkpoint can advance after all required facts and a sealed manifest/recoverable routing work exist, without waiting for HTTP. A separate routing cursor proves every delivery was materialized.

Before capture/interval publication, a per-source activation barrier serializes with routing reservations; manifests cannot seal against a superseded interval revision. Each subscription activation declares `(source identity, exclusive start position, optional inclusive end position)`. Updates close prior intervals and open nonoverlapping ones at captured source watermarks under expected configuration version. If source bounds cannot be acquired coherently, status stays unready; do not use worker time. A multi-source activation stores separate cut points, not a fictitious global instant. Admission of desired configuration is not proof of activation. Exact eligibility uses source interval plus immutable filter/config revision.

### ProviderHandoff, DeliveryObservation and ReplayRun

Logical delivery UID is deterministic from event/subscription UIDs, independent of provider/attempt/replay. Persist provider deployment/tenant, immutable destination reference/revision, effective profile revision, run UID, payload digest and submission key. An enumerable handoff intent precedes the remote call. Prepared -> Submitting -> Accepted / DefinitelyRejected / SubmissionUncertain is internal handoff state, separate from receiver outcome. Lost responses use provider reconciliation and bounded deduplication windows; unresolved ambiguity remains Paused with a safe reason. No automatic resubmission after an expired deduplication horizon.

Public phases remain Pending, InFlight, RetryScheduled, Paused, Succeeded, DeadLettered and Cancelled. InFlight includes provider-accepted work awaiting external outcome, without asserting a receiver attempt. Authenticated provider observations update typed status under expected-version guards with observation identity; terminal outcomes never regress due to delayed polling/callbacks. Unknown counts/times are nullable. No global HTTP permits or production GitStore send-head scheduler. Source-position provenance remains ordered; HTTP arrival is not guaranteed serial.

Replay persists a separately authorized/audited run intent before provider redelivery of the original retained message/destination. Preserve CloudEvent content/source/id, original outcome and logical delivery UID. Transport IDs follow adapter-qualified semantics. Resolve replay ambiguity without duplicate requests; cap retained runs at10/delivery and one active, with <=12 normalized latest attempt summaries/run. status.resolved.lastReplay is a nested latest-run summary, not another resource. Unconfirmed cancellation remains pending/paused. Dev queue state is process-local; only Svix qualification establishes production delivery durability.

## Query-first storage layout

New **forward migrations**, no changes to applied files or secondary indexes/materialized views. Names below are proposed tables, not deployed storage. memdb implements matching conditionals/errors for contract tests; it does not prove restart durability.

| Table/family | Partition and clustering intent | Safety/recovery authority |
| --- | --- | --- |
| events_by_series / event_names_by_namespace | `(namespaceUID,seriesUID)` control/summary/applied rows; namespace-UID/name claim. | Control+summary+marker same-partition conditional batch; persistent closed authority prevents resurrection. Special CDC adapter emits only summary transitions; internal markers advance source progress. |
| event_receipts_by_key | producer/scope/key digest; bounded immutable receipt and state. | Canonical key claim/hash binding. |
| event_intents_by_bucket | minute bucket/shard; intent ID/time and receipt identity. | Written before receipt; bounded replay/repair enumeration. |
| events_by_namespace_time / events_by_resource_time / events_by_expiry | namespace/resource UID or due bucket with bounded shard, time/UID order. | Derived index; repairable from receipt/summary intents, revalidate authority before delete/return. |
| audit_logs_by_time / audit_logs_by_uid | day/shard + time/UID; UID point projection. | Immutable evidence with recoverable index intent. Audit not in generic resource journal. |
| audit_operations_by_id / audit_recovery_by_bucket | operation ID; due bucket/shard. | Required command state and enumerable completion recovery. |
| outbox_events_by_source / outbox_events_by_uid | full source identity digest; UID projection. | Immutable source-key CAS/digest; recovery/index intent before checkpoints. |
| integration_sources_by_id | source/consumer ID. | Separate materialization cursor/epoch/gap control, version/fence guarded. No resource_watch_clock lease reuse. |
| subscription_activations_by_source / integration_routes_by_event | source/kind/scope intervals; event UID and route page. | Pinned activation/history and sealed routing manifest. |
| delivery_handoffs_by_id / delivery_handoffs_by_due | Delivery/run UID authority; bounded due bucket/shard index. | Enumerable intent before provider call; CAS/fence, stable submission key, payload/config digest and immutable acceptance receipt; ambiguous calls reconcile within provider guarantees. |
| delivery_destinations_by_revision | Namespace/subscription UID plus immutable destination revision. | Private provider/tenant/endpoint binding and effective policy version; no retargeting or cross-tenant fan-out. |
| webhook_deliveries_by_uid / delivery_attempts_by_run / deliveries_by_subscription_time | UID lookup; bounded run and time/shard list projections. | Versioned provider observations, <=12 latest attempt summaries/run, <=10 replay runs; no unknown outcome fabricated as success. |

Partitions/segments are capped: Event 10,000 markers; time indexes 64 shards with hourly/minute buckets as appropriate and <=10,000 rows/segment; routing <=64 active destinations/namespace, 32/page; handoff/list indexes <=1,000 entries/segment with bounded due scans and recoverable index intents. Every cross-table/partition write has an explicit before/after recovery cut; no assumed conditional batch across these families. Implement generated-source and backend conformance tests alongside interfaces.

See [storage-recovery.md](contracts/storage-recovery.md) for exact commit ordering and [retention.md](contracts/retention.md) for independent lifetimes.
