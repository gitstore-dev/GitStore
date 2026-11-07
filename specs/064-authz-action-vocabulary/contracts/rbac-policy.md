# Contract: v1beta1 policy and decisions

## Document

```yaml
apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: RbacLocalConfiguration
clusterRoles:
  namespace-owner:
    rules:
      - resources: [namespace]
        verbs: [read, update, delete, transfer]
        when: [owner]
  public-reader:
    rules:
      - resources: [repository/contents]
        verbs: [read]
        when: [{attr: visibility, op: eq, value: public}]
roles:
  catalog-editor:
    rules:
      - resources: [product]
        verbs: [create, read, list, update, delete]
      - resources: [product/management]
        verbs: [read, list]
clusterRoleBindings:
  - roleRef: {kind: ClusterRole, name: namespace-owner}
    subjects: [{kind: Group, name: 'system:authenticated'}]
  - roleRef: {kind: ClusterRole, name: public-reader}
    subjects: [{kind: Group, name: 'system:unauthenticated'}]
roleBindings:
  - namespace: acme-store
    roleRef: {kind: Role, name: catalog-editor}
    subjects: [{kind: Group, name: 'system:group:merchandiser'}]
defaultDeny: true
```

This is a contract example, not the shipped policy. Rules are allow-only;
`effect`, allow/deny action lists, v1 and unknown fields are errors. Exactly one
YAML document is accepted; duplicate keys, aliases/cycles and excess bounds are
rejected. Roles are maps of names to `{rules: [...]}`. Definitions do not carry
namespaces. Bindings are ordered lists, not maps, and need no new name field;
their stable diagnostic identity is e.g. `roleBindings[0]`. Empty collections are
valid and deny everything; empty roles confer no permission. `defaultDeny` is
optional/true only; false is rejected with an actionable error.

ClusterRoleBindings must reference ClusterRole and must not contain namespace.
RoleBindings require nonempty namespace and may reference Role or ClusterRole.
All references must exist in the selected map. Names are nonempty; duplicate
binding entries grant no additional authority. No inheritance or nested groups.

## Rule projection and bounds

`action-vocabulary.json` is the finite authority: exact action, canonical kind,
kind scope, policy resource and pure verb. Exact resources/verbs must form valid
catalog pairs across their Cartesian product. A literal `*` in resources or
verbs expands over registered pairs; partial wildcards are invalid. An exact
verb that matches no catalog entry for its selected resources is invalid.
Namespaced Role definitions cannot include cluster pairs, including through
wildcards. A RoleBinding referencing a broad ClusterRole filters out cluster
pairs during evaluation. No namespace attribute can change a kind's scope.

`resourceNames` absent/empty means all instances; otherwise exact name set
membership is required. A collection request has an empty Name and never matches
an instance restriction. Do not use instance grants to authorize an unfiltered
list or watch; per-object result filtering is out of scope.

Initial hard limits, validated before compilation:

| Dimension | Limit |
| --- | ---: |
| Policy input | 4 MiB |
| Combined ClusterRole and Role definitions | 200 |
| Combined bindings | 1,000 |
| Total rules across definitions | 2,000 |
| Subjects per binding | 64 |
| Conditions per rule | 16 |
| Comparison values per condition | 64 |
| Resource names per rule | 256 |
| Resources / verbs per rule | 128 / 32 |
| Identifier or attribute string | 256 UTF-8 bytes |
| Principal groups | 256 (performance fixture uses 50) |

Reject over-limit inputs; never silently truncate. Authenticate/normalize groups
before authorization. Deduplicate subjects/groups and candidate binding-rule
pairs, preserving binding scope/provenance: the same transfer rule can have
different override semantics in cluster and namespace bindings. Use references
to compiled rules so shared bindings do not copy policy bodies.
Reload builders are serialized to bound memory to active, candidate and any
still-referenced in-flight snapshot. Readers take one snapshot and never wait
for YAML parsing or compilation. Superseded snapshots are reclaimed normally.

## Conditions

`when` is an optional list of zero or more entries, combined with AND:

- `owner`: nonempty OwnerSub equals principal.Subject or a principal group.
- `targetOwnerMember`: structurally valid `Attrs.targetOwnerRef`, encoded using
  the existing OwnerRef codec, equals principal.Subject or a principal group.
- `{attr: namespace|tier|visibility, op: eq|neq, value: string}`.
- `{attr: namespace|tier|visibility, op: in|notIn, values: [string, ...]}`.

Reject unknown keys, attributes, operators, condition tokens, empty sets and
mixed value/values forms. String comparison is exact and case-sensitive. Tier
values are USER/ORGANIZATION; visibility uses private/internal/public. Missing
or wrongly typed resource attributes return false even for neq/notIn. No
expression parsing, coercion, regexes, evaluation I/O or caller-set booleans.
`targetOwnerRef` is `{kind: User|Group|ServiceAccount, name: string}`;
GraphQL enum values are normalized at the boundary. OwnerSub uses the existing
prefix encoding; already encoded group names must not be double-prefixed.

## Evaluation

1. Reject malformed/unknown actions, mismatched Kind and invalid principal facts.
   Even `*` or masters does not make an unregistered action valid.
2. Trusted `system:masters` group membership allows a valid request. It cannot
   waive admission invariants, token proof requirements or target-shape validity.
3. Select candidate bindings using structured subject kind/name and groups.
   User matches only a human/internal User principal; ServiceAccount requires a
   verified SA principal and matches bare name; Group matches the group set.
4. Cluster bindings cover all instances. Namespace bindings require a namespaced
   catalog kind and equal nonempty `Attrs.namespace`. Missing namespace never
   falls back to any arbitrary RoleBinding.
5. Match action, instance and every condition. Transfer additionally enforces,
   within a single matching rule:

   `(owner || (clusterBinding && unconditional)) &&
   (targetOwnerMember || unconditional)`

   Unconditional means no `when` entries, not a role name or broad wildcard.
   Current-owner bypass needs a cluster binding; target bypass needs an
   unconditional rule. Target structure is mandatory for all transfer requests.
6. Any complete grant allows; otherwise deny. Conditions from different rules
   cannot be combined into a transfer override. No explicit deny precedence.

Deterministic diagnostics use canonical action, policy content digest, binding
list/index, role kind/name and rule index. Denials identify a bounded semantic
reason (no binding, scope mismatch, instance mismatch, condition missing/false,
invalid request or transfer constraint), not an unbounded list of candidates.
Provider-specific reason prose/timestamps are not fixture equality fields.

## Response semantics and management scope

Decision and ResourceContext types remain unchanged. The boundary response
mapper handles denials consistently for every provider: a single-object `read`
(including contents/management read) with present non-public visibility maps to
NOT_FOUND; missing/public/wrongly typed visibility and other operations map to
FORBIDDEN. List/watch results are not visibility-filtered. Missing fields must
not be filled with invented public facts. Git HTTP maps these to its existing
404/403 semantics after its existing authentication challenge behavior.

Keep the current single GraphQL endpoint and catalog publication filtering.
Resolve base read/list and management read/list entitlements explicitly through
the provider where the existing resolver needs PUBLIC/MANAGEMENT selection;
never infer management from role names or make it a resource input. A management
grant remains independently sufficient for management access. Do not implement
ADRs 0011/0012 or add a new visibility field as part of this feature.

## Built-in policy

| ClusterRole | Explicit grant set |
| --- | --- |
| cluster-admin | All registered pairs; shipped only with cluster binding to local admin |
| admin | edit plus purge where registered and transfer on namespaced kinds |
| edit | create/read/list/watch/update/delete on namespaced core kinds; product and productVariant management read/list |
| view | read/list/watch on namespaced core kinds; product and productVariant management read/list |
| namespace-owner | namespace read/update/delete/transfer, when [owner], bound to system:authenticated |
| namespace-creator | namespace create, when tier eq USER, bound to system:authenticated |
| controller | Explicit namespace/repository/categoryTaxonomy/product/file list/watch and required reads/status.write; all four completion purges; product management read/list; namespace.system-repository.provision; repository create/read as actually required by existing flows |
| controller-token | serviceAccount/token issue, resourceNames [gitstore-controller-manager], bound only to that ServiceAccount |
| system:api-internal | repository create/delete and repository/contents read/write, bound only to User system:api |
| public-reader | repository/contents read with visibility eq public, bound to system:unauthenticated; absent visibility still denies |

Use explicit core-kind pairs so future reserved domain actions do not expand
admin/edit/view/controller automatically. No status writes or cluster kinds in
admin/edit/view. Controllers retain only concrete operations required by traced
requests; test the enumerated grants and expected denials. A separately named
controller-token bundle narrows token issuance without granting it on all SAs.
The controller's effective required permission set includes that bundle.
`system:masters` has no default members. Binding names do not grant privileges.

Startup validation evaluates every active static user against its actual and
synthesized groups and at least one satisfiable rule; empty/contradictory rules
do not count. Use finite condition constraints, not a datastore scan of resources
or an assumption that any matching role name is useful. Reload rejection leaves
the active provider intact, including when this startup-equivalent check fails.

## CLI

Extend existing commands; these are the planned selectors:

```sh
make add-role ROLE=catalog-editor ALLOW=product.read,product.management.list SCOPE=namespace
make assign-role ROLE=catalog-editor SUBJECT=bob SUBJECT_KIND=User ROLE_KIND=Role SCOPE=namespace NAMESPACE=acme-store
make add-role ROLE=auditor ALLOW=repository.contents.read SCOPE=cluster
make assign-role ROLE=auditor SUBJECT=review-bot SUBJECT_KIND=ServiceAccount ROLE_KIND=ClusterRole SCOPE=cluster
make check TARGET=config
```

Default SCOPE=cluster, SUBJECT_KIND=User and ROLE_KIND=ClusterRole preserve the
simple operator flow. Namespace-scoped assign-role requires an explicit
NAMESPACE; do not silently use the root bootstrap default. Namespace-scoped
add-role creates a Role definition without a namespace.
ServiceAccount subjects are bare account names and never carry namespace.
Reject DENY, legacy actions and invalid scope/ref combinations before writing.
Add-role groups resource/verb pairs only if the cross-product grants exactly the
input set. Identical bindings are byte-for-byte no-ops. Preserve existing YAML
comments where possible using policyfile.go, and validate the full candidate
before atomic replace. Failure leaves the original file untouched.

The neutral `decision-table.json` is consumed unchanged by provider adapters.
Its validation, decision, and response sections are distinct so malformed policy
is never mistaken for a valid policy that denies a request.
