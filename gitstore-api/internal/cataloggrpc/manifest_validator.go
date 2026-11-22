// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"bytes"
	"context"
	"fmt"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
)

var _ admission.ManifestValidator = (*Server)(nil)

// ValidateManifest applies the push pre-receive validation to one manifest
// change, so a mutation can never admit a manifest a push would reject.
func (s *Server) ValidateManifest(ctx context.Context, req admission.ManifestValidationRequest) ([]admission.Diagnostic, error) {
	if req.RepositoryID == "" || req.Path == "" {
		return nil, fmt.Errorf("manifest validation requires repository and path")
	}
	tree := &catalogv1.ResourceValidationTree{}
	if req.OldContent != nil {
		tree.OldBlobs = []*catalogv1.ResourceBlob{{Path: req.Path, Content: req.OldContent}}
	}
	if req.NewContent != nil {
		tree.ProposedBlobs = []*catalogv1.ResourceBlob{{Path: req.Path, Content: req.NewContent}}
	}
	response, err := s.validateResources(ctx, &catalogv1.ValidateResourcesRequest{
		RepositoryId: req.RepositoryID,
		Trees:        []*catalogv1.ResourceValidationTree{tree},
	})
	if err != nil {
		return nil, err
	}
	if response.GetAccepted() {
		return nil, nil
	}
	return admission.FromValidationErrors(response.GetErrors()), nil
}

// This internal stage requires an already authorized snapshot. It does not
// activate public admission or substitute for effective-candidate lookup.
func validateTranslationManifestSnapshot(ctx context.Context, parser ResourceParser, content []byte, namespace string, target *validate.TranslationTargetSnapshot) (*validate.ParsedResource, []byte, *validate.TranslationSnapshotValidation, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	parsed, body, err := parser.ParseResource(bytes.NewReader(content))
	if err != nil {
		return nil, nil, nil, err
	}
	if parsed == nil || parsed.Kind != "Translation" || parsed.Translation == nil {
		return nil, nil, nil, fmt.Errorf("translation manifest validation requires a Translation resource")
	}
	result, err := validate.ValidateTranslationSnapshot(ctx, parsed.Translation, body, namespace, target)
	if err != nil {
		return nil, nil, nil, err
	}
	return parsed, body, result, nil
}

func (s *Server) validateTranslationManifestCandidate(ctx context.Context, content []byte, namespace string, operations map[string]resourceAdmissionOperation) (*validate.ParsedResource, []byte, *validate.TranslationSnapshotValidation, error) {
	parsed, body, _, err := validateTranslationManifestSnapshot(ctx, s.parser, content, namespace, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	result, err := s.validateTranslationManifestTarget(ctx, parsed, body, namespace, operations)
	if err != nil {
		return nil, nil, nil, err
	}
	return parsed, body, result, nil
}

func (s *Server) validateTranslationManifestTarget(ctx context.Context, parsed *validate.ParsedResource, body []byte, namespace string, operations map[string]resourceAdmissionOperation) (result *validate.TranslationSnapshotValidation, err error) {
	started := time.Now()
	defer func() {
		s.translationMetrics.observeValidation(ctx, translationValidationTarget, started, err, result != nil && !result.TargetResolved)
	}()
	target, err := s.lookupTranslationManifestTarget(ctx, parsed.Translation, namespace, operations)
	if err != nil {
		return nil, err
	}
	return validate.ValidateTranslationSnapshot(ctx, parsed.Translation, body, namespace, target)
}
