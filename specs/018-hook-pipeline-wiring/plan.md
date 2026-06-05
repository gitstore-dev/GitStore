# Implementation Plan: Hook Pipeline Wiring — Pre-Receive Validation and Post-Receive Admission

**Branch**: `018-hook-pipeline-wiring` | **Date**: 2026-06-05 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/018-hook-pipeline-wiring/spec.md`

## Summary

Wire the existing `NoopAdmissionHandler` stubs in `gitstore-git-service` to real gRPC callouts against `gitstore-api`: a blocking `SchemaValidationHandler` (pre-receive, calls `CatalogService.ValidateResources`) and a fire-and-forget `AdmissionControlHandler` (post-receive, calls `CatalogService.AdmitResources`). A new `gitstore.catalog.v1` proto service is the API-first contract between the two services. This unblocks all existing integration tests in `tests/integration/` which currently fail because the pipeline is no-op.

## Technical Context

**Language/Version**: Rust edition 2021, MSRV 1.82 (`gitstore-git-service`); Go 1.25 (`gitstore-api`)
**Primary Dependencies**:
- Rust: `gix 0.84.0`, `tonic 0.14.6`, `tokio 1.35`, `tracing 0.1`, `prometheus 0.14.0`, `async-trait 0.1`, `config 0.15.22`
- Go: `gqlgen v0.17.90`, `google.golang.org/grpc v1.81.1`, `google.golang.org/protobuf v1.36.11`, `go.uber.org/zap`, `github.com/google/uuid v1.6.0`, `go-playground/validator/v10`
- Proto toolchain: buf v2 (`buf.gen.go.yaml` → `gitstore-api/gen/`, `buf.gen.rust.yaml` → `gitstore-git-service/gen/`)

**Storage**: Existing `Datastore` interface (`CreateProduct` / `UpdateProduct`) — no schema changes

**Testing**:
- Rust: `cargo test` (unit), integration test at `gitstore-git-service/tests/integration/mod.rs`
- Go: `go test ./...` (unit + contract), `tests/integration/` (integration, requires running stack)

**Target Platform**: Linux server (Docker Compose stack for local dev)

**Performance Goals**: Pre-receive callout + push accepted in < 5 seconds for 100-file push (SC-002); catalog queryable within 5 seconds of push (SC-003)

**Constraints**: `ValidationHandler` must use the quarantine-area objects (gix pre-receive access); `AdmitResources` must not block the git push response (FR-012)

**Scale/Scope**: Up to 100 product files per push (SC-002); up to 10,000 products in catalog

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Notes |
|---|---|---|
| I. Test-First | PASS | Integration tests already exist and are failing (red). Unit tests written before implementation per task ordering. |
| II. API-First | PASS | `catalog_service.proto` contract defined before any implementation task. |
| III. Clear Contracts | PASS | Proto file versioned in `shared/proto/gitstore/catalog/v1/`. Additive only; no existing RPCs changed. |
| IV. Observability | PASS | `gitstore_schema_validation_total` counter + structured log per callout defined in data-model. |
| V. User Story Driven | PASS | All tasks labelled US1/US2/US3. |
| VI. Incremental Delivery | PASS | Pre-receive (US1) and post-receive (US2) are independently deployable milestones. |
| VII. Simplicity | PASS | Blob extraction is local (gix, no new infra); single new proto service; no new dependencies beyond existing tonic/gRPC stack. |

**Post-design re-check**: No violations. The `ValidationHandler` trait addition is the minimal change that avoids breaking spec#013's `AdmissionHandler` contract. The reverse callout (git-service → gitstore-api) is justified because validation logic lives in Go (`validate.Parse`) and must not be duplicated in Rust.

## Project Structure

### Documentation (this feature)

```text
specs/018-hook-pipeline-wiring/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/
│   └── catalog_service.proto   # Phase 1 output — API-first contract
└── tasks.md             # Phase 2 output (/speckit.tasks command)
```

### Source Code

```text
# Proto (new)
shared/proto/gitstore/catalog/v1/
└── catalog_service.proto

# gitstore-git-service (Rust)
gitstore-git-service/
├── build.rs                              # enable build_client=true for catalog proto
├── gitstore.toml                         # updated config defaults
└── src/
    ├── config.rs                         # restructured config structs
    ├── git/
    │   ├── hooks.rs                      # ValidationHandler trait, ResourceBlob, HookPipeline restructure
    │   ├── hooks/
    │   │   ├── validation_handler.rs     # SchemaValidationHandler (NEW)
    │   │   └── admission_handler.rs      # AdmissionControlHandler (NEW)
    │   └── metrics.rs                    # gitstore_schema_validation_total counter
    └── main.rs                           # wiring: config → handlers → HookPipeline

# gitstore-api (Go)
gitstore-api/
├── gen/gitstore/catalog/v1/              # buf-generated stubs (NEW)
├── internal/
│   └── cataloggrpc/
│       ├── server.go                     # CatalogServiceServer (NEW)
│       └── server_test.go               # unit tests (NEW)
└── cmd/server/main.go                    # register CatalogServiceServer on gRPC listener

# Integration tests (existing, newly passing)
tests/integration/
└── product_lifecycle_test.go             # unchanged — these are the acceptance tests
```

**Structure Decision**: Two-service polyglot layout (Rust + Go). New `internal/cataloggrpc/` package in gitstore-api isolates hook callout logic from GraphQL resolvers. New `src/git/hooks/` submodule in git-service isolates the two concrete handler implementations.

## Complexity Tracking

No constitution violations requiring justification.
