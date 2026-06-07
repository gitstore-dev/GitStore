// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

// CollectionResource is the top-level envelope parsed from a Collection
// Markdown file's YAML frontmatter. Only author-writable fields are present here.
type CollectionResource struct {
	APIVersion string         `yaml:"apiVersion" validate:"required,eq=catalog.gitstore.dev/v1beta1"`
	Kind       string         `yaml:"kind"       validate:"required,eq=Collection"`
	Metadata   ObjectMeta     `yaml:"metadata"   validate:"required"`
	Spec       CollectionSpec `yaml:"spec"`
}

// CollectionSpec is the author-controlled declarative specification for a collection.
type CollectionSpec struct {
	Title     string            `yaml:"title"     validate:"required"`
	Selector  *LabelSelector    `yaml:"selector"`
	TargetRef *ObjectReference  `yaml:"targetRef"`
	Media     []MediaDefinition `yaml:"media"     validate:"omitempty,dive"`
}

// LabelSelector selects resources by label constraints.
// matchLabels and matchExpressions are combined with logical AND.
// An empty or absent selector matches nothing.
type LabelSelector struct {
	MatchLabels      map[string]string          `yaml:"matchLabels"`
	MatchExpressions []LabelSelectorRequirement `yaml:"matchExpressions" validate:"omitempty,dive"`
}

// LabelSelectorRequirement is a single set-based label constraint.
type LabelSelectorRequirement struct {
	Key      string   `yaml:"key"      validate:"required"`
	Operator string   `yaml:"operator" validate:"required,oneof=In NotIn Exists DoesNotExist"`
	Values   []string `yaml:"values"`
}
