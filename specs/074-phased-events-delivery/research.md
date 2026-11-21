# Research: Phased Events, Audit and Reliable Delivery

**Date**: 2026-11-21. Repository baseline: `f8bd3bca`. All decisions below are proposed implementation contracts, not shipped capabilities. Read-only research covered the Git source, Event/audit persistence and outbox/wire boundaries independently.

## R1 — Reuse the envelope and status contracts

**Decision**: Stored records use canonical ObjectMeta; Event, OutboxEvent, EventSubscription and WebhookDelivery use the established status fields with typed `resolved`. AuditLog omits status. Immutable runtime records have real stored generation 1, actual owner/UID/timestamps, nullable observedGeneration/lastAppliedRevision where meaningless and no invented readiness. ObjectMetaInput.name remains required; recordEvent accepts a producer-chosen stable series name and rejects create-time resourceVersion. Extend the existing CatalogObjectReferenceInput additively with optional UID for captured diagnostic references. No EventMeta/EventReference.

**Rationale/source**: `shared/schemas/admin/schema.graphqls` ObjectMeta/Condition, `repository.graphqls` ObjectMetaInput, `product.graphqls` CatalogObjectReference/Input and `category.graphqls` status convention. Input currently lacks UID; output already supports it. Producer-asserted historical references are not independent evidence that a resource existed. Server-authored audit references come from trusted capture.

**Alternatives considered**: Reduced runtime metadata drifts. Resolving a deleted reference by name can capture a new incarnation. Making shared metadata.name optional for one recorder would weaken unrelated contracts.

## R2 — Exact accepted observations without cross-partition fiction

**Decision**: Bind producer-scoped idempotency key to immutable request hash and series UID in a durable receipt. Persist an enumerable recovery intent before the receipt. Claim each namespace/name for one producer/series, then apply an observation using lifecycle/control, applied marker and summary CAS in the **same table/partition**. Expiry closes persistent lifecycle while deleting summary, so absence cannot resurrect the series. Return an Event only after materialization completes. A timeout after durable preparation returns FAILED_PRECONDITION/RECORDING_PENDING and requires retry of the same key; it is not confirmed rejection. Limit series to 10,000 observations/24 hours; new observations on a closed series fail explicitly and require a new series name/key, while known retries retain their result.

**Rationale**: Separate receipt/count writes need a recoverable state machine. [Scylla LWT](https://docs.scylladb.com/manual/stable/features/lwt.html) requires a conditional batch's statements to share table and partition; conditions use the initial state. [Driver guidance](https://gocql-driver.docs.scylladb.com/stable/statements/lightweight-transactions.html) requires observing CAS results. Use bigint values computed from the loaded version, not counter columns. Existing `scylla/recovery.go` provides roll-forward patterns, not an enumerable generic queue.

**Alternatives considered**: A counter plus an independently inserted dedupe row double-counts after crashes. Acknowledging before recoverable intent can strand work. Unbounded markers in one series eventually defeat bounded storage.

## R3 — Retention means different things at each layer

**Decision**: Default live Event TTL 1 hour, operator min/max 15 minutes/24 hours, maximum series lifetime 24 hours. Operator-configured namespace-UID overrides only in v1. Expiry is an explicit guarded delete driven by a due index. Disclose protected CDC content retention of 14 days and configured journal retention (default 7 days), independently of live visibility. Reject a content-deletion policy shorter than this path can enforce; do not imply one-hour physical erasure. Short-content-retention/encrypted/minimal-source variants are future separately reviewed work.

**Rationale/source**: `internal/config/config.go` fixes CDC at 14 days and defaults journal to 168 hours; source full images and repeated series updates can outlive the visible record. Expired replay must not recreate a live Event. The summary/applied-marker table needs a deliberate source adapter: summary changes project Events, marker-only frames are internal progress through existing CDC registration/sequencing.

**Alternatives considered**: Ordinary TTL does not prove a public deletion notification. Merely omitting body from journal leaves it in full-image CDC. Adding encryption/key-erasure infrastructure without an approved privacy guarantee is unnecessary for a truthful baseline.

## R4 — Audit is trusted evidence, not diagnostics

**Decision**: Cluster-scoped operator-only AuditLog read/list/export; no public append/update/delete/watch initially. REQUIRED_PRE durability for RBAC, credential lifecycle, audit export, subscription changes and replay; ASYNC_BOUNDED for authentication/authorization denials, ordinary reads and catalog completion, with visible loss accounting and the option to strengthen coverage. Capture distinct immutable Intent/Completion/Uncertain records. Required commands need an owned recovery identity before effects; unsupported required coverage fails closed. Audit storage is initialized before protected writes become ready; health and internal append/cleanup are nonrecursive. No implicit emergency bypass.

**Rationale/source**: Existing pluggable Principal/auth action catalog and `admission.Error` retain authority. A commit and audit append are not a cross-service transaction. Existing signed Git correlation can connect intent to later verified source outcome; author text is not the authenticated actor.

**Alternatives considered**: Blanket best effort cannot certify required audit. Logging only Intent falsely implies completion. Gating health/authentication diagnosis on recursive audit availability creates a recovery deadlock.

## R5 — #139 needs a durable source, not a callback

**Decision**: A private framed/checksummed protobuf log on retained Git storage outside removable bare repositories. One per-repository mutation gate covers all native/API writers. Sync intent before publication; append verified completion and expose only committed facts. Recover unfinished intents before further writes. Replay is repository/epoch/sequence scoped; stream request uses existing signed authorization and finite authenticated reconnect. Exact protocol is in [git-source.md](contracts/git-source.md).

**Rationale/source**: `grpc/server.rs` has bounded repository marker locks and guarded file receipt helpers, including explicit partial loose-ref commit and anti-ABA handling. They do not cover ReceivePack/legacy commit/tag creation. Repository deletion retains a sibling tombstone; source history must survive separately too. `git/hooks/mod.rs` starts detached admission only after refs commit. Existing proto has no notification RPC. Quarantine promotion does not establish all power-loss sync guarantees.

**Alternatives considered**: Process-local broadcast loses accepted facts. Polling current refs loses A -> B -> A. Git reflogs/particular write receipts do not uniformly cover all ref types and writers. SQLite/broker adoption is unnecessary for one retained singleton writer. Initial certified fault model is process crash/replacement on healthy retained storage; host/media/power loss needs additional verified object/ref/log durability and cannot be silently claimed.

## R6 — Provider-owned production delivery

**Decision**: API owns source ingestion, immutable facts, routing, enumerable provider handoff intents and guarded status reconciliation. Internal DeliveryProvider follows the existing provider pattern with dev and Svix implementations. Dev uses a bounded process-local HTTP sender with explicit restart/replica limits; production delegates accepted-message queueing, signing, retries and redelivery to Svix. No controller sender or webhook worker GraphQL operations/types/actions; no SDL hiding is needed for this feature.

**Rationale/source**: `gitstore-api/internal/auth/types.go` demonstrates provider interfaces without requiring another transport. Keeping production retry queues/permits inside GitStore would recreate the distributed sender being outsourced. Durable outbox-to-provider handoff remains necessary: remote acceptance and local receipt are not atomic. Lost responses reconcile through stable submission identity within the verified provider deduplication/lookup horizon; beyond it expose uncertainty. Existing watchjournal.LeaseManager is not a generic delivery lock.

**Alternatives considered**: Controller-manager claim/prepare/complete GraphQL commands introduce execution surface and replica protocol complexity. API-owned distributed HTTP queues still carry most of that maintenance burden. Automatic provider failover can duplicate pending messages or change transport identity. A local dev queue does not certify production durability.

## R7 — Activation, identity, replay and provider status

**Decision**: Retain source-position activation barriers, deterministic fact/delivery identity and immutable routing manifests. Pin provider deployment/tenant, isolated destination revision and effective policy version on each handoff/run. Endpoint edits cannot redirect accepted work. Provider observations are authenticated/bound/idempotent; stale status cannot regress terminal outcomes. Acceptance is distinct from receiver receipt and unknown counts stay null. Persist audited replay intent before redelivery of the original retained provider message/destination, preserving CloudEvent source/id/content and original run outcome.

**Rationale**: [Svix message creation](https://www.svix.com/guides/sending/send-webhooks-with-typescript/) accepts caller payload/eventId; [official Go SDK](https://pkg.go.dev/github.com/svix/svix-webhooks/v2/go) documents conflicts for duplicate eventId. These do not establish indefinite deduplication or arbitrary endpoint isolation. Qualify service/version/account capabilities and finite horizons. Provider replay retention is required in addition to GitStore's retained original payload. Terminal GitStore status is a projection backed by evidence, not an independent queue authority. No strict receiver arrival order in the baseline.

**Alternatives considered**: Equating provider acceptance with delivery invents success. Time-based activation varies with lag. Resubmitting after an expired deduplication horizon or recreating an expired message hides uncertainty. Reopening original outcome loses provenance. Cancellation only becomes terminal after confirmation; accepted external work cannot be recalled by a local fence.

## R8 — GitStore envelope, provider transport and security

**Decision**: Preserve approved CloudEvents 1.0.2 structured JSON (specversion 1.0, application/cloudevents+json), without a Svix wrapper or transformations. Store canonical content/digest; provider submission uses the full CloudEvent payload. Standard Webhooks-compatible HMAC v1 signs actual transmitted bytes. Semantic event identity remains provider-independent; dev uses delivery UID as transport ID, while Svix transport IDs are private mapped identities. Provider encoders may serialize equivalent JSON differently; qualify stable bytes/IDs for retries/redelivery within each adapter.

**Rationale/sources**: [CloudEvents](https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/spec.md), [Standard Webhooks](https://github.com/standard-webhooks/standard-webhooks/blob/main/spec/standard-webhooks.md), [Svix verification](https://docs.svix.com/receiving/verifying-payloads/how). Svix documents branded header aliases and raw-body verification. Existing shared/secretmaterial owns authorized reference resolution; endpoint signing-key provisioning/rotation and external payload/key custody require an explicit production profile. HTTPS/egress/DNS/redirect protections must be verified at the actual sender; API URL validation alone cannot control Svix's network stack.

**Alternatives considered**: Wrapping the CloudEvent or replacing its ID leaks vendor schema into the approved client contract. Promising exact byte equality across encoders, identical retry schedules or per-attempt GitStore authorization inside Svix without adapter evidence would overstate compatibility. Versioned operator profiles replace arbitrary per-subscription retry knobs; unsupported profiles stay unready. Dev limits do not define Svix behavior. See [delivery-provider.md](contracts/delivery-provider.md).

## R9 — Sequencing, capacity and lifecycle

**Decision**: Independent Event, Audit, Git-source, Outbox and Delivery slices under existing owners. Extend existing capacity/chaos selectors, with explicitly sized fan-out, payload/retention and drain tests. Contract additions are additive behind inactive capabilities until indexes/security/recovery are ready. Normal checks must include shared schema/semantic drift cases. Update design 039; audit ADR core-decision acceptance explicitly in implementing PRs.

**Rationale**: 100 facts/s at 64 KiB is about 527 GiB/day before replication/index overhead; proposed target rates alone are not a disk budget. The capacity contract declares smaller normal payloads plus max-size probes, finite storage quotas and backpressure. ADRs 0015/0018 remain Proposed in planning; delivery fencing does not automatically implement all lifecycle hooks or field ownership.

**Alternatives considered**: One all-or-nothing initiative PR prevents independent evidence. Capacity tests that ignore retained payloads or routing fan-out certify a different workload. Blanket ADR acceptance overstates implemented scope.
