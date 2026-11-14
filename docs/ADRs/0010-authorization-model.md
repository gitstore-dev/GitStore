# ADR 0010: Authorization Model — Canonical Actions, Scope and Conditions

> **Endpoint boundary update (2026-11-04):** Admin uses the fixed `/admin/graphql` path on `api.admin_port`; Storefront uses `/storefront/graphql` on `api.storefront_port`. The coordinated pre-GA cutover removes the generic port and legacy route in one release. The identity-only Storefront schema currently returns a fixed unavailable response before authentication or data execution. Publication/target readers, buyer workflows and declarative authorization directives described below remain separate work; no enablement or compatibility layer is introduced.

**Status**: Accepted (2026-10-11)

**Date**: 2026-09-24

**Audience**: API, controller, Git service, policy and deployment authors.

## Context

The original vocabulary mixed ownership decisions into `.own`/`.any`,
used both `category` and `categoryTaxonomy`, and conflated metadata reads
with Git contents access. Callers selected permissions after comparing owners,
preventing a pluggable authorizer from making the complete decision.

**Amendment (2026-10-11, spec 064):** The implementation adopts the strict,
allow-only model below, replacing the draft's deny precedence, expression
strings, namespaced ServiceAccounts and compatibility shim. Acceptance is
prepared in the implementing change; the date is the status-flip date because
its merge date is not yet known (README dating rule 3). Remaining capacity and
deployed fault evidence is tracked in [#451](https://github.com/gitstore-dev/GitStore/issues/451).
Architectural acceptance is not capacity acceptance. `rbac-local` remains
development/evaluation-only.

**Amendment (2026-11-10, spec 066):** Section 16 records the implemented,
production-gated Git-backed model and embedded OPA evaluator. It preserves
this ADR's acceptance date, the provider contract and allow-only vocabulary.
Validation-start capture deliberately does not implement a global
authorization fence. Replica/rolling, capacity and rollout acceptance remain
separate gates; this amendment does not enable production OPA.

## Decision

### 1. Preserve the provider contract

`AuthZProvider.Authorize(ctx, principal, action, ResourceContext)` and
`ResourceContext{Kind, Name, OwnerSub, Attrs}` are unchanged. Callers supply
authoritative facts; providers decide access. Principals carry trusted
subject/groups, not roles or `IsAdmin`. Role names belong to policy, never
token or signed-wire authority.

### 2. Canonical grammar and finite catalog

Actions are `<kind>[.<subresource>].<verb>`; the last component is always the
verb. Canonical camelCase kinds include `namespace`, `repository`,
`categoryTaxonomy`, `product`, `productVariant`, `collection`, `file`
and `serviceAccount`. Use the same kind in `ResourceContext.Kind`.

The authoritative catalog is `shared/schemas/authorization-actions.json`; generated
Go/Rust consumers and `make generate TARGET=authz` prevent separate vocabularies.
Every exact resource/verb pair must be registered. Reserved actions do not
create endpoints or watches. Wildcards expand over the finite catalog at load;
they never authorize unknown runtime actions.

| Action | Policy resource | Verb |
| --- | --- | --- |
| `repository.contents.read` | `repository/contents` | `read` |
| `namespace.status.write` | `namespace/status` | `write` |
| `product.management.read` | `product/management` | `read` |
| `product.purge` | `product` | `purge` |
| `serviceAccount.token.issue` | `serviceAccount/token` | `issue` |

### 3. Distinct operations require distinct grants

Control-plane verbs are `create`, `read`, `list`, `watch`, `update`,
`delete`. Cataloged domain/subresource verbs include `transfer`, `purge`,
`status.write`, `contents.read/write`, `token.issue`, `key.rotate` and
`namespace.system-repository.provision`. Read does not grant list/watch or
contents access; status writes do not grant final removal.

### 4. Ownership is a provider-evaluated fact

There are no `.own`/`.any` actions. Scope comes from bindings and instance
restrictions; ownership comes from `when: [owner]`. The provider matches
nonempty `OwnerSub` against the trusted subject/groups. Callers must not
compare owners to select a permission or bypass `Authorize`.
Concurrency comparisons against an authorized snapshot remain necessary;
conflicts require reloading and reauthorizing current facts.

### 5. Management and purge are independent capabilities

`product.management.read/list` and `productVariant.management.read/list`
are independently sufficient grants, not a second grant after ordinary
read/list. No role-name shortcut is allowed. Spec 064 retains the single
GraphQL endpoint and existing filtering; it does not add the proposed
Admin/Storefront split, visibility fields or ADR 0012 publication semantics.

Every existing completion mutation needs its own purge action:
`namespace.purge`, `repository.purge`, `categoryTaxonomy.purge`,
`product.purge`. Delete/status grants are insufficient. Finalizers, UID and
resource-version guards remain independent requirements.

### 6. Two scope tiers; allow-only union

`ClusterRoleBinding` reaches cluster and namespaced resources. A
`RoleBinding` requires a namespace and may reference `Role` or
`ClusterRole`, confined to that namespace. Static configuration role definitions
have no namespace; Git-backed Role resource envelopes carry their Namespace
identity as described in section 16.
Cluster-scoped kinds (`namespace`, `serviceAccount`) require cluster bindings;
scope comes from the catalog, not the presence of a namespace attribute.

A binding matches the structured principal subject or a trusted group. Access
requires one rule satisfying action, scope, instance and every condition.
Rules/bindings form an allow-only union; unmatched requests are denied.
There are no deny entries, role inheritance or nested groups. Separate rules
cannot contribute separate conditions to construct a grant.

### 7. Instance grants belong to rules

`resourceNames` restricts a rule to exact names; omitted/empty means all
instances within binding scope. List/watch requests have no instance name and
cannot use instance-only grants. Bindings carry structured
`roleRef: {kind: Role | ClusterRole, name: ...}` and
`subjects: [{kind: User | Group | ServiceAccount, name: ...}]`.

### 8. Structured conditions and denial presentation

`when` is an AND-list of `owner`, `targetOwnerMember`, or comparisons:

```yaml
when:
  - {attr: visibility, op: eq, value: public}
```

Allowed attributes are `namespace`, `tier`, `visibility`. Operators
`eq`/`neq` require a string `value`; `in`/`notIn` require a bounded
nonempty string `values` list. Missing/wrong-type facts fail every comparison,
including negative ones. No arbitrary expression evaluator or per-decision
external lookup is added.

For denied single-resource reads (including contents), a present non-public
string visibility yields `NOT_FOUND`; absent, public or wrong-type visibility
yields `FORBIDDEN`. This uses resource facts, never provider reason text.
Lists/watches preserve existing behavior.

### 9. Tier policy and immutable namespace invariants

Tier grants use `namespace.create` with a structured comparison, not a
special action. The shipped creator role grants USER; organization creation
requires a separate grant. USER namespace names equal their User owner's
normalized username; self-service creation names the caller. Group/SA ownership,
USER transfer/rename and any tier change are rejected even for cluster-admin
or masters. GraphQL and Git preflight/apply enforce these invariants.
Fresh bootstrap creates `default` as ORGANIZATION; no old-row repair is added.

### 10. Strict v1beta1 policy and built-ins

```yaml
apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: RbacLocalConfiguration
defaultDeny: true
clusterRoles:
  public-reader:
    rules:
      - resources: [repository/contents]
        verbs: [read]
        when: [{attr: visibility, op: eq, value: public}]
  controller-token:
    rules:
      - resources: [serviceAccount/token]
        verbs: [issue]
        resourceNames: [gitstore-controller-manager]
roles:
  catalog-reader:
    rules:
      - resources: [repository/contents]
        verbs: [read]
        resourceNames: [catalog]
clusterRoleBindings:
  - roleRef: {kind: ClusterRole, name: public-reader}
    subjects: [{kind: Group, name: 'system:unauthenticated'}]
  - roleRef: {kind: ClusterRole, name: controller-token}
    subjects: [{kind: ServiceAccount, name: gitstore-controller-manager}]
roleBindings:
  - namespace: acme-store
    roleRef: {kind: Role, name: catalog-reader}
    subjects: [{kind: Group, name: 'system:group:merchandiser'}]
```

This illustrative policy is not the full controller role. The complete shipped
`config/policy.yaml` defines `cluster-admin`, reusable `admin/edit/view`,
`namespace-owner`, `namespace-creator`, `controller`, instance-only
`controller-token`, narrow `system:api-internal` and conditional
`public-reader`. The local admin has a cluster-admin binding, not masters
membership. The controller receives its actual list/watch/status/provision/purge
inventory, not broad wildcard or SA-management authority. Its token grant
never replaces proof for the exact enrolled account.

Unknown fields/pairs, duplicate YAML keys, aliases, multiple documents, scalar
coercion, deny syntax, expression strings and `defaultDeny: false` fail loading.
Every active local user needs a satisfiable grant. CLI edits validate the whole
candidate before atomic replacement, preserve idempotence and never broaden
exact action pairs through resources×verbs grouping.

### 11. Evaluation and atomic reload

Validate the registered action, canonical kind, principal and required transfer
facts first. Trusted `system:masters` may then allow without policy lookup;
it is not synthesized from roles and cannot bypass domain/AuthN invariants.
Otherwise require one matching allow rule; default deny.

Policy loading builds bounded immutable indexes; publication is atomic.
Concurrent decisions see one complete snapshot, never mixed rules.
Invalid reload retains the previous configuration. Evaluation performs no
policy file, network or datastore reads. Limits are 4 MiB, 200 roles,
1,000 bindings, 2,000 rules and 256 principal groups. Logs omit credentials,
arbitrary attributes and policy contents. Measured latency/soak acceptance
remains separate work in #451.

### 12. Trusted subjects and cluster ServiceAccounts

Human subjects cannot assert reserved system identities. Authentication supplies
exclusive `system:authenticated` or `system:unauthenticated`; verified SAs
also receive `system:serviceaccounts`. Human groups use `system:group:<name>`.
Only configured trusted identity sources may supply privileged groups.
Explicit internal contexts alone may assert `system:api`; a missing caller
never promotes a request to that identity.

SAs have globally unique names and immutable UIDs. Binding subjects use the
plain name; authenticated subject/owner encoding is
`system:serviceaccount:<name>`. Namespace settings/arguments and old
namespaced subjects are rejected. Tokens/assertions still require current
authoritative UID/key, audience, expiry, enabled state and replay checks.
Delete/recreate cannot revive old enrollment. Name/UID uniqueness and updates
use atomic backend conditions; projections are never independent AuthN authority.

### 13. Breaking alpha baseline; no compatibility layer

There are no released deployments requiring old policy, wire or SA schema.
Reject v1 policy, legacy actions, namespaced SA config and bearer-only internal
RPCs rather than translating. Replace the undeployed SA baseline directly;
initialize fresh explicitly owned schemas. Never silently reset existing data
or bypass migration checksums. Historical actor strings remain audit data,
not accepted new identities.

API→Git and Git→API requests use signed, expiring, audience/method/body-bound
envelopes with single-use nonces. Callback replay state is shared atomically
between APIs; singleton Git persists bounded replay state across non-overlapping
replacement. Verified receive/ref/commit/repository actor facts reach callbacks,
which recheck live semantic per-kind policy. Post-receive denial is observable,
not bypassed. Exactly one active Git service is supported.

### 14. Creator, owner and transfer

`CreationActor` is immutable audit provenance. Mutable ownership is encoded
in `metadata.annotations["rbac.authorization.gitstore.dev/owner"]`; absence
falls back to CreationActor. `OwnerSub` projects the effective owner.
Structured owner inputs are normalized as facts; no new OwnerRef column is needed.

Each matching transfer rule must satisfy the complete formula:

```text
(currentOwnerMatches OR (clusterBinding AND unconditionalRule))
AND (targetOwnerMember OR unconditionalRule)
```

Its action/scope/name/conditions must also match. Separate rules cannot supply
separate halves. An unconditional namespaced grant cannot override the current
owner check. Valid `targetOwnerRef` is required even for masters. Removing an
owner annotation transfers back to the creator and needs transfer authority;
changing other authored fields also needs update authority. Domain invariants
and optimistic concurrency remain independent requirements.

The former caller-side `ownerMatchesPrincipal` permission path is removed;
providers consume owner/target facts. CAS retries reload and reauthorize changed
owners; stale writers cannot overwrite newer owner/status/spec fields.
Future ReBAC providers may project these facts into relation tuples without
introducing caller-side permission logic.

### 15. Proposed App and custom-kind catalog extension

**Proposed amendment (2026-11-01; not implemented).** This section describes a
future integration requirement for [ADR 0019](0019-app-installation-and-platform-capabilities.md).
Sections 1–14 and the compiled finite catalog remain the implemented baseline;
this addition does not make runtime action strings valid today.

CRD installation and declared transitions need a bounded, authoritative extension
to the finite action catalog. It must preserve the existing provider contract,
canonical action grammar, scope facts and default-deny behavior. Group-qualified
RBAC is not introduced: lower-camelCase kind slugs remain globally unique across
core kinds and installed definitions. Collisions are rejected atomically, even
when definitions are initially bound to disjoint namespaces. Namespace binding
controls kind visibility; it does not create a new authorization name space.

Proposed core App entries are `app.{create,read,list,watch,update,delete}`,
`app.capabilities.approve` and `app.status.write`. CRD registration entries and
custom CRUD/status/transition entries are registered before their endpoints or
bindings are advertised. Only trusted admission can derive entries from an
admitted definition and validated executor contract. Package input cannot supply
arbitrary scope, replace core actions or register actions in a request handler.

The extension snapshot contains immutable definition UID/generation, canonical
kind, exact action/resource/verb tuples, scope and digest. Bound its total kinds,
transitions and actions per namespace and deployment. Compile policy and catalog
into one immutable revision outside the request path; decision evaluation stays
bounded and performs no catalog/datastore/network I/O. Unsupported providers
cannot activate custom-kind dispatch; they fail closed rather than silently
falling back to a more permissive provider.

Exact grants require known entries at compile time. Wildcards expand over that
revision's finite catalog and do not automatically acquire subsequently installed
kinds. A catalog extension changing effective permissions requires an explicit,
audited policy recompilation/activation by an authorized principal. Preview must
show the added actions and effective-grant changes before approval. Requests pin
one compatible catalog/policy revision; replicas unable to load it deny or return
a typed unavailable result without executing the operation.

Catalog retirement first stops new dispatch and policy expansion, then retains
retired identity/action metadata as needed for pinned executions and audit.
Name reuse must not revive an old installation's grants: scope effective custom
grants to definition UID/catalog revision and require fresh approval for a
replacement UID. Retained workflow continuations run only under explicit current
authority and their pinned contract; retention is not a permanent permission.
Deletion and revocation invalidate relevant cached decisions and live dispatch.

Creating an App or pushing its manifests is not capability approval. Installation
must authorize each affected operation: definition/binding creation, managed
resources, cluster ServiceAccount provisioning/key enrollment, identity reuse,
RBAC assignment, subscription source access and transition invocation. Capability
approval cannot manufacture RBAC grants, and an RBAC grant cannot bypass host
capability approval. Recheck scope and UID facts after a conflicting CAS.

Approvals and external attestations are separate authorities. An approval policy
selects eligible principals through the configured provider and enforces
separation of duties. Do not embed a role name in a gate and compare it against
token claims. Evidence submission, subject reads and transition execution need
independent grants; delivery signature verification alone grants none of them.
Issuer registration and capability approval require separate audited actions
before those operations can be advertised. This proposal does not add those
entries to the generated JSON/Go/Rust catalogs in a documentation-only change.

Acceptance requires provider-neutral conformance, bounded compilation/evaluation,
two-API/two-controller concurrent activation/revocation, rolling replacement,
wildcard non-expansion, collision/name-reuse rejection, and sustained admission
and reconciliation load. The first implementation must specify shared revision
storage and activation barriers before claiming replica consistency.

### 16. Git-backed RBAC and embedded OPA

The four provider-neutral kinds are `Role`, `ClusterRole`, `RoleBinding` and
`ClusterRoleBinding` in `rbac.authorization.gitstore.dev/v1beta1`. Namespaced
resources belong to an immutable Namespace UID and its system repository;
cluster resources belong to the root system repository. Fixed canonical
manifest paths, whole-candidate admission and expected Git OIDs protect both
pushes and GraphQL writes. Git is authoritative; atomic Scylla/memdb tiers,
projection receipts and bounded repair are derived state. A committed write
with a failed projection is not a Git rollback.

The embedded `opa` provider prepares release-owned Rego once and evaluates
bounded immutable data indexes locally. It accepts no operator Rego, remote
bundle or sidecar, and performs no datastore, UserDir or network reads per
ordinary decision. Missing, stale, partial or cursorless snapshots fail closed
before privileged-identity shortcuts. Decisions carry their evaluated tier
revisions and catalog/built-in digests, not whichever publication happens to
be current at logging time. Namespace name reuse cannot select an old UID's
grants. Static providers remain independent; their grants are never merged.

Writes and reviews capture authoritative snapshots current at validation
start, within bounded retries/deadlines. The tier vector is not globally
atomic. An authorized write may finish after a concurrent cross-repository
revocation; a new candidate or conflict retry must capture authority again.
There is no global commit fence or distributed Git intent protocol. A
candidate cannot authorize itself. When unconditional full permission
coverage cannot be proved, bind/escalate must be granted explicitly; an
absent role target requires bind.

Release-owned roles/system bindings are immutable virtual resources with
deterministic identities and release digests. Initial administrator bootstrap
instead creates one ordinary removable binding and a durable once-only root
receipt; removal must survive restart. The enrolled primary lifecycle group
resolves role UIDs through durable acquire-before-publish claims and
UID/resourceVersion CAS, not name-only attachment. Namespace owner links are
non-blocking; role owner links block final removal while bindings reference
that UID. Terminating resources stop granting before finalization.

Live Staff USER namespace owners receive only the verified deterministic
`namespace-owner-access` template referencing `namespace-full-control`.
The subject is the trusted immutable owner subject, not a substituted username.
Owners and administrators cannot replace or remove this managed template;
ordinary collisions fail closed. Namespace removal first persists a UID-bound
cleanup handoff, then proceeds independently of child finalization. Bounded
background cleanup is not general orphan sweeping or Git object GC.

Admin `accessReview` adapts a transient SubjectAccessReview, with independently
authorized reviewer and target, strict trusted membership when groups are
omitted, explicit hypothetical inputs and exact evaluated revision identities.
It adds no SelfSubjectAccessReview or durable resource lifecycle. Opt-in
resource source diagnostics use bounded signed first-parent history at the
admitted revision; Git authors are metadata, not authenticated actors. Virtual
built-ins identify their release definition instead.

Production OPA construction remains refused pending deployed functional,
replica/rolling, bootstrap, load/fault and large-catalog acceptance. Provider
cutover requires grant parity, write freeze, traffic drain and snapshot-ready
admission; stale static rollback is unsafe. ADR 0018 remains Proposed: this
scoped UID/CAS/claim coordination implements neither platform-wide controller
leases nor external side-effect fencing. A future implementation of that core
requires its own explicit status-flip/index task. Git remains singleton-only
with non-overlapping replacement and retained storage.

## Consequences

- One closed vocabulary is shared by policy, API, Git service and tooling.
- Scope, ownership and instance restrictions are independent provider inputs;
  missing facts fail closed.
- Operators update policy/configuration/enrollment together. No mixed-version
  compatibility promise or automatic data conversion is provided.
- Production OPA activation, ReBAC, generic IAM entities, GraphQL directives
  and publication-aware Storefront query plans remain outside this amendment.
  The gated RBAC resource implementation is not acceptance of those proposals.
- Architectural acceptance does not establish capacity acceptance; #451 retains
  the sustained-load and deployed replacement/fault requirements.

## References

- [Pluggable AuthN/AuthZ Architecture](../implementation/020-pluggable_auth_architecture.md)
- [OPA Data-Aware Authorization](../implementation/022-opa-data-authorization.md)
- [Spec 064](../../specs/064-authz-action-vocabulary/spec.md)
- [Service boundaries](../../specs/064-authz-action-vocabulary/contracts/service-boundaries.md)
- [Controller authentication](../runbooks/controller-auth.md)
