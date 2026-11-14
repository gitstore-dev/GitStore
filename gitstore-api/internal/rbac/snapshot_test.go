// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func snapshotFixture(t *testing.T) (rbac.SnapshotReader, rbac.SnapshotOrigin) {
	t.Helper()
	ds, err := memdb.New()
	require.NoError(t, err)
	store := ds.(datastore.RBACCapable).RBACStore()
	tier := uuid.NewString()
	projection := rbac.ProjectionIdentity{
		RepositoryID: uuid.NewString(), GitRef: "refs/heads/main",
		GitRevision: "main@sha1:" + strings.Repeat("1", 40), TreeDigest: strings.Repeat("2", 64), Actor: "alice",
	}
	_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{
		TierID: tier, Projection: &projection,
		Changes: []rbac.Change{{Resource: &rbac.Resource{
			APIVersion: rbac.APIVersion, Kind: rbac.Role, NamespaceUID: tier,
			Metadata: rbac.Metadata{Name: "reader", Namespace: "shop", UID: uuid.NewString()},
			Spec:     json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["read"]}]}`),
		}}},
	})
	require.NoError(t, err)
	origin := rbac.SnapshotOrigin{
		NamespaceUID: tier, Namespace: "shop", NamespaceVersion: "1", Active: true,
		RepositoryID: projection.RepositoryID, GitRef: projection.GitRef,
		GitRevision: projection.GitRevision, TreeDigest: projection.TreeDigest,
	}
	reader := rbac.SnapshotReader{Store: store, CatalogDigest: strings.Repeat("3", 64), BuiltinDigest: strings.Repeat("4", 64)}
	return reader, origin
}

func TestAuthoritativeSnapshotPinsCompleteIdentityAndOwnsResources(t *testing.T) {
	reader, origin := snapshotFixture(t)
	reader.ReadOrigin = func(context.Context, string) (rbac.SnapshotOrigin, error) { return origin, nil }
	first, err := reader.Read(t.Context(), origin.NamespaceUID)
	require.NoError(t, err)
	second, err := reader.Read(t.Context(), origin.NamespaceUID)
	require.NoError(t, err)
	require.Equal(t, first.ContentDigest, second.ContentDigest)
	require.Equal(t, rbac.ModelVersion, first.ModelVersion)
	require.Equal(t, origin, first.Origin)
	require.EqualValues(t, 1, first.State.Revision)
	require.Len(t, first.Resources, 1)
	first.Resources[0].Metadata.Name = "changed"
	require.Equal(t, "reader", second.Resources[0].Metadata.Name)
}

func TestAuthoritativeSnapshotRejectsRetiredTierEvenWithStaleLiveOrigin(t *testing.T) {
	reader, origin := snapshotFixture(t)
	reader.ReadOrigin = func(context.Context, string) (rbac.SnapshotOrigin, error) { return origin, nil }
	state, err := reader.Store.ReadRBACTier(t.Context(), origin.NamespaceUID)
	require.NoError(t, err)
	_, err = reader.Store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: origin.NamespaceUID,
		ExpectedRevision: state.Revision, RetireNamespace: true})
	require.NoError(t, err)
	_, err = reader.Read(t.Context(), origin.NamespaceUID)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
}

func TestAuthoritativeSnapshotRejectsUnverifiedOrigin(t *testing.T) {
	for _, invalid := range []string{"terminating", "recreated", "repository", "ref", "git", "tree", "catalog", "builtin"} {
		t.Run(invalid, func(t *testing.T) {
			reader, origin := snapshotFixture(t)
			tier := origin.NamespaceUID
			switch invalid {
			case "terminating":
				origin.Active = false
			case "recreated":
				origin.NamespaceUID = uuid.NewString()
			case "repository":
				origin.RepositoryID = uuid.NewString()
			case "ref":
				origin.GitRef = "refs/heads/review"
			case "git":
				origin.GitRevision = "main@sha1:" + strings.Repeat("5", 40)
			case "tree":
				origin.TreeDigest = strings.Repeat("6", 64)
			case "catalog":
				reader.CatalogDigest = ""
			case "builtin":
				reader.BuiltinDigest = ""
			}
			reader.ReadOrigin = func(context.Context, string) (rbac.SnapshotOrigin, error) { return origin, nil }
			snapshot, err := reader.Read(t.Context(), tier)
			require.ErrorIs(t, err, rbac.ErrUnavailable)
			require.Empty(t, snapshot.Resources)
			require.Empty(t, snapshot.ContentDigest)
		})
	}
}

func TestAuthoritativeSnapshotRetriesLifecycleChangeAndHonorsCancellation(t *testing.T) {
	reader, origin := snapshotFixture(t)
	calls := 0
	reader.ReadOrigin = func(context.Context, string) (rbac.SnapshotOrigin, error) {
		calls++
		if calls >= 2 {
			origin.NamespaceVersion = "2"
		}
		return origin, nil
	}
	snapshot, err := reader.Read(t.Context(), origin.NamespaceUID)
	require.NoError(t, err)
	require.Equal(t, 4, calls)
	require.Equal(t, "2", snapshot.Origin.NamespaceVersion)
	calls = 0
	reader.ReadOrigin = func(context.Context, string) (rbac.SnapshotOrigin, error) {
		calls++
		origin.NamespaceVersion = strings.Repeat("1", calls)
		return origin, nil
	}
	_, err = reader.Read(t.Context(), origin.NamespaceUID)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	require.Equal(t, 6, calls)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = reader.Read(ctx, origin.NamespaceUID)
	require.ErrorIs(t, err, context.Canceled)
}

func TestAuthoritativeSnapshotHonorsLowerRetryDeadlineAndSizeLimits(t *testing.T) {
	reader, origin := snapshotFixture(t)
	reader.Retries = 1
	calls := 0
	reader.ReadOrigin = func(context.Context, string) (rbac.SnapshotOrigin, error) {
		calls++
		current := origin
		current.NamespaceVersion = strings.Repeat("1", calls)
		return current, nil
	}
	_, err := reader.Read(t.Context(), origin.NamespaceUID)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	require.Equal(t, 2, calls, "a lower retry limit must not perform another identity scan")
	pages := &changingSnapshotPages{Store: reader.Store}
	reader.Store, reader.MaxBytes = pages, 1
	reader.ReadOrigin = func(context.Context, string) (rbac.SnapshotOrigin, error) { return origin, nil }
	_, err = reader.Read(t.Context(), origin.NamespaceUID)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	require.Zero(t, pages.calls, "declared oversized tiers must fail before listing resources")
	reader.MaxBytes, reader.Timeout = 0, 10*time.Millisecond
	reader.ReadOrigin = func(ctx context.Context, _ string) (rbac.SnapshotOrigin, error) {
		<-ctx.Done()
		return rbac.SnapshotOrigin{}, ctx.Err()
	}
	_, err = reader.Read(t.Context(), origin.NamespaceUID)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

type changingSnapshotPages struct {
	rbac.Store
	calls int
}

func (s *changingSnapshotPages) ListRBAC(ctx context.Context, request rbac.ListRequest) (rbac.Page, error) {
	s.calls++
	page, err := s.Store.ListRBAC(ctx, request)
	page.Revision++
	return page, err
}

func TestAuthoritativeSnapshotSharesSingleThreeAttemptBudget(t *testing.T) {
	reader, origin := snapshotFixture(t)
	pages := &changingSnapshotPages{Store: reader.Store}
	reader.Store = pages
	reader.ReadOrigin = func(context.Context, string) (rbac.SnapshotOrigin, error) { return origin, nil }
	_, err := reader.Read(t.Context(), origin.NamespaceUID)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	require.Equal(t, 3, pages.calls)
}
