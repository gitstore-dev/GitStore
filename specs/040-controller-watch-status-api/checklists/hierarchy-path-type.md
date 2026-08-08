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

- [x] CHK001 Does spec 040 state whether renaming `ResolvedCategoryTaxonomyInput.ancestorPath` (input) also requires renaming the corresponding `ResolvedCategoryTaxonomy.ancestorPath` (existing output type) in the same change, or whether the two are permitted to diverge temporarily? [Completeness, Gap] — **Resolved**: research.md R9 states both change together, not independently.
- [x] CHK002 Does spec 039 define what "path" means once expressed as an array — is it root-to-self order (`["electronics", "computers", "laptops"]`) or self-to-root, and is that order stated explicitly rather than left to the reader's assumption from the slash-separated example? [Completeness, Gap] — **Resolved on spec 040 side**: research.md R9 and contracts/status-api.graphql's `path` field doc both state root-to-self order explicitly. Spec 039's own text still needs the corresponding update (tracked there).
- [x] CHK003 Does either spec define the array's contents for a root-level category (empty array vs. single-element array containing only the category's own name)? [Completeness, Edge Case, Gap] — **Resolved on spec 040 side**: single-element array, stated in research.md R9 and the field doc comment.
- [ ] CHK004 Does spec 039 define whether `path` (as an array) is still required to be reconstructible into the pre-existing slash-separated display format (e.g. for the storefront breadcrumb consumer named in US1's "Why this priority"), or whether that reconstruction becomes a separate, unspecified consumer responsibility? [Completeness, Gap] — out of scope for spec 040; tracked for spec 039.
- [ ] CHK005 Is there a requirement covering what happens to `gitstore-api/internal/graph/resolver/converters.go`'s existing `strings.Split(c.AncestorPath, "/")` derivation logic (which currently produces `Category.path: [String!]!` for GraphQL reads from the stored slash-separated string) once the stored/wire representation itself becomes an array — is the datastore's internal storage format (materialized path column) also expected to change, or only the wire-facing GraphQL field? [Completeness, Gap] — still open; this is an implementation-planning question for whichever spec touches `converters.go` (likely spec 039's plan phase, since it owns the reconciler that writes the materialized value).

## Requirement Clarity

- [x] CHK006 Is "path" as a replacement term for "ancestorPath" used unambiguously, given that `Category.path` already exists in `shared/schemas/category.graphqls` as a *derived, read-time* field distinct from the *materialized, write-time* `ResolvedCategoryTaxonomy.ancestorPath` — do the specs distinguish these two existing concepts clearly enough that reusing the name "path" for the renamed field won't be confused with the pre-existing `Category.path`? [Clarity, Ambiguity, Spec 040 §data-model.md] — **Resolved**: both research.md R9 and the field's GraphQL doc comment (contracts/status-api.graphql) now explicitly distinguish the two fields by type and by read-time-vs-write-time semantics.
- [ ] CHK007 Where spec 039 says "materialized ancestor path expressed as a slash-separated sequence of ancestor names" (FR-002), is it clear whether this requirement text itself must be rewritten for an array representation, or whether the requirement is describing a *concept* that is separately encoded either way? [Clarity, Ambiguity, Spec 039 §FR-002] — open; tracked for spec 039.
- [ ] CHK008 Is the acceptance-scenario language in spec 039 ("leaf depth 2, path `electronics/computers/laptops`") clear about whether the literal backtick-quoted value shown is a display convention only, or a literal expected field value that would need updating to an array-literal form? [Clarity, Ambiguity, Spec 039 §Acceptance Scenario 1] — open; tracked for spec 039.

## Requirement Consistency

- [x] CHK009 Are spec 039 and spec 040 consistent with each other on the field's type — does spec 040's `contracts/status-api.graphql`/`data-model.md` (currently `ancestorPath: String!`) match whatever spec 039 ultimately requires the reconciler to compute and write? [Consistency, Spec 039 §FR-002, Spec 040 §data-model.md] — **Resolved on spec 040 side** (now `path: [String!]!`); spec 039 must be updated to match — research.md R9's Scope Note flags this explicitly as a pending cross-spec dependency.
- [x] CHK010 Is the field name/type consistent between spec 040's `ResolvedCategoryTaxonomyInput` (mutation-input mirror) and the pre-existing `ResolvedCategoryTaxonomy` output type it mirrors — do the two specs agree on whether both change together or one lags the other? [Consistency, Spec 040 §research.md R4] — **Resolved**: R9 states both change together.
- [ ] CHK011 Does spec 039's edge-case text ("descendants are only recomputed after their ancestor's path is settled") remain internally consistent with FR-005/FR-008's cycle-handling language once "path" no longer implies a single string that can be trivially compared for staleness — is there a defined equality/staleness check for the array form? [Consistency, Spec 039 §Edge Cases, §FR-008] — open; tracked for spec 039.

## Acceptance Criteria Quality

- [ ] CHK012 Can spec 039's SC-001 ("shows correct depth and ancestorPath in status") be objectively verified against an array-typed field without the success criterion itself being rewritten to state array-equality rather than string-equality? [Measurability, Spec 039 §SC-001] — open; tracked for spec 039.
- [ ] CHK013 Are the acceptance scenarios in spec 039 (US1 AC1/AC2/AC4) rewritten (or flagged as needing rewriting) with concrete array-literal expected values, rather than left with slash-separated string examples that no longer match the field's type? [Acceptance Criteria Quality, Spec 039 §Acceptance Scenarios] — open; tracked for spec 039.

## Scenario Coverage

- [ ] CHK014 Is there a requirement covering the migration/backfill scenario — existing `CategoryTaxonomy` rows presumably already carry a slash-separated `ancestor_path` string in the datastore; do either spec's requirements address what happens to already-admitted resources when the field's shape changes? [Coverage, Gap] — still open for both specs; no migration/backfill requirement written yet anywhere.
- [ ] CHK015 Does either spec address whether this is a breaking change to the `Category`/`CategoryTaxonomy` GraphQL contract (an existing, presumably-consumed field changing shape) versus an additive change, and if breaking, whether the constitution's "additive changes preferred, deprecation before removal" schema-evolution principle applies here? [Consistency, Gap, Constitution Principle III] — still open; `ResolvedCategoryTaxonomy.ancestorPath` is a pre-existing shipped field being changed, not a purely additive change — this needs an explicit call-out in whichever spec's plan.md touches the output type.

## Dependencies & Assumptions

- [x] CHK016 Is the dependency between spec 040's `ResolvedCategoryTaxonomyInput` and spec 039's reconciler-computed `resolved` payload made explicit enough that a type change decided in one spec is guaranteed to propagate to the other before either is implemented? [Dependency, Spec 040 §research.md R8] — **Resolved**: R9's Scope Note explicitly cross-references this checklist and states the two specs must agree before either is implemented.
- [x] CHK017 Is there a stated assumption about why an array is preferred over the slash-separated string (e.g. avoiding delimiter-escaping issues for category names containing `/`, or aligning with the pre-existing `Category.path` read-side convention) — or is the preference currently undocumented rationale living only in conversation, not in either spec's Assumptions section? [Traceability, Assumption, Gap] — **Resolved**: research.md R9's Rationale documents both reasons (delimiter-escaping edge case, alignment with `Category.path` precedent).

## Ambiguities & Conflicts

- [x] CHK018 Is there a naming conflict risk flagged anywhere in the requirements between the renamed `ResolvedCategoryTaxonomy(Input).path` and the existing, semantically-different `Category.path` (already an array, already read-time-derived) — without an explicit disambiguation requirement, are two different concepts about to share one field name across two levels of the same resource's GraphQL shape? [Conflict, Ambiguity, Gap] — **Resolved**: flagged and disambiguated in research.md R9 and the field doc comment; confirmed no schema-level collision (different types), only a documentation obligation, which is now met.
- [ ] CHK019 Is a requirement/acceptance-criteria ID scheme cross-reference established between spec 039 and spec 040 for this shared field, so a future reader can trace "the `path` field's type" back to a single authoritative decision rather than two independently-evolving descriptions? [Traceability, Gap] — partially resolved: spec 040 now points to spec 039 (research.md R9 Scope Note); spec 039 does not yet point back. Fully resolved once spec 039 is updated.

## Notes

- Spec 040's side of this rename is applied: `contracts/status-api.graphql`, `data-model.md`, `research.md` (new R9), and `quickstart.md` all use `path: [String!]!` / `Path []string` now, with the `Category.path` vs. `ResolvedCategoryTaxonomy.path` distinction documented at each site.
- Remaining open items (CHK004, CHK005, CHK007, CHK008, CHK011, CHK012, CHK013, CHK014, CHK015, CHK019) require editing spec 039 (`specs/039-category-taxonomy-reconciler/spec.md`, on branch `039-category-taxonomy-reconciler`) — FR-002, FR-005, FR-008, FR-015, SC-001, SC-002, all four US1 acceptance scenarios, and two edge cases need corresponding updates. Not a pure find-and-replace, since several describe the *string* semantics (slash-separated) explicitly.
- CHK005/CHK014/CHK015 (converters.go derivation logic, migration/backfill, breaking-change classification) remain open regardless of which spec claims them — they are implementation-planning-level questions, not yet answered by either spec's current text.
