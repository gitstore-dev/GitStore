// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func snapshotFixture(t testing.TB) (rbac.SnapshotReader, rbac.SnapshotOrigin) {
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

func rootSnapshotFixture(t testing.TB, count int) (rbac.SnapshotReader, rbac.SnapshotOrigin) {
	t.Helper()
	reader, origin := snapshotFixture(t)
	origin.Root = true
	reader.ReadOrigin = func(context.Context, string) (rbac.SnapshotOrigin, error) { return origin, nil }
	projection := rbac.ProjectionIdentity{
		RepositoryID: origin.RepositoryID, GitRef: origin.GitRef,
		GitRevision: origin.GitRevision, TreeDigest: origin.TreeDigest, Actor: "alice",
	}
	changes := make([]rbac.Change, count)
	for index := range changes {
		changes[index].Resource = &rbac.Resource{
			APIVersion: rbac.APIVersion, Kind: rbac.ClusterRole,
			Metadata: rbac.Metadata{Name: fmt.Sprintf("maximum-batch-%03d", index), UID: uuid.NewString(),
				Annotations: map[string]string{"capacity.gitstore.dev/padding": strings.Repeat("x", 16000)}},
			Spec: json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["read"]}]}`),
		}
	}
	_, err := reader.Store.ApplyRBAC(t.Context(), rbac.TierWrite{
		TierID: rbac.ClusterTierID, Projection: &projection, Changes: changes,
	})
	require.NoError(t, err)
	return reader, origin
}

func BenchmarkAuthoritativeSnapshotMaximumRootBatch(b *testing.B) {
	for _, cached := range []bool{false, true} {
		b.Run(fmt.Sprintf("cached=%t", cached), func(b *testing.B) {
			reader, _ := rootSnapshotFixture(b, rbac.MaxCandidateResources)
			if cached {
				reader.Validations = rbac.NewManifestValidationCache(rbac.NewMemoryBudget(rbac.ManifestValidationCacheBytes))
				b.Cleanup(func() { reader.Validations.Clear(true) })
			}
			_, err := reader.Read(b.Context(), rbac.ClusterTierID)
			require.NoError(b, err)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_, err := reader.Read(b.Context(), rbac.ClusterTierID)
				require.NoError(b, err)
			}
		})
	}
}

type mutatingSnapshotPages struct {
	rbac.Store
	mutate func(*rbac.Resource)
}

type capturedSnapshotStore struct {
	rbac.Store
	capture func(context.Context, string, int64) (datastore.RBACTierState, [][]byte, error)
}

func (s capturedSnapshotStore) CaptureRBACSnapshot(ctx context.Context, tier string, maxBytes int64) (datastore.RBACTierState, [][]byte, error) {
	return s.capture(ctx, tier, maxBytes)
}

func TestAuthoritativeSnapshotRetriesPendingProjectionWithinCaptureBudget(t *testing.T) {
	for _, pending := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("pending=%d", pending), func(t *testing.T) {
			reader, origin := snapshotFixture(t)
			reader.ReadOrigin = func(context.Context, string) (rbac.SnapshotOrigin, error) { return origin, nil }
			captured, err := datastore.ReadRBACSnapshot(t.Context(), reader.Store, origin.NamespaceUID)
			require.NoError(t, err)
			payload, err := json.Marshal(captured.Resources[0])
			require.NoError(t, err)
			attempts := 0
			reader.Store = capturedSnapshotStore{capture: func(context.Context, string, int64) (datastore.RBACTierState, [][]byte, error) {
				attempts++
				state := captured.State
				state.ProjectionPending = attempts <= pending
				return state, [][]byte{payload}, nil
			}}
			snapshot, err := reader.Read(t.Context(), origin.NamespaceUID)
			if pending == 3 {
				require.ErrorIs(t, err, rbac.ErrUnavailable)
				require.Equal(t, 3, attempts)
				attempts = 0
				candidate, err := reader.ReadCandidate(t.Context(), origin.NamespaceUID)
				require.NoError(t, err)
				require.True(t, candidate.State.ProjectionPending, "publication candidates must retain the repair signal")
				require.Equal(t, 1, attempts)
				return
			}
			require.NoError(t, err)
			require.False(t, snapshot.State.ProjectionPending)
			require.Equal(t, pending+1, attempts)
			require.Len(t, snapshot.Resources, 1)
		})
	}
}

func TestAuthoritativeSnapshotValidatesStableRawCaptureAfterLaterWrites(t *testing.T) {
	reader, origin := snapshotFixture(t)
	reader.ReadOrigin = func(context.Context, string) (rbac.SnapshotOrigin, error) { return origin, nil }
	captured, err := datastore.ReadRBACSnapshot(t.Context(), reader.Store, origin.NamespaceUID)
	require.NoError(t, err)
	payload, err := json.Marshal(captured.Resources[0])
	require.NoError(t, err)
	live := reader.Store
	reader.Store = capturedSnapshotStore{capture: func(ctx context.Context, tier string, maxBytes int64) (datastore.RBACTierState, [][]byte, error) {
		current, err := rbac.CloneResource(captured.Resources[0])
		require.NoError(t, err)
		current.Status = json.RawMessage(`{"observedGeneration":1}`)
		_, err = live.ApplyRBAC(ctx, rbac.TierWrite{
			TierID: tier, ExpectedRevision: captured.State.Revision,
			Changes: []rbac.Change{{Resource: current, ExpectedUID: current.Metadata.UID, ExpectedResourceVersion: current.Metadata.ResourceVersion}},
		})
		require.NoError(t, err)
		return captured.State, [][]byte{payload}, nil
	}}
	snapshot, err := reader.Read(t.Context(), origin.NamespaceUID)
	require.NoError(t, err)
	require.Equal(t, captured.State, snapshot.State, "later metadata cannot relabel a complete earlier capture")
	require.Equal(t, captured.Resources, snapshot.Resources)
	state, err := live.ReadRBACTier(t.Context(), origin.NamespaceUID)
	require.NoError(t, err)
	require.Greater(t, state.Revision, snapshot.State.Revision)
}

func TestRawSnapshotCaptureRetainsValidationAndSharedRetryBudget(t *testing.T) {
	reader, origin := snapshotFixture(t)
	source, err := datastore.ReadRBACSnapshot(t.Context(), reader.Store, origin.NamespaceUID)
	require.NoError(t, err)
	payload, err := json.Marshal(source.Resources[0])
	require.NoError(t, err)
	for _, change := range []string{"valid", "conflict", "invalid-json", "duplicate", "count", "bytes", "scope", "canceled"} {
		t.Run(change, func(t *testing.T) {
			state, payloads := source.State, [][]byte{payload}
			switch change {
			case "invalid-json":
				payloads[0] = []byte(`{`)
			case "duplicate":
				payloads = append(payloads, payload)
			case "count":
				state.RoleCount++
			case "bytes":
				state.PayloadBytes++
			case "scope":
				state.TierID = uuid.NewString()
			}
			calls := 0
			store := capturedSnapshotStore{capture: func(ctx context.Context, _ string, _ int64) (datastore.RBACTierState, [][]byte, error) {
				calls++
				if change == "conflict" {
					return state, nil, datastore.RBACError(rbac.ErrConflict)
				}
				if change == "canceled" {
					return state, nil, context.Canceled
				}
				return state, payloads, nil
			}}
			snapshot, err := datastore.ReadRBACSnapshot(t.Context(), store, origin.NamespaceUID)
			if change == "valid" {
				require.NoError(t, err)
				require.Equal(t, source, snapshot)
			} else {
				require.Error(t, err)
				require.Empty(t, snapshot.Resources)
			}
			if change == "conflict" {
				require.Equal(t, 3, calls, "capture retries cannot multiply the existing three-attempt budget")
			} else {
				require.Equal(t, 1, calls)
			}
		})
	}
}

func TestSnapshotCaptureRetriesRecheckLiveOriginBeforeReusingTreeProof(t *testing.T) {
	for _, fastCheck := range []bool{false, true} {
		t.Run(fmt.Sprint(fastCheck), func(t *testing.T) {
			reader, origin := snapshotFixture(t)
			source, err := datastore.ReadRBACSnapshot(t.Context(), reader.Store, origin.NamespaceUID)
			require.NoError(t, err)
			payload, err := json.Marshal(source.Resources[0])
			require.NoError(t, err)
			reads, checks, captures := 0, 0, 0
			reader.ReadOrigin = func(context.Context, string) (rbac.SnapshotOrigin, error) {
				reads++
				return origin, nil
			}
			if fastCheck {
				reader.CheckOrigin = func(_ context.Context, tier string, expected rbac.SnapshotOrigin) error {
					checks++
					require.Equal(t, origin.NamespaceUID, tier)
					require.Equal(t, origin, expected)
					return nil
				}
			}
			reader.Store = capturedSnapshotStore{capture: func(context.Context, string, int64) (datastore.RBACTierState, [][]byte, error) {
				captures++
				require.Equal(t, captures-1, checks+(reads-1), "each retry requires fresh live origin verification")
				if captures < 3 {
					return source.State, nil, datastore.RBACError(rbac.ErrConflict)
				}
				return source.State, [][]byte{payload}, nil
			}}
			snapshot, err := reader.ReadCandidate(t.Context(), origin.NamespaceUID)
			require.NoError(t, err)
			require.Equal(t, source.State, snapshot.State)
			require.Equal(t, source.Resources, snapshot.Resources)
			require.Equal(t, 3, captures)
			if fastCheck {
				require.Equal(t, 1, reads)
				require.Equal(t, 2, checks)
			} else {
				require.Equal(t, 3, reads)
			}
		})
	}
}

func TestSnapshotCaptureRetryOriginChangesRetainSharedBudgetAndProjectionPin(t *testing.T) {
	for _, change := range []string{"namespace-version", "canonical-revision", "canceled"} {
		t.Run(change, func(t *testing.T) {
			reader, original := snapshotFixture(t)
			source, err := datastore.ReadRBACSnapshot(t.Context(), reader.Store, original.NamespaceUID)
			require.NoError(t, err)
			payload, err := json.Marshal(source.Resources[0])
			require.NoError(t, err)
			current := original
			reads, checks, captures := 0, 0, 0
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reader.ReadOrigin = func(context.Context, string) (rbac.SnapshotOrigin, error) {
				reads++
				return current, nil
			}
			reader.CheckOrigin = func(_ context.Context, _ string, expected rbac.SnapshotOrigin) error {
				checks++
				require.Equal(t, original, expected)
				if change == "canceled" {
					cancel()
					return ctx.Err()
				}
				return rbac.ErrUnavailable
			}
			reader.Store = capturedSnapshotStore{capture: func(context.Context, string, int64) (datastore.RBACTierState, [][]byte, error) {
				captures++
				if captures == 1 {
					if change == "canonical-revision" {
						current.GitRevision = "main@sha1:" + strings.Repeat("5", 40)
					} else {
						current.NamespaceVersion = "2"
					}
					return source.State, nil, datastore.RBACError(rbac.ErrConflict)
				}
				return source.State, [][]byte{payload}, nil
			}}
			snapshot, err := reader.ReadCandidate(ctx, original.NamespaceUID)
			if change == "namespace-version" {
				require.NoError(t, err)
				require.Equal(t, current, snapshot.Origin)
			} else {
				require.Error(t, err)
				require.Empty(t, snapshot.Resources)
				if change == "canceled" {
					require.ErrorIs(t, err, context.Canceled)
				} else {
					require.ErrorIs(t, err, rbac.ErrSnapshotProjectionMismatch)
				}
			}
			require.Equal(t, 1, checks)
			if change == "canceled" {
				require.Equal(t, 1, reads)
				require.Equal(t, 1, captures)
			} else {
				require.Equal(t, 2, reads)
				require.Equal(t, 2, captures, "failed live identity check consumes one of the same three attempts")
			}
		})
	}
}

func TestRawSnapshotCaptureValidatesEveryPage(t *testing.T) {
	tier := uuid.NewString()
	state := datastore.RBACTierState{TierID: tier, Revision: 7, RoleCount: 1, BindingCount: 1000}
	payloads := make([][]byte, 0, state.RoleCount+state.BindingCount)
	for index := range state.RoleCount + state.BindingCount {
		kind := rbac.RoleBinding
		if index == 0 {
			kind = rbac.Role
		}
		payload, err := json.Marshal(rbac.Resource{
			APIVersion: rbac.APIVersion, Kind: kind,
			Metadata: rbac.Metadata{Name: fmt.Sprintf("resource-%04d", index), UID: uuid.NewString(), ResourceVersion: "7"},
			Spec:     json.RawMessage(`{}`),
		})
		require.NoError(t, err)
		payloads = append(payloads, payload)
		state.PayloadBytes += int64(len(payload))
	}
	store := capturedSnapshotStore{capture: func(context.Context, string, int64) (datastore.RBACTierState, [][]byte, error) {
		return state, payloads, nil
	}}
	snapshot, err := datastore.ReadRBACSnapshot(t.Context(), store, tier)
	require.NoError(t, err)
	require.Len(t, snapshot.Resources, 1001)
	require.Equal(t, "resource-1000", snapshot.Resources[1000].Metadata.Name)
	payloads[1000] = payloads[0]
	_, err = datastore.ReadRBACSnapshot(t.Context(), store, tier)
	require.ErrorIs(t, err, rbac.ErrUnavailable, "validation must not stop after the first captured page")
}

func (s *mutatingSnapshotPages) ReadRBACTier(ctx context.Context, tier string) (rbac.TierState, error) {
	state, err := s.Store.ReadRBACTier(ctx, tier)
	if err != nil || s.mutate == nil {
		return state, err
	}
	page, err := s.ListRBAC(ctx, rbac.ListRequest{TierID: tier, Limit: 1000})
	if err != nil {
		return state, err
	}
	state.PayloadBytes = 0
	for _, resource := range page.Resources {
		data, err := json.Marshal(resource)
		if err != nil {
			return state, err
		}
		state.PayloadBytes += int64(len(data))
	}
	return state, nil
}

func (s *mutatingSnapshotPages) ListRBAC(ctx context.Context, request rbac.ListRequest) (rbac.Page, error) {
	page, err := s.Store.ListRBAC(ctx, request)
	if err == nil && s.mutate != nil {
		for _, resource := range page.Resources {
			s.mutate(resource)
		}
	}
	return page, err
}

func TestAuthoritativeSnapshotValidationReusePreservesFreshSourceAndBounds(t *testing.T) {
	reader, origin := rootSnapshotFixture(t, 1)
	budget := rbac.NewMemoryBudget(rbac.ManifestValidationCacheBytes)
	reader.Validations = rbac.NewManifestValidationCache(budget)
	t.Cleanup(func() { reader.Validations.Clear(true) })
	pages := &mutatingSnapshotPages{Store: reader.Store}
	reader.Store = pages
	first, err := reader.Read(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	require.Positive(t, budget.Usage().Reserved)
	second, err := reader.Read(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	require.Equal(t, first.ContentDigest, second.ContentDigest)
	require.Equal(t, first.Resources, second.Resources, "validation reuse cannot strip source annotations")
	second.Resources[0].Metadata.Annotations["capacity.gitstore.dev/padding"] = "changed"
	third, err := reader.Read(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	require.Equal(t, first.Resources, third.Resources)

	now := time.Now().UTC()
	uid := uuid.NewString()
	pages.mutate = func(resource *rbac.Resource) {
		resource.Metadata.UID = uid
		resource.Metadata.DeletionTimestamp = &now
		resource.Status = json.RawMessage(`{"observedGeneration":1}`)
	}
	fresh, err := reader.Read(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	require.Equal(t, uid, fresh.Resources[0].Metadata.UID)
	require.Equal(t, now, *fresh.Resources[0].Metadata.DeletionTimestamp)
	require.JSONEq(t, `{"observedGeneration":1}`, string(fresh.Resources[0].Status))
	require.NotEqual(t, first.ContentDigest, fresh.ContentDigest)

	for _, invalid := range []string{"uid", "resource-version", "namespace", "annotation", "oversized", "spec"} {
		t.Run(invalid, func(t *testing.T) {
			pages.mutate = func(resource *rbac.Resource) {
				switch invalid {
				case "uid":
					resource.Metadata.UID = "invalid"
				case "resource-version":
					resource.Metadata.ResourceVersion = "0"
				case "namespace":
					resource.Metadata.Namespace = "other"
				case "annotation":
					resource.Metadata.Annotations["capacity.gitstore.dev/padding"] = "\xff"
				case "oversized":
					resource.Metadata.Annotations["capacity.gitstore.dev/padding"] = strings.Repeat("x", rbac.MaxManifestBytes)
				case "spec":
					resource.Spec = json.RawMessage(`{"rules":[{"resources":["nonexistent"],"verbs":["read"]}]}`)
				}
			}
			snapshot, err := reader.Read(t.Context(), rbac.ClusterTierID)
			require.ErrorIs(t, err, rbac.ErrUnavailable)
			require.Empty(t, snapshot.Resources)
		})
	}
	pages.mutate = nil
	origin.GitRevision = "main@sha1:" + strings.Repeat("5", 40)
	reader.ReadOrigin = func(context.Context, string) (rbac.SnapshotOrigin, error) { return origin, nil }
	_, err = reader.Read(t.Context(), rbac.ClusterTierID)
	require.ErrorIs(t, err, rbac.ErrSnapshotProjectionMismatch, "cached content never proves current Git identity")
}

func TestSnapshotCandidateDefersOnlyThePublicationOriginFence(t *testing.T) {
	reader, origin := snapshotFixture(t)
	reader.ReadOrigin = func(context.Context, string) (rbac.SnapshotOrigin, error) { return origin, nil }
	checks := 0
	reader.CheckOrigin = func(context.Context, string, rbac.SnapshotOrigin) error {
		checks++
		return rbac.ErrUnavailable
	}
	candidate, err := reader.ReadCandidate(t.Context(), origin.NamespaceUID)
	require.NoError(t, err, "candidate construction delegates the final live fence to publication")
	require.Len(t, candidate.Resources, 1)
	require.Zero(t, checks)
	_, err = reader.Read(t.Context(), origin.NamespaceUID)
	require.ErrorIs(t, err, rbac.ErrUnavailable, "authoritative reads must retain their own final live fence")
	require.Positive(t, checks)

	origin.GitRevision = "main@sha1:" + strings.Repeat("5", 40)
	_, err = reader.ReadCandidate(t.Context(), origin.NamespaceUID)
	require.ErrorIs(t, err, rbac.ErrSnapshotProjectionMismatch, "candidates still pin the exact canonical projection")
	origin.GitRevision = candidate.Origin.GitRevision
	reader.Store = &mutatingSnapshotPages{Store: reader.Store, mutate: func(resource *rbac.Resource) {
		resource.Metadata.UID = ""
	}}
	_, err = reader.ReadCandidate(t.Context(), origin.NamespaceUID)
	require.ErrorIs(t, err, rbac.ErrUnavailable, "candidates still require complete valid authoritative resources")
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
