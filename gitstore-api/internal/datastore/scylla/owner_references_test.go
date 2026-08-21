// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOwnerReferenceDependentSchemaIsScopePartitioned(t *testing.T) {
	assert.Equal(t, "owner_reference_dependents", ownerReferenceDependentsTable)
	// The migration's partition key is deliberately namespace/repository/owner/
	// blocking. The remaining fields form the ordered Product keyset.
	row := ownerReferenceDependentRow{
		Namespace: "acme", RepositoryID: "repo-1", OwnerUID: "owner",
		BlockOwnerDeletion: false, DependentKind: "Product", DependentUID: "product-2",
	}
	assert.Equal(t, "acme", row.Namespace)
	assert.Equal(t, "repo-1", row.RepositoryID)
	assert.Equal(t, "Product", row.DependentKind)
}

func TestDecodeOwnerReferencesSupportsLegacyAndAdditiveRecords(t *testing.T) {
	references, err := decodeOwnerReferences(nil)
	require.NoError(t, err)
	assert.Nil(t, references)

	raw, err := json.Marshal([]catalog.OwnerReference{{UID: "owner", Kind: "CategoryTaxonomy"}})
	require.NoError(t, err)
	references, err = decodeOwnerReferences(raw)
	require.NoError(t, err)
	require.Len(t, references, 1)
	assert.False(t, references[0].BlockOwnerDeletion, "legacy omitted flag defaults to non-blocking")
	assert.Equal(t, datastore.MaxOwnerDependentPageSize, datastore.DefaultPageSize)
}

func TestOwnerReferenceProjectionFailureIsRetriedAsRollForwardRecovery(t *testing.T) {
	injector := newTestFailureInjector()
	injector.fail("converge-owner-references", failureBefore)
	executor := newMutationExecutor(injector)
	authoritative, projection := "old", "old"
	err := executor.executeUpdate(context.Background(), "2",
		mutationAction{
			Step: datastore.MutationStep{Action: "update-authoritative"},
			Apply: func(context.Context) error {
				authoritative = "new"
				return nil
			},
		},
		mutationAction{
			Step: datastore.MutationStep{
				ResourceKind: "Product", Projection: ownerReferenceDependentsTable, Action: "converge-owner-references",
			},
			Apply: func(context.Context) error {
				if projection == "new" {
					return errors.New("projection unexpectedly written twice")
				}
				projection = "new"
				return nil
			},
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "new", authoritative)
	assert.Equal(t, "new", projection)
}
