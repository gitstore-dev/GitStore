# Quickstart: Git Smart-HTTP Authentication (035)

## Prerequisites

- Running environment from `make dev` or `make compose`
- Admin password hash in `gitstore-api/.env` (`make gen-admin-password ADMIN_PASSWORD=<pw>`)
- Bootstrap token cached (`make bootstrap-token ADMIN_PASSWORD=<pw>`)
- A namespace and repository exist (`make bootstrap ADMIN_PASSWORD=<pw>`)

## Test authenticated clone and push

```bash
# Clone with credentials (replace <ns>/<repo> with your bootstrap values)
git clone http://admin:<password>@localhost:5000/gitstore-test/catalog.git

# Push a commit
cd catalog
echo "test" > test.txt
git add test.txt
git commit -m "test: auth smoke test"
git push origin main
```

Expected: push completes; admission log includes `actor_subject=admin`.

## Test unauthenticated fetch (anonymous allow)

Requires `rbac-local` policy granting `repository.read` to the anonymous role.

```bash
git clone http://localhost:5000/gitstore-test/catalog.git anon-clone
```

Expected: clone succeeds without credentials.

## Test 401 on unauthenticated push

```bash
git -C anon-clone push origin main
```

Expected: git prompts for credentials (401 + `WWW-Authenticate: Basic realm="GitStore"`).

## Test 403 on insufficient permissions

Configure a principal with `repository.read` only (via `rbac-policy.yaml`), then:

```bash
git push http://<read-only-user>:<password>@localhost:5000/gitstore-test/catalog.git
```

Expected: 403 Forbidden; git-service not contacted.

## Test push policy enforcement

Set `MaxFileSizeBytes = 1048576` (1 MB) on the repository record, then push a commit containing a 2 MB file.

Expected: push rejected with an error referencing the size limit; no objects written to disk.

## Verify metrics

```bash
curl -s http://localhost:4000/metrics | grep gitstore_git_http_auth
```

Expected output includes:
```
gitstore_git_http_auth_requests_total{outcome="allow",service="receive_pack"} 1
gitstore_git_http_auth_requests_total{outcome="deny",service="upload_pack"} 1
```

## Run tests

```bash
# Go unit + integration tests
cd gitstore-api && go test ./internal/githttp/... ./internal/middleware/... ./internal/health/... -v

# Rust tests
cd gitstore-git-service && cargo test

# Full integration suite. See .github/workflows/ci-integration.yml for setup and start services with `make dev`
cd tests/integration && go test -v -timeout 120s ./...
```
