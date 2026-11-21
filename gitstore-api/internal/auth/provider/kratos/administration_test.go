// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func TestHumanCreationReservesNativeIdentifiersWithoutStoringAPassword(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == http.MethodPost {
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.NotContains(t, body, "credentials")
		} else {
			require.Equal(t, http.MethodPatch, r.Method)
			var patch []map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&patch))
			require.Len(t, patch, 1)
			require.Equal(t, "add", patch[0]["op"])
			require.Equal(t, "/credentials/password", patch[0]["path"])
			password := patch[0]["value"].(map[string]any)
			require.Equal(t, []any{"alice", "alice@example.test"}, password["identifiers"])
			require.Empty(t, password["config"])
		}
		json.NewEncoder(w).Encode(identityFixture())
	}))
	defer upstream.Close()
	provider, err := New(directoryConfig(upstream.URL))
	require.NoError(t, err)
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "operator", AuthMethod: "static-users"})
	require.NoError(t, provider.UpsertProfile(ctx, &auth.UserProfile{Username: "alice", Email: "alice@example.test",
		Kind: auth.ProfileStaff, Lifecycle: auth.ActorInvited}))
	require.Equal(t, 2, calls)
}

func TestReservationConflictRollsBackOnlyTheIdentityCreatedByThisCall(t *testing.T) {
	var calls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method)
		switch r.Method {
		case http.MethodPost:
			json.NewEncoder(w).Encode(identityFixture())
		case http.MethodPatch:
			w.WriteHeader(http.StatusConflict)
		case http.MethodDelete:
			require.Equal(t, "/admin/identities/identity-uid", r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer upstream.Close()
	provider, err := New(directoryConfig(upstream.URL))
	require.NoError(t, err)
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "operator", AuthMethod: "static-users"})
	err = provider.UpsertProfile(ctx, &auth.UserProfile{Username: "alice", Email: "alice@example.test",
		Kind: auth.ProfileStaff, Lifecycle: auth.ActorInvited})
	require.ErrorIs(t, err, auth.ErrDirectoryConflict)
	require.Equal(t, []string{http.MethodPost, http.MethodPatch, http.MethodDelete}, calls)
}

func TestGitStorePatchAdvancesRevisionsAndStampsUpdateActor(t *testing.T) {
	var patched bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patched = true
			require.Equal(t, "application/json-patch+json", r.Header.Get("Content-Type"))
			var patch []map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&patch))
			values := map[string]any{}
			for _, operation := range patch {
				if operation["op"] == "test" {
					continue
				}
				values[operation["path"].(string)] = operation["value"]
			}
			require.Equal(t, "Renamed display", values["/traits/full_name"])
			require.Equal(t, "editor", values["/metadata_admin/update_actor"])
			metadata := values["/metadata_admin/gitstore"].(map[string]any)
			require.EqualValues(t, 2, metadata["generation"])
			require.Equal(t, "2", metadata["resource_version"])
			require.NotContains(t, values, "/traits/username")
			require.NotContains(t, values, "/metadata_admin/creation_actor")
		}
		json.NewEncoder(w).Encode(identityFixture())
	}))
	defer upstream.Close()
	provider, err := New(directoryConfig(upstream.URL))
	require.NoError(t, err)
	invalidations := 0
	provider.SetInvalidationHandler(func() { invalidations++ })
	profile, err := provider.GetBySubject(context.Background(), "identity-uid")
	require.NoError(t, err)
	profile.DisplayName = "Renamed display"
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "editor", AuthMethod: "static-users"})
	require.NoError(t, provider.UpsertProfile(ctx, profile))
	require.True(t, patched)
	require.Equal(t, 1, invalidations, "successful writes revoke local connection snapshots")
}

func TestGitStoreMutationRejectsRenameAndMissingInitiator(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		json.NewEncoder(w).Encode(identityFixture())
	}))
	defer upstream.Close()
	provider, err := New(directoryConfig(upstream.URL))
	require.NoError(t, err)
	profile, err := provider.GetBySubject(context.Background(), "identity-uid")
	require.NoError(t, err)
	require.Error(t, provider.UpsertProfile(context.Background(), profile))
	profile.Username = "replacement"
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "editor", AuthMethod: "static-users"})
	require.ErrorIs(t, provider.UpsertProfile(ctx, profile), ErrInvalidInput)
	profile.Username = "alice"
	profile.Metadata.Annotations = map[string]string{datastore.OwnerAnnotationKey: "another-owner"}
	require.ErrorIs(t, provider.UpsertProfile(ctx, profile), ErrInvalidInput)
}

func TestGroupAdministrationOnlyPatchesProtectedGroupAndRevisionFacts(t *testing.T) {
	record := identityFixture()
	patches := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patches++
			var patch []map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&patch))
			require.Len(t, patch, 3)
			metadata := record["metadata_admin"].(map[string]any)
			for _, item := range patch {
				path := item["path"].(string)
				require.Contains(t, []string{"/metadata_admin/groups", "/metadata_admin/gitstore", "/metadata_admin/update_actor"}, path)
				metadata[strings.TrimPrefix(path, "/metadata_admin/")] = item["value"]
			}
		}
		require.NoError(t, json.NewEncoder(w).Encode(record))
	}))
	defer upstream.Close()
	provider, err := New(directoryConfig(upstream.URL))
	require.NoError(t, err)
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "operator", AuthMethod: "static-users"})
	profile, err := provider.SetActorGroup(ctx, auth.ProfileStaff, "identity-uid", "editors", true)
	require.NoError(t, err)
	require.Contains(t, profile.Groups, "system:group:editors")
	require.EqualValues(t, 2, profile.Metadata.Generation)
	profile, err = provider.SetActorGroup(ctx, auth.ProfileStaff, "identity-uid", "editors", false)
	require.NoError(t, err)
	require.NotContains(t, profile.Groups, "system:group:editors")
	require.EqualValues(t, 3, profile.Metadata.Generation)
	for _, group := range []string{"", "system:staff", "system:group:editors", " editors "} {
		_, err = provider.SetActorGroup(ctx, auth.ProfileStaff, "identity-uid", group, true)
		require.ErrorIs(t, err, ErrInvalidInput)
	}
	require.Equal(t, 2, patches, "invalid groups must not mutate the directory")
}

func TestCustomerGroupsRemainMandatoryGlobalMemberships(t *testing.T) {
	record := identityFixture()
	record["schema_id"] = "customer"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method, "mandatory Customer membership cannot be overwritten per identity")
		require.NoError(t, json.NewEncoder(w).Encode(record))
	}))
	defer upstream.Close()
	cfg := directoryConfig(upstream.URL)
	cfg.CustomerGroups = []string{"shopper"}
	provider, err := New(cfg)
	require.NoError(t, err)
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "operator", AuthMethod: "static-users"})
	profile, err := provider.SetActorGroup(ctx, auth.ProfileCustomer, "identity-uid", "shopper", true)
	require.NoError(t, err)
	require.Equal(t, []string{"system:group:shopper", "system:customers"}, profile.Groups)
	_, err = provider.SetActorGroup(ctx, auth.ProfileCustomer, "identity-uid", "shopper", false)
	require.ErrorIs(t, err, ErrInvalidInput)
	_, err = provider.SetActorGroup(ctx, auth.ProfileCustomer, "identity-uid", "operators", true)
	require.ErrorIs(t, err, ErrInvalidInput)
}
