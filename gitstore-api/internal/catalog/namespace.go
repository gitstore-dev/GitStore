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
	Title              string                       `yaml:"title" validate:"omitempty,max=200"`
	Tier               string                       `yaml:"tier" validate:"required,oneof=USER ORGANIZATION"`
	RepositoryDefaults *NamespaceRepositoryDefaults `yaml:"repositoryDefaults"`
	PushPolicyDefaults *NamespacePushPolicyDefaults `yaml:"pushPolicyDefaults"`
}

type NamespaceRepositoryDefaults struct {
	Visibility    string `yaml:"visibility"`
	DefaultBranch string `yaml:"defaultBranch"`
}

type NamespacePushPolicyDefaults struct {
	MaxPackSizeBytes int64 `yaml:"maxPackSizeBytes"`
	MaxFileSizeBytes int64 `yaml:"maxFileSizeBytes"`
}
