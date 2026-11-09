# Admin GraphQL Contract: Actors

Operator operations use the existing Admin endpoint and authorization/error
conventions. This contract is implemented by the generated Admin schema and
provider-backed resolvers, not a second identity implementation.

## Actor envelope and Relay identity

GraphQL permits interfaces to implement other interfaces. Extend the existing
`Actor` to implement `Node`; every concrete object must explicitly implement
both `Actor` and `Node`. Staff and Customer additionally implement the human-only
`User` interface and repeat its concrete fields. Reuse `ActorLifecycle` and `ActorLifecycleState`, not
a profile-type enum or an `active` field.

```graphql
interface Actor implements Node {
  id: ID!
  metadata: ObjectMeta!
  lifecycle: ActorLifecycle!
}

interface User implements Actor & Node {
  id: ID!
  apiVersion: String!
  kind: String!
  metadata: ObjectMeta!
  fullName: String!
  identities: [String!]!
  groups: [String!]!
  lifecycle: ActorLifecycle!
  primaryEmail: String!
  emailVerified: Boolean!
  emailDeliverable: Boolean!
  contactConsent: ContactConsent!
}

type Staff implements User & Actor & Node {
  id: ID!
  apiVersion: String!
  kind: String!
  metadata: ObjectMeta!
  fullName: String!
  identities: [String!]!
  groups: [String!]!
  lifecycle: ActorLifecycle!
  primaryEmail: String!
  emailVerified: Boolean!
  emailDeliverable: Boolean!
  contactConsent: ContactConsent!
}

type Customer implements User & Actor & Node {
  id: ID!
  apiVersion: String!
  kind: String!
  metadata: ObjectMeta!
  fullName: String!
  identities: [String!]!
  groups: [String!]!
  lifecycle: ActorLifecycle!
  primaryEmail: String!
  emailVerified: Boolean!
  emailDeliverable: Boolean!
  contactConsent: ContactConsent!
}

type ContactConsent {
  transactional: Boolean!
  marketing: Boolean!
}
```

Borrow the OpenShift User envelope (`apiVersion`, `kind`, `metadata`,
`fullName`, `identities`, `groups`), not its database model or deprecated
group-administration mechanism. `metadata.name` is the canonical human
username; `metadata.uid` is the immutable directory identity UID.
`identities` represents provider-owned identity associations, not a new
GitStore Identity resource or subject-link table. It must not expose raw
tokens or provider credentials.

Extend the existing `ServiceAccount` definition to `implements Actor & Node`
with `id: ID!`; retain its existing metadata, keyIDs and lifecycle fields.
Add `groups: [String!]!` with lazy directory lookup by immutable source UID.
The optional provider-neutral `ServiceAccountGroupDirectory` capability uses
bounded native lookup/cache; Kratos validates mirror kind and linked source UID.
Missing mirrors and group-incapable providers return an empty list; operational
lookup errors remain explicit. Unselected groups do not perform directory I/O.
Linked Kratos groups include ordinary prefixed memberships and
`system:serviceaccounts`, never a fabricated `system:authenticated`.
ServiceAccount does not implement User. Existing group mutation names and Actor
payloads remain unchanged.
Do not create another ServiceAccount type. Its Relay ID uses the GitStore
UID, never the Kratos mirror UID. Human Relay IDs encode Staff/Customer and
the directory UID using the existing global-ID convention.

`__typename` identifies the concrete actor type. Internal trusted profile
classification remains available to AuthZ; it is not a GraphQL
`UserProfileType`. No flat `subject`, `username`, `displayName`, `active` or
`createdAt` fields are added. Creation metadata stays in
`metadata.creationTimestamp`; no new GraphQL audit types are introduced.

External ObjectMeta.owner defaults to the Principal initiating the operation,
using the existing ResourceOwner kind/name convention. Protected provider
metadata uses creation_actor/update_actor. Staff counters are best-effort for
integrated writes; Customer generation/resourceVersion are -1/"-1" and cannot
be used as concurrency tokens. Concurrent PATCH can lose increments or
overwrite changes, and out-of-band writes are not tracked. The pin rejects
PATCH `test`; an observed version is not a database condition. System-observed
changes do not advance generation. Verify concurrent administration/public
flow updates; do not claim remote CAS without evidence. Metadata shape reuse
does not turn directory identities into Git-backed resources.

## Lookup and connections

Kratos cursors bind to the connection and configured Admin URI. API replicas
using the same logical directory must use one stable URI; changing the
configured endpoint deliberately invalidates old cursors. Native v26.2.0
accepts interior-edge UID continuation through that endpoint.

```graphql
input ActorBy @oneOf {
  id: ID
  username: String
  serviceAccountName: String
}

type ActorEdge {
  cursor: String!
  node: Actor!
}

type ActorConnection {
  edges: [ActorEdge!]!
  pageInfo: PageInfo!
}

type ServiceAccountEdge {
  cursor: String!
  node: ServiceAccount!
}

type ServiceAccountConnection {
  edges: [ServiceAccountEdge!]!
  pageInfo: PageInfo!
}

extend type Query {
  actor(by: ActorBy!): Actor
  actors(first: Int, after: String, last: Int, before: String): ActorConnection!
  serviceAccounts(
    first: Int
    after: String
    last: Int
    before: String
  ): ServiceAccountConnection!
}
```

Human usernames are globally unique. A ServiceAccount may have the same name
as a human, so its name selector is separate. Integrate all three concrete
types into existing `node`/`nodes` lookup, authorization and error behavior.

`actors` enumerates the configured UserDir, not a merge of a Scylla human
table and a Kratos result set. Kratos lists all three schemas; each mirror
of a ServiceAccount is hydrated through a bounded authoritative UID lookup.
The dedicated ServiceAccount connection uses existing
`ListServiceAccounts`; it remains independent of UserDir availability.
Directory-list coverage of ServiceAccounts is best-effort, not an atomic
snapshot or guaranteed complete after missed/discarded events. Source-backed
reads remain authoritative; stale mirrors cannot bind to a replacement UID.

ServiceAccount synchronization consumes the shared resource journal internally;
neither watchResources nor an external list/watch client is required here.
The dedicated ServiceAccount connection is an Admin read surface, not the
internal synchronization transport. A future controller can add an external
adapter using existing generic watch conventions over the same backend.
The internal consumer retries finitely then discards; no durable applied
checkpoint, restart replay or automatic mirror repair is required.
The future transport/bootstrap contract is deferred; a Relay edge cursor is not
automatically a resourceVersion. No Kratos-specific subscription is added.

Use the established page limits, opaque edge cursors and forward/backward
ordering. Bind cursors to the provider and connection; reject malformed or
cross-provider cursors. No `nodes`-only connection, total count, typed
`UserFilter`, free-text search or new `query` argument ships here. Filter
syntax is deferred to #471. Retain existing internal provider search where
needed for exact lookup; it is not a new public filter convention.

**Provider pagination validation**: Ship the Relay connection and standard
arguments. Validate requested direction in the provider at query time:
Kratos initially supports bounded first/after and rejects last/before through
the existing unified error diagnostic, before directory enumeration.
Forward tokens still require correct edge cursors/PageInfo and provider-bound
cursor validation. No whole-directory scan, deep offset fallback, process-local
cursor history or speculative Scylla projection implements backward support.
Provider/extension capability advertisement is deferred to #472.

## Mutations

Every operation uses `<action>(input: <Action>Input!): <Action>Payload!`.
Payloads wrap the affected concrete object or `actor`; they never return an
identity directly. Reuse established metadata and global-ID inputs.

```graphql
input CreateStaffInput {
  metadata: ObjectMetaInput!
  fullName: String!
  primaryEmail: String!
  groups: [String!]
}

type CreateStaffPayload {
  staff: Staff
}

input UpdateUserInput {
  id: ID!
  fullName: String
  primaryEmail: String
  transactionalConsent: Boolean
  marketingConsent: Boolean
}

type UpdateUserPayload {
  actor: Actor
}

input DeactivateUserInput { id: ID! }
type DeactivateUserPayload { actor: Actor }
input ReactivateUserInput { id: ID! }
type ReactivateUserPayload { actor: Actor }
input SuspendUserInput { id: ID! }
type SuspendUserPayload { actor: Actor }
input ResendStaffInvitationInput { id: ID! }
type ResendStaffInvitationPayload { staff: Staff }
input DeleteUserInput { id: ID! }
type DeleteUserPayload { actor: Actor }
input AssignUserGroupInput { id: ID!, group: String! }
type AssignUserGroupPayload { actor: Actor }
input RemoveUserGroupInput { id: ID!, group: String! }
type RemoveUserGroupPayload { actor: Actor }
input SetUserEmailDeliverabilityInput { id: ID!, deliverable: Boolean! }
type SetUserEmailDeliverabilityPayload { actor: Actor }

extend type Mutation {
  createStaff(input: CreateStaffInput!): CreateStaffPayload!
  updateUser(input: UpdateUserInput!): UpdateUserPayload!
  deactivateUser(input: DeactivateUserInput!): DeactivateUserPayload!
  reactivateUser(input: ReactivateUserInput!): ReactivateUserPayload!
  suspendUser(input: SuspendUserInput!): SuspendUserPayload!
  resendStaffInvitation(input: ResendStaffInvitationInput!): ResendStaffInvitationPayload!
  deleteUser(input: DeleteUserInput!): DeleteUserPayload!
  assignUserGroup(input: AssignUserGroupInput!): AssignUserGroupPayload!
  removeUserGroup(input: RemoveUserGroupInput!): RemoveUserGroupPayload!
  setUserEmailDeliverability(input: SetUserEmailDeliverabilityInput!): SetUserEmailDeliverabilityPayload!
}
```

Lifecycle/edit/delete operations accept only human actors; use the existing
ServiceAccount administration path for source-owned workload state. Group
operations may administer a ServiceAccount's directory groups, but cannot
change its UID, keys, enablement or lifecycle. Email operations reject
ServiceAccounts. Customer creation remains Kratos self-registration.

Customer ordinary groups remain mandatory global memberships from
`customer_groups`, not an allowlist for per-identity edits. Assigning a
configured Customer group is idempotent; assigning any other group or removing
a mandatory membership is rejected. Protected per-identity metadata cannot
override this policy.

Staff creation initiates `INVITED`; resend applies only to an outstanding
staff invitation. Verification/setup flows govern `PENDING_VERIFICATION`
and `ACTIVE`. Suspension and deactivation are distinct
`SUSPENDED`/`INACTIVE` states; reactivation cannot bypass unfinished setup.
See `data-model.md` for the provider mapping and admission rules. No
plaintext password or arbitrary lifecycle-state setter is accepted.

`UpdateUserInput` cannot change type, username, groups or lifecycle.
Email changes remain pending until verified. Conflicts do not reveal
another identity's type or subject. No link/unlink mutations or alias
resource are introduced.

## Authorization and audit

| Operation | Required action |
|---|---|
| `actor`, `node`, `nodes` human result | `user.read` |
| ServiceAccount lookup | Existing `serviceAccount.read` |
| `actors` | `user.list` and existing `serviceAccount.list`, before enumeration |
| `serviceAccounts` | Existing `serviceAccount.list` |
| `createStaff` | `user.create` |
| `updateUser`, lifecycle operations, resend invitation, email deliverability | `user.update` |
| `deleteUser` | `user.delete` |
| `assignUserGroup`, `removeUserGroup` | `user.group.update` |

Use the existing field/node authorizer and canonical vocabulary, not a new
AuthZ mechanism. Directory administration is cluster-scoped; only
`cluster-admin` receives the new `user.*` actions by default. Failed
authorization must not disclose identity existence through edges, PageInfo
or errors. Credentials, password hashes, recovery/verification codes and raw
Kratos metadata are never exposed. Audit mutations through existing logging;
no database audit table or auditing GraphQL fields are added.
