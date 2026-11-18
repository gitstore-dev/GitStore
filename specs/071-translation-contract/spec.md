# Feature Specification: Translation Resource Contract

**Feature Branch**: `071-translation-contract`
**Created**: 2026-11-18
**Status**: Draft
**Input**: User description: "#188 needs a spec: define the canonical Translation
resource contract, its frontmatter fields, validation/error boundaries and
examples before implementation."

## Scope and Dependencies

This feature specifies the contract-and-documentation slice in #188, under
the Translation initiative #78 and catalog-frontmatter initiative #40. It
establishes an authoring baseline, not a claim that Translation admission,
querying, reconciliation or localized content delivery is implemented.

**In scope**:

- The versioned Translation envelope, authored field shapes and requiredness.
- Reuse of common resource metadata and object-reference vocabulary.
- Structural validation and actionable error expectations.
- Field-only, body-only and combined authoring examples.
- Explicit ownership of the remaining semantic decisions and implementation.

**Out of scope**:

- Runtime parsers, schemas, admission handlers, persistence or endpoints.
- Detailed field-path grammar, selectors, duplicate/overlapping patch behavior
  and Markdown-body processing semantics, owned by #189.
- Detailed locale validation/canonicalization, the target-kind allowlist,
  reference resolution and target-field eligibility, owned by #190.
- Integration coverage and user-documentation rollout, owned by #191 after its
  prerequisites.
- Applying or merging translations, fallback selection, localized projections,
  translation tooling, migration tooling or legacy-format compatibility.
- New authentication providers, permissions, controllers, deployment topology,
  capacity guarantees or changes to ADR status.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Author a Translation without guessing its shape (Priority: P1)

A catalog author consults one canonical contract to write a Translation for a
locale and a referenced resource. The contract explains which values belong in
frontmatter, which content belongs in the Markdown body, and which metadata
belongs to the system rather than the author.

**Why this priority**: Subsequent validation and authoring tools cannot agree
until the resource has one unambiguous shape.

**Independent Test**: Using only the contract and its examples, a reviewer can
construct a field-only, body-only and combined Translation and identify every
required field without consulting implementation code.

**Acceptance Scenarios**:

1. **Given** a contributor needs a translated title, **When** they follow the
   contract, **Then** they can author the exact version/kind, resource identity,
   locale, target reference and a patch with `fieldPath` and `translation`.
2. **Given** a contributor needs only translated body content, **When** they
   follow the body-only example, **Then** they can omit `patches` and provide a
   non-empty Markdown body without inventing a body field inside `spec`.
3. **Given** both patches and a body are supplied, **When** the document is
   reviewed, **Then** the contract identifies both authored inputs while
   distinguishing their shape from the application semantics deferred to #189.

### User Story 2 - Understand a malformed document (Priority: P1)

A catalog author or validator implementer can distinguish malformed Translation
structure from domain checks that require the later semantic contracts.

**Why this priority**: Vague rejection behavior produces inconsistent validators
and forces authors to discover the resource format through failed pushes.

**Independent Test**: Review the documented invalid examples and validation
matrix; every structural failure names the offending field and violated rule,
and semantic cases name their owning follow-up rather than an invented result.

**Acceptance Scenarios**:

1. **Given** a missing locale, missing target name or non-string translation
   value, **When** the author consults the matrix, **Then** they find the
   violated structural rule and the location an eventual diagnostic must name.
2. **Given** the wrong resource version/kind or an unknown Translation field,
   **When** the document is classified, **Then** it is structurally invalid,
   rather than silently interpreted as another resource or an ignored patch.
3. **Given** a structurally complete reference to a missing target, **When**
   validation responsibilities are reviewed, **Then** the contract assigns its
   admission-versus-resolution behavior to #190 and does not fabricate current
   runtime behavior.

### User Story 3 - Implement dependent contracts consistently (Priority: P2)

Contributors responsible for field-path/body semantics and locale/target rules
share the same resource vocabulary and understand which decisions remain theirs.

**Why this priority**: A stable boundary allows independent follow-up work
without prematurely expanding this documentation slice into runtime delivery.

**Independent Test**: Trace each field and validation category to either this
baseline or its named semantic follow-up; no category has conflicting owners.

**Acceptance Scenarios**:

1. **Given** a contributor begins #189, **When** they read this contract, **Then**
   the patch entry shape and body channel are already fixed, while path grammar,
   selector resolution and conflicts remain explicitly in #189's scope.
2. **Given** a contributor begins #190, **When** they read this contract, **Then**
   locale and target-reference fields reuse the baseline shape, while locale
   equivalence, allowed targets and reference-resolution outcomes remain in
   #190's scope.
3. **Given** the contract is published, **When** a reader checks support status,
   **Then** proposed examples are not represented as working admission,
   reconciliation or localized-query procedures.

### Edge Cases

- Missing, null, empty or incorrectly typed required fields.
- A non-list `patches` value, non-object entries, missing patch members and
  non-string translation values.
- A body-only document versus a document with neither patches nor meaningful
  body content.
- An explicitly empty translation string, distinct from a missing or null value.
- Unknown envelope/spec/patch fields and attempted authoring of system metadata
  or status.
- Locale spelling variants, unsupported target kinds, namespace mismatches and
  absent targets: their structural fields are covered here; their domain rules
  belong to #190.
- Duplicate or overlapping paths, selectors, body/field collisions and fields
  whose target value is not text: their interpretation belongs to #189/#190.
- `File` as the media-content example in #78, without treating infrastructure or
  authorization resources as permitted Translation targets.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The published contract MUST define one canonical resource with
  `apiVersion: i18n.gitstore.dev/v1beta1`, `kind: Translation`, `metadata` and
  `spec`; both version and kind are required exact strings.
- **FR-002**: The contract MUST identify Translation as namespace-scoped, require
  an authored name and namespace using the existing common metadata rules, and
  distinguish author-controlled metadata from system-controlled identity,
  revision, generation and lifecycle fields. It MUST NOT introduce a parallel
  metadata format.
- **FR-003**: The contract MUST define `spec.locale` as a required non-empty
  string identifying a BCP 47 locale, `spec.targetRef` as a required reference
  object, and `spec.patches` as an optional ordered list of patch objects.
  Locale syntax, equivalence and canonicalization details belong to #190.
- **FR-004**: The contract MUST reuse the common object-reference vocabulary,
  require `targetRef.apiVersion`, `targetRef.kind` and `targetRef.name` as
  non-empty strings, and enumerate optional reference members and their
  ownership. Scope enforcement, target-kind eligibility, reference pins and
  missing-target behavior MUST be assigned to #190 rather than assumed.
- **FR-005**: Each patch MUST contain a non-empty string `fieldPath` and a string
  `translation`. A missing/null translation is invalid; an explicitly empty
  string is an authored value, not an omitted patch. Non-string values MUST NOT
  be silently converted to strings. Detailed path and conflict semantics belong
  to #189.
- **FR-006**: The contract MUST describe the Markdown body outside frontmatter
  as the translated body channel. It MUST allow field-only, body-only and
  combined documents, require at least one patch or a non-whitespace body, and
  distinguish this authoring rule from body preservation/application rules in
  #189. A synthetic body patch MUST NOT be required for body-only authoring.
- **FR-007**: The contract MUST enumerate required, optional and prohibited
  envelope/spec/patch fields, reject unknown fields at those levels, and apply
  the existing common rules to metadata and reference members. It MUST include
  an explicit structural validation table for missing, empty, null and
  incorrectly typed values.
- **FR-008**: The contract MUST distinguish authored content from observed
  status. Authoring examples MUST omit machine-owned `status` and metadata.
  Proposed conditions or resolved values from #78 MUST NOT be presented as
  emitted or supported before their runtime contracts are implemented.
- **FR-009**: Structural rejection examples MUST identify the document, offending
  field or patch entry, and violated rule without requiring disclosure of raw
  translated content, credentials or other tenants' resources. Existing
  admission/error conventions MUST be reused rather than inventing a competing
  error envelope.
- **FR-010**: The contract MUST include at least four complete valid examples:
  a Product field-only Translation, a Product body-only Translation, a combined
  document, and the media/File body example from #78. Each example MUST identify
  which later semantic checks remain prerequisites; none may imply permission
  to translate arbitrary non-catalog resources.
- **FR-011**: The contract MUST include invalid examples for wrong version/kind,
  missing locale/reference members, malformed patches, non-string translation
  values and absence of both translation channels. Structural cases MUST have
  a deterministic expected rejection; domain cases MUST name their owning
  semantic contract.
- **FR-012**: The canonical contributor contract and existing overview MUST
  agree on field names, resource version and support status. Follow-up issues
  MUST be able to reference this baseline without interpreting preliminary
  initiative examples as implemented behavior.

### Key Entities

- **Translation document**: A namespace-scoped authored resource identifying a
  locale and target, with field patches, Markdown body content, or both.
- **Target reference**: The common reference shape identifying the resource
  whose content is translated; domain resolution is a separate concern.
- **Translation definition**: One authored `fieldPath`/`translation` pair.
- **Structural validation case**: A document shape, violated rule, diagnostic
  location and expected classification, distinct from target-dependent checks.
- **Observed status**: System-owned information, not an input granting the
  author authority to assert resolution, readiness or successful application.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: One canonical contract supplies requiredness, types, ownership and
  omission/empty-value behavior for every envelope, spec and patch field.
- **SC-002**: All four required valid examples satisfy the documented structural
  rules, and a reviewer can author each supported shape using the contract
  alone, without inspecting implementation code.
- **SC-003**: Every structural negative example has exactly one documented
  violated rule and an actionable diagnostic location; none is silently
  accepted or described as a successful partial application.
- **SC-004**: Every deferred validation category maps explicitly to #189 or
  #190, with no contradictory ownership or claim of current runtime support.
- **SC-005**: The canonical contract and overview have zero conflicting resource
  versions, field names or support-status claims when reviewed together.

## Assumptions

- This feature delivers documentation, not a load-bearing service change.
  Production capacity, replica, authorization and recovery validation remain
  requirements for the later runtime features; this spec claims no such pass.
- The version, resource kind, locale, reference, patch member names and separate
  Markdown body channel come from #78 and the existing localization overview.
- The body-only File example in #78 is intentional media-content scope. The
  complete allowed-target set and eligible target fields remain #190's decision.
- Existing common metadata, object references and admission diagnostics are the
  starting vocabulary. No legacy-format compatibility or migration work is
  required for this contract slice.
- No ADR's core decision is implemented by writing this specification; no ADR
  acceptance or feature-closure status is changed.
