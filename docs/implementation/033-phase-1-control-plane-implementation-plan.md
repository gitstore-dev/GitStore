# Phase 1 Control-Plane Implementation Plan

**Status**: Proposed

**Date**: 2026-06-26

**Audience**: GitStore engineering team.

This document synthesises ADRs 0002–0008 into a concrete Phase 1 execution plan.
Read it alongside `docs/implementation/phases.md` (Phase 1 alpha objectives) and the
individual lifecycle ADRs it references.

## Executive summary

Phase 1 alpha must close the architecture proof: admission is end-to-end, all core
Git-backed catalog resources have stable contracts, GraphQL mutations delegate to git
rather than bypass it, and the namespace/repository control plane enforces deletion
ordering through foreground finalizers. The seven lifecycle ADRs establish:

1. **Namespace** (ADR-0002) and **Repository** (ADR-0003) are hybrid resources with a
   bootstrap exception. Subsequent management is git-backed via `gitstore-system`.
2. **Product**, **ProductVariant**, **CategoryTaxonomy**, **Collection**, **File**
   (ADRs 0004–0008) are fully git-backed. GraphQL mutations delegate to git commits;
   no direct datastore writes exist for these kinds.
3. Foreground finalizers block deletion of owners until all dependents are drained.
   The ordering is strict: Namespace → Repository → catalog resources.
4. Cross-namespace references are rejected at admission time in Phase 1.
5. Two-phase reference resolution: push-time structural validation is synchronous and
   blocking; semantic resolution (does the referenced object exist?) is async and
   controller-managed.
6. The `gitstore-system` repository is the default target for GraphQL mutations that
   do not specify a repository; convention-based directory placement (`products/`,
   `categories/`, etc.) applies.

## Dependency graph

```
Namespace (ADR-0002)
  └─► Repository (ADR-0003)   ← owns gitstore-system (bootstrap); other repos via git
        ├─► CategoryTaxonomy (ADR-0006)
        │     └── spec.parentRef → CategoryTaxonomy (self, same repo/namespace)
        │     └── spec.media[*].fileRef → File (Phase 2, GH#244)
        ├─► File (ADR-0008)
        │     └── ◄── fileRef from Product / ProductVariant / CategoryTaxonomy / Collection
        ├─► Product (ADR-0004)
        │     └── spec.categoryRef → CategoryTaxonomy (async)
        │     └── spec.media[*].fileRef → File (async, Phase 2)
        │     └─► ProductVariant (ADR-0005)  [ownerRef on variant]
        │           └── spec.productRef → Product (required; async if co-created)
        │           └── spec.media[*].fileRef → File (async, Phase 2)
        └─► Collection (ADR-0007)
              └── spec.selector → Product labels (live projection, no ref stored)
              └── spec.media[*].fileRef → File (async, Phase 2)
```

**No circular dependencies.** `File` is a leaf; it is referenced by catalog resources
but does not reference any. `Collection` references `Product` via selector only
(read-only, no back-ref). `CategoryTaxonomy` self-references via `parentRef` but the
acyclicity invariant prevents loops.

## Resolved design decisions (incorporated from user direction)

| Decision                                     | Resolution                                                                                                                                                                                                                                                                                |
|----------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Deletion ordering for Namespace/Repository   | Foreground finalizer: dependents must drain before owner is removed; no silent cascade. `deleteNamespace` rejected if repos exist; `deleteRepository` rejected if catalog resources exist.                                                                                                |
| GraphQL mutation write path                  | Mutations are not blocked. `create`/`update`/`delete` mutations delegate to git (commit + admission) rather than directly writing to datastore. Catalog mutations without a repository name target `gitstore-system` using convention-based directory placement.                          |
| Cross-namespace references                   | Rejected at admission time in Phase 1. All `categoryRef`, `parentRef`, `fileRef`, `productRef` must be in the same namespace as the referencing resource.                                                                                                                                 |
| Orphaned resources after Repository deletion | GC handles this: catalog resources carry `ownerReferences` pointing at the Repository record. When the Repository is deleted (after catalog resources have drained), any remaining controller-managed derived artefacts are cleaned up by the controller as part of finalizer processing. |
| Namespace/Repository classification          | Hybrid. Bootstrap path is direct datastore write (no git). Post-bootstrap management is git-backed via `gitstore-system`.                                                                                                                                                                 |

## Recommended execution order

Group issues into four sequential waves. Each wave is independently mergeable; later
waves depend on the earlier ones being stable.

### Wave 1 — Control-plane foundation (prerequisites for everything)

These close before any catalog resource work is trustworthy.

| Priority | Issue / Work item                                       | What it provides                                                                           | ADR dependency            |
|----------|---------------------------------------------------------|--------------------------------------------------------------------------------------------|---------------------------|
| P0       | GH#225, GH#226 (authn phases 1–3, merged)               | Identity on all write paths                                                                | ADR-0002, ADR-0003        |
| P0       | GH#126 (phase 4 — HMAC gRPC, `cmd/gitctl`)              | Secure git-service channel                                                                 | ADR-0003                  |
| P0       | GH#123 (API Admission Controller contract)              | Push rejection envelope; admission chain                                                   | ADR-0004 through ADR-0008 |
| P0       | GH#174 (Namespace Validation and Admission Matrix)      | Namespace `Active` enforcement at admission                                                | ADR-0002                  |
| P1       | GH#165 (Namespace lifecycle)                            | `createNamespace`/`deleteNamespace` foreground finalizer; `gitstore-system` auto-provision | ADR-0002                  |
| P1       | GH#40 / GH#67 (Repository lifecycle, API-driven create) | Repository CRUD; `gitstore-system` convention; foreground finalizer                        | ADR-0003                  |

### Wave 2 — Core catalog resource contracts

These implement the Phase 1 exit criteria for git-backed catalog resources.

| Priority | Issue / Work item                                        | What it provides                                                                                     | ADR dependency |
|----------|----------------------------------------------------------|------------------------------------------------------------------------------------------------------|----------------|
| P1       | GH#77 (Product frontmatter)                              | `createProduct`/`updateProduct`/`deleteProduct`; admission; finalizer                                | ADR-0004       |
| P1       | GH#82 remaining (CategoryTaxonomy deletion + controller) | Deletion rejection when children/products exist; `ancestorPath` recomputation; cycle detection       | ADR-0006       |
| P1       | GH#79 (File frontmatter baseline)                        | `createFile`/`updateFile`/`deleteFile`; fileRef reference check at delete                            | ADR-0008       |
| P1       | GH#143 (ProductVariant as sellable unit)                 | Enforce `spec.productRef` required; `ProductResolved` condition; variant-first contract              | ADR-0005       |
| P1       | GH#84 (Collection remaining work)                        | `createCollection`/`updateCollection`/`deleteCollection`; selector evaluation; membership projection | ADR-0007       |

ProductVariant frontmatter (GH#83) is already closed; GH#143 closes the contract
enforcement gap.

### Wave 3 — Lifecycle versioning and admission hardening

| Priority | Issue / Work item                                                | What it provides                                                                         | ADR dependency            |
|----------|------------------------------------------------------------------|------------------------------------------------------------------------------------------|---------------------------|
| P1       | GH#137 (Object Lifecycle Versioning Contract)                    | UID preservation on rename/move; resourceVersion monotonicity; delete/recreate semantics | ADR-0004 through ADR-0008 |
| P1       | GH#134 (Separate Markdown Parsing Layer from Catalog Validation) | Prevents validator complexity from blocking resource evolution                           | ADR-0004 through ADR-0008 |
| P1       | GH#135 (Stable Catalog Resource Identity)                        | `metadata.name` as identity anchor; provenance vs identity separation                    | All ADRs                  |
| P2       | Push rejection diagnostics contract (new issue)                  | Structured error format for admission failures; needed for GraphQL mutation consumers    | ADR-0004 through ADR-0008 |

### Wave 4 — Controller reconciliation substrate

| Priority | Issue / Work item                                                          | What it provides                                                         | ADR dependency                |
|----------|----------------------------------------------------------------------------|--------------------------------------------------------------------------|-------------------------------|
| P1       | GH#131 (Watch API with resourceVersion resume)                             | Controller re-queue on resource change; event cursor                     | ADR-0004 through ADR-0008     |
| P1       | GH#177, GH#178, GH#179 (Status subresource, write boundaries, concurrency) | `StatusPatch` with optimistic concurrency; controller-only status writes | All status conditions in ADRs |
| P1       | GH#176 (Namespace Watch Contract)                                          | Namespace controller reconcile loop                                      | ADR-0002                      |
| P2       | GH#182, GH#183, GH#188 (resume, integration tests, runbook)                | Phase 1 exit quality gate                                                | All ADRs                      |

## GraphQL mutations — first to implement

Implement in this order:

1. **`createNamespace`** — direct datastore write + `gitstore-system` auto-provision.
   No git delegation for bootstrap.
2. **`deleteNamespace`** — foreground finalizer; reject if repos exist.
3. **`createRepository`** — commits `repositories/<name>.md` to `gitstore-system`;
   admission + controller storage provisioning.
4. **`deleteRepository`** — foreground finalizer; reject if catalog resources exist.
5. **`createProduct`** — commits `products/<name>.md` to named repo or
   `gitstore-system`; admission; returns hydrated record.
6. **`createProductVariant`** — same delegation; `spec.productRef` required.
7. **`createCategoryTaxonomy`** — same delegation; cycle check at admission.
8. **`createFile`** — same delegation; binary upload is out-of-band.
9. **`createCollection`** — same delegation; selector evaluated by controller.
10. All `update*` and `delete*` mutations for the above, following the finalizer
    protocol from the respective ADRs.

Mutations 1–4 can be implemented as a unit (Wave 1). Mutations 5–10 depend on
Wave 2 catalog resource contracts.

## Validation/admission rules — required before controller reconciliation

These rules must be live in the admission pipeline before any controller reconciliation
can be trusted:

1. **Namespace active check** — all catalog pushes rejected if namespace is
   `Terminating`.
2. **Repository active check** — all catalog pushes rejected if repository is
   `Terminating` or `Suspended`.
3. **Structural ref validation** — `productRef.name`, `parentRef.name`,
   `fileRef.name` must be non-empty when the ref field is present (push-time, blocking).
4. **Same-namespace enforcement** — all cross-resource references must resolve within
   the same namespace; cross-namespace refs rejected with
   `FailedPrecondition: cross-namespace reference`.
5. **`status` field forbidden** — push rejected if any resource includes a `status`
   key in the frontmatter.
6. **`ownerReferences` field forbidden** — push rejected if any resource includes
   `metadata.ownerReferences` in the frontmatter.
7. **CategoryTaxonomy self-parent rejected** — `parentRef.name == metadata.name`
   rejected at push time.
8. **Product variants must have `spec.productRef`** — missing `productRef` rejected
   at push time.
9. **Collection `spec.targetRef.kind` must be `"Product"`** when present.

Rules 3–9 are admittable as blocking pre-receive or admission checks (see GH#123 for
the chain). Rules 1–2 require the namespace/repository record to be read from the
datastore at admission time.

## Docs to update after implementation

| Document                                    | Update needed                                                                                                                                                                        |
|---------------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `docs/resources/README.md`                  | Add lifecycle states table per resource type; update storage group table to reflect hybrid classification for Namespace/Repository.                                                  |
| `docs/implementation/phases.md`             | Mark Wave 1–4 items complete as they land; update open issue links; add `gitstore-system` convention.                                                                                |
| `docs/resources/git-backed.md`              | Add "Deletion semantics" section citing foreground finalizer protocol; update Namespace/Repository entries to note hybrid classification.                                            |
| `docs/products/product-spec.md`             | Add lifecycle section on finalizer and deletion rejection rule.                                                                                                                      |
| `docs/products/product-variant-spec.md`     | Add variant-first contract enforcement note; add `ProductResolved` condition docs.                                                                                                   |
| `docs/categories/category-taxonomy-spec.md` | Add deletion rejection rules; add cycle detection section; resolve GH#244 deferral note once Phase 2 controller work is done.                                                        |
| `docs/collections/collection-spec.md`       | Add selector-evaluation trigger documentation; add deletion lifecycle section.                                                                                                       |
| `docs/ADRs/`                                | These ADRs (0002–0009) are the new additions; ensure they are listed in any ADR index.                                                                                               |
| `docs/api-reference.md`                     | Add delegation model note: catalog mutations commit to git; document `gitstore-system` as the default repository for namespace-scoped mutations without an explicit repository name. |

## Phase 1 exit criteria (from `docs/implementation/phases.md`)

- [ ] Git push is admission-controlled end-to-end in local/dev environment.
- [ ] All core alpha resources (Namespace, Repository, Product, ProductVariant,
  CategoryTaxonomy, Collection, File) have stable contracts.
- [ ] GraphQL reads remain correct after admitted pushes.
- [ ] Repository authorization is enforced (GH#126).
- [ ] `make pr-ready` gate is green.
- [ ] Foreground finalizers enforce deletion ordering for Namespace → Repository →
  catalog resources.
- [ ] Cross-namespace references are rejected at admission time.
- [ ] `gitstore-system` repository is auto-provisioned on namespace bootstrap.
- [ ] GraphQL mutations for all seven resource kinds delegate to git; no direct
  datastore writes for catalog resources.
