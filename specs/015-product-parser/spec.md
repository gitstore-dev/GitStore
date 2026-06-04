# Feature Specification: Product Parser and Kind Validation Enforcement

**Feature Branch**: `015-product-parser`
**Created**: 2026-06-04
**Status**: Closed
**Input**: User description: "Product Parser and Kind Validation Enforcement (GH#185)"
**Related**: GH#185 (task), GH#77 (initiative), spec#014 (Product Resource Contract — dependency)

## Clarifications

### Session 2026-06-04

- Q: When a file is pushed, how does the system decide whether to run it through the product parser? → A: Frontmatter opt-in — any file starting with `---` is treated as a candidate resource and validated; files without frontmatter (e.g. README.md) are silently skipped and never passed to the parser.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Parse a Valid Product File (Priority: P1)

A developer or CI pipeline passes a well-formed product Markdown file to the parser. The file contains valid `apiVersion`, `kind`, `metadata`, and `spec` frontmatter and a Markdown body. The parser extracts all fields without error and returns a fully populated `ProductResource` object.

**Why this priority**: This is the happy path that all downstream work (datastore writes, GraphQL responses, admission hooks) depends on. Without a working parser for valid files, nothing else can function.

**Independent Test**: A fixture file with all documented spec#014 fields parses successfully and all extracted values match the input. Can be verified with a single round-trip fixture test.

**Acceptance Scenarios**:

1. **Given** a Markdown file with `apiVersion: catalog.gitstore.dev/v1beta1`, `kind: Product`, `metadata.name`, and a populated `spec`, **When** the parser processes the file, **Then** a `ProductResource` is returned containing all frontmatter fields and the raw Markdown body.
2. **Given** a product file that omits optional fields (`spec.tags`, `spec.media`, `spec.options`), **When** the parser processes the file, **Then** parsing succeeds and omitted fields default to empty/nil without error.
3. **Given** a product file with `metadata.labels` and `metadata.annotations` populated, **When** the parser processes the file, **Then** both maps are correctly extracted and accessible.

---

### User Story 2 — Reject a File with Wrong or Missing Kind (Priority: P1)

An author accidentally creates a product file with an incorrect `kind` value (e.g. `kind: Category`) or omits the field entirely. The system must reject the file immediately with a clear, field-specific error before any further processing.

**Why this priority**: Kind is the primary discriminator for all resource routing. Accepting a misidentified resource would corrupt downstream catalogue state. This is the most critical guard in the admission pipeline.

**Independent Test**: A file with `kind: Category` and all other fields valid is submitted. The parser returns an error referencing the `kind` field and the expected value. Fully testable with a single invalid fixture.

**Acceptance Scenarios**:

1. **Given** a file with `kind: Category` and all other fields valid, **When** the parser processes the file, **Then** it is rejected with an error referencing the `kind` field and stating the expected value `Product`.
2. **Given** a file with the `kind` field entirely absent, **When** the parser processes the file, **Then** it is rejected with an error identifying `kind` as missing.
3. **Given** a file with `kind: product` (lowercase), **When** the parser processes the file, **Then** it is rejected — the value must be exactly `Product` (case-sensitive).
4. **Given** a file with `kind: Product` (correct), **When** the parser processes the file, **Then** no kind-related error is raised.

---

### User Story 3 — Reject a File with Missing Required Fields (Priority: P1)

An author pushes a product file that omits one or more required fields — `apiVersion`, `metadata.name`, or the `spec` block itself. Each missing required field must produce a specific, actionable error.

**Why this priority**: Required field enforcement is the second line of defence after kind validation. Without it, incomplete records silently enter the system and cause unpredictable failures downstream.

**Independent Test**: Submit three separate fixtures, each missing one required field. Each produces exactly one error naming the missing field. Independently testable per fixture.

**Acceptance Scenarios**:

1. **Given** a product file missing `apiVersion`, **When** the parser processes the file, **Then** it is rejected with an error identifying `apiVersion` as required and stating the expected value.
2. **Given** a product file missing `metadata.name` or with `metadata.name` set to an empty string, **When** the parser processes the file, **Then** it is rejected with an error identifying `metadata.name` as required.
3. **Given** a product file with the `spec` block entirely absent, **When** the parser processes the file, **Then** it is rejected with an error identifying `spec` as required.
4. **Given** a product file with an unrecognised `apiVersion` (e.g. `catalog.gitstore.dev/v1`), **When** the parser processes the file, **Then** it is rejected with an error indicating the supported value.

---

### User Story 4 — Reject Forbidden System-Managed Fields (Priority: P1)

An author (or a misconfigured tool) includes `status` or read-only metadata fields (`uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`) in a pushed product file. These fields are system-managed and must never appear in author-supplied git content. The admission check rejects the file before any further processing.

**Why this priority**: Allowing authors to supply system fields would let them forge UIDs, spoof resource versions, or corrupt generation counters. This is a correctness and integrity gate.

**Independent Test**: Submit a fixture containing `status: {}` alongside otherwise valid content. The parser returns a forbidden-field error. Testable with a single fixture per forbidden field.

**Acceptance Scenarios**:

1. **Given** a product file that includes a top-level `status` key (even if empty), **When** the parser processes the file, **Then** it is rejected with an error identifying `status` as a system-managed field not permitted in author-supplied files.
2. **Given** a product file whose `metadata` block includes any of `uid`, `resourceVersion`, `generation`, `creationTimestamp`, or `revision`, **When** the parser processes the file, **Then** it is rejected with an error naming the offending field and explaining it is system-managed.
3. **Given** a product file with none of the forbidden fields present, **When** the parser processes the file, **Then** no forbidden-field error is raised.

---

### User Story 5 — Enforce Spec-Level Constraint Rules (Priority: P2)

An author writes a product spec that violates a structural rule — duplicate option names, a missing required sub-field within an options entry, or a label key or value that exceeds the allowed length. Each violation produces a specific, path-qualified error.

**Why this priority**: These rules preserve the semantic integrity of catalogue data. Violations would cause silent failures in variant resolution or taxonomy lookups. They are secondary to kind/required-field checks but essential before any datastore write.

**Independent Test**: Submit a fixture with two `spec.options` entries sharing the same `name`. The parser returns an error identifying the duplicated option name. Independently testable per constraint rule.

**Acceptance Scenarios**:

1. **Given** a product spec with two `spec.options` entries that share the same `name` value, **When** the parser processes the file, **Then** it is rejected with an error identifying the duplicated option name and its position.
2. **Given** a product spec with an `options` entry that has no `name` field, **When** the parser processes the file, **Then** it is rejected with an error identifying `spec.options[N].name` as required.
3. **Given** a product file with a `metadata.labels` key whose name segment exceeds 63 characters, **When** the parser processes the file, **Then** it is rejected with an error citing the offending key and the length constraint.
4. **Given** a product file with a `metadata.labels` key whose prefix exceeds 253 characters, **When** the parser processes the file, **Then** it is rejected with an error citing the offending key prefix and the length constraint.
5. **Given** a product spec where all options entries have unique names and all labels conform to length rules, **When** the parser processes the file, **Then** no constraint violation errors are raised.

---

### Edge Cases

- What happens when both `kind` is wrong and `metadata.name` is missing — does the system report all errors or stop at the first?
- What happens when `spec.options` is present but is an empty list?
- What happens when a label key has a valid prefix and name segment individually but is syntactically invalid as a combined form?
- What happens when `metadata.generateName` is provided but `metadata.name` is also present?
- What happens when a file begins with `---` but the YAML frontmatter is syntactically malformed (not valid YAML)?
- What happens when a file begins with `---` and has valid frontmatter but no Markdown body?
- What happens when a non-product file (e.g. README.md) has no `---` delimiter — it is silently skipped, never validated.
- What happens when `metadata.namespace` is provided in the file but does not match the namespace resolved from push context?

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The parser MUST extract all author-supplied fields from YAML frontmatter: `apiVersion`, `kind`, writable `metadata` fields (`name`, `generateName`, `namespace`, `labels`, `annotations`, `ownerReferences`), and `spec`. The raw Markdown body MUST be extracted separately and preserved verbatim.
- **FR-002**: The parser MUST reject any file where `kind` is not exactly the string `Product` (case-sensitive). The error MUST identify the `kind` field, the value supplied, and the expected value.
- **FR-003**: The parser MUST reject any file where `apiVersion` is absent or is not `catalog.gitstore.dev/v1beta1`. The error MUST identify the `apiVersion` field and state the expected value.
- **FR-004**: The parser MUST reject any file where `metadata.name` is absent or is an empty string. The error MUST identify `metadata.name` as a required field.
- **FR-005**: The parser MUST reject any file where the `spec` block is entirely absent. The error MUST identify `spec` as required.
- **FR-006**: The admission check MUST reject any file that contains a top-level `status` key. The error MUST identify `status` as a system-managed field and explain it must not appear in author-supplied files.
- **FR-007**: The admission check MUST reject any file whose `metadata` block contains any of the read-only system fields: `uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`. The error MUST name the specific forbidden field.
- **FR-008**: The parser MUST validate that `spec.options` contains no duplicate `name` values. When duplicates exist, the error MUST identify the duplicated name and the positions of the conflicting entries.
- **FR-009**: The parser MUST validate that every entry in `spec.options` has a `name` field. When absent, the error MUST identify `spec.options[N].name` as required.
- **FR-010**: The parser MUST enforce Kubernetes label key conventions: each key name segment MUST NOT exceed 63 characters and the optional prefix MUST NOT exceed 253 characters. Errors MUST cite the offending key and the violated limit.
- **FR-011**: All validation errors MUST be structured with at minimum: the field path (e.g. `spec.options[1].name`), the violated constraint (e.g. "must not be empty"), and a human-readable explanation. Multiple distinct violations in a single file MUST all be reported — the parser MUST NOT stop at the first error.
- **FR-012**: The parser MUST handle syntactically malformed YAML frontmatter by returning a parse error that identifies the location of the syntax problem.
- **FR-013**: The parser MUST use a frontmatter opt-in model: a file that does NOT begin with a `---` YAML frontmatter block (e.g. README.md, CHANGELOG.md) is silently skipped and returns a no-op result — it is never validated or rejected. Only files that begin with `---` are treated as candidate resources and subjected to kind, required-field, and constraint checks.

### Key Entities

- **ProductResource**: The in-memory representation of a successfully parsed product file. Contains the parsed `APIVersion`, `Kind`, `ObjectMeta` (writable fields only), `ProductSpec`, and raw Markdown body. System-managed fields (`status`, read-only metadata) are never present in the parsed result — they are sourced from the datastore at read time.
- **ParseError**: A structured validation error produced when a file cannot be parsed or violates a constraint. Contains: `field` (dotted path), `constraint` (rule violated), and `message` (human-readable explanation). The parser collects all errors before returning.
- **AdmissionError**: A specialisation of `ParseError` for forbidden-field violations (`status`, read-only metadata fields). Distinct from structural parse errors so callers can route to appropriate error responses.
- **ProductSpec**: The author-controlled declarative description of a product (title, categoryRef, tags, media, options). Defined by spec#014; this feature adds enforcement of its structural rules (option name uniqueness, required sub-fields).
- **ObjectMeta**: Author-supplied metadata fields (`name`, `generateName`, `namespace`, `labels`, `annotations`, `ownerReferences`). Read-only fields are excluded from the parsed result. Labels are subject to key-length enforcement.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Every product file that conforms to the spec#014 schema passes all parser checks without errors.
- **SC-002**: Every documented constraint violation (wrong kind, missing required field, forbidden system field, duplicate option name, label length overflow) produces an error that names the exact field path and violated rule — with zero ambiguous or generic error messages.
- **SC-003**: When a file contains multiple distinct violations, all violations are reported in a single parser response — no violations are silently swallowed by early exit.
- **SC-004**: A test suite covering every acceptance scenario defined in this spec achieves 100% scenario pass rate before the feature is marked complete.
- **SC-005**: No valid file (one that fully conforms to spec#014) is rejected by the parser — the false-positive rejection rate is zero.

## Assumptions

- The parser operates on the content of a single file at a time; cross-file uniqueness of `metadata.name` within a namespace is enforced by the datastore layer, not by this parser.
- `metadata.namespace` resolution from push context (when absent from the file) is handled by the caller; the parser treats an absent `metadata.namespace` as valid and leaves the field empty in the parsed result.
- Label value length constraints follow Kubernetes conventions (≤63 characters per value); this is a reasonable default not expected to require clarification.
- The parser does not validate the contents of `spec.categoryRef` against existing categories — cross-resource existence checks are deferred to the admission phase (GH#105, GH#106).
- `metadata.generateName` and `metadata.name` may coexist in an author-supplied file; name generation from `generateName` is handled by the admission layer, not the parser.
- Malformed YAML produces a single parse error (not a collection) since field-level errors cannot be extracted from unparseable input.
- The order of validation checks is: (1) YAML parse, (2) forbidden-field admission check, (3) required-field checks, (4) spec-level constraint rules. This ensures authors see the most fundamental errors first.
