# Quickstart: Product Parser and Kind Validation Enforcement

**Branch**: `015-product-parser` | **Date**: 2026-06-04

## What changes

Two behavioral gaps in `internal/validate` are closed:

1. **Frontmatter opt-in** — `validate.Parse` now returns `(nil, rawBytes, nil)` for any file that does not begin with `---`. Previously it returned an error. README.md, docs, and all non-product Markdown are silently skipped.

2. **Multi-error reporting** — when a file has frontmatter and fails post-parse validation, all violations are collected and returned together rather than stopping at the first error.

Additionally, missing test coverage is added for all acceptance scenarios in spec#015.

## Files to change

| File                                  | Change                                                                                        |
|---------------------------------------|-----------------------------------------------------------------------------------------------|
| `internal/validate/validator.go`      | Opt-in skip logic; multi-error collection in post-parse stages; `spec` absent pre-parse check |
| `internal/validate/validator_test.go` | Add ~13 missing test cases (see research.md gap analysis)                                     |

## Key implementation notes

### Opt-in skip

Replace the current `extractFrontmatterBlock` error-on-missing-delimiter with a `hasFrontmatter` check at the top of `Parse`:

```go
// Opt-in: files without --- are not product files; skip silently.
raw, _ := io.ReadAll(r)
if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("---")) {
    return nil, raw, nil
}
```

Then proceed with the existing extraction and validation flow.

### Multi-error collection

After the pre-parse checks (which still short-circuit), collect errors from struct validation, `validateSpec`, and `validateLabels` into a `[]error` slice and return `errors.Join(errs...)`.

### `spec` absent pre-parse check

In `preParseChecks`, add after the `apiVersion` check:

```go
if _, ok := raw["spec"]; !ok {
    return fmt.Errorf("validate: spec is required")
}
```

## Running tests

```bash
cd gitstore-api
go test ./internal/validate/... -v
```

All tests must pass (red → green per constitution Principle I).

## Acceptance check

```bash
# Should exit 0 (no product errors, README skipped)
go test ./internal/validate/... -run TestParse -v
```
