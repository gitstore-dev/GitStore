# Configure local authorization

The `rbac-local` authorization provider reads a YAML policy from
`api.auth.rbac_local.policy_file` (environment variable
`GITSTORE_API__AUTH__RBAC_LOCAL__POLICY_FILE`). See
[configuration](configuration.md#authorization-and-user-directory) for provider
settings and [the example policy](../gitstore-api/policy.yaml.example) for the
development baseline and commented examples you can adapt. Review the baseline
before production use, including its administrator and anonymous public-read
grants.

## Roles and binding scope

A role defines allowed resource/verb pairs. A binding grants those rules to
users, groups or service accounts. Defining a role alone grants no access.

| Policy field | Purpose |
| --- | --- |
| `roles` | Named `Role` definitions for use with namespaced bindings |
| `clusterRoles` | Named `ClusterRole` definitions reusable with either binding tier |
| `roleBindings` | Require `namespace`; reference a `Role` or `ClusterRole` and grant access only within that namespace |
| `clusterRoleBindings` | Reference a `ClusterRole`, omit `namespace`, and grant access across namespaces and to cluster-scoped resources |

Role definitions themselves have no namespace. A `RoleBinding` referencing a
`ClusterRole` still grants access only in its specified namespace. Cluster-scoped
resources such as `namespace` and `serviceAccount` require a
`ClusterRoleBinding`; a namespaced binding cannot grant those actions.

Subjects use `kind: User`, `Group` or `ServiceAccount`. User names must match the
authenticated subject, and group names must match groups supplied by the trusted
authentication provider. Service accounts use their bare cluster-wide name,
such as `gitstore-controller-manager`, with no namespace or subject prefix.
Bindings grant permissions; they do not create identities or enroll service
accounts.

## Define a Role and bind it to a namespace

This complete minimal policy lets `alice` read or update two repositories and
two products in `acme`, and read the contents of `catalog`:

```yaml
apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: RbacLocalConfiguration
defaultDeny: true
roles:
  catalog-maintainer:
    rules:
      - resources: [repository]
        verbs: [read, update]
        resourceNames: [catalog, seasonal-catalog]
      - resources: [product]
        verbs: [read, update]
        resourceNames: [coffee, tea]
      - resources: [repository/contents]
        verbs: [read]
        resourceNames: [catalog]
roleBindings:
  - namespace: acme
    roleRef: {kind: Role, name: catalog-maintainer}
    subjects: [{kind: User, name: alice}]
```

This is a focused access example. When adapting the development baseline, merge
these entries into its existing mappings and lists so the API and controller
retain their required service grants. Do not repeat top-level YAML keys.

## Bind a reusable ClusterRole within a namespace

The shipped example defines `view`, `edit` and `admin` ClusterRoles. Add this
entry to its `roleBindings` list to grant `catalog-readers` the `view` rules only
in `acme`:

```yaml
roleBindings:
  - namespace: acme
    roleRef: {kind: ClusterRole, name: view}
    subjects: [{kind: Group, name: catalog-readers}]
```

To grant the same role across namespaces, use a cluster binding instead:

```yaml
clusterRoleBindings:
  - roleRef: {kind: ClusterRole, name: view}
    subjects: [{kind: Group, name: catalog-readers}]
```

Choose the binding tier deliberately: a cluster binding to `view` grants its
namespaced read permissions in every namespace.

## Restrict rules with resourceNames

`resourceNames` is an optional list of exact, case-sensitive resource names.
Use `catalog` for a repository named `catalog`, not its UUID, Git URL or
`acme/catalog`. Use product names such as `coffee` for product rules and bare
service-account names for `serviceAccount/token` rules. A repository's name on
a `repository` rule does not restrict a separate `product` rule or grant
`repository/contents` access; each rule names the resource it authorizes.

- Omitting `resourceNames` or setting it to `[]` allows all names within the
  binding's scope. `resourceNames: ["*"]` matches the literal name `*`; it is
  not a wildcard.
- List and watch requests have no instance name. A rule restricted by
  `resourceNames` cannot grant them, and the list is not a result filter. Add a
  separate unrestricted list/watch rule only if the subject should have that
  broader access.
- Keep create grants in separate rules without `resourceNames`. The current
  local provider does not reject `create` rules containing `resourceNames`:
  it applies its usual exact-name check when the request supplies a proposed
  name. A request without a name cannot match such a rule.
- Grants are additive. A broader rule or another matching binding can allow
  access outside a name list; there are no deny rules to override that grant.
- A rule must match the resource/verb pair, binding scope, resource name and
  every `when` condition together. Permissions from separate rules cannot be
  combined to satisfy parts of a single authorization check.

For example, the baseline's `controller-token` ClusterRole grants
`serviceAccount/token` with verb `issue` only for
`resourceNames: [gitstore-controller-manager]`. Its cluster binding is required
because service accounts are cluster-scoped.

## Apply when conditions

`when` is a list of conditions that must all match within one rule. Omit it for
an unconditional rule. Conditions use trusted authorization facts supplied by
the operation; they do not read arbitrary request fields. A condition on an
attribute that the operation does not supply denies access, including with
`neq` or `notIn`.

### Ownership and transfers

`owner` requires the resource's owner to match the authenticated subject or one
of its trusted groups. For example, add this rule to a Role and bind it in the
intended namespace to allow updates only to repositories the subject owns:

```yaml
rules:
  - resources: [repository]
    verbs: [update]
    when: [owner]
```

`targetOwnerMember` requires the transfer target to match the subject or one of
its trusted groups. Use both conditions to require ownership of the source and
membership in the target owner:

```yaml
rules:
  - resources: [repository]
    verbs: [transfer]
    when: [owner, targetOwnerMember]
```

Transfer requests must supply a valid `targetOwnerRef`. A namespaced transfer
grant still requires source ownership even if `owner` is omitted. An
unconditional cluster binding can override source ownership; an unconditional
transfer rule can override target membership. Use explicit conditions when
those restrictions should apply.

### Attribute comparisons

Supported attributes are `namespace`, `tier` and `visibility`. Comparisons are
case-sensitive. Namespace tiers use `USER` or `ORGANIZATION`; repository
visibility uses `public` or `private`.

| Operator | Operand | Example | Meaning |
| --- | --- | --- | --- |
| `eq` | String `value` | `{attr: tier, op: eq, value: USER}` | Only the USER tier |
| `neq` | String `value` | `{attr: visibility, op: neq, value: private}` | A supplied visibility other than private |
| `in` | Nonempty string list `values` | `{attr: namespace, op: in, values: [acme, storefront]}` | Either listed namespace |
| `notIn` | Nonempty string list `values` | `{attr: namespace, op: notIn, values: [restricted, internal]}` | A supplied namespace outside the list |

Do not combine `value` and `values`. Quote strings that YAML could interpret as
booleans or numbers, for example `value: "true"`.

The baseline's `namespace-creator` ClusterRole uses a tier comparison:

```yaml
rules:
  - resources: [namespace]
    verbs: [create]
    when: [{attr: tier, op: eq, value: USER}]
```

Bind it with a `ClusterRoleBinding` because namespaces are cluster-scoped. This
rule grants creation of USER namespaces without restricting resource names.

Multiple comparisons can narrow a single grant. This ClusterRole rule permits
repository-content reads only in `acme` or `storefront`, and only for public
repositories:

```yaml
rules:
  - resources: [repository/contents]
    verbs: [read]
    when:
      - {attr: namespace, op: in, values: [acme, storefront]}
      - {attr: visibility, op: eq, value: public}
```

As an alternative, this rule excludes named namespaces and private repositories:

```yaml
rules:
  - resources: [repository/contents]
    verbs: [read]
    when:
      - {attr: namespace, op: notIn, values: [restricted, internal]}
      - {attr: visibility, op: neq, value: private}
```

The alternative permits any other supplied namespace and any other supplied
visibility, so it is broader than an explicit allow-list if new values are
introduced. A namespaced binding would further confine either rule to its
namespace. Another matching rule or binding can still grant broader access.

## Policy format and deployment

Use `apiVersion: rbac.authorization.gitstore.dev/v1beta1` and
`kind: RbacLocalConfiguration`. Unmatched requests are denied; `defaultDeny`
may be omitted or set to `true`. Legacy action lists and deny rules are rejected.
Resources and verbs are case-sensitive and must be supported pairs, such as
`repository`/`read` or `repository/contents`/`write`. Unknown fields, duplicate
keys, undefined role references and invalid pairs cause policy validation to
fail.

Deploy the reviewed file at the configured policy path on every API replica.
Keep the same policy revision across replicas and verify both allowed and
denied operations with the intended identities, including access to another
namespace and another resource name. A successful named read does not prove
that a list, watch or Git push is authorized; those operations require their
own grants.
