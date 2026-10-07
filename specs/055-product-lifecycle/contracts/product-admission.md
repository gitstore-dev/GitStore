# Contract: Product Admission, Ownership, and Deletion

## Git admission

Product manifests remain accepted through the existing catalog Git admission
pipeline. GraphQL create commits under the canonical Product path in the
namespace's `gitstore-system` repository. GraphQL update resolves persisted
Product provenance and commits to its original repository and source path,
including a non-system Git repository, then awaits that same pipeline.
Admission preserves UID on mutable updates, advances generation/resource
version appropriately, records Git provenance, and rejects author-supplied
system metadata/status.

All Product read/query and admission authorization uses the authenticated
subject and namespace/repository resource context. A Product authored in a
non-system repository remains a valid Git-push path, but no GraphQL repository
selection is exposed.

## Owner-reference rules

| Relation | Writer | `blockOwnerDeletion` | Effect |
|---|---|---:|---|
| ProductVariant → resolved Product | system admission/resolution | true | Blocks Product deletion and final removal. |
| Product → resolved CategoryTaxonomy | existing system resolver | false | Does not block Product deletion; drives existing category reconciliation. |

The blocking lookup is indexed by resolved Product ownership and namespace; it
does not scan unstructured Product-reference names. A ProductVariant newly
created or newly resolved to a Product with a deletion timestamp is rejected.

## Foreground deletion protocol

| Boundary | Required behavior |
|---|---|
| Request | Authenticate/authorize; locate Product without leaking cross-scope existence; check indexed live blockers before any mark/delete mutation. |
| Blocking variants present | Reject with a stable failed-precondition result. Product remains active; no manifest/lifecycle partial state is committed. |
| Eligible request | Canonical Git delete/admission marks `metadata.deletionTimestamp`, adds `gitstore.dev/foreground-deletion`, and exposes derived `Terminating`. |
| Repeated request | Return/continue the existing terminating state; never create a second workflow. |
| Reconcile completion | Refetch with expected resource version, recheck live blockers, retain/requeue on a blocker or conflict, then remove the finalizer and final-delete exactly once. |

Product deletion never cascades ProductVariants. Existing variants may hold a
terminating Product; new or newly resolved variants cannot attach once
termination begins.

## Retirement compatibility

`spec.lifecycle.state: RETIRED` is author intent, not deletion/finalizer
state. It remains private catalog state and makes the Product plus its child
variants ineligible for a newly prepared release. Release preparation rejects
an explicit retired entry rather than silently omitting it. This contract does
not implement release, publication, snapshot, tag, workflow, emergency
suppression, or public-serving behavior.
