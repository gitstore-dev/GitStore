# Data Model: Namespace API Semantics: Spec Writes, Status Updates, Concurrency

## Updated `datastore.Namespace` entity

Extends the entity as it exists today (`gitstore-api/internal/datastore/entities.go`), mirroring the shape `Repository` already gained in spec 045.

| Field | Type | Ownership | Notes |
|---|---|---|---|
| `ID` | `string` | System | Unchanged. |
| `Identifier` | `string` | Author (via Git, non-bootstrap) / System (bootstrap) | Unchanged; maps to `metadata.name`. Immutable after creation. |
| `DisplayName` | `string` | Author (via Git) | Unchanged; maps to `spec.title`. |
| `Tier` | `NamespaceTier` | Author (via Git, create-only) | Unchanged field; now immutable-after-create except for upgrade (demotion rejected — Decision 6 in research.md via admission). |
| `CreatedAt`, `CreatedBy`, `UpdatedAt`, `UpdatedBy` | unchanged | System | Unchanged. |
| `Generation` | `int64` **(new)** | System | Starts at `1` on admission/bootstrap. Advances only on author-controlled spec changes (create/update manifest admitted). |
| `ResourceVersion` | `string` **(new)** | System | Starts at `"1"`. Advances on every successful write (spec admission, status/condition change, deletion-marker set). |
| `Status` | `json.RawMessage` **(new)** | System | `{"observedGeneration":0,"conditions":[]}` initial value, mirroring `repositoryInitialStatus`. Holds `conditions` (`AdmissionAccepted`, `SystemRepoReady`, `Ready`, `Terminating` — see vocabulary below) and `observedGeneration`. |
| `DeletionTimestamp` | `*time.Time` **(new)** | System | Nil for an active namespace; set once an eligible deletion request is accepted. Presence is the read-time signal that a namespace is `Terminating`. |
| `Finalizers` | `[]string` **(new)** | System | Empty for an active namespace; contains `"gitstore.dev/foreground-deletion"` once deletion is accepted, removed by the controller once the drain condition clears. |

`GetNamespace`/`GetNamespaceByIdentifier`/`ListNamespaces` are unaffected in signature; they now return rows carrying these additional fields.

## `NormalizeNamespaceContract` / `AdvanceNamespaceSpecVersion` / `AdvanceNamespaceSystemVersion`

New file `gitstore-api/internal/datastore/namespace_contract.go`, structurally identical to `repository_contract.go`:

- `NamespaceInitialGeneration int64 = 1`, `NamespaceInitialResourceVersion string = "1"`, `namespaceInitialStatus = {"observedGeneration":0,"conditions":[]}`.
- `NormalizeNamespaceContract(ns *Namespace)`: supplies these defaults for namespaces that predate this contract (i.e., the two bootstrap namespaces at their moment of creation, and any pre-existing row).
- `AdvanceNamespaceSpecVersion(ns *Namespace)`: called on admission of a create/update manifest — increments `Generation`, advances `ResourceVersion`.
- `AdvanceNamespaceSystemVersion(ns *Namespace)`: called on status/condition writes and on setting `DeletionTimestamp`/`Finalizers` — advances `ResourceVersion` only, `Generation` unchanged.

## Bootstrap vs. Git-backed namespace

| Property | Bootstrap (`gitstore-system`, `default`) | Git-backed (every other namespace) |
|---|---|---|
| Created by | API startup, direct datastore write | `Namespace` manifest admitted from `gitstore-system/gitstore-system` |
| Updated by | Never (no update path defined for bootstrap namespaces) | New manifest pushed/committed to `gitstore-system/gitstore-system` |
| Deletable | Never (FR-011) | Yes, once empty (spec 041) and not already `Terminating` |
| `createNamespace`/`updateNamespace` mutation behavior | Rejected outright (FR-008) | Commits manifest, waits for admission |

## Admission state machine

```
                 push/commit to gitstore-system/gitstore-system
                                   │
                                   ▼
                    ┌─────────────────────────┐
                    │  pre-receive: kind and   │──reject (wrong repo, bad kind)──▶ push rejected, no record touched
                    │  repository check        │
                    └────────────┬─────────────┘
                                   │ accepted
                                   ▼
                    ┌─────────────────────────┐
                    │  post-receive admission  │──reject (bootstrap name, tier demotion, structural)──▶ push accepted in git, admission error surfaced to mutation caller
                    │  (cataloggrpc "Namespace")│
                    └────────────┬─────────────┘
                                   │ accepted
                                   ▼
              record created/updated; AdmissionAccepted=True; Generation/ResourceVersion advanced
                                   │
                                   ▼ (controller-manager reconciler, async)
              per-namespace gitstore-system repo provisioned; SystemRepoReady=True
                                   │
                                   ▼
                              Ready=True
```

Deletion state machine:

```
deleteNamespace / manifest deletion
        │
        ▼
  HasRepositories?──yes──▶ rejected (spec 041, unchanged); namespace stays active
        │ no
        ▼
  bootstrap namespace?──yes──▶ rejected (FR-011)
        │ no
        ▼
  already Terminating?──yes──▶ treated as redundant (FR-014), no new attempt started
        │ no
        ▼
  DeletionTimestamp set; Finalizers=["gitstore.dev/foreground-deletion"]; ResourceVersion advances
        │
        ▼ (controller-manager reconciler, async)
  HasRepositories still false?──yes──▶ finalizer removed
        │
        ▼
  record hard-deleted
```

## Status condition vocabulary

Extends the vocabulary already documented in `docs/namespace/namespace-spec.md` (`Ready`, `AdmissionAccepted`, `DeletionBlocked`) with the two ADR-0002 conditions this spec introduces:

| Condition | Meaning | Set by |
|---|---|---|
| `AdmissionAccepted` | The namespace's current spec was successfully admitted from a manifest (or bootstrap). | Admission (`cataloggrpc`) |
| `SystemRepoReady` | The namespace's own per-namespace `gitstore-system` repository exists and is ready. | Controller reconciler |
| `Ready` | The namespace is fully operational (`AdmissionAccepted=True` and `SystemRepoReady=True`). | Controller reconciler |
| `Terminating` | `DeletionTimestamp` is set and the `foreground-deletion` finalizer is present. | Read-time derivation from `DeletionTimestamp`/`Finalizers` (not a separately-stored condition — see below) |

`Terminating` is exposed as a condition for read convenience, but its source of truth is `DeletionTimestamp`/`Finalizers`, not an independent flag — this avoids the two ever disagreeing.

## GraphQL surface (unchanged shape, changed behavior)

No change to `CreateNamespaceInput`, `UpdateNamespaceInput` (as scoped by this spec's mutation-delegation model), `DeleteNamespaceInput`, or their payload types. `NamespaceStatus.conditions` now returns real, persisted values instead of always `[]`. `metadata.resourceVersion`/`metadata.generation` now return real, persisted values instead of always `"1"`/`1`.

## Relationship to specs 041, 044, 045, 047

- **Spec 041** (`HasRepositories`): reused unchanged as the sole finalizer-drain condition.
- **Spec 044** (declarative schema): unchanged GraphQL shape; this spec makes its previously-fabricated `resourceVersion`/`generation`/`conditions` values real.
- **Spec 045** (`Repository` contract + `UpdateRepository`): the `Generation`/`ResourceVersion`/`Status` addition pattern and the optimistic-concurrency datastore method shape are copied verbatim for `Namespace`.
- **Spec 047** (validation/admission matrix): owns the full structural-vs-policy validation rule catalogue; this spec owns only the lifecycle behavior and the specific rules (repository restriction, tier-demotion rejection) called out directly in ADR-0002's phase table.
