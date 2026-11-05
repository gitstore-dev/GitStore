# Upgrade Compatibility

Review release notes and the compatibility changes relevant to your starting
version before upgrading. These are planned-change procedures, not recurring
maintenance. `CHANGELOG.md` is generated from commit messages; the notes below
explain changes that require policy, client, or deployment action.

Retain persistent storage and a recovery path. Exactly one Git service may be
active; replace it without overlap. Do not assume that arbitrary old/new API
or controller combinations are compatible.

## Category lifecycle rollout order

When upgrading from controllers without `completeCategoryDeletion`, deploy the
supporting API first. Grant `categoryTaxonomy.purge` to the controller role
before upgrading controllers, otherwise categories remain terminating. The
reference `config/policy.yaml` and `gitstore-api/policy.yaml.example` already
grant it.

Custom policies must also use `categoryTaxonomy.delete` instead of
`category.delete`, and grant `categoryTaxonomy.create` and
`categoryTaxonomy.update` where those mutations are used. See
[catalog lifecycle](catalog-lifecycle.md) for deletion and rollback safeguards.

## Client and authorization compatibility

- Category reads require namespace-scoped `categoryTaxonomy.list` for
  `categories` and `categoryTaxonomy.read` for `category(by:)`. Grant these to
  callers that read categories, including storefront, anonymous, and controller
  identities as appropriate. Reference policies already include them.
- `Category.path` and `Category.depth` are replaced by `status.resolved.path`
  and `status.resolved.depth`. They are `null` until first reconciliation and
  eventually consistent after an ancestor move.
- `DeleteCategoryPayload.deletedCategoryId` and `orphanedProductIds` are
  replaced by `{ category, outcome }`, with outcomes `TERMINATION_STARTED`
  and `ALREADY_TERMINATING`. Deletion removes the Git manifest.
- `createCategory`, `updateCategory`, and `completeCategoryDeletion` require
  `categoryTaxonomy.create`, `categoryTaxonomy.update`, and
  `categoryTaxonomy.purge`, respectively. `categories` accepts a `filter`.
- `UpdateCategoryStatusInput.completeDeletion` is removed; controllers use
  `completeCategoryDeletion`.
- Namespace mutation errors use the shared envelope: `NAMESPACE_*` codes
  become `ADMISSION_REJECTED`, `ALREADY_EXISTS`, `NOT_FOUND`, `CONFLICT`, or
  `FAILED_PRECONDITION`. Match `diagnostics[].reason` instead of `phase`,
  `reason`, or `reasons`; reason values are unchanged. See
  [Namespace admission](namespace-admission.md#migrating-from-the-earlier-codes).
- Status and `complete*Deletion` resource-version conflicts use code
  `CONFLICT` with diagnostic reason `RESOURCE_VERSION_CONFLICT`. The current
  version is in the message, not a `resourceVersion` extension. Controllers
  accept both error forms; that particular change permits either rollout
  order, but does not override other API-first requirements.
- Git pushes reject cross-namespace Category parent references and changes to
  `metadata.name` or `metadata.namespace` at the same file path.

## Monitoring compatibility

Update dashboards and alerts that match the old label sets:

- `gitstore_namespace_validation_rejections_total` uses `{code,reason}`,
  previously `{phase,reason}`.
- `gitstore_namespace_validation_duration_seconds` uses `{stage}`, previously
  `{phase}`; values are `STRUCTURAL` and `POLICY`.
- Additional signals are `gitstore_admission_rejections_total{kind,phase}`,
  `gitstore_category_ancestor_index_writes_total{result}`, and
  `gitstore_category_ancestor_index_repair_required_total`.

## Scylla schemas and projections

Schema migrations apply on API startup unless
`GITSTORE_API__DATASTORE__SCYLLA__AUTO_MIGRATE=false`; with automatic migration
disabled, run `gitctl migrate` first. In-memory deployments need no migration
step. Validate compatibility with the deployed schema before mixing versions;
the [Product and Namespace schema baseline](../architecture/datastore-resource-storage.md#schema-compatibility)
does not support writers built for the earlier layout.

When upgrading from writers without the category subtree index, wait until all
API replicas use the supporting version, then audit projections. From
`gitstore-api/`:

```bash
go run ./cmd/gitctl scylla-projection-audit > projection-audit.json
go run ./cmd/gitctl scylla-projection-repair --dry-run > projection-repair-plan.json
```

Repair only reviewed findings, following the
[projection repair safeguards](scylla-projection-repair.md#category-ancestor-index):

```bash
go run ./cmd/gitctl scylla-projection-repair --confirm
go run ./cmd/gitctl scylla-projection-audit   # expect zero findings
```

An audit without findings does not require repair. Schema validation and
projection repair do not authorize resetting or reseeding persistent data.
