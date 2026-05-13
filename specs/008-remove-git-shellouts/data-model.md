# Data Model: Remove Git Binary Shell-Outs

**Branch**: `008-remove-git-shellouts` | **Date**: 2026-05-13

This feature is a pure refactor — no new domain entities are introduced and no existing entities change shape. The section below documents the runtime configuration additions and the in-process protocol handler boundary that replaces the shell-out call sites.

---

## New Configuration Fields

### `GitServerConfig` (extension to existing config struct)

| Field                 | Type  | Description                                                                                                                          | Default              |
|-----------------------|-------|--------------------------------------------------------------------------------------------------------------------------------------|----------------------|
| `max_pack_size_bytes` | `u64` | Maximum accepted pack body size for push operations. Pushes exceeding this limit are rejected with HTTP 413 before any write begins. | `52_428_800` (50 MB) |

**Validation rule**: Must be > 0. A value of `0` is treated as "use default". Configuration key: `GITSTORE_GIT__MAX_PACK_SIZE_BYTES`.

---

## In-Process Protocol Handler (Internal Boundary)

The `HttpPackServer` struct replaces the four `std::process::Command` call sites. It holds no persistent state beyond a reference to the repository path and is not persisted to any store.

### `HttpPackServer`

| Field           | Type      | Description                                       |
|-----------------|-----------|---------------------------------------------------|
| `repo_path`     | `PathBuf` | Canonicalized path to the bare repository on disk |
| `max_pack_size` | `u64`     | Enforced limit on incoming pack stream size       |

**Methods (replacing shell-outs)**:

| Method                                                       | Replaces                            | Output                                   |
|--------------------------------------------------------------|-------------------------------------|------------------------------------------|
| `advertise_upload_pack_refs(&self) -> Result<Vec<u8>>`       | `git upload-pack --advertise-refs`  | pkt-line encoded ref list + capabilities |
| `handle_upload_pack(&self, body: &[u8]) -> Result<Vec<u8>>`  | `git upload-pack --stateless-rpc`   | pack data stream                         |
| `advertise_receive_pack_refs(&self) -> Result<Vec<u8>>`      | `git receive-pack --advertise-refs` | pkt-line encoded ref list + capabilities |
| `handle_receive_pack(&self, body: &[u8]) -> Result<Vec<u8>>` | `git receive-pack --stateless-rpc`  | pack-protocol result stream              |

**Atomicity contract**: `handle_receive_pack` MUST use `gix::refs::transaction` to apply all ref updates as a single atomic transaction. If pack writing or any ref edit fails, the transaction is rolled back and a pkt-line error band message is written to the response; no partial state is left on disk.

---

## Structured Log Events (FR-012)

Each method on `HttpPackServer` emits a `tracing::info!` span with the following fields. No schema change to external stores.

| Field             | Type   | Present on                                                                                       |
|-------------------|--------|--------------------------------------------------------------------------------------------------|
| `repo`            | `&str` | All four operations                                                                              |
| `operation`       | `&str` | `"upload-pack-advertise"`, `"upload-pack-rpc"`, `"receive-pack-advertise"`, `"receive-pack-rpc"` |
| `duration_ms`     | `u64`  | All four operations (measured from handler entry to response written)                            |
| `outcome`         | `&str` | `"ok"` or error variant name                                                                     |
| `pack_size_bytes` | `u64`  | `receive-pack-rpc` only                                                                          |
