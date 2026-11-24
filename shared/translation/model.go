// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package translation defines pure validation shared by catalog ingestion and
// reconciliation. Callers own authorization, target lookup and persistence.
package translation

import (
	"bytes"
	"fmt"
	"unicode/utf8"
)

const (
	APIVersion       = "i18n.gitstore.dev/v1beta1"
	MaxDocumentBytes = 1 << 20
	MaxPatches       = 256
	MaxPathBytes     = 1024
	MaxPathSegments  = 16
	MaxSelectors     = 8
)

type Spec struct {
	Locale    string    `json:"locale" yaml:"locale"`
	TargetRef Reference `json:"targetRef" yaml:"targetRef"`
	Patches   []Patch   `json:"patches,omitempty" yaml:"patches,omitempty"`
}

type Reference struct {
	APIVersion      string `json:"apiVersion" yaml:"apiVersion"`
	Kind            string `json:"kind" yaml:"kind"`
	Name            string `json:"name" yaml:"name"`
	Namespace       string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	UID             string `json:"uid,omitempty" yaml:"uid,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty" yaml:"resourceVersion,omitempty"`
}

type Patch struct {
	FieldPath   string `json:"fieldPath" yaml:"fieldPath"`
	Translation string `json:"translation" yaml:"translation"`
}

type Error struct {
	Field   string
	Reason  string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s: %s", e.Field, e.Reason, e.Message)
}

func invalid(field, reason, message string) error {
	return &Error{Field: field, Reason: reason, Message: message}
}

// ValidateSpec checks every rule decidable without a target lookup.
func ValidateSpec(spec Spec, namespace string, body []byte) error {
	if err := validateContentBudget(spec.Patches, body); err != nil {
		return err
	}
	if _, err := CanonicalLocale(spec.Locale); err != nil {
		return err
	}
	if spec.TargetRef.Name == "" {
		return invalid("spec.targetRef.name", "Required", "a target name is required")
	}
	if namespace != "" && spec.TargetRef.Namespace != "" && spec.TargetRef.Namespace != namespace {
		return invalid("spec.targetRef.namespace", "CrossNamespaceRef", "the target must be in the same namespace")
	}
	descriptor, err := DescriptorFor(spec.TargetRef.APIVersion, spec.TargetRef.Kind)
	if err != nil {
		return err
	}
	if len(spec.Patches) == 0 && len(bytes.TrimSpace(body)) == 0 {
		return invalid("spec.patches", "EmptyTranslation", "a patch or meaningful body is required")
	}
	if !utf8.Valid(body) {
		return invalid("body", "InvalidEncoding", "body must be valid UTF-8")
	}
	paths := make([]string, len(spec.Patches))
	for i, patch := range spec.Patches {
		paths[i] = patch.FieldPath
	}
	pathIndex, pathErr := validatePaths(paths)
	for i, patch := range spec.Patches {
		if i == pathIndex {
			return pathErr
		}
		if !utf8.ValidString(patch.Translation) {
			return invalid(fmt.Sprintf("spec.patches[%d].translation", i), "InvalidEncoding", "text must be valid UTF-8")
		}
		if err := descriptor.validatePath(patch.FieldPath); err != nil {
			return patchError(i, err)
		}
		if err := descriptor.validateText(patch.FieldPath, patch.Translation); err != nil {
			return invalid(fmt.Sprintf("spec.patches[%d].translation", i), "InvalidText", "translation does not satisfy the target text constraint")
		}
	}
	return nil
}

func validateContentBudget(patches []Patch, body []byte) error {
	if len(patches) > MaxPatches {
		return invalid("spec.patches", "LimitExceeded", "at most 256 patches are permitted")
	}
	remaining := MaxDocumentBytes
	charge := func(size int) bool {
		if size > remaining {
			return false
		}
		remaining -= size
		return true
	}
	if !charge(len(body)) {
		return invalid("body", "LimitExceeded", "Translation content exceeds the document byte budget")
	}
	for i, patch := range patches {
		if !charge(len(patch.FieldPath)) || !charge(len(patch.Translation)) {
			return invalid(fmt.Sprintf("spec.patches[%d]", i), "LimitExceeded", "Translation content exceeds the document byte budget")
		}
	}
	return nil
}

func patchError(index int, err error) error {
	if issue, ok := err.(*Error); ok {
		copy := *issue
		copy.Field = fmt.Sprintf("spec.patches[%d].fieldPath", index)
		return &copy
	}
	return err
}
