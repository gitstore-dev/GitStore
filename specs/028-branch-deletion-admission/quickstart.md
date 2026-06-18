# Quickstart: Branch Deletion Admission (spec 028)

## What changes and why

A branch-delete push sends ref-update commands with `new_oid = "0000...0"` and no PACK
data. The gRPC `receive_pack` handler was unconditionally calling `stage_pack_from_reader`
on an empty byte stream, which fails. The fix adds a guard that skips pack staging when
all ref commands are deletes. After the fix:

- `git push origin --delete <branch>` succeeds.
- The `AdmissionControlHandler` forwards the zero-new-OID update to `AdmitResources`.
- The Go API's existing zero-OID path deletes all resources admitted on the branch.

---

## Files changed

```text
gitstore-git-service/src/grpc/server.rs
  └── receive_pack()  — add is_delete_only guard before stage_pack_from_reader

gitstore-git-service/src/git/hooks/admission_handler.rs
  └── tests           — add T019f: zero new-OID on matching ref triggers AdmitResources
```

No Go, proto, or test-infrastructure changes are required.

---

## Running the unit tests

```bash
# Rust unit tests (includes T019f)
cd gitstore-git-service && cargo test admission_handler

# Full Rust test suite
cd gitstore-git-service && cargo test
```

---

## Running the integration test

```bash
# Start full stack with branch pattern including feature/* branches
GITSTORE_ADMISSION_CONTROL__BRANCH_PATTERN="refs/heads/*" make dev   # or compose

# Run the branch-deletion integration test
cd tests/integration && \
  NAMESPACE=gitstore-test \
  go test -v -run TestAdmission_BranchDeletion ./...
```

---

## Verifying end-to-end

```bash
# 1. Push a product on a feature branch
git checkout -b feature/del-test
cat > catalog/test-product.md <<'EOF'
---
apiVersion: commerce.gitstore.io/v1
kind: Product
metadata:
  name: del-test-product
spec:
  title: Delete Test Product
  description: Created to test branch deletion admission
---
EOF
git add catalog/test-product.md && git commit -m "feat: add del-test-product"
git push origin feature/del-test

# 2. Query to verify product exists
# (adjust endpoint and namespace as configured)
curl -s http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -d '{"query":"{ product(namespace:\"gitstore-test\",name:\"del-test-product\") { name } }"}'
# Expected: {"data":{"product":{"name":"del-test-product"}}}

# 3. Delete the branch
git checkout main
git push origin --delete feature/del-test
# Expected: remote accepts the delete (exit 0, no error)

# 4. Wait ~1s for fire-and-forget admission, then query again
sleep 1
curl -s http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -d '{"query":"{ product(namespace:\"gitstore-test\",name:\"del-test-product\") { name } }"}'
# Expected: {"data":{"product":null}}  or a not-found error
```
