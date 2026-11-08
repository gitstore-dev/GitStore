// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/stretchr/testify/require"
)

func directoryConfig(endpoint string) config.KratosUserDirConfig {
	return config.KratosUserDirConfig{
		AdminURI: endpoint, AdminToken: "test-directory-token", Timeout: 100 * time.Millisecond,
		MaxConcurrent: 2, MaxResponseBytes: 1 << 20, CacheEntries: 16, CacheTTL: time.Minute,
		Mappings: []config.SubjectMapping{
			{Provider: "oidc-jwt", Issuer: "https://issuer.example", Mode: "uid"},
			{Provider: "static-users", Issuer: "gitstore/static-users", Mode: "username"},
			{Provider: "external", Issuer: "external", Mode: "external_id", Prefix: "external:"},
		},
	}
}

func identityFixture() map[string]any {
	return map[string]any{
		"id": "identity-uid", "schema_id": "staff", "state": "active",
		"traits":               map[string]any{"username": "alice", "email": "alice@example.test", "full_name": "Alice"},
		"verifiable_addresses": []map[string]any{{"value": "alice@example.test", "verified": true}},
		"metadata_admin": map[string]any{
			"creation_actor":  "operator",
			"update_actor":    "operator",
			"groups":          []string{"merchandiser", "system:staff", "system:customers", "system:serviceaccounts"},
			"contact_consent": map[string]any{"transactional": true, "marketing": false},
			"gitstore": map[string]any{
				"uid": "identity-uid", "name": "alice",
				"generation": 1, "resource_version": "1", "creation_timestamp": "2026-11-07T00:00:00Z",
			},
		},
	}
}

func TestServiceAccountGroupsUseImmutableSourceUIDAndValidateMirror(t *testing.T) {
	for _, test := range []struct {
		name, sourceUID, schema string
		status                  int
		wantErr                 error
	}{
		{name: "linked workload", sourceUID: "source-uid", schema: "serviceAccount"},
		{name: "reused name with different UID", sourceUID: "replacement-uid", schema: "serviceAccount", wantErr: auth.ErrProfileNotFound},
		{name: "human identity", sourceUID: "source-uid", schema: "staff", wantErr: auth.ErrProfileNotFound},
		{name: "missing mirror", status: http.StatusNotFound, wantErr: auth.ErrProfileNotFound},
		{name: "directory outage", status: http.StatusServiceUnavailable, wantErr: auth.ErrDirectoryUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				require.Equal(t, "/admin/identities/by/external/gitstore:serviceaccount:source-uid", r.URL.Path)
				require.Empty(t, r.URL.RawQuery)
				if test.status != 0 {
					w.WriteHeader(test.status)
					return
				}
				record := identityFixture()
				record["schema_id"] = test.schema
				if test.schema == "serviceAccount" {
					record["traits"] = map[string]any{"source_uid": test.sourceUID, "source_name": "alice"}
				}
				require.NoError(t, json.NewEncoder(w).Encode(record))
			}))
			defer server.Close()
			provider, err := New(directoryConfig(server.URL))
			require.NoError(t, err)
			groups, err := provider.ListServiceAccountGroups(t.Context(), "source-uid")
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				require.Nil(t, groups)
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{"system:group:merchandiser", "system:serviceaccounts"}, groups)
				groups[0] = "mutated"
				groups, err = provider.ListServiceAccountGroups(t.Context(), "source-uid")
				require.NoError(t, err)
				require.Equal(t, "system:group:merchandiser", groups[0])
				require.EqualValues(t, 1, calls.Load(), "reuse bounded directory cache")
			}
			before := calls.Load()
			_, err = provider.ListServiceAccountGroups(t.Context(), "")
			require.ErrorIs(t, err, auth.ErrDirectoryInvalidInput)
			require.Equal(t, before, calls.Load(), "invalid source UID must not make a request")
		})
	}
}

func TestResolvePrincipalUsesQualifiedBoundedMappings(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "Bearer test-directory-token", r.Header.Get("Authorization"))
		require.Empty(t, r.URL.Query().Get("include_credential"))
		switch r.URL.Path {
		case "/admin/identities/identity-uid", "/admin/identities/by/external/external:remote":
			require.NoError(t, json.NewEncoder(w).Encode(identityFixture()))
		case "/admin/identities":
			require.Equal(t, "alice", r.URL.Query().Get("credentials_identifier"))
			require.Equal(t, "2", r.URL.Query().Get("page_size"))
			require.NoError(t, json.NewEncoder(w).Encode([]any{identityFixture()}))
		default:
			t.Errorf("unexpected directory enumeration: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	provider, err := New(directoryConfig(server.URL))
	require.NoError(t, err)
	for _, principal := range []*auth.Principal{
		{Subject: "identity-uid", Issuer: "https://issuer.example", AuthMethod: "oidc-jwt"},
		{Subject: "alice", Issuer: "gitstore/static-users", AuthMethod: "static-users"},
		{Subject: "remote", Issuer: "external", AuthMethod: "external"},
	} {
		profile, err := provider.ResolvePrincipal(context.Background(), principal)
		require.NoError(t, err)
		require.Equal(t, auth.ProfileStaff, profile.Kind)
		require.Equal(t, "alice", profile.Username)
		require.Equal(t, auth.ActorActive, profile.Lifecycle)
		require.True(t, profile.EmailVerified)
		require.True(t, profile.ContactConsent.Transactional)
		require.False(t, profile.ContactConsent.Marketing)
		require.Equal(t, []string{"system:group:merchandiser", "system:staff"}, profile.Groups)
	}
	before := calls.Load()
	_, err = provider.ResolvePrincipal(context.Background(), &auth.Principal{
		Subject: "identity-uid", Issuer: "https://another-issuer.example", AuthMethod: "oidc-jwt",
	})
	require.ErrorIs(t, err, auth.ErrProfileNotFound)
	require.Equal(t, before, calls.Load(), "unmapped issuers must not contact the directory")
}

func TestDirectoryFailuresAreNotEmptyProfilesOrCachedOutages(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code := int(status.Load()); code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		json.NewEncoder(w).Encode(identityFixture())
	}))
	defer server.Close()
	provider, err := New(directoryConfig(server.URL))
	require.NoError(t, err)
	_, err = provider.GetBySubject(context.Background(), "identity-uid")
	require.ErrorIs(t, err, auth.ErrDirectoryUnavailable)
	status.Store(http.StatusNotFound)
	_, err = provider.GetBySubject(context.Background(), "identity-uid")
	require.ErrorIs(t, err, auth.ErrProfileNotFound)
	status.Store(http.StatusOK)
	profile, err := provider.GetBySubject(context.Background(), "identity-uid")
	require.NoError(t, err)
	require.NotNil(t, profile)
	profile.Groups[0] = "mutated"
	again, err := provider.GetBySubject(context.Background(), "identity-uid")
	require.NoError(t, err)
	require.NotContains(t, again.Groups, "mutated", "cached profiles must not be shared mutable snapshots")
}

func TestDirectoryDeadlineAndBackwardValidation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-r.Context().Done()
	}))
	defer server.Close()
	provider, err := New(directoryConfig(server.URL))
	require.NoError(t, err)
	start := time.Now()
	_, err = provider.GetBySubject(context.Background(), "identity-uid")
	require.True(t, errors.Is(err, auth.ErrDirectoryUnavailable))
	require.Less(t, time.Since(start), time.Second)
	before := calls.Load()
	last := 10
	_, err = provider.ListActors(context.Background(), auth.ActorPageRequest{Last: &last})
	require.ErrorIs(t, err, auth.ErrNotSupported)
	require.Equal(t, before, calls.Load(), "unsupported direction must be rejected before enumeration")
}

func TestDirectoryResponseErrorsAndBounds(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"conflict", http.StatusConflict, "private identity detail", auth.ErrDirectoryConflict},
		{"invalid", http.StatusBadRequest, "private identity detail", ErrInvalidInput},
		{"unauthorized", http.StatusUnauthorized, "private credential detail", auth.ErrDirectoryUnavailable},
		{"malformed", http.StatusOK, "{private", auth.ErrDirectoryUnavailable},
		{"oversized", http.StatusOK, strings.Repeat("x", 1025), auth.ErrDirectoryUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				w.Write([]byte(test.body))
			}))
			defer server.Close()
			cfg := directoryConfig(server.URL)
			cfg.MaxResponseBytes = 1024
			provider, err := New(cfg)
			require.NoError(t, err)
			_, err = provider.GetBySubject(context.Background(), "identity-uid")
			require.ErrorIs(t, err, test.want)
			require.NotContains(t, err.Error(), "private")
		})
	}
}

func TestDirectoryDoesNotFollowCredentialRedirects(t *testing.T) {
	var redirected atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Store(true)
	}))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	provider, err := New(directoryConfig(server.URL))
	require.NoError(t, err)
	_, err = provider.GetBySubject(context.Background(), "identity-uid")
	require.ErrorIs(t, err, auth.ErrDirectoryUnavailable)
	require.False(t, redirected.Load())
}

func TestDirectorySaturationHasNoWaitQueue(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(identityFixture())
	}))
	defer server.Close()
	provider, err := New(directoryConfig(server.URL))
	require.NoError(t, err)
	for range cap(provider.requests) {
		provider.requests <- struct{}{}
	}
	_, err = provider.GetBySubject(context.Background(), "identity-uid")
	require.ErrorIs(t, err, auth.ErrDirectoryUnavailable)
	require.Zero(t, calls.Load())
	for range cap(provider.requests) {
		<-provider.requests
	}
	_, err = provider.GetBySubject(context.Background(), "identity-uid")
	require.NoError(t, err)
}

func TestExactUsernameDoesNotLinkEmailOrAmbiguousResults(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
		count int
		want  error
	}{
		{"email is not a link", "alice@example.test", 1, auth.ErrProfileNotFound},
		{"ambiguous username", "alice", 2, auth.ErrDirectoryConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/admin/identities", r.URL.Path)
				require.Equal(t, test.query, r.URL.Query().Get("credentials_identifier"))
				results := make([]any, test.count)
				for i := range results {
					results[i] = identityFixture()
				}
				json.NewEncoder(w).Encode(results)
			}))
			defer server.Close()
			provider, err := New(directoryConfig(server.URL))
			require.NoError(t, err)
			_, err = provider.ResolvePrincipal(context.Background(), &auth.Principal{
				Subject: test.query, Issuer: "gitstore/static-users", AuthMethod: "static-users",
			})
			require.ErrorIs(t, err, test.want)
		})
	}
}

func TestCustomerIgnoresEditableMemberships(t *testing.T) {
	var record identity
	fixture := identityFixture()
	fixture["schema_id"] = "customer"
	body, err := json.Marshal(fixture)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &record))
	provider, err := New(directoryConfig("http://directory.example"))
	require.NoError(t, err)
	provider.config.CustomerGroups = []string{"shopper"}
	profile, err := provider.profile(record)
	require.NoError(t, err)
	require.Equal(t, []string{"system:group:shopper", "system:customers"}, profile.Groups)
	require.False(t, profile.EmailDeliverable, "verification must not manufacture an administrator-controlled contact fact")
	record.MetadataAdmin.Metadata = nil
	profile, err = provider.profile(record)
	require.NoError(t, err)
	require.EqualValues(t, -1, profile.Metadata.Generation)
	require.Equal(t, "-1", profile.Metadata.ResourceVersion)
}

func TestProfileCacheBoundsAndExpiredMemberships(t *testing.T) {
	cache := newProfileCache(1, time.Minute)
	cache.put("first", &auth.UserProfile{Groups: []string{"old"}})
	cache.put("second", &auth.UserProfile{Groups: []string{"current"}})
	require.Nil(t, cache.get("first"))
	cache.mu.Lock()
	element := cache.entries["second"]
	entry := element.Value.(cacheEntry)
	entry.expires = time.Now().Add(-time.Second)
	element.Value = entry
	cache.mu.Unlock()
	require.Nil(t, cache.get("second"), "expired memberships must not survive as a successful lookup")
	require.Empty(t, cache.entries)
}

func TestProfileCacheDoesNotRenewAnExpiredSourceSnapshot(t *testing.T) {
	cache := newProfileCache(1, time.Minute)
	cache.put("expired", &auth.UserProfile{
		Groups: []string{"system:staff"}, FreshUntil: time.Now().Add(-time.Second),
	})
	require.Nil(t, cache.get("expired"), "cache insertion must not extend source freshness")
}

func TestInvalidationCannotRepopulateCacheFromInflightLookup(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		json.NewEncoder(w).Encode(identityFixture())
	}))
	defer server.Close()
	cfg := directoryConfig(server.URL)
	provider, err := New(cfg)
	require.NoError(t, err)
	canceled := 0
	provider.SetInvalidationHandler(func() { canceled++ })
	done := make(chan error, 1)
	go func() {
		_, err := provider.GetBySubject(t.Context(), "identity-id")
		done <- err
	}()
	<-entered
	provider.invalidate()
	close(release)
	require.NoError(t, <-done)
	require.Equal(t, 1, canceled)
	require.Empty(t, provider.cache.entries, "pre-invalidation lookups must not restore cached snapshots")
}

func TestWorkloadMappingUsesImmutableSourceUIDNotReusableName(t *testing.T) {
	var wrongUID atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/identities/by/external/gitstore:serviceaccount:source-uid", r.URL.Path)
		record := identityFixture()
		record["schema_id"] = "serviceAccount"
		uid := "source-uid"
		if wrongUID.Load() {
			uid = "replacement-uid"
		}
		record["traits"] = map[string]any{"source_uid": uid, "source_name": "automation"}
		json.NewEncoder(w).Encode(record)
	}))
	defer server.Close()
	cfg := directoryConfig(server.URL)
	cfg.Mappings = []config.SubjectMapping{
		{Provider: "serviceaccount-jwt", Issuer: "gitstore", Mode: "serviceaccount_uid"},
	}
	principal := &auth.Principal{
		Subject: "system:serviceaccount:automation", AuthMethod: "serviceaccount-jwt",
		Issuer: "gitstore", ServiceAccountUID: "source-uid",
	}
	provider, err := New(cfg)
	require.NoError(t, err)
	profile, err := provider.ResolvePrincipal(context.Background(), principal)
	require.NoError(t, err)
	require.Equal(t, auth.ProfileServiceAccount, profile.Kind)
	require.Equal(t, "source-uid", profile.ServiceAccountUID)
	require.Equal(t, []string{"system:group:merchandiser", "system:serviceaccounts"}, profile.Groups)
	wrongUID.Store(true)
	provider, err = New(cfg)
	require.NoError(t, err)
	_, err = provider.ResolvePrincipal(context.Background(), principal)
	require.ErrorIs(t, err, auth.ErrProfileNotFound)
}
