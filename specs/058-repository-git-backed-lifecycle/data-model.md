# Data Model: Repository Git-Backed Lifecycle, Admission, and Reconciler

## Unchanged `datastore.Repository` entity

No new fields. Extends the entity exactly as it exists today (`gitstore-api/internal/datastore/entities.go`, added by spec 045):

| Field | Type | Ownership | Notes |
|---|---|---|---|
| `UID` | `string` | System | Unchanged. |
| `Namespace` | `string` | Author (via Git, create-only) | Maps to `metadata.namespace`. Immutable after creation — a manifest attempting to change it is rejected at admission (there is no `transferRepository` path once this spec ships; see Decision 8, research.md). |
| `Name` | `string` | Author (via Git, create-only) | Maps to `metadata.name`. Immutable after creation — a manifest attempting to change it is rejected at admission (there is no `renameRepository` path once this spec ships). |
| `Generation` | `int64` | System | Already exists (spec 045). Starts at `1` on admission. Advances only on author-controlled spec changes (create/update manifest admitted). |
| `ResourceVersion` | `string` | System | Already exists (spec 045). Starts at `"1"`. Advances on every successful write (spec admission, status/condition change, deletion-marker set). |
| `Revision` | `string` | System | Already exists (spec 045). Git revision the current spec was admitted from. |
| `CreationTimestamp`, `CreationActor`, `UpdateTimestamp`, `UpdateActor` | system audit fields | System | Unchanged. |
| `Labels`, `Annotations`, `OwnerReferences` | manifest metadata | Author (via Git) | Unchanged shape; already round-tripped through admission for other kinds. |
| `Finalizers` | `[]string` | System | Already exists (spec 045), currently always empty in practice. Gains real meaning here: contains `"gitstore.dev/foreground-deletion"` once deletion is accepted, removed by the controller once both drain conditions (§ Admission and deletion state machines) clear. |
| `DeletionTimestamp` | `*time.Time` | System | Already exists (spec 045), currently always nil in practice. Gains real meaning here: nil for an active repository, set once an eligible deletion request is accepted. |
| `RepositoryID`, `SourcePath`, `GitCommitSHA`, `GitRef` | system/admission bookkeeping | System | Unchanged; `SourcePath`/`GitCommitSHA`/`GitRef` are populated from the admitted manifest's push, exactly as for every other git-backed kind. |
| `Spec`, `Body` | manifest content | Author (via Git) | Unchanged shape (`Spec` holds the marshaled `RepositorySpec`-equivalent manifest spec; `Body` holds the Markdown description). |
| `Status` | `json.RawMessage` | System | Already exists (spec 045), currently always the deterministic placeholder `{"observedGeneration":0,"conditions":[]}`. Gains real meaning here: holds the real `conditions` (`AdmissionAccepted`, `StorageProvisioned`, `Ready`, `Terminating`) and `observedGeneration`. |
| `DefaultBranch`, `StorageClass` | manifest content | Author (via Git, `StorageClass` upgrade-only) | `DefaultBranch` fully mutable; `StorageClass` mutable only as an upgrade, never a downgrade (ADR-0003 §"Update"). |
| `MaxPackSizeBytes`, `MaxFileSizeBytes` | push policy limits | Unchanged | Not addressed by this spec; remain whatever spec 035's push-policy resolution already sets. |

`GetRepository`/`LookupRepository`/`ListRepositoriesByNamespace` return rows carrying these fields unchanged in shape; only their *values* for `Status`/`Finalizers`/`DeletionTimestamp` become meaningful once this spec ships.

## `NormalizeRepositoryContract` / `AdvanceRepositorySpecVersion` / `AdvanceRepositorySystemVersion`

Already implemented in `gitstore-api/internal/datastore/repository_contract.go` (spec 045). Reused verbatim by the new `admitRepository` admission case:

- `NormalizeRepositoryContract(repository *Repository)`: supplies contract defaults for rows that predate real lifecycle semantics.
- `AdvanceRepositorySpecVersion(repository *Repository)`: called on admission of a create/update manifest — increments `Generation`, advances `ResourceVersion`.
- `AdvanceRepositorySystemVersion(repository *Repository)`: called on status/condition writes and on setting `DeletionTimestamp`/`Finalizers` — advances `ResourceVersion` only, `Generation` unchanged.

No changes to this file are required.

## Bootstrap vs. Git-backed repository

| Property | Bootstrap (`gitstore-system`, one per namespace) | Git-backed (every other repository in that namespace) |
|---|---|---|
| Created by | Namespace creation, direct datastore write (spec 041, unchanged by this spec) | `Repository` manifest admitted from `<namespace>/gitstore-system` at `repositories/<name>.md` |
| Updated by | Never (no update path defined for the bootstrap repository) | New manifest pushed/committed to `<namespace>/gitstore-system` |
| Deletable | Never while its namespace exists (FR-012) | Yes, once empty of catalog resources (spec 041) and its bare Git repository is confirmed removable, and not already `Terminating` |
| `createRepository`/`updateRepository` mutation behavior | Rejected outright (FR-008) | Commits manifest, waits for admission |
| `renameRepository`/`transferRepository` mutation behavior | `Unimplemented` (unconditional, same as every other repository) | `Unimplemented` (unconditional, FR-010) |

## Admission state machine

```
                 push/commit to <namespace>/gitstore-system
                                   │
                                   ▼
                    ┌─────────────────────────┐
                    │  pre-receive: kind and   │──reject (wrong repo, bad kind)──▶ push rejected, no record touched
                    │  per-namespace repo check│
                    └────────────┬─────────────┘
                                   │ accepted
                                   ▼
                    ┌─────────────────────────┐
                    │  post-receive admission  │──reject (bootstrap name, immutable-field change,
                    │  (cataloggrpc "Repository")│    storageClass downgrade, namespace Terminating/missing,
                    └────────────┬─────────────┘    structural)──▶ push accepted in git, admission
                                   │ accepted            error surfaced to mutation caller
                                   ▼
              record created/updated; AdmissionAccepted=True; Generation/ResourceVersion advanced
                                   │
                                   ▼ (controller-manager reconciler, async)
              bare Git repository provisioned on git-service filesystem; StorageProvisioned=True
                                   │
                                   ▼
                              Ready=True
```

Deletion state machine:

```
deleteRepository / manifest deletion
        │
        ▼
  HasCatalogResources?──yes──▶ rejected (spec 041, unchanged); repository stays active
        │ no
        ▼
  bootstrap repository?──yes──▶ rejected (FR-012)
        │ no
        ▼
  already Terminating?──yes──▶ treated as redundant (FR-015), no new attempt started
        │ no
        ▼
  DeletionTimestamp set; Finalizers=["gitstore.dev/foreground-deletion"]; ResourceVersion advances
        │
        ▼ (controller-manager reconciler, async)
  HasCatalogResources still false AND bare Git repository confirmed removed?──yes──▶ finalizer removed
        │                                                                     (retries with backoff otherwise —
        │                                                                      ADR-0003 "git-service unreachable")
        ▼
  record hard-deleted
```

## Status condition vocabulary

Extends the vocabulary documented in `docs/repository/repository-spec.md` (currently empty — "no Repository controller, reconciler, status mutation, or condition-producing writer") with the four conditions ADR-0003 already names:

| Condition | Meaning | Set by |
|---|---|---|
| `AdmissionAccepted` | The repository's current spec was successfully admitted from a manifest (or is the bootstrap repository). | Admission (`cataloggrpc`) |
| `StorageProvisioned` | The repository's bare Git repository exists on the git-service filesystem. | Controller reconciler |
| `Ready` | The repository is fully operational (`AdmissionAccepted=True` and `StorageProvisioned=True`). | Controller reconciler |
| `Terminating` | `DeletionTimestamp` is set and the `foreground-deletion` finalizer is present. | Read-time derivation from `DeletionTimestamp`/`Finalizers` (not a separately-stored condition — see below) |

`Terminating` is exposed as a condition for read convenience, but its source of truth is `DeletionTimestamp`/`Finalizers`, not an independent flag — this avoids the two ever disagreeing, exactly as spec 046 established for Namespace. `Suspended` (ADR-0003's operator-set push-rejection condition) is explicitly out of scope for this spec (see spec.md Assumptions) and is not part of this vocabulary yet.

## Immutable vs. mutable field matrix (update / manifest re-push)

| Field | Mutability | Rejection behavior if violated |
|---|---|---|
| `metadata.name` | Immutable after creation | Admission rejects the manifest; existing record unchanged |
| `metadata.namespace` | Immutable after creation | Admission rejects the manifest; existing record unchanged |
| `spec.defaultBranch` | Mutable | N/A |
| `spec.visibility` | Mutable | N/A |
| `spec.storageClass` | Mutable, upgrade only (never downgrade) | Admission rejects a downgrade attempt; existing record unchanged |
| `status` (any submitted block) | Not author-writable | Silently ignored, never applied |

## GraphQL surface (resource-envelope shape, changed/added behavior)

`createRepository` moves to the same declarative envelope shape Namespace's mutations already use (spec 046); `updateRepository` is added new, using the identical shape:

```graphql
input CreateRepositoryInput {
  apiVersion: String!
  kind: String!
  metadata: RepositoryMetadataInput!
  spec: RepositorySpecInput!
}

input UpdateRepositoryInput {
  apiVersion: String!
  kind: String!
  metadata: RepositoryMetadataInput!
  spec: RepositorySpecInput!
}

type UpdateRepositoryPayload {
  repository: Repository!
}
```

The required fields are:
- `apiVersion`: resource contract version
- `kind`: must be `Repository`
- `metadata.name`: repository identifier; bootstrap name `gitstore-system` is rejected
- `metadata.namespace`: owning namespace identifier; immutable after creation
- `spec`: `defaultBranch`, `visibility`, `storageClass` (upgrade-only)

`RenameRepositoryInput`/`RenameRepositoryPayload` and `TransferRepositoryInput`/`TransferRepositoryPayload` are unchanged in shape; `DeleteRepositoryInput`/`DeleteRepositoryPayload` are unchanged. `RepositoryStatus.conditions` now returns real, persisted values instead of always `[]`. `metadata.resourceVersion`/`metadata.generation` now return real, persisted values (they already did, per spec 045 — this spec makes the *conditions* real, not the counters, which were already real).

### Status condition matrix

| Condition | System-owned source | Mutation/manifest input effect |
|---|---|---|
| `AdmissionAccepted` | admitted manifest outcome | ignored if author attempts to set it in Git or GraphQL input |
| `StorageProvisioned` | controller reconciliation | ignored if author attempts to set it in Git or GraphQL input |
| `Ready` | controller reconciliation | ignored if author attempts to set it in Git or GraphQL input |
| `Terminating` | deletion marker + finalizer state | derived from system deletion flow; never writable by callers |

## Relationship to specs 041, 045, 046, 048, and future 047/GH#174 analogs

- **Spec 041** (`HasCatalogResources`): reused unchanged as the catalog-resource half of the finalizer-drain condition.
- **Spec 045** (`Repository` contract): the `Generation`/`ResourceVersion`/`Status`/`Finalizers`/`DeletionTimestamp` fields and their contract-helper functions are reused verbatim; this spec is the first to give them real values.
- **Spec 046** (Namespace API Semantics): the admission-dispatch-case pattern, the finalizer/`Terminating` state machine shape, the controller-manager reconciler pattern, and the declarative mutation envelope shape are all copied for Repository, one tier down the ownership chain.
- **Spec 048** (Scylla query design): Repository's query-first Scylla projections and `UpdateRepository`'s `IF resource_version=?` LWT are reused unchanged; no new access pattern is introduced.
- **Future "Repository Validation and Admission Matrix"** (mirrors spec 047): owns the full structural-vs-policy validation rule catalogue beyond what this spec's admission state machine already specifies.
- **Future "Repository Watch Contract"** (mirrors GH#174's `watchNamespaces`): owns watch/subscription semantics for Repository; out of scope here.
