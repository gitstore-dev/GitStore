# Data Model: Collection Frontmatter Integration Tests and Documentation

## Overview

This spec adds no new entities to the data model. It tests and documents the entities introduced in spec **022-collection-resource-contract**. The entities below are reproduced here as the authoritative reference for test fixture generation and documentation authoring.

---

## Entity: CollectionResource (frontmatter envelope)

Author-written document parsed from YAML frontmatter.

| Field | Type | Required | Validation |
|-------|------|----------|------------|
| `apiVersion` | string | yes | must equal `catalog.gitstore.dev/v1beta1` |
| `kind` | string | yes | must equal `Collection` |
| `metadata` | ObjectMeta | yes | see ObjectMeta below |
| `spec` | CollectionSpec | yes (struct present) | see CollectionSpec below |

---

## Entity: ObjectMeta (author-writable subset)

| Field | Type | Required | Validation |
|-------|------|----------|------------|
| `name` | string | yes | DNS label, unique within namespace |
| `namespace` | string | yes | must match repository's namespace |
| `labels` | map[string]string | no | key and value max 63 chars; key format `[prefix/]name` |
| `annotations` | map[string]string | no | no length restriction |

System-managed fields (`uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`, `ownerReferences`) are populated by the API after admission and MUST NOT appear in author-written documents.

---

## Entity: CollectionSpec

| Field | Type | Required | Validation |
|-------|------|----------|------------|
| `title` | string | **yes** | non-empty string |
| `selector` | LabelSelector | **no** | when absent, collection has zero members |
| `targetRef` | ObjectReference | no | when present, `kind` must be `"Product"` |
| `media` | []MediaDefinition | no | each entry validated via struct tags (`dive`) |

---

## Entity: LabelSelector

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `matchLabels` | map[string]string | no | AND-combined exact matches |
| `matchExpressions` | []LabelSelectorRequirement | no | AND-combined set-based constraints |

`matchLabels` and `matchExpressions` are combined with logical AND. An absent or empty selector yields zero members.

---

## Entity: LabelSelectorRequirement

| Field | Type | Required | Validation |
|-------|------|----------|------------|
| `key` | string | yes | non-empty |
| `operator` | string | yes | one of `In`, `NotIn`, `Exists`, `DoesNotExist` |
| `values` | []string | context-dependent | required (non-empty) for `In`/`NotIn`; must be empty for `Exists`/`DoesNotExist` |

---

## Entity: MediaDefinition

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `fileRef.name` | string | yes (when media entry present) | name of the File resource |
| `fileRef.kind` | string | no | defaults to `"File"` |
| `fileRef.optional` | bool | no | when `true`, absent file does not block admission |

---

## Entity: ObjectReference (targetRef)

| Field | Type | Required | Validation |
|-------|------|----------|------------|
| `kind` | string | yes (when targetRef present) | must be `"Product"` |
| `name` | string | no | reserved for future named-target scoping |

---

## Entity: CollectionStatus (system-managed, read-only)

Written by the admission pipeline after a successful push. Never author-writable.

| Field | Type | Notes |
|-------|------|-------|
| `observedGeneration` | int64 | generation of the spec this status corresponds to |
| `lastAppliedRevision` | string | git ref + SHA, e.g. `main@sha1:abc123` |
| `conditions` | []CollectionCondition | see conditions below |
| `resolved.memberCount` | int64 | cached hint at admission time; `collection.products` is authoritative |
| `resolved.media` | []ResolvedFileDefinition | resolved URLs for media entries |

### Condition types

| Type | Meaning | Normal `status` |
|------|---------|-----------------|
| `SelectorAccepted` | selector syntax is valid | `"True"` |
| `MembersResolved` | membership count was computed | `"True"` |
| `Ready` | all conditions are True | `"True"` |

### Condition reasons (non-exhaustive)

| Reason | Condition | Meaning |
|--------|-----------|---------|
| `SelectorValid` | SelectorAccepted | selector parsed and validated |
| `ProductsMatched` | MembersResolved | one or more products matched |
| `NoProductsMatched` | MembersResolved | selector present but zero products matched |
| `Reconciled` | Ready | all sub-conditions satisfied |
| `MediaNotResolved` | Ready `"False"` | non-optional media entry not resolvable |

---

## State transitions

```
author pushes collection document
        │
        ▼
pre-receive: ParseResource (kind=Collection)
        │
    ┌───┴──────────────────┐
    │ validation error?    │
    │ yes → push rejected  │
    │ no  ↓                │
    └──────────────────────┘
        │
post-receive: admitResources
        │
  ┌─────┴──────────────────────────────────────────┐
  │ CreateCollection / UpdateCollection (datastore) │
  │ evaluate selector against products              │
  │ write CollectionStatus                          │
  └─────────────────────────────────────────────────┘
        │
        ▼
GraphQL API: collection query returns full resource
```

---

## Test fixture shape

Every valid Collection test fixture must contain at minimum:

```yaml
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: <dns-label>
  namespace: <namespace>
spec:
  title: <non-empty string>
---
<optional markdown body>
```

To exercise selector membership, products with the target labels must be seeded into the catalog before the collection is pushed.
