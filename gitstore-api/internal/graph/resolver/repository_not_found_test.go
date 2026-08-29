// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

const repositoryNotFoundTestNamespaceID = "01960000-0000-7000-8000-000000000140"

// lookupRepositoryErrStore wraps a Datastore and forces LookupRepository to
// return an arbitrary, non-ErrNotFound error, simulating a genuine failure
// (e.g. a Scylla timeout) rather than a missing repository.
type lookupRepositoryErrStore struct {
	datastore.Datastore
	err error
}

func (s *lookupRepositoryErrStore) LookupRepository(_ context.Context, _, _ string) (*datastore.NamespaceMapping, error) {
	return nil, s.err
}

func newRepositoryNotFoundHarness(t *testing.T, store datastore.Datastore) (*resolver.Resolver, *datastore.Namespace) {
	t.Helper()
	ctx := context.Background()
	namespace := &datastore.Namespace{
		UID:               repositoryNotFoundTestNamespaceID,
		Name:              "repository-not-found-contract",
		Tier:              datastore.NamespaceTierUser,
		CreationTimestamp: time.Date(2026, time.August, 29, 0, 0, 0, 0, time.UTC),
		CreationActor:     "fixture",
		UpdateActor:       "fixture",
	}
	require.NoError(t, store.CreateNamespace(ctx, namespace))

	root, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:  store,
		Logger: zap.NewNop(),
	})
	require.NoError(t, err)
	return root, namespace
}

// TestQueryRepositoryByNamespacePath_MissingRepositoryReportsNotFoundCode
// confirms the repository(by: namespacePath) query surfaces a genuinely
// missing repository as a GraphQL error carrying extensions.code
// "NOT_FOUND" — the contract gitstore-controller-manager's
// systemRepositoryExists relies on to distinguish "doesn't exist yet" from
// a hard failure (see EnsureSystemRepository, spec 046).
func TestQueryRepositoryByNamespacePath_MissingRepositoryReportsNotFoundCode(t *testing.T) {
	baseStore, err := memdb.New()
	require.NoError(t, err)
	root, namespace := newRepositoryNotFoundHarness(t, baseStore)

	_, err = root.Query().Repository(context.Background(), model.RepositoryBy{
		NamespacePath: &model.RepositoryNamespacePath{
			Namespace: namespace.Name,
			Name:      "gitstore-system",
		},
	})

	require.Error(t, err)
	var gqlErr *gqlerror.Error
	require.True(t, errors.As(err, &gqlErr), "expected *gqlerror.Error, got %T", err)
	assert.Equal(t, "repository not found", gqlErr.Message)
	assert.Equal(t, "NOT_FOUND", gqlErr.Extensions["code"])
}

// TestQueryRepositoryByNamespacePath_GenuineLookupFailurePropagates confirms
// that a non-ErrNotFound LookupRepository failure (e.g. a datastore
// timeout) is NOT mislabeled as "repository not found" / NOT_FOUND — it
// must propagate as a distinct error so callers do not mistake a real
// failure for "safe to create".
func TestQueryRepositoryByNamespacePath_GenuineLookupFailurePropagates(t *testing.T) {
	baseStore, err := memdb.New()
	require.NoError(t, err)
	genuineErr := errors.New("datastore: connection reset")
	store := &lookupRepositoryErrStore{Datastore: baseStore, err: genuineErr}
	root, namespace := newRepositoryNotFoundHarness(t, store)

	_, err = root.Query().Repository(context.Background(), model.RepositoryBy{
		NamespacePath: &model.RepositoryNamespacePath{
			Namespace: namespace.Name,
			Name:      "gitstore-system",
		},
	})

	require.Error(t, err)
	assert.NotEqual(t, "repository not found", err.Error())
	var gqlErr *gqlerror.Error
	if errors.As(err, &gqlErr) {
		assert.NotEqual(t, "NOT_FOUND", gqlErr.Extensions["code"])
	}
}
