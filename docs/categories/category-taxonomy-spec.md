# CategoryTaxonomy Spec Reference

**API Version**: `catalog.gitstore.dev/v1beta1`  
**Kind**: `CategoryTaxonomy`

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
  namespace: my-store          # optional; inferred from the repository's owning namespace
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
| `metadata.namespace` | Optional. Inferred from the repository's owning namespace (see [Namespace inference](#namespace-inference)) |
| `metadata.labels` | Key-value pairs for classification |
| `metadata.annotations` | Key-value pairs for tooling metadata |
| `spec.parentRef` | Reference to a parent `CategoryTaxonomy` by name |
| `spec.media` | List of media file references |

### Read-only fields (rejected at push time)

The following fields are system-managed and must **not** appear in committed files:
`metadata.uid`, `metadata.resourceVersion`, `metadata.generation`,
`metadata.creationTimestamp`, `metadata.revision`, `metadata.ownerReferences`,
`metadata.finalizers`, `status`.

## Namespace inference

All current Git-backed catalog resources (`Product`, `ProductVariant`, `CategoryTaxonomy`, `Collection`) treat `metadata.namespace` as
optional in the committed file. When omitted, the namespace is resolved at admission
time from the push context:

1. The post-receive pipeline receives the repository UUID (`repositoryId`) from the
   git hook.
2. The admission server calls `GetRepository(repositoryId)` to retrieve the repository
   record.
3. It then calls `GetNamespace(repository.namespaceID)` to retrieve the owning namespace.
4. The namespace `identifier` (a human-readable slug such as `my-store`) is used as the
   stored namespace for the resource.

**The raw repository UUID is never stored as the namespace.** If the repository or its
namespace cannot be resolved, the push admission is aborted and no resources are stored.

Even when `metadata.namespace` is present in the file it is still validated to match the
inferred namespace. The field exists to allow multiple repositories within the same
namespace to push resources that cross-reference each other by name (product and category
names are unique per namespace, not per repository).

## Lifecycle

GitStore identifies a category by `apiVersion`, `kind`, resolved namespace, and `metadata.name`; the file path is provenance only. Moving a category file preserves `metadata.uid`. Changing `spec` or the Markdown body increments `metadata.generation` and `metadata.resourceVersion`. Path-only moves and label/annotation-only edits preserve `generation` and increment `resourceVersion`. Deleting the file removes the category from GraphQL reads after post-receive admission; adding the same identity again later creates a new UID.

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
# Look up a category by namespace + name
query {
  category(by: { namespacePath: { namespace: "gitstore-test", name: "personal-computers" } }) {
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
