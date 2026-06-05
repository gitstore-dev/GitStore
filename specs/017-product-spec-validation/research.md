# Research: Product Spec/Status Validation Semantics and Integration Tests

**Feature**: `017-product-spec-validation`  
**Date**: 2026-06-05  
**Status**: Complete — all NEEDS CLARIFICATION resolved

---

## Decision Log

### 1. Multi-Error Collection Strategy

**Decision**: Use `errors.Join` (Go 1.20+ stdlib) to accumulate all violations before returning, as already used in `validate.Parse` for post-parse errors.

**Rationale**: `errors.Join` produces a flat multi-error that is `errors.Is`/`errors.As` compatible and requires no additional dependency. The existing `Parse` function already calls `errors.Join(errs...)` at the end of the post-parse stage. The gap is in `preParseChecks`, which returns on the first error and misses co-occurring forbidden metadata fields (e.g. `uid` + `resourceVersion` in the same document). Collecting into a `[]error` slice and joining at the end is idiomatic and sufficient.

**Alternatives considered**:
- `github.com/hashicorp/go-multierror` — adds a dependency; no advantage over stdlib `errors.Join` for this use case.
- Returning a custom struct — unnecessary complexity; callers already pattern-match on string content for user messages.

---

### 2. Field-Indexed Error Messages for `spec.media` and `spec.options`

**Decision**: The `validateSpec` function already reports `options[N].name` with the index. The struct-tag validator reports leaf field names via `fe.Field()` (e.g. `Name`, `Kind`). To satisfy FR-002 (report `spec.media[N].fileRef.name`), the `toFriendlyError` mapper needs to use `fe.StructNamespace()` to produce a qualified path, lowercased and dotted (e.g. `ProductResource.Spec.Media[0].FileRef.Name` → `spec.media[0].fileRef.name`).

**Rationale**: `go-playground/validator` exposes both `fe.Field()` (leaf) and `fe.StructNamespace()` (fully-qualified from root struct). Using the namespace produces user-friendly, spec-accurate paths without custom recursion.

**Alternatives considered**:
- Manual recursion over the spec tree — brittle, duplicates validation logic.
- Keeping `fe.Field()` (current) — fails the `spec.media[N].fileRef.name` requirement in FR-002 because it only emits `name`, not the full path.

---

### 3. Status Hydration — Unknown Condition Type Passthrough

**Decision**: When `statusFromJSON` encounters a `type` string not in `k8sConditionTypeToGraphQL`, it already passes it through uppercased. This satisfies the edge case "condition with unrecognised type must not crash; pass through or log warning". No change needed; verified by reading `converters.go`.

**Rationale**: The existing code is:
```go
condType, ok := k8sConditionTypeToGraphQL[c.Type]
if !ok {
    condType = model.ProductConditionType(strings.ToUpper(c.Type))
}
```
This is the correct passthrough behaviour. The spec asks for a warning log as an alternative — but silently passing through is acceptable under the "must not crash" requirement.

**Alternatives considered**:
- Dropping unknown conditions — explicitly rejected by the edge case in the spec.
- Logging a WARN via `converterLogger` for unknown types — acceptable enhancement but not required for the acceptance criteria.

---

### 4. Integration Test Build Tag

**Decision**: Integration tests live in the root-level `tests/integration/` module (`github.com/gitstore-dev/gitstore/tests/integration`) — a separate Go module already wired to run against live services over HTTP/GraphQL. No build tag is needed; the module is not part of `go test ./...` from the repo root and is only run explicitly via `cd tests/integration && go test ./...`.

**Rationale**: The root `tests/integration/` package is the established pattern for black-box end-to-end tests in this repo. It reads service URLs from environment variables (`GIT_SERVER_GIT_URL`, `API_URL`) and exercises the real stack. Adding product lifecycle tests there keeps the same convention used by `git_http_test.go` and `health_test.go`.

**Alternatives considered**:
- `gitstore-api/tests/integration/` — would be an in-process test inside the API module; contradicts the existing root-level pattern and mixes unit/integration concerns.
- Build tag `//go:build scylla,integration` inside `gitstore-api` — unnecessary; the root-level module separation already achieves the same isolation.

---

### 5. Documentation Examples as Tested Fixtures

**Decision**: The example files in `docs/products/examples/` are symlinked or copied into `internal/validate/testdata/` (or directly loaded via `os.Open` in integration tests using `../../docs/products/examples/`). The `//go:embed testdata/*` directive in `validator_test.go` is the existing pattern; a separate `//go:embed` in the integration test package loads the documentation examples.

**Rationale**: FR-017 requires examples to be parseable by the actual parser "without modification". The cleanest approach is to load them from `docs/` in a test rather than duplicating them in `testdata/`. This ensures documentation and test fixtures are always in sync.

**Alternatives considered**:
- Copy examples to `testdata/` — creates duplication; examples can drift.
- Store examples only in `testdata/` and reference them from docs — docs would contain references, not the actual YAML files, making them less useful for developers.

---

### 6. `preParseChecks` — Accumulate All Forbidden Metadata Fields

**Decision**: Change `preParseChecks` from early-return per forbidden field to collecting all forbidden fields into a `[]string` and returning a single combined error. The `status` forbidden check is independent and returns immediately (it's a top-level guard). The read-only metadata fields are accumulated.

**Rationale**: FR-009 requires "all violations reported together in a single rejection response". Currently `preParseChecks` returns on the first forbidden metadata field. Accumulating them produces a message like "metadata.uid, metadata.resourceVersion are read-only…".

**Alternatives considered**:
- Keep early-return — fails FR-009.
- Return separate errors per field and join in `Parse` — would require `preParseChecks` to return `[]error`; a cleaner interface change but the single-error-per-call convention is simpler.

---

### 7. `shopspring/decimal` and Zero-Decimal Currency Round-Trip

**Decision**: No code change needed. `decimal.Decimal` marshals to a JSON string representation (e.g. `"1000"` for JPY 1000) and unmarshals back exactly. This is already tested in `catalog/product_test.go` (`TestProductStatus_FullRoundTrip`). The integration test should add a JPY case to satisfy SC-005.

**Rationale**: `shopspring/decimal` uses arbitrary-precision representation internally; JSON round-trip preserves the exact string. Zero-decimal currencies like JPY have no fractional part, so there is no truncation risk.

**Alternatives considered**:
- `float64` — loses precision for large monetary values and is the reason `shopspring/decimal` was chosen in the first place (per existing code comments).

---

## Open Questions — None

All NEEDS CLARIFICATION items resolved. No blockers to Phase 1.
