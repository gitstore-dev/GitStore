# Quickstart: Product Spec/Status Validation

**Feature**: `017-product-spec-validation`  
**Date**: 2026-06-05

This guide covers the three development tasks for this feature:
1. Extending the validator (`validate.Parse`) to produce field-scoped multi-error messages.
2. Verifying status hydration behaviour.
3. Adding integration tests and documentation examples.

---

## Prerequisites

```bash
# Install git hooks (run once per clone)
./scripts/install-git-hooks.sh

# Verify the test suite passes before starting
make test
```

---

## Task 1: Extend `preParseChecks` to collect all forbidden metadata fields

**Location**: `gitstore-api/internal/validate/validator.go`

The current `preParseChecks` returns on the first forbidden metadata field. Change it to collect all violations:

```go
// Before (returns early):
for _, field := range readOnly {
    if _, present := meta[field]; present {
        return fmt.Errorf("validate: metadata.%s is read-only ...", field)
    }
}

// After (collects all):
var forbidden []string
for _, field := range readOnly {
    if _, present := meta[field]; present {
        forbidden = append(forbidden, field)
    }
}
if len(forbidden) > 0 {
    msgs := make([]string, len(forbidden))
    for i, f := range forbidden {
        msgs[i] = fmt.Sprintf("validate: metadata.%s is read-only and must not be set by authors", f)
    }
    return fmt.Errorf("%s", strings.Join(msgs, "\n"))
}
```

**Test first**: Add `TestParse_MultipleReadOnlyFieldsReportedTogether` in `validator_test.go` — verify both `uid` and `resourceVersion` appear in the error. Run it: it must fail before the fix, pass after.

---

## Task 2: Improve field paths in `toFriendlyError`

**Location**: `gitstore-api/internal/validate/validator.go`

`fe.Field()` returns only the leaf field name. `fe.StructNamespace()` returns the fully-qualified path from the root struct. Use the namespace to build user-friendly dotted paths:

```go
// In toFriendlyError, replace fe.Field() with a path helper:
func fieldPath(fe validator.FieldError) string {
    // StructNamespace: "ProductResource.Spec.Media[0].FileRef.Name"
    // Target:          "spec.media[0].fileRef.name"
    ns := fe.StructNamespace()
    // Strip root struct name prefix
    if idx := strings.IndexByte(ns, '.'); idx >= 0 {
        ns = ns[idx+1:]
    }
    return strings.ToLower(ns)
}
```

**Test first**: Add `TestParse_SpecMedia_EmptyFileRefName_PathInError` — verify the error contains `spec.media[0].fileref.name` (or the exact dotted path the validator produces). Run it: fail → implement → pass.

---

## Task 3: Verify status hydration (no code change expected)

**Location**: `gitstore-api/internal/graph/converters.go`, `converters_test.go`

Run the existing converter tests to confirm:

```bash
cd gitstore-api && go test ./internal/graph/... -v -run TestStatus
```

Add missing test cases for:
- All six Kubernetes TitleCase condition types normalised correctly (FR-012)
- An unrecognised condition type passed through uppercased (edge case)
- JPY monetary values round-trip without precision loss (SC-005)

---

## Task 4: Write integration tests

**Location**: `tests/integration/` (root-level separate Go module — `github.com/gitstore-dev/gitstore/tests/integration`)

This module runs against live services over HTTP/GraphQL. Add a new test file alongside the existing `git_http_test.go` and `health_test.go`:

```go
// tests/integration/product_lifecycle_test.go
package integration

import (
    "net/http"
    "strings"
    "testing"
    // ...
)

// TestProductLifecycle_ValidFile_AcceptedAndQueryable pushes a valid product
// file via git and queries it back through the GraphQL API.
func TestProductLifecycle_ValidFile_AcceptedAndQueryable(t *testing.T) { ... }

// TestProductLifecycle_InvalidTitle_Rejected pushes a file with spec.title > 200
// chars and verifies the push is rejected with the right error message.
func TestProductLifecycle_InvalidTitle_Rejected(t *testing.T) { ... }

// TestProductLifecycle_StatusHydration stores a controller status blob and
// verifies the GraphQL API returns all conditions correctly.
func TestProductLifecycle_StatusHydration(t *testing.T) { ... }
```

The tests read `GIT_SERVER__GIT_URL` and `API_URL` from environment (defaulting to `localhost:5000` and `localhost:4000`). Start the stack first:

```bash
make dev          # or: make compose
```

Run the integration tests:

```bash
cd tests/integration && go test ./... -v
```

---

## Task 5: Add documentation examples

**Location**: `docs/products/examples/`

Create four example files that are testable via the parser:

| File | Purpose |
|------|---------|
| `valid-product.md` | Happy path — accepted verbatim (FR-016) |
| `invalid-status.md` | `status:` present — rejected (FR-016) |
| `invalid-title.md` | `spec.title` > 200 chars — rejected (FR-016) |
| `invalid-media.md` | `spec.media[0].fileRef.name` absent — rejected (FR-016) |

Each file must be parseable by the actual parser (FR-017). The integration tests must load these files from `docs/products/examples/` and run them through `validate.Parse` to verify the stated outcome.

---

## Running All Tests

```bash
# Unit tests only (fast, no infrastructure)
make test

# Integration tests (memdb, no Scylla needed)
cd gitstore-api && go test -tags integration ./tests/integration/... -v

# Full PR readiness check
make pr-ready
```
