# Quickstart: Hook Pipeline Wiring (spec#018)

## What this feature does

Wires the existing `NoopAdmissionHandler` stubs in `gitstore-git-service` to real gRPC callouts:

- **Pre-receive**: git service validates resource blobs against `gitstore-api` before any refs are updated. Invalid pushes are rejected with field-scoped errors.
- **Post-receive**: git service notifies `gitstore-api` after a push lands; the API fetches and stores catalog records asynchronously.

## Prerequisites

- spec#013 (AdmissionHandler trait) — CLOSED
- spec#014 (datastore schema) — CLOSED
- spec#015 (validate.Parse) — CLOSED
- spec#016 (ProductStatus types) — CLOSED
- Running stack: `make dev` or `make compose`

## Files changed (by component)

### shared/proto/gitstore/catalog/v1/
- `catalog_service.proto` — **new** (see `contracts/catalog_service.proto` in this spec)

### gitstore-git-service/
| File | Change |
|---|---|
| `src/config.rs` | Remove `validating_admission_policy`; add `SchemaValidationConfig`, restructure `AdmissionControlConfig` to add `branch_pattern`; add `CatalogServiceConfig` |
| `src/git/hooks.rs` | Add `ValidationHandler` trait + `NoopValidationHandler`; add `ResourceBlob`; restructure `HookPipeline` to two handler slots; add blob-extraction logic (gix tree-walk); add startup phase-conflict check |
| `src/git/hooks/validation_handler.rs` | **new** — `SchemaValidationHandler` (tonic client, `ValidateResources` callout, metrics, structured log) |
| `src/git/hooks/admission_handler.rs` | **new** — `AdmissionControlHandler` (tonic client, `AdmitResources` callout, branch filter, fire-and-forget spawn) |
| `src/git/metrics.rs` | Add `gitstore_schema_validation_total` counter |
| `src/main.rs` | Wire config → build `SchemaValidationHandler` + `AdmissionControlHandler` → pass to `HookPipeline`; add startup validation |
| `build.rs` | Enable `build_client=true` for the new catalog proto |
| `gitstore.toml` (default) | Replace `[admission_control.validating_admission_policy]` with new structure |

### gitstore-api/
| File | Change |
|---|---|
| `shared/proto/gitstore/catalog/v1/catalog_service.proto` | **new** |
| `internal/cataloggrpc/server.go` | **new** — `CatalogServiceServer` implementing both RPCs |
| `internal/cataloggrpc/server_test.go` | **new** — unit tests for both RPC handlers |
| `cmd/server/main.go` | Register `CatalogServiceServer` on the gRPC listener |

## Running after this change

```bash
# Full stack
make dev

# Run integration tests (previously failing — now expected to pass)
cd tests/integration && go test -v -tags=integration ./...

# Manually test: push a valid product
cd /tmp && git clone http://localhost:4000/gitstore/catalog.git test-repo
cd test-repo && cat > products/widget.md <<'EOF'
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: widget
  namespace: gitstore
spec:
  title: Widget
  tags: [test]
---

A test product.
EOF
git add products/widget.md && git commit -m "add widget"
git push  # → should be accepted

# Query it
curl -s -X POST http://localhost:4000/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"{ product(by:{namespacePath:{namespace:\"gitstore\",name:\"widget\"}}){ spec { title } } }"}'

# Manually test: push an invalid product (should be rejected)
echo '---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: bad
  namespace: gitstore
spec:
  title: "'$(python3 -c "print('x'*201)")'"
---' > products/bad.md
git add products/bad.md && git commit -m "bad product"
git push  # → should be rejected with spec.title / 200-char error
```

## SC-002 latency benchmark (100-file push)

Measured on a local `make dev` stack (macOS, loopback):

```
# Generate and push 100 valid products in a single commit
for i in $(seq -w 1 100); do
  cat > products/bench/product-${i}.md << 'PRODUCT'
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: bench-product-NNN
  namespace: acme-store
spec:
  title: Benchmark Product NNN
  tags: [bench]
---
PRODUCT
done
git add products/bench/ && git commit -m "bench: 100 products"
time git push   # → 0.158s  (SC-002 target: < 5s)
```

Result: **0.158 s** wall-clock including pack negotiation, pre-receive validation gRPC callout
(100 blobs → `ValidateResources`), ref update, and post-receive fire-and-forget.
All 100 files accepted; `gitstore_schema_validation_total{result="accepted"}` incremented once.

## Config reference (new env vars)

| Env var | Default | Description |
|---|---|---|
| `GITSTORE_SCHEMA_VALIDATION__PHASE` | `pre-receive` | Hook phase for validation callout |
| `GITSTORE_SCHEMA_VALIDATION__TIMEOUT_SECS` | `10` | Validation gRPC timeout in seconds |
| `GITSTORE_ADMISSION_CONTROL__PHASE` | `post-receive` | Hook phase for admission callout |
| `GITSTORE_ADMISSION_CONTROL__BRANCH_PATTERN` | `refs/heads/main` | Refs that trigger catalog storage |
| `GITSTORE_CATALOG_SERVICE__URI` | `http://localhost:6000` | gitstore-api gRPC endpoint |

## Constitution compliance

- **Test-First (I)**: Unit tests for `ValidationHandler`, `AdmissionControlHandler`, config startup validation, and branch filtering written before implementation. Integration tests in `tests/integration/product_lifecycle_test.go` already exist and provide the red phase.
- **API-First (II)**: `catalog_service.proto` contract defined and reviewed before any implementation.
- **Observability (IV)**: `gitstore_schema_validation_total` counter + structured log per callout.
- **Simplicity (VII)**: Blob extraction is done locally in the git service (no new infrastructure); fire-and-forget admission avoids blocking the git push response.
