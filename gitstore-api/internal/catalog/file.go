// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

import "fmt"

const FileAPIVersion = "storage.gitstore.dev/v1beta1"

type FileResource struct {
	APIVersion string     `yaml:"apiVersion" validate:"required,eq=storage.gitstore.dev/v1beta1"`
	Kind       string     `yaml:"kind" validate:"required,eq=File"`
	Metadata   ObjectMeta `yaml:"metadata" validate:"required"`
	Spec       FileSpec   `yaml:"spec"`
}

type FileSpec struct {
	ContentType string                    `yaml:"contentType" validate:"required"`
	Type        string                    `yaml:"type,omitempty"`
	Source      FileSourceDefinition      `yaml:"source" validate:"required"`
	Processing  *FileProcessingDefinition `yaml:"processing,omitempty"`
}

type FileSourceDefinition struct {
	Type           string        `yaml:"type" validate:"required"`
	URI            string        `yaml:"uri" validate:"required"`
	Checksum       *FileChecksum `yaml:"checksum,omitempty"`
	CredentialsRef *SecretRef    `yaml:"credentialsRef,omitempty"`
}

type FileChecksum struct {
	Algorithm string `yaml:"algorithm" validate:"required"`
	Value     string `yaml:"value" validate:"required"`
}

type SecretRef struct {
	Kind      string `yaml:"kind" validate:"required"`
	Name      string `yaml:"name" validate:"required"`
	Key       string `yaml:"key,omitempty"`
	Namespace string `yaml:"namespace,omitempty"`
}

type FileProcessingDefinition struct {
	Image *FileImageProcessing `yaml:"image,omitempty"`
}

type FileImageProcessing struct {
	Variants []FileVariantRequest `yaml:"variants,omitempty" validate:"omitempty,dive"`
}

type FileVariantRequest struct {
	Name string `yaml:"name" validate:"required"`
}

type FileStatus struct {
	ObservedGeneration  int64                   `json:"observedGeneration"`
	LastAppliedRevision string                  `json:"lastAppliedRevision"`
	Conditions          []Condition             `json:"conditions"`
	Resolved            *ResolvedFileDefinition `json:"resolved,omitempty"`
}

type ResolvedFileVariant struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

func (s FileSpec) Validate(resourceNamespace string) error {
	switch s.Source.Type {
	case "git", "lfs", "s3", "gcs":
	default:
		return fmt.Errorf("validate: spec.source.type must be one of git, lfs, s3, gcs, got %q", s.Source.Type)
	}
	if s.ContentType == "" {
		return fmt.Errorf("validate: spec.contentType is required")
	}
	if s.Source.URI == "" {
		return fmt.Errorf("validate: spec.source.uri is required")
	}
	if s.Source.Checksum != nil && (s.Source.Checksum.Algorithm == "" || s.Source.Checksum.Value == "") {
		return fmt.Errorf("validate: spec.source.checksum.algorithm and value are required together")
	}
	if s.Source.CredentialsRef != nil && s.Source.CredentialsRef.Namespace != "" &&
		s.Source.CredentialsRef.Namespace != resourceNamespace {
		return fmt.Errorf("validate: spec.source.credentialsRef.namespace must match the resource namespace")
	}
	if s.Processing != nil && s.Processing.Image != nil {
		for i, variant := range s.Processing.Image.Variants {
			if variant.Name == "" {
				return fmt.Errorf("validate: spec.processing.image.variants[%d].name is required", i)
			}
		}
	}
	return nil
}
