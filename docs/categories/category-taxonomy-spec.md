# CategoryTaxonomy Spec Reference

**API Version**: `catalog.gitstore.dev/v1beta1`  
**Kind**: `CategoryTaxonomy`

A CategoryTaxonomy resource is a Markdown file with YAML frontmatter pushed to a GitStore repository. It represents a hierarchical catalog category with optional parent linkage, media references, and a resolved ancestor path.

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

Categories form a tree via `spec.parentRef`. The system computes a materialized `ancestorPath` (slash-separated names from root to self) at admission time.

**Rules enforced at push time (pre-receive, blocking):**
- `spec.parentRef.name` must not equal `metadata.name` (self-parenting rejected)

**Rules enforced at admission time (post-receive, non-blocking):**
- If the parent is in the same push (co-creation), `ancestorPath` is set to `parentName/childName` and `ParentResolved=True`
- If the parent exists in the datastore, `ancestorPath` inherits `parent.ancestorPath/childName` and `ParentResolved=True`
- If the parent is not found anywhere, the category is stored as a tentative root (`ancestorPath=name`) with `ParentResolved=False`
- Intra-push mutual cycles (A→B, B→A) are stored with `Acyclic=False`

---

## Status Conditions

The system writes a `status` blob to the datastore after each push. Conditions follow the Kubernetes convention (`True`/`False`/`Unknown`).

| Condition           | Meaning                                                         |
|--------------------|-----------------------------------------------------------------|
| `AdmissionAccepted` | Resource was stored by the post-receive pipeline                |
| `ParentResolved`    | `spec.parentRef` was found (in push or in DB)                   |
| `Acyclic`           | No intra-push cycle detected involving this category           |
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
