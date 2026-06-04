// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

// ProductResource is the top-level envelope parsed from a product Markdown
// file's YAML frontmatter. Only author-writable fields are present here;
// status and read-only metadata fields are stored in the datastore and merged
// at read time.
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
	Name         string            `yaml:"name"         validate:"required"`
	Namespace    string            `yaml:"namespace"`
	GenerateName string            `yaml:"generateName"`
	Labels       map[string]string `yaml:"labels"`
	Annotations  map[string]string `yaml:"annotations"`
}

// ProductSpec is the author-controlled declarative specification for a product.
type ProductSpec struct {
	Title       string                    `yaml:"title"      validate:"omitempty,max=200"`
	CategoryRef *ObjectReference          `yaml:"categoryRef"`
	Tags        []string                  `yaml:"tags"`
	Media       []MediaDefinition         `yaml:"media"`
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
// Name is validated by validateSpec (not struct tags) so the index appears in
// the error message.
type ProductOptionDefinition struct {
	Name   string   `yaml:"name"`
	Title  string   `yaml:"title"`
	Values []string `yaml:"values"`
}
