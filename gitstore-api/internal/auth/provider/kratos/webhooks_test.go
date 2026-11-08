// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/stretchr/testify/require"
)

func TestRegistrationHookStampsActorsAndUnsupportedCustomerCounters(t *testing.T) {
	provider, err := New(directoryConfig("http://kratos:4434"))
	require.NoError(t, err)
	record := identityFixture()
	record["schema_id"] = "customer"
	record["metadata_admin"] = map[string]any{"integration": "preserved"}
	body, err := json.Marshal(map[string]any{"identity": record})
	require.NoError(t, err)
	result := httptest.NewRecorder()
	provider.WebhookHandler().ServeHTTP(result, httptest.NewRequest(http.MethodPost, RegistrationHookPath, bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, result.Code)
	var response struct {
		Identity struct {
			MetadataAdmin map[string]any `json:"metadata_admin"`
		} `json:"identity"`
	}
	require.NoError(t, json.Unmarshal(result.Body.Bytes(), &response))
	require.Equal(t, "preserved", response.Identity.MetadataAdmin["integration"])
	require.Equal(t, "alice", response.Identity.MetadataAdmin["creation_actor"])
	require.Equal(t, "alice", response.Identity.MetadataAdmin["update_actor"])
	metadata := response.Identity.MetadataAdmin["gitstore"].(map[string]any)
	require.EqualValues(t, -1, metadata["generation"])
	require.Equal(t, "-1", metadata["resource_version"])
}

func TestPasswordSettingsHookAloneMarksSetupCompleteAndKeepsSuspensionAndProtectedFields(t *testing.T) {
	current := identityFixture()
	current["state"] = "inactive"
	current["metadata_admin"].(map[string]any)["setup_complete"] = false
	current["metadata_admin"].(map[string]any)["lifecycle_state"] = auth.ActorSuspended
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(current)
	}))
	defer upstream.Close()
	provider, err := New(directoryConfig(upstream.URL))
	require.NoError(t, err)
	for _, path := range []string{SettingsHookPath, "/internal/userdir/kratos/settings-password"} {
		candidate := identityFixture()
		candidate["metadata_admin"] = map[string]any{"setup_complete": true, "groups": []string{"admin"}}
		candidate["credentials"] = map[string]any{"password": map[string]any{"config": map[string]any{"password": "private-password"}}}
		body, err := json.Marshal(map[string]any{"identity": candidate})
		require.NoError(t, err)
		result := httptest.NewRecorder()
		provider.WebhookHandler().ServeHTTP(result, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
		require.Equal(t, http.StatusOK, result.Code)
		var response struct {
			Identity struct {
				MetadataAdmin map[string]any `json:"metadata_admin"`
			} `json:"identity"`
		}
		require.NoError(t, json.Unmarshal(result.Body.Bytes(), &response))
		require.Equal(t, path != SettingsHookPath, response.Identity.MetadataAdmin["setup_complete"])
		require.Equal(t, string(auth.ActorSuspended), response.Identity.MetadataAdmin["lifecycle_state"])
		require.Equal(t, "operator", response.Identity.MetadataAdmin["creation_actor"])
		require.ElementsMatch(t, current["metadata_admin"].(map[string]any)["groups"], response.Identity.MetadataAdmin["groups"])
		require.NotContains(t, result.Body.String(), "private-password")
	}
}

func TestSettingsHookRejectsRenameAndPreservesProtectedMetadata(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/identities/identity-uid", r.URL.Path)
		json.NewEncoder(w).Encode(identityFixture())
	}))
	defer upstream.Close()
	provider, err := New(directoryConfig(upstream.URL))
	require.NoError(t, err)
	for _, change := range []string{"none", "username", "schema", "display"} {
		record := identityFixture()
		record["metadata_admin"] = nil
		switch change {
		case "username":
			record["traits"].(map[string]any)["username"] = "replacement"
		case "schema":
			record["schema_id"] = "customer"
		case "display":
			record["traits"].(map[string]any)["full_name"] = "Changed"
		}
		body, err := json.Marshal(map[string]any{"identity": record})
		require.NoError(t, err)
		result := httptest.NewRecorder()
		provider.WebhookHandler().ServeHTTP(result, httptest.NewRequest(http.MethodPost, SettingsHookPath, bytes.NewReader(body)))
		if change == "username" || change == "schema" {
			require.Equal(t, http.StatusBadRequest, result.Code)
		} else {
			require.Equal(t, http.StatusOK, result.Code)
			require.Contains(t, result.Body.String(), "merchandiser")
			require.Contains(t, result.Body.String(), `"creation_actor":"operator"`)
			require.Contains(t, result.Body.String(), `"update_actor":"alice"`)
			if change == "none" {
				require.Contains(t, result.Body.String(), `"generation":1`)
			} else {
				require.Contains(t, result.Body.String(), `"generation":2`)
			}
			require.Contains(t, result.Body.String(), `"resource_version":"2"`)
		}
	}
}

func TestWebhookBoundsAndValidationFailClosed(t *testing.T) {
	provider, err := New(directoryConfig("http://kratos:4434"))
	require.NoError(t, err)
	valid, err := json.Marshal(map[string]any{"identity": identityFixture()})
	require.NoError(t, err)
	for _, body := range []string{
		`{`, `{}`, `{"identity":{"schema_id":"staff"}}`,
		string(valid) + `{}`, strings.Repeat(" ", int(provider.config.MaxResponseBytes)+1),
	} {
		result := httptest.NewRecorder()
		provider.WebhookHandler().ServeHTTP(result, httptest.NewRequest(http.MethodPost, RegistrationHookPath, bytes.NewBufferString(body)))
		require.GreaterOrEqual(t, result.Code, http.StatusBadRequest)
	}
}

func TestSettingsHookFailsClosedWhenDirectoryIsUnavailable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "private provider response", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	provider, err := New(directoryConfig(upstream.URL))
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{"identity": identityFixture()})
	require.NoError(t, err)
	result := httptest.NewRecorder()
	provider.WebhookHandler().ServeHTTP(result, httptest.NewRequest(http.MethodPost, SettingsHookPath, bytes.NewReader(body)))
	require.Equal(t, http.StatusServiceUnavailable, result.Code)
	require.NotContains(t, result.Body.String(), "private provider response")
}

func TestProfileMetadataFallbackAndExhaustedRevisionsRemainReadable(t *testing.T) {
	provider, err := New(directoryConfig("http://kratos:4434"))
	require.NoError(t, err)
	for _, kind := range []string{"staff", "customer"} {
		raw := identityFixture()
		raw["schema_id"], raw["metadata_admin"] = kind, nil
		data, err := json.Marshal(raw)
		require.NoError(t, err)
		var record identity
		require.NoError(t, json.Unmarshal(data, &record))
		profile, err := provider.profile(record)
		require.NoError(t, err)
		require.Equal(t, "alice", profile.Metadata.OwnerSub)
		require.Empty(t, profile.Metadata.CreationActor)
		require.EqualValues(t, -1, profile.Metadata.Generation)
		require.Equal(t, "-1", profile.Metadata.ResourceVersion)
	}
	data, err := json.Marshal(identityFixture())
	require.NoError(t, err)
	var record identity
	require.NoError(t, json.Unmarshal(data, &record))
	record.MetadataAdmin.Metadata.ResourceVersion = "18446744073709551615"
	_, err = provider.profile(record)
	require.NoError(t, err)
	record.MetadataAdmin.Metadata.Generation = -1
	_, err = provider.profile(record)
	require.ErrorIs(t, err, auth.ErrDirectoryUnavailable)
	record.SchemaID = "customer"
	record.MetadataAdmin.Metadata.Generation = 1
	updated, err := revisedMetadata(record, nil, "alice", true)
	require.NoError(t, err, "unsupported Customer counters must not impose a revision check")
	metadata := updated["gitstore"].(map[string]any)
	require.EqualValues(t, -1, metadata["generation"])
	require.Equal(t, "-1", metadata["resource_version"])
}
