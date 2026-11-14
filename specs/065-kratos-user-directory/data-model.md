# Data Model: External Actor Directory

## Ownership and envelope

Staff and Customer identities live only in the configured UserDir (Kratos
in the reference deployment). No GitStore user/profile, subject-link or
reconciliation-intent table is introduced. The existing authoritative
ServiceAccount table gains CDC using the shared controller-facing journal.
Application profiles remain a later feature.

Expose concrete `Staff`, `Customer` and the existing `ServiceAccount`, all
implementing `Actor` and `Node`. Staff/Customer also implement the human-only
`User` interface, sharing the OpenShift User envelope and contact fields.
ServiceAccount remains outside User. Reuse the existing `ActorLifecycle` / `ActorLifecycleState`.

| Field | Ownership and invariant |
|---|---|
| `id` | Existing kind + immutable UID Relay encoding; ServiceAccount uses GitStore UID |
| `apiVersion`, `kind` | GitStore API envelope; concrete kind is Staff, Customer or ServiceAccount |
| `metadata.name` | Globally unique canonical human username; ServiceAccount name comes from GitStore |
| `metadata.uid` | Human directory UID; GitStore ServiceAccount UID, not mirror UID |
| `metadata.creationTimestamp` | Provider creation time for humans; existing metadata for ServiceAccounts |
| `metadata.owner` | Defaults to the initiating Principal using the existing ResourceOwner convention |
| `metadata.generation` | Best-effort author-change counter for GitStore-managed Staff; -1 for unsupported Customer counters |
| `metadata.resourceVersion` | Best-effort integrated-write revision for Staff; "-1" for unsupported Customer counters, never remote CAS |
| `fullName` | Human-editable display name on Staff/Customer |
| `identities` | Provider-owned identity associations on Staff/Customer; no local alias entity |
| `groups` | Human profile memberships; lazy source-UID-linked directory memberships for ServiceAccount. Reject empty/reserved administration inputs; no mirror or capability returns `[]`, not invented groups |
| `lifecycle.state` | Existing enum; human state from provider; workload enablement from GitStore |
| `primaryEmail`, `emailVerified` | Human login/contact address and provider verification |
| `emailDeliverable` | Administrator/system-controlled human contact fact |
| `contactConsent` | Transactional/marketing consent; administrator-controlled in this feature |

No flat `createdAt`, `active`, profile-type discriminator, subject or
GraphQL auditing metadata is added. Provider-neutral internal subject and
trusted profile classification remain necessary for AuthN/AuthZ; they are
not a new persisted GitStore model. Store external owner/generation/version
facts in protected provider metadata using `creation_actor` and `update_actor`;
retain source metadata for ServiceAccounts. Staff counters are best-effort
and can lose increments under concurrent writes. They do not track every
out-of-band credential/address change. Customer counters are unsupported.
Use blocking internal registration/settings hooks and Admin JSON Patch rather
than customizing Kratos's database tables. Direct Admin writes bypass hooks.

Keep standard Relay arguments. Kratos provides bounded forward pagination;
unsupported last/before requests return the unified diagnostic at query time,
not a scan/offset fallback. Capability discovery is deferred to #472.

### Schema-specific invariants

- Staff and Customer schemas use username and email as password identifiers.
  Both identifiers are globally unique across human schemas.
- Public registration selects Customer only. Staff is created through
  authorized Admin operations; ServiceAccount mirrors react to GitStore
  events. Users cannot edit schema assignment, username, groups or lifecycle.
- GitStore-created ServiceAccount mirrors have no human identifiers, password
  credentials, verification/recovery addresses or browser login. The schema
  does not prevent privileged Admin callers from creating credentials; those
  writes are outside the supported guarantee.

## Provider-owned resolution

AuthN-provider configuration selects an explicit bounded mapping: direct
directory UID, provider-qualified external identifier, or an exact supported
username lookup. The reference OIDC bridge already uses the Kratos identity
UID as its subject. Do not infer a link from matching email/display name or
search all identities. Never treat identical subject strings from different
issuers/providers as equivalent without configured mapping.

Kratos `external_id` is a unique provider-owned field for external linkage.
For a ServiceAccount mirror, derive it from the immutable GitStore UID with
a ServiceAccount-specific prefix. Keep canonical
`system:serviceaccount:<name>` and source resourceVersion in protected
provider metadata. Deleting/recreating a name must produce a different
external identifier and must not inherit old groups or identities.

One `external_id` is not an arbitrary multi-alias index. Multiple AuthN
paths may use the same direct UID or explicitly configured supported
mapping; arbitrary many-to-one alias administration is out of scope.
Unsupported mappings are rejected, not emulated with a local link table.

## Groups and principal enrichment

Directory memberships live in Kratos `metadata_admin` and are managed via
Admin GraphQL. Ordinary groups become `system:group:<name>`; customers
receive only the configured customer set.

AuthN synthesizes `system:authenticated`. Successful trusted UserDir
resolution synthesizes exactly one of `system:staff`, `system:customers`,
`system:serviceaccounts`. Neither token claims nor editable group metadata
may supply these groups. Missing profiles and group-incapable providers do
not invent directory groups.

Namespace authorization remains RBAC-driven: the shipped
`namespace-creator` binds to `system:staff` and uses `when` predicates for
USER tier and AuthN-selected subject matching. The directory's identity UID
and username are enrichment facts, not replacements for the authenticated
subject used by namespace ownership or User bindings.

## Actor lifecycle

These are mappings to the existing enum, not a new state-machine framework:

| Existing state | Human meaning and operations | ServiceAccount |
|---|---|---|
| `INVITED` | Staff created, setup invitation outstanding; resend invitation is allowed | Not applicable |
| `PENDING_VERIFICATION` | Human registration/setup awaits required verification | Not applicable |
| `ACTIVE` | Required setup/verification complete; normal admission may proceed | GitStore account enabled |
| `INACTIVE` | Explicit administrative deactivation; reactivate subject to setup prerequisites | GitStore account disabled |
| `SUSPENDED` | Administrative suspension; explicit reactivation required | Not introduced for workloads |

Staff setup progresses from INVITED through required verification to ACTIVE;
Customer registration starts PENDING_VERIFICATION and becomes ACTIVE through
the verified public flow. Deactivate/suspend do not erase unfinished setup.
Reactivation restores eligibility only after required setup/verification.
Deletion removes the identity; it is not another enum state. Email changes
preserve the current deliverable address until the replacement is verified.

Kratos's native active/inactive field cannot represent all five states.
Preserve invitation/suspension distinctions in protected provider metadata
and derive verification state from trusted flow/address facts. Prove this
mapping and session revocation with the implementation-time pin. Do not
assume setting native inactive alone disables every Kratos flow.
GitStore enrichment must deny non-ACTIVE humans within the freshness bound;
this does not change the policy-driven Namespace authorization model.

## ServiceAccount shared journal events and best-effort consumption

Source writes use the existing ServiceAccount store and concurrency contract.
Full-image CDC on service_accounts_by_cluster_name feeds the same fenced
resource_watch_* journal backend used by controllers.
Gate enrollment visibility, retain UID on deletion, and expose provider-neutral
resource events with the existing type/resourceVersion envelope. No private
key material or Kratos-specific payload is included.
ServiceAccount deletion payloads retain only `{"UID": "<source UID>"}` through
the existing journal payload column. Other kinds keep payload-free deletions.
The source-qualified shared runner handles cluster scope and the authoritative
enrollment marker, not a separate ServiceAccount CDC runner.

The configured profile-capable UserDir consumes the shared stream directly
through bounded internal journal reads/subscribers, not raw CDC, GraphQL
watchResources, external list/watch or an independent in-process bus.
MemDB provides the equivalent internal journal contract. A future
ServiceAccount controller can use an external adapter over this same source
without changing the producer; neither adapter nor controller is scope.

Existing CDC publication progress/leases belong to the shared producer.
UserDir adds no persistent applied position or consumer lease. Start at the
current stream boundary and keep any read position/retries process-local.
Attempt each event with a small finite retry budget and bounded backoff;
on success continue, on exhaustion record a redacted failure, discard and
continue. Bound buffers and overflow handling. No DLQ, outbox, SaveProgress
extension, filesystem checkpoint or new operator volume is introduced.
The implemented buffer holds 64 events; application makes three attempts with
100/200 ms backoff under the configured directory deadline. Epoch/retention
discontinuities discard the gap and restart at the current boundary. Runtime
reload/shutdown cancels the old consumer and rebinds local invalidation callbacks.

The consumer has no durability guarantee: a crash, outage or dropped event
can leave a missing/stale mirror or orphan indefinitely. No replay, automatic
catch-up, retention-expiry repair or source/provider inventory sweep is required.
There is no backfill CLI or assumed pre-existing production population.

Duplicate, stale and out-of-order events must be harmless: re-read current
source state, compare UID/revision, preserve directory-owned groups, and
never grant access to a deleted/replaced UID. Validate competing consumer
writes and name reuse across replicas; do not invent remote CAS support.
Bound pages, concurrent requests, delivery buffers and retry/backoff. No
persisted intents, per-kind journal, new broker or application tables are
introduced. Preserve shared producer leases/progress. CDC creates its managed
log table; enabling it is a reviewed schema change, not permission to edit
applied migration history.

Synchronization is best-effort, not guaranteed eventual convergence.
Measure event latency, retry/drop outcomes and bounded resource use at the
declared scale. GitStore remains authoritative regardless of mirror freshness.

## Notification mail

Kratos courier sends authenticated, bounded requests to the GitStore mail
adapter, which forwards to the configured development provider. Log outcome
and correlation ID, never recipient, body or verification/recovery secrets.
Delivery is at-least-once, not exactly-once.
