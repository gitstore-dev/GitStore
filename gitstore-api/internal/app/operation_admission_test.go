// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPrincipalBudgetOnlyExemptsVerifiedController(t *testing.T) {
	profile := config.DefaultEndpoint("admin")
	profile.Limits.PrincipalBurst = 1
	profile.Limits.PrincipalPerSecond = 0.00001
	control := newOperationAdmission(profile)
	for _, principal := range []*auth.Principal{
		{Subject: "alice", AuthMethod: "static-users"},
		{Subject: profile.ControllerSubject, AuthMethod: "oidc-jwt"},
		{Subject: profile.ControllerSubject, AuthMethod: "serviceaccount-jwt"},
		{Subject: "system:serviceaccount:other", AuthMethod: "serviceaccount-jwt", ServiceAccountUID: "other"},
	} {
		control := newOperationAdmission(profile)
		require.True(t, control.charge(principal, 1))
		require.False(t, control.charge(principal, 1), principal.Subject)
	}
	controller := &auth.Principal{Subject: profile.ControllerSubject, AuthMethod: "serviceaccount-jwt", ServiceAccountUID: "verified"}
	require.True(t, control.charge(controller, 1000))
	require.True(t, control.charge(controller, 1000))
}

func TestOperationCapacityIsPerPrincipalAndReleased(t *testing.T) {
	profile := config.DefaultEndpoint("admin")
	profile.Limits.WSOperations = 1
	control := newOperationAdmission(profile)
	release, ok := control.acquire("alice")
	require.True(t, ok)
	_, ok = control.acquire("alice")
	require.False(t, ok)
	other, ok := control.acquire("bob")
	require.True(t, ok)
	other()
	release()
	release()
	release, ok = control.acquire("alice")
	require.True(t, ok)
	release()
	require.Empty(t, control.active)
}
