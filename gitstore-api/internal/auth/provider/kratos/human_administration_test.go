// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/stretchr/testify/require"
)

func TestInvitationRequiresConfiguredDeliveryBeforeCreatingAnIdentity(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	provider, err := New(directoryConfig(server.URL))
	require.NoError(t, err)
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "operator", AuthMethod: "static-users"})
	err = provider.CreateStaff(ctx, &auth.UserProfile{Username: "alice", Email: "alice@example.test", Kind: auth.ProfileStaff})
	require.ErrorIs(t, err, auth.ErrNotSupported)
	require.Zero(t, requests)
}

func TestInvitationsDeliverPrivateSetupLinksWithoutAddingThemToProfiles(t *testing.T) {
	record := identityFixture()
	record["metadata_admin"].(map[string]any)["lifecycle_state"] = auth.ActorInvited
	record["metadata_admin"].(map[string]any)["setup_complete"] = false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/recovery/link" {
			json.NewEncoder(w).Encode(map[string]any{"recovery_link": "https://identity.example.test/setup?token=private-fixture-token"})
			return
		}
		if r.Method == http.MethodPatch {
			var patch []map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&patch))
			for _, operation := range patch {
				if operation["path"] == "/metadata_admin" {
					record["metadata_admin"] = operation["value"]
				}
			}
		}
		json.NewEncoder(w).Encode(record)
	}))
	defer server.Close()
	provider, err := New(directoryConfig(server.URL))
	require.NoError(t, err)
	delivered := 0
	provider.SetInvitationSender(func(_ context.Context, profile *auth.UserProfile, link string) error {
		require.Equal(t, "alice@example.test", profile.Email)
		require.True(t, len(link) > 0)
		delivered++
		return nil
	})
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "operator", AuthMethod: "static-users"})
	profile, err := provider.ResendInvitation(ctx, "identity-uid")
	require.NoError(t, err)
	require.Equal(t, 1, delivered)
	data, err := json.Marshal(profile)
	require.NoError(t, err)
	require.NotContains(t, string(data), "private-fixture-token")
	require.Equal(t, "2", profile.Metadata.ResourceVersion)
}

func TestHumanDeactivationRevokesNativeSessionsAndReactivationDoesNotBypassSetup(t *testing.T) {
	record := identityFixture()
	record["metadata_admin"].(map[string]any)["setup_complete"] = false
	record["metadata_admin"].(map[string]any)["lifecycle_state"] = auth.ActorInvited
	revoked := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPatch:
			var patch []map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&patch))
			for _, operation := range patch {
				switch operation["path"] {
				case "/state":
					record["state"] = operation["value"]
				case "/metadata_admin":
					record["metadata_admin"] = operation["value"]
				case "/metadata_admin/lifecycle_state":
					record["metadata_admin"].(map[string]any)["lifecycle_state"] = operation["value"]
				}
			}
		case http.MethodDelete:
			require.Equal(t, "/admin/identities/identity-uid/sessions", r.URL.Path)
			revoked++
			if revoked > 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		json.NewEncoder(w).Encode(record)
	}))
	defer server.Close()
	provider, err := New(directoryConfig(server.URL))
	require.NoError(t, err)
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "operator", AuthMethod: "static-users"})
	for _, state := range []auth.ActorLifecycleState{auth.ActorSuspended, auth.ActorInactive, auth.ActorActive} {
		profile, err := provider.SetHumanLifecycle(ctx, "identity-uid", state)
		require.NoError(t, err)
		want := state
		if state == auth.ActorActive {
			want = auth.ActorInvited
		}
		require.Equal(t, want, profile.Lifecycle)
		require.False(t, profile.Active)
		require.Equal(t, "operator", profile.Metadata.CreationActor)
	}
	require.Equal(t, 2, revoked)
}
