# Contract: Go Type Definitions — Product Kubernetes-style Resource

**Package**: `github.com/gitstore-dev/gitstore/api/internal/catalog`  
**Feature**: 014-product-frontmatter  
**Date**: 2026-06-01

These are the authoritative type signatures for the Go implementation. They define the contract between the YAML frontmatter parser, the admission layer, the datastore, and the GraphQL resolvers.

---

## Frontmatter Types (git-sourced, author-written)

```go
// ProductResource is the top-level envelope parsed from a product Markdown file's
// YAML frontmatter. Only author-writable fields are present here — status and
// read-only metadata fields are stored in the datastore and merged at read time.
type ProductResource struct {
    APIVersion string      `yaml:"apiVersion" validate:"required,eq=catalog.gitstore.dev/v1beta1"`
    Kind       string      `yaml:"kind"       validate:"required,eq=Product"`
    Metadata   ObjectMeta  `yaml:"metadata"   validate:"required"`
    Spec       ProductSpec `yaml:"spec"`
}

// ObjectMeta holds author-supplied metadata. Read-only system fields (UID,
// ResourceVersion, Generation, CreationTimestamp, Revision, OwnerReferences)
// are managed by the system and stored in the datastore only.
type ObjectMeta struct {
    Name         string            `yaml:"name"         validate:"required,slug"`
    Namespace    string            `yaml:"namespace"`
    GenerateName string            `yaml:"generateName"`
    Labels       map[string]string `yaml:"labels"       validate:"omitempty,k8s_labels"`
    Annotations  map[string]string `yaml:"annotations"`
}

// ProductSpec is the author-controlled declarative specification for a product.
type ProductSpec struct {
    Title       string                   `yaml:"title"       validate:"omitempty,max=200"`
    CategoryRef *ObjectReference         `yaml:"categoryRef"`
    Tags        []string                 `yaml:"tags"`
    Media       []MediaDefinition        `yaml:"media"`
    Options     []ProductOptionDefinition `yaml:"options"    validate:"omitempty,dive"`
}

// ObjectReference is a pointer to another catalogue resource.
type ObjectReference struct {
    APIVersion      string `yaml:"apiVersion"`
    Kind            string `yaml:"kind"`
    Name            string `yaml:"name"      validate:"required"`
    Namespace       string `yaml:"namespace"`
    UID             string `yaml:"uid"`
    ResourceVersion string `yaml:"resourceVersion"`
    FieldPath       string `yaml:"fieldPath"`
}

// MediaDefinition is a product media slot referencing a File resource.
type MediaDefinition struct {
    FileRef FileReference `yaml:"fileRef" validate:"required"`
}

// FileReference identifies a File resource by name and kind.
type FileReference struct {
    Name     string `yaml:"name" validate:"required"`
    Kind     string `yaml:"kind" validate:"required"`
    Optional bool   `yaml:"optional"`
}

// ProductOptionDefinition is a variant dimension (e.g. Colour, Size).
type ProductOptionDefinition struct {
    Name   string   `yaml:"name"   validate:"required"`
    Title  string   `yaml:"title"`
    Values []string `yaml:"values"`
}
```

---

## Status Types (datastore-sourced, system-written)

```go
// ProductStatus is the system-written state for a product. Never stored in git.
type ProductStatus struct {
    ObservedGeneration  int64                    `json:"observedGeneration"`
    LastAppliedRevision string                   `json:"lastAppliedRevision"`
    Conditions          []Condition              `json:"conditions"`
    Resolved            *ResolvedProductDefinition `json:"resolved,omitempty"`
}

// Condition is a named status signal following the Kubernetes condition convention.
type Condition struct {
    Type               ConditionType `json:"type"               validate:"required,oneof=Published AdmissionAccepted CategoryResolved OptionsAccepted VariantsResolved Ready"`
    Status             ConditionStatus `json:"status"           validate:"required,oneof=True False Unknown"`
    ObservedGeneration int64         `json:"observedGeneration"`
    LastTransitionTime time.Time     `json:"lastTransitionTime"`
    Reason             string        `json:"reason"`
    Message            string        `json:"message"`
}

type ConditionType   = string
type ConditionStatus = string

const (
    ConditionPublished         ConditionType = "Published"
    ConditionAdmissionAccepted ConditionType = "AdmissionAccepted"
    ConditionCategoryResolved  ConditionType = "CategoryResolved"
    ConditionOptionsAccepted   ConditionType = "OptionsAccepted"
    ConditionVariantsResolved  ConditionType = "VariantsResolved"
    ConditionReady             ConditionType = "Ready"

    ConditionTrue    ConditionStatus = "True"
    ConditionFalse   ConditionStatus = "False"
    ConditionUnknown ConditionStatus = "Unknown"
)

// ResolvedProductDefinition holds system-computed aggregates for a product.
type ResolvedProductDefinition struct {
    Category          *ResolvedCategoryDefinition `json:"category,omitempty"`
    PriceRange        []PriceRangeDefinition      `json:"priceRange,omitempty"`
    TotalInventory    int64                        `json:"totalInventory"`
    VariantSummary    *VariantSummaryDefinition    `json:"variantSummary,omitempty"`
    DefaultVariantRef *ObjectReference             `json:"defaultVariantRef,omitempty"`
    Media             []ResolvedFileDefinition     `json:"media,omitempty"`
}

type ResolvedCategoryDefinition struct {
    Name string   `json:"name"`
    Path []string `json:"path"`
}

// PriceRangeDefinition uses shopspring/decimal for monetary values, consistent
// with the rest of the API (scalar/scalars.go Decimal scalar binding).
type PriceRangeDefinition struct {
    CurrencyCode string          `json:"currencyCode"`
    Min          decimal.Decimal `json:"min"`
    Max          decimal.Decimal `json:"max"`
}

type VariantSummaryDefinition struct {
    Total       int64 `json:"total"`
    Ready       int64 `json:"ready"`
    Unavailable int64 `json:"unavailable"`
}

type ResolvedFileDefinition struct {
    Name        string `json:"name"`
    URL         string `json:"url"`
    ContentType string `json:"contentType"`
}
```

---

## Read-Only Metadata Types (datastore-sourced)

```go
// SystemObjectMeta holds read-only fields the system assigns. Merged with
// ObjectMeta at read time to produce the full resource view.
type SystemObjectMeta struct {
    UID             string           `json:"uid"`
    ResourceVersion string           `json:"resourceVersion"`
    Generation      int64            `json:"generation"`
    CreationTimestamp time.Time      `json:"creationTimestamp"`
    Revision        string           `json:"revision"`
    OwnerReferences []OwnerReference `json:"ownerReferences,omitempty"`
}

type OwnerReference struct {
    APIVersion string `json:"apiVersion"`
    Kind       string `json:"kind"`
    Name       string `json:"name"`
    UID        string `json:"uid"`
}
```

---

## Forbidden Fields Contract (Admission Validation)

Author-pushed Markdown files MUST NOT contain these YAML keys. The admission handler in `gitstore-git-service` must reject files that include them.

**At `status:` top level**: any content under this key is forbidden in pushed files.

**At `metadata:` level**: the following keys are forbidden:
- `uid`
- `resourceVersion`
- `generation`
- `creationTimestamp`
- `revision`
- `ownerReferences`

**Error format**:
```
admission rejected: <file-path>: <reason>
```

Examples:
- `admission rejected: products/macbook-pro.md: status is system-managed and must not be set by authors`
- `admission rejected: products/macbook-pro.md: metadata.uid is read-only`
- `admission rejected: products/macbook-pro.md: document does not use Kubernetes-style frontmatter (missing apiVersion); migration is not supported in alpha`
