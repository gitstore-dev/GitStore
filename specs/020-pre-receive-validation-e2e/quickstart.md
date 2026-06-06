# Quickstart: Pre-Receive Validation End-to-End (spec#020)

## What this spec changes

The only file modified is `.github/workflows/ci-integration.yml`:

1. **Port-6000 readiness check** added to the `integration-test` job (after the existing port-50051 check).
2. **New `integration-test-scylla` job** that replays the same product-lifecycle tests with the ScyllaDB backend.

No service code, test code, or compose files are changed.

## Prerequisites

- Docker and Docker Compose installed
- Go 1.25 installed
- `make` available

## Running the memdb integration tests locally

```bash
# Start the core stack (memdb backend)
docker compose up -d --build

# Wait for all ports to be ready
until curl -sf http://localhost:4000/health; do sleep 2; done
until curl -sf http://localhost:5000/health; do sleep 2; done
until nc -z localhost 50051; do sleep 1; done
until nc -z localhost 6000; do sleep 1; done    # CatalogService gRPC

# Bootstrap and seed
make bootstrap ADMIN_PASSWORD=admin123 NAMESPACE=gitci NAMESPACE_DISPLAY_NAME=CI NAMESPACE_TIER=USER REPOSITORY=catalog

git config --global user.email "ci@gitstore.dev"
git config --global user.name "CI"
tmpdir=$(mktemp -d)
git clone http://localhost:5000/gitci/catalog.git "$tmpdir"
echo "# CI seed" > "$tmpdir/README.md"
git -C "$tmpdir" add README.md
git -C "$tmpdir" commit -m "ci: seed initial commit"
git -C "$tmpdir" push origin main

# Run tests
cd tests/integration
NAMESPACE=gitci REPOSITORY=catalog go test -v -timeout 120s ./...

# Cleanup
docker compose down -v
```

## Running the ScyllaDB integration tests locally

```bash
# Start the full stack with ScyllaDB overlay
docker compose -f compose.yml -f compose.scylla.yml up -d --build

# Wait for all ports — ScyllaDB adds a longer startup window
until curl -sf http://localhost:4000/health; do sleep 2; done
until curl -sf http://localhost:5000/health; do sleep 2; done
until nc -z localhost 50051; do sleep 1; done
until nc -z localhost 6000; do sleep 1; done

# Bootstrap and seed (identical to memdb run)
make bootstrap ADMIN_PASSWORD=admin123 NAMESPACE=gitci NAMESPACE_DISPLAY_NAME=CI NAMESPACE_TIER=USER REPOSITORY=catalog

git config --global user.email "ci@gitstore.dev"
git config --global user.name "CI"
tmpdir=$(mktemp -d)
git clone http://localhost:5000/gitci/catalog.git "$tmpdir"
echo "# CI seed" > "$tmpdir/README.md"
git -C "$tmpdir" add README.md
git -C "$tmpdir" commit -m "ci: seed initial commit"
git -C "$tmpdir" push origin main

# Run tests
cd tests/integration
NAMESPACE=gitci REPOSITORY=catalog go test -v -timeout 120s ./...

# Cleanup
docker compose -f compose.yml -f compose.scylla.yml down -v
```

## Verifying individual test scenarios

### Verify pre-receive rejection

```bash
# Create an invalid product (title > 200 chars)
cat > /tmp/bad-product.md << 'EOF'
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: bad-product
  namespace: gitci
spec:
  title: "$(python3 -c 'print("x"*201)')"
---
EOF

tmpdir=$(mktemp -d)
git clone http://localhost:5000/gitci/catalog.git "$tmpdir"
mkdir -p "$tmpdir/products"
cp /tmp/bad-product.md "$tmpdir/products/"
git -C "$tmpdir" add .
git -C "$tmpdir" commit -m "test: invalid title"
git -C "$tmpdir" push origin main   # Should fail with spec.title / 200 in the output
```

### Verify post-receive admission (valid push → queryable)

Push a valid product file, then query GraphQL:

```bash
curl -s -X POST http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -d '{"query":"query { product(by: {namespacePath: {namespace: \"gitci\", name: \"my-product\"}}) { id spec { title } } }"}' \
  | jq .
```

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| Push accepted when it should be rejected | Port 6000 not ready when push ran | Ensure `nc -z localhost 6000` passes before pushing |
| Product not found after valid push | `AdmitResources` fire-and-forget failed | Check `docker compose logs api` for gRPC errors |
| ScyllaDB job fails with "keyspace not found" | `scylla-init` service not completed | Wait for `scylla-init` to exit 0 before running tests |
| `nc: command not found` in CI | Base image lacks netcat | Use `curl --silent --fail http://localhost:6000/...` or install netcat |
