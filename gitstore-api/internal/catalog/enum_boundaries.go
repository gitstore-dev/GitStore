// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

import "gopkg.in/yaml.v3"

// Manifest decoding keeps the established internal values used by admission,
// authorization and controllers. YAML encoding always emits authored spelling.
// Unknown values are left unchanged so normal validation can reject them.

func RepositoryVisibilityFromManifest(value string) string {
	switch value {
	case "Public", "public":
		return "PUBLIC"
	case "Private", "private":
		return "PRIVATE"
	case "Internal", "internal":
		return "INTERNAL"
	default:
		return value
	}
}

func repositoryVisibilityFromYAML(value string) string {
	// Keep legacy lowercase storage stable on re-admission; normalize it only
	// when projecting a typed GraphQL enum or encoding authored frontmatter.
	switch value {
	case "Public", "Private", "Internal":
		return RepositoryVisibilityFromManifest(value)
	default:
		return value
	}
}

func repositoryVisibilityToManifest(value string) string {
	switch RepositoryVisibilityFromManifest(value) {
	case "PUBLIC":
		return "Public"
	case "PRIVATE":
		return "Private"
	case "INTERNAL":
		return "Internal"
	default:
		return value
	}
}

func ProductLifecycleStateFromManifest(value string) string {
	switch value {
	case "Active":
		return "ACTIVE"
	case "Retired":
		return "RETIRED"
	default:
		return value
	}
}

func ProductLifecycleStateToManifest(value string) string {
	switch ProductLifecycleStateFromManifest(value) {
	case "ACTIVE":
		return "Active"
	case "RETIRED":
		return "Retired"
	default:
		return value
	}
}

func InventoryPolicyFromManifest(value string) string {
	switch value {
	case "Deny":
		return "deny"
	case "Backorder":
		return "backorder"
	default:
		return value
	}
}

func (s *NamespaceSpec) UnmarshalYAML(node *yaml.Node) error {
	type plain NamespaceSpec
	decoded := plain(*s)
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	switch decoded.Tier {
	case "User":
		decoded.Tier = "USER"
	case "Organization":
		decoded.Tier = "ORGANIZATION"
	}
	*s = NamespaceSpec(decoded)
	return nil
}

func (s NamespaceSpec) MarshalYAML() (any, error) {
	type plain NamespaceSpec
	switch s.Tier {
	case "USER":
		s.Tier = "User"
	case "ORGANIZATION":
		s.Tier = "Organization"
	}
	return plain(s), nil
}

func (s *NamespaceRepositoryDefaults) UnmarshalYAML(node *yaml.Node) error {
	type plain NamespaceRepositoryDefaults
	decoded := plain(*s)
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	decoded.Visibility = repositoryVisibilityFromYAML(decoded.Visibility)
	*s = NamespaceRepositoryDefaults(decoded)
	return nil
}

func (s NamespaceRepositoryDefaults) MarshalYAML() (any, error) {
	type plain NamespaceRepositoryDefaults
	s.Visibility = repositoryVisibilityToManifest(s.Visibility)
	return plain(s), nil
}

func (s *RepositorySpec) UnmarshalYAML(node *yaml.Node) error {
	type plain RepositorySpec
	decoded := plain(*s)
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	decoded.Visibility = repositoryVisibilityFromYAML(decoded.Visibility)
	*s = RepositorySpec(decoded)
	return nil
}

func (s RepositorySpec) MarshalYAML() (any, error) {
	type plain RepositorySpec
	s.Visibility = repositoryVisibilityToManifest(s.Visibility)
	return plain(s), nil
}

func (s *ProductLifecycleSpec) UnmarshalYAML(node *yaml.Node) error {
	type plain ProductLifecycleSpec
	decoded := plain(*s)
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	decoded.State = ProductLifecycleStateFromManifest(decoded.State)
	*s = ProductLifecycleSpec(decoded)
	return nil
}

func (s ProductLifecycleSpec) MarshalYAML() (any, error) {
	type plain ProductLifecycleSpec
	s.State = ProductLifecycleStateToManifest(s.State)
	return plain(s), nil
}

func (s *InventoryDefinition) UnmarshalYAML(node *yaml.Node) error {
	type plain InventoryDefinition
	decoded := plain(*s)
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	decoded.Policy = InventoryPolicyFromManifest(decoded.Policy)
	*s = InventoryDefinition(decoded)
	return nil
}

func (s InventoryDefinition) MarshalYAML() (any, error) {
	type plain InventoryDefinition
	switch InventoryPolicyFromManifest(s.Policy) {
	case "deny":
		s.Policy = "Deny"
	case "backorder":
		s.Policy = "Backorder"
	}
	return plain(s), nil
}

func (s *StrategyDefinition) UnmarshalYAML(node *yaml.Node) error {
	type plain StrategyDefinition
	decoded := plain(*s)
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	switch decoded.Type {
	case "Fixed":
		decoded.Type = "fixed"
	case "FixedUnitPrice":
		decoded.Type = "fixedUnitPrice"
	case "Percentage":
		decoded.Type = "percentage"
	case "Tiered":
		decoded.Type = "tiered"
	}
	*s = StrategyDefinition(decoded)
	return nil
}

func (s StrategyDefinition) MarshalYAML() (any, error) {
	type plain StrategyDefinition
	switch s.Type {
	case "fixed":
		s.Type = "Fixed"
	case "fixedUnitPrice":
		s.Type = "FixedUnitPrice"
	case "percentage":
		s.Type = "Percentage"
	case "tiered":
		s.Type = "Tiered"
	}
	return plain(s), nil
}

func (s *FileSpec) UnmarshalYAML(node *yaml.Node) error {
	type plain FileSpec
	decoded := plain(*s)
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	switch decoded.Provider {
	case "Git":
		decoded.Provider = "git"
	case "Lfs":
		decoded.Provider = "lfs"
	case "S3":
		decoded.Provider = "s3"
	case "Gcs":
		decoded.Provider = "gcs"
	}
	*s = FileSpec(decoded)
	return nil
}

func (s FileSpec) MarshalYAML() (any, error) {
	type plain FileSpec
	switch s.Provider {
	case "git":
		s.Provider = "Git"
	case "lfs":
		s.Provider = "Lfs"
	case "s3":
		s.Provider = "S3"
	case "gcs":
		s.Provider = "Gcs"
	}
	return plain(s), nil
}
