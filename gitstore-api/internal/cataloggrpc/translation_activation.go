// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"fmt"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/gitstore-dev/gitstore/translation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var errTranslationInactive = status.Error(codes.Unavailable, "Translation admission is not activated")

type TranslationActivation struct {
	Enabled          bool
	DescriptorDigest string
}

func (activation TranslationActivation) Validate() error {
	if activation.DescriptorDigest != "" && activation.DescriptorDigest != translation.CapabilityDigest() {
		return fmt.Errorf("cataloggrpc: Translation descriptor capability mismatch")
	}
	if activation.Enabled && activation.DescriptorDigest == "" {
		return fmt.Errorf("cataloggrpc: enabled Translation requires an explicit descriptor capability pin")
	}
	return nil
}

func (s *Server) translationInactive(content []byte) error {
	if !s.translationActivation.Enabled && validate.IsTranslationDocument(content) {
		return errTranslationInactive
	}
	return nil
}

func (s *Server) validateTranslationActivation(trees []*catalogv1.ResourceValidationTree) error {
	for _, tree := range trees {
		for _, blobs := range [][]*catalogv1.ResourceBlob{tree.GetOldBlobs(), tree.GetProposedBlobs()} {
			for _, blob := range blobs {
				if err := s.translationInactive(blob.GetContent()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
