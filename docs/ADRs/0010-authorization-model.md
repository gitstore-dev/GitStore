# ADR 0010: Authorization Model — Canonical Actions, Scope and Conditions

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
`ClusterRole`, confined to that namespace. Role definitions have no namespace.
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

## Consequences

- One closed vocabulary is shared by policy, API, Git service and tooling.
- Scope, ownership and instance restrictions are independent provider inputs;
  missing facts fail closed.
- Operators update policy/configuration/enrollment together. No mixed-version
  compatibility promise or automatic data conversion is provided.
- Production OPA/ReBAC, persisted IAM entities, group-directory sourcing,
  GraphQL directives and Admin/Storefront splitting remain out of scope.
- Architectural acceptance does not establish capacity acceptance; #451 retains
  the sustained-load and deployed replacement/fault requirements.

## References

- [Pluggable AuthN/AuthZ Architecture](../implementation/020-pluggable_auth_architecture.md)
- [OPA Data-Aware Authorization](../implementation/022-opa-data-authorization.md)
- [Spec 064](../../specs/064-authz-action-vocabulary/spec.md)
- [Service boundaries](../../specs/064-authz-action-vocabulary/contracts/service-boundaries.md)
- [Controller authentication](../runbooks/controller-auth.md)
