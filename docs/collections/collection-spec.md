# Collection Spec Reference

**API Version**: `catalog.gitstore.dev/v1beta1`  
**Kind**: `Collection`

A Collection resource is a Markdown file with YAML frontmatter pushed to a GitStore repository. The frontmatter declares a declarative label selector that determines which products belong to the collection; the Markdown body is a free-form description.

---

## Envelope Fields

| Field        | Type   | Required      | Constraint                                |
|--------------|--------|---------------|-------------------------------------------|
| `apiVersion` | string | yes           | Must be `catalog.gitstore.dev/v1beta1`    |
| `kind`       | string | yes           | Must be `Collection` (case-sensitive)     |
| `metadata`   | object | yes           | See Metadata Fields                       |
| `spec`       | object | yes           | See Spec Fields                           |
| `status`     | —      | **forbidden** | System-managed; presence causes rejection |

---

## Metadata Fields

| Field                  | Type              | Required | Constraint                                                             |
|------------------------|-------------------|----------|------------------------------------------------------------------------|
| `metadata.name`        | string            | yes      | DNS subdomain format; unique within namespace                          |
| `metadata.namespace`   | string            | no       | Optional. Inferred from the repository's owning namespace when omitted |
| `metadata.labels`      | map[string]string | no       | Key prefix ≤ 253 chars; key name ≤ 63 chars; value ≤ 63 chars          |
| `metadata.annotations` | map[string]string | no       | No length restriction                                                  |

System-managed metadata fields (`uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`, `ownerReferences`) are populated by the system after admission. They must **not** appear in author-written documents.

## Lifecycle

GitStore identifies a collection by `apiVersion`, `kind`, resolved namespace, and `metadata.name`; the file path is provenance only. Moving a collection file preserves `metadata.uid`. Changing `spec` or the Markdown body increments `metadata.generation` and `metadata.resourceVersion`. Path-only moves and label/annotation-only edits preserve `generation` and increment `resourceVersion`. Deleting the file removes the collection from GraphQL reads after post-receive admission; adding the same identity again later creates a new UID.

---

## Spec Fields

| Field            | Type   | Required | Constraint                                                  |
|------------------|--------|----------|-------------------------------------------------------------|
| `spec.title`     | string | **yes**  | Non-empty string; human-readable display name               |
| `spec.selector`  | object | no       | `LabelSelector`; when absent, collection has zero members   |
| `spec.targetRef` | object | no       | `ObjectReference`; when present, `kind` must be `"Product"` |
| `spec.media`     | array  | no       | List of `MediaDefinition` entries                           |

---

## LabelSelector

A `LabelSelector` determines collection membership by matching product labels. `matchLabels` and `matchExpressions` are combined with logical AND. An absent or empty selector yields zero members.

### matchLabels

Exact key-value pairs that must all be present on a product's labels.

```yaml
selector:
  matchLabels:
    gitstore.dev/brand: apple
    gitstore.dev/product-type: laptop
```

A product is included only if **all** listed key-value pairs match.

### matchExpressions

Set-based label requirements. All entries must be satisfied.

| Operator       | Meaning                               | `values` required? |
|----------------|---------------------------------------|--------------------|
| `In`           | Label value must be in the list       | yes (≥ 1 value)    |
| `NotIn`        | Label value must not be in the list   | yes (≥ 1 value)    |
| `Exists`       | Label key must be present (any value) | no (must be empty) |
| `DoesNotExist` | Label key must be absent              | no (must be empty) |

#### In

```yaml
selector:
  matchExpressions:
  - key: gitstore.dev/product-type
    operator: In
    values:
    - laptop
    - macbook
```

#### NotIn

```yaml
selector:
  matchExpressions:
  - key: gitstore.dev/brand
    operator: NotIn
    values:
    - samsung
```

#### Exists

```yaml
selector:
  matchExpressions:
  - key: gitstore.dev/featured
    operator: Exists
```

#### DoesNotExist

```yaml
selector:
  matchExpressions:
  - key: gitstore.dev/discontinued
    operator: DoesNotExist
```

---

## targetRef

An optional reference that constrains which resource kind can be a member of the collection. Only `kind: Product` is currently supported.

```yaml
spec:
  targetRef:
    kind: Product
```

When omitted, `kind: Product` is the implied default.

---

## Media Fields

| Field                       | Type   | Required | Constraint                                                 |
|-----------------------------|--------|----------|------------------------------------------------------------|
| `media[*].fileRef.name`     | string | yes      | Name of the `File` resource                                |
| `media[*].fileRef.kind`     | string | no       | Defaults to `"File"`                                       |
| `media[*].fileRef.optional` | bool   | no       | When `true`, admission succeeds even if the file is absent |

---

## Status Fields (system-managed, read-only)

Populated by the admission pipeline after a successful push. Never author-writable.

| Field                         | Type   | Description                                                                    |
|-------------------------------|--------|--------------------------------------------------------------------------------|
| `status.observedGeneration`   | int    | Generation of the spec this status reflects                                    |
| `status.lastAppliedRevision`  | string | Git ref + SHA, e.g. `main@sha1:abc123`                                         |
| `status.conditions`           | array  | See Condition Types below                                                      |
| `status.resolved.memberCount` | int    | Cached product count at admission time; `collection.products` is authoritative |
| `status.resolved.media`       | array  | Resolved file URLs for each media entry                                        |

### Condition Types

| Type               | Meaning                       | Normal status |
|--------------------|-------------------------------|---------------|
| `SelectorAccepted` | Selector syntax is valid      | `"True"`      |
| `MembersResolved`  | Membership count was computed | `"True"`      |
| `Ready`            | All sub-conditions are True   | `"True"`      |

### Condition Reasons

| Reason              | Condition        | Meaning                                                   |
|---------------------|------------------|-----------------------------------------------------------|
| `SelectorValid`     | SelectorAccepted | Selector parsed successfully                              |
| `ProductsMatched`   | MembersResolved  | One or more products matched the selector                 |
| `NoProductsMatched` | MembersResolved  | Selector present but zero products matched (not an error) |
| `Reconciled`        | Ready            | All sub-conditions satisfied                              |
| `MediaNotResolved`  | Ready `"False"`  | A non-optional media file could not be resolved           |

---

## Validation Errors

All validation errors are returned as plain text on the `git push` stderr stream. Tests should use substring matching rather than exact string comparison.

| Condition                     | Error message pattern                                                                                 |
|-------------------------------|-------------------------------------------------------------------------------------------------------|
| Wrong `apiVersion`            | `validate: apiVersion must be "catalog.gitstore.dev/v1beta1"`                                         |
| Wrong `kind`                  | `validate: kind must be "Collection"`                                                                 |
| Unrecognized `kind`           | `validate: kind %q is not a recognized catalog resource type`                                         |
| Missing `metadata.name`       | `validate: metadata.name is required`                                                                 |
| Missing `spec.title`          | `validate: spec.title is required`                                                                    |
| Invalid `targetRef.kind`      | `validate: spec.targetRef.kind must be "Product", got "<value>"`                                      |
| `In`/`NotIn` with no values   | `validate: spec.selector.matchExpressions[N]: operator "In" requires at least one value`              |
| Invalid operator              | `validate: spec.selector.matchExpressions[N].operator must be one of In, NotIn, Exists, DoesNotExist` |
| `status` key present          | `validate: "status" is a system-managed field and must not be set by the author`                      |
| System-managed metadata field | `validate: metadata.<field> is a system-managed field and must not be set by the author`              |

---

## Examples

### Minimal (title only, no selector)

A valid collection with no members. Useful as a placeholder or to reserve a collection name.

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: placeholder-collection
  namespace: acme-store
spec:
  title: Placeholder Collection
---

This collection has no selector and will always have zero members.
```

### Full Collection with Selector and Media

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: apple-laptops
  namespace: acme-store
  labels:
    gitstore.dev/featured: "true"
spec:
  title: Apple Laptops
  selector:
    matchLabels:
      gitstore.dev/brand: apple
    matchExpressions:
    - key: gitstore.dev/product-type
      operator: In
      values:
      - laptop
      - macbook
  media:
  - fileRef:
      name: apple-laptops-hero
      kind: File
      optional: true
---

Apple laptop products across MacBook Air and MacBook Pro families.
```

### Zero-Member Selector (no `spec.selector`)

Omitting `spec.selector` is valid. The collection is admitted with `status.resolved.memberCount: 0`.

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: coming-soon
  namespace: acme-store
spec:
  title: Coming Soon
---

Products launching next season.
```

---

## Querying Collections via GraphQL

### Get a single collection

```graphql
query {
  collection(by: { namespacePath: { namespace: "acme-store", name: "apple-laptops" } }) {
    metadata {
      name
      uid
      generation
      revision
    }
    spec {
      title
      selector {
        matchLabels { key value }
        matchExpressions { key operator values }
      }
    }
    status {
      observedGeneration
      lastAppliedRevision
      conditions { type status reason message }
      resolved { memberCount }
    }
    products(first: 20) {
      edges {
        node {
          metadata { name }
          spec { title }
        }
      }
      pageInfo { hasNextPage endCursor }
      totalCount
    }
  }
}
```

### List all collections (paginated)

```graphql
query {
  collections(first: 10) {
    edges {
      node {
        metadata { name namespace }
        spec { title }
        status { resolved { memberCount } }
      }
      cursor
    }
    pageInfo { hasNextPage endCursor }
    totalCount
  }
}
```

---

## Push a Collection

Place the file anywhere in the repository. The `collections/` sub-directory is conventional but not required.

```bash
mkdir -p collections
cat > collections/apple-laptops.md << 'EOF'
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: apple-laptops
  namespace: acme-store
spec:
  title: Apple Laptops
  selector:
    matchLabels:
      gitstore.dev/brand: apple
---

Apple laptop products.
EOF

git add collections/apple-laptops.md
git commit -m "feat(catalog): add apple-laptops collection"
git push origin main
```
