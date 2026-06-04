# Research: Product Parser and Kind Validation Enforcement

**Branch**: `015-product-parser` | **Date**: 2026-06-04

## Decision 1: Frontmatter opt-in — `Parse` vs `MustParse`

**Decision**: Use `frontmatter.Parse` (not `MustParse`).

**Rationale**: `frontmatter.Parse` treats frontmatter as optional — it returns an empty struct and the full file bytes as body when no `---` delimiter is present. This naturally implements FR-013: the caller receives a nil `ProductResource` with a `(nil, body, nil)` return, which the pre-receive hook interprets as "not a product file, skip". `frontmatter.MustParse` would error on any file without frontmatter, breaking README.md and all non-product Markdown.

**Alternatives considered**: Pre-filter by file path convention (directory whitelist) — rejected per clarification session; frontmatter opt-in was the chosen approach.

**Implementation note**: The current `extractFrontmatterBlock` function errors on a missing `---` delimiter. For the opt-in model this function is replaced by a `hasFrontmatter` check: if the file does not start with `---`, return `(nil, rawBytes, nil)` immediately without calling `preParseChecks` or struct binding.

---

## Decision 2: Multi-error collection

**Decision**: Collect all validation errors before returning; return a joined error.

**Rationale**: FR-011 requires that all violations in a single file are reported together. The current implementation short-circuits at the first error (`preParseChecks` → `validate.Struct` → `validateSpec` → `validateLabels`). The fix: accumulate errors in a `[]error` slice across all post-parse validation stages and return `errors.Join(errs...)` at the end.

**Scope boundary**: Pre-parse checks (YAML syntax error, forbidden fields) still short-circuit immediately — there is no value collecting further errors when the document is fundamentally malformed or contains a security-violating field. Multi-error collection applies to the post-parse validation stages: struct-tag errors, spec-level rules, and label rules.

**Alternatives considered**: Wrapping in a custom `ValidationError` type — rejected; `errors.Join` is sufficient and avoids new types.

---

## Decision 3: Explicit YAML-only format

**Decision**: Construct the parser with `frontmatter.NewFormat("---", "---", yaml.Unmarshal)` explicitly.

**Rationale**: This is already in `validator.go` (line 48–50). It ensures TOML (`+++`) and JSON (`{`) frontmatter are rejected at the library level rather than by post-parse inspection. No change needed — document for clarity.

---

## Decision 4: Forbidden `ownerReferences` treatment

**Decision**: `ownerReferences` remains in the forbidden read-only list in `preParseChecks`.

**Rationale**: `ownerReferences` is system-managed (set by admission controllers, not authors). It is already in the `readOnly` slice in `validator.go:111`. A test for it is missing and must be added (see Phase 1 gap list).

---

## Decision 5: `spec` entirely absent

**Decision**: When `spec` is absent from the frontmatter, `validate.Struct` fires a `required` error on the `Spec` field because `ProductResource.Spec` does not carry a `validate:"required"` tag — it is a value type (`ProductSpec`), not a pointer. A zero-value `ProductSpec` satisfies `validate.Struct`. Therefore a dedicated pre-parse check for missing `spec` is needed, or the struct tag must be adjusted.

**Resolution**: Add `validate:"required"` is not applicable to a value type. Instead add an explicit check in `preParseChecks`: if `raw["spec"]` is nil/absent, return an error `"validate: spec is required"`. This is the minimal change consistent with the existing pattern.

---

## Gap Analysis: Missing test coverage (to be added in Phase 1)

The following scenarios from the spec have no corresponding test:

| Scenario                                        | Spec Ref       | Current gap                                                                  |
|-------------------------------------------------|----------------|------------------------------------------------------------------------------|
| File without `---` is silently skipped (opt-in) | FR-013         | No test; current code errors                                                 |
| `apiVersion` wrong value (not `v1beta1`)        | FR-003         | No test                                                                      |
| `spec` block entirely absent                    | FR-005         | No test; behaviour unclear (see Decision 5)                                  |
| Forbidden `ownerReferences` in metadata         | FR-007         | No test                                                                      |
| Forbidden `resourceVersion` in metadata         | FR-007         | No test                                                                      |
| Forbidden `generation` in metadata              | FR-007         | No test                                                                      |
| Forbidden `creationTimestamp` in metadata       | FR-007         | No test                                                                      |
| Forbidden `revision` in metadata                | FR-007         | No test                                                                      |
| Label value exceeds 63 chars                    | FR-010         | No test                                                                      |
| Label key prefix exceeds 253 chars              | FR-010         | No test                                                                      |
| Multiple violations reported together           | FR-011         | No test                                                                      |
| `kind` lowercase (`product`) rejected           | US2 scenario 3 | No test                                                                      |
| `metadata.name` empty string rejected           | US3 scenario 2 | Covered by `MissingNameRejected` implicitly — add explicit empty-string case |
