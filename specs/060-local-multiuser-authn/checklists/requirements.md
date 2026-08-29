# Specification Quality Checklist: Local Multi-User AuthN + UserDir Provider (`static-users`)

**Purpose**: Validate specification completeness and quality before proceeding to planning
**Created**: 2026-08-29 (revised 2026-08-29)
**Feature**: [spec.md](../spec.md)

## Content Quality

- [x] No implementation details (languages, frameworks, APIs)
- [x] Focused on user value and business needs
- [x] Written for non-technical stakeholders
- [x] All mandatory sections completed

## Requirement Completeness

- [x] No [NEEDS CLARIFICATION] markers remain
- [x] Requirements are testable and unambiguous
- [x] Success criteria are measurable
- [x] Success criteria are technology-agnostic (no implementation details)
- [x] All acceptance scenarios are defined
- [x] Edge cases are identified
- [x] Scope is clearly bounded
- [x] Dependencies and assumptions identified

## Feature Readiness

- [x] All functional requirements have clear acceptance criteria
- [x] User scenarios cover primary flows
- [x] Feature meets measurable outcomes defined in Success Criteria
- [x] No implementation details leak into specification

## Notes

- **Revision history**: this spec was substantially rewritten after its first pass, which kept `static-admin` unchanged and added `static-users` as an opt-in sibling. The revision removes `static-admin` entirely; `static-users` fully replaces it, and "admin" becomes purely an `rbac-local` role name. Every artifact in this feature's doc set (`spec.md`, `plan.md`, `research.md`, `data-model.md`, `quickstart.md`, both `contracts/` files, `tasks.md`) was updated in the same pass — there should be no remaining reference anywhere in this doc set to `static-admin` coexisting with `static-users`, to the first draft's dedicated-JWT-secret design, or to the first draft's planned `ChainedAuthN.IssueSessionFor` addition (both superseded — see research.md Decisions 4/8, which explicitly record what changed and why, not just the new conclusion).
- User Story 3 (migration safety) encodes the revision's central new finding: removing `static-admin`'s hardcoded role assignment makes `rbac-local`'s `role_bindings` the *sole* source of any role, which converts "forgot a migration step" from a cosmetic gap into a silent-lockout hazard. FR-013 and the fail-fast startup check exist specifically to close this by construction, matching this spec's (and its first draft's) recurring principle: don't leave correctness dependent on operator discipline alone when a startup-time check can catch the mistake instead.
- The config-validation fix (FR-014/FR-015/FR-016) was reported from direct operator experience mid-revision, not derived from the spec's own original scope — it is included here because it is the same config-validation code surface (`gitstore-api/internal/config/config.go`'s `validateConfig`) this spec was already rewriting for the provider swap, and leaving a known, confirmed startup-ergonomics bug on that exact surface unaddressed would be an odd omission. `research.md` Decision 12 records the precise enumeration of which env vars were checked and why the third one (`auth.grpc.hmac_secret`) was ruled out rather than included.
- This spec's Clarifications section and Assumptions continue to cross-reference spec 059 (`specs/059-optional-oidc-provider/`) to record that the two specs remain complementary, unaffected by this revision.
