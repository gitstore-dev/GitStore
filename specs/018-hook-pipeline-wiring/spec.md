# Feature Specification: Hook Pipeline Wiring — Pre-Receive Validation and Post-Receive Admission

**Feature Branch**: `018-hook-pipeline-wiring`
**Created**: 2026-06-05
**Status**: Closed
**Issues**: GH#105 (pre-receive schema validation callout), GH#106 (post-receive admission/storage callout)
**Parent**: GH#77 (Kubernetes-style Product frontmatter initiative)
**Depends on**: spec#013 (AdmissionHandler trait), spec#014 (schema contract + datastore schema), spec#015 (validator), spec#016 (ProductStatus types)

---

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Catalog author receives validation errors on push (Priority: P1)

A catalog author pushes a product file whose frontmatter is invalid — wrong `kind`, a forbidden `status` key, or a `spec.title` that exceeds 200 characters. They expect the push to be rejected immediately, before any data is written, with a clear error message they can act on without guessing.

**Why this priority**: Without this, every product push succeeds regardless of correctness. The entire validation pipeline (specs #014–#017) has no effect in production because the hook that calls it is a no-op. This is the linchpin that makes validation user-visible.

**Independent Test**: Push a product file with `status:` in the frontmatter. Verify the push is rejected with "status is system-managed" and no product record is created.

**Acceptance Scenarios**:

1. **Given** a product file with an invalid `kind` value, **When** the author runs `git push`, **Then** the push is rejected before any refs are updated and the error names the `kind` field.
2. **Given** a product file with a `status:` key, **When** the author runs `git push`, **Then** the push is rejected with a message stating `status` is system-managed.
3. **Given** a product file with `spec.title` exceeding 200 characters, **When** the author runs `git push`, **Then** the push is rejected and the error names `spec.title` and the 200-character limit.
4. **Given** a product file with multiple violations, **When** the author runs `git push`, **Then** all violations are reported together in a single rejection response.
5. **Given** a product file with a valid `spec: {}` (empty spec), **When** the author runs `git push`, **Then** the push is accepted.
6. **Given** a commit that contains only files without a YAML frontmatter block (e.g. `README.md`, binary assets), **When** the author runs `git push`, **Then** the push is accepted without calling the validation service.
7. **Given** a commit containing both valid and invalid product files, **When** the author runs `git push`, **Then** the push is rejected and every invalid file is named in the error.

---

### User Story 2 — Operator queries a product immediately after push (Priority: P1)

An operator pushes a valid product file and immediately queries the catalog API. They expect the product to be present in the API response with its spec fields populated. Currently a successful push produces no queryable record.

**Why this priority**: The storage callout is the other half of the pipeline — without it, valid pushes vanish silently and the catalog is always empty. Both P1 stories together constitute the minimal viable push-to-query lifecycle.

**Independent Test**: Push a valid product file, then query the product by name within 5 seconds. The product must be present with spec fields matching the pushed file.

**Acceptance Scenarios**:

1. **Given** a valid product file is pushed, **When** the operator queries the product by name, **Then** the product is returned with all spec fields (`title`, `tags`, `options`, `media`, `categoryRef`) matching the pushed file.
2. **Given** a valid product file is pushed, **When** the operator queries the product, **Then** system-managed identity fields (`uid`, `resourceVersion`, `creationTimestamp`) are present and non-empty in the response.
3. **Given** a valid product file is pushed, **When** the operator queries the product, **Then** the `status` field reflects at minimum `AdmissionAccepted: True` once the admission pipeline completes.
4. **Given** a product file is pushed a second time with updated content, **When** the operator queries the product, **Then** the spec fields reflect the latest push and the generation counter has incremented.
5. **Given** two different product files are pushed in a single commit, **When** the operator queries both products, **Then** both are present in the catalog.

---

### User Story 3 — Push performance stays within the latency budget (Priority: P2)

A catalog author pushes a batch of product files. They expect the validation step not to make `git push` noticeably slower — the pre-receive callout must complete within the configured timeout and not block indefinitely.

**Why this priority**: Push latency is a usability constraint. If validation adds seconds to every push, authors will route around it. P2 because the system is non-functional without P1, but the latency budget must be enforced once P1 works.

**Independent Test**: Push a commit with 100 valid product files. The push must complete (pre-receive validation + push accepted) in under 5 seconds.

**Acceptance Scenarios**:

1. **Given** a push of 100 valid product files, **When** all pass validation, **Then** the pre-receive callout completes and the push is accepted in under 5 seconds.
2. **Given** the validation service is unreachable, **When** the author runs `git push`, **Then** the push is rejected with a "validation service unavailable" message within the configured timeout — the push does not hang indefinitely.
3. **Given** the admission service is unreachable after a successful pre-receive, **When** the push completes, **Then** the push is accepted (admission is fire-and-forget), the error is logged server-side, and the author is not blocked.

---

### Edge Cases

- What happens when a pushed commit modifies a non-product `.md` file (e.g. `README.md`)? Files that do not begin with a `---` YAML frontmatter block are silently skipped — the validation service is never called for them. Files that do begin with `---` are treated as candidate resources and dispatched based on their `kind` and `apiVersion` fields.
- What happens when the incoming commit contains a binary file with a `.md` extension? The validation service returns an error naming the file; the push is rejected.
- What happens if the post-receive admission service fails to store one product in a multi-product commit? Each product is processed independently; successful ones are stored, failures are logged. The push is not rolled back.
- What happens when the same product `name` in the same `namespace` is pushed again? The existing record is updated (upsert); system identity fields (`uid`, `creationTimestamp`) are preserved; `resourceVersion` and `generation` increment.
- What happens if the pre-receive timeout fires before all blobs are validated? The push is rejected with a timeout error; no refs are updated and no partial state is written.
- What happens on a force-push? The validation and admission pipeline treats the new tree identically to a regular push — all product files in the new tree tip are validated and admitted.
- What happens on a new-branch creation push (old object ID is the zero hash)? Pre-receive validation runs identically to any other push — the new tree tip is fully validated. There is no exemption for branch creation.

---

## Requirements *(mandatory)*

### Functional Requirements

**Pre-receive schema validation (GH#105)**

- **FR-001**: When a push contains product files, the system MUST validate each file's frontmatter before any refs are updated. An invalid file MUST cause the push to be rejected with a field-scoped error message.
- **FR-002**: The validation callout MUST be blocking — the push is held until a pass/fail decision is returned or the configured timeout elapses.
- **FR-003**: The validation callout MUST report all violations across all pushed product files in a single rejection response (not fail-fast on the first invalid file).
- **FR-004**: Files that do not begin with a `---` YAML frontmatter block MUST be silently skipped — the validation service is never called for them. Files that do begin with `---` are dispatched by `kind` and `apiVersion`; an unrecognised `kind` MUST be rejected with a clear error naming the unsupported kind.
- **FR-005**: If the validation service is unreachable, the push MUST be rejected with a clear error within the configured timeout. Pushes MUST NOT block indefinitely.
- **FR-006**: The pre-receive validation timeout MUST be configurable and MUST default to a value that keeps total push latency under 5 seconds for a 100-file push on a local stack.

**Post-receive admission and storage (GH#106)**

- **FR-007**: After a push is accepted, the system MUST asynchronously store each valid product record in the catalog without blocking the git push response to the author.
- **FR-008**: Each stored product record MUST be assigned system-managed fields: `uid` (globally unique, immutable), `resourceVersion` (monotonic), `generation` (starts at 1), `creationTimestamp`, and `revision` (the git commit SHA of the accepted push).
- **FR-009**: Each stored product record MUST have an initial pipeline status condition `AdmissionAccepted: True` written at admission time.
- **FR-010**: If a product with the same `namespace` and `name` already exists, the record MUST be updated: `spec`, `revision`, `resourceVersion`, and `generation` are updated; `uid` and `creationTimestamp` are preserved.
- **FR-011**: If admission fails for one product in a multi-product commit, the failure MUST be logged with the product name and reason, and processing of remaining products MUST continue independently.
- **FR-012**: The post-receive callout MUST be fire-and-forget — the git push response is returned to the author before admission completes.
- **FR-016**: Post-receive admission MUST only fire for pushes to refs matching a configurable branch pattern, controlled by the `GITSTORE_ADMISSION_CONTROL__BRANCH_PATTERN` environment variable (default: the repository's configured default branch). Pushes to non-matching refs (feature branches, tags) MUST be accepted without triggering catalog storage.

**Observability**

- **FR-017**: The `SchemaValidationHandler` MUST emit a structured log entry for every pre-receive callout containing: file count, outcome (`accepted`/`rejected`), callout latency in milliseconds, and — on rejection — a summary of validation errors.
- **FR-018**: The `SchemaValidationHandler` MUST increment a counter metric for each pre-receive callout outcome, labelled by result (`accepted`/`rejected`/`timeout`/`service_unavailable`). Post-receive admission failures (FR-011) are already logged; no additional metric is required for that path.

**Configuration**

- **FR-013**: The phase at which schema validation fires MUST be configurable (default: `pre-receive`), controlled by the `GITSTORE_SCHEMA_VALIDATION__PHASE` environment variable.
- **FR-014**: The phase at which admission control fires MUST be configurable (default: `post-receive`), controlled by the `GITSTORE_ADMISSION_CONTROL__PHASE` environment variable.
- **FR-015**: The existing `validating_admission_policy` configuration key MUST be removed; it is superseded by the two independent phase configurations introduced by this feature.
- **FR-019**: At service startup, the git service MUST validate that `GITSTORE_SCHEMA_VALIDATION__PHASE` and `GITSTORE_ADMISSION_CONTROL__PHASE` are set to distinct values. If they are identical, the service MUST refuse to start and emit a clear configuration error naming both env vars and their conflicting value.
- **FR-020**: Pre-receive validation MUST run for all `RefUpdate` entries regardless of whether `old_oid` is the zero hash. New branch creation and ref updates are treated identically — there is no exemption based on ref existence.

### Key Entities

- **RefUpdate**: A single ref change in a push (old object ID, new object ID, ref name). The unit of work passed to both admission handlers.
- **ResourceBlob**: The raw bytes of a candidate resource file (any file beginning with `---`) read from an incoming commit, along with its path and object ID. Input to the schema validation callout; dispatched by `kind` and `apiVersion`.
- **AdmissionDecision**: The outcome of a callout — `Accept` or `Reject` carrying a user-facing error message.
- **HookPipeline**: The component in the git service that orchestrates both hook phases. Gains two independent handler slots: one for schema validation (blocking), one for admission control (fire-and-forget).
- **SchemaValidationHandler**: The concrete handler for `pre-receive` — calls the validation service and returns the decision synchronously.
- **AdmissionControlHandler**: The concrete handler for `post-receive` — calls the admission service asynchronously; errors are logged, not propagated to the author.

---

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A push containing any invalid product file is rejected with a field-scoped error in 100% of cases — zero invalid products silently accepted into the catalog.
- **SC-002**: A push of 100 valid product files completes (pre-receive validation + push accepted) in under 5 seconds on a local stack.
- **SC-003**: A valid product pushed to the catalog is queryable via the catalog API within 5 seconds of the push completing — zero valid pushes that result in an absent product record.
- **SC-004**: All existing integration tests in `tests/integration/` pass against a running stack with the wired pipeline — zero failures, zero skips (these tests currently fail because hooks are no-ops).
- **SC-005**: A force-push that updates a product file results in the API returning the new spec fields — the previous revision is replaced, not duplicated.

---

## Assumptions

- The `AdmissionHandler` trait (Rust) and `NoopAdmissionHandler` are already defined (spec#013, CLOSED) and require no interface changes.
- `validate.Parse()` in `gitstore-api` (spec#015, CLOSED) is the authoritative validation function. The pre-receive gRPC handler calls it directly — no new validation logic is needed.
- The datastore schema for `products` (spec#014, CLOSED) and the `Datastore.CreateProduct` / `UpdateProduct` interface are in place.
- `ProductStatus` types and the condition set (including `AdmissionAccepted`) are defined (spec#016, CLOSED).
- Resource dispatch is frontmatter-driven, not path-driven: any file beginning with `---` is a candidate resource; its `kind` and `apiVersion` determine how it is validated. Path location within the repository is irrelevant to validation routing.
- The gRPC connection from `gitstore-git-service` to `gitstore-api` already exists (used for `GetFile`, `CommitFile`, etc.). The new admission RPCs use the same connection.
- A new proto service definition is required to expose the validation and admission RPC endpoints. Proto codegen is in scope for this feature. Validation failures are returned as a structured `repeated ValidationError` field in the response body (not gRPC `Status.details`), keeping them machine-readable in the Rust/tonic layer and consistent with the `ParseError` shape from spec#015.
- Atomic multi-product transactions across post-receive are out of scope; each product is stored independently.
- Admission storage fires only for pushes to refs matching `GITSTORE_ADMISSION_CONTROL__BRANCH_PATTERN`; the default is the repository's configured default branch.
- The pre-receive timeout default is assumed to be 10 seconds (configurable).

## Clarifications

### Session 2026-06-05

- Q: Which branches trigger post-receive admission and storage? → A: Configurable branch pattern (env var), defaulting to the repository's default branch.
- Q: How are structured field-level validation errors carried across the gRPC wire? → A: Structured `repeated ValidationError` field in the RPC response message body.
- Q: What observability signals are required for the pre-receive validation callout? → A: Structured log entry per callout (file count, outcome, latency ms, error summary) plus accept/reject counter metric.
- Q: Is a configuration where both handlers share the same phase value valid? → A: No — startup validation rejects identical phase values with a clear configuration error.
- Q: Should pre-receive validation run on new-branch creation pushes (old_oid = zero hash)? → A: Yes — new branch creation and ref updates are treated identically; all pushes are validated.

---

## Dependencies

- **Depends on**: spec#013 (CLOSED), spec#014 (CLOSED), spec#015 (CLOSED), spec#016 (CLOSED)
- **Unblocks**: `tests/integration/` product lifecycle tests (currently failing due to `NoopAdmissionHandler`)
- **Parent**: GH#77 (Kubernetes-style Product frontmatter initiative)
