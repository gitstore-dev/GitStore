# Quickstart: CategoryTaxonomy Development

**Feature**: 021-category-taxonomy | **Date**: 2026-06-06

---

## Prerequisites

```bash
# Start the full stack (API + git service)
make dev

# Or with ScyllaDB
make compose-scylla

# Bootstrap default namespace and repository
make bootstrap ADMIN_PASSWORD=secret
```

---

## Push a Root Category

Create `categories/electronics.md`:

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: electronics
  namespace: gitstore
  labels:
    gitstore.dev/segment: tech
spec:
  title: Electronics
---

The Electronics category covers all consumer electronic products.
```

Push:

```bash
git add categories/electronics.md
git commit -m "feat: add electronics category"
git push
```

Expected: push accepted, category queryable.

---

## Push a Child Category

Create `categories/computers.md`:

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: computers
  namespace: gitstore
spec:
  parentRef:
    kind: CategoryTaxonomy
    name: electronics
  title: Computers
---

Desktop and laptop computers.
```

Push: accepted. `computers.AncestorPath` = `electronics/computers`.

---

## Verify Rejection — Missing parent

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: laptops
spec:
  parentRef:
    name: nonexistent-parent
  title: Laptops
---
```

Expected push rejection:
```
remote: error: categoryTaxonomy "laptops": parentRef "nonexistent-parent" does not exist
```

---

## Verify Rejection — Cycle

After pushing `electronics` (root) and `computers` (child of electronics):

Update `electronics.md` to set `parentRef.name: computers`:

```yaml
spec:
  parentRef:
    name: computers
  title: Electronics
```

Expected push rejection:
```
remote: error: categoryTaxonomy "electronics": parentRef "computers" creates a cycle in the category ancestry
```

---

## Query via GraphQL

```graphql
query {
  category(by: { name: "computers" }) {
    id
    name
    title
    categoryStatus {
      lastAppliedRevision
      conditions {
        type
        status
        message
      }
    }
  }
}
```

---

## Run Tests

```bash
# Unit + integration tests
cd gitstore-api
go test ./internal/validate/... ./internal/cataloggrpc/... ./internal/datastore/...

# E2E tests (requires running stack)
go test ./tests/e2e/... -run TestCategoryTaxonomy

# All
make test
```

---

## Key Files

| File | Purpose |
|---|---|
| `gitstore-api/internal/catalog/category.go` | CategoryTaxonomyResource, Spec struct |
| `gitstore-api/internal/validate/validator.go` | Kind-routing, schema validation |
| `gitstore-api/internal/cataloggrpc/server.go` | ValidateResources + AdmitResources handlers |
| `gitstore-api/internal/datastore/entities.go` | CategoryTaxonomy entity |
| `gitstore-api/internal/datastore/memdb/schema.go` | go-memdb table and indexes |
| `gitstore-api/internal/datastore/scylla/migrations/003_category_taxonomy.cql` | ScyllaDB migration |
| `shared/schemas/category.graphqls` | GraphQL schema additions |
