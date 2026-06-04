# Contract: `validate.Parse`

**Package**: `github.com/gitstore-dev/gitstore/api/internal/validate`  
**Function**: `Parse(r io.Reader) (*catalog.ProductResource, []byte, error)`

## Signature (unchanged)

```go
func Parse(r io.Reader) (*catalog.ProductResource, []byte, error)
```

## Behavioural Contract

### Preconditions

- `r` is a readable `io.Reader` containing a UTF-8 Markdown document.
- `r` may be any Markdown file pushed to the repository (product or non-product).

### Postconditions

| Condition                                              | Return                                                                    |
|--------------------------------------------------------|---------------------------------------------------------------------------|
| File does not begin with `---` (no YAML frontmatter)   | `nil, rawBytes, nil` — caller MUST treat as "not a product file" and skip |
| File has frontmatter; all constraints satisfied        | `*ProductResource, body, nil` — fully populated resource                  |
| File has frontmatter; one or more constraints violated | `nil, nil, err` — `err` contains all violation messages joined            |

### Error Guarantee

When the file has frontmatter and validation fails, the returned `error`:
- Is non-nil.
- Contains **all** distinct violations found (not just the first).
- Each violation message includes the dotted field path (e.g. `metadata.uid`) and a human-readable description.
- Violations are joined with newlines (`errors.Join`).

### Short-circuit Cases (single error, no collection)

The following conditions still return immediately with a single error:
- YAML frontmatter is syntactically malformed (cannot unmarshal).
- File contains a forbidden field (`status`, read-only metadata sub-keys).

These cases are fundamentally invalid and collecting further errors would be misleading.

### Invariants

- The function NEVER modifies the contents of `r` beyond consuming it.
- A file without `---` is NEVER rejected — it always returns `(nil, rawBytes, nil)`.
- A file with `---` and a missing `apiVersion` is ALWAYS rejected (legacy format guard).
- Struct binding uses `frontmatter.NewFormat("---", "---", yaml.Unmarshal)` — TOML and JSON frontmatter are silently treated as no-frontmatter (returned as skip).

## Callers

- `gitstore-git-service` pre-receive hook (via gRPC): iterates pushed files, calls `Parse` on each, rejects the push if any file returns a non-nil error.
- Future: admission webhook, catalogue import tool.

## Non-Goals

- This function does NOT validate `metadata.namespace` against push context — that is the caller's responsibility.
- This function does NOT check `spec.categoryRef` existence in the datastore.
- This function does NOT generate `metadata.name` from `metadata.generateName`.
