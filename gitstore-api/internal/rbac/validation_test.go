// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/opa"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type validationProvider struct{ *opa.Engine }

func (*validationProvider) Name() string { return "opa" }
func (*validationProvider) Authorize(context.Context, *auth.Principal, string, auth.ResourceContext) (auth.Decision, error) {
	return auth.Deny("ordinary-cache", "deliberately stale fixture"), nil
}

func validationFixture(t *testing.T) (rbac.ValidationCapture, rbac.SnapshotOrigin, rbac.ResourceKey) {
	t.Helper()
	reader, origin := snapshotFixture(t)
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	reader.CatalogDigest, reader.BuiltinDigest = auth.AuthorizationCatalogDigest(), builtins.Digest()
	root := rbac.SnapshotOrigin{NamespaceUID: uuid.NewString(), Namespace: "root", NamespaceVersion: "1", Active: true, Root: true,
		RepositoryID: uuid.NewString(), GitRef: "refs/heads/main", GitRevision: "main@sha1:" + strings.Repeat("a", 40), TreeDigest: strings.Repeat("b", 64)}
	spec, err := json.Marshal(rbac.BindingSpec{RoleRef: rbac.RoleRef{Kind: rbac.ClusterRole, Name: "delegator"},
		Subjects: []rbac.Subject{{Kind: "User", Name: "alice"}}})
	require.NoError(t, err)
	key := rbac.ResourceKey{TierID: rbac.ClusterTierID, Kind: rbac.ClusterRoleBinding, Name: "alice-access"}
	_, err = reader.Store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: rbac.ClusterTierID,
		Projection: &rbac.ProjectionIdentity{RepositoryID: root.RepositoryID, GitRef: root.GitRef, GitRevision: root.GitRevision, TreeDigest: root.TreeDigest, Actor: "fixture"},
		Changes: []rbac.Change{
			{Resource: &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.ClusterRole,
				Metadata: rbac.Metadata{Name: "delegator", UID: uuid.NewString()},
				Spec:     json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["read"]}]}`)}},
			{Resource: &rbac.Resource{APIVersion: rbac.APIVersion, Kind: key.Kind,
				Metadata: rbac.Metadata{Name: key.Name, UID: uuid.NewString()}, Spec: spec}},
		}})
	require.NoError(t, err)
	reader.ReadOrigin = func(_ context.Context, tier string) (rbac.SnapshotOrigin, error) {
		if tier == rbac.ClusterTierID {
			return root, nil
		}
		return origin, nil
	}
	binding, err := reader.Store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	_, err = (rbac.References{Store: reader.Store, Builtins: builtins}).Resolve(t.Context(), key, binding.Metadata.UID, binding.Metadata.ResourceVersion)
	require.NoError(t, err)
	engine, err := opa.NewEngine(t.Context())
	require.NoError(t, err)
	return rbac.ValidationCapture{Reader: reader, Registry: auth.NewProviderRegistry(nil, &validationProvider{engine}, nil)}, origin, key
}

func TestValidationCaptureBypassesOrdinaryCacheAndPinsActorAndIdentity(t *testing.T) {
	capture, origin, _ := validationFixture(t)
	principal := &auth.Principal{Subject: "alice", AuthMethod: "static-users", Groups: []string{"system:authenticated"}}
	resource := auth.ResourceContext{Kind: "product", Name: "widget", Attrs: map[string]any{"namespace": "shop"}}
	cached, err := capture.Registry.AuthZ().Authorize(t.Context(), principal, "product.read", resource)
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeDeny, cached.Outcome)
	validation, err := capture.Begin(t.Context(), principal, origin.NamespaceUID)
	require.NoError(t, err)
	defer validation.Close()
	principal.Subject, principal.Groups[0] = "bob", "changed"
	decision, err := validation.Authorize(t.Context(), "product.read", resource)
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome)
	require.Len(t, decision.PolicyDigest, 64)
	snapshot, err := validation.Snapshot(origin.NamespaceUID)
	require.NoError(t, err)
	require.Equal(t, origin, snapshot.Origin)
	require.Empty(t, snapshot.Resources, "metadata readback must not expose mutable model rows")
	require.NotZero(t, snapshot.State.Revision)
	validation.Close()
	_, err = validation.Authorize(t.Context(), "product.read", resource)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
}

func TestValidationReviewCannotReplaceCapturedActor(t *testing.T) {
	capture, origin, _ := validationFixture(t)
	validation, err := capture.Begin(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"}, origin.NamespaceUID)
	require.NoError(t, err)
	defer validation.Close()
	resource := auth.ResourceContext{Kind: "product", Name: "widget", Attrs: map[string]any{"namespace": "shop"}}
	decision, err := validation.Review(t.Context(), &auth.Principal{Subject: "bob", AuthMethod: "access-review"}, "product.read", resource)
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeDeny, decision.Outcome)
	actor, err := validation.Authorize(t.Context(), "product.read", resource)
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, actor.Outcome)
	require.Equal(t, actor.AuthorizationSnapshot, decision.AuthorizationSnapshot)
}

func TestValidationCaptureAllowsChosenCrossRepositoryRevocationRace(t *testing.T) {
	capture, origin, key := validationFixture(t)
	principal := &auth.Principal{Subject: "alice", AuthMethod: "static-users"}
	first, err := capture.Begin(t.Context(), principal, origin.NamespaceUID)
	require.NoError(t, err)
	defer first.Close()
	binding, err := capture.Reader.Store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	state, err := capture.Reader.Store.ReadRBACTier(t.Context(), key.TierID)
	require.NoError(t, err)
	spec, err := json.Marshal(rbac.BindingSpec{RoleRef: rbac.RoleRef{Kind: rbac.ClusterRole, Name: "delegator"},
		Subjects: []rbac.Subject{{Kind: "User", Name: "bob"}}})
	require.NoError(t, err)
	binding.Spec = spec
	projection := state.Projection
	projection.GitRevision, projection.TreeDigest = "main@sha1:"+strings.Repeat("c", 40), strings.Repeat("d", 64)
	_, err = capture.Reader.Store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: key.TierID, ExpectedRevision: state.Revision,
		ExpectedGitRevision: state.Projection.GitRevision, Projection: &projection,
		Changes: []rbac.Change{{Resource: binding, ExpectedUID: binding.Metadata.UID, ExpectedResourceVersion: binding.Metadata.ResourceVersion}}})
	require.NoError(t, err)
	readOrigin := capture.Reader.ReadOrigin
	capture.Reader.ReadOrigin = func(ctx context.Context, tier string) (rbac.SnapshotOrigin, error) {
		value, err := readOrigin(ctx, tier)
		if tier == rbac.ClusterTierID {
			value.GitRevision, value.TreeDigest = projection.GitRevision, projection.TreeDigest
		}
		return value, err
	}
	resource := auth.ResourceContext{Kind: "product", Name: "widget", Attrs: map[string]any{"namespace": "shop"}}
	before, err := first.Authorize(t.Context(), "product.read", resource)
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, before.Outcome, "an independently committed revocation must not fence a prior capture")
	next, err := capture.Begin(t.Context(), principal, origin.NamespaceUID)
	require.NoError(t, err)
	defer next.Close()
	after, err := next.Authorize(t.Context(), "product.read", resource)
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeDeny, after.Outcome)
	require.NotEqual(t, before.PolicyDigest, after.PolicyDigest)
}

func TestValidationCaptureRejectsProjectionMismatchAndExpiredAttempts(t *testing.T) {
	capture, origin, _ := validationFixture(t)
	readOrigin := capture.Reader.ReadOrigin
	capture.Reader.ReadOrigin = func(ctx context.Context, tier string) (rbac.SnapshotOrigin, error) {
		value, err := readOrigin(ctx, tier)
		value.GitRevision = "main@sha1:" + strings.Repeat("f", 40)
		return value, err
	}

	principal := &auth.Principal{Subject: "alice", AuthMethod: "static-users"}
	validation, err := capture.Begin(t.Context(), principal, origin.NamespaceUID)
	require.Nil(t, validation)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	capture.Reader.ReadOrigin = readOrigin
	ctx, cancel := context.WithCancel(t.Context())
	validation, err = capture.Begin(ctx, principal, origin.NamespaceUID)
	require.NoError(t, err)
	defer validation.Close()
	cancel()
	_, err = validation.Authorize(t.Context(), "product.read", auth.ResourceContext{Kind: "product"})
	require.ErrorIs(t, err, context.Canceled)
	validation, err = capture.Begin(ctx, principal, origin.NamespaceUID)
	require.Nil(t, validation)
	require.ErrorIs(t, err, context.Canceled)
}

func TestValidationCaptureDeadlineCannotBeReused(t *testing.T) {
	for _, principalExpiry := range []bool{false, true} {
		capture, origin, _ := validationFixture(t)
		deadline := time.Now().Add(time.Second)
		ctx := t.Context()
		principal := &auth.Principal{Subject: "alice", AuthMethod: "static-users"}
		if principalExpiry {
			principal.ExpiresAt = deadline
		} else {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, deadline)
			defer cancel()
		}
		validation, err := capture.Begin(ctx, principal, origin.NamespaceUID)
		require.NoError(t, err)
		defer validation.Close()
		timer := time.NewTimer(time.Until(deadline))
		<-timer.C
		require.Eventually(t, func() bool {
			return validation.Check(t.Context()) != nil
		}, time.Second, time.Millisecond)
		require.ErrorIs(t, validation.Check(t.Context()), context.DeadlineExceeded)
		_, err = validation.Authorize(t.Context(), "product.read", auth.ResourceContext{Kind: "product"})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		_, err = validation.Covers(t.Context(), auth.AuthorizationCoverageRequest{})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		reused, err := capture.Begin(ctx, principal, origin.NamespaceUID)
		require.Nil(t, reused)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		principal.ExpiresAt = time.Time{}
		fresh, err := capture.Begin(t.Context(), principal, origin.NamespaceUID)
		require.NoError(t, err)
		fresh.Close()
	}
}

func TestValidationCaptureSharesConfiguredScratchAndDeadline(t *testing.T) {
	capture, origin, _ := validationFixture(t)
	capture.Reader.MaxBytes = 1 << 20
	capture.Scratch = rbac.NewMemoryBudget(2 << 20)
	principal := &auth.Principal{Subject: "alice", AuthMethod: "static-users"}
	first, err := capture.Begin(t.Context(), principal, origin.NamespaceUID)
	require.NoError(t, err)
	t.Cleanup(first.Close)
	require.EqualValues(t, 2<<20, capture.Scratch.Used())
	_, err = capture.Begin(t.Context(), principal, origin.NamespaceUID)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	first.Close()
	require.Zero(t, capture.Scratch.Used())
	capture.Timeout = 10 * time.Millisecond
	capture.Reader.ReadOrigin = func(ctx context.Context, _ string) (rbac.SnapshotOrigin, error) {
		<-ctx.Done()
		return rbac.SnapshotOrigin{}, ctx.Err()
	}
	_, err = capture.Begin(t.Context(), principal, origin.NamespaceUID)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Zero(t, capture.Scratch.Used())
}

func TestValidationCaptureReservesBeforeScanningAndReclaimsOnCancellation(t *testing.T) {
	capture, origin, _ := validationFixture(t)
	capture.Scratch = rbac.NewMemoryBudget(2 * rbac.RebuildScratchLimit)
	readOrigin, scans := capture.Reader.ReadOrigin, 0
	capture.Reader.ReadOrigin = func(ctx context.Context, tier string) (rbac.SnapshotOrigin, error) {
		scans++
		return readOrigin(ctx, tier)
	}

	principal := &auth.Principal{Subject: "alice", AuthMethod: "static-users"}
	ctx, cancel := context.WithCancel(t.Context())
	first, err := capture.Begin(ctx, principal, origin.NamespaceUID)
	require.NoError(t, err)
	t.Cleanup(first.Close)
	require.EqualValues(t, 2*rbac.RebuildScratchLimit, capture.Scratch.Used())
	before := scans
	blocked, err := capture.Begin(t.Context(), principal, origin.NamespaceUID)
	require.Nil(t, blocked)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	require.Equal(t, before, scans, "saturated capture must perform no source scans")
	cancel()
	require.Eventually(t, func() bool { return capture.Scratch.Used() == 0 }, time.Second, time.Millisecond)
	first.Close()
	next, err := capture.Begin(t.Context(), principal, origin.NamespaceUID)
	require.NoError(t, err)
	next.Close()
	require.Zero(t, capture.Scratch.Used())
	capture.Reader.ReadOrigin = func(context.Context, string) (rbac.SnapshotOrigin, error) {
		return rbac.SnapshotOrigin{}, rbac.ErrUnavailable
	}
	failed, err := capture.Begin(t.Context(), principal, origin.NamespaceUID)
	require.Nil(t, failed)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	require.Zero(t, capture.Scratch.Used(), "failed scans must reclaim every reserved tier")
}
