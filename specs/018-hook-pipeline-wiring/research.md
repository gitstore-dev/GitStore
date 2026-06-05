# Research: Hook Pipeline Wiring (spec#018)

## Proto / Codegen

- **Decision**: New proto file `shared/proto/gitstore/catalog/v1/catalog_service.proto` with `package gitstore.catalog.v1`.
- **Rationale**: The existing `gitstore.git.v1` file is git-primitive only; mixing hook/catalog concerns violates its additive-only discipline and buf `FILE`-level breaking rules.
- **Toolchain**: buf v2 exclusively (`buf.gen.go.yaml` → `gitstore-api/gen/`, `buf.gen.rust.yaml` → `gitstore-git-service/gen/`). No raw `protoc`.
- **Field-number convention**: `repository_id` at field 15; logical-domain fields from 1 upward. Follow this in new messages.
- **Go package**: `github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1;catalogv1`

## Call Direction: git-service → gitstore-api (NEW)

- **Finding**: Current flow is strictly gitstore-api → gitstore-git-service. The git service has `build_server=true` / `build_client=false` in `build.rs` — no outbound gRPC client exists today.
- **Decision**: This feature adds the first reverse callout: gitstore-git-service (Rust, tonic client) → gitstore-api (Go, tonic server). The buf Rust codegen must enable `build_client=true` for the new catalog proto. The existing git_service proto `build_client=false` stays unchanged.
- **Alternatives considered**: Embedding validation logic in the git service (rejected — duplicates `validate.Parse()` in Rust, would require porting Go validation); using a sidecar/queue (rejected — adds infrastructure complexity, contradicts Constitution Principle VII).

## Blob Extraction: Pre-Receive Quarantine

- **Finding**: gix objects pushed by a client are in a quarantine area before pre-receive completes. They are accessible via the `new_oid` in each `RefUpdate` using gix object readers, but are NOT accessible via the existing `GetFile` / `ListFiles` RPCs (those read from committed refs only).
- **Decision**: The git service extracts blob bytes locally using gix tree-walking on the quarantine objects, constructs `ResourceBlob` messages, and sends them to `ValidateResources`. The Go API cannot fetch them itself at this point.
- **Post-receive**: After refs are committed, gitstore-api CAN call `GetFile`/`ListFiles` itself. So `AdmitResources` sends only `repository_id` + `commit_sha` + `ref_name`; gitstore-api fetches and parses blobs independently.

## Trait Architecture: ValidationHandler vs AdmissionHandler

- **Finding**: The existing `AdmissionHandler::admit(phase, updates)` trait does not carry `git_dir` or pre-extracted blobs. The `SchemaValidationHandler` needs blobs (from quarantine) before making the gRPC call. Extending the existing trait would require changing `NoopAdmissionHandler` and all callers.
- **Decision**: Introduce a **new** `ValidationHandler` trait for the pre-receive path:
  ```rust
  async fn validate(&self, blobs: &[ResourceBlob]) -> anyhow::Result<AdmissionDecision>;
  ```
  Blob extraction (gix tree-walk + frontmatter detection) is the `HookPipeline`'s responsibility, done before calling `validate()`. The existing `AdmissionHandler` trait is **unchanged** and is used only by `AdmissionControlHandler` (post-receive).
- **Alternatives considered**: Adding `git_dir` parameter to `AdmissionHandler` (rejected — breaks spec#013 assumption and requires updating all implementations); constructing per-request handlers (rejected — `Arc<dyn Handler>` is shared, not per-request).

## Config Restructure

- **Finding**: `AdmissionControlConfig.validating_admission_policy.phase` is a single config key (FR-015 requires its removal). The double-underscore env separator means `GITSTORE_ADMISSION_CONTROL__VALIDATING_ADMISSION_POLICY__PHASE` is unwieldy.
- **Decision**:
  - Remove `[admission_control.validating_admission_policy]`
  - Add `[schema_validation]` with `phase` (default: `"pre-receive"`) and `timeout_secs` (default: 10)
  - Add new `[admission_control]` top-level with `phase` (default: `"post-receive"`) and `branch_pattern` (default: `"refs/heads/main"`)
  - Add `[catalog_service]` with `url` (address of gitstore-api gRPC endpoint, default: `"http://localhost:4000"`)
- **Env vars**: `GITSTORE_SCHEMA_VALIDATION__PHASE`, `GITSTORE_ADMISSION_CONTROL__PHASE`, `GITSTORE_ADMISSION_CONTROL__BRANCH_PATTERN`, `GITSTORE_CATALOG_SERVICE__URL`

## HookPipeline Restructure

- **Current shape**: single `admission_phase: String` + single `admission_handler: Arc<dyn AdmissionHandler>`
- **New shape**:
  - `schema_validation_phase: String`
  - `schema_validation_handler: Arc<dyn ValidationHandler>`
  - `schema_validation_timeout: Duration`
  - `admission_control_phase: String`
  - `admission_control_handler: Arc<dyn AdmissionHandler>`
  - `admission_branch_pattern: String`
  - `repository_id: String` (needed by `AdmissionControlHandler` to construct the gRPC request)
- `run_phase_with_admission` splits into `run_schema_validation` (called at `schema_validation_phase`) and spawn logic in `run_post_receive` (called at `admission_control_phase`).

## Integration Tests

- **Finding**: `tests/integration/product_lifecycle_test.go` already has `TestProductLifecycle_ValidFile_AcceptedAndQueryable`, `TestProductLifecycle_InvalidTitle_PushRejected`, `TestProductLifecycle_StatusPresent_PushRejected`, `TestProductLifecycle_MissingFileRefName_PushRejected`. These are the primary acceptance tests for this feature and currently fail because the pipeline is no-op.
- **Decision**: No new integration test files needed for the core push-lifecycle scenarios. New unit tests cover: `ValidationHandler` mock, `AdmissionControlHandler` branch filtering, config startup validation, and the zero-hash new-branch case.
