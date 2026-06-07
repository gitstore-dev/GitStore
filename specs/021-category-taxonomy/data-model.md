# Data Model: CategoryTaxonomy Frontmatter and Hierarchy Enforcement

**Date**: 2026-06-06 | **Feature**: 021-category-taxonomy

---

## 1. Go Catalog Types (`gitstore-api/internal/catalog/`)

### `CategoryTaxonomyResource` (new file: `category.go`)

Author-writable envelope parsed from a `CategoryTaxonomy` Markdown file's YAML frontmatter.

```go
type CategoryTaxonomyResource struct {
    APIVersion string               `yaml:"apiVersion" validate:"required,eq=catalog.gitstore.dev/v1beta1"`
    Kind       string               `yaml:"kind"       validate:"required,eq=CategoryTaxonomy"`
    Metadata   ObjectMeta           `yaml:"metadata"   validate:"required"`
    Spec       CategoryTaxonomySpec `yaml:"spec"       validate:"required"`
}

type CategoryTaxonomySpec struct {
    Title     string            `yaml:"title"     validate:"required"`
    ParentRef *ObjectReference  `yaml:"parentRef"`
    Media     []MediaDefinition `yaml:"media"     validate:"omitempty,dive"`
}
```

> `ObjectMeta`, `ObjectReference`, and `MediaDefinition` are reused from `product.go` (no duplication).

### `CategoryTaxonomyStatus` (extend `status.go`)

System-written state. Never present in committed files. Stored as JSON blob in the datastore.

```go
const (
    ConditionParentResolved ConditionType = "ParentResolved"
    ConditionAcyclic        ConditionType = "Acyclic"
    // ConditionReady already defined
)

type CategoryTaxonomyStatus struct {
    ObservedGeneration  int64                           `json:"observedGeneration"`
    LastAppliedRevision string                          `json:"lastAppliedRevision"`
    Conditions          []Condition                     `json:"conditions"`
    Resolved            *ResolvedCategoryTaxonomy       `json:"resolved,omitempty"`
}

// ResolvedCategoryTaxonomy holds controller-computed aggregates (deferred to GH#244).
// Fields are nil/zero until the controller reconciles.
type ResolvedCategoryTaxonomy struct {
    Depth        int8              `json:"depth"`
    AncestorPath string            `json:"ancestorPath"` // e.g. "electronics/computers/laptops"
    Ancestors    []ObjectReference `json:"ancestors,omitempty"`
    ChildCount   int64             `json:"childCount"`
    ProductCount int64             `json:"productCount"`
}
```

---

## 2. Datastore Entity (`gitstore-api/internal/datastore/entities.go`)

```go
// CategoryTaxonomy is the git-backed Kubernetes-style category resource.
// Mirrors the Product entity structure. Distinct from the legacy Category entity.
type CategoryTaxonomy struct {
    // Identity (primary key: Namespace + Name)
    UID       string
    Namespace string
    Name      string // metadata.name — unique within namespace

    // Resource envelope
    APIVersion string
    Kind       string

    // Versioning
    Generation        int64
    ResourceVersion   string
    CreationTimestamp time.Time
    Revision          string // e.g. "main@sha1:abc123"

    // Author-supplied classification
    Labels      map[string]string
    Annotations map[string]string

    // Hierarchy — adjacency pointer + materialized path
    ParentName   string // spec.parentRef.name; empty string for root categories
    AncestorPath string // slash-separated from root to self, e.g. "electronics/computers/laptops"

    // Git provenance
    GitCommitSHA string
    GitRef       string

    // Spec and body
    Spec json.RawMessage // JSON-encoded CategoryTaxonomySpec
    Body string          // Markdown description

    // Status — system-only JSON blob (written at admission; controller fills Resolved fields)
    Status json.RawMessage
}
```

---

## 3. Datastore Interface (`gitstore-api/internal/datastore/datastore.go`)

New operations added to the `Datastore` interface:

```go
// CategoryTaxonomy operations
CreateCategoryTaxonomy(ctx context.Context, c *CategoryTaxonomy) error
GetCategoryTaxonomyByName(ctx context.Context, namespace, name string) (*CategoryTaxonomy, error)
ListCategoryTaxonomies(ctx context.Context, namespace string, page PageParams) (*PageResult[CategoryTaxonomy], error)
UpdateCategoryTaxonomy(ctx context.Context, c *CategoryTaxonomy) error
```

No `Delete` operation in this spec (deletion semantics deferred to GH#243).

---

## 4. go-memdb Schema (`gitstore-api/internal/datastore/memdb/schema.go`)

New table added to `schema`:

```go
"category_taxonomy": {
    Name: "category_taxonomy",
    Indexes: map[string]*memdb.IndexSchema{
        "id": {
            Name:    "id",
            Unique:  true,
            Indexer: &memdb.UUIDFieldIndex{Field: "UID"},
        },
        "name_namespace": {
            Name:   "name_namespace",
            Unique: true,
            Indexer: &memdb.CompoundIndex{
                Indexes: []memdb.Indexer{
                    &memdb.StringFieldIndex{Field: "Namespace"},
                    &memdb.StringFieldIndex{Field: "Name"},
                },
            },
        },
        "namespace": {
            Name:    "namespace",
            Unique:  false,
            Indexer: &memdb.StringFieldIndex{Field: "Namespace"},
        },
        "parent_name": {
            Name:    "parent_name",
            Unique:  false,
            Indexer: &memdb.StringFieldIndex{Field: "ParentName"},
        },
        "ancestor_path": {
            Name:    "ancestor_path",
            Unique:  false,
            Indexer: &memdb.StringFieldIndex{Field: "AncestorPath"},
        },
    },
},
```

---

## 5. ScyllaDB Migration (`003_category_taxonomy.cql`)

```cql
-- spec#021: CategoryTaxonomy git-backed resource table
CREATE TABLE IF NOT EXISTS category_taxonomy (
    namespace        text,
    name             text,
    uid              text,
    api_version      text,
    kind             text,
    generation       bigint,
    resource_version text,
    creation_ts      timestamp,
    revision         text,
    labels           map<text, text>,
    annotations      map<text, text>,
    parent_name      text,      -- spec.parentRef.name; empty for roots
    ancestor_path    text,      -- slash-separated from root to self
    git_commit_sha   text,
    git_ref          text,
    spec             text,      -- JSON-encoded CategoryTaxonomySpec
    body             text,      -- Markdown description
    status           text,      -- JSON-encoded CategoryTaxonomyStatus (system-only)
    PRIMARY KEY (namespace, name)
) WITH comment = 'git-backed CategoryTaxonomy resources (spec#021)';

CREATE INDEX IF NOT EXISTS ON category_taxonomy (parent_name);
CREATE INDEX IF NOT EXISTS ON category_taxonomy (ancestor_path);
```

---

## 6. Validation Rules Summary

| Field | Rule | Phase |
|---|---|---|
| `apiVersion` | `eq=catalog.gitstore.dev/v1beta1` | pre-receive (struct tag) |
| `kind` | `eq=CategoryTaxonomy` | pre-receive (struct tag) |
| `metadata.name` | `required` | pre-receive (struct tag) |
| `spec.title` | `required` | pre-receive (struct tag) |
| `spec.parentRef.name == metadata.name` | direct self-parenting — always reject | pre-receive (no DB, pure struct check) |
| `spec.parentRef.name` | parent existence: set `ParentResolved: False` if not found in DB and not in same push | post-receive AdmitResources (DB lookup, status condition) |
| intra-push mutual cycle | push contains A→parentRef=B AND B→parentRef=A | post-receive AdmitResources (graph-walk push blobs, status condition) |
| cross-push cycle | ancestor chain forms cycle across multiple pushes | controller reconciliation GH#244 (deferred) |
| `spec.media[i].fileRef.name` | `required` if entry present | pre-receive (struct tag) |
| `spec.media[i].optional: false` | File existence check | controller reconciliation (GH#244, deferred) |
| `status` | forbidden in author files | pre-receive (preParseChecks) |
| read-only metadata keys | forbidden in author files | pre-receive (preParseChecks) |
| `metadata.name` character set | valid resource identifier (no spaces, slashes) | pre-receive (new regex validator) |
| Product `spec.categoryRef` | single pointer only — type-enforced | pre-receive (struct unmarshal) |
| Cross-namespace `parentRef` | rejected (namespace must match or be empty) | pre-receive (ValidateResources) |

---

## 7. State Transitions

```
push received
    ↓
ValidateResources (pre-receive, synchronous, blocking — NO DB lookups)
    • schema validation: kind, required fields, forbidden keys (status, read-only metadata)
    • direct self-parenting: parentRef.name == metadata.name → Reject push
    ↓ Accept
AdmitResources (post-receive, fire-and-forget)
    • parse all CategoryTaxonomy blobs in the push
    • for each category:
      - intra-push cycle check: graph-walk parentRef links among push blobs
        → if cycle found: store with Acyclic=False condition
      - parentRef existence: lookup in DB
        → if not found in DB and not in same push: store with ParentResolved=False condition
      - compute AncestorPath from stored parent.AncestorPath + "/" + name (if parent found)
      - CreateCategoryTaxonomy or UpdateCategoryTaxonomy
      - write status: AdmissionAccepted=True, ParentResolved=True/False, Acyclic=True/False
    ↓
[deferred GH#244] Controller reconciliation
    • cross-push cycle detection (walk stored ancestor chain)
    • fill ResolvedCategoryTaxonomy (depth, child count, product count)
    • check File references → set FileRef status conditions
    • set Ready condition
```

---

## 8. Document Format Example

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: personal-computers
  namespace: my-store
  labels:
    gitstore.dev/segment: electronics
spec:
  parentRef:
    apiVersion: catalog.gitstore.dev/v1beta1
    kind: CategoryTaxonomy
    name: electronics
  title: Personal Computers
  media:
  - fileRef:
      name: category-hero
      kind: File
      optional: true
---

Personal Computers is the category for desktop and laptop computers.
```

Status (system-written, never in git):
```yaml
status:
  observedGeneration: 1
  lastAppliedRevision: "main@sha1:abc123"
  conditions:
  - type: AdmissionAccepted
    status: "True"
    reason: AdmittedByHookPipeline
    message: Resource admitted via the post-receive hook pipeline.
  - type: ParentResolved
    status: "True"
    reason: ParentFound
    message: Parent category exists.
  - type: Acyclic
    status: "True"
    reason: NoCycle
    message: Category ancestry has no cycle.
  - type: Ready
    status: Unknown
    reason: ControllerPending
    message: Controller has not yet reconciled this resource.
```
