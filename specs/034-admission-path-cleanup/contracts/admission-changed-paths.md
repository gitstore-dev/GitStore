# Contract: AdmitResources `changed_paths` Population

**Feature**: `034-admission-path-cleanup`  
**Protocol**: gRPC (existing `AdmitResourcesRequest` proto, field 5)  
**Status**: Additive — no breaking changes to existing proto or API

---

## Proto field (existing, unchanged)

```protobuf
// AdmitResourcesRequest — gitstore/catalog/v1/catalog_service.proto
message AdmitResourcesRequest {
  string repository_id  = 1;
  string commit_sha     = 2;  // deprecated alias for new_commit_sha
  string old_commit_sha = 3;
  string ref_name       = 4;
  repeated string changed_paths = 5;  // THIS FIELD IS NOW ALWAYS POPULATED
  string new_commit_sha = 6;
}
```

---

## Sender contract (Rust git-service)

After Phase 1 ships, every `AdmitResourcesRequest` sent by the git service MUST satisfy:

| Condition | `old_commit_sha` | `changed_paths` |
|-----------|-----------------|-----------------|
| New branch (first push) | all-zeros (40 × `0`) | all file paths in `new_commit_sha` tree |
| Branch update (existing branch) | previous tip SHA | paths that differ between `old_commit_sha` and `new_commit_sha` trees |
| Branch deletion | previous tip SHA | all file paths in `old_commit_sha` tree |
| gix error / empty git_dir | unchanged (real or all-zeros) | `[]` (fallback) |

**`old_commit_sha` guarantee**: After Phase 1, `old_commit_sha` is NEVER an empty string. It is either a 40-character real SHA or 40 × `0`.

---

## Receiver contract (Go API)

The Go `AdmitResources` handler MUST process every request via the diff-aware path:

```
oldEntries = loadParsedEntries(old_commit_sha, changed_paths)
newEntries = loadParsedEntries(new_commit_sha, changed_paths)
ops        = deriveResourceAdmissionOperations(oldEntries, newEntries, changed_paths)
applyResourceOperations(ops)
```

**`changed_paths` semantics on the Go side** (existing, unchanged):
- Empty `changed_paths` → `loadParsedEntries` reads ALL files from the commit (full scan).
- Non-empty `changed_paths` → `loadParsedEntries` filters to only those paths (fast-path).

The legacy `if old_commit_sha == ""` branch MUST NOT be present after Phase 2.

---

## Ordering constraint

Phase 2 (Go legacy removal) MUST NOT be deployed before Phase 1 (Rust `changed_paths` population) is deployed and verified. If deployed out of order, any push from an unupgraded git-service would result in `old_commit_sha == ""` reaching the API, which would now have no handler and would silently return without admitting anything.

**Verification signal**: After Phase 1 deploys, the log line  
`"admit_resources: old commit absent; falling back to full-tree snapshot admission"`  
MUST never appear. Its absence confirms Phase 2 is safe to deploy.
