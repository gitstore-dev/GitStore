# Data Model: CategoryTaxonomy Path Freshness, Git-Backed Mutations, and Descendant Filtering

## CategoryTaxonomy (existing; `datastore.CategoryTaxonomy`, `entities.go:231-274`)

No new columns.

| Field                                  | Role in this feature                                                                                                                                                  |
|----------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `RepositoryID`, `SourcePath`, `GitRef` | Stored provenance. `updateCategory`/`deleteCategory` write here. If either `RepositoryID` or `SourcePath` is empty, the mutation fails with "provenance unavailable". |
| `GitCommitSHA`                         | Own-commit verification (FR-017). Must equal the admitted commit, unless admission reports NoOp.                                                                      |
| `DeletionTimestamp`, `Finalizers`      | Existing foreground-deletion lifecycle. Set by admission of a Git removal.                                                                                            |
| `AncestorPath`                         | Admission-internal only (cycle checks). Never read by GraphQL.                                                                                                        |
| `Status.resolved.path` / `.depth`      | Only client-visible hierarchy. `null` until the first reconcile. Source of the ancestor index.                                                                        |
| `Annotations[owner]`                   | Immutable across updates (`guardOwnerAnnotationUnchanged`).                                                                                                           |

**Validation (pre-receive, FR-016)**:
- valid envelope, with `kind: CategoryTaxonomy` and `apiVersion: catalog.gitstore.dev/v1beta1`;
- `metadata.name` is a DNS label of 63 characters or fewer;
- `spec.title` is 1–200 characters;
- `spec.parentRef.name` is not equal to `metadata.name`;
- the parent is not `Terminating`;
- `parentRef` does not point to another namespace;
- `media[*].fileRef.name` is present;
- on update, `metadata.name`/`metadata.namespace` are unchanged and the category is not `Terminating`.

**State transitions (unchanged lifecycle; new entry points)**:

```text
(absent) --createCategory/push--> Admitted(AdmissionAccepted=True)
Admitted --updateCategory/push (accepted)--> Admitted(gen+1)
Admitted --updateCategory/push (denied post-receive)--> Admitted(last accepted gen, AdmissionAccepted=False/AdmissionReportFailed)
Admitted --deleteCategory/push removal [no blocking children]--> Terminating --products decoupled, controller completes--> (removed)
Terminating --deleteCategory--> Terminating (ALREADY_TERMINATING, no commit)
```

## CategoryAncestorIndex (new projection)

This is a derived, rebuildable projection and is never authoritative.

| Field              | Type          | Notes                                                                                                |
|--------------------|---------------|------------------------------------------------------------------------------------------------------|
| `namespace`        | text          | partition key (part 1)                                                                               |
| `ancestor`         | text          | partition key (part 2). The ancestor's name. Includes the category itself, at depth 0.               |
| `depth`            | tinyint 0–128 | clustering, ascending. `index(self) − index(ancestor)` in `status.resolved.path`.                    |
| `descendant`       | text          | clustering, ascending. The category name.                                                            |
| `descendant_uid`   | uuid          | Used to fetch the record and to detect stale or recreated rows.                                      |
| `resource_version` | text          | The category resource version that produced the row. On concurrent upserts, the higher version wins. |

**Derivation**: for `status.resolved.path = [a0, …, ak]` (`ak` = self), the category owns exactly the rows `{(ns, ai, k−i, ak) | 0 ≤ i ≤ k}`. A category with `status.resolved = null` owns no rows.

**Invariants**:
- Every category with non-null `status.resolved` has exactly k+1 rows matching its current path, once repair or the next status write completes.
- No row exists for a removed category.
- A partition `(ns, X)` contains only X's current subtree, plus transient rows during a cascade.

**Write rules**:

| Event                                              | Index change                                                                                                           |
|----------------------------------------------------|------------------------------------------------------------------------------------------------------------------------|
| Status write with `Resolved` set                   | Upsert all current rows. If the path changed, delete the old rows whose `(ancestor, depth)` pair is no longer present. |
| Status write without `Resolved`                    | None                                                                                                                   |
| Admission create / update                          | None                                                                                                                   |
| `CompleteCategoryTaxonomyDeletion` (final removal) | Delete all of the category's rows                                                                                      |
| `gitctl scylla-projection-repair --confirm`        | Insert missing rows, delete dangling ones, correct stale ones                                                          |

**memdb**: table `category_ancestor_index`, with these indexes:
- `id` (unique `ns/ancestor/depth/descendant`);
- `ancestor` (compound `Namespace, Ancestor`, iterated and sorted by `(Depth, Descendant)`);
- `descendant` (compound `Namespace, Descendant`, for rewrite and delete).

Rows are written in the same transaction as the status or delete.

## Admission Diagnostic (new shared type, `internal/admission`)

| Field                                                       | Type                           | Notes                                             |
|-------------------------------------------------------------|--------------------------------|---------------------------------------------------|
| `reason` | string | Required machine-readable SCREAMING_SNAKE value, e.g. `SELF_PARENT` |
| `file`                                                      | string                         | Manifest path, e.g. `categories/laptops.md`       |
| `field`                                                     | string?                        | Field path, e.g. `spec.title`                     |
| `level`                                                     | `FAILURE \| WARNING \| NOTICE` | A subset of the AdmissionReport annotation levels |
| `message`                                                   | string                         | Same text as the corresponding push rejection     |
| `startLine`, `startColumn`, `endLine`, `endColumn`, `title` | optional, reserved             | May be added later without breaking clients       |

`admission.Error{Code, Phase?, CommitSHA?, Diagnostics}` → GraphQL error with the four-key envelope (see `contracts/admission-diagnostics.md`).

## Category GraphQL (changed)

- Removed: `Category.path`, `Category.depth`.
- Now resolved: `Category.parent`, `Category.children`.
- New inputs and payloads: `CreateCategoryInput`, `UpdateCategoryInput`, `CategorySpecInput`, `CategoryFilterInput`, `CreateCategoryPayload`, `UpdateCategoryPayload`.
- Changed payload: `DeleteCategoryPayload` → `{ category, outcome }`.

See `contracts/graphql.md`.
