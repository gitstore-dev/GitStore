# CategoryTaxonomy Spec Reference

**API Version**: `catalog.gitstore.dev/v1beta1`  
**Kind**: `CategoryTaxonomy`

A CategoryTaxonomy resource is a Markdown file with YAML frontmatter pushed to a GitStore repository. It represents a hierarchical catalog category with optional parent linkage, media references, and a controller-resolved ancestor path.

---

## Envelope Fields

| Field        | Type   | Required      | Constraint                               |
|--------------|--------|---------------|------------------------------------------|
| `apiVersion` | string | yes           | Must be `catalog.gitstore.dev/v1beta1`   |
| `kind`       | string | yes           | Must be `CategoryTaxonomy` (case-sensitive) |
| `metadata`   | object | yes           | See Metadata Fields                      |
| `spec`       | object | yes           | See Spec Fields                          |
| `status`     | —      | **forbidden** | System-managed; presence causes rejection |

---

## Metadata Fields

| Field                  | Type              | Required | Constraint                                                             |
|------------------------|-------------------|----------|------------------------------------------------------------------------|
| `metadata.name`        | string            | yes      | DNS subdomain format; unique within namespace                          |
| `metadata.namespace`   | string            | no       | Optional. Inferred from the repository's owning namespace when omitted |
| `metadata.labels`      | map[string]string | no       | Key prefix ≤ 253 chars; key name ≤ 63 chars; value ≤ 63 chars          |
| `metadata.annotations` | map[string]string | no       | No length restriction                                                  |

**Forbidden metadata fields** (read-only, system-assigned):
`uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`, `ownerReferences`

## Lifecycle

GitStore identifies a category by `apiVersion`, `kind`, resolved namespace, and `metadata.name`; the file path is provenance only. Moving a category file preserves `metadata.uid`. Changing `spec` or the Markdown body increments `metadata.generation` and `metadata.resourceVersion`. Path-only moves and label/annotation-only edits preserve `generation` and increment `resourceVersion`. Deleting the file removes the category from GraphQL reads after post-receive admission; adding the same identity again later creates a new UID.

---

## Spec Fields

All spec fields are individually optional unless noted otherwise. Constraints apply when the field is present.

| Field           | Type   | Constraint                                         |
|----------------|--------|----------------------------------------------------|
| `spec.title`   | string | Human-readable display title; required            |
| `spec.parentRef` | object | Optional; parent category reference               |
| `spec.media`   | list   | Optional; file references for category presentation |

### Parent Reference

| Field                | Type   | Required | Constraint                                    |
|----------------------|--------|----------|-----------------------------------------------|
| `parentRef.name`     | string | yes      | Name of the parent `CategoryTaxonomy` resource |
| `parentRef.kind`     | string | no       | Defaults to `CategoryTaxonomy`                |
| `parentRef.optional` | bool   | no       | Present for parity only; ignored              |

### Media Fields

| Field                       | Type   | Required | Constraint                                        |
|-----------------------------|--------|----------|---------------------------------------------------|
| `media[*].fileRef.name`     | string | yes      | Name of the `File` resource                       |
| `media[*].fileRef.kind`     | string | no       | Defaults to `"File"`                              |
| `media[*].fileRef.optional` | bool   | no       | When `true`, admission succeeds if absent        |

---

## Namespace Inference

All current Git-backed catalog resources (`Product`, `ProductVariant`, `CategoryTaxonomy`, `Collection`) treat `metadata.namespace` as optional in the committed file. When omitted, the namespace is resolved at admission time from the push context.

The raw repository UUID is never stored as the namespace. If the repository or its namespace cannot be resolved, the push admission is aborted and no resources are stored.

Even when `metadata.namespace` is present in the file it is still validated to match the inferred namespace. The field exists to allow multiple repositories within the same namespace to push resources that cross-reference each other by name.

---

## Hierarchy

Categories form a tree via `spec.parentRef`. A category's position in the tree is computed by the controller manager and published in `status.resolved`:

| Field                      | Meaning                                                              |
|----------------------------|----------------------------------------------------------------------|
| `status.resolved.path`     | Names from root to self, e.g. `["electronics", "computers", "laptops"]`. A root's path is its own name. |
| `status.resolved.depth`    | Depth in the tree. Roots are `0`.                                    |
| `status.resolved.childCount` / `productCount` | Materialized counts.                             |

`status.resolved` is the only source of hierarchy. There are no top-level `Category.path` or `Category.depth` fields, and admission does not compute a path.

- `status.resolved` is `null` until the category is first reconciled. A category with no `resolved` value is not matched by subtree filters.
- The value is eventually consistent. After an ancestor is created, moved or reparented, descendants converge one level at a time as the controller reconciles them, so a read immediately after the change can briefly show the old path.
- A category whose parent cannot be found gets `ParentResolved=False` and is re-reconciled once the parent appears.
- `Category.parent` resolves the direct parent (null for roots, unresolved parents, or before the first reconcile). `Category.children` returns at most 100 direct children ordered by name, and is empty until the children have been reconciled.

**Rules enforced at push time (pre-receive, blocking):**
- `spec.parentRef.name` must not equal `metadata.name` (self-parenting rejected)
- `spec.parentRef.namespace` must be omitted or equal the category's namespace (cross-namespace parents rejected)
- A parent that is terminating cannot gain new children
- At the same file path, `metadata.name` and `metadata.namespace` cannot change. Create a new category and delete the old one instead.
- Removing a category's manifest is rejected while child categories still reference it, unless the same push removes or reparents them.

**Rules enforced at admission time (post-receive, non-blocking):**
- Intra-push mutual cycles (A→B, B→A) are stored with `Acyclic=False`
- A parent that cannot be found is recorded as `ParentResolved=False`

### Listing a subtree

`categories(namespace:, filter:)` returns the subtree below `filter.descendantOf`, ordered by relative depth and then name. `includeSelf: true` adds the named category as the first result, and `maxDepth` (1 to 128) limits how many levels below it are returned. An unknown `descendantOf` name returns an empty connection. Cursors from filtered and unfiltered listings are not interchangeable and are rejected with `BAD_USER_INPUT`. See the [API reference](../api-reference.md#categories).

---

## Writing categories through GraphQL

Categories can be authored by Git push or by the `createCategory`, `updateCategory` and `deleteCategory` mutations. The mutations commit the manifest to the namespace's `gitstore-system` repository and run the same checks a push does, so both paths accept and reject the same manifests.

- `createCategory` writes a new manifest. It fails with `ALREADY_EXISTS` if the name is taken.
- `updateCategory` commits to the repository and path the category was admitted from. `metadata.name` and `metadata.namespace` cannot change. Omitting `body` keeps the current body.
- `deleteCategory` removes the manifest and starts foreground deletion (outcome `TERMINATION_STARTED`; a repeat returns `ALREADY_TERMINATING` without a new commit). It fails with `FAILED_PRECONDITION` while child categories exist. Assigned products are decoupled asynchronously by the controller, which then finishes removal with `completeCategoryDeletion`.

Rejections use the shared error envelope documented in the [API reference](../api-reference.md#error-handling).

---

## Status Conditions

The system writes a `status` blob to the datastore after each push. Conditions follow the Kubernetes convention (`True`/`False`/`Unknown`).

| Condition           | Meaning                                                         |
|--------------------|-----------------------------------------------------------------|
| `AdmissionAccepted` | Resource was stored by the post-receive pipeline. `False` with reason `AdmissionReportFailed` when post-receive admission rejected a commit; the last accepted generation is kept |
| `ParentResolved`    | `spec.parentRef` was found                                      |
| `Acyclic`           | No cycle detected involving this category                       |
| `Terminating`       | Foreground deletion has started                                 |
| `Ready`             | Controller has fully reconciled the resource (GH#244, deferred) |

---

## Validation Errors

| Error                                                        | Cause                               |
|--------------------------------------------------------------|-------------------------------------|
| `spec.title is required`                                     | `spec.title` missing or empty       |
| `metadata.name is required`                                  | `metadata.name` missing             |
| `spec.parentRef.name must not reference the category itself` | Self-parenting                      |
| `kind "X" is not a recognized catalog resource type`         | Unknown `kind` value                |
| `status is system-managed`                                   | `status` key present in author file |
| `metadata.uid is read-only`                                  | System field set in author file     |
| `spec.parentRef.namespace` must match the category namespace | Cross-namespace parent              |
| `metadata.name` / `metadata.namespace` is immutable          | Identity changed at the same path   |
| parent category is terminating                               | New child of a terminating parent   |

Each of these maps to a diagnostic `reason` on GraphQL mutations (`REQUIRED_FIELD`, `SELF_PARENT`, `CROSS_NAMESPACE_REFERENCE`, `IMMUTABLE_NAME`, `IMMUTABLE_NAMESPACE`, `PARENT_TERMINATING`, ...); see the [reason table](../api-reference.md#category-reasons).

---

## Examples

### Minimal Category

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: personal-computers
  namespace: my-store
spec:
  title: Personal Computers
---

Personal Computers is the category for desktop and laptop computers.
```

### Category With Parent And Media

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: laptops
  namespace: my-store
spec:
  title: Laptops
  parentRef:
    name: personal-computers
  media:
  - fileRef:
      name: category-hero
      kind: File
      optional: true
---

Category copy for laptops.
```

---

## File Existence Checks

`spec.media[].fileRef` entries reference `File` resources. Push-time validation only checks that `fileRef.name` and `fileRef.kind` are present. Whether the referenced `File` resource exists is checked by the controller reconciler (GH#244, deferred from this spec).

Set `fileRef.optional: true` to prevent the controller from blocking the `Ready` condition when the file is absent.
