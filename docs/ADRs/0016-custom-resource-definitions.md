# ADR 0016: Custom Resource Definitions

**Status**: Proposed

**Date**: 2026-09-30

**Audience**: extension authors, catalog operators, and API and controller-manager maintainers.

**Motivating resource**: `PricingTable` ([#253](https://github.com/gitstore-dev/GitStore/issues/253)).

## Context

Some commerce resources, such as pricing tables, workflow definitions
([037](../implementation/037-custom-commerce-workflows.md)) and marketplace-specific
configuration, matter to specific deployments but should not grow the core API
([resource storage: Core vs Extension/CRD](../resource-storage/README.md#core-vs-extensioncrd)).
GitStore needs a way to define new kinds that:

- are authored and reviewed like core resources;
- participate in admission, lifecycle hooks, watches and GraphQL;
- can't compromise the determinism of Git-backed admission.

Kubernetes CRDs are the obvious model. They describe their schema with `openAPIV3Schema`, and
since `apiextensions.k8s.io/v1` that schema must be **structural**
([CRDs](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/)).
Kubernetes chose OpenAPI for historical and ecosystem reasons that don't apply to GitStore:

- The Kubernetes API was already published as OpenAPI (`/openapi/v2`, `/openapi/v3`), which
  `kubectl explain`, client generation and server-side apply consume.
- CRD v1 went GA in 2019. OpenAPI only aligned with JSON Schema 2020-12 in 3.1, released in 2021,
  and the 3.0 dialect was already in use.
- The structural restrictions exist so that pruning, defaulting and merge strategies are
  well-defined. In effect they make the schema a type system rather than an arbitrary validator.

GitStore's API is GraphQL, not OpenAPI. Its authored files are YAML frontmatter. It needs
**offline, deterministic** validation in the admission path. JSON Schema 2020-12 is the current
standard for that, has mature validators in Go and Rust, and is what #253 specifies
(`spec.versions[].schema`). The lesson GitStore takes from Kubernetes is the **structural
constraint**, not the dialect.

## Decision

### 1. Definition format

A `CustomResourceDefinition` is a Git-backed Markdown document with YAML frontmatter and the
`apiextensions.gitstore.dev/v1alpha1` API version, as in #253:

```yaml
apiVersion: apiextensions.gitstore.dev/v1alpha1
kind: CustomResourceDefinition
metadata:
  name: pricingtables.storefront.gitstore.dev
spec:
  group: storefront.gitstore.dev
  scope: Namespaced
  storageGroup: GitBacked          # GitBacked | DatastoreOnly
  names:
    kind: PricingTable
    plural: pricingtables
    singular: pricingtable
    shortNames: [pt]
  versions:
  - name: v1alpha1
    served: true
    storage: true
    schema:
      $schema: https://schemas.gitstore.dev/crd/2020-12/structural   # GitStore profile (§2)
      type: object
      properties:
        spec: { ... }
    subresources:
      status: {}
    x-gitstore-graphql:
      expose: true
```

Rules:

- **Scope.** Only `Namespaced` is supported in v1alpha1. Cluster-scoped kinds remain core.
- **Group ownership.** Core groups (`*.gitstore.dev` groups owned by GitStore:
  `catalog`, `storage`, `admission`, `apiextensions`, …) are reserved. Installing a CRD in an
  unreserved group requires `customResourceDefinition.create`
  ([ADR 0010](0010-authorization-model.md)) in the defining namespace. A CRD is visible only in
  namespaces that have a `CustomResourceDefinitionBinding`, which is the operator control over
  which namespaces get a kind.
- **Storage group.** `storageGroup` is immutable after creation. It selects the hook matrix row
  in [ADR 0015 §2](0015-resource-lifecycle-hooks.md#2-storage-group-matrix). A `GitBacked` kind
  therefore can't have mutating hooks, and its instances are Markdown documents like core
  resources. `DatastoreOnly` kinds are written through GraphQL. LFS payloads are referenced via
  `File`, never embedded, and transient kinds are not definable in v1alpha1.

### 2. The GitStore structural profile

`spec.versions[].schema` is JSON Schema 2020-12, validated against a GitStore meta-schema that
declares a `https://schemas.gitstore.dev/vocab/structural` vocabulary. A schema that doesn't
conform to the profile is rejected at CRD admission, with a path to the offending node.

| Rule                         | Constraint                                                                                                                                                                                             | Why                                                                               |
|------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|-----------------------------------------------------------------------------------|
| Determinable type            | Every node has a single `type` (or `const`/`enum` of one type). `x-gitstore-preserve-unknown-fields: true` marks a free-form object explicitly                                                         | GraphQL types, datastore projections and CEL typing need one shape per path       |
| Local references only        | `$ref` only to `#/$defs/...`. No remote `$ref`, `$id`-based resolution, `$dynamicRef` or `$recursiveRef`. Validators resolve offline from the bundled document                                         | Admission never fetches schemas over the network                                  |
| Shape-preserving combinators | `allOf`/`anyOf`/`oneOf`/`not` may only add constraints to a node whose type is already declared. `if`/`then`/`else`, `dependentSchemas` and `patternProperties` that introduce new shapes are rejected | Validation-only combinators keep the structure static                             |
| Closed objects by default    | Objects without `x-gitstore-preserve-unknown-fields` are treated as `unevaluatedProperties: false`                                                                                                     | Typos fail admission instead of being silently ignored                            |
| Defaults                     | `default` is allowed on properties. It is applied **only to the hydrated read model**, deterministically, pinned to the CRD generation that admitted the instance. It is **never written to Git**      | Consistent with ADR 0015's Git-backed mutating ban                                |
| `format`                     | Treated as an **assertion** for an allowlist (`date-time`, `date`, `uri`, `email`, `uuid`, `hostname`, `ipv4`, `ipv6`, `regex`). Other formats are rejected                                            | 2020-12 treats `format` as annotation-only by default. GitStore needs it enforced |
| Bounds                       | Strings need `maxLength` and arrays need `maxItems` wherever an `x-gitstore-validations` rule reads them                                                                                               | Makes CEL cost estimation possible                                                |
| YAML input                   | Instances are decoded as YAML 1.2 core schema: `yes`/`no`/`on`/`off` are strings, no custom tags, no anchors that cross documents, and duplicate keys are rejected                                     | The same bytes produce the same JSON on every validator                           |

Extension keywords, all prefixed `x-gitstore-`:

- `x-gitstore-validations: [{rule, message, reason, fieldPath}]` holds CEL rules for cross-field
  checks, in the same style as Kubernetes `x-kubernetes-validations`. Rules run in the API
  admission `Chain` as validating policies.
- `x-gitstore-list-type: atomic|set|map` and `x-gitstore-list-map-keys: [...]` define list
  identity for diffs, watches and annotations.
- `x-gitstore-index: true` requests a datastore projection for list filtering. The number of
  indexes per kind is bounded.
- `x-gitstore-graphql` controls field exposure and naming in the generated GraphQL schema.
- `x-gitstore-reference: {kind, apiVersion}` marks a field as an object reference. The reference
  participates in ownerReferences and readiness, and must resolve to a core or CRD kind.

### 3. Versions and conversion

- **Exactly one version has `storage: true`.** Any number may be `served`. This is the Kubernetes
  rule ([versioning](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definition-versioning/)).
- For Git-backed kinds, the **authored `apiVersion` is what is stored in Git**, and GitStore never
  rewrites it. The hydrated read model is converted to the storage version. Reads of another
  served version convert on read.
- Conversion is either `None`, which is allowed only when schemas are identical apart from the
  `apiVersion`, or a WASM **Function** with the `gitstore:apiextensions/convert@1` target
  ([ADR 0015 §7](0015-resource-lifecycle-hooks.md#7-functions-wasm)). Conversion is pure and
  deterministic, runs in-process, and needs round-trip tests. Conversion webhooks are not
  supported.
- Removing a served version requires that no admitted instance on any admission branch still uses
  it. The API reports per-version counts in `CustomResourceDefinition.status.storedVersions`.
- Breaking changes to a stored version's schema are rejected. The only allowed changes are adding
  optional fields and relaxing constraints. Anything else needs a new version.

### 4. Status, conditions and finalizers

- `subresources.status: {}` gives the kind a system-managed `status`, which authors must not set
  (the same rule as core resources). Status updates go through the controller status API and don't
  advance `generation`.
- Status follows Kubernetes API conventions
  ([api-conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md)):
  `conditions[]` with `type`, `status`, `reason`, `message`, `lastTransitionTime` and
  `observedGeneration`. `AdmissionAccepted` and `Ready` are always present. `Ready` is `True` only
  when every declared reference is ready and every gate registered for `Ready` is satisfied.
- Finalizers work as in core resources: a deletion timestamp is set, finalizers are removed one by
  one, and the object is deleted when none remain. Extensions register finalizers through
  `LifecycleGate` (ADR 0015 §5). Deleting a CRD is blocked while instances exist
  (`blockOwnerDeletion`).

### 5. Lifecycle participation

CRD instances automatically get:

- admission (structural schema, then `x-gitstore-validations`, then bound policies and webhooks);
- a watch journal source, so they get `watchResources` and event subscriptions. Sources are
  registered in the shared catalog CDC source registry (kind → authoritative table → payload
  decoder) rather than per-kind runners; the generic CRD instance table is future schema work;
- GraphQL list/get/watch types generated from the served versions;
- authorization actions in the [ADR 0010](0010-authorization-model.md) grammar.
  - The `<kind>` slug is `names.kind` in lower camelCase (`PricingTable` → `pricingTable`).
  - The verbs are `read`, `list` and `watch`, plus `create`/`update`/`delete` for datastore-only
    kinds. For Git-backed kinds those writes happen only through push admission.
  - A status subresource adds `<kind>.status.write` for its controller's service account.
  - The slug must be unique across core kinds and installed CRDs. A CRD whose slug collides is
    rejected at admission.

A CRD can opt into catalog release selection with `x-gitstore-releaseable: true`. Its instances
can then appear in a `CatalogRelease` selection and in Storefront snapshots
([ADR 0014](0014-catalog-release-and-publication.md)). Without the opt-in, instances are never
public.

### 6. Controllers for custom kinds

GitStore does not host arbitrary controllers. A CRD's behaviour comes from:

- declarative validations, defaults and references (§2);
- Functions for pure computations;
- gates for transitions;
- event subscriptions, which let an external controller act on changes and write `status` back
  through the status API using a service account
  ([021](../implementation/021-controller_service_account_auth.md)).

## Consequences

Positive:

- One schema dialect for authors, validators and tooling. Standard 2020-12 tooling works because
  the profile is a subset of 2020-12.
- Deterministic, offline validation in both the Go API and any Rust pre-check.
- CRDs get the full lifecycle (admission, hooks, watches, release) without per-kind code.

Negative:

- Authors coming from Kubernetes have to translate `openAPIV3Schema` to 2020-12, and learn a
  profile that rejects some valid 2020-12 constructs.
- Generated GraphQL types make schema evolution a public API concern. The breaking-change rule
  (§3) is strict for that reason.
- WASM conversion requires every multi-version CRD to ship and test a module.

## Cross-references

- [ADR 0010](0010-authorization-model.md): CRD actions.
- [ADR 0014](0014-catalog-release-and-publication.md): releaseable CRDs.
- [ADR 0015](0015-resource-lifecycle-hooks.md): hook matrix, Functions and gates.
- [039](../implementation/039-resource-lifecycle-hooks.md): registration contracts.
- [#253](https://github.com/gitstore-dev/GitStore/issues/253): `PricingTable`, the first
  candidate CRD.

## Alternatives considered

### OpenAPI 3.0 `openAPIV3Schema` (Kubernetes-compatible)

Rejected. GitStore publishes no OpenAPI document. The 3.0 dialect diverges from JSON Schema, for
example in `nullable` and in its limited `$ref` handling. Kubernetes compatibility would be
superficial, because GitStore CRDs are Markdown documents with different storage semantics.

### Unrestricted JSON Schema 2020-12

Rejected. Conditional shapes and dynamic references make GraphQL generation, datastore
projections and CEL typing ambiguous, and remote references make admission depend on the network.

### Conversion webhooks

Rejected. They would add a synchronous remote dependency to every read of a non-storage version.
WASM conversion runs in-process with the same determinism guarantees.

### Writing defaults back into Git

Rejected. See ADR 0015 §2.
