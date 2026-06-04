# Implementation Plan: Product Parser and Kind Validation Enforcement

**Branch**: `015-product-parser` | **Date**: 2026-06-04 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `specs/015-product-parser/spec.md`

## Summary

Extend the existing `internal/validate` package (delivered by spec#014) to implement the frontmatter opt-in model (files without `---` are silently skipped), collect all validation errors before returning (multi-error), and add the remaining test coverage for forbidden read-only metadata fields, wrong `apiVersion`, absent `spec`, label prefix/value length, and multi-violation scenarios. The `catalog` and `validate` packages already contain the correct struct types, the `frontmatter.Parse` call, and passing tests for the core happy-path and primary rejection cases; this feature completes the contract.

## Technical Context

**Language/Version**: Go 1.25  
**Primary Dependencies**: `github.com/adrg/frontmatter v0.2.0`, `go-playground/validator/v10 v10.30.3`, `gopkg.in/yaml.v3`  
**Storage**: N/A — parser operates on `io.Reader`; no persistence  
**Testing**: `go test ./...`, `testify/require`, `testify/assert`  
**Target Platform**: Linux (CI) / macOS (dev) — same binary as `gitstore-api`  
**Project Type**: Internal library package within `gitstore-api`  
**Performance Goals**: < 5 seconds for a 100-file push (constitution target); individual file parse < 50ms  
**Constraints**: Must not break any existing passing test; zero new external dependencies  
**Scale/Scope**: Single-file parsing; no concurrency concerns within the parser itself

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle                         | Status | Notes                                                                                                        |
|-----------------------------------|--------|--------------------------------------------------------------------------------------------------------------|
| I. Test-First                     | ✅ Pass | All new behaviour added as failing tests first, then implementation                                          |
| II. API-First                     | ✅ Pass | Public `validate.Parse` signature is unchanged; opt-in skip is a behavioural contract change documented here |
| III. Clear Contracts & Versioning | ✅ Pass | No breaking change to `Parse` signature; callers distinguish skip via `nil, nil, nil` return                 |
| IV. Observability                 | ✅ Pass | Validation failures already produce structured error messages with field paths                               |
| V. User Story Driven              | ✅ Pass | All tasks map to US1–US5 from spec                                                                           |
| VI. Incremental Delivery          | ✅ Pass | US1 (valid parse) already passes; this feature completes US2–US5 gaps                                        |
| VII. Simplicity                   | ✅ Pass | No new dependencies; extending existing package rather than introducing new abstraction                      |

**Complexity violations**: None.

## Project Structure

### Documentation (this feature)

```text
specs/015-product-parser/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── contracts/           # Phase 1 output
└── tasks.md             # Phase 2 output (/speckit.tasks)
```

### Source Code (existing — extend, do not restructure)

```text
gitstore-api/
├── internal/
│   ├── catalog/
│   │   └── product.go          # No changes needed — structs complete from spec#014
│   └── validate/
│       ├── validator.go         # Modify: opt-in skip + multi-error collection
│       ├── validator_test.go    # Add: all missing scenario coverage (see Phase 1)
│       └── testdata/
│           └── macbook-pro-64gb-1tb-ssd-m4.md   # Existing fixture — keep unchanged
```

**Structure Decision**: Single package extension. No new packages, no new files beyond test additions. The `validate` package is the correct home — it already owns `Parse`, `preParseChecks`, `validateSpec`, and `validateLabels`.

## Phase 0: Research

*Prerequisites: Technical Context and Constitution Check complete.*

**No NEEDS CLARIFICATION items remain.** The technology is fully determined from the existing codebase. Research is complete — see [research.md](research.md).

Key decisions:
- Use `frontmatter.Parse` (opt-in); files without `---` return `(nil, rawBytes, nil)` — never an error.
- Use `frontmatter.NewFormat("---", "---", yaml.Unmarshal)` explicitly — already in place.
- Multi-error: collect post-parse violations; return `errors.Join(errs...)`. Pre-parse checks still short-circuit.
- Add `spec` absent check in `preParseChecks` (see research Decision 5).
- Add 13 missing test cases covering all spec scenario gaps (see research gap analysis table).

## Phase 1: Design & Contracts

*Prerequisites: research.md complete.*

### Data Model

No new entities. All types defined in spec#014 (`ProductResource`, `ObjectMeta`, `ProductSpec`, etc.) are unchanged. Parse result states and error format documented in [data-model.md](data-model.md).

### Interface Contract

**`validate.Parse` function contract** — see [contracts/parse-function.md](contracts/parse-function.md).

Behavioural change summary (not a signature change):

| Caller input                  | Old behaviour    | New behaviour                 |
|-------------------------------|------------------|-------------------------------|
| File without `---`            | Error            | `(nil, rawBytes, nil)` — skip |
| File with multiple violations | First error only | All errors joined             |
| File with absent `spec`       | Passes silently  | Error: `spec is required`     |

### Constitution Check (post-design)

All principles still pass. No complexity violations introduced. The `Parse` contract change (skip vs error for no-frontmatter) is a non-breaking behavioural fix — callers that previously never sent README.md to the parser are unaffected; callers that did send it now get a skip signal instead of an error, which is strictly less restrictive.
