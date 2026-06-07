# Quickstart: Collection Frontmatter Integration Tests and Documentation

## Prerequisites

- Docker and Docker Compose installed
- Go 1.25+ on `PATH`
- Repo cloned; `make` available at repo root

---

## Running integration tests

### memdb backend (default — no additional infra)

```bash
# Start the core stack (API + git service, memdb backend)
make compose DETACH=1

# Bootstrap a namespace and repository
make bootstrap ADMIN_PASSWORD=admin123

# Run the full integration suite (includes collection tests once implemented)
cd tests/integration
go test -v -timeout 120s ./...
```

### ScyllaDB backend

```bash
# Start full stack with ScyllaDB overlay
DETACH=1 make compose-scylla

# Bootstrap
make bootstrap ADMIN_PASSWORD=admin123

# Run integration tests against the ScyllaDB-backed stack
cd tests/integration
go test -v -timeout 120s ./...
```

The test binaries are identical for both runs. Backend selection is controlled by which compose overlay is active (the `GITSTORE_DATASTORE__BACKEND` env var is injected by `compose.scylla.yml`).

---

## Running datastore contract tests (CRUD layer)

```bash
# memdb (always on)
cd gitstore-api
go test -v -race ./tests/contract/datastore/...

# ScyllaDB (requires a running ScyllaDB instance)
docker run -d --name test-scylla -p 9042:9042 \
  scylladb/scylla:5.4 \
  --developer-mode=1 --overprovisioned=1 --smp=1

# Wait for ScyllaDB to be ready
timeout 60 sh -c 'until docker exec test-scylla cqlsh -e "describe cluster" 2>/dev/null; do sleep 2; done'

cd gitstore-api
GITSTORE_TEST_SCYLLA_ADDR=127.0.0.1:9042 go test -tags scylla -v ./tests/contract/datastore/...
```

---

## Writing a valid Collection document

Minimum valid document:

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: my-collection
  namespace: acme-store
spec:
  title: My Collection
---

Optional markdown body describing this collection.
```

With label selector:

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
      name: collection-hero
      kind: File
      optional: true
---

Apple laptop products across MacBook Air and MacBook Pro families.
```

Push it with standard git:

```bash
mkdir -p collections
cp my-collection.md collections/
git add collections/my-collection.md
git commit -m "feat(catalog): add apple-laptops collection"
git push origin main
```

---

## Querying a Collection via GraphQL

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
      conditions { type status reason message }
      resolved { memberCount }
    }
    products(first: 10) {
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

---

## Environment variables reference

| Variable | Default | Purpose |
|----------|---------|---------|
| `API_URL` | `http://localhost:4000` | GraphQL API endpoint (integration tests) |
| `GIT_URL` | `http://localhost:5000` | Git HTTP service URL (integration tests) |
| `NAMESPACE` | `gitstore-test` | Catalog namespace for integration tests |
| `REPOSITORY` | `catalog` | Repository name for integration tests |
| `GITSTORE_TEST_SCYLLA_ADDR` | `127.0.0.1:9042` | ScyllaDB address for contract tests |
| `GITSTORE_DATASTORE__BACKEND` | `memdb` | Backend selection (injected by compose overlay) |
