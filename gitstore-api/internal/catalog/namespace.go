// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

type NamespaceResource struct {
	APIVersion string        `yaml:"apiVersion" validate:"required,eq=gitstore.dev/v1beta1"`
	Kind       string        `yaml:"kind" validate:"required,eq=Namespace"`
	Metadata   ObjectMeta    `yaml:"metadata" validate:"required"`
	Spec       NamespaceSpec `yaml:"spec" validate:"required"`
}

type NamespaceSpec struct {
	Title              string                       `yaml:"title" json:"title" validate:"omitempty,max=200"`
	Tier               string                       `yaml:"tier" json:"tier" validate:"required,oneof=USER ORGANIZATION"`
	RepositoryDefaults *NamespaceRepositoryDefaults `yaml:"repositoryDefaults" json:"repositoryDefaults,omitempty"`
	PushPolicyDefaults *NamespacePushPolicyDefaults `yaml:"pushPolicyDefaults" json:"pushPolicyDefaults,omitempty"`
}

type NamespaceRepositoryDefaults struct {
	Visibility    string `yaml:"visibility" json:"visibility"`
	DefaultBranch string `yaml:"defaultBranch" json:"defaultBranch"`
}

type NamespacePushPolicyDefaults struct {
	MaxPackSizeBytes int64 `yaml:"maxPackSizeBytes" json:"maxPackSizeBytes"`
	MaxFileSizeBytes int64 `yaml:"maxFileSizeBytes" json:"maxFileSizeBytes"`
}
