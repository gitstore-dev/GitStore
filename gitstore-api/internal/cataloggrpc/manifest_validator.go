// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"context"
	"fmt"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
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
	response, err := s.ValidateResources(ctx, &catalogv1.ValidateResourcesRequest{
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
