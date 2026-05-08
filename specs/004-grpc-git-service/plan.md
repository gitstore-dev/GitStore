# Implementation Plan: Decouple API from Git Storage via gRPC Git Service

**Branch**: `004-grpc-git-service` | **Date**: 2026-05-06 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/004-grpc-git-service/spec.md`

## Summary

Replace the shared-volume + `go-git` coupling between `gitstore-api` and `gitstore-git-service` with a versioned gRPC contract. All git reads (file listing, raw file bytes, branch/tag enumeration) and writes (raw file commit, tag creation) performed by the API are routed through a gRPC service hosted by git-service. The API loses its shared volume mount and its `go-git` dependency; git-service gains a gRPC server alongside its existing git-protocol and websocket servers. Websocket-based release notifications are preserved unchanged.

## Technical Context

**Language/Version**:
- git-service: Rust 1.75+ (edition 2021)
- API: Go 1.25
- Proto toolchain: buf CLI (workspace managed at repo root)

**Primary Dependencies** (additions):
- git-service (Rust): `tonic` (gRPC server + build), `prost` (protobuf codegen), `tonic-reflection` (optional, for grpcurl), prometheus Rust client (`prometheus` crate) with tonic interceptor for per-RPC metrics
- API (Go): `google.golang.org/grpc`, `google.golang.org/protobuf`, `github.com/grpc-ecosystem/go-grpc-prometheus` (per-RPC Prometheus metrics middleware)
- Shared: `.proto` files in `shared/proto/` at repo root; `buf.work.yaml` + per-module `buf.yaml`

**Storage**: git-service owns the bare repository on disk. API has no repository storage.

**Testing**:
- Rust: `cargo test` + `tonic` in-process test server
- Go: `go test` with in-process gRPC server (no testcontainers needed — tonic server started in-process via `net.Pipe()` or a random localhost port)
- Contract tests: existing `tests/contract/` suite in API extended to cover all gRPC-backed operations

**Target Platform**: Linux container (Docker / Kubernetes)

**Performance Goals**:
- Catalogue load at startup: < 60s for up to 10,000 product files (SC-001)
- Catalogue reload after tag push: < 30s per API instance (SC-003, aligns with constitution 30s target)
- Single write mutation round-trip (API → git-service gRPC → commit → response): < 5s (consistent with constitution's "git push validation < 5s")

**Constraints**:
- Zero shared volume mount on API container after implementation (FR-004, FR-009)
- API must not import `go-git` for any repository mutation or catalogue read path (FR-005)
- Contract is versioned with package namespace `gitstore.git.v1`; both services deployed atomically on breaking changes
- gRPC call metrics (request count, latency histogram, error rate per RPC method) must appear on both services' Prometheus endpoints (FR-011)

**Scale/Scope**: Up to 10,000 catalogue files, up to 100 concurrent websocket connections, 1–5 concurrent write mutations

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-checked after Phase 1 design.*

| Principle                         | Status | Notes                                                                                                                                                   |
|-----------------------------------|--------|---------------------------------------------------------------------------------------------------------------------------------------------------------|
| I. Test-First                     | ✅ PASS | US4 (contract test coverage) is P4 but tasks will be ordered test-first per constitution: contract `.proto` first, then test stubs, then implementation |
| II. API-First                     | ✅ PASS | Proto contract defined in Phase 1 before any server or client code                                                                                      |
| III. Clear Contracts & Versioning | ✅ PASS | Package namespace `gitstore.git.v1`, atomic co-deployment enforced                                                                                      |
| IV. Observability                 | ✅ PASS | Per-RPC Prometheus metrics in scope (FR-011, SC-007)                                                                                                    |
| V. User Story Driven              | ✅ PASS | Four prioritised user stories; all tasks tagged US1–US4                                                                                                 |
| VI. Incremental Delivery          | ✅ PASS | P1 (scale without shared storage) delivers independently; P2/P3/P4 layer on top                                                                         |
| VII. Simplicity/YAGNI             | ✅ PASS | No backward-compat shim, no multi-version contract negotiation, no independent rolling upgrades — alpha-stage decision documented in spec               |

No violations. No complexity tracking entry required.

## Project Structure

### Documentation (this feature)

```text
specs/004-grpc-git-service/
├── plan.md              ← this file
├── research.md          ← Phase 0 output
├── data-model.md        ← Phase 1 output
├── quickstart.md        ← Phase 1 output
├── contracts/
│   └── gitstore.git.v1.proto   ← Phase 1 output
└── tasks.md             ← Phase 2 output (/speckit.tasks)
```

### Source Code (repository root)

```text
shared/
└── proto/
    ├── buf.work.yaml
    └── gitstore/
        └── git/
            └── v1/
                └── git_service.proto   ← single source of truth for contract

gitstore-git-service/
├── Cargo.toml                       ← add tonic, prost, prometheus deps
├── build.rs                         ← new: prost/tonic codegen from proto/
└── src/
    ├── grpc/
    │   ├── mod.rs
    │   ├── server.rs                ← tonic service impl
    │   └── metrics.rs               ← per-RPC prometheus interceptor
    ├── git/
    │   ├── mod.rs
    │   ├── repo.rs                  ← extended: read/write primitives for gRPC ops
    │   ├── hooks.rs
    │   ├── events.rs
    │   └── metrics.rs               ← existing repo size metrics
    ├── websocket/                   ← unchanged
    ├── http_git_server.rs           ← unchanged
    ├── lib.rs
    └── main.rs                      ← wire gRPC server alongside existing servers

gitstore-api/
├── go.mod                           ← add grpc, protobuf, go-grpc-prometheus; remove go-git
└── internal/
    ├── gitclient/                   ← replaced: go-git impl → gRPC client
    │   ├── grpc_client.go           ← new: gRPC connection + retry logic
    │   ├── read.go                  ← new: ReadFile, ListFiles, ListTags RPCs
    │   ├── write.go                 ← new: CommitFile, DeleteFile, CreateTag RPCs
    │   ├── metrics.go               ← new: grpc-prometheus middleware wiring
    │   └── (commit.go, push.go,     ← deleted: replaced by gRPC client
    │       pool.go, tag.go,
    │       writer.go, http_client.go)
    ├── catalog/
    │   └── loader.go                ← updated: replace go-git calls with gRPC client
    ├── graph/
    │   └── mutations.go             ← updated: replace CommitBuilder with gRPC write calls
    └── websocket/
        └── client.go                ← unchanged
```

**Structure Decision**: Two-service monorepo with shared `shared/proto/` directory at root managed by buf. The API's `internal/gitclient/` package is replaced wholesale; all other packages consume it through the same interface so changes are localised.

---

## Phase 0: Research

*See [research.md](research.md) for full findings. Summary of key decisions:*

### Decision 1 — Proto Toolchain

**Decision**: buf CLI with `buf.work.yaml` at repo root; `shared/proto/gitstore/git/v1/git_service.proto`

**Rationale**: buf enforces breaking-change detection, handles lint, and generates both Rust (via `buf generate` + `protoc-gen-prost` / `protoc-gen-tonic`) and Go code from a single `buf.gen.yaml`. Alternatives: raw `protoc` (no breaking-change detection, more boilerplate) — rejected.

### Decision 2 — Rust gRPC Server

**Decision**: `tonic` 0.12 + `prost` 0.13 + `tonic-build` in `build.rs`

**Rationale**: tonic is the de-facto async gRPC library for Rust/tokio, already used in the project's async runtime. Alternatives: `grpcio` (C binding, heavier) — rejected; `h2` direct (no codegen, no streaming support) — rejected.

### Decision 3 — Go gRPC Client

**Decision**: `google.golang.org/grpc` v1.x + `google.golang.org/protobuf` + buf-generated Go stubs

**Rationale**: Official Google gRPC Go library, best maintained, idiomatic with Go modules. `github.com/grpc-ecosystem/go-grpc-prometheus` for per-RPC metrics middleware. Alternatives: `connectrpc/connect-go` (different wire format, not native gRPC) — rejected.

### Decision 4 — per-RPC Prometheus Metrics

**Decision**:
- Rust (server): `tonic` interceptor pattern using `prometheus` crate; custom `CountingInterceptor` records `grpc_server_requests_total` (labels: method, status) and `grpc_server_duration_seconds` histogram
- Go (client): `go-grpc-prometheus` unary + stream interceptors registered on `grpc.Dial`; exposes `grpc_client_*` family

**Rationale**: Standard interceptor pattern for both sides keeps metrics instrumentation outside business logic. Consistent label set allows correlation across server and client dashboards.

### Decision 5 — Integration Testing

**Decision**: In-process gRPC server using a random TCP port (Go `net.Listen("tcp", "127.0.0.1:0")`); start a real git-service binary via `testcontainers-go` for cross-language integration tests

**Rationale**: In-process is faster for pure Go unit-level contract tests. Cross-language integration tests (Go API ↔ Rust git-service) require a real server binary; testcontainers-go is already available in the Go ecosystem and avoids `docker compose` in CI.

---

## Phase 1: Design & Contracts

*See [contracts/gitstore.git.v1.proto](contracts/gitstore.git.v1.proto) for the full contract. See [data-model.md](data-model.md) for entity mapping.*

### Service Contract Overview

The contract is defined once in `shared/proto/gitstore/git/v1/git_service.proto` and referenced from the contracts copy in this spec dir.

**Service**: `GitService`

| RPC Method      | Request               | Response               | Direction        | Notes                                      |
|-----------------|-----------------------|------------------------|------------------|--------------------------------------------|
| `GetFile`       | `GetFileRequest`      | `GetFileResponse`      | Unary            | Returns raw file bytes for a path at a ref |
| `ListFiles`     | `ListFilesRequest`    | `ListFilesResponse`    | Unary            | Lists file paths under a prefix at a ref   |
| `GetFileStream` | `GetFileRequest`      | `stream FileChunk`     | Server-streaming | For large files; used for catalogue load   |
| `CommitFile`    | `CommitFileRequest`   | `CommitFileResponse`   | Unary            | Write a single file and commit             |
| `DeleteFile`    | `DeleteFileRequest`   | `DeleteFileResponse`   | Unary            | Delete a file and commit                   |
| `CreateTag`     | `CreateTagRequest`    | `CreateTagResponse`    | Unary            | Create an annotated tag on HEAD            |
| `ListTags`      | `ListTagsRequest`     | `ListTagsResponse`     | Unary            | Enumerate tags with optional prefix filter |
| `GetLatestTag`  | `GetLatestTagRequest` | `GetLatestTagResponse` | Unary            | Returns the latest semver release tag      |

All messages carry a `string version = 1` field at the service level (set to `"v1"` by caller); the server rejects mismatched versions with `FAILED_PRECONDITION`.

### Catalogue Load Flow (post-implementation)

```
API startup
  │
  ├─► gRPC: ListTags(prefix="v")
  │      ← [tag list]
  ├─► gRPC: GetLatestTag()
  │      ← {tag: "v1.2.0", commit: "abc123"}
  ├─► gRPC: ListFiles(ref="v1.2.0", prefix="")
  │      ← [file paths]
  ├─► gRPC: GetFile(ref="v1.2.0", path="products/…") × N  (or GetFileStream for large repos)
  │      ← {bytes: …}
  └─► Catalogue parsed in API (GH#105 parsing layer)
```

### Write Mutation Flow (post-implementation)

```
GraphQL mutation (createProduct)
  │
  ├─► API builds raw markdown bytes (writer.go logic preserved, file I/O removed)
  ├─► gRPC: CommitFile(path, bytes, message, author)
  │      ← {commit_sha: "…"}
  └─► Return result to caller
      (git-service handles temporary clone internally)
```

### Websocket Reload Flow (unchanged externally)

```
git push release tag
  └─► git-service emits WS event {event:"tag_push", tag:"v1.2.1"}
        └─► API receives WS notification
              └─► Triggers catalogue reload via gRPC (same as startup flow from GetLatestTag)
```

---

## Complexity Tracking

No constitution violations. The gRPC addition is justified: it is the explicit goal of GH#65 and the only way to eliminate shared-volume coupling across replicas. No alternative within the existing HTTP/WebSocket architecture achieves the same result without introducing a full file-transfer protocol.
