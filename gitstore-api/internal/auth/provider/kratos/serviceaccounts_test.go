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
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/stretchr/testify/require"
)

func TestServiceAccountMirrorUsesCurrentSourceAndPreservesGroups(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	account := &datastore.ServiceAccount{UID: "00000000-0000-0000-0000-000000000065", Name: "ci-bot",
		ResourceVersion: "1", Generation: 1, CreationTimestamp: time.Now().UTC()}
	require.NoError(t, store.CreateServiceAccount(t.Context(), account))
	var mirror map[string]any
	var patches []map[string]any
	var afterPatch func()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			require.Equal(t, "/admin/identities/by/external/gitstore:serviceaccount:"+account.UID, r.URL.Path)
			if mirror == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
		case http.MethodPost:
			require.Equal(t, "/admin/identities", r.URL.Path)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&mirror))
			require.Equal(t, "gitstore:serviceaccount:"+account.UID, mirror["external_id"])
			require.Equal(t, "serviceAccount", mirror["schema_id"])
			require.NotContains(t, mirror, "credentials")
			require.NotContains(t, mirror, "verifiable_addresses")
			require.NotContains(t, mirror, "recovery_addresses")
			mirror["id"] = "mirror-uid"
		case http.MethodPatch:
			require.Equal(t, "/admin/identities/mirror-uid", r.URL.Path)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&patches))
			for _, patch := range patches {
				path := patch["path"].(string)
				require.NotContains(t, path, "groups")
				require.NotEqual(t, "/metadata_admin", path)
				if strings.HasPrefix(path, "/metadata_admin/") {
					mirror["metadata_admin"].(map[string]any)[strings.TrimPrefix(path, "/metadata_admin/")] = patch["value"]
				} else {
					mirror[strings.TrimPrefix(path, "/")] = patch["value"]
				}
			}
			if afterPatch != nil {
				callback := afterPatch
				afterPatch = nil
				callback()
			}
		case http.MethodDelete:
			require.Equal(t, "/admin/identities/mirror-uid", r.URL.Path)
			mirror = nil
			w.WriteHeader(http.StatusNoContent)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(mirror))
	}))
	defer server.Close()
	p, err := New(directoryConfig(server.URL))
	require.NoError(t, err)
	payload, err := json.Marshal(account)
	require.NoError(t, err)
	event := datastore.ResourceWatchEvent{Kind: "ServiceAccount", Type: datastore.ResourceWatchAdded, Name: account.Name, Payload: payload}
	require.NoError(t, p.ApplyServiceAccountEvent(t.Context(), store, event))
	require.Equal(t, "active", mirror["state"])
	mirror["metadata_admin"].(map[string]any)["groups"] = []string{"ci"}
	require.NoError(t, store.SetServiceAccountDisabled(t.Context(), account.UID, true))
	afterPatch = func() {
		require.NoError(t, store.SetServiceAccountDisabled(t.Context(), account.UID, false))
	}
	require.ErrorIs(t, p.ApplyServiceAccountEvent(t.Context(), store, event), auth.ErrDirectoryConflict,
		"a source change during native I/O must retry, not acknowledge the stale write")
	require.Equal(t, "inactive", mirror["state"])
	require.NoError(t, p.ApplyServiceAccountEvent(t.Context(), store, event))
	require.Equal(t, "active", mirror["state"], "retry must converge to current source authority")
	require.Equal(t, []string{"ci"}, mirror["metadata_admin"].(map[string]any)["groups"])
	require.NoError(t, store.SetServiceAccountDisabled(t.Context(), account.UID, true))
	require.NoError(t, p.ApplyServiceAccountEvent(t.Context(), store, event), "an old event must use the current source, not reactivate its mirror")
	require.Equal(t, "inactive", mirror["state"])
	require.Equal(t, []string{"ci"}, mirror["metadata_admin"].(map[string]any)["groups"])
	require.NotEmpty(t, patches)
	patches = nil
	require.NoError(t, p.ApplyServiceAccountEvent(t.Context(), store, event))
	require.Empty(t, patches, "duplicate current delivery must not write")
	require.NoError(t, store.DeleteServiceAccount(t.Context(), account.UID))
	event.Type = datastore.ResourceWatchDeleted
	event.Payload = []byte(`{"UID":"` + account.UID + `"}`)
	replacement := *account
	replacement.UID = "00000000-0000-0000-0000-000000000066"
	require.NoError(t, store.CreateServiceAccount(t.Context(), &replacement))
	require.NoError(t, p.ApplyServiceAccountEvent(t.Context(), store, event))
	require.Nil(t, mirror)
	current, err := store.GetServiceAccountBySubject(t.Context(), account.Name)
	require.NoError(t, err)
	require.Equal(t, replacement.UID, current.UID)
}

func TestServiceAccountMirrorDoesNotDeleteOnSourceReadOutage(t *testing.T) {
	p, err := New(directoryConfig("http://127.0.0.1:1"))
	require.NoError(t, err)
	err = p.ApplyServiceAccountEvent(t.Context(), failingServiceAccountSource{}, datastore.ResourceWatchEvent{
		Kind: "ServiceAccount", Type: datastore.ResourceWatchDeleted, Payload: []byte(`{"UID":"source-uid"}`),
	})
	require.ErrorIs(t, err, auth.ErrDirectoryUnavailable)
}

type failingServiceAccountSource struct{}

func (failingServiceAccountSource) GetServiceAccountByUID(_ context.Context, _ string) (*datastore.ServiceAccount, error) {
	return nil, auth.ErrDirectoryUnavailable
}
