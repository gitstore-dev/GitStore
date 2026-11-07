// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The mutation path must reject exactly what a push rejects, with the same
// diagnostics, for every category validation fixture.
func TestValidateManifestMatchesPushValidationForCategoryFixtures(t *testing.T) {
	ctx := context.Background()
	store := newTestDatastore(t)
	now := time.Now().UTC()
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, &datastore.CategoryTaxonomy{
		UID: uuid.NewString(), Namespace: "gitstore", Name: "retired", APIVersion: "catalog.gitstore.dev/v1beta1",
		Kind: "CategoryTaxonomy", Generation: 1, ResourceVersion: "1", CreationTimestamp: now, DeletionTimestamp: &now,
	}))
	srv := newCatalogServer(t, store, nil)

	expected := map[string][]string{
		"valid-root.md":              nil,
		"valid-child.md":             nil,
		"missing-title.md":           {"REQUIRED_FIELD"},
		"self-parent.md":             {"SELF_PARENT"},
		"cross-namespace-parent.md":  {"CROSS_NAMESPACE_REFERENCE"},
		"media-missing-file-name.md": {"REQUIRED_FIELD"},
		"invalid-envelope.md":        {"INVALID_ENVELOPE"},
		"terminating-parent.md":      {"PARENT_TERMINATING"},
	}
	for fixture, reasons := range expected {
		t.Run(fixture, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join("testdata", "category", fixture))
			require.NoError(t, err)
			path := "categories/" + fixture

			push, err := srv.ValidateResources(ctx, &catalogv1.ValidateResourcesRequest{
				RepositoryId: testRepoID,
				Trees: []*catalogv1.ResourceValidationTree{{ProposedBlobs: []*catalogv1.ResourceBlob{
					{Path: path, BlobOid: "oid", Content: content},
				}}},
			})
			require.NoError(t, err)

			diagnostics, err := srv.ValidateManifest(ctx, admission.ManifestValidationRequest{
				RepositoryID: testRepoID, Path: path, NewContent: content,
			})
			require.NoError(t, err)

			assert.Equal(t, push.Accepted, len(diagnostics) == 0, "push and mutation must agree on acceptance")
			assert.Equal(t, admission.FromValidationErrors(push.Errors), diagnostics, "push and mutation diagnostics must match")
			assert.Equal(t, admission.FormatRejection(admission.FromValidationErrors(push.Errors)), admission.FormatRejection(diagnostics))

			got := make([]string, 0, len(diagnostics))
			for _, d := range diagnostics {
				got = append(got, d.Reason)
				assert.Equal(t, path, d.File)
				assert.Equal(t, admission.LevelFailure, d.Level)
			}
			if reasons == nil {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, reasons, got)
			}
		})
	}
}

func TestValidateManifestRejectsImmutableNameChange(t *testing.T) {
	srv := newCatalogServer(t, newTestDatastore(t), nil)
	diagnostics, err := srv.ValidateManifest(context.Background(), admission.ManifestValidationRequest{
		RepositoryID: testRepoID,
		Path:         "categories/electronics.md",
		OldContent:   categoryManifestTitled("electronics", "Electronics"),
		NewContent:   categoryManifestTitled("gadgets", "Electronics"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, diagnostics)
	assert.Equal(t, "IMMUTABLE_NAME", diagnostics[0].Reason)
	assert.Equal(t, "metadata.name", diagnostics[0].Field)
}
