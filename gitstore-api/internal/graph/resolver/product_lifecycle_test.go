// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// productLifecycleFixture is deliberately resource-complete. Subsequent
// lifecycle tests use it to distinguish Git-owned provenance from system-owned
// deletion metadata without relying on production fixtures.
func productLifecycleFixture(namespace, name string) *datastore.Product {
	now := time.Now().UTC()
	return &datastore.Product{
		UID:               uuid.NewString(),
		Namespace:         namespace,
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Product",
		Generation:        1,
		ResourceVersion:   "1",
		CreationTimestamp: now,
		UpdateTimestamp:   now,
		RepositoryID:      uuid.NewString(),
		SourcePath:        "products/" + name + ".yaml",
		GitRef:            "refs/heads/main",
		GitCommitSHA:      "0123456789abcdef",
	}
}

// productLifecycleAdmitter models the shared committed-admission boundary: a
// delete is not a direct GraphQL datastore mutation, but a Git admission that
// atomically leaves the Product terminating for the controller finalizer.
type productLifecycleAdmitter struct {
	store datastore.Datastore
	calls []admission.CommittedManifestRequest
}

func (a *productLifecycleAdmitter) AdmitCommittedManifest(ctx context.Context, request admission.CommittedManifestRequest) (*admission.CommittedManifestResult, error) {
	a.calls = append(a.calls, request)
	if request.Operation == admission.OperationDelete {
		product, err := a.store.GetProductByName(ctx, request.Namespace, request.Name)
		if err != nil {
			return nil, err
		}
		if _, err := a.store.(datastore.ProductLifecycleStore).MarkProductTerminating(ctx, product.UID, product.ResourceVersion, "gitstore.dev/foreground-deletion", time.Now().UTC()); err != nil {
			return nil, err
		}
	}
	return &admission.CommittedManifestResult{Kind: "Product", Name: request.Name, CommitSHA: request.CommitSHA}, nil
}

func TestDeleteProductReturnsTerminatingEnvelopeAndIsIdempotent(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	product := productLifecycleFixture("acme", "widget")
	require.NoError(t, store.CreateProduct(context.Background(), product))
	writer := &repositoryLifecycleWriter{}
	admitter := &productLifecycleAdmitter{store: store}
	service, err := NewService(ServiceDeps{Store: store, GitWriter: writer, Logger: zap.NewNop(), CommittedManifestAdmitter: admitter})
	require.NoError(t, err)
	mutation := &mutationResolver{Resolver: &Resolver{service: service, logger: zap.NewNop()}}
	id := mustEncodeNodeID(nodeKindProduct, product.UID)

	first, err := mutation.DeleteProduct(context.Background(), model.DeleteProductInput{ID: &id})
	require.NoError(t, err)
	assert.Equal(t, model.ResourceDeletionOutcomeTerminationStarted, first.Outcome)
	require.NotNil(t, first.Product.Metadata.DeletionTimestamp)
	require.Len(t, admitter.calls, 1)
	assert.Equal(t, admission.OperationDelete, admitter.calls[0].Operation)
	assert.Equal(t, product.RepositoryID, admitter.calls[0].RepositoryID)

	second, err := mutation.DeleteProduct(context.Background(), model.DeleteProductInput{ID: &id})
	require.NoError(t, err)
	assert.Equal(t, model.ResourceDeletionOutcomeAlreadyTerminating, second.Outcome)
	assert.Len(t, admitter.calls, 1, "an already-terminating Product must not cascade a second Git delete")
}
