// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

import "time"

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

// ObjectMeta is the full Kubernetes-style resource metadata for a catalog resource.
//
// Author-writable fields (Name, Namespace, Labels, Annotations) may appear in
// committed frontmatter YAML. System-managed fields (UID through Finalizers) are
// written by the ingest pipeline and stored in the datastore only; preParseChecks
// rejects them when present in author-committed files.
type ObjectMeta struct {
	// Author-writable
	Name        string            `yaml:"name"        validate:"required"`
	Namespace   string            `yaml:"namespace"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
	// System-managed (read-only; populated from datastore at read time)
	UID               string           `yaml:"uid"`
	ResourceVersion   string           `yaml:"resourceVersion"`
	Generation        int64            `yaml:"generation"`
	CreationTimestamp time.Time        `yaml:"creationTimestamp"`
	Revision          string           `yaml:"revision"`
	OwnerReferences   []OwnerReference `yaml:"ownerReferences"`
	Finalizers        []string         `yaml:"finalizers"`
}

// ProductSpec is the author-controlled declarative specification for a product.
type ProductSpec struct {
	Title       string                    `yaml:"title"      validate:"omitempty,max=200"`
	CategoryRef *ObjectReference          `yaml:"categoryRef"`
	Tags        []string                  `yaml:"tags"`
	Media       []MediaDefinition         `yaml:"media"               validate:"omitempty,dive"`
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
