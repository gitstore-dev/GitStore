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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/stretchr/testify/require"
)

func maximumRootBatchTier(t testing.TB) Tier {
	t.Helper()
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	resources := make([]*rbac.Resource, rbac.MaxCandidateResources)
	for index := range resources {
		name := fmt.Sprintf("maximum-batch-%03d", index)
		resources[index] = &rbac.Resource{
			APIVersion: rbac.APIVersion, Kind: rbac.ClusterRole,
			Metadata: rbac.Metadata{Name: name, UID: testUID(name),
				Annotations: map[string]string{"capacity.gitstore.dev/padding": strings.Repeat("x", 16000)}},
			Spec: json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["read"]}]}`),
		}
	}
	require.NoError(t, rbac.ValidateCandidate(resources))
	return Tier{ID: rbac.ClusterTierID, Active: true, Resources: resources, Builtins: builtins}
}

func BenchmarkProviderMaximumRootBatchTranslation(b *testing.B) {
	tier := maximumRootBatchTier(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, err := Translate(tier)
		require.NoError(b, err)
	}
}

func BenchmarkProviderMaximumRootBatchPreparation(b *testing.B) {
	source := servingTier(b, rbac.ClusterTierID, "", 1, false)
	source.Resources = maximumRootBatchTier(b).Resources
	source.State.RoleCount = len(source.Resources)
	provider, err := NewProvider(b.Context())
	require.NoError(b, err)
	b.Cleanup(provider.Close)
	prepared, err := provider.PrepareTier(b.Context(), source)
	require.NoError(b, err)
	prepared.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		prepared, err := provider.PrepareTier(b.Context(), source)
		require.NoError(b, err)
		prepared.Close()
	}
}

func TestPaddedRootValidationCachePreservesCurrentAuthority(t *testing.T) {
	source := servingTier(t, rbac.ClusterTierID, "", 1, false)
	source.Resources = readTier(t, true).Resources
	source.Resources[0].Metadata.Annotations = map[string]string{"padding": strings.Repeat("x", 16000), "replacement": "\ufffd", "\ufffd": "note"}
	source.Resources[0].Metadata.Labels = map[string]string{"replacement": "\ufffd"}
	provider, err := NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	require.NoError(t, provider.PublishTier(t.Context(), source))
	principal := &auth.Principal{Subject: "alice", AuthMethod: "static-users"}
	resource := auth.ResourceContext{Kind: "product"}
	decision, err := provider.Authorize(t.Context(), principal, "product.read", resource)
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome)
	for _, invalid := range []string{"oversized-annotations", "invalid-utf8", "invalid-key", "invalid-label", "changed-name", "changed-spec"} {
		t.Run(invalid, func(t *testing.T) {
			changed := source
			role, err := rbac.CloneResource(source.Resources[0])
			require.NoError(t, err)
			changed.Resources = []*rbac.Resource{role, source.Resources[1]}
			switch invalid {
			case "oversized-annotations":
				role.Metadata.Annotations["padding"] = strings.Repeat("x", rbac.MaxManifestBytes)
			case "invalid-utf8":
				role.Metadata.Annotations["replacement"] = "\xff"
			case "invalid-key":
				delete(role.Metadata.Annotations, "\ufffd")
				role.Metadata.Annotations["\xff"] = "note"
			case "invalid-label":
				role.Metadata.Labels["replacement"] = "\xff"
			case "changed-name":
				role.Metadata.Name = "invalid name"
			case "changed-spec":
				role.Spec = json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["read"],"deny":true}]}`)
			}
			prepared, err := provider.PrepareTier(t.Context(), changed)
			if prepared != nil {
				prepared.Close()
			}
			require.ErrorIs(t, err, ErrUnavailable)
		})
	}
	for _, change := range []string{"unresolved-binding", "terminated-role", "recreated-role", "revoked-rule"} {
		t.Run(change, func(t *testing.T) {
			changed := source
			changed.Resources = make([]*rbac.Resource, len(source.Resources))
			for index, original := range source.Resources {
				changed.Resources[index], err = rbac.CloneResource(original)
				require.NoError(t, err)
			}
			switch change {
			case "unresolved-binding":
				changed.Resources[1].Status = nil
			case "terminated-role":
				now := time.Now().UTC()
				changed.Resources[0].Metadata.DeletionTimestamp = &now
			case "recreated-role":
				changed.Resources[0].Metadata.UID = testUID("recreated-root-role")
			case "revoked-rule":
				changed.Resources[0].Spec = json.RawMessage(`{"rules":[]}`)
			}
			source.State.Revision++
			changed.State.Revision = source.State.Revision
			require.NoError(t, provider.PublishTier(t.Context(), changed))
			decision, err := provider.Authorize(t.Context(), principal, "product.read", resource)
			require.NoError(t, err)
			require.Equal(t, auth.OutcomeDeny, decision.Outcome)
		})
	}
}

func TestPaddedRootValidationCacheYieldsSnapshotBudgetAndCloses(t *testing.T) {
	source := servingTier(t, rbac.ClusterTierID, "", 1, false)
	source.Resources = maximumRootBatchTier(t).Resources
	provider, err := NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	prepared, err := provider.PrepareTier(t.Context(), source)
	require.NoError(t, err)
	prepared.Close()
	require.Positive(t, provider.MemoryUsage().Reserved)
	require.LessOrEqual(t, provider.MemoryUsage().Reserved, int64(rbac.ManifestValidationCacheBytes))
	results := make(chan error, 4)
	for range 4 {
		go func() {
			prepared, err := provider.PrepareTier(t.Context(), source)
			if prepared != nil {
				prepared.Close()
			}
			results <- err
		}()
	}
	for range 4 {
		require.NoError(t, <-results)
	}
	for _, role := range source.Resources {
		role.Metadata.Annotations["changed"] = "note"
	}
	prepared, err = provider.PrepareTier(t.Context(), source)
	require.NoError(t, err)
	prepared.Close()
	require.LessOrEqual(t, provider.MemoryUsage().Reserved, int64(rbac.ManifestValidationCacheBytes))
	provider.Close()
	require.Zero(t, provider.MemoryUsage().Reserved)

	for _, role := range source.Resources {
		role.Metadata.Annotations = nil
	}
	uncached, err := NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(uncached.Close)
	prepared, err = uncached.PrepareTier(t.Context(), source)
	require.NoError(t, err)
	required := uncached.MemoryUsage().Reserved
	prepared.Close()
	uncached.Close()
	bounded, err := NewProviderWithMemoryLimit(t.Context(), required)
	require.NoError(t, err)
	t.Cleanup(bounded.Close)
	for _, role := range source.Resources {
		role.Metadata.Annotations = map[string]string{"padding": strings.Repeat("x", 16000)}
	}
	prepared, err = bounded.PrepareTier(t.Context(), source)
	require.NoError(t, err, "optional validation reuse must yield space needed by a complete publication")
	prepared.Close()
	require.LessOrEqual(t, bounded.MemoryUsage().PeakReserved, required)
}

func TestPreparedTierRebasesEquivalentSourceWithoutReplacingPolicyOrRenewingProof(t *testing.T) {
	provider, err := NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	require.NoError(t, provider.RequireOriginProgress(2*time.Second))
	source := servingTier(t, rbac.ClusterTierID, "", 1, false)
	source.Resources = readTier(t, true).Resources
	source.State.RoleCount, source.State.BindingCount = 1, 1
	prepared, err := provider.PrepareTier(t.Context(), source)
	require.NoError(t, err)
	t.Cleanup(prepared.Close)
	at := time.Now().Add(-time.Second)
	require.NoError(t, prepared.SetOriginProgress(at))
	policy := prepared.tier.data
	latest := source
	latest.State.Revision++
	latest.ContentDigest = strings.Repeat("c", 64)
	latest.Resources = append([]*rbac.Resource(nil), source.Resources...)
	latest.Resources[0], err = rbac.CloneResource(source.Resources[0])
	require.NoError(t, err)
	latest.Resources[0].Metadata.ResourceVersion = "2"
	updated := time.Now().UTC()
	latest.Resources[0].Metadata.UpdateTimestamp = &updated
	latest.Resources[0].Metadata.UpdateActor = "controller"
	latest.Resources[0].Metadata.Finalizers = []string{"gitstore.dev/owner-links"}
	latest.Resources[0].Metadata.OwnerReferences = json.RawMessage(`[{"uid":"owner"}]`)
	latest.Resources[0].Status = json.RawMessage(`{"observedGeneration":1}`)
	require.NoError(t, prepared.Rebase(t.Context(), latest))
	require.Same(t, policy, prepared.tier.data, "catch-up must retain the immutable policy prepared before the live proof")
	require.False(t, provider.originReady(prepared.tier, at.Add(2*time.Second+time.Nanosecond)),
		"metadata catch-up cannot extend the original proof's expiry")
	require.NoError(t, prepared.Install())
	snapshot, ok := provider.AuthorizationSnapshot()
	require.True(t, ok)
	require.Equal(t, uint64(2), snapshot.Tiers[0].Revision)
	require.Equal(t, latest.ContentDigest, snapshot.Tiers[0].ContentDigest)
	decision, err := provider.Authorize(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"},
		"product.read", auth.ResourceContext{Kind: "product"})
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome)
	latest.State.Revision++
	require.ErrorIs(t, prepared.Rebase(t.Context(), latest), ErrUnavailable, "installed publications remain immutable")
}

func TestPreparedTierRebaseRejectsChangedAuthorityOrIdentityWithoutMutation(t *testing.T) {
	for _, change := range []string{"revocation", "terminating-role", "replaced-role", "canonical", "scope-version", "pending", "retired", "old-revision", "unproved", "cancelled", "closed"} {
		t.Run(change, func(t *testing.T) {
			provider, err := NewProvider(t.Context())
			require.NoError(t, err)
			t.Cleanup(provider.Close)
			source := servingTier(t, rbac.ClusterTierID, "", 1, false)
			source.Resources = readTier(t, true).Resources
			source.State.RoleCount, source.State.BindingCount = 1, 1
			prepared, err := provider.PrepareTier(t.Context(), source)
			require.NoError(t, err)
			t.Cleanup(prepared.Close)
			if change != "unproved" {
				require.NoError(t, prepared.SetOriginProgress(time.Now()))
			}
			provider.validations.Clear(false)
			reserved := provider.MemoryUsage().Reserved
			latest := source
			latest.State.Revision++
			latest.ContentDigest = strings.Repeat("c", 64)
			ctx := t.Context()
			switch change {
			case "revocation":
				latest.Resources[1].Status = nil
			case "terminating-role":
				at := time.Now()
				latest.Resources[0].Metadata.DeletionTimestamp = &at
			case "replaced-role":
				latest.Resources[0].Metadata.UID = testUID("replacement")
			case "canonical":
				latest.State.Projection.GitRevision = "main@sha1:" + strings.Repeat("c", 40)
			case "scope-version":
				latest.NamespaceVersion = "2"
			case "pending":
				latest.State.ProjectionPending = true
			case "retired":
				latest.State.NamespaceRetired = true
			case "old-revision":
				latest.State.Revision = source.State.Revision
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "closed":
				provider.Close()
			}
			require.ErrorIs(t, prepared.Rebase(ctx, latest), ErrUnavailable)
			provider.validations.Clear(false)
			require.Equal(t, reserved, provider.MemoryUsage().Reserved, "failed catch-up must release its temporary allocation")
			if change == "closed" {
				require.ErrorIs(t, prepared.Install(), ErrUnavailable)
				return
			}
			require.NoError(t, prepared.Install())
			snapshot, ok := provider.AuthorizationSnapshot()
			require.True(t, ok)
			require.Equal(t, uint64(1), snapshot.Tiers[0].Revision)
			require.Equal(t, source.ContentDigest, snapshot.Tiers[0].ContentDigest)
		})
	}
}

func TestPreparedTierRebaseChargesItsTemporaryTranslationToTheExistingBudget(t *testing.T) {
	source := servingTier(t, rbac.ClusterTierID, "", 1, false)
	source.Resources = readTier(t, true).Resources
	source.State.RoleCount, source.State.BindingCount = 1, 1
	measured, err := NewProvider(t.Context())
	require.NoError(t, err)
	prepared, err := measured.PrepareTier(t.Context(), source)
	require.NoError(t, err)
	measured.validations.Clear(false)
	required := measured.MemoryUsage().Reserved
	prepared.Close()
	measured.Close()

	bounded, err := NewProviderWithMemoryLimit(t.Context(), required)
	require.NoError(t, err)
	t.Cleanup(bounded.Close)
	prepared, err = bounded.PrepareTier(t.Context(), source)
	require.NoError(t, err)
	t.Cleanup(prepared.Close)
	require.NoError(t, prepared.SetOriginProgress(time.Now()))
	bounded.validations.Clear(false)
	latest := source
	latest.State.Revision++
	latest.ContentDigest = strings.Repeat("c", 64)
	require.ErrorIs(t, prepared.Rebase(t.Context(), latest), ErrUnavailable)
	require.Equal(t, required, bounded.MemoryUsage().Reserved)
	require.LessOrEqual(t, bounded.MemoryUsage().PeakReserved, required)
	require.NoError(t, prepared.Install(), "a failed optional catch-up must leave the original preparation intact")
	snapshot, ok := bounded.AuthorizationSnapshot()
	require.True(t, ok)
	require.Equal(t, uint64(1), snapshot.Tiers[0].Revision)
}

func servingTier(t testing.TB, tier, namespace string, revision uint64, grant bool) auth.AuthorizationTier {
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

func TestProviderMissingNamespaceRequestsLoadWithAndWithoutCapture(t *testing.T) {
	for _, captured := range []bool{false, true} {
		t.Run(fmt.Sprint(captured), func(t *testing.T) {
			provider, err := NewProvider(t.Context())
			require.NoError(t, err)
			t.Cleanup(provider.Close)
			require.NoError(t, provider.PublishTier(t.Context(), servingTier(t, rbac.ClusterTierID, "", 1, false)))
			var requested []string
			provider.SetNamespaceLoader(func(name string) { requested = append(requested, name) })
			ctx := t.Context()
			if captured {
				var closeCapture func()
				ctx, closeCapture = auth.ContextWithServingAuthorizationCapture(ctx)
				defer closeCapture()
			}
			actor := &auth.Principal{Subject: "alice", AuthMethod: "static-users"}
			resource := auth.ResourceContext{Kind: "product", Name: "widget", Attrs: map[string]any{"namespace": "shop"}}
			decision, err := provider.Authorize(ctx, actor, "product.read", resource)
			require.ErrorIs(t, err, ErrUnavailable)
			require.Equal(t, auth.OutcomeDeny, decision.Outcome)
			require.Equal(t, []string{"shop"}, requested, "missing snapshots must be scheduled even for request-scoped captures")
			require.NoError(t, provider.PublishTier(t.Context(), servingTier(t, testUID("shop"), "shop", 1, true)))
			decision, err = provider.Authorize(ctx, actor, "product.read", resource)
			require.NoError(t, err)
			require.Equal(t, auth.OutcomeAllow, decision.Outcome)
		})
	}
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
			decision, err := provider.Authorize(t.Context(), actor, "product.read", resource)
			require.ErrorIs(t, err, ErrUnavailable)
			scope := "namespace"
			if expired == rbac.ClusterTierID {
				scope = "cluster"
			}
			require.Equal(t, "tier unavailable: "+scope+" origin expired", decision.Reason,
				"an expired proof must be distinguishable from a missing policy without disclosing its identity")
			readiness, _ := provider.AuthorizationReadiness(namespace.State.TierID)
			require.False(t, readiness.Ready)
			wrong := identity
			wrong.GitRevision = "other"
			require.ErrorIs(t, provider.RecordOriginProgress(expired, wrong, time.Now()), ErrUnavailable)
			require.NoError(t, provider.RecordOriginProgress(expired, identity, time.Now()))
			decision, err = provider.Authorize(t.Context(), actor, "product.read", resource)
			require.NoError(t, err)
			require.Equal(t, auth.OutcomeAllow, decision.Outcome)
		})
	}
}

func TestProviderOriginFailuresDistinguishMissingAndUnverifiedProofs(t *testing.T) {
	for _, scope := range []string{"cluster", "namespace"} {
		for _, state := range []string{"not installed", "not verified", "clock ahead"} {
			t.Run(scope+"/"+state, func(t *testing.T) {
				provider, err := NewProvider(t.Context())
				require.NoError(t, err)
				t.Cleanup(provider.Close)
				require.NoError(t, provider.RequireOriginProgress(5*time.Second))
				cluster := servingTier(t, rbac.ClusterTierID, "", 1, false)
				namespace := servingTier(t, testUID("shop"), "shop", 1, true)
				for _, source := range []auth.AuthorizationTier{cluster, namespace} {
					require.NoError(t, provider.PublishTier(t.Context(), source))
					require.NoError(t, provider.RecordOriginProgress(source.State.TierID, source.State.Projection, time.Now()))
				}
				target := namespace.State.TierID
				if scope == "cluster" {
					target = rbac.ClusterTierID
				}
				if state == "not installed" {
					provider.InvalidateTier(target)
				} else {
					provider.mu.Lock()
					at := time.Time{}
					if state == "clock ahead" {
						at = time.Now().Add(time.Hour)
					}
					provider.tiers[target].originAt = at
					provider.mu.Unlock()
				}
				actor := &auth.Principal{Subject: "admin", AuthMethod: "static-users", Groups: []string{"system:masters"}}
				resource := auth.ResourceContext{Kind: "product", Attrs: map[string]any{"namespace": "shop"}}
				decision, err := provider.Authorize(t.Context(), actor, "product.read", resource)
				require.ErrorIs(t, err, ErrUnavailable)
				require.ErrorIs(t, err, auth.ErrAuthorizationUnavailable)
				require.Equal(t, auth.OutcomeDeny, decision.Outcome)
				require.Equal(t, "tier unavailable: "+scope+" origin "+state, decision.Reason)
			})
		}
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

func TestProviderAuthoritativePinsReuseOnlyAuthorValidation(t *testing.T) {
	provider, err := NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	cluster := servingTier(t, rbac.ClusterTierID, "", 1, false)
	require.NoError(t, provider.PublishTier(t.Context(), cluster))
	baseline := provider.budget.Used()
	role := &rbac.Resource{
		APIVersion: rbac.APIVersion, Kind: rbac.ClusterRole,
		Metadata: rbac.Metadata{Name: "reader", UID: testUID("pinned-reader"),
			Annotations: map[string]string{"capacity.gitstore.dev/padding": strings.Repeat("x", 16000)}},
		Spec: json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["read"]}]}`),
	}
	cluster.Resources, cluster.State.RoleCount = []*rbac.Resource{role}, 1
	model := auth.AuthorizationModel{ModelVersion: rbac.ModelVersion, Cluster: cluster}
	pinned, err := provider.PinAuthorization(t.Context(), model)
	require.NoError(t, err)
	pinned.Close()
	cached := provider.budget.Used()
	require.Greater(t, cached, baseline, "author validation should remain cached after releasing the pin")
	pinned, err = provider.PinAuthorization(t.Context(), model)
	require.NoError(t, err)
	pinned.Close()
	require.Equal(t, cached, provider.budget.Used(), "repeated pins must share the bounded author-content cache")

	model.Cluster.State.ProjectionPending = true
	_, err = provider.PinAuthorization(t.Context(), model)
	require.ErrorIs(t, err, ErrUnavailable, "cached author content cannot establish projection readiness")
	model.Cluster.State.ProjectionPending = false
	role.Status = json.RawMessage(`{`)
	_, err = provider.PinAuthorization(t.Context(), model)
	require.ErrorIs(t, err, ErrUnavailable, "cached author content cannot validate current status")
	role.Status = nil
	role.Spec = json.RawMessage(`{"rules":[],"unknown":true}`)
	_, err = provider.PinAuthorization(t.Context(), model)
	require.ErrorIs(t, err, ErrUnavailable, "changed author content must be validated again")
	provider.validations.Clear(false)
	require.Equal(t, baseline, provider.budget.Used())
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
		name := fmt.Sprintf("capacity-role-%02d", index)
		rule := rbac.Rule{Resources: []string{"role", "product", "categoryTaxonomy"}, Verbs: []string{"read", "list"}}
		if index%2 == 1 {
			rule.When = []rbac.Condition{{Attr: "namespace", Op: "eq", Value: &namespace}}
		}
		spec, err := json.Marshal(rbac.RoleSpec{Rules: []rbac.Rule{rule}})
		require.NoError(t, err)
		source.Resources = append(source.Resources, &rbac.Resource{APIVersion: rbac.APIVersion,
			Kind: rbac.Role, NamespaceUID: tier,
			Metadata: rbac.Metadata{Name: name, Namespace: namespace, UID: testUID(namespace + name)},
			Spec:     spec})
	}
	for index := range 50 {
		name, roleName := fmt.Sprintf("capacity-binding-%02d", index), fmt.Sprintf("capacity-role-%02d", index%10)
		ref := rbac.RoleRef{Kind: rbac.Role, Name: roleName}
		subject := rbac.Subject{Kind: "User", Name: fmt.Sprintf("rbac-seed-fixture-filler-0001-%02d", index)}
		uid := testUID(namespace + roleName)
		switch index {
		case 1:
			subject.Kind, subject.Name = "Group", "system:group:capacity-readers"
		case 2:
			ref.Kind, ref.Name = rbac.ClusterRole, "view"
			role, err := builtins.Get(ref.Kind, ref.Name)
			require.NoError(t, err)
			uid = role.Metadata.UID
		case 3:
			ref.Name, uid = "capacity-unresolved", ""
		}
		spec, err := json.Marshal(rbac.BindingSpec{RoleRef: ref, Subjects: []rbac.Subject{subject}})
		require.NoError(t, err)
		var status json.RawMessage
		if uid != "" {
			status = json.RawMessage(`{"resolved":{"roleRef":{"uid":"` + uid + `"}}}`)
		}
		source.Resources = append(source.Resources, &rbac.Resource{APIVersion: rbac.APIVersion,
			Kind: rbac.RoleBinding, NamespaceUID: tier,
			Metadata: rbac.Metadata{Name: name, Namespace: namespace, UID: testUID(namespace + name)},
			Spec:     spec, Status: status})
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
	prepared, err := provider.PrepareTier(t.Context(), declaredNamespaceTier(t, "rbac-seed-fixture-0001"))
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
	source := declaredNamespaceTier(b, "rbac-seed-fixture-0001")
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

func BenchmarkProviderDeclaredNamespaceDecision(b *testing.B) {
	const namespace = "rbac-seed-fixture-0001"
	for _, scenario := range []struct {
		name      string
		principal *auth.Principal
		allowed   bool
	}{
		{"direct-user", &auth.Principal{Subject: "rbac-seed-fixture-filler-0001-00"}, true},
		{"conditional-group", &auth.Principal{Subject: "group-reader", Groups: []string{"system:group:capacity-readers"}}, true},
		{"builtin-view", &auth.Principal{Subject: "rbac-seed-fixture-filler-0001-02"}, true},
		{"unbound-user", &auth.Principal{Subject: "unbound-reader"}, false},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			scenario.principal.AuthMethod = "static-users"
			provider, err := NewProvider(b.Context())
			require.NoError(b, err)
			b.Cleanup(provider.Close)
			require.NoError(b, provider.PublishTier(b.Context(), servingTier(b, rbac.ClusterTierID, "", 1, false)))
			require.NoError(b, provider.PublishTier(b.Context(), declaredNamespaceTier(b, namespace)))
			resource := auth.ResourceContext{Kind: "product", Name: "widget", Attrs: map[string]any{"namespace": namespace}}
			expected := auth.OutcomeDeny
			if scenario.allowed {
				expected = auth.OutcomeAllow
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				decision, err := provider.Authorize(b.Context(), scenario.principal, auth.ActionProductList, resource)
				if err != nil || decision.Outcome != expected {
					b.Fatalf("prepared decision differs from the declared fixture: outcome=%v reason=%s error=%v",
						decision.Outcome, decision.Reason, err)
				}
			}
		})
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
