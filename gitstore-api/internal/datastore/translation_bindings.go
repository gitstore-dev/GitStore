// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/gitstore-dev/gitstore/translation"
	"github.com/google/uuid"
)

func CloneTranslationBinding(binding *TranslationBinding) *TranslationBinding {
	if binding == nil {
		return nil
	}
	cloned := *binding
	cloned.Epochs = slices.Clone(binding.Epochs)
	return &cloned
}

func translationBindingReference(source *Translation) (TranslationBindingEpoch, error) {
	if source == nil || source.ResourceVersion == "" || len(source.ResourceVersion) > 256 {
		return TranslationBindingEpoch{}, ErrInvalidArgument
	}
	var spec translation.Spec
	if err := json.Unmarshal(source.Spec, &spec); err != nil || spec.TargetRef.Name == "" ||
		(spec.TargetRef.Namespace != "" && spec.TargetRef.Namespace != source.Namespace) {
		return TranslationBindingEpoch{}, ErrInvalidArgument
	}
	if _, err := translation.DescriptorFor(spec.TargetRef.APIVersion, spec.TargetRef.Kind); err != nil {
		return TranslationBindingEpoch{}, fmt.Errorf("%w: unsupported Translation binding reference", ErrInvalidArgument)
	}
	return TranslationBindingEpoch{Target: TranslationTargetKey{
		NamespaceUID: source.NamespaceID, APIVersion: spec.TargetRef.APIVersion,
		Kind: spec.TargetRef.Kind, Name: spec.TargetRef.Name,
	}, UIDPin: spec.TargetRef.UID, VersionPin: spec.TargetRef.ResourceVersion}, nil
}

// PrepareTranslationBinding must commit with its source transition, or through
// the backend's durable source/binding protocol. It does not itself persist.
func PrepareTranslationBinding(source *Translation, current *TranslationBinding) (*TranslationBinding, error) {
	reference, err := translationBindingReference(source)
	if err != nil {
		return nil, err
	}
	if current == nil {
		reference.Epoch = uuid.NewString()
		initial := &TranslationBinding{TranslationUID: source.UID, ResourceVersion: "1",
			SourceVersion: source.ResourceVersion, CurrentEpoch: reference.Epoch,
			Epochs: []TranslationBindingEpoch{reference}}
		return boundedTranslationBinding(initial)
	}
	if current.TranslationUID != source.UID || len(current.Epochs) == 0 || len(current.Epochs) > MaxTranslationEpochs ||
		current.Epochs[len(current.Epochs)-1].Epoch != current.CurrentEpoch ||
		current.ResourceVersion == "" || len(current.ResourceVersion) > 256 {
		return nil, ErrRepairRequired
	}
	next := CloneTranslationBinding(current)
	next.SourceVersion = source.ResourceVersion
	next.ResourceVersion = nextResourceVersion(current.ResourceVersion)
	if len(next.ResourceVersion) > 256 ||
		ValidateCatalogUpdateVersion(current.ResourceVersion, current.ResourceVersion, next.ResourceVersion) != nil {
		return nil, ErrRepairRequired
	}
	last := current.Epochs[len(current.Epochs)-1]
	if last.Target == reference.Target && last.UIDPin == reference.UIDPin && last.VersionPin == reference.VersionPin {
		return boundedTranslationBinding(next)
	}
	if len(current.Epochs) == MaxTranslationEpochs {
		return nil, fmt.Errorf("%w: Translation reference epoch budget exhausted", ErrInvalidArgument)
	}
	for i := len(current.Epochs) - 1; i >= 0; i-- {
		previous := current.Epochs[i]
		if previous.Target == reference.Target && previous.BoundUID != "" {
			if reference.UIDPin == "" || reference.UIDPin == previous.BoundUID {
				reference.BoundUID = previous.BoundUID
			}
			break
		}
	}
	reference.Epoch = uuid.NewString()
	next.CurrentEpoch = reference.Epoch
	next.Epochs = append(next.Epochs, reference)
	return boundedTranslationBinding(next)
}

func boundedTranslationBinding(binding *TranslationBinding) (*TranslationBinding, error) {
	remaining := translation.MaxDocumentBytes - 4096
	for _, epoch := range binding.Epochs {
		// Reserve the eventual UUID before first binding so status/binding
		// progress cannot exhaust space already admitted for this epoch.
		epoch.BoundUID = "00000000-0000-4000-8000-000000000000"
		raw, err := json.Marshal(epoch)
		if err != nil || len(raw)+1 > remaining {
			return nil, fmt.Errorf("%w: Translation binding byte budget exhausted", ErrInvalidArgument)
		}
		remaining -= len(raw) + 1
	}
	return binding, nil
}

func ValidateTranslationBindingUpdate(current, candidate *TranslationBinding, source *Translation, expected string) error {
	if current == nil || candidate == nil || source == nil || len(candidate.ResourceVersion) > 256 {
		return ErrInvalidArgument
	}
	if err := ValidateCatalogUpdateVersion(current.ResourceVersion, expected, candidate.ResourceVersion); err != nil {
		return err
	}
	if current.SourceVersion != source.ResourceVersion || candidate.SourceVersion != source.ResourceVersion ||
		current.TranslationUID != source.UID || candidate.TranslationUID != current.TranslationUID ||
		candidate.CurrentEpoch != current.CurrentEpoch || len(current.Epochs) == 0 || len(current.Epochs) > MaxTranslationEpochs ||
		len(candidate.Epochs) != len(current.Epochs) {
		return ErrConflict
	}
	reference, err := translationBindingReference(source)
	if err != nil {
		return err
	}
	for i, previous := range current.Epochs {
		desired := candidate.Epochs[i]
		if i == len(current.Epochs)-1 {
			if previous.Epoch != current.CurrentEpoch || previous.Target != reference.Target ||
				previous.UIDPin != reference.UIDPin || previous.VersionPin != reference.VersionPin {
				return ErrRepairRequired
			}
			if _, err := uuid.Parse(desired.BoundUID); err != nil {
				return ErrInvalidArgument
			}
			if (previous.BoundUID != "" && desired.BoundUID != previous.BoundUID) ||
				(previous.UIDPin != "" && desired.BoundUID != previous.UIDPin) {
				return ErrConflict
			}
			previous.BoundUID = desired.BoundUID
		}
		if desired != previous {
			return ErrConflict
		}
	}
	return nil
}
