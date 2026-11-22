// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package validate

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/translation"
)

// TranslationTargetSnapshot must be supplied by an authorized effective-candidate
// lookup. This adapter does no I/O and cannot establish lookup authorization.
type TranslationTargetSnapshot struct {
	APIVersion      string
	Kind            string
	Namespace       string
	Name            string
	UID             string
	ResourceVersion string
	Spec            map[string]any
}

type TranslationSnapshotValidation struct {
	TargetResolved bool
	Reason         string
	ValidatedPaths []string
}

// ValidateTranslationSnapshot treats nil as an authorized, successful lookup
// of an absent target, never as a provider, transport or storage failure.
func ValidateTranslationSnapshot(ctx context.Context, resource *catalog.TranslationResource, body []byte, namespace string, target *TranslationTargetSnapshot) (*TranslationSnapshotValidation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resource == nil || namespace == "" {
		return nil, translationError("metadata.namespace", "NamespaceUnavailable", "an effective source namespace is required")
	}
	if resource.Metadata.Namespace != "" && resource.Metadata.Namespace != namespace {
		return nil, translationError("metadata.namespace", "CrossNamespaceRef", "source must belong to its effective namespace")
	}
	if err := translation.ValidateSpec(resource.Spec, namespace, body); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if target == nil {
		return &TranslationSnapshotValidation{Reason: "Missing"}, nil
	}
	ref := resource.Spec.TargetRef
	if target.APIVersion != ref.APIVersion || target.Kind != ref.Kind || target.Name != ref.Name || target.Namespace != namespace {
		return nil, translationError("spec.targetRef", "TargetSnapshotUnavailable", "snapshot does not match the authorized target identity")
	}
	if ref.UID != "" && target.UID != "" && ref.UID != target.UID {
		return nil, translationError("spec.targetRef.uid", "PinMismatch", "target UID does not match its pin")
	}
	if ref.ResourceVersion != "" && target.ResourceVersion != "" && ref.ResourceVersion != target.ResourceVersion {
		return nil, translationError("spec.targetRef.resourceVersion", "PinMismatch", "target resourceVersion does not match its pin")
	}
	paths, err := translation.ValidateFieldsContext(ctx, ref.APIVersion, ref.Kind, target.Spec, resource.Spec.Patches, body)
	if err != nil {
		return nil, err
	}
	if (ref.UID != "" && target.UID == "") || (ref.ResourceVersion != "" && target.ResourceVersion == "") {
		return &TranslationSnapshotValidation{Reason: "PinUnavailable"}, nil
	}
	return &TranslationSnapshotValidation{TargetResolved: true, ValidatedPaths: paths}, nil
}
