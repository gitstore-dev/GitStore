// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

import "github.com/gitstore-dev/gitstore/translation"

type TranslationResource struct {
	APIVersion string          `yaml:"apiVersion" validate:"required,eq=i18n.gitstore.dev/v1beta1"`
	Kind       string          `yaml:"kind" validate:"required,eq=Translation"`
	Metadata   ObjectMeta      `yaml:"metadata" validate:"required"`
	Spec       TranslationSpec `yaml:"spec"`
}

type TranslationSpec = translation.Spec
type TranslationPatch = translation.Patch

type TranslationStatus struct {
	ObservedGeneration  int64                `json:"observedGeneration"`
	LastAppliedRevision string               `json:"lastAppliedRevision"`
	Conditions          []Condition          `json:"conditions"`
	Resolved            *ResolvedTranslation `json:"resolved,omitempty"`
}

type ResolvedTranslation struct {
	CanonicalLocale     string                     `json:"canonicalLocale,omitempty"`
	Target              *TranslationTargetEvidence `json:"target,omitempty"`
	ValidatedFieldPaths []string                   `json:"validatedFieldPaths"`
	// Validation fingerprints are persisted, not public resolved fields.
	DescriptorDigest string `json:"descriptorDigest,omitempty"`
	SourceDigest     string `json:"sourceDigest,omitempty"`
}

type TranslationTargetEvidence struct {
	APIVersion      string `json:"apiVersion"`
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	UID             string `json:"uid"`
	Generation      int64  `json:"generation"`
	Revision        string `json:"revision"`
	ResourceVersion string `json:"resourceVersion"`
	ContentDigest   string `json:"contentDigest"`
}
