# Quickstart: Product Resource Contract — Kubernetes-style Frontmatter

**Feature**: 014-product-frontmatter | **Date**: 2026-06-01

## What this feature delivers

Defines the canonical schema for `Product` catalog documents using Kubernetes-style frontmatter. After this feature:

- Authors write product files with `apiVersion`, `kind`, `metadata`, `spec` fields.
- On push acceptance, the system parses the git content and writes the fully hydrated record (spec + all metadata + status) to the datastore.
- All reads go directly to the datastore — no git blob lookups, no merge at read time.
- The git service rejects files that include `status` or read-only metadata fields.

---

## Writing a Product File

Create a Markdown file in your namespace's product directory:

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: macbook-pro-m4          # required; unique within namespace
  namespace: my-store           # optional; inherited from push context
  labels:
    gitstore.dev/brand: apple
    gitstore.dev/product-type: laptop
  annotations:
    gitstore.dev/notes: "Flagship model 2026"
spec:
  title: MacBook Pro 16" M4 Max
  categoryRef:
    kind: CategoryTaxonomy
    name: personal-computers
  tags: [laptop, apple-silicon, macbook]
  options:
  - name: color
    title: Colour
    values: [silver, space-black]
  - name: ram
    title: Memory
    values: [36GB, 64GB]
  - name: storage
    values: [1TB SSD, 2TB SSD]
  media:
  - fileRef:
      kind: File
      name: hero-image
      optional: true
---

# MacBook Pro 16" with M4 Max

The most powerful MacBook Pro ever.
```

Push this file to the repository. The system will:
1. `pre-receive` — **schema validation** (blocking, `GITSTORE_SCHEMA_VALIDATION__PHASE=pre-receive`): the git service calls out to `gitstore-api`; the API reads the incoming blobs, parses frontmatter, and rejects the push if the schema is invalid.
2. `post-receive` — **admission control** (fire-and-forget, `GITSTORE_ADMISSION_CONTROL__PHASE=post-receive`): the git service spawns a non-blocking callout to `gitstore-api`; the API assigns system metadata (`uid`, `resourceVersion`, etc.), writes initial `status`, and stores the fully hydrated record in the datastore.
3. The full resource view (including system-populated `status`) is queryable via GraphQL from the datastore once admission control completes.

---

## What NOT to include

The following will cause your push to be **rejected**:

```yaml
# ❌ Do not include status
status:
  conditions: [...]

# ❌ Do not include read-only metadata fields
metadata:
  uid: "some-uuid"
  resourceVersion: "abc123"
  generation: 5
  creationTimestamp: "2026-01-01T00:00:00Z"
```

---

## Querying a Product

Once pushed, query the full resource via GraphQL. All fields are served from the datastore — no git I/O on the read path:

```graphql
query {
  product(namespace: "my-store", name: "macbook-pro-m4") {
    apiVersion
    kind
    metadata {
      name
      namespace
      uid
      resourceVersion
      generation
      labels
    }
    spec {
      title
      categoryRef { kind name }
      tags
      options { name title values }
      media { fileRef { name kind optional } }
    }
    status {
      conditions { type status reason message }
      resolved {
        category { name path }
        priceRange { currencyCode min max }
        totalInventory
        variantSummary { total ready unavailable }
      }
    }
  }
}
```

---

## Implementation Checklist (for contributors)

### Go (`gitstore-api`)

- [ ] Add `github.com/gitstore-dev/gitstore/api/internal/catalog` package with typed structs from `contracts/go-types.md`
- [ ] Add production code to `gitstore-api/internal/validate/` (currently stub)
- [ ] Rewrite `gitstore-api/internal/datastore/memdb/schema.go` `product` table (namespace+name compound key)
- [ ] Rewrite `gitstore-api/internal/datastore/scylla/migrations/001_initial_schema.cql` with new `products` table (namespace+name primary key, spec/status JSON blobs)
- [ ] Rewrite `shared/schemas/product.graphqls` with K8s-style `Product` type from `contracts/graphql-schema.md`

### Rust (`gitstore-git-service`)

No changes required. The `AdmissionHandler` delegation boundary from feature #013 is the integration point. The concrete gRPC admission callout to `gitstore-api` is implemented by GH#105/106.

### Test-first (constitution requirement)

Write these tests before implementation code:

- [ ] `catalog/product_test.go` — typed struct parsing round-trips for all `ProductResource`, `ProductSpec`, `ProductStatus` fields
- [ ] `validate/validator_test.go` — acceptance: valid product frontmatter parses without error
- [ ] `validate/validator_test.go` — rejection: wrong kind, missing name, legacy frontmatter, forbidden `status` field, forbidden read-only metadata fields

---

## Running the Validate Tests

```bash
# Go only — no Rust changes in this feature
cd gitstore-api && go test ./internal/catalog/... ./internal/validate/...
```

---

## Related Issues

| Issue  | Description                                            | Status |
|--------|--------------------------------------------------------|--------|
| GH#40  | [Initiative] Kubernetes-style Catalog Frontmatter      | Open   |
| GH#77  | Support Kubernetes-style Product frontmatter           | Open   |
| GH#184 | Product Resource Contract (this feature)               | Open   |
| GH#185 | Validation and parsing semantics (blocked by GH#184)   | Open   |
| GH#186 | Secondary domain constraints (blocked by GH#184)       | Open   |
| GH#187 | Integration tests and docs (blocked by GH#185, GH#186) | Open   |
| GH#105 | Catalog Validation in gitstore-api (blocked by GH#40)  | Open   |
| GH#106 | ValidatingAdmissionPolicy Engine (blocked by GH#40)    | Open   |
