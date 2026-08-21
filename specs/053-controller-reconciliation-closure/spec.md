# Feature Specification: Controller Manager Reconciliation Loop Closeout and Rescope (GH#165)

**Feature Branch**: `053-controller-reconciliation-closure`
**Created**: 2026-08-20
**Status**: Closed
**Input**: User description: "Controller Manager Reconciliation Loop for Core Resources and CRDs — Phase-1 closeout and rescope of GH#165. GH#165 is an umbrella issue whose own Implementation Plan (#180-#183) and Namespace-specific slice (spec 046) are fully shipped. Its body-text dependency list (#131, #149, #164) is stale: #131 is closed, #149/#164 are Phase-4 CRD work per docs/implementation/032-phased-implementation.md, and should not gate a Phase-1 initiative. #243 (CategoryTaxonomy deletion/GC) is the one legitimate Phase-1-relevant dependent still citing #165 as a blocker, but that blocker is resolved now that the controller-manager runtime exists; #243's real remaining gap is its own unwritten spec (specs/052-categorytaxonomy-deletion-semantics). This spec documents that audit and specifies the closeout/rescope actions needed to bring GH#165 and its dependency graph in line with actual code and doc state."

## User Scenarios & Testing *(mandatory)*

<!--
  This is not a net-new user-facing feature. It is an initiative-closeout and
  issue-rescope artifact. The "users" are maintainers, issue triage/project
  management, and downstream spec authors who consult GH#165 to decide what
  work remains. Stories are written as their real workflows.
-->

### User Story 1 - Maintainer determines Phase-1 reconciliation status without re-deriving the audit (Priority: P1)

A maintainer reviewing GH#165 today needs to know, without re-reading every linked issue and diffing it against the codebase, whether "the controller manager reconciliation loop for core resources" is actually done for Phase 1, and if not, what specifically remains and why.

**Why this priority**: This is the core value of the spec — it is a citable, durable answer to "why is #165 still open" that survives beyond one person's memory of doing the audit. Without it, every future maintainer re-derives the same investigation from scratch.

**Independent Test**: Can be fully tested by having a maintainer read this spec's evidence section and confirm, by checking the cited files (`gitstore-controller-manager/internal/namespace/reconciler.go`, `specs/046-namespace-api-semantics/`) and cited closed issues (#180-#183), that the Phase-1 reconciliation-runtime claim is verifiable and accurate as of the spec's creation date.

**Acceptance Scenarios**:

1. **Given** GH#165 is open and a maintainer has this spec, **When** they check whether the core Phase-1 reconciliation loop (runtime, reconcile-handler contract, startup resume, integration tests) is implemented, **Then** they can confirm it via the four closed issues (#180, #181, #182, #183) and the merged spec 046 namespace reconciler without opening any other document.
2. **Given** a maintainer wants evidence for the namespace-specific slice of #165, **When** they inspect `gitstore-controller-manager/internal/namespace/reconciler.go`, **Then** they find the `ForegroundDeletionFinalizer` constant and a `HasRepositories`-gated drain check, confirming the finalizer/auto-provisioning behavior described in spec 046 is live in code, not just planned.

---

### User Story 2 - Issue triage resolves GH#165's stale dependency list (Priority: P1)

Whoever owns issue triage needs to reconcile GH#165's body-text "Depends on: #131, #149, #164" line with reality: one dependency is closed, two are legitimately out of phase, and the issue's actual remaining Phase-1-relevant dependent (#243) isn't even listed there.

**Why this priority**: An open umbrella issue with a stale, contradictory dependency list actively misleads anyone using it to plan work — it implies Phase-4 CRD work is required before Phase-1 closure, and it hides the one dependent that actually matters right now.

**Independent Test**: Can be tested independently by checking GH#165's dependency-list text against this spec's rescope table and confirming each entry's disposition (closed / re-tracked to Phase 4 / superseded by #243) is unambiguous and actionable without further investigation.

**Acceptance Scenarios**:

1. **Given** GH#165 lists "#131, #149, #164" as dependencies, **When** triage applies this spec's findings, **Then** #131 is recognized as already closed (no action), and #149/#164 are re-tracked under Phase-4 CRD tracking (new or existing Phase-4-labeled tracking issue/milestone) rather than left as unresolved blockers on a Phase-1-titled initiative.
2. **Given** #243 states "Blocked by: #165 (controller manager runtime needed for GC reconciliation)," **When** triage applies this spec's findings, **Then** they record that this blocker is resolved (runtime exists per spec 046 + #180-183) and that #243's only real remaining gap is its own spec authoring/implementation (tracked at `specs/052-categorytaxonomy-deletion-semantics`).

---

### User Story 3 - Downstream spec author confirms the controller-manager runtime is safe to build on (Priority: P2)

A downstream spec author (e.g., the author of `specs/052-categorytaxonomy-deletion-semantics`, or a future author of a similar reconciler-dependent spec) needs confidence that the controller-manager runtime foundation referenced by their spec's dependencies is genuinely available, not aspirational.

**Why this priority**: This unblocks concurrent and future spec work that cites GH#165 as a prerequisite; it is valuable but secondary to the triage-facing closure itself (P1 stories), since without those the dependency data downstream authors would consult is still wrong.

**Independent Test**: Can be tested independently by having a downstream spec author cross-reference this spec's evidence section before writing a "Depends on #165" line in their own spec, and confirming they can instead cite the specific shipped capability (e.g., spec 046's finalizer/drain-check pattern) rather than the umbrella issue.

**Acceptance Scenarios**:

1. **Given** a new spec needs a controller-manager reconciliation runtime capability, **When** its author consults this spec, **Then** they find a direct pointer to the shipped runtime foundation (closed #180-#183, spec 046) instead of an open, ambiguous umbrella issue.

---

### Edge Cases

- What happens if #243's own spec (`specs/052-categorytaxonomy-deletion-semantics`) is abandoned or its scope changes such that it no longer needs the controller-manager runtime? Then GH#165 has no remaining legitimate Phase-1/2 dependent, and this spec's closure recommendation (Success Criteria SC-004) applies immediately rather than waiting on #243.
- What happens if #149 or #164 land ahead of schedule during Phase 1 or 2 instead of Phase 4? The rescope recommendation in this spec is about tracker hygiene (matching the phase model to actual scope), not about blocking or delaying that work — early delivery does not invalidate the recommendation to re-label them under Phase-4 tracking; it simply means that tracking issue closes early.
- What happens if a future audit finds GH#165's Namespace-slice claim (spec 046 shipped) was only partially true (e.g., a regression reopened a gap)? This spec's evidence citations are point-in-time (as of 2026-08-20); a maintainer finding contradicting evidence should treat this spec as stale and re-audit rather than trusting it blindly — see Assumptions.
- What happens if GitHub's structured `trackedIssues`/`trackedInIssues` fields are populated later (e.g., via automation) so that they stop being empty for #165/#243? The body-text dependency convention this spec relies on becomes redundant with structured data at that point; triage should prefer structured fields once available, but until then body text remains authoritative per this repo's established convention.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: This spec MUST document, with citable evidence, that GH#165's own stated Implementation Plan (#180 runtime foundations, #181 reconcile handler contract, #182 startup resume, #183 integration tests + runbook) is fully closed, and that no further implementation work is required to satisfy that plan.
- **FR-002**: This spec MUST document, with citable evidence (file path and identifying symbol), that GH#165's Namespace-specific slice — the `createNamespace`/`deleteNamespace` foreground-deletion finalizer and `gitstore-system` auto-provisioning — is implemented and merged via spec 046, and MUST NOT prescribe re-implementing any part of it.
- **FR-003**: This spec MUST enumerate GH#165's body-text-declared dependencies (#131, #149, #164) and assign each a definitive disposition: closed-no-action, or re-tracked-to-Phase-4, with the phase-table citation (`docs/implementation/032-phased-implementation.md`) justifying any Phase-4 assignment.
- **FR-004**: This spec MUST identify #243 (CategoryTaxonomy Deletion Semantics, OwnerReferences, and Garbage Collection) as the sole remaining Phase-1/2-relevant item that nominally cites GH#165 as a blocker, and MUST state that this blocker is resolved as of this audit because the controller-manager runtime it depends on already exists.
- **FR-005**: This spec MUST cross-reference `specs/052-categorytaxonomy-deletion-semantics` (GH#243) by path as the vehicle through which #243's own remaining gap (an unwritten/unimplemented spec, not a missing runtime) is to be closed, regardless of whether that spec directory exists yet at the time this spec is read.
- **FR-006**: This spec MUST state that #149 (Dynamic GraphQL Schema Synthesis and Stitching for Custom Kinds) and #164 (Hub-and-Spoke CRD Versioning with WASI Conversion Hooks) are out of Phase-1 and Phase-2 scope in their entirety, per the phase table in `docs/implementation/032-phased-implementation.md`, and MUST NOT be treated as blockers for closing GH#165's Phase-1 scope.
- **FR-007**: This spec MUST recommend that #149 and #164 be re-tracked under their own Phase-4-labeled tracking issue(s) or milestone, separated from GH#165, so that the issue tracker's phase model matches the documented phase plan and current code state.
- **FR-008**: This spec MUST recommend that GH#165 be formally closed once #243 ships (i.e., once `specs/052-categorytaxonomy-deletion-semantics` is implemented and merged), with the #149/#164 re-tracking (FR-007) completed independently of and not gating that closure.
- **FR-009**: This spec MUST NOT introduce, modify, or plan any code, schema, API, or runtime behavior change — its only deliverables are this specification document and its generated requirements checklist. `/speckit-plan` and `/speckit-tasks` MUST NOT be run against this spec.
- **FR-010**: This spec's evidence section MUST distinguish between GitHub's structured issue-relationship fields (`trackedIssues`/`trackedInIssues`, confirmed empty for #165 and #243 at audit time) and this repository's informal convention of recording dependencies as issue-body text, and MUST state explicitly that body text is treated as authoritative for this audit given that convention.

### Key Entities *(include if feature involves data)*

- **GH#165 (umbrella issue)**: The initiative issue being audited and rescoped. Attributes relevant here: title, open/closed state, body-text dependency list, linked Implementation Plan sub-issues (#180-#183), Namespace-slice description.
- **Implementation Plan sub-issues (#180, #181, #182, #183)**: Closed issues representing runtime foundations, reconcile handler contract, startup resume, and integration tests/runbook respectively. Evidence that GH#165's Phase-1 engineering plan is complete.
- **Dependency issues (#131, #149, #164)**: Issues cited in GH#165's body text as dependencies. #131 is closed; #149 and #164 are open, Phase-4-scoped CRD work.
- **Dependent issue (#243)**: CategoryTaxonomy Deletion Semantics, OwnerReferences, and Garbage Collection. Cites GH#165 as a blocker in its own body text; the one legitimate remaining Phase-1/2 linkage to GH#165.
- **specs/046-namespace-api-semantics**: Merged spec providing the evidence for GH#165's Namespace-slice closure (foreground-deletion finalizer, `HasRepositories` drain check, `gitstore-system` auto-provisioning).
- **specs/052-categorytaxonomy-deletion-semantics**: The (possibly not-yet-existing) spec that is the intended vehicle for closing #243, and therefore for removing the last legitimate blocker keeping GH#165 open.
- **docs/implementation/032-phased-implementation.md**: The phase-table document used to justify the Phase-4 disposition of #149 and #164.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A maintainer with no prior context on GH#165 can determine its true Phase-1 completion status and remaining blockers by reading only this spec, in under 10 minutes, without independently re-auditing GitHub or the codebase.
- **SC-002**: 100% of GH#165's body-text-declared dependencies (#131, #149, #164) have an unambiguous, spec-documented disposition (closed / re-tracked to Phase 4) with a citable justification.
- **SC-003**: The one legitimate remaining Phase-1/2 dependent of GH#165 (#243) has its blocking condition on #165 explicitly marked resolved in this spec, with a named successor artifact (`specs/052-categorytaxonomy-deletion-semantics`) responsible for its own remaining closure work.
- **SC-004**: Once `specs/052-categorytaxonomy-deletion-semantics` (GH#243) ships, GH#165 can be closed within one triage cycle using this spec as the sole supporting rationale, with zero new investigation required at that time.
- **SC-005**: #149 and #164 are re-tracked into Phase-4-labeled tracking within one triage cycle of this spec's publication, removing them as apparent blockers on a Phase-1-titled initiative.

## Assumptions

- **Point-in-time audit**: The evidence in this spec (issue states, closed/open status, code citations) reflects the state observed as of 2026-08-20. If GH#165, #243, #131, #149, or #164 change state after that date, this spec's factual claims should be re-verified before being relied upon for closure; this spec does not self-update.
- **Body text as dependency convention**: Because GitHub's structured `trackedIssues`/`trackedInIssues` GraphQL relationship fields return empty for both #165 and #243, this spec treats each issue's body-text "Depends on" / "Blocked by" statements as the authoritative dependency record, consistent with this repository's established informal convention. If this repo later adopts structured issue linking, that should supersede body text.
- **`specs/052-categorytaxonomy-deletion-semantics` may not exist yet**: This spec references that path as the intended closure vehicle for #243 regardless of whether the directory exists at read time, since it is being authored concurrently by a separate workstream. If that spec is renumbered or its scope diverges from GC/deletion-semantics for CategoryTaxonomy, the cross-reference in FR-005/SC-003 should be updated to point at whatever artifact actually closes #243.
- **No structured GitHub sub-issue relationship exists for #165 ↔ #243 or #165 ↔ #131/#149/#164**: Confirmed via direct GraphQL query at audit time; this spec does not assume that will remain true indefinitely.
- **Scope of "Phase 1" and "Phase 4"**: This spec adopts the phase definitions and issue-to-phase mapping in `docs/implementation/032-phased-implementation.md` as the authoritative phase model. It does not re-derive or re-justify that phase model from scratch.
- **No code changes accompany this spec**: Because this is a closeout/rescope artifact rather than a feature, no Production Requirements section (replica safety, multi-user security, capacity, backpressure, recovery) applies — those are relevant only to code/runtime-affecting specs, and this spec explicitly makes none of those changes (FR-009). The Production Requirements section is therefore omitted rather than filled with placeholders.
