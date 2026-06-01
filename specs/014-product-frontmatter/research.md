# Research: Product Resource Contract — Kubernetes-style Frontmatter Schema

**Branch**: `014-product-frontmatter` | **Phase**: 0 | **Date**: 2026-06-01

## Decision Log

---

### D-001: Frontmatter Parsing Strategy

**Decision**: Typed Go structs with `yaml` struct tags, parsed via `github.com/adrg/frontmatter v0.2.0` (already in `go.mod`).

**Rationale**: The existing `gitstore-api/internal/validate/` package is a stub — it has only a test file with an anonymous `struct{ Kind string; Metadata map[string]any }` that leaves `apiVersion` and all `spec` fields unread. A fully typed struct (`ProductResource` → `ObjectMeta` → `ProductSpec`) gives compile-time safety, enables `go-playground/validator/v10` tag-based rules (already in `go.mod`), and produces clear error messages for rejected documents.

**Alternatives considered**:
- `map[string]any` / dynamic unmarshalling: rejected — no compile-time field validation, harder to generate actionable error messages.
- JSON Schema (external): rejected — adds a runtime dependency not yet present; typed structs are sufficient for the contract definition phase and are internally consistent with the Go codebase.

---

### D-002: GraphQL Schema — Full Rewrite

**Decision**: Replace the existing flat `Product` type and all associated input/payload types with the new Kubernetes-style schema. No additive/side-by-side approach.

**Rationale**: Alpha software — no deployed consumers, no backwards compatibility obligation. The existing schema (`sku`, `price`, `currency`, `inventoryStatus`, etc.) is fundamentally incompatible with the new resource model and would require every field to be mapped or deprecated anyway. A clean rewrite produces a coherent schema with no vestigial types. The constitution's Principle III (clear contracts) is satisfied by publishing the new schema as the v1beta1 contract before any resolver code is written.

**Removed types**: `CreateProductInput`, `UpdateProductInput`, `DeleteProductInput`, `CreateProductPayload`, `UpdateProductPayload`, `DeleteProductPayload`, `OptimisticLockConflict`, `ProductBy`, `InventoryStatus` enum. Product lifecycle is git-driven; GraphQL mutations are not part of this contract.

**Alternatives considered**:
- Additive extension alongside existing `Product`: rejected — alpha software has no consumers to protect; side-by-side types add schema noise and delay consistency.
- Deprecation markers on old fields: rejected — YAGNI; no clients exist to migrate.

---

### D-003: ScyllaDB Schema — Full Rewrite, Datastore Holds Hydrated View

**Decision**: Rewrite `001_initial_schema.cql` directly with the new `products` table schema. No additive migration. The datastore holds the complete hydrated product record (spec + all metadata + status + body) so that all reads go directly to ScyllaDB with no git lookups.

**Rationale**: Alpha software — no production data, no migration needed. The existing table schema (`bucket`, `sku`, `price`, `currency`, `inventory_status`, `category_id`, `collection_ids`, `images`) is incompatible with the new resource model at the primary key level (`bucket + created_at + id` → `namespace + name`). Re-keying requires a full table rebuild regardless, so a rewrite is strictly simpler. 
Storing the full hydrated view in the datastore avoids a split-read path (git blob + datastore merge) on every product query — the constitution's performance target (< 500ms for 1000+ products) is met with a single datastore read per product.

**New schema**: `namespace` (partition key) + `name` (clustering key) + `uid`, `api_version`, `kind`, `generation`, `resource_version`, `creation_timestamp`, `revision`, `labels`, `annotations`, `owner_refs`, `git_commit_sha`, `git_ref`, `spec` (JSON blob), `body`, `status` (JSON blob).

**Alternatives considered**:
- `ALTER TABLE ADD` migration: rejected — primary key change cannot be done with ALTER; rewrite is unavoidable.
- Separate `product_status` table: rejected — adds a join on every read with no benefit; single-row read is the most efficient pattern for ScyllaDB.
- Split storage (git for spec, datastore for status): rejected — pays a performance penalty on every read (two I/O operations); datastore-only reads are simpler and faster.

---

### D-004: Two Independent Callouts — Schema Validation and Admission Control

**Decision**: The `gitstore-git-service` is a dumb git layer with no knowledge of Markdown, frontmatter, or business logic. The hook pipeline makes **two independent callouts** to `gitstore-api` (Go) at configurable phases:

1. **Schema validation** — lightweight structural check, blocking. Configured via `GITSTORE_SCHEMA_VALIDATION__PHASE` (default: `pre-receive`). The API reads changed `.md` blobs, parses frontmatter, and rejects if the document violates the schema contract. Runs before the commit lands; can abort the push.

2. **Admission control** — full admission pipeline (mutating + validating + ingest), fire-and-forget. Configured via `GITSTORE_ADMISSION_CONTROL__PHASE` (default: `post-receive`). The API assigns system-managed metadata, writes the hydrated record to the datastore, and handles all other admission concerns. Cannot reject the push; errors are logged.

The Go API owns the distinction between these two concerns (analogous to Kubernetes admission controllers separating validating and mutating webhooks). The git service only calls `admission_handler.admit(phase, updates)` at the configured phase — it has no knowledge of what the handler does.

**Config shape** (`gitstore-git-service/src/config.rs`):
- Remove `ValidatingAdmissionPolicyConfig` and `validating_admission_policy` nested key — superseded.
- Add `phase: String` field to `AdmissionControlConfig` (default: `"post-receive"`). Env var: `GITSTORE_ADMISSION_CONTROL__PHASE`.
- Add new top-level `SchemaValidationConfig { phase: String }` (default: `"pre-receive"`). Env var: `GITSTORE_SCHEMA_VALIDATION__PHASE`.

**HookPipeline shape** (in scope for GH#105/106):
- Needs two independent handler slots: `schema_validation_handler` (blocking, at `schema_validation.phase`) and `admission_control_handler` (spawned fire-and-forget at `admission_control.phase` in `run_post_receive`). `NoopAdmissionHandler` is the default for both until GH#105/106 wire the concrete impl.

**Rationale**: Schema validation at `pre-receive` is the only blocking point before a commit lands in git history — lightweight enough to stay on the critical path. Admission control at `post-receive` handles everything that is either expensive or mutating, and cannot block `receive-pack`. Separating them avoids the latency of a full admission pipeline on the push critical path.

**Alternatives considered**:
- Single configurable phase for both: rejected — schema validation must be blocking; admission control must be non-blocking; conflating them forces an unacceptable tradeoff.
- `GITSTORE_ADMISSION_CONTROL__VALIDATING_ADMISSION_POLICY__PHASE` (old key): superseded — `validating_admission_policy` is removed; the Go API handles the admission controller split.
- Rust YAML parsing: rejected — git service is a dumb transport; all business logic belongs in the API.

---

### D-005: memdb Schema — Full Rewrite of `product` Table

**Decision**: Replace the existing `product` table definition in `memdb/schema.go` with a new schema indexed on `(namespace, name)` compound unique + `namespace` list + `uid` global lookup. No separate `product_resource` table.

**Rationale**: Consistent with D-003 — alpha, no existing memdb data to preserve. The existing `product` table indexes (`id` UUID, `sku` string, `category_id` string) map to the old flat model. The new indexes match the new primary key convention and the `Datastore` interface's lookup patterns. A single rewritten table is simpler than running two tables in parallel.

**Alternatives considered**:
- Add `product_resource` alongside existing `product` table: rejected — unnecessary complexity; old table indexes are unused once the new code path is in place. Clean rewrite is simpler.

---

## Open Questions (Deferred to GH#185 / GH#186)

- **SKU field fate**: The current `Product` has `sku: String!` as a first-class field. The new K8s-style spec uses `metadata.name` as the primary slug. Whether `sku` becomes a label, an annotation, or disappears entirely is a domain constraint question deferred to GH#186.
- **Collections**: `spec.collections` is not in the Product spec — it belongs to the Collection resource (GH#84). Not addressed here.
- **Price in spec**: Pricing is a `status.resolved.priceRange` concern. Where product authors declare base price (if at all) is deferred to GH#185/186. `spec` currently has no price field.
- **gRPC callout shape for schema validation and admission control**: Neither endpoint exists yet in `shared/proto/gitstore/git/v1/git_service.proto`. GH#105 (Catalogue Validation) and GH#106 (ValidatingAdmissionPolicy Engine) will define the service contract. For this feature the Go-side validation and ingest logic is written as standalone functions, callable from either a future gRPC handler or tests directly.
- **Rust config changes** (`GITSTORE_SCHEMA_VALIDATION__PHASE`, `GITSTORE_ADMISSION_CONTROL__PHASE`, removal of `validating_admission_policy`): These changes to `config.rs` and the two-handler `HookPipeline` are deferred to GH#105/106 where the concrete `AdmissionHandler` impls are wired. The Rust-side config refactor is noted here so those issues can scope it.
