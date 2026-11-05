# ADR 0016: Custom Resource Definitions

**Status**: Proposed

**Date**: 2026-09-30

**Audience**: extension authors, catalog operators, and API and controller-manager maintainers.

**Motivating resource**: `PricingTable` ([#253](https://github.com/gitstore-dev/GitStore/issues/253)).

## Context

Some commerce resources, such as pricing tables, workflow definitions
([037](../implementation/037-custom-commerce-workflows.md)) and marketplace-specific
configuration, matter to specific deployments but should not grow the core API
([resource storage: Core vs Extension/CRD](../storage-groups/README.md#resource-catalogue-scope)).
GitStore needs a way to define new kinds that:

- are authored and reviewed like core resources;
- participate in admission, lifecycle hooks, watches and GraphQL;
- can't compromise the determinism of Git-backed admission.

GitStore's API is GraphQL and its authored files are YAML frontmatter. It needs
offline, deterministic validation with a static field shape for typed policies,
discovery and projections. JSON Schema 2020-12 supplies the schema dialect;
GitStore's structural profile restricts it to predictable, bounded shapes.

A definition is not an installed application. An app may introduce several
definitions, reuse existing kinds or have none. [ADR 0019](0019-app-installation-and-platform-capabilities.md)
defines package composition, capability approval and installation. This ADR
defines kind-level contracts. Both remain proposed; examples do not advertise
implemented CRD APIs or runtime target support.

## Decision

### 1. Definition format

A `CustomResourceDefinition` is a Git-backed Markdown document with YAML frontmatter and the
`apiextensions.gitstore.dev/v1alpha1` API version, as in #253:

```yaml
apiVersion: apiextensions.gitstore.dev/v1alpha1
kind: CustomResourceDefinition
metadata:
  name: pricingtables.storefront.acme.example
spec:
  group: storefront.acme.example
  scope: Namespaced
  storageGroup: GitBacked          # GitBacked | DatastoreOnly
  names:
    kind: PricingTable
    plural: pricingtables
    singular: pricingtable             # optional discovery alias
    shortNames: [pt]                   # optional CLI aliases
  capabilities:
    catalog:
      adminVisible: true
      releaseEligible: true
      publicProjection: SchemaFields
  versions:
  - name: v1alpha1
    served: true
    storage: true
    schema:
      $schema: https://schemas.gitstore.dev/crd/2020-12/structural   # GitStore profile (§2)
      type: object
      required: [spec]
      properties:
        spec:
          type: object
          required: [title]
          properties:
            title: {type: string, minLength: 1, maxLength: 200}
    subresources:
      status: {}
    publicProjection:
      fields: [spec.title]
```

Rules:

- **Envelope.** The shared platform envelope validates `apiVersion`, `kind` and
  metadata separately. The version schema describes the kind-specific payload;
  the host composes it with reserved envelope fields before enforcing closed
  objects. Definitions cannot redefine identity, revision or system status
  authority. The small example above illustrates a minimal payload, not the
  complete PricingTable presentation contract.
- **Scope.** Only `Namespaced` is supported in v1alpha1. Cluster-scoped kinds remain core.
- **Names.** `kind` is the canonical identity and `plural` is the stable discovery
  collection name used in the definition name. `singular` and `shortNames` are
  optional presentation/CLI aliases; they never define REST routes, GraphQL
  fields or authorization actions. Aliases must be unambiguous in the visible
  namespace; clients require an explicit kind selector on ambiguity and must
  not guess by pluralization. Kind/collection identity is immutable after use.
- **Group ownership.** Core groups (`*.gitstore.dev` groups owned by GitStore:
  `catalog`, `storage`, `admission`, `apiextensions`, …) are reserved. Installing a CRD in an
  unreserved group requires `customResourceDefinition.create`
  ([ADR 0010](0010-authorization-model.md)) in the defining namespace. A CRD is visible only in
  namespaces that have a `CustomResourceDefinitionBinding`, which is the operator control over
  which namespaces get a kind.
- **Storage group.** `storageGroup` is immutable after creation. It selects the hook matrix row
  in [ADR 0015 §2](0015-resource-lifecycle-hooks.md#2-storage-group-matrix). A `GitBacked` kind
  therefore can't have mutating hooks, and its instances are Markdown documents like core
  resources. Resources can enter through Git or authorized GraphQL mutations.
  GraphQL writes to `GitBacked` kinds route through the Git service and return
  admission outcome/correlation; they never directly update hydrated spec rows.
  Git admission has bounded pre-receive checks and asynchronous post-receive
  admission; a ref move alone is not accepted read-model state. `DatastoreOnly`
  kinds use the datastore write path and are rejected on Git ingress. LFS
  payloads are referenced via `File`, never embedded, and transient kinds are
  not definable in v1alpha1.

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
  checks. Rules run in the API
  admission `Chain` as validating policies.
- `x-gitstore-list-type: atomic|set|map` and `x-gitstore-list-map-keys: [...]` define list
  identity for diffs, watches and annotations.
- `x-gitstore-index: true` requests a datastore projection for list filtering. The number of
  indexes per kind is bounded.
- `x-gitstore-graphql` may constrain field exposure in discovery. It does not
  install remote resolvers or require a dynamically generated schema (§7).
- `x-gitstore-reference: {kind, apiVersion}` marks a field as an object reference. The reference
  participates in ownerReferences and readiness, and must resolve to a core or CRD kind.

### 3. Versions and conversion

- **Exactly one version has `storage: true`.** Any number may be `served`.
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
- Status uses the shared resource condition convention:
  `conditions[]` with `type`, `status`, `reason`, `message`, `lastTransitionTime` and
  `observedGeneration`. `AdmissionAccepted` and `Ready` are always present. `Ready` is `True` only
  when the responsible controller's own readiness facts, every declared
  reference and every required `Ready` gate are satisfied for current material
  inputs. Absence of gates does not make an unreconciled resource ready.
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
- generic GraphQL list/get/watch/status operations with explicit discovery
  mappings; dynamically generated types or schema stitching are not prerequisites (§7);
- authorization actions in the [ADR 0010](0010-authorization-model.md) grammar.
  - The `<kind>` slug is `names.kind` in lower camelCase (`PricingTable` → `pricingTable`).
  - The verbs are `read`, `list`, `watch`, `create`, `update` and `delete` for
    writable kinds. Git-backed writes use Git admission regardless of the
    client ingress path; push permission does not replace per-kind authority.
  - A status subresource makes `<kind>.status.write` available for an explicit
    controller ServiceAccount grant; declaration grants no authority itself.
  - The slug must be unique across core kinds and installed CRDs. A CRD whose slug collides is
    rejected at admission.

Automatic action availability is contingent on the proposed bounded installed-kind
catalog extension in [ADR 0010 §15](0010-authorization-model.md#15-proposed-app-and-custom-kind-catalog-extension).
The implemented finite catalog does not accept arbitrary runtime action strings.

`spec.capabilities.catalog` separates three decisions:

| Field | Default | Meaning |
| --- | --- | --- |
| `adminVisible` | `false` | Eligible for schema-driven catalog administration; still requires read/list authority |
| `releaseEligible` | `false` | Instances may be explicitly selected by `CatalogRelease.spec.selection.resourceRefs` |
| `publicProjection` | `None` | `SchemaFields` permits a reviewed public projection of the selected served-version schema |

`SchemaFields` requires `releaseEligible: true` and admission-time rejection of
secret/provider credential fields in the public shape. Public serializers must
use an explicit bounded allowlist; references to private resources, raw status,
internal IDs and extension credentials are excluded. Publication is still an
independent, authorized operation. A private configuration resource can drive a
public search provider without exposing the configuration itself.

Each served version declares `publicProjection.fields`: exact schema leaf
paths selected for public serialization (array elements use `[]` in the path).
No object-subtree or recursive wildcard selection is allowed; newly added
fields are private until explicitly selected. Body projection is a separate
`publicProjection.body: true` opt-in and uses the resolved public body IR.
Unknown, sensitive or unbounded paths fail definition admission. Metadata is
limited to the platform's reviewed public identity fields; status is never
implicitly copied. Serializer and selected-path digests are pinned at release.

This proposed structured contract replaces this ADR's earlier
`x-gitstore-releaseable: true` shorthand. Because CRDs have not shipped, no
dual spelling or compatibility parser is required. A runtime boolean never
substitutes for public schema review or a publication. See
[ADR 0014 §2a](0014-catalog-release-and-publication.md#2a-custom-resource-selection-and-public-projection).

### 6. Controllers for custom kinds

GitStore does not host arbitrary controllers. A CRD's behaviour comes from:

- declarative validations, defaults and references (§2);
- Functions for pure computations;
- gates for transitions;
- event subscriptions, which let an external controller act on changes and write `status` back
  through the status API using a service account
  ([021](../implementation/021-controller_service_account_auth.md)).

Merchant or authorized automation writes desired state; watches and webhook
delivery wake reconcilers. Secondary catalog/publication changes do not require
synthetic CR updates. Request-time search and recommendation output belongs in
runtime serving state, not authored CR instances. The three concrete capability
examples are in [ADR 0019 §§7–9](0019-app-installation-and-platform-capabilities.md#7-use-case-search-and-recommendation).

### 7. API exposure and discovery

The first CRD API contract uses generic resource operations parameterized by
`apiVersion`, `kind`, namespace and identity. Discovery must advertise exact
callable GraphQL operation/argument mappings, schema digest/version, supported
CRUD/watch/status/transition operations, authorization slug, storage group,
scope, concurrency preconditions and optional aliases. Unsupported operations
are omitted or explicitly unavailable, never inferred from schema existence.

AuthZ filters discovery to visible definitions/bindings. A discovery result is
not a permission grant; each invocation rechecks current identity and policy.
Lists are paginated and watches resumable through the journal. Input validation,
query complexity, field exposure, body/status bounds and namespace confinement
apply to generic operations just as to typed core fields. Return a stable typed
unsupported-kind/version/operation error rather than guessing a resolver.

Generated typed fields can be a separately versioned convenience after a schema
lifecycle contract is reviewed. App-owned remote schema stitching is not part
of CRD registration. Function input selection can use the advertised generic
mapping and host target schema; it does not depend on remote schema composition.

### 8. Declared custom transitions

Core transitions `Ready`, `Delete` and, for eligible kinds, `ReleaseEligible`
have fixed platform semantics. A CRD can additionally declare named operations
under `spec.lifecycle.transitions`. Names are stable, bounded identifiers,
unique per kind; they cannot shadow core transitions. A declaration requires a
host-supported executor contract and a cataloged action. It does not install
code or turn a status field into an enforcing API.

```yaml
spec:
  lifecycle:
    transitions:
    - name: Activate
      action: searchIndex.activate
      executor:
        type: Workflow
        definitionRef:
          name: staged-index-activation
        command: Activate
```

This is a fragment of a `SearchIndex` definition. The workflow definition must
declare the subject and typed command under the proposed custom-resource scope
in [037](../implementation/037-custom-commerce-workflows.md#custom-resource-transition-adapters).
The executor is the API workflow command handler, not arbitrary external code.
The definition and its schema/command version are pinned on each execution.
Definition resolution is a prepared dependency; instances remain unavailable
for transition dispatch until the adapter is active. Unknown executors fail
closed. No CRD-defined transition is needed for normal reconciliation.

The action must use the kind slug and a registered verb; additional segments
require an explicit subresource contract. Execution goes through the bounded,
authorized, idempotent command and evidence checks in
[039 §11](../implementation/039-resource-lifecycle-hooks.md#11-transition-and-evidence-contracts).
A LifecycleGate can constrain the operation only after this declaration is
validated. A FunctionBinding or EventSubscription cannot declare an operation.
Removing or changing a transition with live executions requires retirement and
retention of the pinned definition; edits never reinterpret old executions.

## Consequences

Positive:

- One schema dialect for authors, validators and tooling. Standard 2020-12 tooling works because
  the profile is a subset of 2020-12.
- Deterministic, offline validation in both the Go API and any Rust pre-check.
- CRDs get the full lifecycle (admission, hooks, watches, release) without per-kind code.

Negative:

- Authors must learn a structural profile that rejects some valid 2020-12 constructs.
- Schema evolution affects generic clients, policies and projections even
  without generated GraphQL fields. The breaking-change rule (§3) remains strict.
- WASM conversion requires every multi-version CRD to ship and test a module.

## Cross-references

- [ADR 0010](0010-authorization-model.md): CRD actions.
- [ADR 0014](0014-catalog-release-and-publication.md): releaseable CRDs.
- [ADR 0015](0015-resource-lifecycle-hooks.md): hook matrix, Functions and gates.
- [039](../implementation/039-resource-lifecycle-hooks.md): registration contracts.
- [#253](https://github.com/gitstore-dev/GitStore/issues/253): `PricingTable`, the first
  candidate CRD.

## Alternatives considered

### OpenAPI 3.0 schema dialect

Rejected. GitStore publishes no OpenAPI document. The 3.0 dialect diverges from JSON Schema, for
example in `nullable` and in its limited `$ref` handling. GitStore CRDs use
Markdown documents and require deterministic offline validation.

### Unrestricted JSON Schema 2020-12

Rejected. Conditional shapes and dynamic references make GraphQL generation, datastore
projections and CEL typing ambiguous, and remote references make admission depend on the network.

### Conversion webhooks

Rejected. They would add a synchronous remote dependency to every read of a non-storage version.
WASM conversion runs in-process with the same determinism guarantees.

### Writing defaults back into Git

Rejected. See ADR 0015 §2.
