// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/allowall"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/anonymous"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestAdminRejectsStorefrontIDsBeforeTypedLookup(t *testing.T) {
	env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
	id := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/StorefrontProduct/public-uid"))
	for _, field := range []string{"product", "file", "category", "collection", "productVariant", "repository", "namespace"} {
		t.Run(field, func(t *testing.T) {
			query := `query($id:ID!){` + field + `(by:{id:$id}){id}}`
			body, err := json.Marshal(map[string]any{"query": query, "variables": map[string]any{"id": id}})
			require.NoError(t, err)
			r, err := http.NewRequest(http.MethodPost, env.server.URL+"/admin/graphql", strings.NewReader(string(body)))
			require.NoError(t, err)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer valid")
			response, err := env.server.Client().Do(r)
			require.NoError(t, err)
			data, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			require.NoError(t, err)
			require.Equal(t, 200, response.StatusCode, string(data))
			require.Contains(t, string(data), `"code":"NOT_FOUND"`)
		})
	}
}

func TestAdminPrincipalQuotaReturnsHTTP429(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	profile := config.DefaultEndpoint("admin")
	profile.Limits.PrincipalBurst = 1
	profile.Limits.PrincipalPerSecond = 0.000001
	registry := auth.NewProviderRegistry(auth.NewChainedAuthN(anonymous.New()), allowall.New(zap.NewNop()), nil)
	h, err := NewGraphQLHandler(GraphQLHandlerDeps{Endpoint: profile, Store: store, Registry: registry, Logger: zap.NewNop(), IDs: apiruntime.NewSequenceIDGenerator()})
	require.NoError(t, err)
	for _, status := range []int{200, 429} {
		r := httptest.NewRequest(http.MethodPost, "/admin/graphql", strings.NewReader(`{"query":"{ __typename }"}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		require.Equal(t, status, w.Code, w.Body.String())
		if status == 429 {
			require.Contains(t, w.Body.String(), "THROTTLED")
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		}
	}
}

func TestPersistedDocumentsAreOwnedByEachExecutor(t *testing.T) {
	first := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
	second := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
	query := "{ __typename }"
	digest := sha256.Sum256([]byte(query))
	extensions := map[string]any{"persistedQuery": map[string]any{"version": 1, "sha256Hash": hex.EncodeToString(digest[:])}}
	request := func(origin string, includeQuery bool) string {
		payload := map[string]any{"extensions": extensions}
		if includeQuery {
			payload["query"] = query
		}
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		r, err := http.NewRequest(http.MethodPost, origin+"/admin/graphql", strings.NewReader(string(body)))
		require.NoError(t, err)
		r.Header.Set("Authorization", "Bearer valid")
		r.Header.Set("Content-Type", "application/json")
		response, err := first.server.Client().Do(r)
		require.NoError(t, err)
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		return string(data)
	}
	require.Contains(t, request(first.server.URL, true), `"__typename":"Query"`)
	require.Contains(t, request(first.server.URL, false), `"__typename":"Query"`)
	require.Contains(t, request(second.server.URL, false), "PersistedQueryNotFound")
}

func TestPersistedCacheHitsReapplyCurrentVariableBounds(t *testing.T) {
	env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
	query := `query($ids:[ID!]!){nodes(ids:$ids){__typename}}`
	digest := sha256.Sum256([]byte(query))
	for _, count := range []int{0, 51} {
		payload := map[string]any{"variables": map[string]any{"ids": make([]string, count)}, "extensions": map[string]any{"persistedQuery": map[string]any{"version": 1, "sha256Hash": hex.EncodeToString(digest[:])}}}
		if count == 0 {
			payload["query"] = query
		}
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		r, err := http.NewRequest(http.MethodPost, env.server.URL+"/admin/graphql", strings.NewReader(string(body)))
		require.NoError(t, err)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer valid")
		response, err := env.server.Client().Do(r)
		require.NoError(t, err)
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		require.NoError(t, err)
		if count == 0 {
			require.Equal(t, 200, response.StatusCode)
			require.Contains(t, string(data), `"nodes":[]`)
		} else {
			require.Equal(t, 422, response.StatusCode)
			require.Contains(t, string(data), "MAX_NODE_IDS_EXCEEDED")
		}
	}
}

func TestAdminDocumentSizeExactBoundaryAndOneOver(t *testing.T) {
	env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
	for _, extra := range []int{0, 1} {
		query := "{" + strings.Repeat(" ", (64<<10)-len("{__typename}")+extra) + "__typename}"
		body, err := json.Marshal(map[string]any{"query": query})
		require.NoError(t, err)
		r, err := http.NewRequest(http.MethodPost, env.server.URL+"/admin/graphql", strings.NewReader(string(body)))
		require.NoError(t, err)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer valid")
		response, err := env.server.Client().Do(r)
		require.NoError(t, err)
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		require.NoError(t, err)
		if extra == 0 {
			require.Equal(t, 200, response.StatusCode, string(data))
		} else {
			require.Equal(t, 422, response.StatusCode)
			require.Contains(t, string(data), "MAX_DOCUMENT_SIZE_EXCEEDED")
		}
	}
}

func TestAdminParserTokenLimitExactBoundaryAndOneOver(t *testing.T) {
	for _, limit := range []int{3, 2} {
		env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute), func(profile *config.EndpointConfig) { profile.Limits.ParserTokens = limit })
		r, err := http.NewRequest(http.MethodPost, env.server.URL+"/admin/graphql", strings.NewReader(`{"query":"{__typename}"}`))
		require.NoError(t, err)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer valid")
		response, err := env.server.Client().Do(r)
		require.NoError(t, err)
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		require.NoError(t, err)
		if limit == 3 {
			require.Equal(t, 200, response.StatusCode, string(data))
		} else {
			require.Equal(t, 422, response.StatusCode, string(data))
			require.Contains(t, string(data), "exceeded token limit")
		}
	}
}

func TestAdminIntrospectionRequiresAuthenticatedPrincipal(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	registry := auth.NewProviderRegistry(auth.NewChainedAuthN(anonymous.New()), allowall.New(zap.NewNop()), nil)
	h, err := NewGraphQLHandler(GraphQLHandlerDeps{Store: store, Registry: registry, Logger: zap.NewNop(), IDs: apiruntime.NewSequenceIDGenerator(), ResourceJournal: store.(datastore.ResourceWatchCapable).ResourceWatchJournal()})
	require.NoError(t, err)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/admin/graphql", strings.NewReader(`{"query":"{ __schema { queryType { name } } }"}`))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, r)
	require.NotContains(t, w.Body.String(), `"name":"Query"`, w.Body.String())
	require.Contains(t, w.Body.String(), `"errors"`)
	require.Contains(t, w.Body.String(), `"code":"UNAUTHENTICATED"`)
}

func TestAuthenticatedAdminIntrospectionRemainsAvailable(t *testing.T) {
	env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
	r, err := http.NewRequest(http.MethodPost, env.server.URL+"/admin/graphql", strings.NewReader(`{"query":"{ __schema { queryType { name } } }"}`))
	require.NoError(t, err)
	r.Header.Set("Authorization", "Bearer valid")
	r.Header.Set("Content-Type", "application/json")
	response, err := env.server.Client().Do(r)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
}
