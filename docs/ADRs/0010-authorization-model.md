# ADR 0010: Authorization Model — Scope Tiers, Canonical Verbs, and Capability Qualifiers

**Status**: Proposed

**Date**: 2026-09-24

**Audience**: GitStore API, controller, git-service, IAM/policy, and deployment authors.

## Context

GitStore's `AuthZProvider.Authorize(ctx, principal, action, ResourceContext)` contract
([ADR-referenced doc 020](../implementation/020-pluggable_auth_architecture.md)) is stable, and
the OPA data-authorization design ([doc 022](../implementation/022-opa-data-authorization.md))
fixes the production engine and the `PUBLIC`/`MANAGEMENT` decision-scope semantics. What was never
pinned down is the **action-string vocabulary itself** — the set of `action` values middleware
passes to `Authorize`. As a result the vocabulary drifted:

- **`.own`/`.any` suffixes** (`namespace.delete.own`, `repository.read.any`) bake an ownership
  *decision* into the verb. The caller pre-computes ownership (`ns.OwnerUsername != principal.Subject`)
  and selects the action string, so the authorizer can no longer make that decision — even though
  `ResourceContext.OwnerSub` already carries the fact needed to make it.
- **Two slugs for one resource** — `category.*` in the live policy vs `categoryTaxonomy.*` in code.
- **Inconsistent repository verbs** — `repository.write`, `repository.read`, `repository.read.any`,
  `repository.create.own`, `repository.rename.own`, `repository.transfer.own` mix CRUD, git-content,
  and ownership concerns with no closed set.
- **No `list` or `watch` verbs** despite both being distinct control-plane operations.
- **Cluster-scoped subjects and cluster-scoped resources** had no model: role bindings were keyed by
  subject only, with cluster reach expressed implicitly through `.any`.

This ADR defines one canonical authorization vocabulary and scope model for the seven core resources
plus `serviceAccount`, reconciled with the frozen contracts in 020 and 022. It is **design-only**;
implementation (policy loader, evaluator, `ResourceContext` consumption) is deferred to a feature spec.

## Decision

### The core relationship: Subject · Group · Role · Permission

The model has four nouns and one edge:

- **Subject** — an authenticated principal identity: a `User`, a `ServiceAccount`, or a system
  identity. It is `principal.subject` on the `Principal`.
- **Group** — a named collection of subjects. Membership is asserted by the IdP/UserDir and arrives as
  `principal.groups`; GitStore persists *bindings*, never membership lists
  ([022 §8](../implementation/022-opa-data-authorization.md)).
- **Permission** — a single `<kind>[.<subresource>].<verb>` capability, with no qualifier position
  (§2/§5). It is exactly the `action` string passed to `Authorize`.
- **Role** (`Role` / `ClusterRole`) — a named set of **rules**; each rule grants a set of permissions,
  computed as `resources × verbs`, optionally narrowed by `resourceNames` (instance grants, §7) and/or
  a `when` condition (ownership/visibility/tier, §4/§8/§9).
- **Binding** (`RoleBinding` / `ClusterRoleBinding`) — the single edge that grants a Role's permissions
  to **Subjects and/or Groups**, at a tier (cluster, or one namespace).

```
Subject ─┐                                   ┌─(resources × verbs)
         ├─(RoleBinding / ClusterRoleBinding)─► Role ─(rules grant)─► Permission (= action)
Group  ──┘   binds subjects+groups to a role       [± resourceNames, ± when]
```

`Authorize(subject, action, resource)` allows iff some Role bound — via a `RoleBinding` in the
resource's namespace or a `ClusterRoleBinding` — to the subject **or to one of its groups** contains a
rule whose `(resources, verbs, resourceNames, when)` matches `(action, resource)`, and no rule denies.
Groups are the indirection: a subject inherits every permission of every role bound to any group it
belongs to. There is no role→role or group→group nesting (§6).

*Worked example (scenario):* principal `bob`, member of group `system:group:merchandiser`, in
namespace `acme-store`. A `RoleBinding` binds the `merchandiser` role to that group; `merchandiser`
grants `product.*`. So `Authorize(bob, "product.read", {kind: product, namespace: acme-store})` → allow
through his group, even though `bob` has no direct binding.

### 1. The frozen contract is preserved

`AuthZProvider.Authorize(ctx, principal, action, res ResourceContext)` and
`ResourceContext{Kind, Name, OwnerSub, Attrs}` do **not** change. Everything below is expressed in
terms of the existing `action string` argument and the existing `ResourceContext` fields. The
vocabulary change is a **v2 action grammar carried under `apiVersion: rbac.authorization.gitstore.dev/v1beta1`**,
not a signature change.

### 2. Canonical action grammar

Every action string has **at most two dot-segments after `<kind>`, and the last one is always the
verb** — there is no third, qualifier-shaped segment:

```
<kind>[.<subresource>].<verb>
```

- `<kind>` is the resource slug (camelCase, singular): `namespace`, `repository`, `categoryTaxonomy`,
  `product`, `productVariant`, `collection`, `file`, `serviceAccount`.
- `<subresource>` is an optional named subresource (`status`, `contents`, `token`, `key`,
  `management` — §5).
- `<verb>` is a control-plane verb or a subresource-natural verb (§3). There is no qualifier position:
  what a third `.<qualifier>` segment previously expressed (`read.management`, `delete.complete`) is
  now either a subresource or its own verb (§5).

This is what makes the grammar unambiguous to parse without a vocabulary lookup: previously, a
two-token suffix after `<kind>` (`X.T1.T2`) could mean either `(subresource, verb)`
(`repository.contents.read`) or `(verb, qualifier)` (`product.read.management`) — indistinguishable
positionally. With qualifiers retired, a two-token suffix means `(subresource, verb)` unconditionally;
the last segment is always the verb, full stop. The action string itself is unchanged from what
shipped code already emits (`config/policy.yaml`'s `repository.contents.read`,
`serviceaccount.token.issue`); only the policy-rule *projection* splits `<kind>.<subresource>` into a
`/`-joined `resources:` path, exactly as before:

| Action string                | Policy rule                                                 |
|-------------------------------|--------------------------------------------------------------|
| `repository.contents.read`   | `{ resources: ["repository/contents"], verbs: ["read"] }`   |
| `namespace.status.write`     | `{ resources: ["namespace/status"], verbs: ["write"] }`     |
| `product.management.read`   | `{ resources: ["product/management"], verbs: ["read"] }`    |
| `product.purge`              | `{ resources: ["product"], verbs: ["purge"] }`               |
| `serviceAccount.token.issue` | `{ resources: ["serviceAccount/token"], verbs: ["issue"] }` |

### 3. Verbs

**Control-plane verbs (closed set):** `create · read · list · watch · update · delete`. `list` and
`watch` are first-class and distinct from `read` (a single-object read). Canonical CRUD is retained
rather than `read`/`write` because the newer domain docs already depend on a richer verb space
(§below) and `create`/`delete` carry lifecycle meaning `write` cannot.

**Subresource-natural verbs:** subresources are not restricted to the control-plane set; they carry
the verb natural to the subresource — `status.write`, `contents.read`, `contents.write`,
`token.issue`, `key.rotate`, `management.read`, `management.list` (§5).

**Hard-delete verbs:** a resource kind may define its own, more destructive verb alongside `delete`
(e.g. `purge` for product — §5), rather than expressing it as a qualifier on `delete`.

**Domain verbs (non-CRUD):** the grammar accommodates the workflow/publication domain verbs already
defined in [doc 037](../implementation/037-custom-commerce-workflows.md) and
[publication-lifecycle](../products/publication-lifecycle.md) —
`workflow.definition.manage`, `workflow.release.approve`, `workflow.execution.transition`,
`workflow.bundle.install`, `workflow.action.<capability>`, `catalogRelease.create`,
`publication.create`, `publication.read` — and the IAM-administration verb `iam.manage`. These are
`<kind>.<verb>` (or `<kind>.<subverb>.<verb>`) values under the same grammar; no special case.

**Canonical vocabulary for the seven core resources + `serviceAccount`:**

| Kind               | Control-plane verbs                  | Subresources                                                              | Other verbs / conditions                    |
|--------------------|--------------------------------------|-----------------------------------------------------------------------------|----------------------------------------------|
| `namespace`        | create·read·list·watch·update·delete | `status.write`                                                             | `create` gated on `attrs.tier` (org/user)   |
| `repository`       | create·read·list·watch·update·delete | `status.write`, `contents.read`, `contents.write`                         | —                                            |
| `categoryTaxonomy` | create·read·list·watch·update·delete | `status.write`                                                             | —                                            |
| `product`          | create·read·list·watch·update·delete | `status.write`, `management.read`, `management.list`                      | `purge` (hard delete, §5)                    |
| `productVariant`   | create·read·list·watch·update·delete | `status.write`, `management.read`, `management.list`                      | —                                            |
| `collection`       | create·read·list·watch·update·delete | `status.write`                                                             | —                                            |
| `file`             | create·read·list·watch·update·delete | `status.write`                                                             | —                                            |
| `serviceAccount`   | create·read·list·update·delete       | `token.issue`, `key.rotate`                                                | —                                            |

`categoryTaxonomy` is the **single** slug; `category.*` is retired.

### 4. `.own`/`.any` are removed; scope and ownership are orthogonal binding-level axes

The `.own`/`.any` suffixes are eliminated. What they conflated splits into two independent axes
evaluated by the authorizer, never by the caller:

- **Scope** — expressed by *which tier the binding lives in* and, for a single instance, by
  `resourceNames` on the rule:
  - **cluster** — a `ClusterRoleBinding`; reaches every namespace (the former `.any`).
  - **namespace** — a `RoleBinding` in one namespace; reaches only that namespace.
  - **instance** — a rule carrying `resourceNames`; reaches only the named objects (§7).
- **Ownership** — a rule condition `when: owner`, which the authorizer evaluates by comparing
  `{principal.subject} ∪ principal.groups` against `ResourceContext.OwnerSub` (the former `.own`).

So `namespace.delete.own` becomes the action `namespace.delete` granted by a role whose rule has
`when: owner`; `namespace.delete.any` becomes `namespace.delete` granted by a `ClusterRoleBinding`.
Verbs stay pure.

### 5. No qualifiers: a `management` subresource, and hard-delete as its own verb

Earlier drafts of this ADR expressed two different things as a third `.<qualifier>` verb segment.
Neither is actually a qualifier on the base verb, and both are retired in favor of mechanisms the
grammar already has:

- **The catalog management-scope entitlement is a subresource, not a verb qualifier.** Reading or
  listing non-public rows is a distinct *view* of the resource — exactly what `contents` already is
  for `repository`, or `status` for every kind. So `product.read.management` /
  `product.list.management` become `product.management.read` / `product.management.list`: the
  `management` subresource, with `read`/`list` as its natural verbs. This preserves independent
  grantability — a role can hold `product.management.list` without `product.management.read`, exactly
  as `product.list.management` and `product.read.management` were independently grantable before
  (needed by the [022 §9](../implementation/022-opa-data-authorization.md) controller case: product
  counts including unpublished products, without per-product management reads).
- **`delete.complete` was never a modifier on `delete` — it is a distinct, more destructive verb.** It
  becomes its own control-plane-adjacent verb, `purge` (e.g. `product.purge`), granted independently
  of `product.delete`. A rule granting `delete` does not imply `purge`, and vice versa.

Per [doc 022](../implementation/022-opa-data-authorization.md), `PUBLIC` vs `MANAGEMENT`
(`catalog.visibility` / `DecisionScope`) is an **output of authorization consumed by the resolver**,
never a policy input. `product.management.read`/`product.management.list` are the entitlements whose
presence causes the authorizer to *emit* `MANAGEMENT` for that request; a resource never carries a
"management" flag that policy reads. Modeling the entitlement as a subresource rather than a verb
qualifier changes nothing about that output contract — it only changes how the grant is spelled.

### 6. Two scope tiers

Authorization has a **cluster tier** above the existing namespace IAM boundary:

| Tier      | Role kind     | Binding kind         | Reach                                                                                         |
|-----------|---------------|----------------------|-----------------------------------------------------------------------------------------------|
| Cluster   | `ClusterRole` | `ClusterRoleBinding` | all namespaces + cluster-scoped resources (`namespace`, `serviceAccount`)                     |
| Namespace | `Role`        | `RoleBinding`        | one namespace (the IAM boundary of [022 §8](../implementation/022-opa-data-authorization.md)) |

Cluster-scoped resources (`namespace`, `serviceAccount`) can only be governed by the cluster tier.
Namespaced resources may be governed by either tier. A namespaced `RoleBinding` may reference a
`ClusterRole` — reuse the permission bundle, confine its reach to one namespace (the binding decides
the blast radius, the role stays scope-agnostic). Consistent with
[022 §8.3](../implementation/022-opa-data-authorization.md), there is **no role inheritance and no
nested groups** in v1beta1; a subject's effective permissions are the flat union of its bound cluster
and namespace rules, minus explicit denies.

**Scope resolution (evaluator rule).** The evaluator derives the request scope from the resource — no
namespace attribute means a cluster-scoped kind:

```
scope := "cluster"  if ResourceContext has no namespace attr  else  Attrs["namespace"]

allow  iff  ∃ binding B, role R:
  subjectOf(B) ∈ {principal.subject} ∪ principal.groups      # the group edge (see relationship model)
  AND (B is a ClusterRoleBinding  OR  B.namespace == scope)   # binding scope covers the resource
  AND R ∈ B.roleRef  AND R has a rule allowing the action     # ± resourceNames / when
  AND no matching (B,R) denies the action                     # deny wins globally (§11)
```

Groups are the indirection: membership arrives on `principal.groups`, and a binding to a group grants
every member. For cluster-scoped kinds only `ClusterRoleBinding`s apply; for namespaced kinds both a
matching namespaced `RoleBinding` and any `ClusterRoleBinding` apply.

### 7. Instance-level grants live on the rule

A subject granted access to specific instances (e.g. the `catalog` and `spring-sale` repositories, but
not all repositories) is expressed by `resourceNames` **on the rule**, not on the binding:

```yaml
- resources: ["repository", "repository/contents"]
  verbs: ["read"]
  resourceNames: ["catalog", "spring-sale"]   # empty/absent ⇒ all instances of the kind
```

Bindings carry only `roleRef` + `subjects`; `resourceNames` is never duplicated onto the binding.
Both are **structured** (K8s-faithful): `roleRef: { kind: Role | ClusterRole, name: <role> }` and
`subjects: [{ kind: User | Group | ServiceAccount, name: <id> }]`. `roleRef.kind` is load-bearing — a
`RoleBinding` may reference a `ClusterRole` to grant that cluster role's rules *within one namespace*.
`subjects[].kind` disambiguates the principal type in place of prefix strings; reserved system
identities are groups — `{ kind: Group, name: "system:masters" }`,
`{ kind: Group, name: "system:authenticated" }`. This is the rbac-local precursor to the relationship
tuples a future ReBAC/OpenFGA provider would use.

### 8. Visibility bands are ABAC conditions, fail-closed

Resource visibility (`private` / `internal` / `public`) is an ABAC condition on the rule, read from
`ResourceContext.Attrs`:

```yaml
- resources: ["repository"]
  verbs: ["read", "contents.read"]
  when: "attrs.visibility == 'public'"
```

Denial of a read on a non-visible resource **fails closed to `NOT_FOUND`**, not `FORBIDDEN`, to
preserve the enumeration protection of [022 §13](../implementation/022-opa-data-authorization.md).

### 9. Tier gating is an ABAC condition

Namespace creation gated on tier (`ORGANIZATION` vs `USER`) is `namespace.create` with a rule
condition on `attrs.tier`, replacing the `namespace.create.organization` action-string variant:

```yaml
- resources: ["namespace"]
  verbs: ["create"]
  when: "attrs.tier == 'USER'"
```

### 10. Policy document shape (`rbac-local`, dev/eval only)

`clusterRoles` and `roles` are **maps keyed by role name** whose value is a rule-set; a role definition
carries no namespace of its own — the *binding* that references it decides the namespace of the grant.
Every element required by this ADR appears below: subresource paths (`repository/contents`,
`product/status`, `product/management`), the hard-delete verb (`purge`), visibility bands and
ownership as `when` conditions, instance grants via `resourceNames`, prefixed subjects, and structured
`roleRef`/`subjects`.

```yaml
apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: RbacLocalConfiguration

clusterRoles:
  public-reader:                                   # visibility band: public
    rules:
      - resources: ["repository/contents"]         # subresource path
        verbs: ["read"]
        when: 'attrs.visibility == "public"'       # ABAC condition (§8)
  controller:
    rules:
      - resources: ["repository", "product", "categoryTaxonomy", "namespace"]
        verbs: ["create", "read", "list", "watch"]
      - resources: ["product/status", "namespace/status", "repository/status"]
        verbs: ["update"]                          # status subresource ⇒ *.status.write action
      - resources: ["product/management"]          # management subresource, not a verb qualifier (§5)
        verbs: ["read", "list"]
      - resources: ["product"]
        verbs: ["purge"]                            # hard-delete verb, independent of "delete" (§5)

roles:                                             # namespaced rule-sets; namespace comes from binding
  merchandiser:
    rules:
      - resources: ["product", "productVariant", "collection"]
        verbs: ["create", "read", "list", "update", "delete"]
  internal-reader:
    rules:
      - resources: ["repository/contents"]
        verbs: ["read"]
        when: 'attrs.visibility == "internal"'     # visibility band: internal
  catalog-spring-sale-reader:
    rules:
      - resources: ["repository/contents"]
        resourceNames: ["catalog", "spring-sale"]  # instance-level grant (§7)
        verbs: ["read"]
  namespace-owner:
    rules:
      - resources: ["namespace"]
        verbs: ["read", "update", "delete"]
        when: owner                                # ownership condition (§4)

clusterRoleBindings:
  - roleRef: { kind: ClusterRole, name: public-reader }
    subjects: [ { kind: Group, name: "system:unauthenticated" } ]
  - roleRef: { kind: ClusterRole, name: controller }
    subjects: [ { kind: ServiceAccount, name: "system:serviceaccount:controllers:gitstore-controller-manager" } ]

roleBindings:
  - namespace: acme-store
    roleRef: { kind: Role, name: internal-reader }
    subjects: [ { kind: Group, name: "system:authenticated" } ]
  - namespace: acme-store
    roleRef: { kind: Role, name: catalog-spring-sale-reader }
    subjects: [ { kind: User, name: "bob" } ]
  - namespace: acme-store
    roleRef: { kind: Role, name: merchandiser }
    subjects: [ { kind: Group, name: "system:group:merchandiser" } ]

defaultDeny: true
```

> The catalog MANAGEMENT-scope entitlement is the `management` subresource's `read`/`list` verbs — it
> names the `DecisionScope` it yields (§5). `purge` is the separate hard-delete verb, granted
> independently of `delete`.

The `rbac.authorization.gitstore.dev` group is **reserved for the subject↔role↔permission plane
only**. `RbacLocalConfiguration` is dev/eval-only; production authorization is OPA (022).

### 11. Evaluation precedence

1. explicit **deny** anywhere wins;
2. `system:masters` short-circuits to allow;
3. matching **instance** grant (rule with `resourceNames`);
4. matching **namespace** binding;
5. matching **cluster** binding;
6. otherwise **default deny**.

Rule conditions (`when: owner`, `attrs.visibility`, `attrs.tier`) are evaluated *within* the matching
rule; a rule whose condition is false does not contribute an allow.

### 12. Subjects, groups, and the reserved prefixes

A binding subject is the structured `{ kind: User | Group | ServiceAccount, name: <id> }` of §7.
**Human groups are cluster-scoped names whose reach is decided by where they are bound** (K8s-aligned):
a `RoleBinding` scopes the *grant* to a namespace but does not make the subject namespaced.
**ServiceAccount identity is intrinsically namespaced**, and so is its implicit SA group. Membership is
authoritative in the IdP/UserDir and arrives on `principal.groups`; GitStore persists only bindings
([022 §8](../implementation/022-opa-data-authorization.md)).

The reserved name prefixes:

| Prefix / name                                     | Meaning                                      | Reason                                                                                     |
|---------------------------------------------------|----------------------------------------------|--------------------------------------------------------------------------------------------|
| `system:group:<name>`                             | cluster-wide group                           | canonical group identifier                                                                 |
| `system:serviceaccount:<ns>:<name>`               | service-account username                     | K8s-aligned; namespaced                                                                    |
| `system:serviceaccounts:<ns>`                     | SA group (all SAs in a namespace)            | correctly namespaced, because SA identity is                                               |
| `system:authenticated` / `system:unauthenticated` | synthesized by AuthN into `principal.groups` | bind cluster roles to these (`kind: Group`)                                                |
| `system:masters`                                  | root group                                   | evaluator short-circuits to allow before policy lookup (§11), granted via group membership |

A future namespace-local group, if ever needed, is a namespace-qualified *name*
(`system:group:<namespace>:<name>`) still bound per scope — deferred until a real use case, never a
`system:groups:<namespace>` enumeration.

### 13. Compatibility shim for legacy suffixes

Under `v1beta1` the loader recognizes legacy action strings for one deprecation window:

- `X.<verb>.any` → `X.<verb>` granted at the cluster tier;
- `X.<verb>.own` → `X.<verb>` with an implicit `when: owner` condition;
- `category.<verb>` → `categoryTaxonomy.<verb>`;
- `X.read.management` / `X.read.unpublished` → `X.management.read` (§5);
- `X.list.management` / `X.list.unpublished` → `X.management.list` (§5);
- `X.delete.complete` → `X.purge` (§5).

This reconciles the "existing action strings do not change" guarantee of
[022 §17](../implementation/022-opa-data-authorization.md): the *contract* (signature, decision
semantics) is unchanged; the vocabulary migrates behind the compat shim.

### 14. Ownership assignment and transfer

`ResourceContext.OwnerSub` is populated today from `CreationActor` — an **immutable audit fact**
(who admitted the resource), always the individual caller's `principal.subject`. Nothing lets a
caller assign a group as owner, and conflating "who created this" with "who owns this" blocks
ownership from ever moving.

**Split creator from owner.** A resource carries two distinct fields:

- `CreationActor` (unchanged) — immutable, set once at admission, audit/history only.
- `OwnerRef` — mutable, structured `{ kind: User | Group | ServiceAccount, name: <id> }` (the same
  shape as a binding subject, §12). `ResourceContext.OwnerSub` becomes a projection of
  `OwnerRef.name`; the `when: owner` condition (§4) is unchanged — it still compares that name against
  `{principal.subject} ∪ principal.groups`. Defaults to `{kind: User, name: principal.subject}` at
  creation when the caller does not specify one, preserving today's behavior exactly.

**Assigning ownership at creation** is an optional `ownerRef` input on the create mutation (e.g.
`createNamespace(input: { ..., ownerRef: { kind: GROUP, name: "merchandiser" } })`).

**Reassigning ownership after creation** is its own action, not folded into `update` — handing a
resource to a different group is a materially different blast radius than editing a field. It is a
domain verb per §3's grammar: `<kind>.transfer` (e.g. `namespace.transfer`), carrying the proposed
owner in `ResourceContext.Attrs["targetOwnerRef"]`. A rule permits it only when **both** hold:

1. the caller currently satisfies the resource's existing `when: owner` condition (you can only give
   away what you own) — or holds an unconditional cluster-tier grant of `<kind>.transfer`; and
2. the caller is a member of the target owner (`attrs.targetOwnerRef.name ∈ principal.groups`, or the
   target is the caller's own subject) — or the caller's grant is unconditional (an admin reassigning
   on another owner's behalf).

This prevents a caller giving a resource away to a group they have no standing in, while still letting
cluster-tier admins perform administrative reassignment.

**Forward-compatibility with ReBAC/Zanzibar providers.** `OwnerRef`/`OwnerSub` is an **ABAC seed
fact**, not a second source of truth for ownership. A Zanzibar-style provider (OpenFGA) does not read
`ResourceContext.OwnerSub` per request — it maintains its own `(resource, "owner", subject-or-group)`
relation tuple, written at admission/transfer time, and evaluates `Authorize` entirely against its
tuple graph. `OwnerRef` is what a write-time sync hook reads to populate that tuple; splitting it from
`CreationActor` now is what makes that hook straightforward, since a ReBAC "owner" relation is
inherently mutable/transferable, exactly like `OwnerRef` and unlike the immutable audit field.

The one real risk to that swap is **not** the field split — it's any call site that compares ownership
directly (`resource.OwnerRef == principal.Subject`) instead of routing through `Authorize`. That
reintroduces the `.own`/`.any` pre-computation anti-pattern (§4) under a different name and would need
duplicating in every future provider. `internal/middleware/security/secure.go` and `graphql.go` (the
`ns.CreationActor == principal.Subject` checks) already do this today and must be migrated to call
`Authorize` in the same feature spec that implements this section — not carried forward as a second
ownership code path once `OwnerRef` exists.

## Consequences

- **Verbs are pure and the authorizer regains its decision.** Callers stop pre-computing ownership;
  `ResourceContext.OwnerSub` is finally consumed.
- **One vocabulary** across code, policy, and docs; `category`/`categoryTaxonomy` and the
  `repository.*` verb sprawl collapse to the canonical table.
- **Cluster scope is explicit**, not smuggled through `.any`.
- **The grammar has no qualifier position and is unambiguous to parse without a vocabulary lookup**
  (§2/§5): the last dot-segment is always the verb. `read.management`/`list.management` became the
  `management` subresource; `delete.complete` became the independent `purge` verb.
- **Instance grants and visibility** are expressible in rbac-local today and map cleanly onto a future
  ReBAC provider and onto OPA Rego.
- **Migration cost:** the live `config/policy.yaml`, the `rbac-local` loader/evaluator, and every
  call site that emits `.own`/`.any`/`category.*`/`namespace.create.organization`/
  `.read.management`/`.list.management`/`.delete.complete` must be updated in the follow-up feature
  spec. The compat shim bounds the blast radius during rollout. This includes
  the two live call sites (`secure.go`, `graphql.go`) that pre-compute ownership directly against
  `CreationActor` instead of calling `Authorize` — these must move to `OwnerRef`/`Authorize` in the
  same spec (§14).
- **Ownership becomes assignable and transferable** (§14): the immutable `CreationActor` audit field
  and the mutable, structured `OwnerRef` are split, so a resource can be owned by a group from
  creation or reassigned later, gated by membership in the target owner.
- **Instance grants and visibility** are expressible in rbac-local today and map cleanly onto a future
  ReBAC provider and onto OPA Rego. `OwnerRef` is deliberately kept as an ABAC seed fact rather than an
  authority a provider reads directly, so a future Zanzibar/OpenFGA provider can populate its own
  owner relation tuples from it without `ResourceContext` becoming a second source of truth.
- **Not addressed here (deferred):** the OPA Rego encoding of tiers/conditions/instance grants, the
  persisted IAM entities for the cluster tier, the concrete `OwnerRef` datastore migration, and any
  change to `ResourceContext`'s Go type (none required — `Attrs` carries `visibility`/`tier`/
  `targetOwnerRef`, `OwnerSub` carries the current owner).

## References

- [doc 020 — Pluggable AuthN/AuthZ Architecture](../implementation/020-pluggable_auth_architecture.md)
- [doc 022 — OPA Data-Aware Authorization](../implementation/022-opa-data-authorization.md)
- [doc 037 — Custom Commerce Workflows](../implementation/037-custom-commerce-workflows.md)
- [publication-lifecycle](../products/publication-lifecycle.md)
- [ADR 0009 — Credential and Secret Material Boundary](0009-credential-secret-boundary.md)
