# Research: Repository Storage Identity and Path Strategy

**Branch**: `010-repo-storage-identity` | **Date**: 2026-05-26

## Decision Log

---

### D-001: UUID variant for `repo_id` and `namespace_id`

**Decision**: UUIDv7 for both `repo_id` and `namespace_id`.

**Rationale**: `github.com/google/uuid v1.6.0` (already in go.mod) exposes `uuid.NewV7()`. UUIDv7 is time-ordered (48-bit millisecond timestamp prefix), which improves ScyllaDB partition locality and avoids the random I/O scatter of UUIDv4 on range scans. Both fields are assigned once at creation and never change, so monotonicity is a free benefit. UUIDv7 is the IETF-standardised choice for database-friendly identifiers.

**Alternatives considered**:
- UUIDv4: simpler, but random distribution hurts ScyllaDB write performance at scale.
- UUIDv1: time-ordered but leaks MAC address; deprecated for new designs.
- Sequential integer: requires a coordination point (counter or sequence); breaks in distributed setup.

---

### D-002: Fanout path strategy for storage layout

**Decision**: Two-level hex-prefix fanout — `{data_dir}/{xx}/{yy}/{repo_id}.git`

Where `xx` = first two hex characters of the UUID (hyphens stripped), `yy` = next two hex characters.

Example: UUID `0196f3a2-4b1c-7e9d-a301-8b2c4d5e6f7a` → `{data_dir}/01/96/0196f3a2-4b1c-7e9d-a301-8b2c4d5e6f7a.git`

**Rationale**: GitLab Gitaly uses `@hashed/{xx}/{yy}/{sha256(project_id)}.git`. Since our `repo_id` is already a UUID (essentially random/time-based), hashing it again adds no entropy and complicates debugging. Using the UUID directly with a two-level prefix gives 256² = 65,536 possible leaf-level directories, which is sufficient to avoid large flat-directory hotspots at any realistic scale. The path is fully deterministic from the UUID alone — no external state required to compute it.

**Alternatives considered**:
- Single-level fanout (`{data_dir}/{xx}/{repo_id}.git`): 256 top-level dirs; sufficient for ALPHA but GitLab convention uses two levels.
- Hash-then-fanout (GitLab Gitaly style): extra SHA256 step adds no value when repo_id is already a UUID.
- Flat layout (`{data_dir}/{repo_id}.git`): single directory with unlimited entries — fails at scale on most filesystems.

---

### D-003: Migration consolidation

**Decision**: Update `001_initial_schema.cql` and `002_add_initial_indices.cql` in place. Do not create new migration files.

**Rationale**: The application is early ALPHA with no production data. There is no constraint requiring additive-only migrations. Updating the existing files keeps the schema definition minimal and avoids migration-version machinery overhead for a state that will never be rolled back from. The user explicitly confirmed this approach.

**Alternatives considered**:
- New `003_add_repositories.cql` + `004_add_repo_indices.cql`: standard practice for live systems; unnecessary overhead for ALPHA.

---

### D-004: `repository_id` field semantics in gRPC proto

**Decision**: The existing `repository_id` field (field 15 on all request messages, field 1 on lifecycle messages) transitions from a human-readable repo name to an opaque stable UUID (`repo_id`). No field number changes. Comment/documentation updated.

**Rationale**: The field name `repository_id` already implies an opaque identity, not a namespace path. `gitstore-git-service` never needs to see namespace strings. The gRPC boundary enforces the `repo_id`-only contract. No existing callers are in production, so the semantic shift carries no migration cost.

**Alternatives considered**:
- Add a new field `repo_uuid` alongside `repository_id`: creates ambiguity and bloats the proto; unnecessary since no production callers exist.
- Rename to `repo_id` in a new proto version (v2): premature; the existing name is unambiguous with updated documentation.

---

### D-005: `StoragePath` is derived, not stored

**Decision**: The filesystem path is always computed on-the-fly from `repo_id` using the fanout formula. It is never persisted to the database.

**Rationale**: Storing the path would create a consistency hazard — if the formula ever changes, stored paths would diverge from computed paths. Since the formula is deterministic and depends only on `repo_id` (which is immutable), there is never a reason to store the result. The Rust path resolver is a pure function: `(data_dir, repo_id) → PathBuf`.

---

### D-006: `NamespaceMapping` as a first-class datastore entity

**Decision**: `NamespaceMapping` is a separate entity with its own table (`namespace_mappings`), not a nested collection on `Namespace`.

**Rationale**: In ScyllaDB, a compound primary key `(namespace_id, name)` → `repo_id` gives O(1) lookup by namespace + name — the exact query pattern used by every inbound git request. Embedding mappings in the Namespace row (e.g., as a `map<text, uuid>`) would require loading the entire Namespace row for every git operation, prevents efficient secondary indexing on `repo_id`, and complicates atomic rename/transfer operations.

In memdb, the same entity is stored in a `namespace_mapping` table with a CompoundIndex on `(NamespaceID, Name)` for primary lookup and a separate index on `RepoID` for reverse lookups.

---

### D-007: Rename and transfer atomicity

**Decision**: For ALPHA, rename and transfer are modelled as a delete-old + insert-new on the `namespace_mappings` table, wrapped in the datastore layer. No distributed transaction is used.

**Rationale**: go-memdb transactions are fully ACID in-memory. For ScyllaDB, a lightweight-transaction (LWT) CAS approach is appropriate but adds complexity. For ALPHA with a single-node ScyllaDB and no concurrent admin traffic, a sequential delete+insert is sufficient. The datastore interface exposes `RenameRepository` and `TransferRepository` as atomic methods; the implementation strategy is a planning detail, not a spec requirement.

---

### D-008: Existing `confine_repo_path` (HTTP) vs `resolve_repo_path` (gRPC) in git-service

**Current state**: 
- `confine_repo_path` (HTTP): takes a human-readable `repo` string, joins it to `data_root`, calls `canonicalize()` to prevent path traversal — **to be removed** when HTTP Smart HTTP moves to `gitstore-api` in #103.
- `resolve_repo_path` (gRPC): takes `id` string, joins to `data_root` as `{data_root}/{id}.git` — **to be replaced** with the fanout formula.

**Decision**: Replace `resolve_repo_path` with the new fanout-based resolver. `confine_repo_path` can be removed with #103 (out of scope here).

The new resolver validates that `id` is a well-formed UUID (36-char hyphenated format) before path construction. Path traversal protection becomes trivially safe since a UUID cannot contain `/` or `..`.
