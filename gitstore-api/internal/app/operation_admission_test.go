// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCustomerProfileCannotExecuteAdminOperations(t *testing.T) {
	for _, kind := range []auth.ProfileKind{auth.ProfileCustomer, auth.ProfileStaff, auth.ProfileServiceAccount, ""} {
		control := newOperationAdmission(config.DefaultEndpoint("admin"))
		ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{
			Subject: "identity", AuthMethod: "oidc-jwt", ProfileKind: kind,
			Groups: []string{"system:staff", "system:serviceaccounts", "system:masters", "system:group:operators"},
		})
		ctx = graphql.WithOperationContext(ctx, &graphql.OperationContext{})
		executed := false
		response := control.middleware(ctx, func(context.Context) graphql.ResponseHandler {
			executed = true
			return graphql.OneShot(&graphql.Response{Data: []byte("{}")})
		})(ctx)
		if kind == auth.ProfileCustomer {
			require.False(t, executed)
			require.Equal(t, "FORBIDDEN", response.Errors[0].Extensions["code"])
		} else {
			require.True(t, executed)
		}
	}
}

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
