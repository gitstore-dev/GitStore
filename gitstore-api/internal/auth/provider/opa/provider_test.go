// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package opa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/stretchr/testify/require"
)

func servingTier(t *testing.T, tier, namespace string, revision uint64, grant bool) auth.AuthorizationTier {
	t.Helper()
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	source := auth.AuthorizationTier{Namespace: namespace, NamespaceVersion: fmt.Sprint(revision), Active: true,
		CatalogDigest: auth.AuthorizationCatalogDigest(), BuiltinDigest: builtins.Digest(),
		State: rbac.TierState{TierID: tier, Revision: revision, Projection: rbac.ProjectionIdentity{
			GitRevision: fmt.Sprintf("main@sha1:%040x", revision), TreeDigest: fmt.Sprintf("%064x", revision)}}}
	if namespace != "" {
		role := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.Role, NamespaceUID: tier,
			Metadata: rbac.Metadata{Name: "reader", Namespace: namespace, UID: testUID(tier + "-reader")},
			Spec:     json.RawMessage(`{"rules":[]}`)}
		if grant {
			role.Spec = json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["read"]}]}`)
		}
		binding := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.RoleBinding, NamespaceUID: tier,
			Metadata: rbac.Metadata{Name: "access", Namespace: namespace, UID: testUID(tier + "-access")},
			Spec:     json.RawMessage(`{"roleRef":{"kind":"Role","name":"reader"},"subjects":[{"kind":"User","name":"alice"}]}`),
			Status:   json.RawMessage(`{"resolved":{"roleRef":{"uid":"` + role.Metadata.UID + `"}}}`)}
		source.Resources = []*rbac.Resource{role, binding}
		source.State.RoleCount, source.State.BindingCount = 1, 1
	}
	content, err := json.Marshal(source)
	require.NoError(t, err)
	digest := sha256.Sum256(content)
	source.ContentDigest = hex.EncodeToString(digest[:])
	return source
}

func TestNamespaceStorageCleanupRequiresCurrentClusterAuthority(t *testing.T) {
	provider, err := NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	require.NoError(t, provider.PublishTier(t.Context(), servingTier(t, rbac.ClusterTierID, "", 1, false)))
	actor := &auth.Principal{Subject: "system:api", Issuer: "gitstore-api", AuthMethod: "internal"}
	resource := auth.ResourceContext{Kind: "repository", Name: "gitstore-system",
		Attrs: map[string]any{"namespace": "retired", "namespaceCleanup": true}}
	decision, err := provider.Authorize(t.Context(), actor, auth.ActionRepositoryDelete, resource)
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome)
	require.Len(t, decision.AuthorizationSnapshot.Tiers, 1)
	require.Equal(t, rbac.ClusterTierID, decision.AuthorizationSnapshot.Tiers[0].TierID)
	require.Equal(t, "retired", resource.Attrs["namespace"], "cleanup must not erase the actual target namespace")

	_, err = provider.Authorize(t.Context(), actor, auth.ActionRepositoryContentsWrite, resource)
	require.ErrorIs(t, err, ErrUnavailable, "cleanup cannot authorize other repository operations")
	_, err = provider.Authorize(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"}, auth.ActionRepositoryDelete, resource)
	require.ErrorIs(t, err, ErrUnavailable, "ordinary callers cannot select cluster-only cleanup")
	resource.Attrs["namespaceCleanup"] = false
	_, err = provider.Authorize(t.Context(), actor, auth.ActionRepositoryDelete, resource)
	require.ErrorIs(t, err, ErrUnavailable, "ordinary deletion retains namespace freshness requirements")
	resource.Attrs["namespaceCleanup"] = true
	provider.InvalidateTier(rbac.ClusterTierID)
	_, err = provider.Authorize(t.Context(), actor, auth.ActionRepositoryDelete, resource)
	require.ErrorIs(t, err, ErrUnavailable, "cleanup cannot bypass unavailable cluster authority")
}

func TestProviderPinsExactEvaluatedPublicationAndAccountsRetiredReaders(t *testing.T) {
	provider, err := NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	cluster := servingTier(t, rbac.ClusterTierID, "", 1, false)
	namespace := servingTier(t, testUID("shop"), "shop", 1, true)
	require.NoError(t, provider.PublishTier(t.Context(), cluster))
	require.NoError(t, provider.PublishTier(t.Context(), namespace))
	resource := auth.ResourceContext{Kind: "product", Name: "widget", Attrs: map[string]any{"namespace": "shop"}}
	actor := &auth.Principal{Subject: "alice", AuthMethod: "static-users"}
	pinned, err := provider.pinServing(t.Context(), resource)
	require.NoError(t, err)
	t.Cleanup(pinned.Close)
	replacement := servingTier(t, testUID("shop"), "shop", 2, false)
	require.NoError(t, provider.PublishTier(t.Context(), replacement))
	withRetired := provider.budget.Used()
	old, err := pinned.Authorize(t.Context(), actor, "product.read", resource)
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, old.Outcome)
	require.EqualValues(t, 1, old.AuthorizationSnapshot.Tiers[1].Revision)
	current, err := provider.Authorize(t.Context(), actor, "product.read", resource)
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeDeny, current.Outcome)
	require.EqualValues(t, 2, current.AuthorizationSnapshot.Tiers[1].Revision)
	require.NotEqual(t, old.PolicyDigest, current.PolicyDigest)
	pinned.Close()
	pinned.Close()
	require.Less(t, provider.budget.Used(), withRetired)
	require.ErrorIs(t, provider.PublishTier(t.Context(), namespace), ErrUnavailable, "a regressing tier cannot replace newer data")
	provider.Close()
	require.Zero(t, provider.budget.Used())
}

func TestProviderReadinessPrecedesTrustedFastPathsAndNamespaceReuse(t *testing.T) {
	provider, err := NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	actor := &auth.Principal{Subject: "alice", AuthMethod: "static-users", Groups: []string{"system:masters"}}
	resource := auth.ResourceContext{Kind: "product", Name: "widget", Attrs: map[string]any{"namespace": "shop"}}
	decision, err := provider.Authorize(t.Context(), actor, "product.read", resource)
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorIs(t, err, auth.ErrAuthorizationUnavailable)
	require.Equal(t, auth.OutcomeDeny, decision.Outcome)
	require.NoError(t, provider.PublishTier(t.Context(), servingTier(t, rbac.ClusterTierID, "", 1, false)))
	_, err = provider.Authorize(t.Context(), actor, "product.read", resource)
	require.ErrorIs(t, err, ErrUnavailable, "a loaded cluster cannot imply an empty namespace tier")
	require.ErrorIs(t, err, auth.ErrAuthorizationUnavailable)
	source := servingTier(t, testUID("shop"), "shop", 1, true)
	require.NoError(t, provider.PublishTier(t.Context(), source))
	decision, err = provider.Authorize(t.Context(), actor, "unknown.read", resource)
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeDeny, decision.Outcome, "masters cannot allow unknown actions")
	recreated := servingTier(t, testUID("recreated-shop"), "shop", 1, true)
	require.ErrorIs(t, provider.PublishTier(t.Context(), recreated), ErrUnavailable, "UID replacement requires explicit old-incarnation invalidation")
	provider.InvalidateTier(source.State.TierID)
	readiness, ok := provider.AuthorizationReadiness(source.State.TierID)
	require.True(t, ok)
	require.False(t, readiness.Ready)
	_, err = provider.Authorize(t.Context(), actor, "product.read", resource)
	require.ErrorIs(t, err, ErrUnavailable)
	require.NoError(t, provider.PublishTier(t.Context(), recreated))
	decision, err = provider.Authorize(t.Context(), actor, "product.read", resource)
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome)
	require.Equal(t, recreated.State.TierID, decision.AuthorizationSnapshot.Tiers[1].TierID)
	recreated.Active = false
	provider.InvalidateTier(recreated.State.TierID)
	require.ErrorIs(t, provider.PublishTier(t.Context(), recreated), ErrUnavailable)
	_, err = provider.Authorize(t.Context(), actor, "product.read", resource)
	require.ErrorIs(t, err, ErrUnavailable, "terminating namespace data never becomes a grant")
}

func TestMutationCaptureFinishesWithoutGrantingNewRequestsOrScopes(t *testing.T) {
	provider, err := NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	cluster := servingTier(t, rbac.ClusterTierID, "", 1, false)
	namespace := servingTier(t, testUID("shop"), "shop", 1, true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx, closeCapture := auth.ContextWithServingAuthorizationCapture(ctx)
	defer closeCapture()
	actor := &auth.Principal{Subject: "alice", AuthMethod: "static-users"}
	resource := auth.ResourceContext{Kind: "product", Name: "widget", Attrs: map[string]any{"namespace": "shop"}}
	_, err = provider.Authorize(ctx, actor, "product.read", resource)
	require.ErrorIs(t, err, ErrUnavailable, "initial unavailability must not panic or poison later capture")
	require.NoError(t, provider.PublishTier(t.Context(), cluster))
	require.NoError(t, provider.PublishTier(t.Context(), namespace))
	decision, err := provider.Authorize(ctx, actor, "product.read", resource)
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome)
	provider.InvalidateTier(namespace.State.TierID)
	provider.InvalidateTier(cluster.State.TierID)
	decision, err = provider.Authorize(ctx, actor, "product.read", resource)
	require.NoError(t, err, "an admitted operation must survive invalidation caused by its own commit")
	require.Equal(t, auth.OutcomeAllow, decision.Outcome)
	require.EqualValues(t, 1, decision.AuthorizationSnapshot.Tiers[1].Revision)
	_, err = provider.Authorize(t.Context(), actor, "product.read", resource)
	require.ErrorIs(t, err, ErrUnavailable, "a new operation must remain fail closed")
	internal := &auth.Principal{Subject: "system:api", Issuer: "gitstore-api", AuthMethod: "internal"}
	decision, err = provider.Authorize(ctx, internal, "repository.contents.read", auth.ResourceContext{
		Kind: "repository", Attrs: map[string]any{"namespace": "shop"},
	})
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome, "post-commit verification uses the same captured publication")
	_, err = provider.Authorize(ctx, internal, "repository.contents.read", auth.ResourceContext{
		Kind: "repository", Attrs: map[string]any{"namespace": "another"},
	})
	require.ErrorIs(t, err, ErrUnavailable)
	other, err := NewProvider(t.Context())
	require.NoError(t, err)
	defer other.Close()
	_, err = other.Authorize(ctx, actor, "product.read", resource)
	require.ErrorIs(t, err, ErrUnavailable, "provider replacement must not inherit another provider's capture")
	cancel()
	_, err = provider.Authorize(ctx, actor, "product.read", resource)
	require.ErrorIs(t, err, context.Canceled)
	closeCapture()
	closeCapture()
	require.Zero(t, provider.budget.Used(), "closing the operation releases retired publications")
}

func TestProviderRootNamespaceRequiresOnlyTheProvenClusterPublication(t *testing.T) {
	provider, err := NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	require.NoError(t, provider.RequireOriginProgress(5*time.Second))
	resource := auth.ResourceContext{Kind: "repository", Name: "gitstore-system", Attrs: map[string]any{"namespace": "gitstore-system"}}
	internal := &auth.Principal{Subject: "system:api", Issuer: "gitstore-api", AuthMethod: "internal"}
	_, err = provider.Authorize(t.Context(), internal, "repository.contents.read", resource)
	require.ErrorIs(t, err, ErrUnavailable)
	source := servingTier(t, rbac.ClusterTierID, "", 1, false)
	require.NoError(t, provider.PublishTier(t.Context(), source))
	_, err = provider.Authorize(t.Context(), internal, "repository.contents.read", resource)
	require.ErrorIs(t, err, ErrUnavailable)
	require.NoError(t, provider.RecordOriginProgress(rbac.ClusterTierID, source.State.Projection, time.Now()))
	decision, err := provider.Authorize(t.Context(), internal, "repository.contents.read", resource)
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome)
	require.Len(t, decision.AuthorizationSnapshot.Tiers, 1)
	decision, err = provider.Authorize(t.Context(), &auth.Principal{Subject: "ordinary", AuthMethod: "static-users"},
		"repository.contents.read", resource)
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeDeny, decision.Outcome)
	provider.InvalidateTier(rbac.ClusterTierID)
	_, err = provider.Authorize(t.Context(), internal, "repository.contents.read", resource)
	require.ErrorIs(t, err, ErrUnavailable, "root scope is not a trusted-identity readiness bypass")
}

func TestProviderRequiresExplicitOriginProofBeforeInitialServing(t *testing.T) {
	provider, err := NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	require.NoError(t, provider.RequireOriginProgress(5*time.Second))
	source := servingTier(t, rbac.ClusterTierID, "", 1, false)
	require.NoError(t, provider.PublishTier(t.Context(), source))
	actor := &auth.Principal{Subject: "admin", AuthMethod: "static-users", Groups: []string{"system:masters"}}
	_, err = provider.Authorize(t.Context(), actor, "namespace.read", auth.ResourceContext{Kind: "namespace"})
	require.ErrorIs(t, err, ErrUnavailable, "translation time is not evidence that Git was checked")
	require.NoError(t, provider.RecordOriginProgress(rbac.ClusterTierID, source.State.Projection, time.Now()))
	decision, err := provider.Authorize(t.Context(), actor, "namespace.read", auth.ResourceContext{Kind: "namespace"})
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome)
}

func TestProviderOriginProofExpiresBeforeTrustedFastPathsAndCannotRefreshAnotherRevision(t *testing.T) {
	for _, expired := range []string{rbac.ClusterTierID, testUID("shop")} {
		t.Run(expired, func(t *testing.T) {
			provider, err := NewProvider(t.Context())
			require.NoError(t, err)
			t.Cleanup(provider.Close)
			require.NoError(t, provider.RequireOriginProgress(5*time.Second))
			cluster := servingTier(t, rbac.ClusterTierID, "", 1, false)
			namespace := servingTier(t, testUID("shop"), "shop", 1, true)
			require.NoError(t, provider.PublishTier(t.Context(), cluster))
			require.NoError(t, provider.PublishTier(t.Context(), namespace))
			require.NoError(t, provider.RecordOriginProgress(rbac.ClusterTierID, cluster.State.Projection, time.Now()))
			require.NoError(t, provider.RecordOriginProgress(namespace.State.TierID, namespace.State.Projection, time.Now()))
			resource := auth.ResourceContext{Kind: "product", Attrs: map[string]any{"namespace": "shop"}}
			actor := &auth.Principal{Subject: "admin", AuthMethod: "static-users", Groups: []string{"system:masters"}}
			provider.mu.Lock()
			provider.tiers[expired].originAt = time.Now().Add(-6 * time.Second)
			identity := provider.tiers[expired].source.State.Projection
			provider.mu.Unlock()
			_, err = provider.Authorize(t.Context(), actor, "product.read", resource)
			require.ErrorIs(t, err, ErrUnavailable)
			readiness, _ := provider.AuthorizationReadiness(namespace.State.TierID)
			require.False(t, readiness.Ready)
			wrong := identity
			wrong.GitRevision = "other"
			require.ErrorIs(t, provider.RecordOriginProgress(expired, wrong, time.Now()), ErrUnavailable)
			require.NoError(t, provider.RecordOriginProgress(expired, identity, time.Now()))
			decision, err := provider.Authorize(t.Context(), actor, "product.read", resource)
			require.NoError(t, err)
			require.Equal(t, auth.OutcomeAllow, decision.Outcome)
		})
	}
}

func TestProviderPrepareFailureAndDiscardReleaseReservations(t *testing.T) {
	provider, err := NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	cluster := servingTier(t, rbac.ClusterTierID, "", 1, false)
	require.NoError(t, provider.PublishTier(t.Context(), cluster))
	baseline := provider.budget.Used()
	prepared, err := provider.PrepareTier(t.Context(), servingTier(t, testUID("shop"), "shop", 1, true))
	require.NoError(t, err)
	require.Greater(t, provider.budget.Used(), baseline)
	prepared.Close()
	prepared.Close()
	require.Equal(t, baseline, provider.budget.Used())
	remainder, err := provider.budget.Reserve(rbac.TranslatedMemoryLimit - baseline)
	require.NoError(t, err)
	_, err = provider.PrepareTier(t.Context(), servingTier(t, testUID("shop"), "shop", 1, true))
	require.ErrorIs(t, err, ErrUnavailable)
	remainder.Close()
	require.Equal(t, baseline, provider.budget.Used(), "failed translation must not leak its partial reservation")
	pinned, err := provider.PinAuthorization(t.Context(), auth.AuthorizationModel{ModelVersion: rbac.ModelVersion, Cluster: cluster})
	require.NoError(t, err)
	require.Greater(t, provider.budget.Used(), baseline)
	pinned.Close()
	pinned.Close()
	require.Equal(t, baseline, provider.budget.Used())
}

func TestProviderBuiltinReleaseMismatchCannotReplaceAnInstalledSnapshot(t *testing.T) {
	provider, err := NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	first := servingTier(t, rbac.ClusterTierID, "", 1, false)
	require.NoError(t, provider.PublishTier(t.Context(), first))
	next := servingTier(t, rbac.ClusterTierID, "", 2, false)
	next.BuiltinDigest = fmt.Sprintf("%064x", 0)
	require.ErrorIs(t, provider.PublishTier(t.Context(), next), ErrUnavailable)
	decision, err := provider.Authorize(t.Context(), &auth.Principal{Subject: "fixture", AuthMethod: "static-users"},
		"namespace.read", auth.ResourceContext{Kind: "namespace"})
	require.NoError(t, err)
	require.EqualValues(t, 1, decision.AuthorizationSnapshot.Tiers[0].Revision)
}

func TestProviderConfiguredMemoryLimitFailsClosedWithoutRetainingFailedTranslation(t *testing.T) {
	provider, err := NewProviderWithMemoryLimit(t.Context(), 1024)
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	require.ErrorIs(t, provider.PublishTier(t.Context(), servingTier(t, rbac.ClusterTierID, "", 1, false)), ErrUnavailable)
	require.Zero(t, provider.budget.Used())
	ready, _ := provider.AuthorizationReadiness(rbac.ClusterTierID)
	require.False(t, ready.Ready)
}

func declaredNamespaceTier(t testing.TB, namespace string) auth.AuthorizationTier {
	t.Helper()
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	tier := testUID(namespace)
	source := auth.AuthorizationTier{Namespace: namespace, NamespaceVersion: "1", Active: true,
		CatalogDigest: auth.AuthorizationCatalogDigest(), BuiltinDigest: builtins.Digest(),
		State: rbac.TierState{TierID: tier, Revision: 1, RoleCount: 10, BindingCount: 50,
			Projection: rbac.ProjectionIdentity{GitRevision: fmt.Sprintf("main@sha1:%040x", 1)}}}
	for index := range 10 {
		name := fmt.Sprintf("role-%d", index)
		source.Resources = append(source.Resources, &rbac.Resource{APIVersion: rbac.APIVersion,
			Kind: rbac.Role, NamespaceUID: tier,
			Metadata: rbac.Metadata{Name: name, Namespace: namespace, UID: testUID(namespace + name)},
			Spec:     json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["read","create","update","delete"]}]}`)})
	}
	for index := range 50 {
		name, roleName := fmt.Sprintf("binding-%d", index), fmt.Sprintf("role-%d", index%10)
		spec, err := json.Marshal(rbac.BindingSpec{RoleRef: rbac.RoleRef{Kind: rbac.Role, Name: roleName},
			Subjects: []rbac.Subject{{Kind: "User", Name: fmt.Sprintf("subject-%d", index)}}})
		require.NoError(t, err)
		source.Resources = append(source.Resources, &rbac.Resource{APIVersion: rbac.APIVersion,
			Kind: rbac.RoleBinding, NamespaceUID: tier,
			Metadata: rbac.Metadata{Name: name, Namespace: namespace, UID: testUID(namespace + name)},
			Spec:     spec, Status: json.RawMessage(`{"resolved":{"roleRef":{"uid":"` + testUID(namespace+roleName) + `"}}}`)})
	}
	content, err := json.Marshal(source)
	require.NoError(t, err)
	digest := sha256.Sum256(content)
	source.ContentDigest = hex.EncodeToString(digest[:])
	return source
}

func TestProviderDeclaredNamespaceReservationFitsAggregateBudget(t *testing.T) {
	provider, err := NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	require.NoError(t, provider.PublishTier(t.Context(), servingTier(t, rbac.ClusterTierID, "", 1, false)))
	clusterBytes := provider.budget.Used()
	prepared, err := provider.PrepareTier(t.Context(), declaredNamespaceTier(t, "shop"))
	require.NoError(t, err)
	defer prepared.Close()
	namespaceBytes := provider.budget.Used() - clusterBytes
	require.LessOrEqual(t, namespaceBytes*1004+clusterBytes*2, int64(rbac.TranslatedMemoryLimit),
		"the declared 1,000-Namespace fixture must not exhaust the reservation budget before replacement headroom")
}

func BenchmarkProviderDeclaredNamespaceTranslation(b *testing.B) {
	provider, err := NewProvider(b.Context())
	require.NoError(b, err)
	defer provider.Close()
	source := declaredNamespaceTier(b, "shop")
	b.ResetTimer()
	for range b.N {
		prepared, err := provider.PrepareTier(b.Context(), source)
		require.NoError(b, err)
		b.ReportMetric(float64(provider.budget.Used()), "reserved-B")
		prepared.Close()
	}
	b.StopTimer()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var retained []*PreparedTier
	for range 16 {
		prepared, err := provider.PrepareTier(b.Context(), source)
		require.NoError(b, err)
		retained = append(retained, prepared)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(retained)
	if after.HeapAlloc >= before.HeapAlloc {
		b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/16, "retained-B/tier")
	}
	for _, prepared := range retained {
		prepared.Close()
	}
}

func TestProviderConcurrentPublicationAndEvaluationUseOneIdentity(t *testing.T) {
	provider, err := NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	require.NoError(t, provider.PublishTier(t.Context(), servingTier(t, rbac.ClusterTierID, "", 1, false)))
	require.NoError(t, provider.PublishTier(t.Context(), servingTier(t, testUID("shop"), "shop", 1, true)))
	resource := auth.ResourceContext{Kind: "product", Name: "widget", Attrs: map[string]any{"namespace": "shop"}}
	actor := &auth.Principal{Subject: "alice", AuthMethod: "static-users"}
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for range 40 {
				decision, err := provider.Authorize(t.Context(), actor, "product.read", resource)
				if err != nil {
					t.Errorf("evaluation failed: %v", err)
					return
				}
				revision := decision.AuthorizationSnapshot.Tiers[1].Revision
				if (revision%2 == 1) != (decision.Outcome == auth.OutcomeAllow) {
					t.Errorf("evaluated revision %d does not describe decision %v", revision, decision.Outcome)
				}
			}
		})
	}
	for revision := uint64(2); revision <= 12; revision++ {
		require.NoError(t, provider.PublishTier(t.Context(), servingTier(t, testUID("shop"), "shop", revision, revision%2 == 1)))
	}
	readers.Wait()
}

func TestProviderChecksCachedSourceExpiryBeforeTrustedFastPath(t *testing.T) {
	provider, err := NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	require.NoError(t, provider.PublishTier(t.Context(), servingTier(t, rbac.ClusterTierID, "", 1, false)))
	progress := watchjournal.NewReadiness(5 * time.Second)
	provider.SetProgressCheck(progress.Check)
	actor := &auth.Principal{Subject: "alice", AuthMethod: "static-users", Groups: []string{"system:masters"}}
	resource := auth.ResourceContext{Kind: "namespace", Name: "shop"}
	for _, status := range []watchjournal.MaterializerStatus{
		{},
		{JournalContinuous: true, LastProgressAt: time.Now().Add(-6 * time.Second)},
	} {
		progress.Update(status)
		decision, err := provider.Authorize(t.Context(), actor, "namespace.read", resource)
		require.ErrorIs(t, err, ErrUnavailable)
		require.Equal(t, auth.OutcomeDeny, decision.Outcome)
		readiness, ok := provider.AuthorizationReadiness(rbac.ClusterTierID)
		require.True(t, ok)
		require.False(t, readiness.Ready)
	}
	progress.Update(watchjournal.MaterializerStatus{JournalContinuous: true, LastProgressAt: time.Now()})
	decision, err := provider.Authorize(t.Context(), actor, "namespace.read", resource)
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome)
}

func TestProviderBoundsAuthoritativePinsAndReclaimsEvaluationSlots(t *testing.T) {
	provider, err := NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	provider.evaluating = make(chan struct{}, 2)
	cluster := servingTier(t, rbac.ClusterTierID, "", 1, false)
	require.NoError(t, provider.PublishTier(t.Context(), cluster))
	baseline := provider.budget.Used()
	var pins []auth.PinnedAuthorization
	t.Cleanup(func() {
		for _, pin := range pins {
			pin.Close()
		}
	})
	for range cap(provider.evaluating) {
		pin, err := provider.PinAuthorization(t.Context(), auth.AuthorizationModel{ModelVersion: rbac.ModelVersion, Cluster: cluster})
		require.NoError(t, err)
		pins = append(pins, pin)
	}
	_, err = provider.PinAuthorization(t.Context(), auth.AuthorizationModel{ModelVersion: rbac.ModelVersion, Cluster: cluster})
	require.ErrorIs(t, err, ErrUnavailable)
	_, err = provider.Authorize(t.Context(), nil, "namespace.read", auth.ResourceContext{})
	require.ErrorIs(t, err, ErrUnavailable)
	for _, pin := range pins {
		pin.Close()
	}
	require.Equal(t, baseline, provider.budget.Used())
	_, err = provider.Authorize(t.Context(), nil, "namespace.read", auth.ResourceContext{})
	require.NoError(t, err)
}
