# Hierarchy Path Type Checklist: ancestorPath → path Rename

**Purpose**: Validate requirement quality (completeness, clarity, consistency, traceability) for changing the CategoryTaxonomy hierarchy path field from a slash-separated `ancestorPath: String!` to a `path: [String!]!` array, across spec 039 (CategoryTaxonomy Controller Reconciliation), spec 040 (Controller Watch API and Status Subresource Contract), and the pre-existing schema TODO.  
**Created**: 2026-08-07  
**Feature**: [spec.md](../spec.md) (040-controller-watch-status-api); also covers `specs/039-category-taxonomy-reconciler/spec.md` on branch `039-category-taxonomy-reconciler`

**Note**: This checklist tests whether the REQUIREMENTS are well-specified for this rename, not whether any code implements it correctly. No implementation has been written yet — the rename decision itself is still open.

## Scope Note

Formal gate depth. Covers three surfaces:
1. **Spec 040's new input-type mirror** — `ResolvedCategoryTaxonomyInput.ancestorPath` in `contracts/status-api.graphql` / `data-model.md`, being added by this spec
2. **Spec 039's requirements** — 13+ mentions of `ancestorPath` as a slash-separated string across FR-002, FR-005, FR-008, FR-015, SC-001, SC-002, acceptance scenarios, and edge cases in `specs/039-category-taxonomy-reconciler/spec.md`
3. **The pre-existing output-type TODO** — `shared/schemas/category.graphqls` line 185, `# TODO: Change to 'path: [String!]!'` on `ResolvedCategoryTaxonomy.ancestorPath`

## Requirement Completeness

- [ ] CHK001 Does spec 040 state whether renaming `ResolvedCategoryTaxonomyInput.ancestorPath` (input) also requires renaming the corresponding `ResolvedCategoryTaxonomy.ancestorPath` (existing output type) in the same change, or whether the two are permitted to diverge temporarily? [Completeness, Gap]
- [ ] CHK002 Does spec 039 define what "path" means once expressed as an array — is it root-to-self order (`["electronics", "computers", "laptops"]`) or self-to-root, and is that order stated explicitly rather than left to the reader's assumption from the slash-separated example? [Completeness, Gap]
- [ ] CHK003 Does either spec define the array's contents for a root-level category (empty array vs. single-element array containing only the category's own name)? [Completeness, Edge Case, Gap]
- [ ] CHK004 Does spec 039 define whether `path` (as an array) is still required to be reconstructible into the pre-existing slash-separated display format (e.g. for the storefront breadcrumb consumer named in US1's "Why this priority"), or whether that reconstruction becomes a separate, unspecified consumer responsibility? [Completeness, Gap]
- [ ] CHK005 Is there a requirement covering what happens to `gitstore-api/internal/graph/resolver/converters.go`'s existing `strings.Split(c.AncestorPath, "/")` derivation logic (which currently produces `Category.path: [String!]!` for GraphQL reads from the stored slash-separated string) once the stored/wire representation itself becomes an array — is the datastore's internal storage format (materialized path column) also expected to change, or only the wire-facing GraphQL field? [Completeness, Gap]

## Requirement Clarity

- [ ] CHK006 Is "path" as a replacement term for "ancestorPath" used unambiguously, given that `Category.path` already exists in `shared/schemas/category.graphqls` as a *derived, read-time* field distinct from the *materialized, write-time* `ResolvedCategoryTaxonomy.ancestorPath` — do the specs distinguish these two existing concepts clearly enough that reusing the name "path" for the renamed field won't be confused with the pre-existing `Category.path`? [Clarity, Ambiguity, Spec 040 §data-model.md]
- [ ] CHK007 Where spec 039 says "materialized ancestor path expressed as a slash-separated sequence of ancestor names" (FR-002), is it clear whether this requirement text itself must be rewritten for an array representation, or whether the requirement is describing a *concept* that is separately encoded either way? [Clarity, Ambiguity, Spec 039 §FR-002]
- [ ] CHK008 Is the acceptance-scenario language in spec 039 ("leaf depth 2, path `electronics/computers/laptops`") clear about whether the literal backtick-quoted value shown is a display convention only, or a literal expected field value that would need updating to an array-literal form? [Clarity, Ambiguity, Spec 039 §Acceptance Scenario 1]

## Requirement Consistency

- [ ] CHK009 Are spec 039 and spec 040 consistent with each other on the field's type — does spec 040's `contracts/status-api.graphql`/`data-model.md` (currently `ancestorPath: String!`) match whatever spec 039 ultimately requires the reconciler to compute and write? [Consistency, Spec 039 §FR-002, Spec 040 §data-model.md]
- [ ] CHK010 Is the field name/type consistent between spec 040's `ResolvedCategoryTaxonomyInput` (mutation-input mirror) and the pre-existing `ResolvedCategoryTaxonomy` output type it mirrors — do the two specs agree on whether both change together or one lags the other? [Consistency, Spec 040 §research.md R4]
- [ ] CHK011 Does spec 039's edge-case text ("descendants are only recomputed after their ancestor's path is settled") remain internally consistent with FR-005/FR-008's cycle-handling language once "path" no longer implies a single string that can be trivially compared for staleness — is there a defined equality/staleness check for the array form? [Consistency, Spec 039 §Edge Cases, §FR-008]

## Acceptance Criteria Quality

- [ ] CHK012 Can spec 039's SC-001 ("shows correct depth and ancestorPath in status") be objectively verified against an array-typed field without the success criterion itself being rewritten to state array-equality rather than string-equality? [Measurability, Spec 039 §SC-001]
- [ ] CHK013 Are the acceptance scenarios in spec 039 (US1 AC1/AC2/AC4) rewritten (or flagged as needing rewriting) with concrete array-literal expected values, rather than left with slash-separated string examples that no longer match the field's type? [Acceptance Criteria Quality, Spec 039 §Acceptance Scenarios]

## Scenario Coverage

- [ ] CHK014 Is there a requirement covering the migration/backfill scenario — existing `CategoryTaxonomy` rows presumably already carry a slash-separated `ancestor_path` string in the datastore; do either spec's requirements address what happens to already-admitted resources when the field's shape changes? [Coverage, Gap]
- [ ] CHK015 Does either spec address whether this is a breaking change to the `Category`/`CategoryTaxonomy` GraphQL contract (an existing, presumably-consumed field changing shape) versus an additive change, and if breaking, whether the constitution's "additive changes preferred, deprecation before removal" schema-evolution principle applies here? [Consistency, Gap, Constitution Principle III]

## Dependencies & Assumptions

- [ ] CHK016 Is the dependency between spec 040's `ResolvedCategoryTaxonomyInput` and spec 039's reconciler-computed `resolved` payload made explicit enough that a type change decided in one spec is guaranteed to propagate to the other before either is implemented? [Dependency, Spec 040 §research.md R8]
- [ ] CHK017 Is there a stated assumption about why an array is preferred over the slash-separated string (e.g. avoiding delimiter-escaping issues for category names containing `/`, or aligning with the pre-existing `Category.path` read-side convention) — or is the preference currently undocumented rationale living only in conversation, not in either spec's Assumptions section? [Traceability, Assumption, Gap]

## Ambiguities & Conflicts

- [ ] CHK018 Is there a naming conflict risk flagged anywhere in the requirements between the renamed `ResolvedCategoryTaxonomy(Input).path` and the existing, semantically-different `Category.path` (already an array, already read-time-derived) — without an explicit disambiguation requirement, are two different concepts about to share one field name across two levels of the same resource's GraphQL shape? [Conflict, Ambiguity, Gap]
- [ ] CHK019 Is a requirement/acceptance-criteria ID scheme cross-reference established between spec 039 and spec 040 for this shared field, so a future reader can trace "the `path` field's type" back to a single authoritative decision rather than two independently-evolving descriptions? [Traceability, Gap]

## Notes

- No code has been changed. This checklist exists to surface what the *requirements* need to say before either spec's implementation tasks reference a `path`/`ancestorPath` field.
- CHK001, CHK005, CHK006, and CHK018 flag the highest-risk ambiguity: reusing the name "path" for the renamed field risks colliding, in meaning, with the pre-existing `Category.path` (a different, already-shipped field). Resolve this naming question before editing either spec.
- If proceeding with the rename, spec 039 requires updates to FR-002, FR-005, FR-008, FR-015, SC-001, SC-002, all four US1 acceptance scenarios, and two edge cases — not just a find-and-replace of the identifier, since several of these describe the *string* semantics (slash-separated) explicitly.
