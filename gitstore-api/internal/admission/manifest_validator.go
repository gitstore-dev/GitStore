// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admission

import "context"

// ManifestValidationRequest describes one manifest change to validate before
// it is committed. OldContent is nil on create; NewContent is nil on delete.
type ManifestValidationRequest struct {
	RepositoryID string
	Path         string
	OldContent   []byte
	NewContent   []byte
}

// ManifestValidator runs the checks a Git push enforces before the ref moves,
// for a single manifest. API commits skip the Git hooks, so mutations call it
// before committing. It returns FAILURE diagnostics, or none when accepted.
type ManifestValidator interface {
	ValidateManifest(ctx context.Context, req ManifestValidationRequest) ([]Diagnostic, error)
}
