# Implementation Plan: Production User Directory Provider (Kratos)

**Branch**: `065-kratos-user-directory` | **Updated**: 2026-11-09
**Spec**: [spec.md](spec.md)

## Summary

Implement Kratos behind the existing provider-neutral UserDir boundary.
Keep human identity storage external. Expose OpenShift-style Staff/Customer
envelopes and reuse ServiceAccount; all implement Actor and Node. Staff and
Customer also share the human-only User interface; ServiceAccount exposes
lazy directory groups without implementing User. Use the existing
ActorLifecycleState and Relay lookup/edge conventions.

GitStore retains ServiceAccount authority. ServiceAccount CDC feeds the
existing shared resource watch journal used by controllers. The configured
UserDir reads provider-neutral changes directly through the internal journal
boundary with bounded retry-and-discard; no self-GraphQL client, parallel process-local bus or
Kratos-specific CDC pipeline.
No subject-link table, outbox or broker is planned. Enabling CDC requires
an existing-table schema change with reviewed migration-history handling.
Synchronization is best-effort, not a GitStore/Kratos atomic transaction or
guaranteed eventual convergence. UserDir adds no durable consumer checkpoint.

Metadata enforcement uses supported blocking internal Kratos hooks and Admin
JSON Patch, not PostgreSQL triggers or an upstream-table extension. Staff
revisions are best-effort; Customer counters are unsupported (-1/"-1").
Use protected creation_actor/update_actor facts with a canonical-username
ownership fallback. Concurrent PATCH can overwrite changes/lose increments;
out-of-band Admin and unintegrated credential/address writes are not tracked.
Hook delivery is configured only with the provider enabled; disabled routes
return 404 and blocking-hook failure prevents protected self-service writes.

## Technical Context

**Language/Dependencies**: Use repository-pinned Go, gqlgen and existing
auth, datastore, observability and resolver packages. When updating the
reference stack, select and explicitly pin the latest stable compatible
Kratos, Hydra, client SDK and affected images at implementation time.
Validate their configuration, flows and API against those exact pins;
the versions observed during planning are not a release target.

**Storage**: Kratos-owned PostgreSQL for human identities and workload
mirrors. Existing GitStore ServiceAccount records remain unchanged in
ownership. No GitStore human-profile or alias database or intent table.
Enable full-image CDC on the authoritative ServiceAccount table only;
reuse resource_watch_* tables and source-qualified progress/leases.
The Scylla-managed CDC log is not a new application identity table.
Provider-local metadata holds external linkage and human lifecycle details.

**Scope**: UserDir enrichment on Admin/Git HTTP paths, concrete actor
lookup/listing and human/group/lifecycle administration, ServiceAccount Node
and connection support, reference customer self-service and HTTP mail.
Defer filters/search syntax to #471, application profiles to #28, and
production notification providers to their own feature.

**Topology**: At least two API replicas; bounded per-replica caches;
concurrent event subscribers are idempotent and tested during rolling
replacement. Kratos is the external shared identity authority. Git service
stays stateful and singleton-only, unchanged.

**Performance**: Target 1,000 staff, 1,000,000 customers, 1,000 ServiceAccounts;
500 authenticated requests/s per API replica. Resolution overhead at most
10 ms p99 warm and 100 ms p99 cold, with fewer than 50 directory calls/s per
replica. Cache/group/deactivation freshness at most five minutes.
Authentication recovery for existing profiles is tested within 30 seconds of
dependency readiness. Missed or discarded ServiceAccount events may leave
absent/stale mirrors or orphans; no automatic repair guarantee is planned.

**Bounds**: Two-second outbound deadline, bounded cache entries/concurrency,
no unbounded request queue. Bound page sizes, mirror hydration, event
delivery buffers, retry attempts/backoff and mail payload/concurrency.

## Design and contracts

### Authentication and provider-owned mapping

AuthN establishes the subject and synthesizes `system:authenticated`.
UserDir resolves a configured direct UID, provider-qualified external ID
or exact supported username mapping. No local subject-link persistence,
email merge or whole-directory lookup. Unsupported mapping capabilities
must fail explicitly. UserDir owns trusted profile classification and
groups, synthesizing staff/customer/ServiceAccount system groups.

Reference OIDC uses the Kratos identity UID already carried by the bridge.
ServiceAccount mirrors use Kratos `external_id` derived from immutable
GitStore UID; canonical name/subject and source revision are protected
provider metadata. Name reuse must not inherit the old profile or grants.

Namespace creation stays in existing RBAC, with `system:staff` binding and
`when` tier/username conditions. Existing Admin/Storefront admission remains;
no parallel hard-coded Namespace guard is added.

### GraphQL

`Actor implements Node`; Staff, Customer and ServiceAccount explicitly
implement both. `User implements Actor & Node` shares human fields, and Staff
and Customer explicitly implement all three interfaces. ServiceAccount groups
use bounded source-UID directory lookup only when selected. Add ServiceAccount global-ID lookup through the existing
node registry/resolver/authorizer. Human envelopes borrow OpenShift User's
metadata/fullName/identities/groups shape. No UserProfileType, flat active/
createdAt fields or new auditing types.

Use `actor(by: ActorBy!)`, Relay edges/PageInfo, and existing pagination
arguments. Unified `actors` delegates enumeration to UserDir; it does not
join separate human Scylla tables. Hydrate workload mirrors by bounded
authoritative UID lookup. A dedicated ServiceAccount connection reuses
`ListServiceAccounts` and works without UserDir. Public filters are deferred.

All mutations use action-specific input and payload types. Staff invitations,
verification, suspension, deactivation and reactivation map to the existing
ActorLifecycleState; no new state-machine framework. Native Kratos active/
inactive is insufficient for all five states, so protected metadata and
verified provider-flow facts supply the remaining distinctions.

See `contracts/admin-graphql.md` for the schema and authorization matrix.
Preserve existing ServiceAccount mutations and source-owned lifecycle.

### ServiceAccount synchronization

Enable full preimage/postimage CDC on
`service_accounts_by_cluster_name`, the existing authoritative table.
Reuse the shared backend: fenced CDC materialization into
`ResourceWatchJournal`, resource_watch_* persistence, source-qualified
progress and bounded journal reads/subscribers.
Adapt cluster-scoped ServiceAccount source semantics using existing
cluster-scoped patterns; do not blindly register it as a namespaced kind.
MemDB must implement equivalent internal journal behavior.

Publish provider-neutral ServiceAccount changes, independent of whether
Kratos or another UserDir is active. Gate creation on enrollment_complete
and projection visibility; preserve deletion UID from the preimage.
The configured profile-capable UserDir consumes this stream with bounded,
idempotent handlers using external_id. No direct Kratos CDC reader or
separate ServiceAccount journal/event bus is introduced.

The internal consumer uses the existing journal-backed subscriber or bounded
ReadAfter batches with a process-local position, not GraphQL watchResources,
WebSocket transport or the external controller list/watch loop. It subscribes
from the current stream boundary at startup, without catch-up of missed history.
Apply each event with a small finite retry budget, bounded backoff and request
deadlines. On success move on; after retry exhaustion record a redacted failure
and discard the event, then continue. Bound buffers and overflow handling so a
failed/slow provider cannot block source writes or grow an unbounded queue.

No provider applied cursor is persisted, no SaveProgress extension or
consumer-fencing scheme is added, and no checkpoint directory, volume, DLQ,
outbox or controller-manager runtime is required. Existing CDC/materializer
leases and publication progress remain unchanged in purpose: they support
the shared producer, not durable UserDir delivery. A future ServiceAccount
controller can use an external adapter over the same journal; that adapter,
its list/watch bootstrap and the controller itself are deferred.

**Known limitation**: The consumer has no durability guarantee. A crash can lose in-flight work;
outages, overflow and exhausted retries can leave missing/stale mirrors or
orphans indefinitely. Shared journal retention does not imply required UserDir
replay. No automatic catch-up, expiry recovery, inventory sweep or
reconciliation loop is introduced. GitStore UID/enablement remain authoritative,
so a stale mirror must not restore a disabled, deleted or replaced account.
No cross-system atomicity, at-least-once consumer delivery, exactly-once
delivery or eventual convergence is claimed.
Document missing/stale/orphaned mirrors and incomplete directory-backed actor
lists in the operator configuration guidance; do not imply automatic recovery.

Validate conditional writes, partial enrollment, deletion/recreation,
failed provider writes and internal source scoping. Enabling CDC is
an existing-table schema change; this does not approve rewriting applied
migration checksums or silently creating a new event persistence subsystem.

No operator sync/backfill CLI or assumed pre-existing production population
is in scope. Restart resumes live consumption, not guaranteed missed-work recovery.

### Mail and reference-stack versions

Use the pinned Kratos HTTP courier through an authenticated, bounded API
adapter and development notification provider. Verify schema/flow/courier
compatibility after implementation-time pin updates. At-least-once mail
delivery, redacted logs and internal-only administration remain required.

## Constitution Check

Planning commitments are not evidence of implemented guarantees.

| Principle | Design assessment |
|---|---|
| Test-First / API-First | Contracts precede code; add focused provider, schema, event and lifecycle failures first |
| Clear Contracts / Independent Delivery | Document Actor/Node and lifecycle rollout, pin compatibility and event versions; test old/new API overlap |
| Replica Safety | Idempotent duplicate handling and UID/revision races across replicas; missed events remain best-effort |
| Multi-User Security | Trusted directory groups/type, policy-driven Namespace checks, per-kind actor authorization and redaction |
| Bounded Work / Capacity | Bound enumeration/hydration, event buffers and retry budgets; prove pagination and delivery costs at scale |
| Observability | Directory latency/cache/errors, event retry/drop outcomes and mail saturation; no identifier labels |
| Simplicity | Reuse provider boundary, ServiceAccount store, shared journal/progress and Relay helpers; no self-GraphQL client, checkpoint volume, domain tables or broker |

### Implementation gates

1. Verify Relay forward edges/PageInfo and standard pagination arguments
   against the selected pin. The provider returns the unified diagnostic
   at query time for unsupported last/before. No scans/offsets or local
   cursor history; capability advertisement is deferred to #472.
2. Implement external ObjectMeta owner defaulting to the initiating Principal,
   with represented-username fallback when creator metadata is missing.
   Staff author-change/integrated-write counters are best-effort; Customer
   counters are -1/"-1". Prove supported hooks/PATCH and document concurrent
   lost updates, untracked writes and privileged Admin bypass, not remote CAS.
3. Verify ServiceAccount full-image CDC, enrollment/projection fences,
   cluster-scoped internal journal registration and schema-change handling.
4. Verify finite retries, discard/overflow behavior, live restart, MemDB parity
   and UID races across replicas. Prove dropped events do not block subsequent
   work or bypass source authority; no checkpoint/replay/repair guarantee.
5. Verify all ActorLifecycleState mappings, invitation/verification flows,
   session revocation and fail-closed non-ACTIVE admission at the chosen pin.

These gates replace the previous unsupported blanket "Pass" claims.
No readiness claim is made until they are resolved and tested.

### Implementation preflight: 2026-11-07

The existing Actor lifecycle and auth baseline passed
`go test ./internal/admin/graph/... ./internal/auth/...` from `gitstore-api`.
The existing `none`/static UserDir contracts and canonical ServiceAccount
identity remain the baseline; no lifecycle rename was repeated.

The Kratos/Hydra/reference UI v26.2.0 pins passed owned compatibility
smoke checks. Tagged provider source confirms forward
ascending identity pagination, not bounded arbitrary `last`/`before`.
The user resolved external metadata to initiating-Principal ownership,
best-effort Staff revisions and unsupported Customer counters, and pagination
to provider query-time backward diagnostics. See research.md for evidence.
Full forward connection, courier and lifecycle acceptance remains unfinished.
Reference image pins changed without upgrading a deployment. No
GitStore user database or consumer persistence has been introduced.

Foundation profile/principal contracts and bounded Kratos configuration now
compile with the existing providers. AuthN normalization discards supplied
username/profile-kind facts; static directory profiles do not manufacture
Staff classification. Admin credentials remain optional for self-hosted
Kratos's private endpoint; no authenticating gateway is added.

The approved replacement uses blocking parsed registration/profile-settings
hooks and Admin JSON Patch, not PostgreSQL triggers. Actor metadata is stamped
before public-flow persistence; settings reject username/schema changes.
The owned two-Kratos fixture exercises the actual Go receiver, persisted
registration actors/Customer sentinels, immutable public usernames, blocking
receiver failure, GitStore Admin PATCH counter/actor persistence, and direct
Admin bypass/concurrent same-revision writes. v26.2.0 rejects PATCH `test`;
the supported adapter validates its read but does not claim database CAS.
No GitStore user table, API-local locking, timestamp substitute or unrelated
database upgrade is added. See research.md for the write trace and limits.
Docker Desktop startup was authorized; test-owned resources were cleaned up.
The reference schema/metadata implementation is not a claim that the remaining
GitStore admission, invitation/mail or load acceptance is complete.

The bounded Kratos lookup adapter and profile cache now pass race-enabled
tests for qualified mappings, protected profile/group/contact facts, error
classification, response/deadline/concurrency bounds and immutable workload UID
matching. Provider-neutral enrichment tests distinguish unlinked, unavailable
and group-incapable providers and bypass anonymous requests. The owned fixture
proves exact identifier and external-ID lookup on the selected pin. The factory
registers the adapter and its optional private listener, with disabled routes
returning 404. Admin/Git HTTP, fresh GraphQL operations and WebSocket initialization
now use provider-neutral enrichment before authorization. Static directory-only
construction requires neither static AuthN nor a JWT signing key and refuses
authentication/session issuance. The sample Staff namespace policy now uses
`subjectUsername` and USER tier through the existing evaluator.
WebSocket deadlines honor both the remaining cached profile freshness and
shorter native token expiry, with leases below five minutes. Generation-safe
local mutation invalidation evicts cache entries and closes actual authenticated
WebSocket transports, including workloads; in-flight pre-invalidation lookups
cannot repopulate the cache or admit a stale connection. Fresh connections
remain admissible. Mixed-version/two-deployed-API acceptance remains pending.
Basic provider Upsert/Deactivate and bounded native forward Actor pages are
implemented; complete lifecycle/GraphQL administration remains unfinished.

Validation: `go test -race ./internal/auth/... ./internal/config/...
./internal/admin/graph/...`, focused auth/config `go vet`, integration-module
`go vet`, the owned schema/metadata/mapping fixture with race detection,
Compose configuration and license checks passed.
The continuation passed `go test -race ./internal/middleware/security
./internal/app ./internal/auth/...` and matching Go vet.
On explicit request to fix the validation blockers, `converters_test.go` and
`namespace_capacity_test.go` were formatted without behavior changes. The
ignored local API environment's obsolete `GITSTORE_API__PORT` was replaced
with named Admin/Storefront ports, preserving Admin 4000 and Storefront 4001.
`make lint` and `make check TARGET=all` now pass, including configuration,
Compose, license, credential-leakage and authorization-catalog checks.
No credentials were changed and validation was not bypassed.

### Directory, pagination and mail continuation

The owned exact-pin fixture proves every-edge UUID keyset continuation and
static/OIDC Staff policy equivalence with two independent directory caches.
This is provider/AuthN/RBAC acceptance in one test process, not two deployed
API replicas or mixed-version replacement. Unit tests prove local transport
invalidation, native Link PageInfo/empty terminal pages and SDK saturation with
no second provider request or identity labels.

The opt-in development mail adapter is authenticated before parsing, bounded
by the smaller Admin/mail body limit and finite in-flight/deadline gates, and
returns success only after SMTP acceptance. The selected catcher requires
implicit SMTPS, not plaintext SMTP; certificate verification bypass is explicit
and development-only. Native courier Jsonnet uses a top-level `function(ctx)`,
not the callback external variable. The nested native auth environment binding
was verified against v26.2.0. Native code verification, real catcher receipt,
duplicate HTTP delivery and catcher outage passed the owned integration test.
The API route is absent when disabled and never mounted on Storefront.
SDK and captured-log tests verify finite labels and no private message fields.

`go test -race` and Go vet passed for notification, config, app, directory and
WebSocket registry packages. The combined owned schema/courier integration
runner and `make lint` passed. Native Kratos courier failure logs can contain
code-bearing subjects; this is a separate upstream privacy limitation, now
documented. Test failure diagnostics discard those sensitive native fields.
Native contact replacement/bounce/consent, invitation creation/resend, actual
password setup and session revocation/reactivation now pass the owned selected
pin. Customer dual native API/browser identifiers resolve the same UID through
the bridge's real Kratos session client with a stubbed Hydra acceptance callback;
public conflicts do not reveal another UID/schema. Forged privileged groups
cannot bypass trusted Customer admission, including permissive AuthZ.
The bridge requires no password forwarding change. These receipts do not
establish GraphQL human administration, two deployed API replicas, full deployed
rollout or scale/fault acceptance. Partial-operation, deliverability and
concurrency limitations are documented in the configuration/API references.

## Project Structure

### Deployed administration and source acceptance

Current-source two-API acceptance now passes against native v26.2.0 Kratos
replicas sharing PostgreSQL, controlled OIDC discovery/JWKS, the development
catcher and one unaffected Git service. It covers all human lifecycle states,
invitation/resend/native password setup, safe contact changes, unauthorized
mutations, mandatory Customer memberships, protected groups, concurrent
best-effort PATCH, provider-bound forward cursors across API processes,
redacted audit receipts, authenticated WebSocket expiry, physical API restart
and directory pause/restoration within 30 seconds. A stable shared Admin URI
is required for cross-process continuation; separate endpoint fingerprints
intentionally reject each other's cursors.

The shared-Scylla variant passes native CDC-to-Kratos delivery through both
deployed API consumers. It proves credentialless UID-linked mirrors, real
source-issued 60-second credentials and group authorization, finite exhaustion
receipts on both consumers, physical consumer crash/restart at the current
boundary, stale-mirror source denial, later event delivery, deletion and name
reuse without group transfer. It reuses canonical Scylla Compose services
under bounded, explicitly owned overrides. Fresh journal initialization no
longer fails API construction: bounded/cancellable polling captures the current
boundary when the producer initializes. The provider receives the live API
logger instead of relying on an unconfigured global logger.

These fixtures do not prove mixed-version overlap, provider switching, native
Hydra token exchange or a full-capacity workload. The identity restart profile
and runner ownership/restoration guards now pass isolated dispatcher
regressions, including confirmation, ownership, paused/non-identity rejection,
failed-injector restoration and replacement-ID refusal. Those mocked runner
checks are not evidence of physical Pumba injection or authentication recovery
during sustained load. T076/T079 now pass the separate native capacity
acceptance below; T080's sustained fault acceptance remains open.

The existing API readiness profile is a non-mutating smoke and is exempt from
the retained Product dataset preflight; a missing five-million-Product baseline
does not itself block that smoke. The retained Product/Category capacity asset
has not been identified with usable ownership/restoration receipts here.
Preserve the existing topology, dataset, memory and sustained-duration gates
where applicable. Alpha/production source attribution also requires a clean
checkout. Commit/push and declared-scale runs are now explicitly authorized;
the tested source must be committed before those acceptance receipts are
produced. Functional fixtures cannot substitute for the separate capacity and
sustained fault acceptance.

Generation, the full default suite, lint, aggregate configuration/catalog
checks, owned native directory race tests, integration Go vet, whitespace and
consumer-reference scans pass. The AST graph is refreshed; its existing
HCL/Terraform extraction dependency remains unavailable. No fixture-owned
containers, volumes, networks or images remain. The spec stays open and ADR
0020 stays Proposed because its issuer-client/assertion/token-hook core is not
implemented.

### Native declared-envelope capacity acceptance: 2026-11-08

T076/T079 pass against clean committed source
`43e2dc5d743eaf3bcf5d4c69ebe0c9cef7058b0f`, using the existing
`make capacity TARGET=api PROFILE=readiness MODE=alpha` interface separately
for both datasets. The owned fixture verifies actual native PostgreSQL human
counts and authoritative Scylla ServiceAccount counts, rather than treating
seed requests or import HTTP success as dataset proof.

| Dataset | Staff | Customers | ServiceAccounts | Principal requests, API 0 / API 1 | Mean directory calls/s, API 0 / API 1 |
|---|---:|---:|---:|---|---|
| Alpha | 1,000 | 100,000 | 100 | 900,001 / 900,000 | 0.440 / 0.417 |
| Declared production-sized | 1,000 | 1,000,000 | 1,000 | 900,000 / 900,000 | 0.442 / 0.436 |

Each workload offers 500 principal requests/s per API for a complete thirty
minutes, with zero dropped iterations and zero decision/membership check
failures. The additional authorized operator membership probes bring total
HTTP requests to 2,700,001 and 2,700,000 respectively. Every request has matching
grant/denial and group receipts. Both API consumers apply 87 live source events
per dataset; the companion verifies denial by source authority with unexpired
credentials, later delivery preserving exact administered groups, unchanged
singleton Git process and preserved shared PostgreSQL storage.

SDK histogram deltas place successful complete warm profile lookup p99 at or
below 0.1 ms for both replicas/datasets. Cold lookup p99 bucket upper bounds are
10/25 ms for alpha and 10/10 ms for the production-sized dataset. These are
histogram bucket bounds for directory lookup, not end-to-end request timings.
Observed cache occupancy samples remain within the 16-entry bucket against
the configured 256-entry ceiling; buffer observations remain within the
configured 64-entry bound. The call rates above are whole-run means, not
one-second peak-rate measurements.

Docker Desktop reports aarch64, eight logical CPUs and 16,787,881,984 bytes of
memory. Runtime preflight checks two actual release API processes, immutable
image references, exact revision/instance identity and bounded owned
dependencies. PostgreSQL has 1 GiB/two CPUs, Scylla 2 GiB/one CPU, each Kratos
replica 512 MiB/one CPU and singleton Git 384 MiB/one CPU. Sustained datastore
resource verification passes with no OOM kill or unexpected restart.

The six-case repeated working set covers allowed/denied Staff, denied
Customers at both ends of the seeded dataset and a real source-issued
ServiceAccount. This proves the declared directory population with a bounded
representative hot working set, not uniform access to every seeded identity.
Both runs select `MODE=alpha`; the second proves the larger identity envelope,
not unrelated production readiness or a five-million-Product baseline.

The first full-duration attempt exposed the native fixture's default
50-request/s IP admission ceiling and a K6 exported-summary shape mismatch.
The corrected fixture explicitly bounds IP/principal admission for principal
traffic plus operator probes and preallocates sufficient VUs; normal defaults
are unchanged. The verifier supports both actual export and callback metric
shapes without relaxing correctness, throughput or latency gates.

These are healthy sustained workloads with live changes, not confirmed chaos,
API replacement or mixed-version/provider-switch acceptance. T012/T078/
T080/T081/T083 remain open. CI and CI Integration pass on the tested source.
The spec remains open and ADR 0020 remains Proposed.

### Native conditional-write ambiguity acceptance: 2026-11-09

T051 passes through `TestOwnedServiceAccountCDCWriteAcceptance`, reusing the
owned native deployment, canonical bounded Scylla Compose services and an
isolated test keyspace. The reader test uses a protocol-v4 TCP proxy that
forwards setup/prepared traffic but physically discards the server's LWT
result. The caller times out without an acknowledgement; an independent
authoritative read proves that the disable/revision change committed.
The real CDC reader publishes its complete postimage through the shared
journal. Retrying the original conditional write returns not-applied and
produces no extra transition. Failed creates/stale revisions and incomplete
enrollment remain unpublished; delete retains the original UID, and name
reuse publishes the replacement UID. The existing producer lease/materializer
path is reused, not a UserDir checkpoint.

The test first failed at the expected missing-error assertion with the CAS
response forwarded, then passed with actual response loss enabled. The native
pre-013 upgrade/history test passes in the same owned run, preserving data,
checksums and ledger write times and rejecting a rollback bundle missing the
forward migration. Integration and tagged datastore Go vet pass. This is
native acknowledgement-loss evidence, not a fabricated client error.

```text
gitstore-api/
  internal/auth/                    # provider-neutral enrichment and contracts
  internal/auth/provider/kratos/    # adapter, bounded best-effort event subscriber
  internal/datastore/               # ServiceAccount CDC/shared journal integration and MemDB parity
  internal/watchjournal/            # reuse existing materializer/subscriber; no second event bus
  internal/auth/provider/rbaclocal/ # trusted username/tier predicate support
  internal/app/                     # internal provider consumer lifecycle and mail wiring
  internal/config/                  # bounded provider/cache/mail settings
  internal/admin/graph/             # concrete actors, Relay lookup/mutations
shared/schemas/admin/               # Actor/Node and human envelopes
config/oidc/kratos/                  # schemas and implementation-time pins
tests/integration/                  # flows, two replicas and retry/drop faults
tests/capacity/                     # extend existing api/readiness profile
tests/chaos/                        # extend owned identity fault interface
docs/                              # configuration and developer guidance
```

Use existing packages/helpers first; the tree describes responsibilities,
not permission to add duplicate infrastructure. The sole planned datastore
schema change is enabling ServiceAccount CDC; no domain tables are added.

## Validation

- Generate schema code and run existing API schema/resolver/auth tests.
  Validate interface inheritance, concrete Node IDs, `actor(by:)`, payload
  conventions, forward pagination and unified backward-request diagnostics,
  including empty/error cases.
- Test every lifecycle state, unfinished setup, suspended valid tokens,
  failed verification and email replacement without contact leakage.
- Verify each AuthN/UserDir/AuthZ combination, customer isolation, typed
  actor reads/list grants and cursor behavior across replicas/provider switch.
- Exercise sustained CDC/journal/provider consumption alongside authentication;
  fail provider writes, exhaust retries, overflow buffers and crash during
  application. Verify subsequent events continue, startup does not replay missed
  work, duplicates are safe, MemDB has parity and no consumer checkpoint exists.
- Extend `make capacity TARGET=api PROFILE=readiness MODE=alpha` with the
  seeded identity workload and per-request correctness receipts. Distinguish
  the 100,000-customer routine fixture from full 1,000,000-customer scale proof.
- Use existing `make chaos` with explicitly owned containers; verify
  authentication recovery for existing profiles and observable best-effort drops.

## Complexity Tracking

Reuse existing durable CDC/journal producer and bounded subscriber patterns. ServiceAccount source
registration and provider consumption are narrow extensions, not a new broker,
per-kind journal, local user database or ServiceAccount controller.
