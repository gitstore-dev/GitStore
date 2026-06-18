# Data Model: Branch Deletion Admission (spec 028)

No new datastore tables, proto fields, or Go types are introduced by this spec. The fix
is entirely within the Rust git service's `receive_pack` request handler.

---

## Affected Types (unchanged — listed for cross-reference)

### `RefUpdate` (Rust — `gitstore-git-service/src/git/hooks/mod.rs`)

| Field | Type | Notes |
|---|---|---|
| `ref_name` | `String` | Fully-qualified ref, e.g. `refs/heads/main` |
| `old_oid` | `String` | 40-char hex; all-zeros = ref did not previously exist |
| `new_oid` | `String` | 40-char hex; **all-zeros = branch deletion** |

A branch-delete push produces a `RefUpdate` where `new_oid` is
`"0000000000000000000000000000000000000000"`.

### `AdmitResourcesRequest` (proto — `gitstore/catalog/v1/catalog_service.proto`)

| Field | Type | Notes |
|---|---|---|
| `repository_id` | `string` | UUID of the repository |
| `commit_sha` | `string` | Legacy field; superseded by `new_commit_sha` |
| `ref_name` | `string` | Fully-qualified ref |
| `old_commit_sha` | `string` | SHA before the push |
| `new_commit_sha` | `string` | SHA after the push; **all-zeros = branch deletion** |
| `changed_paths` | `[]string` | Paths changed in push; empty for branch deletion |

The Go `AdmitResources` handler already interprets `new_commit_sha == "0000...0"` as a
branch-delete and runs `deriveResourceAdmissionOperations` against the old entries only,
generating `OperationDelete` for every resource that was on the deleted branch.

---

## Control Flow Change (Rust `receive_pack` handler)

The only code change is a guard in the `receive_pack` method of
`gitstore-git-service/src/grpc/server.rs`.

**Before** (broken):

```
receive_pack
  ├── parse ref_commands from first chunk
  ├── bridge remaining chunks to sync channel
  ├── [unconditional] stage_pack_from_reader(channel_reader)  ← fails on empty stream
  ├── build ref_updates from ref_commands
  ├── nff validation
  ├── hook pipeline
  └── ref transaction
```

**After** (fixed):

```
receive_pack
  ├── parse ref_commands from first chunk
  ├── detect is_delete_only = all ref_commands have new_oid == zero
  ├── if !is_delete_only: bridge chunks + stage_pack_from_reader → Some(quarantine)
  ├── else: quarantine = None  (no pack to stage)
  ├── build ref_updates from ref_commands
  ├── nff validation
  ├── hook pipeline (admission_handler forwards zero-new-OID to AdmitResources)
  └── ref transaction (Change::Delete applied via gix)
```

The `promote_quarantine` call inside the transaction commit arm is already conditioned on
`quarantine` being `Some` (line 1022: `promote_quarantine(&repo, quarantine)`). With
`quarantine = None`, the promote step is a no-op and the ref transaction commits cleanly.
