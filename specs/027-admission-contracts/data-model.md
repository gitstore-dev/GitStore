# Data Model: Admission Control Contract (spec 027)

No new datastore tables or schema changes are introduced by this spec. All types defined here are in-memory Go types used within the admission chain.

---

## Core Types (`gitstore-api/internal/admission/`)

### `Operation`

```go
type Operation string

const (
    OperationCreate Operation = "CREATE"
    OperationUpdate Operation = "UPDATE"
    OperationDelete Operation = "DELETE"
)
```

### `Trigger`

```go
type Trigger string

const (
    TriggerGitPush    Trigger = "GIT_PUSH"
    TriggerGraphQL    Trigger = "GRAPHQL"
    TriggerCommitFile Trigger = "COMMIT_FILE" // defined; not wired in this spec
)
```

### `GitAdmissionContext`

Populated only when `Trigger == TriggerGitPush`.

| Field | Type | Description |
|---|---|---|
| `RepositoryID` | `string` | UUID of the git repository |
| `CommitSHA` | `string` | Full 40-character commit SHA |
| `RefName` | `string` | Fully-qualified ref, e.g. `refs/heads/main` |
| `Revision` | `string` | Human label, e.g. `main@sha1:abc123` |

### `AdmissionCondition`

Carries the result of a single named admission check. Emitted in `Allowed.Conditions`.

| Field | Type | Description |
|---|---|---|
| `Type` | `string` | Condition name matching `catalog.ConditionType` constants |
| `Status` | `bool` | `true` = condition satisfied |
| `Reason` | `string` | Optional machine-readable reason code |
| `Message` | `string` | Optional human-readable detail |

### `AdmissionRequest`

The input to the admission chain. Generic across all resource types and trigger paths.

| Field | Type | Description |
|---|---|---|
| `Object` | `any` | The decoded resource (concrete struct or `map[string]any`) |
| `OldObject` | `any` | Prior state; `nil` for creates |
| `Operation` | `Operation` | `CREATE`, `UPDATE`, or `DELETE` |
| `Kind` | `string` | Resource kind, e.g. `"ProductVariant"` |
| `Name` | `string` | Resource name (metadata.name) |
| `Namespace` | `string` | Namespace identifier |
| `Trigger` | `Trigger` | Code path that initiated admission |
| `GitContext` | `*GitAdmissionContext` | Git push details; `nil` for non-git triggers |
| `PushSet` | `[]AdmissionRequest` | Sibling resources in the same push; `nil` outside git-push |
| `Now` | `time.Time` | Admission timestamp set once for the entire batch |

### `AdmissionDecision` (sealed interface)

```go
type AdmissionDecision interface{ admissionDecision() }
```

Two variants:

#### `Allowed`

| Field | Type | Description |
|---|---|---|
| `Conditions` | `[]AdmissionCondition` | Named check results to incorporate into resource status |
| `Patches` | `[]json.RawMessage` | JSON Merge Patch fragments from mutating phase; `nil` for validating |

#### `Denied`

| Field | Type | Description |
|---|---|---|
| `Reason` | `string` | Human-readable rejection reason |
| `Field` | `string` | Optional dotted field path, e.g. `spec.productRef.name` |

Constructor helpers: `DecisionAllow(conditions ...AdmissionCondition) AdmissionDecision`, `DecisionDeny(reason, field string) AdmissionDecision`.

---

## Extension-Point Interfaces (`gitstore-api/internal/admission/`)

### `MutatingAdmissionPolicy`

```go
type MutatingAdmissionPolicy interface {
    Name() string
    Mutate(ctx context.Context, req AdmissionRequest) AdmissionDecision
}
```

### `ValidatingAdmissionPolicy`

```go
type ValidatingAdmissionPolicy interface {
    Name() string
    Validate(ctx context.Context, req AdmissionRequest) AdmissionDecision
}
```

### `MutatingAdmissionWebhook`

```go
type MutatingAdmissionWebhook interface {
    Name() string
    Mutate(ctx context.Context, req AdmissionRequest) AdmissionDecision
}
```

### `ValidatingAdmissionWebhook`

```go
type ValidatingAdmissionWebhook interface {
    Name() string
    Validate(ctx context.Context, req AdmissionRequest) AdmissionDecision
}
```

---

## `Chain` (`gitstore-api/internal/admission/`)

| Field | Type | Description |
|---|---|---|
| `mutatingPolicies` | `[]MutatingAdmissionPolicy` | Phase 1 — built-in mutating |
| `mutatingWebhooks` | `[]MutatingAdmissionWebhook` | Phase 2 — external mutating |
| `validatingPolicies` | `[]ValidatingAdmissionPolicy` | Phase 3 — built-in validating |
| `validatingWebhooks` | `[]ValidatingAdmissionWebhook` | Phase 4 — external validating |
| `log` | `*zap.Logger` | Structured logger for policy errors |

**Invariants**:
- A `Denied` from any phase short-circuits all remaining phases.
- A policy that panics is recovered; the resource is denied with `reason = "InternalError"` and the panic is logged.
- Empty registry for a phase executes in O(1) without iteration.
- Phase execution order is always 1 → 2 → 3 → 4, regardless of registration order of individual policies within a phase.

---

## Concrete Policies (`gitstore-api/internal/admission/catalog/`)

### `ProductValidatingPolicy`

Implements `ValidatingAdmissionPolicy`. Registered for `Kind == "Product"`. Stub — returns `Allowed{}` immediately in spec 027. Exists so that future checks are added by editing an existing file rather than creating new infrastructure.

| Dependency | Type | Purpose |
|---|---|---|
| `log` | `*zap.Logger` | Logging (reserved for future use) |

Conditions emitted: none in spec 027.

### `CollectionValidatingPolicy`

Implements `ValidatingAdmissionPolicy`. Registered for `Kind == "Collection"`. Stub — returns `Allowed{}` immediately in spec 027.

| Dependency | Type | Purpose |
|---|---|---|
| `log` | `*zap.Logger` | Logging (reserved for future use) |

Conditions emitted: none in spec 027.

### `ProductVariantValidatingPolicy`

Implements `ValidatingAdmissionPolicy`. Registered for `Kind == "ProductVariant"`.

| Dependency | Type | Purpose |
|---|---|---|
| `store` | `datastore.Datastore` | Look up parent product by name |
| `celEnv` | `*cel.Env` | CEL expression syntax check; `nil` = skip |
| `log` | `*zap.Logger` | Warn logging for non-fatal check results |

Conditions emitted in `Allowed.Conditions`:

| Condition | Satisfied when |
|---|---|
| `ProductResolved` | Parent product found in datastore |
| `OptionsAccepted` | All selected options are compatible with parent product spec |
| `PricingAccepted` | All CEL eligibility expressions are syntactically valid |

`validateSelectedOptions` and `celValidateExpressions` move here from `cataloggrpc/server.go` as **exported** functions.

### `CategoryTaxonomyValidatingPolicy`

Implements `ValidatingAdmissionPolicy`. Registered for `Kind == "CategoryTaxonomy"`.

| Dependency | Type | Purpose |
|---|---|---|
| `store` | `datastore.Datastore` | Look up out-of-push parents |
| `log` | `*zap.Logger` | Logging |

Conditions emitted in `Allowed.Conditions`:

| Condition | Satisfied when |
|---|---|
| `ParentResolved` | `parentRef.name` found in datastore or in `PushSet` |
| `Acyclic` | Resource is not part of an intra-push cycle |

`detectCycles` and `topoSortCategories` move here from `cataloggrpc/server.go` as exported functions.

---

## State Transitions

Admission in the post-receive phase is non-blocking. All resources are stored regardless of condition values. Status conditions transition from absent (before first admission) to a set of named `True`/`False` conditions after first admission. Subsequent pushes overwrite conditions on update.

```
[not yet admitted]
       │  git push accepted + AdmitResources called
       ▼
[admitted: conditions set]
       │  subsequent push
       ▼
[re-admitted: conditions updated]
```

Condition values are idempotent for the same input — pushing the same file twice sets the same condition values.
