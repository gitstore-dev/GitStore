# Contract: Datastore ancestor index and internal admission interfaces

## Datastore (`gitstore-api/internal/datastore`)

Additive optional interface, type-asserted like `OwnerReferenceStore`. It must be forwarded in `instrumented.go` (`:580-657` pattern).

```go
const MaxCategoryHierarchyDepth = 128

type CategoryDescendant struct {
    Name  string
    UID   string
    Depth int // relative to the ancestor; 0 = the ancestor itself
}

type CategoryDescendantQuery struct {
    Namespace   string
    Ancestor    string // category name
    IncludeSelf bool
    MaxDepth    int // 0 = unbounded (≤ MaxCategoryHierarchyDepth)
    Page        PageParams // cursor encodes (depth, name); see below
}

type CategoryAncestorIndex interface {
    ListCategoryDescendants(ctx context.Context, q CategoryDescendantQuery) (*PageResult[CategoryDescendant], error)
}
```

Maintenance is internal to the existing methods. No new write method is exposed:
- `UpdateCategoryTaxonomyStatus`: when the patch has `Resolved != nil`, upsert the current rows and delete the stale ones. memdb does this in the same transaction. Scylla does it through `executeUpdate`; if projection writes still fail after retry, it returns `RepairRequiredError`.
- `CompleteCategoryTaxonomyDeletion` (and `DeleteCategoryTaxonomy`): delete the category's rows. Scylla does this projections-first through `executeDelete`.

**Cursor**: `base64("closure|<depth>|<name>")`. A keyset cursor (`keyset|…`) supplied to `ListCategoryDescendants`, or a closure cursor supplied to `ListCategoryTaxonomies`, returns `ErrInvalidArgument`. It is never silently ignored.

**Scylla migration** `migrations/010_category_ancestor_index.cql`:

```cql
CREATE TABLE IF NOT EXISTS category_ancestor_index (
  namespace text,
  ancestor text,
  depth tinyint,
  descendant text,
  descendant_uid uuid,
  resource_version text,
  PRIMARY KEY ((namespace, ancestor), depth, descendant)
) WITH CLUSTERING ORDER BY (depth ASC, descendant ASC)
  AND gc_grace_seconds = 864000;
```

There is no CDC. Update `migration_schema_test.go` and the table list in `migration_test.go`.

**Query shape**: `SELECT depth, descendant, descendant_uid FROM category_ancestor_index WHERE namespace=? AND ancestor=? AND depth >= ? AND depth <= ? [AND (depth, descendant) > (?, ?)] LIMIT n+1`. The lower depth bound is 0 with `includeSelf`, otherwise 1.

**Repair** (`scylla/repair.go`): add projection `category_ancestor_index` to `knownProjectionTable`. `Snapshot` reads `namespace, name, uid, resource_version, status` from `category_taxonomies_by_namespace` (paged) and scans the index. `expectedProjections` derives the rows from `status.resolved.path`. The plan emits insert, delete and update actions, applied through the existing conditional writers and `gitctl scylla-projection-audit | scylla-projection-repair --dry-run|--confirm`.

**Contract tests**: a new `t.Run("CategoryAncestorIndex", …)` block in `RunContractSuite` and `RunPaginationSuite` (`tests/contract/datastore/`), run against memdb and Scylla. It covers:
- segment matching (`computers` vs `computers-refurb`);
- `includeSelf`;
- `maxDepth`;
- re-parent (old rows removed);
- final removal;
- `status.resolved = null` → no rows;
- the cursor round trip and mode mismatch;
- idempotent repeated status writes;
- concurrent writes for the same category (the higher resource version wins).

## Admission interfaces (`gitstore-api/internal/admission`)

```go
type Phase string // "PRE_RECEIVE" | "POST_RECEIVE"
type DiagnosticLevel string // "FAILURE" | "WARNING" | "NOTICE"

type Diagnostic struct {
    Reason      string // required, SCREAMING_SNAKE
    Message     string
    Level       DiagnosticLevel
    File, Field string // optional
}

type Code string // ADMISSION_REJECTED | ALREADY_EXISTS | NOT_FOUND | CONFLICT | FAILED_PRECONDITION | BAD_USER_INPUT | FORBIDDEN

// Error is the kind-neutral mutation error. Phase only with ADMISSION_REJECTED; CommitSHA only with POST_RECEIVE.
type Error struct {
    Code        Code
    Phase       Phase
    CommitSHA   string
    Diagnostics []Diagnostic
}
func (e *Error) Error() string                // FormatRejection(e.Diagnostics) for ADMISSION_REJECTED
func (e *Error) ToGQLError() *gqlerror.Error  // four-key envelope; omits absent keys
func FormatRejection([]Diagnostic) string // "file: message; …" — parity with Rust validation_handler.rs

type ManifestValidationRequest struct {
    RepositoryID, Path string
    OldContent, NewContent []byte // OldContent nil on create; NewContent nil on delete
}

// Implemented by cataloggrpc.Server; wraps the pre-receive checks for one manifest.
type ManifestValidator interface {
    ValidateManifest(ctx context.Context, req ManifestValidationRequest) ([]Diagnostic, error)
}

// CommittedManifestResult gains:
//   NoOp     bool          // admission found no spec/body change
//   Warnings []Diagnostic  // non-fatal post-receive entries
// AdmitCommittedManifest returns *Error{Code: ADMISSION_REJECTED, Phase: POST_RECEIVE} on denial/failure.
// AdmitCommittedManifest supports Operation=Delete for Kind=CategoryTaxonomy.
```
