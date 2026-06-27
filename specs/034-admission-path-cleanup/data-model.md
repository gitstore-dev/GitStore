# Data Model: Admission Path Cleanup

**Branch**: `034-admission-path-cleanup` | **Date**: 2026-06-27

No new datastore entities, GraphQL types, or proto messages are introduced. This feature
modifies the internal behaviour of two existing components.

---

## Modified: `AdmissionHandler` trait (Rust)

**File**: `gitstore-git-service/src/git/hooks/mod.rs`

The trait gains a `git_dir` parameter on `admit`. All implementors must be updated.

```
AdmissionHandler::admit(
    phase:         &str,
    updates:       &[RefUpdate],
    repository_id: &str,
    git_dir:       &Path,         // NEW — path to the bare repository on disk
) -> anyhow::Result<AdmissionDecision>
```

**Constraint**: `git_dir` may be an empty path when the handler is called from a
non-filesystem context (e.g., integration tests). Implementors MUST tolerate this by
falling back to `changed_paths: vec![]` rather than panicking.

---

## Modified: `AdmissionControlHandler` (Rust)

**File**: `gitstore-git-service/src/git/hooks/admission_handler.rs`

Gains internal path computation logic. No new public fields or constructor changes.

| Field | Type | Change |
|-------|------|--------|
| `client` | `CatalogServiceClient<Channel>` | Unchanged |
| `branch_pattern` | `Regex` | Unchanged |

**New internal function** (private, not exported):

```
fn compute_changed_paths(
    git_dir: &Path,
    old_oid: &str,
    new_oid: &str,
) -> Vec<String>
```

Behaviour:
- `old_oid` all-zeros → open repo, return all file paths from `new_oid` tree.
- `new_oid` all-zeros → open repo, return all file paths from `old_oid` tree.
- Both non-zero → open repo, return paths that differ between the two trees.
- Any gix error → log `error!`, return `vec![]`.
- `git_dir` empty or not a valid repo → return `vec![]`.

---

## Modified: `AdmitResourcesRequest` usage (Rust)

**File**: `gitstore-git-service/src/git/hooks/admission_handler.rs`

```
AdmitResourcesRequest {
    repository_id,
    commit_sha:     new_commit_sha.clone(),
    ref_name:       ref_name.clone(),
    old_commit_sha,
    new_commit_sha,
    changed_paths:  compute_changed_paths(git_dir, &old_oid, &new_oid),  // WAS vec![]
}
```

---

## Modified: `AdmitResources` handler (Go)

**File**: `gitstore-api/internal/cataloggrpc/server.go`

### Removed: legacy fallback branch

```go
// DELETED (lines 380–388):
if req.GetOldCommitSha() == "" {
    s.log.Warn("admit_resources: old commit absent; falling back to full-tree snapshot admission", ...)
    entries := s.loadParsedEntries(...)
    s.admitParsedEntries(ctx, entries, admCtx, nil)
    return &catalogv1.AdmitResourcesResponse{}, nil
}
```

The code path below this (diff-aware path) becomes the only path.

### Modified: `operationForEntry` return type

```go
// BEFORE:
func (s *Server) operationForEntry(
    ctx context.Context,
    e *parsedEntry,
    explicitOps map[string]resourceAdmissionOperation,
) (admission.Operation, bool)

// AFTER:
func (s *Server) operationForEntry(
    ctx context.Context,
    e *parsedEntry,
    explicitOps map[string]resourceAdmissionOperation,
) (admission.Operation, any, bool)
// Returns: operation, existing object (or nil for create), ok
```

The `existing` value returned here is passed into each admit function, eliminating the
second `store.GetXByName` call inside `admitProduct`, `admitCollection`,
`admitProductVariant`, and `admitCategoryTaxonomyWithContext`.

### Modified: `admitParsedEntries` — CategoryTaxonomy `catPushSet` construction

```go
// BEFORE: calls operationForEntry (DB read) for each category when building catPushSet
for _, e := range categoryEntries {
    siblingOp, ok := s.operationForEntry(ctx, e, explicitOps)
    ...
}

// AFTER: reads op directly from explicitOps (no DB call needed here)
for _, e := range categoryEntries {
    op, ok := explicitOps[e.identity.key()]
    if !ok { continue }
    ...
}
```

---

## Invariants preserved

| Invariant | How preserved |
|-----------|--------------|
| `loadParsedEntries` returns `nil` for all-zeros or empty ref | Unchanged (line 399 guard) |
| `deriveResourceAdmissionOperations` treats nil old/new entries as create/delete | Unchanged |
| Admission is fire-and-forget; errors do not reject pushes | Unchanged in both services |
| `operationForEntry` fallback to DB lookup when not in `explicitOps` | Preserved (the map lookup path is unchanged; the DB-fallback branch is retained for any future caller that passes a partial map) |
