# Quickstart: Admission Path Cleanup

**Feature**: `034-admission-path-cleanup`

## Verifying Phase 1 (Rust `changed_paths` population)

Run the Rust unit tests:

```bash
cd gitstore-git-service
cargo test admission_handler
```

Expect all `T019*` tests to pass plus the new `T020*` tests covering:
- `changed_paths` populated for a single-file update
- `changed_paths` contains all files for a new branch (all-zeros `old_oid`)
- `changed_paths` contains all files from old tree for a branch deletion
- gix open failure → `changed_paths: vec![]` fallback, no panic

## Verifying Phase 2 (Go legacy path removal)

Run the Go unit tests:

```bash
cd gitstore-api
go test ./internal/cataloggrpc/... -v
```

Confirm:
- No test references the legacy warning log `"admit_resources: old commit absent"`.
- A push with `old_commit_sha` set and `changed_paths` containing one path results in exactly one `ReadFile` call for that path (not all files).

## End-to-end verification

Start the stack, then push a single-file change to a repository containing multiple tracked resources:

```bash
make dev                                         # start native stack
make bootstrap ADMIN_PASSWORD=<pw>               # create namespace + repo
git clone http://localhost:3000/gitstore-test/catalog.git /tmp/catalog
cd /tmp/catalog
echo "---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  name: widget\n---\nbody" > products/widget.md
git add products/widget.md && git commit -m "add widget"
git push
```

In the API logs, confirm:
1. No `"admit_resources: old commit absent"` warning.
2. The admission log shows only `products/widget.md` read, not all files.

## Confirming Phase 2 is safe to deploy

After Phase 1 is running in staging, watch for 10 pushes without the fallback warning:

```bash
make logs SERVICE=api | grep "admit_resources"
```

Absence of `"old commit absent"` for 10+ consecutive pushes confirms Phase 2 is safe.
