# Feature Specification: Production User Directory Provider (Kratos)

**Feature Branch**: `065-kratos-user-directory`  
**Created**: 2026-10-06  
**Status**: Draft  
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
  and keep AuthN responsible only for identity.
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

## Architectural Position

The user directory is the foundation for **identity profiles**, not for all
user profile data:

- **Identity profile (this feature, directory-owned).** Who a principal is:
  stable subject, profile type, username, display name, primary email and its
  verification and deliverability state, contact consent, active state and
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

An authorized operator creates staff identities, deactivates and reactivates
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
   type, username, groups, active state or deliverability.
5. **Given** a group name that is reserved (`system:` prefix) or empty,
   **When** an operator assigns it, **Then** it is rejected.

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
   **Then** the directory holds a login-disabled ServiceAccount-schema
   identity linked to `system:serviceaccount:ci-bot`.
2. **Given** that directory identity, **When** anyone attempts a login,
   recovery or verification flow for it, **Then** it fails.
3. **Given** the GitStore ServiceAccount is disabled or deleted, **When** its
   profile is read, **Then** it is reported inactive or absent; GitStore's
   record wins over the directory on any disagreement.
4. **Given** the directory is unavailable during ServiceAccount creation,
   **When** creation is attempted, **Then** it fails as a whole with a
   retryable error, or completes and the directory identity is reconciled
   later — never a ServiceAccount that permanently lacks its profile (FR-009).

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
- Two identities share an email within one profile type → rejected.
- Same person is both staff and customer → two identities, two subjects.
- Customer attempts to create a USER-tier namespace → denied (FR-018).
- Long-lived watch subscription when groups change or the identity is
  deactivated → re-authorized or closed within FR-013/FR-014.
- Switching UserDir provider → subjects not present in the new provider
  resolve as unlinked; no cross-provider merging.
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
- **FR-002**: The UserDir provider MUST be independent of the AuthN and AuthZ
  providers: every AuthN provider (`static-users`, `oidc-jwt`,
  `serviceaccount-assertion`, `serviceaccount-jwt`, `anonymous`) combined with
  every UserDir provider MUST yield a principal whose profile type and groups
  come only from the UserDir.
- **FR-003**: After authentication and before authorization, the system MUST
  resolve the principal's subject through the UserDir and attach profile type
  and groups. Anonymous principals are not resolved.
- **FR-004**: Linking an authenticated subject to a directory identity MUST be
  explicit and configured per AuthN provider (for example by username, by
  the identity's external identifier, or by the identity ID). Unlinked
  subjects carry no directory groups or type.

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
  as an attribute and as the synthesized group `system:staff`,
  `system:customers` or `system:serviceaccounts`. GitStore MUST admit
  customers only on customer endpoints and staff/ServiceAccounts only on
  operator endpoints; until the Admin/Storefront endpoint split exists,
  customers are denied every management operation.
- **FR-008**: ServiceAccount-schema identities MUST have no credentials and
  have every login, recovery and verification flow disabled.
- **FR-009**: GitStore's ServiceAccount record is authoritative for keys, UID
  and enablement. Creating, disabling and deleting a ServiceAccount MUST be
  reflected in the directory identity, and any divergence MUST be
  reconciled so that no ServiceAccount permanently lacks its profile and no
  orphan directory identity remains; GitStore's record wins on conflict.

**Profile contents**

- **FR-010**: Human profiles MUST expose subject (stable, never reused),
  username, display name, primary email, email-verified, email-deliverable,
  contact consent (transactional, marketing), active state and created time.
  ServiceAccount profiles expose subject, name, display name, active state
  and created time; no email.
- **FR-011**: Humans MAY change their own display name and request an email
  change. Type, username, groups, active state and deliverability MUST be
  stored as administrator-controlled data that the identity cannot edit.
  An email change MUST NOT replace the deliverable address until verified.

**Groups**

- **FR-012**: Groups MUST be sourced only from the UserDir provider and never
  from AuthN tokens or claims. Group values are rendered as
  `system:group:<name>`; reserved `system:` names MUST be rejected at
  assignment and ignored at resolution. Customers only receive groups from a
  configured customer group set.
- **FR-013**: Group changes MUST take effect on new requests within 5
  minutes; long-lived subscriptions MUST be re-evaluated or closed within the
  same bound.
- **FR-014**: Deactivation MUST take effect within 5 minutes for new requests
  and subscriptions, even for unexpired tokens or valid local credentials.
- **FR-015**: A failed or timed-out directory lookup MUST fail the request
  closed with a retryable error. A provider without group capability
  (`none`, `static-users`) MUST yield no groups without failing.

**Administration**

- **FR-016**: GitStore's API MUST provide directory administration — create
  staff identity, read, list/search, update administrator-controlled fields,
  deactivate/reactivate, delete, assign and remove groups, set
  deliverability — authorized by cluster-scoped canonical actions on a new
  `user` kind: `user.create`, `user.read`, `user.list`, `user.update`,
  `user.delete`, `user.group.update`. Spec 064's `cluster-admin` includes them;
  `admin`/`edit`/`view` do not.
- **FR-017**: Every directory administration change MUST be audit-logged with
  actor, target subject, change and time, without credentials or full email
  addresses.

**Integration with spec 064**

- **FR-018**: Only `staff` principals MAY own USER-tier namespaces; customer
  principals MUST be denied namespace creation regardless of policy.
- **FR-019**: The reference stack MUST ship example groups (`merchandiser`,
  `catalog-viewer`) bound to spec 064's `edit`/`view` roles in the bootstrap
  namespace.
- **FR-020**: `static-users` remains the local default UserDir; documentation
  MUST describe mixed combinations (e.g. `static-users` AuthN + Kratos
  UserDir) and `make compose IDENTITY=oidc`.

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

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: Each API replica resolves profiles and groups
  independently; any per-replica cache is bounded and expires within
  FR-013/FR-014, so replicas disagree for at most that bound. Directory
  administration writes go to the directory, not replica memory. ServiceAccount
  ↔ directory reconciliation (FR-009) is idempotent and safe with concurrent
  API replicas and controllers. Switching UserDir provider is a configuration
  change plus identity provisioning; no datastore migration. The Git service
  is unaffected.
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
  ServiceAccount creation fails or defers reconciliation; within 30 s of
  recovery, requests succeed with correct groups, no deactivated principal is
  admitted, and pending ServiceAccount profiles are reconciled.

### Key Entities *(include if feature involves data)*

- **Identity Profile**: subject, type, username, display name, contact
  (email, verified, deliverable, consent), active state, created time.
- **Identity Schema**: versioned field definition for one profile type
  (`staff`, `customer`, `serviceAccount`).
- **Subject Link**: per-AuthN-provider mapping from an authenticated subject
  to a directory identity.
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
- **SC-006**: 0 ServiceAccounts lack a directory profile, and 0 orphan
  ServiceAccount directory identities remain, after reconciliation in the
  fault-recovery run.
- **SC-007**: Resolution meets PR-003 latency at the declared dataset size.
- **SC-008**: An email change never exposes an unverified address as
  deliverable (0 occurrences across the email-change test matrix).

## Assumptions

- Kratos is the first production UserDir provider; other providers implement
  the same contract later. Hydra and `gitstore-oidc-bridge` remain one
  possible AuthN path, not a requirement of this provider.
- Customer commerce profiles (#28), the storefront sign-in UI, a production
  notification provider and the Admin/Storefront endpoint split are out of
  scope.
- A person who is both staff and customer holds two identities.
- Email uniqueness is per profile type.
- 5-minute freshness for groups and deactivation is acceptable for alpha.
