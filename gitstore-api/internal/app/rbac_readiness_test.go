// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/opa"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type pendingRuntimeStore struct {
	datastore.RBACStore
	pending atomic.Bool
}

func (s *pendingRuntimeStore) ReadRBACTier(ctx context.Context, tier string) (rbac.TierState, error) {
	state, err := s.RBACStore.ReadRBACTier(ctx, tier)
	state.ProjectionPending = s.pending.Load()
	return state, err
}

func TestRBACApplicationReadinessRequiresSourceProofAndCompleteResync(t *testing.T) {
	for _, tc := range []struct {
		name    string
		repairs uint64
	}{
		{name: "complete"},
		{name: "committed-index-repair", repairs: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := memdb.New()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
			rbacStore := store.(datastore.RBACCapable).RBACStore()
			snapshotStore := &pendingRuntimeStore{RBACStore: rbacStore}
			snapshotStore.pending.Store(tc.repairs != 0)
			builtins, err := rbac.ReleaseBuiltins()
			require.NoError(t, err)
			identity := rbac.ProjectionIdentity{RepositoryID: uuid.NewString(), GitRef: "refs/heads/main",
				GitRevision: "main@sha1:" + strings.Repeat("a", 40), TreeDigest: strings.Repeat("b", 64), Actor: "fixture"}
			_, err = rbacStore.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: rbac.ClusterTierID, Projection: &identity})
			require.NoError(t, err)
			origin := rbac.SnapshotOrigin{NamespaceUID: uuid.NewString(), Namespace: "gitstore-system", NamespaceVersion: "1",
				Active: true, Root: true, RepositoryID: identity.RepositoryID, GitRef: identity.GitRef,
				GitRevision: identity.GitRevision, TreeDigest: identity.TreeDigest}
			var fail atomic.Bool
			var repairs atomic.Uint64
			reader := rbac.SnapshotReader{Store: snapshotStore, CatalogDigest: auth.AuthorizationCatalogDigest(), BuiltinDigest: builtins.Digest(),
				ReadOrigin: func(context.Context, string) (rbac.SnapshotOrigin, error) {
					if fail.Load() {
						return rbac.SnapshotOrigin{}, rbac.ErrUnavailable
					}
					return origin, nil
				}}
			limits := config.OPAConfig{MaxStaleness: 5 * time.Second, ValidationTimeout: 30 * time.Second,
				RebuildWorkers: 1, DirtyQueueCapacity: 8, SnapshotRetries: 1, SnapshotMemorySize: "16MiB", RebuildMemorySize: "1MiB"}
			runtime, err := newRBACRuntime(t.Context(), store, journal, reader, limits, func(_ context.Context, _ string, expected rbac.SnapshotOrigin) error {
				if fail.Load() || expected != origin {
					return rbac.ErrUnavailable
				}
				return nil
			}, func(context.Context, string) error {
				repairs.Add(1)
				snapshotStore.pending.Store(false)
				return nil
			})
			require.NoError(t, err)
			t.Cleanup(runtime.Shutdown)
			router := healthHandler(gin.New(), store, zap.NewNop(), apiruntime.SystemClock{}, nil, "", nil, runtime.Ready)
			status := func(path string) int {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				return response.Code
			}
			require.Equal(t, http.StatusOK, status("/health"))
			require.Equal(t, http.StatusServiceUnavailable, status("/ready"))
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			require.ErrorIs(t, waitRBACReady(ctx, runtime), context.DeadlineExceeded)
			bounds, err := journal.Bounds(t.Context())
			require.NoError(t, err)
			lease, acquired, err := journal.AcquireLease(t.Context(), "owned-runtime-fixture", time.Now(), time.Minute)
			require.NoError(t, err)
			require.True(t, acquired)
			t.Cleanup(func() { require.NoError(t, journal.ReleaseLease(context.Background(), lease)) })
			source := func(name string, at time.Time) {
				t.Helper()
				require.NoError(t, journal.SaveProgress(t.Context(), lease, datastore.ResourceCDCProgress{
					Source: name, StreamID: watchjournal.SourceReadFrontier, Position: []byte(bounds.Epoch), UpdatedAt: at,
				}))
			}
			for _, name := range []string{"Namespace", "Repository", "RBAC", "ServiceAccount"} {
				source(name, time.Now())
			}
			ctx, cancelReady := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancelReady()
			require.NoError(t, waitRBACReady(ctx, runtime))
			require.Equal(t, tc.repairs, repairs.Load(), "repair only proven pending indexes before publishing the current snapshot")
			require.Equal(t, http.StatusOK, status("/ready"))
			masters := &auth.Principal{Subject: "fixture-admin", AuthMethod: "static-users", Groups: []string{"system:masters"}}
			decision, err := runtime.provider.Authorize(t.Context(), masters, "namespace.read", auth.ResourceContext{Kind: "namespace"})
			require.NoError(t, err)
			require.Equal(t, auth.OutcomeAllow, decision.Outcome)
			capture := runtime.validationCapture(auth.NewProviderRegistry(nil, runtime.provider, nil))
			reservation, err := runtime.scratch.Reserve(2 << 20)
			require.NoError(t, err)
			_, err = capture.Begin(t.Context(), masters, "")
			require.ErrorIs(t, err, rbac.ErrUnavailable, "authoritative capture must share the runtime's scratch budget")
			reservation.Close()
			validation, err := capture.Begin(t.Context(), masters, "")
			require.NoError(t, err)
			validation.Close()
			source("RBAC", time.Now().Add(-6*time.Second))
			require.Eventually(t, func() bool { return status("/ready") == http.StatusServiceUnavailable }, time.Second, 5*time.Millisecond)
			_, err = runtime.provider.Authorize(t.Context(), masters, "namespace.read", auth.ResourceContext{Kind: "namespace"})
			require.Error(t, err)
			fail.Store(true)
			source("RBAC", time.Now())
			require.Never(t, func() bool { return runtime.Ready(t.Context()) == nil }, 200*time.Millisecond, 5*time.Millisecond)
			fail.Store(false)
			require.Eventually(t, func() bool { return status("/ready") == http.StatusOK }, 3*time.Second, 5*time.Millisecond)
			require.Equal(t, tc.repairs, repairs.Load(), "unavailable canonical source must not trigger a projection repair scan")
			runtime.Shutdown()
			require.Equal(t, http.StatusServiceUnavailable, status("/ready"))
		})
	}
}

type canonicalRuntimeGit struct{ tip atomic.Value }

func (*canonicalRuntimeGit) ListFiles(context.Context, string, string, string) ([]string, error) {
	return nil, nil
}

func (*canonicalRuntimeGit) ReadFile(context.Context, string, string, string) ([]byte, error) {
	return nil, datastore.ErrNotFound
}

func (g *canonicalRuntimeGit) ResolveRef(ctx context.Context, _, _ string) (string, error) {
	return g.tip.Load().(string), ctx.Err()
}

func TestCanonicalRBACRuntimeRepairsLostCallbacksBeforeRestoringReadiness(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, ensureBootstrapResources(t.Context(), store, &bootstrapGitProvisioner{},
		apiruntime.SystemClock{}, apiruntime.UUIDGenerator{}, zap.NewNop()))
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	bounds, err := journal.Bounds(t.Context())
	require.NoError(t, err)
	lease, acquired, err := journal.AcquireLease(t.Context(), "owned-canonical-runtime", time.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { require.NoError(t, journal.ReleaseLease(context.Background(), lease)) })
	refresh := func() {
		t.Helper()
		for _, name := range []string{"Namespace", "Repository", "RBAC", "ServiceAccount"} {
			require.NoError(t, journal.SaveProgress(t.Context(), lease, datastore.ResourceCDCProgress{
				Source: name, StreamID: watchjournal.SourceReadFrontier, Position: []byte(bounds.Epoch), UpdatedAt: time.Now(),
			}))
		}
	}
	refresh()
	git := &canonicalRuntimeGit{}
	git.tip.Store(strings.Repeat("a", 40))
	_, err = (rbac.OriginReader{Store: store, Git: git}).ReadOrigin(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	registry := auth.NewProviderRegistry(nil, nil, nil)
	server, err := cataloggrpc.NewServer(cataloggrpc.ServerDeps{Store: store, GitReader: git, Logger: zap.NewNop(), Registry: registry,
		RBACValidator: &admission.RBACValidator{Builtins: builtins, Capture: rbac.ValidationCapture{
			Registry: registry, Reader: rbac.SnapshotReader{MaxBytes: 1 << 20},
		}}})
	require.NoError(t, err)
	limits := config.OPAConfig{MaxStaleness: 5 * time.Second, ValidationTimeout: 30 * time.Second,
		RebuildWorkers: 1, DirtyQueueCapacity: 8, SnapshotRetries: 1, SnapshotMemorySize: "16MiB", RebuildMemorySize: "1MiB"}
	var hold atomic.Bool
	runtime, err := newCanonicalRBACRuntime(t.Context(), store, journal, git, limits, func(ctx context.Context, tier string) error {
		if hold.Load() {
			return rbac.ErrUnavailable
		}
		return server.ReconcileRBACTier(ctx, tier)
	})
	require.NoError(t, err)
	t.Cleanup(runtime.Shutdown)
	require.Eventually(t, func() bool { return runtime.Ready(t.Context()) == nil }, 2*time.Second, 5*time.Millisecond)
	hold.Store(true)
	git.tip.Store(strings.Repeat("b", 40))
	require.Eventually(t, func() bool { return runtime.Ready(t.Context()) != nil }, 2*time.Second, 5*time.Millisecond)
	require.Never(t, func() bool { return runtime.Ready(t.Context()) == nil }, 200*time.Millisecond, 5*time.Millisecond)
	refresh()
	hold.Store(false)
	require.Eventually(t, func() bool { return runtime.Ready(t.Context()) == nil }, 3*time.Second, 5*time.Millisecond)
	state, err := store.(datastore.RBACCapable).RBACStore().ReadRBACTier(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	require.Equal(t, "main@sha1:"+strings.Repeat("b", 40), state.Projection.GitRevision)
	require.Equal(t, "system:rbac-projection-repair", state.Projection.Actor)
}

func TestInitializedRBACRuntimeKeepsTrustedTrafficClosedUntilSeedPublication(t *testing.T) {
	store, git, _, _, _ := newRBACBootstrapFixture(t)
	git.unavailable.Store(true)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	bounds, err := journal.Bounds(t.Context())
	require.NoError(t, err)
	lease, acquired, err := journal.AcquireLease(t.Context(), "owned-bootstrap-runtime", time.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { require.NoError(t, journal.ReleaseLease(context.Background(), lease)) })
	refresh := func() {
		t.Helper()
		for _, name := range []string{"Namespace", "Repository", "RBAC", "ServiceAccount"} {
			require.NoError(t, journal.SaveProgress(t.Context(), lease, datastore.ResourceCDCProgress{
				Source: name, StreamID: watchjournal.SourceReadFrontier, Position: []byte(bounds.Epoch), UpdatedAt: time.Now(),
			}))
		}
	}
	refresh()
	limits := config.OPAConfig{InitialAdminSubject: "first-admin", MaxStaleness: 5 * time.Second, ValidationTimeout: 30 * time.Second,
		RebuildWorkers: 1, DirtyQueueCapacity: 8, SnapshotRetries: 1, SnapshotMemorySize: "16MiB", RebuildMemorySize: "1MiB"}
	runtime, err := newInitializedCanonicalRBACRuntime(t.Context(), store, journal, git, limits, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(runtime.Shutdown)
	router := healthHandler(gin.New(), store, zap.NewNop(), apiruntime.SystemClock{}, nil, "", nil, runtime.Ready)
	status := func(path string) int {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		return response.Code
	}
	require.Equal(t, http.StatusOK, status("/health"))
	require.Equal(t, http.StatusServiceUnavailable, status("/ready"))
	for _, principal := range []*auth.Principal{
		{Subject: "fixture-master", AuthMethod: "static-users", Groups: []string{"system:masters"}},
		{Subject: "system:api", AuthMethod: "internal"},
	} {
		_, err := runtime.provider.Authorize(t.Context(), principal, "namespace.read", auth.ResourceContext{Kind: "namespace"})
		require.ErrorIs(t, err, opa.ErrUnavailable)
	}
	git.unavailable.Store(false)
	refreshedAt := time.Now()
	require.Eventually(t, func() bool {
		if time.Since(refreshedAt) >= time.Second {
			refresh()
			refreshedAt = time.Now()
		}
		return status("/ready") == http.StatusOK
	}, 8*time.Second, 5*time.Millisecond)
	decision, err := runtime.provider.Authorize(t.Context(), &auth.Principal{Subject: "first-admin", AuthMethod: "static-users"},
		"namespace.create", auth.ResourceContext{Kind: "namespace"})
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome, "readiness must wait for the resolved seed's actual publication")
	runtime.Shutdown()
	require.Equal(t, http.StatusServiceUnavailable, status("/ready"))
}

type readinessCursorJournal struct {
	datastore.ResourceWatchJournal
	expired  atomic.Bool
	failures atomic.Uint64
}

func (j *readinessCursorJournal) ReadAfter(ctx context.Context, cursor datastore.ResourceWatchCursor, limit int) ([]datastore.ResourceWatchEvent, error) {
	if j.expired.Load() {
		j.failures.Add(1)
		return nil, &watchjournal.TerminalError{Code: watchjournal.CodeExpired, Reason: watchjournal.ReasonEpochMismatch}
	}
	return j.ResourceWatchJournal.ReadAfter(ctx, cursor, limit)
}

func TestRBACApplicationCursorLossRequiresResyncAndIsolatesNamespaceFailures(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	journal := &readinessCursorJournal{ResourceWatchJournal: store.(datastore.ResourceWatchCapable).ResourceWatchJournal()}
	rbacStore := store.(datastore.RBACCapable).RBACStore()
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	shop, other := uuid.NewString(), uuid.NewString()
	origins := make(map[string]rbac.SnapshotOrigin, 3)
	for tier, name := range map[string]string{rbac.ClusterTierID: "gitstore-system", shop: "shop", other: "other"} {
		identity := rbac.ProjectionIdentity{RepositoryID: uuid.NewString(), GitRef: "refs/heads/main",
			GitRevision: "main@sha1:" + strings.Repeat("a", 40), TreeDigest: strings.Repeat("b", 64), Actor: "fixture"}
		_, err = rbacStore.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: tier, Projection: &identity})
		require.NoError(t, err)
		uid := tier
		if tier == rbac.ClusterTierID {
			uid = uuid.NewString()
		}
		origins[tier] = rbac.SnapshotOrigin{NamespaceUID: uid, Namespace: name, NamespaceVersion: "1",
			Active: true, Root: tier == rbac.ClusterTierID, RepositoryID: identity.RepositoryID,
			GitRef: identity.GitRef, GitRevision: identity.GitRevision, TreeDigest: identity.TreeDigest}
	}
	var hold, failShop atomic.Bool
	var scans atomic.Uint64
	readOrigin := func(ctx context.Context, tier string) (rbac.SnapshotOrigin, error) {
		scans.Add(1)
		if hold.Load() || failShop.Load() && tier == shop || ctx.Err() != nil {
			return rbac.SnapshotOrigin{}, rbac.ErrUnavailable
		}
		origin, ok := origins[tier]
		if !ok {
			return rbac.SnapshotOrigin{}, rbac.ErrUnavailable
		}
		return origin, nil
	}
	reader := rbac.SnapshotReader{Store: rbacStore, CatalogDigest: auth.AuthorizationCatalogDigest(),
		BuiltinDigest: builtins.Digest(), ReadOrigin: readOrigin}
	limits := config.OPAConfig{MaxStaleness: 5 * time.Second, ValidationTimeout: 30 * time.Second,
		RebuildWorkers: 2, DirtyQueueCapacity: 8, SnapshotRetries: 1, SnapshotMemorySize: "16MiB", RebuildMemorySize: "1MiB"}
	runtime, err := newRBACRuntime(t.Context(), store, journal, reader, limits,
		func(ctx context.Context, tier string, expected rbac.SnapshotOrigin) error {
			actual, err := readOrigin(ctx, tier)
			if err != nil || actual != expected {
				return rbac.ErrUnavailable
			}
			return nil
		}, nil)
	require.NoError(t, err)
	t.Cleanup(runtime.Shutdown)
	bounds, err := journal.Bounds(t.Context())
	require.NoError(t, err)
	lease, acquired, err := journal.AcquireLease(t.Context(), "owned-cursor-runtime", time.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { require.NoError(t, journal.ReleaseLease(context.Background(), lease)) })
	refresh := func() {
		t.Helper()
		for _, name := range []string{"Namespace", "Repository", "RBAC", "ServiceAccount"} {
			require.NoError(t, journal.SaveProgress(t.Context(), lease, datastore.ResourceCDCProgress{
				Source: name, StreamID: watchjournal.SourceReadFrontier, Position: []byte(bounds.Epoch), UpdatedAt: time.Now(),
			}))
		}
	}
	refresh()
	require.NoError(t, runtime.manager.RequestTier(shop))
	require.NoError(t, runtime.manager.RequestTier(other))
	ready := func(tier string) bool {
		value, supported := runtime.provider.AuthorizationReadiness(tier)
		return supported && value.Ready
	}
	require.Eventually(t, func() bool {
		return runtime.Ready(t.Context()) == nil && ready(shop) && ready(other)
	}, 3*time.Second, 5*time.Millisecond)
	masters := &auth.Principal{Subject: "fixture-master", AuthMethod: "static-users", Groups: []string{"system:masters"}}
	product := func(namespace string) auth.ResourceContext {
		return auth.ResourceContext{Kind: "product", Name: "widget", Attrs: map[string]any{"namespace": namespace}}
	}
	journal.expired.Store(true)
	hold.Store(true)
	require.Eventually(t, func() bool {
		return journal.failures.Load() > 0 && runtime.Ready(t.Context()) != nil
	}, time.Second, 5*time.Millisecond)
	_, err = runtime.provider.Authorize(t.Context(), masters, "product.read", product("shop"))
	require.ErrorIs(t, err, opa.ErrUnavailable)
	journal.expired.Store(false)
	before := scans.Load()
	require.Never(t, func() bool { return runtime.Ready(t.Context()) == nil }, 200*time.Millisecond, 5*time.Millisecond,
		"a restored cursor alone cannot publish Ready before complete resync")
	hold.Store(false)
	refresh()
	require.Eventually(t, func() bool {
		return scans.Load() > before && runtime.Ready(t.Context()) == nil && ready(shop) && ready(other)
	}, 3*time.Second, 5*time.Millisecond)
	failShop.Store(true)
	require.Eventually(t, func() bool { return !ready(shop) }, 3*time.Second, 5*time.Millisecond,
		"live origin checking must invalidate only the failed namespace")
	_, err = runtime.provider.Authorize(t.Context(), masters, "product.read", product("shop"))
	require.ErrorIs(t, err, opa.ErrUnavailable, "a trusted principal cannot bypass a failed namespace")
	require.NoError(t, runtime.Ready(t.Context()), "one namespace's failed scan cannot invalidate a proven cluster tier")
	decision, err := runtime.provider.Authorize(t.Context(), masters, "product.read", product("other"))
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome)
	require.False(t, ready(shop))
	failShop.Store(false)
	require.NoError(t, runtime.manager.RequestTier(shop))
	refresh()
	require.Eventually(t, func() bool { return ready(shop) && ready(other) }, 3*time.Second, 5*time.Millisecond)
}
