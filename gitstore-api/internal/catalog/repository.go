// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

// RepositoryResource is the author-controlled Git manifest for a Repository.
// Status and owner references are intentionally absent: both are system-owned.
type RepositoryResource struct {
	APIVersion string         `yaml:"apiVersion" validate:"required,eq=gitstore.dev/v1beta1"`
	Kind       string         `yaml:"kind" validate:"required,eq=Repository"`
	Metadata   ObjectMeta     `yaml:"metadata" validate:"required"`
	Spec       RepositorySpec `yaml:"spec" validate:"required"`
}

type RepositorySpec struct {
	DefaultBranch string `yaml:"defaultBranch" json:"defaultBranch" validate:"omitempty,max=255"`
	Visibility    string `yaml:"visibility" json:"visibility" validate:"omitempty,oneof=PUBLIC PRIVATE INTERNAL public private internal"`
	StorageClass  string `yaml:"storageClass" json:"storageClass" validate:"omitempty,max=255"`
}
