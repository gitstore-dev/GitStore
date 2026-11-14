// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth_test

import (
	"context"
	"errors"
	"testing"

	authpkg "github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestOptionalAuthorizationCapabilitiesFailClosed(t *testing.T) {
	for _, provider := range []authpkg.AuthZProvider{
		&staticAuthZProvider{name: "legacy"},
		authpkg.NewDecisionLogger(&staticAuthZProvider{name: "legacy"}, zap.NewNop()),
	} {
		registry := authpkg.NewProviderRegistry(nil, provider, nil)
		pinned, err := registry.PinAuthorization(context.Background(), authpkg.AuthorizationModel{})
		require.Nil(t, pinned)
		require.ErrorIs(t, err, authpkg.ErrNotSupported)
		_, supported := registry.AuthorizationReadiness("cluster")
		require.False(t, supported)
		_, err = provider.Authorize(context.Background(), authpkg.Anonymous(), "product.read", authpkg.ResourceContext{})
		require.NoError(t, err, "optional capabilities must not change ordinary authorization")
	}
}

type pinnedProvider struct {
	staticAuthZProvider
	pinned authpkg.PinnedAuthorization
	err    error
	calls  int
}

func (p *pinnedProvider) PinAuthorization(_ context.Context, model authpkg.AuthorizationModel) (authpkg.PinnedAuthorization, error) {
	p.calls++
	if model.ModelVersion != "v1" {
		return nil, errors.New("wrong model")
	}
	return p.pinned, p.err
}

func (*pinnedProvider) AuthorizationReadiness(string) (authpkg.AuthorizationReadiness, bool) {
	return authpkg.AuthorizationReadiness{Ready: true, State: "Ready"}, true
}

type pinnedDecision struct {
	staticAuthZProvider
	closed bool
}

func (*pinnedDecision) AuthorizationSnapshot() (authpkg.AuthorizationSnapshot, bool) {
	return authpkg.AuthorizationSnapshot{Loaded: true, Digest: "pinned"}, true
}

func (p *pinnedDecision) Close() { p.closed = true }

func TestRegistryPinsOnlyItsSelectedProvider(t *testing.T) {
	pinned := &pinnedDecision{staticAuthZProvider: staticAuthZProvider{name: "first"}}
	first := &pinnedProvider{pinned: pinned}
	second := &pinnedProvider{}
	registry := authpkg.NewProviderRegistry(nil, authpkg.NewDecisionLogger(first, zap.NewNop()), nil)
	evaluation, err := registry.PinAuthorization(context.Background(), authpkg.AuthorizationModel{ModelVersion: "v1"})
	require.NoError(t, err)
	registry.Swap(nil, second, nil)
	require.Equal(t, "first", evaluation.Name())
	require.Equal(t, 1, first.calls)
	require.Zero(t, second.calls)
	coverage, err := authpkg.CheckAuthorizationCoverage(context.Background(), evaluation, authpkg.Anonymous(), authpkg.AuthorizationCoverageRequest{})
	require.False(t, coverage.Covered)
	require.ErrorIs(t, err, authpkg.ErrNotSupported, "a decision is not evidence of complete coverage")
	evaluation.Close()
	require.True(t, pinned.closed)
}

func TestPinRejectsMissingEvaluationAndDiscardsErroredResults(t *testing.T) {
	for _, tc := range []struct {
		pinned authpkg.PinnedAuthorization
		err    error
	}{
		{},
		{&pinnedDecision{}, errors.New("capture failed")},
	} {
		provider := &pinnedProvider{pinned: tc.pinned, err: tc.err}
		registry := authpkg.NewProviderRegistry(nil, provider, nil)
		evaluation, err := registry.PinAuthorization(context.Background(), authpkg.AuthorizationModel{ModelVersion: "v1"})
		require.Nil(t, evaluation)
		require.Error(t, err)
		if p, ok := tc.pinned.(*pinnedDecision); ok {
			require.True(t, p.closed, "discarded evaluations must release their budget")
		}
	}
}
