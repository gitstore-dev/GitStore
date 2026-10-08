# ADR Status Lifecycle

GitStore's architecture decision records use four statuses. The `**Status**`
line at the top of each ADR is the only field this convention governs — ADR
bodies are not rewritten when a status changes.

- **Proposed** — under discussion; not yet a binding decision. Code may or
  may not match a Proposed ADR; neither state is evidence either way.
- **Accepted (YYYY-MM-DD)** — decided; the codebase is expected to follow it.
  An ADR flips from Proposed to Accepted in the same PR that merges the first
  change implementing its decision — the same discipline specs use for their
  Status → Closed roll-up. A `/speckit.plan`/`/speckit.tasks` plan that
  implements an ADR's decision must include the Status flip as an explicit
  task.

  **Dating rule.** The acceptance date can never precede the ADR's own
  `**Date**` header — a decision cannot be accepted before it was written.
  Determine it as:
  1. The merge date of the first PR, merged **at or after** the ADR's own
     `**Date**`, that brings the ADR's decision into force on `main`. This is
     the normal case — most ADRs are written before the code that implements
     them.
  2. If the ADR instead merely **codifies behaviour that already shipped
     before the ADR was written** (no implementing PR exists at or after the
     ADR's `**Date**`, only earlier ones), use the date the ADR file itself
     was first merged (`git log --follow --diff-filter=A` on the ADR's own
     path under `docs/ADRs/`), not the earlier implementation PR's date.
  3. If neither date can be determined, use the date of the flip itself,
     called out as such in this index.
- **Superseded by ADR-NNNN** / **Deprecated** — replaced or retired. The file
  is kept; it is not deleted.
- **Amendment (...)** blocks — a dated amendment to an Accepted ADR keeps it
  Accepted. A known unimplemented follow-up noted in an amendment (for
  example "deferred to Phase 2") does not block acceptance as long as the
  ADR's core decision is in force; list such items in this index rather than
  editing the ADR body.

Accept an ADR only when its **core decision** — not every detail or Phase 2
item in it — is implemented and in force in the code on `main`. A superseding
ADR updates both the superseded ADR's status line and this index; see
`AGENTS.md` for the authoring rule.

## Index

| ADR | Title | Status | Notes / open items |
|-----|-------|--------|---------------------|
| [0001](0001-secretref-reference-contract.md) | SecretRef Reference Contract | Accepted (2026-10-04) | `shared/secretmaterial` implements the `SecretRef` shape and `SecretResolver` contract (#439). |
| [0002](0002-namespace-lifecycle.md) | Namespace Lifecycle | Accepted (2026-08-20) | Hybrid bootstrap + Git-backed model implemented (#361). |
| [0003](0003-repository-lifecycle.md) | Repository Lifecycle | Accepted (2026-09-16) | Hybrid bootstrap + Git-backed model and durable watch implemented (#394). |
| [0004](0004-product-lifecycle.md) | Product Lifecycle | Accepted (2026-09-18) | Git-backed admission, reference resolution and deletion guard implemented (#380). |
| [0005](0005-product-variant-lifecycle.md) | ProductVariant Lifecycle | Accepted (2026-06-26) | Variant-first contract, co-creation semantics and async `ProductResolved`/`OptionsAccepted` resolution shipped before the ADR was written (#251, 2026-06-10; #309, 2026-06-18) — dated to the ADR's own merge date per dating-rule case 2. Open item: the variant-side decoupling cascade when the parent Product enters `Terminating` (`ProductResolved=False`/reason `ProductTerminating`) was not found in code. |
| [0006](0006-category-taxonomy-lifecycle.md) | CategoryTaxonomy Lifecycle | Proposed | Deliberately kept Proposed: flips to Accepted in the PR implementing spec 057 (Git-backed mutations and the descendant/`ancestorPath` index), not before. |
| [0007](0007-collection-lifecycle.md) | Collection Lifecycle | Proposed | The Git-backed resource contract and the `collection.products` label-selector query path exist (#247), but the ADR's core "controller-managed `memberCount`/`MembersResolved`" materialization is not in force: no `MembersResolved` condition exists anywhere in the codebase, no Collection reconciler exists in `gitstore-controller-manager`, and `status.resolved.memberCount` is deserialized on read but never written by anything. `collection.products` also re-evaluates the selector live on every page rather than the ADR's described first-page snapshot. |
| [0008](0008-file-lifecycle.md) | File Lifecycle | Proposed | The manifest/pointer resource contract exists (#372), but the ADR's Phase 1 deletion decision is not in force. A pushed File deletion hard-deletes the record with no reference check, finalizer or `Terminating` state, and there is no `deleteFile` mutation. Reference-checked deletion is spec 056 (draft #381); the ADR flips to Accepted in that PR. |
| [0009](0009-credential-secret-boundary.md) | Credential and Secret Material Boundary | Accepted (2026-10-04) | `CredentialsRef`, and the bootstrap/runtime resolver tier split, implemented alongside ADR 0001 (#439). |
| [0010](0010-authorization-model.md) | Authorization Model | Proposed | Only §14 (ownership assignment/transfer) has a partial implementation (`internal/middleware/security/ownership.go`), carrying an explicit `TODO(ADR-0010 §14, follow-up vocabulary spec)` noting `Authorize` still doesn't consume `ResourceContext.OwnerSub`. The core canonical-vocabulary decision (closed verb set, `purge`, single `categoryTaxonomy` slug, `.own`/`.any` removal) is not live: `config/policy.yaml` still mixes `category.*`/`.any` actions with the new grammar, and no `purge` verb exists in the codebase. |
| [0011](0011-graphql-authorization-directive.md) | Declarative GraphQL Authorization via `@authorize` | Proposed | No `@authorize` directive exists in any `shared/schemas/*.graphqls` file. |
| [0012](0012-admin-storefront-graphql-endpoints.md) | Separate Admin and Storefront GraphQL Endpoints | Proposed | Only one `shared/schemas/` tree and one `/graphql` endpoint exist; no `admin/`/`storefront`/`common/` split. |
| [0013](0013-markdown-body-intermediate-representation.md) | Markdown Body Contract and Intermediate Representation | Proposed | Future design; body remains an opaque string today. |
| [0014](0014-catalog-release-and-publication.md) | Catalog Release and Publication | Proposed | Future design; no `CatalogRelease`/`Publication` resources exist. |
| [0015](0015-resource-lifecycle-hooks.md) | Resource Lifecycle Hooks | Proposed | Future design; no hook registry, `AdmissionReport`, or Function host exists. |
| [0016](0016-custom-resource-definitions.md) | Custom Resource Definitions | Proposed | Future design; no `CustomResourceDefinition` kind exists. |
| [0017](0017-aggregate-fields-via-async-materialization.md) | Aggregate Fields via Async Controller Materialization | Accepted (2026-10-03) | Reference implementation (`CategoryTaxonomy.status.resolved.productCount`, #426) predates the ADR's own `**Date**` (2026-10-03) — the ADR codifies an already-shipped pattern, so it's dated to the ADR file's own merge date per dating-rule case 2. The rule is already enforced as a standing `AGENTS.md` development guideline. |
| [0018](0018-controller-ownership-concurrency-and-fencing.md) | Controller Ownership, Concurrency and Fencing | Proposed | The ADR's own Context section states it "does not implement those capabilities or controller fencing" today. |
