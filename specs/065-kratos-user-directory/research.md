# Research: Kratos User Directory

## Implementation preflight: 2026-11-07

GitHub's stable-release endpoints currently identify Kratos v26.2.0 and
Hydra v26.2.0, both published on 2026-03-20. These are the selected release
candidates selected for implementation. The owned compatibility fixture now
validates these images and reference configuration; no existing deployment or
database volume was upgraded.
Use the existing HTTP client patterns; no additional SDK is justified yet.
The self-service UI's GitHub latest release is v0.14.1, whereas the existing
Compose image is tagged v1.3.1. Registry inspection verifies the
`oryd/kratos-selfservice-ui-node:v26.2.0` multi-platform image exists, including
amd64 and arm64. Select that explicit release-aligned image for flow validation;
the older GitHub UI release tag is not the registry release catalogue.

The tagged Kratos API supports direct identity UID, external_id and bounded
exact `credentials_identifier` lookup. The latter matches credential
identifiers, not arbitrary traits: a username must actually be registered
as a password identifier, and the result must be checked against the
canonical username. No similarity-search or email-based identity merging
is approved.

The tagged identity persister proves a public pagination gap:
`ListIdentities` uses `identities.id > ?`, `ORDER BY identities.id ASC`
and `LIMIT page_size + 1`. Its handler exposes the next-page paginator.
The deprecated alternative uses OFFSET. This is not an implementation of
bounded arbitrary `last`/`before`. The user resolved the gap: retain the Relay
connection/signature, validate at query time, and return the unified diagnostic
for backward requests. Capability advertisement is deferred to #472.

The user also resolved external metadata semantics.
`shared/schemas/admin/schema.graphqls` requires
`ObjectMeta.owner: ResourceOwner!`, `resourceVersion: String!` and
`generation: Int!`. ResourceOwner means an authorization owner, not an
identity association or whoever happens to operate Kratos. Owner defaults to
the initiating Principal, with represented-username fallback when creator
metadata is unavailable. Persist actors in protected provider metadata, not a
local profile table. The accepted counter contract is best-effort Staff
author-change/integrated-write revisions and unsupported Customer -1/"-1".
Concurrent increments may be lost; no remote CAS is assumed.

The deployed two-API administration fixture also passes concurrent best-effort
writes and forward continuation against native replicas sharing PostgreSQL.
Both serving APIs must configure the same stable logical Admin URI: cursor
fingerprints intentionally prevent continuation through a differently
configured provider endpoint. `user_directory_admin_test.go` uses the shared
`directory-kratos` Docker DNS alias; it does not weaken provider-bound cursor
validation. Native lost-update, unsupported PATCH `test` and untracked Admin
write evidence still defines the concurrency contract, not a CAS guarantee.

### Supported metadata hooks and Admin PATCH

Provider-neutral profile/principal fields, bounded connection requests, metadata
candidate counters and Kratos configuration are implemented. Legacy static
profiles expose their username/ACTIVE lifecycle without being classified as
Staff; missing profiles and unsupported directory operations remain distinct.
Admin bearer credentials are optional for a private self-hosted endpoint and
are only useful when an authenticating gateway or hosted provider requires
them. No additional gateway is introduced by this feature.

The pinned Admin PUT/PATCH APIs have no expected-version parameter. PUT reads
an identity and assigns the submitted `metadata_admin`; PATCH applies supported
JSON Patch operations to a previously read in-memory identity.
Both subsequently call the identity manager. The persister sets `updated_at`
and executes a SQL update whose predicate contains only identity ID and network
ID, not a previously observed version. The owned v26.2.0 fixture additionally
proved that PATCH `test` is rejected with `unsupported operation: test`.
The adapter uses supported `add` operations after validating an uncached read;
an observed-version check is not a database condition.

Two API replicas reading counter N can both submit N+1 successfully. Ordinary
self-service writes also do not automatically advance GitStore's protected
metadata counters. `DirectoryMetadata.Advance` computes a candidate value only;
it is not evidence that these concurrent/public-flow requirements are met.
The approved replacement removes the PostgreSQL extension and uses supported
blocking registration/profile-settings hooks with parsed responses. These run
after submission and before persistence, unlike flow-initialization `before`
hooks. Registration stamps creator/updater from the canonical username in the
registration write. Settings validate immutable username/kind, merge stored
protected metadata and stamp the updater. Parsed responses replace Admin
metadata rather than merging it, so the receiver preserves unknown keys.
No follow-up registration PATCH, database customization, local profile table,
mutex or additional concurrency layer is introduced.

GitStore-owned Staff writes calculate generation/resourceVersion and persist
them through Admin PATCH; Customer counters remain -1/"-1". Missing creator
metadata is not invented on update; read ownership falls back to the represented
username. Native creation timestamps and identity IDs remain provider-owned.
T031 still includes unfinished Relay proof, not a pending metadata decision.

The user authorized Docker Desktop startup. The daemon was verified responsive;
the fixture creates ownership-labelled temporary containers/network and tmpfs
PostgreSQL storage, and removes only its own resources. Existing deployments
and volumes were not used for testing.

`USER_DIRECTORY_TEST_OWNED_DEPLOYMENT=1 go test -race -run
'^TestKratosSchemaContracts$' -count=1 -timeout=5m` passed from
`tests/integration`. The fixture runs two Kratos replicas sharing PostgreSQL,
uses the actual Go receiver and proves cross-schema credential identifiers,
persisted registration actors/Customer sentinels, immutable public usernames,
protected metadata, failed-hook persistence rejection and both password logins.
It executes actual adapter creation/PATCH and verifies persisted Staff revisions
and creator/updater encoding. It also demonstrates direct Admin username/kind/
metadata bypass and acceptance of concurrent same-revision writes.
It also verifies Hydra discovery/client setup and UI startup with the selected
images. This is a provider compatibility/metadata proof, not the complete
GitStore AuthN/AuthZ, invitation/mail, load or rolling-upgrade acceptance.

Schema-only credentiallessness is insufficient: the pinned Admin API accepts
a password on a workload identity without schema identifiers. Direct
privileged Admin access bypasses supported hooks and is outside GitStore's
credentiallessness/immutability guarantees. Keep it private.

**Documented limitations**: lost increments/last-writer-wins across replicas;
unsupported Customer counters; untracked credential/address/out-of-band writes;
privileged Admin bypass; blocking dependency failures for configured flows;
missing creator audit history; pre-persistence cache invalidation rather than a
commit notification. Callbacks are internal-only, enabled together with the
provider/private listener, and return 404 on a configured disabled-provider
listener. Standalone OIDC loads no callback configuration by default.

Kratos v26.2.0 treats a JSON-array environment value as a string for these hook
settings. Use its supported ordered `--config` files, with an opt-in
`gitstore-hooks.yml` that preserves the password-session hook. The shared
Jsonnet template excludes credentials, cookies and session tokens.

The owned selected-pin fixture also proves `credentials_identifier` with a
bounded two-result request for both password identifiers, and
`/admin/identities/by/external/{external_id}` for a UID-linked workload. The
adapter additionally checks exact canonical username equality, so an email
identifier response cannot become a username subject link. Qualified lookup,
protected profile conversion, reserved-group isolation, immutable workload UID
matching, bounded response/concurrency/deadline/cache behavior, credential
redirect rejection and operational error classification pass unit tests with
race detection. Provider factory/private-listener registration and basic
Upsert/Deactivate are implemented. HTTP/WebSocket enrichment, full lifecycle/
GraphQL administration and actor listing remain unfinished.

Evidence:
[Admin PUT handler](https://github.com/ory/kratos/blob/v26.2.0/identity/handler.go#L952-L1005),
[Admin PATCH handler](https://github.com/ory/kratos/blob/v26.2.0/identity/handler.go#L1097-L1154),
[identity update](https://github.com/ory/kratos/blob/v26.2.0/persistence/sql/identity/persister_identity.go#L1183-L1261),
[SQL update predicate](https://github.com/ory/kratos/blob/v26.2.0/persistence/sql/update/update.go#L19-L56).

Hook references:
[immutable-username discussion](https://github.com/ory/kratos/issues/2903),
[pinned parsed webhook implementation](https://github.com/ory/kratos/blob/v26.2.0/selfservice/hook/web_hook.go),
[Admin JSON Patch API](https://www.ory.com/docs/reference/openapi/patch-identity).

Sources:
[Kratos stable release](https://github.com/ory/kratos/releases/tag/v26.2.0),
[Hydra stable release](https://github.com/ory/hydra/releases/tag/v26.2.0),
[self-service UI stable release](https://github.com/ory/kratos-selfservice-ui-node/releases/tag/v0.14.1),
[tagged identity API](https://github.com/ory/kratos/blob/v26.2.0/spec/api.json),
[tagged identity handler](https://github.com/ory/kratos/blob/v26.2.0/identity/handler.go#L266-L307),
[tagged identity persister](https://github.com/ory/kratos/blob/v26.2.0/persistence/sql/identity/persister_identity.go#L976-L1089),
[tagged configuration schema](https://github.com/ory/kratos/blob/v26.2.0/embedx/config.schema.json).

## Concrete actors and Relay

**Decision**: Reuse ActorLifecycleState and extend Actor to implement Node.
Staff/Customer borrow the OpenShift User envelope; ServiceAccount remains
the existing type. Use concrete `__typename`, not UserProfileType.
Connections expose edges with cursor/node plus existing PageInfo.
Lookup uses `actor(by:)`; mutations use action-specific inputs/payloads.
No flat audit/active fields or UserFilter are introduced; #471 owns filters.

**Evidence**: GraphQL supports interface inheritance and requires concrete
objects to explicitly implement inherited interfaces. OpenShift User has
TypeMeta, ObjectMeta, fullName, identities and groups. Its groups field is
deprecated upstream; borrowing the envelope does not change GitStore's
directory-owned group administration.

**Alternatives rejected**: Flat UserProfile + type enum duplicates concrete
types, nodes-only lists omit Relay edges, subject arguments bypass existing
by/global-ID conventions, and UserFilter preempts the agreed filter language.

Sources: [GraphQL interfaces implementing interfaces](https://spec.graphql.org/September2025/#sec-Interfaces.Interfaces-Implementing-Interfaces),
[Relay connections](https://relay.dev/graphql/connections.htm),
[OpenShift User types](https://github.com/openshift/api/blob/master/user/v1/types.go).

## External identity storage and resolution

**Decision**: No GitStore user table, provider-specific link field or migration.
Use configured direct UID, provider-qualified external ID or exact supported
username mapping. The reference bridge already uses Kratos UID as subject.
Arbitrary alias administration is deferred rather than building an index.

**Evidence**: Kratos has a unique `external_id` for external linkage and an
Admin lookup at `/admin/identities/by/external/{externalID}` in the examined
API. This supports externally created ServiceAccounts. Use immutable
GitStore UID, not reusable account name, for the mirror's external_id.
The provider-neutral contract must not expose a `kratosIdentityID` DB field.

**Limit**: One external_id is not a multi-alias index. Supported configured
mapping must be bounded and collision-safe; never silently scan traits or
link by email. Verify exact endpoints/capabilities at the implementation pin.

Source: [Kratos API definition](https://github.com/ory/kratos/blob/master/spec/api.json).

## Unified listing and pagination

**Decision**: Unified actors enumerate UserDir's three identity schemas.
There is no human Scylla query or new projection. ServiceAccount mirrors are
hydrated with bounded source UID reads; the dedicated ServiceAccount list
uses existing `ListServiceAccounts`. Directory coverage is best-effort during
synchronization, not a cross-store snapshot or guaranteed complete after faults.

**Evidence**: Ory documents page_size/page_token and Link first/next navigation.
Its recommendation to remember previous tokens is not proof of arbitrary
Relay before/last pagination, per-edge cursors or tail access.
The inspected API does not supply a generic stable profile filter contract.

**Decision**: Keep Relay edges/PageInfo and standard arguments. Kratos validates
at query time and returns the unified diagnostic for unsupported backward
pagination; forward behavior remains bounded and provider-bound. No scan,
deep offset, replica-local history, total count or new search/filter surface.
Capability advertisement is deferred to #472.

Source: [Ory pagination](https://www.ory.com/docs/guides/api-rest-pagination).

## ServiceAccount shared CDC/journal and best-effort consumption

**Decision**: Enable ServiceAccount authoritative-table CDC and reuse the
resource_watch_* journal and fenced materializer backend. The configured
UserDir consumes directly through internal bounded journal reads/subscribers,
with a process-local position and a small finite retry budget. Success moves
on; retry exhaustion records a redacted failure, discards the event and moves
on. No external list/watch client or durable consumer checkpoint. It consumes the
provider-neutral source and synchronizes UID-linked external_id mirrors.
Do not build a raw Kratos CDC consumer, independent event bus, outbox or
new per-kind journal. A future ServiceAccount controller can add an external
adapter over the same source; neither is implemented in this feature.

**Rationale**: An acknowledged Scylla write also persists CDC changes,
closing the process-local publication gap at the shared producer. The journal's
durable replay capability remains available to controllers; it does not require
every internal consumer to implement durable delivery. UserDir deliberately
uses best-effort live consumption without restart replay, automatic repair
or a DLQ. Native ServiceAccount writes remain independent of active UserDir.

**Evidence**: ServiceAccount authority remains
service_accounts_by_cluster_name. Incremental migration 013 enables full
preimage/postimage CDC with 14-day TTL without changing applied migration 009.
Native Scylla contracts exercise the shared reader, incomplete enrollment,
rejected writes, retained deletion UID and name reuse. The shared
ResourceWatchJournal offers source-qualified progress and fenced leases.
Native pre-013 upgrade tests preserve existing identities and every applied
ledger checksum/write time, reject incomplete read-only startup, accept
read-only startup after upgrade, and reject rollback artifacts that omit 013.
Those leases/progress remain producer-owned; do not extend SaveProgress for
UserDir. No provider applied cursor is persisted. Start at the current stream
boundary after restart. A crash or discarded event can leave missing/stale
mirrors or orphans indefinitely; producer durability is not consumer durability
or a GitStore/Kratos transaction. GitStore UID/enablement stay authoritative.
Native credentialless mirror application passes against two selected-pin
Kratos replicas sharing PostgreSQL, with a MemDB authority. Race-enabled
consumer/runtime tests cover three attempts, a 64-event buffer, overflow,
later delivery, current-boundary startup and reload/shutdown. These are separate
native-source/native-directory proofs, not a deployed two-GitStore-API
Scylla-to-Kratos, process-crash, rolling-upgrade or sustained-load acceptance.

**Gate**: Validate full-image capture, conditional writes, enrollment and
projection fences, delete UID/name reuse, cluster-scoped internal source,
MemDB parity, bounded retry/drop/overflow behavior, continued delivery after
failure and safe duplicate handling across replicas. No automatic replay,
repair or production backfill command. Review the necessary existing-table DDL under
migration-history rules; no applied checksum rewrite is implied.

Sources: [Scylla CDC overview](https://docs.scylladb.com/manual/stable/features/cdc/cdc-intro.html),
[CDC consistency](https://docs.scylladb.com/manual/stable/features/cdc/cdc-consistency.html).

## Existing ActorLifecycleState

**Decision**: INVITED is staff setup; PENDING_VERIFICATION is unfinished human
verification; ACTIVE permits normal admission; INACTIVE is administrative
deactivation; SUSPENDED is a distinct administrative suspension.
ServiceAccounts retain ACTIVE/INACTIVE from authoritative enablement.
Deletion is absence, not another state. Add invitation resend and suspension
administration rather than substituting an active boolean.

**Evidence/limit**: Kratos native state only has active/inactive, insufficient
for all five values. Protected metadata and trusted verification/setup
facts must retain the distinctions. The examined API describes native state
as having no effect; native deactivation alone is not an adequate promise
of flow/session enforcement. Validate actual behavior against the release
pin and enforce GitStore admission/freshness explicitly.

**Alternatives rejected**: New desiredState enum or state-machine framework;
equating suspension with verification, or treating all inactive identities
as freely reactivatable without checking setup prerequisites.

## Login identifiers and mail

**Decision**: One deployment with Staff/Customer/ServiceAccount schemas;
username/password and email/password for both human types, globally unique
identifiers. Customer is the public registration default. ServiceAccount
mirrors have no human login/recovery/verification identifiers or credentials.

Use Kratos Admin API, not direct PostgreSQL writes. Privileged groups,
lifecycle and contact controls live in metadata_admin; editable human
traits and verification facts use supported provider flows.
HTTP courier delegates mail to the bounded authenticated GitStore adapter.
Delivery is at-least-once; no duplicate-free guarantee or production
notification provider ships here.

**Implemented-pin evidence**: The owned v26.2.0 fixture authenticates the same
publicly registered Customer by both native API and browser password flows.
Both browser sessions pass the bridge's actual Kratos `WhoAmI` client and
produce the same identity UID at the Hydra acceptance callback (stubbed in this
focused test). The bridge never forwards passwords: native UI/flows own
identifier handling, so no production bridge adaptation is necessary.
Public cross-schema conflicts expose neither another UID nor its Staff schema.
Forged registration schema/admin fields are ignored, and trusted Customer
admission rejects even privileged group strings with permissive AuthZ.

Native Staff invitation creation/resend uses private recovery links and actual
password settings to complete setup. Identity creation and identifier
reservation are separate writes; failed reservation attempts only bounded
cleanup of the newly created UID. Invitation delivery and lifecycle/session
revocation are also separate operations and may partially apply. Native session
deletion can return 404 when no sessions remain; only that endpoint after a
successful identity mutation treats absence as successful revocation.
Reactivation neither bypasses incomplete setup nor restores deleted sessions.

Contact resolution is a fresh subject-keyed read. A protected old verified
address survives unverified replacement; old-address bounce observations do
not suppress a newly verified primary. Initial deliverability defaults false
without a trusted observation. Consent boolean changes get internal timestamps;
bounce/timestamp-only updates do not advance generation. Owned invitation,
contact and mail acceptance passes; GraphQL administration and deployed API
rollout/scale acceptance remain separate.

Sources: [Ory identity schemas](https://www.ory.com/docs/kratos/manage-identities/identity-schema),
[identity credentials](https://www.ory.com/docs/kratos/concepts/credentials),
[mail delivery](https://www.ory.com/docs/kratos/emails-sms/sending-emails-smtp).

## Implementation-time version policy

Planning inspected the existing Kratos 1.3.1/Hydra 2.3.0 stack; those are
historical observations, not the implementation target. At implementation,
check current latest stable releases, select compatible explicit pins for
the affected reference stack and SDK, and verify configuration, login,
lifecycle, external_id, pagination and courier behavior against them.
Do not use moving latest tags or assume master/Ory Network documentation
proves a particular self-hosted release capability.

## Identifier privacy

Global uniqueness removes login ambiguity; it is not authorization.
Registration/login errors must not disclose other identity types/subjects.
Username-bearing Namespace reads remain authorized. Never merge profiles
by matching email, or log credentials/contact identifiers in metrics/audit.
