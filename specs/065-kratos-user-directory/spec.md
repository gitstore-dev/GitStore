# Feature Specification: Production User Directory Provider (Kratos)

**Feature Branch**: `065-kratos-user-directory`  
**Created**: 2026-10-06  
**Status**: Closed (2026-11-09)

**Input**: User description: "let's implement the first production user-directory provider. We will support 3 different profiles - Staff users - Customer users - service accounts. these profiles can be represented with a schema for each. In the future, emails will be sent by a Notification provider such as Novu. this means user emails from the user-dir provider should take this into consideration. This should serve as the foundation for user-profiles in GitStore"

**Depends on**: spec 064 (canonical authorization vocabulary, cluster-scoped
ServiceAccount subject `system:serviceaccount:<name>`, evaluator consuming
`principal.Groups`, group sourcing handed to this spec).
**Initiatives**: #45 Headless User Management Module (this feature is its
foundation); #28 User Profiles (application profiles build on it later).

## Clarifications

### Session 2026-10-06

- Q: How are staff and customer identities separated? → A: One Kratos
  directory with one **identity schema per profile type**; GitStore admits
  each type only on its own endpoint. Kratos is a headless identity service;
  no OAuth-client or token-audience separation is involved.
- AuthN, AuthZ and UserDir are **independent planes that GitStore
  orchestrates**. Any combination is valid, e.g. `static-users` AuthN +
  Kratos UserDir + `allow-all` AuthZ. Profile type and groups therefore come
  from the UserDir lookup of the authenticated subject, never from the AuthN
  provider or its token.
- Q: Where do groups come from? → A: The UserDir provider only. Groups are
  never read from AuthN tokens. A token-claim source would work only for
  `oidc-jwt`; `static-users`, `serviceaccount-assertion`,
  `serviceaccount-jwt` and `anonymous` carry no group claims, and external
  IdPs differ (Keycloak, Dex, Gluu/Janssen can emit a groups claim managed in
  their own consoles; Google, Facebook and Twitter emit none). Claim-sourced
  groups would fragment group administration across every IdP in the chain
  and leave social and local logins without groups. Directory-owned groups
  work identically for every AuthN provider, are administered in one place,
  and keep AuthN responsible for credential authentication while UserDir owns
  directory groups.
- Both human profile types MUST support email-and-password and
  username-and-password login through Kratos. Email addresses and usernames
  are globally unique across profile types and are password-login identifiers.
- AuthN continues to synthesize `system:authenticated` for authenticated
  principals. UserDir synthesizes `system:staff`, `system:customers` or
  `system:serviceaccounts` from the trusted profile type; these values are
  never accepted from token claims or user-managed directory groups. Namespace
  creation is authorized through RBAC policy, not a separate GraphQL layer.
- Q: Where do operators administer staff identities and groups? → A:
  GitStore's own API, authorized by canonical `user.*` actions and audited.
- ServiceAccounts get a Kratos identity schema too: Kratos supports identities
  without credentials (login disabled), so a ServiceAccount profile can live
  in the directory without gaining login, recovery or verification flows.
  GitStore's ServiceAccount record remains authoritative for keys, UID and
  enablement.
- Email delivery: Kratos can delegate all identity mail through its HTTP
  email delivery mode. The target is a GitStore endpoint that hands messages
  to a pluggable notification provider; the notification provider itself is
  a later feature.

### Session 2026-11-07

- Staff and Customer borrow the OpenShift User envelope and, together with
  the existing ServiceAccount, implement Actor and Node. Reuse
  ActorLifecycleState via lifecycle.state; no GraphQL UserProfileType,
  flat active/createdAt fields or new auditing metadata.
- Use actor(by:) and Relay edge/cursor/PageInfo connections. Keep the standard
  pagination arguments; validate provider support at query time and return
  the unified error diagnostic for unsupported backward pagination.
  Capability advertisement is deferred to #472; filters/search remain #471.
- External ObjectMeta uses protected `creation_actor`/`update_actor` metadata,
  defaulted from the initiating Principal. Missing ownership falls back to the
  represented identity's username. Staff generation/resourceVersion advances
  are best-effort for integrated GitStore/profile writes, not remote CAS or
  all-provider-change counters. Customer self-service exposes -1/"-1" for
  unsupported counters. Concurrent PATCH operations can lose increments or
  overwrite changes; direct Kratos Admin writes bypass self-service hooks.
  Use blocking internal registration/settings hooks and supported Admin APIs,
  not Kratos-table customization or a GitStore profile database. Disabled
  provider routes return 404; hook unavailability blocks protected flows.
- All mutations use action-specific input and payload types. Invitation,
  pending verification, active, inactive and suspended behavior follows
  the existing enum, not a new state-machine framework.
- Human identity management stays external: no GitStore subject-link,
  user-profile or reconciliation-intent table.
  Provider-owned explicit mapping and Kratos external_id supply linkage;
  external ServiceAccount linkage uses immutable GitStore UID.
- ServiceAccount CDC MUST reuse the shared resource journal backend
  controllers depend on. UserDir consumes provider-neutral changes internally,
  with a small finite retry budget, then discards failed events and continues.
  No durable consumer checkpoint, replay, automatic repair, external list/watch,
  self-GraphQL client or checkpoint directory is required.
  A future controller can add an external adapter over the same source;
  building it or its transport is not scope. Enabling CDC on the existing
  authoritative table is the allowed
  schema change, not a new identity/outbox table or approval to rewrite
  migration history. Consumer delivery is best-effort, not distributed atomicity
  or guaranteed eventual convergence.
- Reference-stack updates select explicit latest stable compatible versions
  at implementation time; historical planning pins are not the target.
- This feature is not already deployed in production. No operator CLI,
  legacy backfill command or production identity-migration workflow is in
  scope. The UserDir consumer has no durability guarantee; missed/discarded
  runtime events need not be recovered automatically.

## Architectural Position

The user directory is the foundation for **identity profiles**, not for all
user profile data:

- **Identity profile (this feature, directory-owned).** Who a principal is:
  stable subject, profile type, username, display name, primary email and its
  verification and deliverability state, contact consent, lifecycle state and
  group membership. Read and administered by GitStore through the pluggable
  UserDir contract; swappable per deployment.
- **Application profile (#28, later, GitStore-owned).** Commerce data about a
  person — addresses, preferences, saved carts, order history, loyalty. It
  needs GitStore's authorization, watch and query model and MUST NOT be stored
  in the directory, keyed instead by the identity subject.
- **Notification readiness.** The directory owns contact facts but GitStore
  never treats an email address as a recipient key. A future notification
  provider addresses recipients by subject and resolves the current
  deliverable address and consent through the directory at send time.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Any authenticated subject resolves to a profile and groups (Priority: P1)

Whatever AuthN provider authenticated the caller, GitStore looks the subject
up in the configured UserDir, attaches its profile type and groups to the
principal, and spec 064's evaluator authorizes it through bindings to those
groups.

**Why this priority**: Spec 064 group bindings have no production source
until this exists.

**Independent Test**: With Kratos UserDir, create staff identity `bob` in
group `merchandiser`; bind `edit` to `system:group:merchandiser` in
`acme-store`. Authenticate `bob` once via `static-users` and once via
`oidc-jwt`; in both cases a product create in `acme-store` is allowed and in
`other-store` denied.

**Acceptance Scenarios**:

1. **Given** an active staff identity in group `merchandiser`, **When** it
   authenticates through any AuthN provider whose subject links to that
   identity, **Then** its principal carries `system:group:merchandiser`,
   `system:staff`, and profile type `staff`.
2. **Given** that member is removed from `merchandiser`, **When** FR-013's
   bound elapses, **Then** new requests no longer carry the group.
3. **Given** a deactivated identity, **When** it presents a still-valid token
   or credential, **Then** requests are rejected within FR-014's bound.
4. **Given** UserDir `none`, **When** any principal authenticates, **Then** it
   carries only spec 064's synthesized system groups and no profile type,
   and requests are not failed for lack of a directory.
5. **Given** an authenticated subject with no linked directory identity under
   Kratos UserDir, **When** it makes a request, **Then** it carries no
   directory groups and no profile type, and is authorized on that basis.

---

### User Story 2 - Customer identities are separated from staff (Priority: P1)

A shopper registers as a customer. Their identity uses the customer schema,
they can never acquire staff groups, and they are admitted only where
customers are admitted.

**Why this priority**: Customers are self-registered and outnumber staff by
orders of magnitude; the staff/customer boundary is this feature's main
security property.

**Independent Test**: Register a customer, authenticate, and confirm profile
type `customer`, no staff groups even with a tampered directory record, and
denial of every operator operation under the shipped policy.

**Acceptance Scenarios**:

1. **Given** self-registration, **When** a shopper registers, **Then** a
   customer-schema identity is created; staff and ServiceAccount identities
   can never be created through self-registration.
2. **Given** a customer, **When** it authenticates, **Then** its principal
   carries `system:customers` and never any group outside the customer group
   set.
3. **Given** a customer principal, **When** it calls any operator
   (management) operation, **Then** it is denied.
4. **Given** a staff principal on a customer-only endpoint, or a customer on a
   staff-only endpoint, **When** it calls, **Then** it is rejected based on
   the directory-resolved profile type.

---

### User Story 3 - Operators administer identities through GitStore (Priority: P2)

An authorized operator invites staff, suspends, deactivates and reactivates
identities, and assigns or removes groups through GitStore's API. Every change
is authorized by canonical `user.*` actions and audited.

**Why this priority**: Group membership drives authorization; it must be
changed through the same RBAC and audit trail as everything else.

**Independent Test**: As an operator with directory-administration grants,
create staff `carol`, add `carol` to `merchandiser`, verify the next request
carries the group; deactivate `carol`, verify rejection; repeat as a principal
without grants and verify every call is denied.

**Acceptance Scenarios**:

1. **Given** an operator granted `user.create`, **When** they create a staff
   identity, **Then** it exists with the staff schema and the change is
   audited.
2. **Given** an operator granted `user.group.update`, **When** they assign a
   group, **Then** it is visible on the member's next request within FR-013.
3. **Given** a principal without these grants, **When** it attempts any
   directory administration, **Then** it is denied.
4. **Given** any human, **When** they edit their own profile, **Then** they can
   change display name and request an email change, but can never change
   type, username, groups, lifecycle state or deliverability.
5. **Given** a group name that is reserved (`system:` prefix) or empty,
   **When** an operator assigns it, **Then** it is rejected.
6. **Given** a staff or customer identity, **When** its owner signs in with
   either its email and password or its username and password, **Then** Kratos
   authenticates that identity; an email address or username already used by
   any human profile type is rejected.
7. **Given** staff creation, **When** invitation/setup is incomplete,
   **Then** lifecycle.state is INVITED or PENDING_VERIFICATION; completion of
   required setup/verification is needed for ACTIVE.
8. **Given** an active human, **When** suspended or deactivated,
   **Then** lifecycle.state is SUSPENDED or INACTIVE respectively; valid
   tokens do not bypass admission and reactivation cannot skip unfinished
   verification. An outstanding staff invitation can be resent.
9. **Given** authorized actor lookup/listing, **When** queried through
   actor(by:), node(id:) or a connection, **Then** Staff, Customer and
   ServiceAccount use stable Relay IDs and edges/cursors/PageInfo without a
   profile-type enum, public filter or flat auditing fields.

---

### User Story 4 - ServiceAccounts have directory profiles without logins (Priority: P2)

When a ServiceAccount is created in GitStore, a credential-less, login-
disabled identity using the ServiceAccount schema is created in the
directory, so its subject resolves to a `serviceAccount` profile (and may
carry directory groups) like any other subject.

**Why this priority**: One lookup contract for every subject; lets operators
manage ServiceAccount group membership like staff.

**Independent Test**: Create ServiceAccount `ci-bot`, look up
`system:serviceaccount:ci-bot`, verify a `serviceAccount` profile with no
email and no usable login; add it to a group and verify its next token-
authenticated request carries the group.

**Acceptance Scenarios**:

1. **Given** `createServiceAccount` for `ci-bot`, **When** it succeeds,
   **Then** a provider-neutral shared journal event lets the configured UserDir
   synchronize a login-disabled ServiceAccount-schema identity, linked by
   immutable GitStore UID using external_id in Kratos.
2. **Given** that directory identity, **When** anyone attempts a login,
   recovery or verification flow for it, **Then** it fails.
3. **Given** the GitStore ServiceAccount is disabled or deleted, **When** its
   profile is read, **Then** it is reported inactive or absent; GitStore's
   record wins over the directory on any disagreement.
4. **Given** the directory is unavailable during ServiceAccount creation,
   **When** the source write succeeds, **Then** native creation remains
   independent of UserDir; application retries finitely then records a redacted
   failure and discards the event if the provider remains unavailable.
   Subsequent events continue. No durable delivery or convergence is claimed.
5. **Given** an API crash during event application or duplicate/stale events,
   **When** the consumer restarts or another replica handles an event,
   **Then** live consumption starts without a durable provider checkpoint or
   missed-history replay, duplicate handling is idempotent and GitStore
   UID/enablement remain authoritative even if a mirror is absent or stale.
6. **Given** a deleted account name is reused, **When** an old event or mirror
   is encountered, **Then** its old UID cannot bind to the replacement or
   transfer old directory groups.

---

### User Story 5 - Identity mail flows through GitStore to a notification provider (Priority: P3)

Kratos's verification, recovery and login-code messages are delivered through
its HTTP email mode to a GitStore endpoint, which hands them to the
configured notification provider. Contact data is addressable by subject.

**Why this priority**: No production notification provider ships here; this
fixes the delivery path and data contract so one can be added without
identity migration.

**Independent Test**: In the reference stack, trigger a verification email;
confirm Kratos posts it to GitStore, GitStore forwards it to the development
notification provider, and the message reaches the local mail catcher.

**Acceptance Scenarios**:

1. **Given** Kratos HTTP email mode targeting GitStore, **When** Kratos sends a
   verification message, **Then** GitStore accepts it only from the
   authenticated directory caller and forwards it to the notification
   provider.
2. **Given** an email change pending verification, **When** the profile is
   read, **Then** the previous verified address remains the deliverable
   address until the new one is verified.
3. **Given** a hard bounce reported for an address, **When** the profile is
   read, **Then** it is not deliverable.
4. **Given** a user who opted out of marketing contact, **When** the profile is
   read, **Then** consent shows transactional allowed and marketing denied.

---

### Edge Cases

- Directory unavailable or timing out during group resolution → the request
  fails closed with a retryable error; it is never evaluated with an empty
  group set, because that would silently skip `effect: deny` rules on groups.
  This differs from UserDir `none`/`static-users`, which have no group
  capability and legitimately return no groups.
- A directory record carries group `system:masters` or any `system:` value →
  ignored and reported; directory groups are always rendered as
  `system:group:<name>`.
- A customer record carries a staff group → ignored; customers only receive
  groups from the customer group set.
- A human edits their own traits to add a group or change type → impossible;
  groups and type are administrator-controlled identity metadata and schema
  assignment, never user-editable traits.
- Two AuthN providers in the chain yield the same subject string for
  different people (e.g. `static-users` `bob` and an OIDC `bob`) → linking is
  per configured AuthN provider (FR-004); an unlinked provider never inherits
  another provider's identity.
- Two human identities share an email across profile types → rejected.
- Same person is both staff and customer → two identities, two subjects, and
  distinct globally unique email addresses and usernames.
- Customer attempts to create a USER-tier namespace → denied (FR-018).
- Long-lived watch subscription when groups change or the identity is
  deactivated → re-authorized or closed within FR-013/FR-014.
- Switching UserDir provider → no cross-provider merging. With Kratos, no
  applicable mapping leaves the principal unlinked; an applicable human
  mapping whose identity is missing or deleted fails admission within the
  freshness bound. Missing workload mirrors remain unlinked and source
  UID/enablement stays authoritative.
- Schema evolution (new optional field) → existing identities stay valid; a
  new required field needs an explicit migration step.
- The GitStore mail endpoint is called by anything other than the directory →
  rejected.

## Requirements *(mandatory)*

### Functional Requirements

**Provider and orchestration**

- **FR-001**: The system MUST provide a production UserDir provider backed by
  Kratos, selectable through configuration alongside the existing `none` and
  `static-users` UserDir providers, implementing the existing UserDir
  contract (lookup by subject, list groups, search, upsert, deactivate).
  Selecting `static-users` as UserDir MUST NOT require `static-users` to also
  be present in the AuthN chain.
- **FR-002**: The UserDir provider MUST be independent of the AuthN and AuthZ
  providers: every AuthN provider (`static-users`, `oidc-jwt`,
  `serviceaccount-assertion`, `serviceaccount-jwt`, `anonymous`) combined with
  every UserDir provider MUST yield a principal whose profile type and groups
  come only from the UserDir.
- **FR-003**: After authentication and before authorization, the system MUST
  resolve the principal's subject through the UserDir and attach profile type
  and groups. Anonymous principals are not resolved. Enrichment MUST preserve
  the winning AuthN provider's `Subject`, `Issuer` and `AuthMethod`: OIDC uses
  its configured username claim with `sub` fallback, while `static-users` uses
  the authenticated static username. Directory usernames and identity UIDs
  MUST NOT replace this subject for User bindings or USER namespace identity.
- **FR-004**: Linking an authenticated subject to a directory identity MUST be
  explicit and configured per AuthN provider (for example by username, by
  the identity's external identifier, or by the identity ID). Mapping MUST
  be bounded, provider/issuer-qualified and owned by configuration/UserDir,
  not a GitStore subject-link table. Unsupported alias/search capabilities
  MUST NOT fall back to scans or email matching. Unlinked subjects carry no
  directory groups or type.

**Profile types and schemas**

- **FR-005**: The system MUST define three profile types, each with its own
  versioned Kratos identity schema: `staff`, `customer`, `serviceAccount`.
  Every directory identity uses exactly one schema; the schema determines
  the profile type.
- **FR-006**: Self-registration MUST create only `customer` identities.
  `staff` identities are created only by authorized administrators;
  `serviceAccount` identities only by GitStore when a ServiceAccount is
  created.
- **FR-007**: The resolved profile type MUST be available to authorization
  as an attribute. UserDir MUST synthesize exactly one profile-type group:
  `system:staff`, `system:customers` or `system:serviceaccounts`.
  AuthN continues to synthesize the generic `system:authenticated` group.
  At the existing Admin/Storefront endpoint boundary, GitStore MUST admit
  staff and ServiceAccount profiles on Admin/operator operations, and customer
  profiles only on Storefront/customer operations; customers MUST be denied all
  management operations. This feature does not change endpoint availability:
  until the Storefront prerequisites in ADR-0012 are met, Storefront requests
  continue to receive its `STOREFRONT_UNAVAILABLE` response. Namespace
  creation authorization MUST be expressed through the RBAC policy and its
  `when` conditions, not a second hard-coded authorization path.
- **FR-008**: ServiceAccount-schema identities MUST have no credentials and
  have every login, recovery and verification flow disabled.
- **FR-009**: GitStore's ServiceAccount record is authoritative for keys, UID
  and enablement. Full-image CDC on its authoritative table MUST feed the
  existing shared ResourceWatchJournal backend.
  Gate creation on enrollment/projection visibility and preserve deletion UID.
  The configured profile-capable UserDir consumes provider-neutral events;
  no direct Kratos CDC reader or parallel event bus. Consume through bounded
  internal journal reads/subscribers, not GraphQL or an external list/watch
  client. Preserve source-qualified publication progress/leases and MemDB
  parity; do not extend them for provider consumer checkpoints. Keep consumer
  position/retries process-local. Retry failed event application a small finite
  number of times with bounded backoff/deadlines, then record a redacted failure,
  discard and continue. Bound buffers/overflow. Start at the current stream
  boundary after restart; no durable consumer delivery, replay, automatic
  catch-up/repair, checkpoint directory, DLQ, outbox, identity/intent table or
  sync CLI. Missing/stale mirrors or orphans may persist after lost events.
  Kratos mirrors use UID-derived external_id, never a reusable name as
  linkage authority. Handling is best-effort, idempotent and replica-safe;
  GitStore wins disagreement. No cross-system atomic transaction is claimed.

**Profile contents**

- **FR-010**: Staff and Customer GraphQL types MUST implement Actor and Node
  and borrow the OpenShift User envelope (apiVersion, kind, metadata,
  fullName, identities, groups). The globally unique human username is
  metadata.name; primary email is also a password-login identifier.
  Human types expose email verification/deliverability and contact consent.
  Reuse ActorLifecycleState through lifecycle.state; no UserProfileType,
  flat active/createdAt or new GraphQL auditing fields. Creation time stays
  in metadata.creationTimestamp. Extend the existing ServiceAccount with
  Node identity; preserve its source-owned metadata/keys/lifecycle and no
  human email. No GitStore human-profile database is introduced.
- **FR-011**: Humans MAY change their own display name and request an email
  change. Type, username, groups, lifecycle and deliverability MUST be
  stored as administrator-controlled data that the identity cannot edit.
  An email change MUST NOT replace the deliverable address until verified.

**Groups**

- **FR-012**: Directory groups and profile-type groups MUST be sourced only
  from the UserDir provider and never from AuthN tokens or claims. AuthN
  synthesizes `system:authenticated` as an authentication-state group.
  Ordinary directory group values are rendered as `system:group:<name>`;
  reserved `system:` names MUST be rejected at assignment and ignored when
  resolving directory group metadata. Customers only receive ordinary groups
  from a configured customer group set.
- **FR-013**: Group changes MUST take effect on new requests within 5
  minutes; long-lived subscriptions MUST be re-evaluated or closed within the
  same bound.
- **FR-014**: Deactivation and suspension MUST take effect within 5 minutes
  for new requests and subscriptions, even for unexpired tokens or valid
  local credentials.
- **FR-015**: A failed or timed-out directory lookup MUST fail the request
  closed with a retryable error. A provider without group capability
  (`none`, `static-users`) MUST yield no groups without failing.

**Administration**

- **FR-016**: GitStore's API MUST provide directory administration — create
  staff invitation, resend invitation, read, list, update administrator-
  controlled fields, suspend/deactivate/reactivate, delete, assign and remove
  groups, set deliverability — authorized by cluster-scoped canonical actions
  on a new `user` kind: `user.create`, `user.read`, `user.list`, `user.update`,
  `user.delete`, `user.group.update`. Spec 064's `cluster-admin` includes them;
  `admin`/`edit`/`view` do not. Every mutation MUST use an action-specific input
  and payload. Human lifecycle maps to all existing ActorLifecycleState
  values, including unfinished setup and distinct suspension; reactivation
  cannot bypass required verification. Preserve existing ServiceAccount
  actions for source-owned workload administration.
- **FR-017**: Every directory administration change MUST be audit-logged with
  actor, target subject, change and time, without credentials or full email
  addresses.

**Integration with spec 064**

- **FR-018**: The reference RBAC policy MUST bind `namespace-creator` to
  `system:staff`, not `system:authenticated` or `system:serviceaccounts`.
  Its `when` conditions MUST restrict creation to USER-tier namespaces whose
  `metadata.name` equals the authenticated staff principal's AuthN-selected
  subject, not its UserDir username or identity UID. The `subjectUsername`
  condition MUST still require a valid directory username and a trusted Staff
  or Customer profile; the reference binding restricts this grant to Staff.
  Existing immutable USER namespace ownership remains unchanged.
  Customer and ServiceAccount profiles MUST NOT receive the
  synthesized `system:staff` group. RBAC policy remains the authorization
  authority; this feature MUST NOT add a separate hard-coded Namespace-create
  denial path.
- **FR-019**: The reference stack MUST ship example groups (`merchandiser`,
  `catalog-viewer`) bound to spec 064's `edit`/`view` roles in the bootstrap
  namespace.
- **FR-020**: `none` remains the default UserDir provider. `static-users` MUST
  remain available as a local UserDir provider when explicitly selected,
  independently of the configured AuthN chain. Documentation MUST describe
  mixed combinations (e.g. `static-users` AuthN + Kratos UserDir) and
  `make compose IDENTITY=oidc`.

**Mail delivery and notification readiness**

- **FR-021**: The reference Kratos configuration MUST use HTTP email delivery
  targeting a GitStore endpoint that accepts messages only from the
  authenticated directory caller and forwards them to a pluggable
  notification provider. This feature ships only a development notification
  provider that delivers to the local mail catcher.
- **FR-022**: The UserDir contract MUST let a consumer resolve a subject to
  its current deliverable email and consent. Consumers MUST key recipients
  by subject, never by email. Deliverability MUST be updatable by a system
  process (e.g. bounce handling) through the contract.

**Actor reads and implementation boundaries**

- **FR-023**: Actor MUST implement Node; Staff, Customer and ServiceAccount
  MUST explicitly implement both. Extend existing node lookup/global-ID and
  authorization paths. Use actor(by: ActorBy!) with one unique selector;
  separate human username and ServiceAccount-name lookup to avoid collisions.
  Unified actors uses UserDir enumeration and Relay edges/cursors/PageInfo,
  not a Scylla human projection or a nodes-only connection. A dedicated
  ServiceAccount connection reuses the existing source list contract.
  Ship Relay edges/PageInfo and the first/after/last/before signature.
  The provider MUST validate direction support at query time and return
  the unified error diagnostic for unsupported backward requests; Kratos
  initially supports forward pagination only. Never emulate backward reads
  with scans, deep offsets or process-local cursor history. Provider/extension
  capability advertisement is deferred to #472.
- **FR-024**: Public UserFilter/search syntax is deferred to #471. This
  feature MUST NOT add new GitStore identity/link/intent tables. Enabling
  ServiceAccount CDC on the existing table is the narrow schema-change
  exception; preserve migration-history rules. Provider-specific linkage
  stays inside UserDir. The shared ServiceAccount journal source MUST remain
  reusable by future controllers; external watch transport and a
  ServiceAccount controller are deferred.
- **FR-025**: Reference-stack dependency updates MUST select and explicitly
  pin latest stable compatible versions available at implementation time.
  Verify configuration, flows, pagination, external_id and HTTP courier
  against those pins, rather than freezing historical planning versions.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: Each API replica resolves profiles and groups
  independently; any per-replica cache is bounded and expires within
  FR-013/FR-014, so replicas disagree for at most that bound. Directory
  administration writes go to the directory, not replica memory. ServiceAccount
  ↔ directory event processing (FR-009) is idempotent and safe with concurrent
  API replicas, duplicate/stale events and name reuse. Verify bounded retries,
  discard/overflow and continued live delivery, not durable consumer recovery.
  Missed events may leave absent/stale mirrors; source authority still governs.
  No remote CAS is assumed. Switching UserDir
  provider is a configuration change plus identity provisioning; no datastore
  migration. The Git service is unaffected.
- **PR-002 Multi-User Security**: Customers can never acquire staff groups or
  operator access; reserved groups cannot come from the directory;
  identities cannot edit authorization-relevant fields; directory
  administration requires explicit grants; the mail endpoint accepts only the
  directory; credentials, tokens and email addresses are not written to
  application logs. Applies to every AuthN/UserDir/AuthZ combination.
- **PR-003 Capacity**: Directory supports 1,000 staff, 1,000,000 customers and
  1,000 ServiceAccounts. Profile/group resolution adds ≤10 ms p99 on a warm
  cache and ≤100 ms p99 on a miss; at 500 authenticated requests/s per
  replica, directory calls stay under 50/s per replica.
- **PR-004 Backpressure**: Directory calls have a bounded timeout (2 s) and
  bounded concurrency per replica; on saturation or timeout requests fail
  closed with a retryable error rather than queueing. Cache size is bounded.
  The mail endpoint bounds in-flight messages and rejects with a retryable
  status when the notification provider is saturated.
- **PR-005 Capacity Evidence**: `make capacity TARGET=api PROFILE=readiness
  MODE=alpha` with Kratos UserDir and a seeded dataset of 1,000 staff, 100,000
  customers and 100 ServiceAccounts; the verifier checks every request's
  group set and decision against the seeded memberships.
- **PR-006 Fault Recovery**: `make chaos` against the identity service: while
  unavailable, authenticated requests fail closed with retryable errors and
  native ServiceAccount commits remain independent while mirror delivery retries
  finitely then discards. Within 30 s of dependency readiness, requests for
  existing profiles succeed with correct groups and no non-ACTIVE human is
  admitted. Verify retry exhaustion, observable drops and API crash during
  application without requiring replay or mirror/orphan repair. The recovery
  bound does not guarantee provisioning a missing ServiceAccount mirror.

### Known limitation: best-effort ServiceAccount mirrors

ServiceAccount mirror synchronization has no consumer durability guarantee.
Dropped events, exhausted retries or process crashes can leave a Kratos mirror
missing, stale or orphaned indefinitely. Restart does not guarantee replay or
repair. GitStore's authoritative UID/enablement checks remain effective, but
directory profile/group availability and unified actor-list completeness are
not guaranteed after these faults. Operator documentation MUST disclose this
limitation; it must not promise automatic convergence.

### Key Entities *(include if feature involves data)*

- **Actor**: concrete Staff, Customer or existing ServiceAccount implementing
  Actor and Node; external human envelope, shared metadata, lifecycle.state
  and human contact facts. Internal subject/type remain provider-neutral.
- **Identity Schema**: versioned field definition for one profile type
  (`staff`, `customer`, `serviceAccount`).
- **Provider Mapping**: configured bounded AuthN-provider/issuer-qualified
  subject resolution; not a new database entity.
- **ServiceAccount Event**: provider-neutral shared resource journal event
  derived from authoritative CDC; shared journal producer infrastructure feeds
  bounded best-effort internal consumption, not external GraphQL, a durable
  provider checkpoint or a new intent entity.
- **Group Membership**: (identity, group name); administrator-controlled;
  rendered as `system:group:<name>`.
- **Contact Consent**: transactional/marketing flags with last-changed time.
- **Application Profile** *(out of scope, #28)*: GitStore-owned commerce data
  keyed by subject.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of seeded staff, customer and ServiceAccount subjects
  resolve to a profile of the correct type through one lookup contract.
- **SC-002**: For every AuthN provider × UserDir provider combination in the
  test matrix, groups and type come only from the UserDir (0 cases of
  token-sourced groups).
- **SC-003**: 0 customer principals carry a staff group or are allowed an
  operator action across the authorization test matrix, including tampered
  directory records.
- **SC-004**: Group removals and deactivations take effect on new requests and
  open subscriptions within 5 minutes in 100% of trials.
- **SC-005**: With the identity service unavailable, 0 requests are admitted
  with an incomplete group set; recovery within 30 s of it returning.
- **SC-006**: Successful ServiceAccount event delivery updates its mirror.
  In fault runs, every exhausted retry budget discards the event with a redacted
  observable outcome and subsequent events continue. Missing/stale mirrors or
  orphans are accepted after missed/discarded events; 0 replaced UIDs inherit
  prior grants.
- **SC-007**: Resolution meets PR-003 latency at the declared dataset size.
- **SC-008**: An email change never exposes an unverified address as
  deliverable (0 occurrences across the email-change test matrix).

## Assumptions

- Kratos is the first production UserDir provider; other providers implement
  the same contract later. Hydra and `gitstore-oidc-bridge` remain one
  possible AuthN path, not a requirement of this provider.
- Customer commerce profiles (#28), the storefront sign-in UI and a production
  notification provider are out of scope. ADR-0012 already provides the
  Admin/Storefront endpoint split; this feature applies profile-based
  admission to those endpoints without changing their availability or
  behavior otherwise.
- A person who is both staff and customer holds two identities, with distinct
  globally unique usernames and email addresses.
- Email addresses and usernames are globally unique across staff and customer
  profiles, consistent with Kratos password-identifier uniqueness.
- 5-minute freshness for groups and deactivation is acceptable for alpha.
- Forward Relay pagination and backward-request diagnostics, external
  ownership/version/generation concurrency, lifecycle/provider
  mapping and multi-replica best-effort event safety are implementation gates,
  not assumptions of already-supported Kratos functionality.
- No production ServiceAccount backfill CLI is required or introduced.
