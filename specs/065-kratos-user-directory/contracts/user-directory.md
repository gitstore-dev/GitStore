# User Directory and Authentication Contract

## Resolution order

For every authenticated non-anonymous request:

1. Authenticate with the configured AuthN provider.
2. Resolve `(provider, issuer, authenticated subject)` through an explicitly
   configured, bounded provider mapping (direct UID, external ID or exact
   supported username lookup). No GitStore subject-link table is introduced.
3. Read the profile and groups from the configured UserDir.
4. Attach profile type, username and directory groups to the principal.
5. Run AuthZ and endpoint/profile-type admission.

Anonymous requests are not looked up. With Kratos, no applicable provider/issuer
mapping leaves the principal unlinked, with no directory groups or profile type.
An applicable human mapping whose identity is missing or deleted fails admission
within the freshness bound, including when credentials remain valid and RBAC
grants the subject directly. Missing ServiceAccount mirrors remain unlinked;
GitStore source UID and enablement remain authoritative. A directory timeout or
provider failure is a retryable fail-closed response; it must never become an
empty group set. UserDir `none` and a provider without group capability return
no directory groups by contract.

Token claims are not a source of profile type, username or groups. Email is
not a subject link. Authentication and UserDir providers remain independently
configurable.

## Provider profile contract

The existing UserDir operations are retained and extended to carry the
identity profile fields in `data-model.md`:

- lookup by resolved subject/identity
- list groups for the resolved identity
- bounded actor list with provider pagination; public filters deferred to #471
- create/update identity profile
- human invitation, verification, suspension, deactivation/reactivation
  mapped to existing ActorLifecycleState
- update deliverability and consent

Persist `creation_actor` and `update_actor` in protected provider metadata,
using the same subject encoding as GitStore resources. Owner resolution follows
the ResourceOwner convention; absent creation-actor metadata falls back to the
represented identity's canonical username, never the requesting administrator.

GitStore-managed Staff mutations advance generation for author-owned changes
and resourceVersion for the integrated write, using the supported Admin PATCH
API. These are best-effort revisions, not remote CAS: concurrent replicas can
lose increments or overwrite changes. The v26.2.0 Admin PATCH endpoint rejects
`test`; the observed-version check is not a database condition. Counters do not cover out-of-band Admin writes or
every credential/address change. Customer self-service counters are unsupported:
generation is -1 and resourceVersion is "-1"; neither is a concurrency token.

Blocking registration/settings webhooks initialize actor metadata and reject
canonical-username/schema changes before persistence. Hook responses merge and
preserve protected metadata. Delivery and the receiver are internal-only and
configured only with the Kratos provider enabled; disabled routes return 404.
Hook failure blocks the protected flow. Direct Kratos Admin API writes bypass
these hooks; GitStore validates its own mutations. No Kratos-table triggers,
database extension, local profile table or additional concurrency layer is used.
The workload schema does not prohibit credentials created directly through
the privileged Admin API. Missing creator metadata is not reconstructed on
update; owner fallback is a read-time convention. Hooks cover the configured
profile/registration methods, not every credential/address write, and are not
durable consumers or commit notifications for cache invalidation.

Connection requests retain standard Relay arguments. Providers validate
direction support at query time and return the unified error diagnostic for
unsupported pagination. Kratos initially supports first/after only.
Capability advertisement remains deferred to #472.

Internal search and actor list requests use bounded work/pages. Kratos API errors are classified
as not-found, conflict, unavailable or invalid input; they are not silently
converted to empty profiles.

## Freshness and failure

- Directory request timeout: at most 2 seconds.
- Per-replica lookup cache: bounded entries and TTL no greater than 5 minutes.
- In-flight Kratos requests: bounded per API replica; no unbounded wait queue.
- Group removal, deactivation and suspension affect new requests within 5 minutes.
- A WebSocket connection's authenticated profile lease is capped below
  5 minutes. Group/deactivation updates cancel local connections immediately;
  the cap bounds stale subscriptions on other replicas.
- ServiceAccount CDC feeds the existing controller-facing ResourceWatchJournal
  backend. The configured provider consumes provider-neutral events directly
  through bounded internal journal reads/subscribers with process-local
  position/retries; no self-GraphQL client, external list/watch, durable consumer
  checkpoint, separate CDC reader, event bus or persistent intent.
- Creation waits for complete enrollment/projection visibility; deletion events
  preserve immutable UID. Preserve shared producer progress/leases and MemDB
  parity without extending them for UserDir. A future external ServiceAccount controller may use
  an adapter over the same journal; it and the adapter are not implemented.
- Retry provider event failures a small finite number of times with bounded
  backoff/deadlines. After exhaustion record a redacted failure, discard and
  continue. Bound buffers/overflow; no indefinite retry or DLQ.
- Start live consumption at the current stream boundary after restart.
  No durable consumer delivery, replay, automatic repair or backfill/sync CLI
  is required. Missing/stale mirrors or orphans can remain after dropped events;
  source UID/enablement still govern admission.
- Authentication recovery for existing profiles is tested within 30 seconds
  of dependency readiness, not recovery of discarded ServiceAccount events.
- Failure responses are retryable and do not disclose the raw subject,
  identifier, token or directory response.

## Authorization contract

- Profile type is derived from the Kratos schema ID and is available as a
  trusted principal attribute and synthesized profile-type group.
- AuthN continues to synthesize the generic `system:authenticated` group.
  After successful profile resolution, UserDir synthesizes exactly one of
  `system:staff`, `system:customers` or `system:serviceaccounts`. These groups
  cannot be supplied by token claims or editable directory group metadata.
- Namespace creation authorization stays in the RBAC policy; do not add a
  separate hard-coded GraphQL denial. The reference policy binds
  `namespace-creator` to `system:staff`, not `system:authenticated` or
  `system:serviceaccounts`.
- The RBAC `when` language must support a trusted `subjectUsername`
  predicate, matching the resource name to `principal.Username`. The
  `namespace-creator` rule combines this predicate with `tier == USER`.
  The shipped policy therefore does not grant Namespace creation to customers
  or ServiceAccounts; policy remains the authorization authority.
- ServiceAccount enablement and UID are authoritative in GitStore.
- Provider-owned workload mirrors use immutable UID-based Kratos external_id;
  repeated names never inherit old identity state or groups.
- Non-ACTIVE human lifecycle states fail admission within the freshness bound;
  protected provider state and verification facts distinguish all existing
  ActorLifecycleState values. No invented state-machine mechanism.
- Admin User operations use cluster-scoped `user.*` actions. Add those
  actions to `cluster-admin`; do not grant them to `admin`, `edit` or `view`.
- USER-tier Namespace and Admin reads remain authorization-protected;
  usernames are identifiers, not public authorization claims.
