# CategoryTaxonomy Document Format

Categories in GitStore are managed through git push — not GraphQL mutations. A
`CategoryTaxonomy` file committed to the catalog repository is validated at push
time and stored via the post-receive admission pipeline.

## Document format

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: personal-computers
  namespace: my-store          # optional; defaults to repository ID
  labels:
    gitstore.dev/segment: electronics
spec:
  title: Personal Computers
  parentRef:
    apiVersion: catalog.gitstore.dev/v1beta1
    kind: CategoryTaxonomy
    name: electronics            # must exist or be in the same push
  media:
    - fileRef:
        name: category-hero
        kind: File
        optional: true           # push accepted even if file does not exist yet
---

Personal Computers is the category for desktop and laptop computers.
```

### Required fields

| Field | Description |
|---|---|
| `apiVersion` | Must be `catalog.gitstore.dev/v1beta1` |
| `kind` | Must be `CategoryTaxonomy` |
| `metadata.name` | Unique identifier within the namespace (URL-safe slug) |
| `spec.title` | Human-readable display title |

### Optional fields

| Field | Description |
|---|---|
| `metadata.namespace` | Defaults to the repository ID |
| `metadata.labels` | Key-value pairs for classification |
| `metadata.annotations` | Key-value pairs for tooling metadata |
| `spec.parentRef` | Reference to a parent `CategoryTaxonomy` by name |
| `spec.media` | List of media file references |

### Read-only fields (rejected at push time)

The following fields are system-managed and must **not** appear in committed files:
`metadata.uid`, `metadata.resourceVersion`, `metadata.generation`,
`metadata.creationTimestamp`, `metadata.revision`, `metadata.ownerReferences`,
`metadata.finalizers`, `status`.

## Hierarchy

Categories form a tree via `spec.parentRef`. The system computes a materialized
`ancestorPath` (slash-separated names from root to self) at admission time.

**Rules enforced at push time (pre-receive, blocking):**
- `spec.parentRef.name` must not equal `metadata.name` (self-parenting rejected)

**Rules enforced at admission time (post-receive, non-blocking):**
- If the parent is in the same push (co-creation), `ancestorPath` is set to
  `parentName/childName` and `ParentResolved=True`
- If the parent exists in the datastore, `ancestorPath` inherits
  `parent.ancestorPath/childName` and `ParentResolved=True`
- If the parent is not found anywhere, the category is stored as a tentative root
  (`ancestorPath=name`) with `ParentResolved=False`
- Intra-push mutual cycles (A→B, B→A) are stored with `Acyclic=False`

## Status conditions

The system writes a `status` blob to the datastore after each push. Conditions
follow the Kubernetes convention (`True`/`False`/`Unknown`).

| Condition | Meaning |
|---|---|
| `AdmissionAccepted` | Resource was stored by the post-receive pipeline |
| `ParentResolved` | `spec.parentRef` was found (in push or in DB) |
| `Acyclic` | No intra-push cycle detected involving this category |
| `Ready` | Controller has fully reconciled the resource (GH#244, deferred) |

## GraphQL queries

```graphql
# Look up a category by name
query {
  category(by: { name: "personal-computers" }) {
    id
    apiVersion
    kind
    metadata {
      name
      uid
      generation
      creationTimestamp
    }
    spec {
      title
      parentRef { name }
    }
    status {
      observedGeneration
      conditions { type status }
    }
    path
    depth
    parent { metadata { name } }
    children { metadata { name } }
  }
}

# List all categories (paginated)
query {
  categories(first: 20) {
    edges {
      node {
        metadata { name }
        spec { title }
        depth
        path
      }
    }
    pageInfo { hasNextPage endCursor }
  }
}
```

## Validation errors

| Error | Cause |
|---|---|
| `spec.title is required` | `spec.title` missing or empty |
| `metadata.name is required` | `metadata.name` missing |
| `spec.parentRef.name must not reference the category itself` | Self-parenting |
| `kind "X" is not a recognized catalog resource type` | Unknown `kind` value |
| `status is system-managed` | `status` key present in author file |
| `metadata.uid is read-only` | System field set in author file |

## File existence checks

`spec.media[].fileRef` entries reference `File` resources. Push-time validation
only checks that `fileRef.name` and `fileRef.kind` are present. Whether the
referenced `File` resource exists is checked by the controller reconciler
(GH#244, deferred from this spec).

Set `fileRef.optional: true` to prevent the controller from blocking the
`Ready` condition when the file is absent.
