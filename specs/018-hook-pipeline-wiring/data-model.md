# Data Model: Hook Pipeline Wiring (spec#018)

## Rust — gitstore-git-service

### New: ResourceBlob

```rust
/// Raw bytes of a candidate resource file extracted from a push commit.
/// Any file beginning with `---` qualifies; kind/apiVersion inside determine routing.
pub struct ResourceBlob {
    pub path: String,       // repository-relative path (e.g. "products/widget.md")
    pub blob_oid: String,   // git object ID of the blob
    pub content: Vec<u8>,   // raw file bytes
}
```

Extracted by `HookPipeline` via gix tree-walk over the incoming commit's tree in the quarantine area. The `---` prefix check is done locally before sending to the validation service.

### Modified: HookPipeline

```rust
pub struct HookPipeline {
    // existing
    pub config: GitReceivePackHooks,

    // new: schema validation slot (blocking, pre-receive default)
    pub schema_validation_phase: String,
    pub schema_validation_timeout: Duration,
    pub validation_handler: Arc<dyn ValidationHandler + Send + Sync>,

    // new: admission control slot (fire-and-forget, post-receive default)
    pub admission_control_phase: String,
    pub admission_branch_pattern: String,
    pub admission_handler: Arc<dyn AdmissionHandler + Send + Sync>,

    // context needed by admission handler
    pub repository_id: String,
}
```

### New: ValidationHandler trait

```rust
#[async_trait]
pub trait ValidationHandler: Send + Sync {
    async fn validate(&self, blobs: &[ResourceBlob]) -> anyhow::Result<AdmissionDecision>;
}

// No-op default (used in tests and when schema validation is disabled)
pub struct NoopValidationHandler;
```

`AdmissionHandler` (spec#013) is **unchanged**.

### Modified: Config structs

```rust
// Replaces AdmissionControlConfig
pub struct SchemaValidationConfig {
    pub phase: String,          // default: "pre-receive"
    pub timeout_secs: u64,      // default: 10
}

pub struct AdmissionControlConfig {
    pub phase: String,          // default: "post-receive"
    pub branch_pattern: String, // default: "refs/heads/main"
}

pub struct CatalogServiceConfig {
    pub url: String,            // default: "http://localhost:4000"
}
```

Startup validation (FR-019): `schema_validation.phase != admission_control.phase` — service refuses to start if equal.

### New: SchemaValidationHandler

```rust
pub struct SchemaValidationHandler {
    client: CatalogServiceClient<tonic::transport::Channel>,
    timeout: Duration,
}
```

- Implements `ValidationHandler`.
- Calls `CatalogService.ValidateResources` gRPC, passing `repeated ResourceBlob`.
- Returns `AdmissionDecision::Accept` or `AdmissionDecision::Reject(aggregated_errors)`.
- On timeout or transport error: returns `AdmissionDecision::Reject("validation service unavailable")`.
- Emits structured log: `validation_callout_complete` with `file_count`, `outcome`, `duration_ms`, `error_summary`.
- Increments counter metric `gitstore_schema_validation_total{result=accepted|rejected|timeout|service_unavailable}`.

### New: AdmissionControlHandler

```rust
pub struct AdmissionControlHandler {
    client: CatalogServiceClient<tonic::transport::Channel>,
}
```

- Implements `AdmissionHandler` (existing trait, post-receive slot).
- Filters by `admission_branch_pattern` — if the ref does not match, returns `Accept` immediately without calling gRPC.
- Calls `CatalogService.AdmitResources` gRPC with `repository_id`, `commit_sha`, `ref_name`.
- Fire-and-forget: spawned as a `tokio::spawn` task; errors logged at ERROR, never propagated.

---

## Proto — shared/proto/gitstore/catalog/v1/catalog_service.proto

New file. Package `gitstore.catalog.v1`.

```protobuf
service CatalogService {
  // Validate validates resource blobs extracted from a push commit.
  // Blocking: called in pre-receive. Returns all violations across all blobs.
  rpc ValidateResources(ValidateResourcesRequest)
      returns (ValidateResourcesResponse);

  // AdmitResources stores validated resources into the catalog.
  // Fire-and-forget: called in post-receive. Git service does not wait.
  rpc AdmitResources(AdmitResourcesRequest)
      returns (AdmitResourcesResponse);
}

message ResourceBlob {
  string path     = 1; // repository-relative path
  string blob_oid = 2; // git object ID
  bytes  content  = 3; // raw bytes
}

message ValidationError {
  string path       = 1; // dotted field path, e.g. "spec.title"
  string constraint = 2; // rule violated, e.g. "max=200"
  string message    = 3; // human-readable explanation
}

message ValidateResourcesRequest {
  string                repository_id = 15;
  repeated ResourceBlob blobs         = 1;
}

message ValidateResourcesResponse {
  bool                     accepted = 1;
  repeated ValidationError errors   = 2; // non-empty only when accepted=false
}

message AdmitResourcesRequest {
  string repository_id = 15;
  string commit_sha    = 1;  // SHA of the accepted push commit
  string ref_name      = 2;  // e.g. "refs/heads/main"
}

message AdmitResourcesResponse {
  // Empty — fire-and-forget; the git service does not inspect this.
}
```

---

## Go — gitstore-api

### New: CatalogService gRPC server

Package: `internal/cataloggrpc` (or `internal/hook`)

Implements `catalogv1.CatalogServiceServer`:

**`ValidateResources`**:
- Iterate blobs; for each, call `validate.Parse(bytes.NewReader(blob.Content))`.
- Collect all `ParseError` fields into `repeated ValidationError`.
- Return `accepted=true` if no errors, else `accepted=false` with full error list.

**`AdmitResources`**:
- Receives `repository_id`, `commit_sha`, `ref_name`.
- Calls `gitclient.ListFiles` (existing RPC) to enumerate files at `commit_sha`.
- For each file: calls `gitclient.GetFile` → `validate.Parse` → constructs `datastore.Product`.
- Calls `Datastore.GetProductByName`; if found: `UpdateProduct`, else `CreateProduct`.
- Assigns system-managed fields (FR-008): `uid` (UUIDv7, immutable on update), `resourceVersion` (increment), `generation` (increment), `creationTimestamp` (immutable on update), `revision` = `<branch>@sha1:<commit_sha>`.
- Writes initial `AdmissionAccepted: True` condition (FR-009).
- Each product processed independently; failures logged, not propagated (FR-011).

### Entity: revision format

`revision` stored as `<branch_name>@sha1:<commit_sha>`, e.g. `main@sha1:a1b2c3d4e5f6`.
- Branch name extracted by stripping `refs/heads/` prefix from `ref_name`.
- Provides human-readable audit trail: branch context + exact commit without extra lookup.

### State transition: Product on AdmitResources

```
[no record]  → CreateProduct → {generation=1, uid=new UUIDv7, AdmissionAccepted:True}
[exists]     → UpdateProduct → {generation++, resourceVersion++, uid preserved, creationTimestamp preserved}
```

---

## Metrics (gitstore-git-service)

| Metric name | Type | Labels | Description |
|---|---|---|---|
| `gitstore_schema_validation_total` | Counter | `result={accepted,rejected,timeout,service_unavailable}` | Pre-receive callout outcomes |

No new metrics on the Go API side for this feature (existing datastore metrics cover the write path).
