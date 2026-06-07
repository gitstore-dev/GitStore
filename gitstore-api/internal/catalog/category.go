// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

// CategoryTaxonomyResource is the top-level envelope parsed from a CategoryTaxonomy
// Markdown file's YAML frontmatter. Only author-writable fields are present here.
type CategoryTaxonomyResource struct {
	APIVersion string               `yaml:"apiVersion" validate:"required,eq=catalog.gitstore.dev/v1beta1"`
	Kind       string               `yaml:"kind"       validate:"required,eq=CategoryTaxonomy"`
	Metadata   ObjectMeta           `yaml:"metadata"   validate:"required"`
	Spec       CategoryTaxonomySpec `yaml:"spec"`
}

// CategoryTaxonomySpec is the author-controlled declarative specification for a category.
type CategoryTaxonomySpec struct {
	Title     string            `yaml:"title"     validate:"required"`
	ParentRef *ObjectReference  `yaml:"parentRef"`
	Media     []MediaDefinition `yaml:"media"     validate:"omitempty,dive"`
}
