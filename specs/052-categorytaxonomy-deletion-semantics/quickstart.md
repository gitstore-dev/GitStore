# Quickstart: Verify CategoryTaxonomy Deletion Semantics

Run the API, controller, and Git service with development credentials. Admit a parent category, child category, and Product referencing the parent.

## 1. Verify child rejection

Delete the parent with `deleteCategory` and by removing its manifest in a Git push.

**Expected**: both return a child-dependent precondition error. Parent, child owner reference, and child status are unchanged.

## 2. Verify Product decoupling

Remove/reparent the child but retain the Product, then delete the parent.

**Expected**: the category becomes `Terminating=True`; Product count does not block. The controller removes the Product owner reference and writes `CategoryResolved=False`, reason `CategoryDeleted`, leaving `spec.categoryRef` untouched.

## 3. Verify race and restart safety

While terminating, attempt a Product create targeting the category and create/reparent a child to it. Restart one controller replica during Product decoupling.

**Expected**: Product admission rejects the terminating target; the new child prevents completion; after child removal, either replica resumes bounded idempotent decoupling and finalizes once.

## 4. Validate

Run the focused checks (validated on 2026-08-20):

```bash
(cd gitstore-api && go test ./cmd/backfill-owner-references ./internal/catalog ./internal/graph/model ./internal/datastore/memdb ./internal/datastore/scylla ./internal/cataloggrpc ./internal/admission/catalog ./internal/graph/resolver)
(cd gitstore-controller-manager && go test ./internal/categorytaxonomy ./internal/listwatch ./cmd/controller ./tests/integration)
(cd gitstore-git-service && cargo test category_taxonomy_deletion_handler)
```

The focused unit, resolver, admission, backfill, controller, and Git-hook
checks pass. The tagged Scylla contract suite requires a reachable Scylla
instance and is run by `make test-scylla-integration`.

Before enabling enforcement against an upgraded production keyspace, dry-run
the idempotent backfill and retain its `resumeAfter` cursor if it must be
resumed:

```bash
(cd gitstore-api && go run ./cmd/backfill-owner-references --dry-run)
(cd gitstore-api && go run ./cmd/backfill-owner-references --resume-after '<cursor>')
```

CategoryTaxonomy deletions are checked synchronously against the old and
proposed ref trees. Creates and updates remain asynchronous post-receive
admission and do not call the API from pre-receive. Run `make pr-ready` before
a PR.
