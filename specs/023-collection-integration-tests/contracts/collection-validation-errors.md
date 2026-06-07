# Contract: Collection Validation Error Semantics

This document is the authoritative reference for all validation errors the system returns when a Collection document fails pre-receive or post-parse validation. It feeds directly into FR-011 (documentation) and FR-002–FR-004 (test assertions).

---

## Error format

All validation errors are returned as a single plain-text message on the git push stderr stream, prefixed with `remote:`. Multiple violations in the same document are joined with a newline.

Example:
```
remote: error: validate: spec.title is required
```

---

## Validation error table

| Condition | Field / rule | Error message pattern |
|-----------|-------------|----------------------|
| Missing or wrong `apiVersion` | `apiVersion != catalog.gitstore.dev/v1beta1` | `validate: apiVersion must be "catalog.gitstore.dev/v1beta1"` |
| Wrong `kind` | `kind != Collection` | `validate: kind must be "Collection"` |
| Unknown `kind` | kind not in {Product, CategoryTaxonomy, Collection} | `validate: kind %q is not a recognized catalog resource type` |
| Missing `metadata.name` | `metadata.name` empty | `validate: metadata.name is required` |
| Missing `metadata.namespace` | `metadata.namespace` empty | `validate: metadata.namespace is required` |
| Missing `spec.title` | `spec.title` empty | `validate: spec.title is required` |
| Invalid `targetRef.kind` | value other than `"Product"` | `validate: spec.targetRef.kind must be "Product", got "<value>"` |
| `In`/`NotIn` with empty values | `matchExpressions[N].operator == In\|NotIn && len(values) == 0` | `validate: spec.selector.matchExpressions[N]: operator "In" requires at least one value` |
| Invalid `operator` | value not in {In, NotIn, Exists, DoesNotExist} | `validate: spec.selector.matchExpressions[N].operator must be one of In, NotIn, Exists, DoesNotExist` |
| Status key present in author document | top-level `status:` key in YAML | `validate: "status" is a system-managed field and must not be set by the author` |
| System-managed metadata field present | `uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision` in `metadata` | `validate: metadata.<field> is a system-managed field and must not be set by the author` |
| Document does not start with `---` | not YAML frontmatter | `validate: document does not use Kubernetes-style frontmatter (missing apiVersion); migration is not supported in alpha` |

---

## Zero-member selector (not an error)

An absent or empty `spec.selector` is **valid**. The collection is admitted with:

```yaml
status:
  conditions:
  - type: MembersResolved
    status: "True"
    reason: NoProductsMatched
    message: Selector absent or empty; no products matched.
  resolved:
    memberCount: 0
```

---

## Multiple documents in one commit

When a push contains multiple Collection (or mixed-kind) documents, validation runs for each file. If any file fails validation, the **entire push is rejected** (atomic pre-receive semantics). Each failing file produces a separate error line.

---

## Notes for test assertions

- Error message patterns above use Go's `fmt.Errorf` format; tests should use `strings.Contains` or regex matching on the push output, not exact equality, to remain resilient to minor wording changes.
- The `[N]` placeholder in matchExpressions errors is the zero-based index of the failing requirement.
