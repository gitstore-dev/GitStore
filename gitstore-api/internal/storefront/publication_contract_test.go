// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package storefront_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/gitstore-dev/gitstore/api/internal/storefront"
	"github.com/gitstore-dev/gitstore/api/internal/storefront/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/storefront/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/storefront/graph/resolver"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// The fixture binds a target only inside a direct generated-executor test.
// It defines no production target header, publication API or ready switch.
type fixtureTargetKey struct{}
type publicationFixture struct {
	published map[string]map[string]bool
	draft     map[string]bool
}

func (f *publicationFixture) Query() generated.QueryResolver { return f }
func (f *publicationFixture) Node(ctx context.Context, id string) (model.Node, error) {
	uid, err := resolver.DecodeNodeID(id)
	if err != nil {
		return nil, err
	}
	target, _ := ctx.Value(fixtureTargetKey{}).(string)
	if !f.published[target][uid] {
		return nil, &gqlerror.Error{Message: "resource not found", Extensions: map[string]any{"code": "NOT_FOUND"}}
	}
	return &model.StorefrontProduct{ID: id}, nil
}

func TestStorefrontPublicationIdentityFixture(t *testing.T) {
	id := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/StorefrontProduct/stable-uid"))
	fixture := &publicationFixture{published: map[string]map[string]bool{"target-a": {"stable-uid": true}, "target-b": {}}, draft: map[string]bool{"newer-draft": true}}
	schema := generated.NewExecutableSchema(generated.Config{Resolvers: fixture})
	reader := handler.NewDefaultServer(schema)
	probe := func(target, query string, ids ...string) string {
		requested := id
		if len(ids) > 0 {
			requested = ids[0]
		}
		body, err := json.Marshal(map[string]any{"query": query, "variables": map[string]any{"id": requested}})
		require.NoError(t, err)
		request := httptest.NewRequest(http.MethodPost, "/storefront/graphql", strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		request = request.WithContext(context.WithValue(request.Context(), fixtureTargetKey{}, target))
		response := httptest.NewRecorder()
		reader.ServeHTTP(response, request)
		return response.Body.String()
	}
	query := `query($id:ID!){node(id:$id){id __typename}}`
	first := probe("target-a", query)
	require.Contains(t, first, id)
	require.Contains(t, first, `"__typename":"StorefrontProduct"`)
	require.Contains(t, probe("target-b", query), `"code":"NOT_FOUND"`)
	draftID := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/StorefrontProduct/newer-draft"))
	require.Contains(t, probe("target-a", query, draftID), `"code":"NOT_FOUND"`)
	// A newer draft remains absent from the published cohort. Changing the
	// fixture release preserves resource identity rather than replacing UID.
	fixture.draft["stable-uid"] = true
	fixture.published["target-a"] = map[string]bool{"stable-uid": true, "release-two": true}
	require.Equal(t, first, probe("target-a", query))
	require.Contains(t, probe("target-a", `query($id:ID!){node(id:$id){... on StorefrontProduct{id title}}}`), "Cannot query field")
	for _, field := range []string{"products", "nodes", "totalCount", "relationships"} {
		require.Nil(t, schema.Schema().Query.Fields.ForName(field))
	}
	require.Nil(t, schema.Schema().Mutation)
	require.Nil(t, schema.Schema().Subscription)
	// The same fixture context cannot activate the production guard.
	request := httptest.NewRequest(http.MethodPost, "/storefront/graphql", nil).WithContext(context.WithValue(context.Background(), fixtureTargetKey{}, "target-a"))
	response := httptest.NewRecorder()
	storefront.NewHandler().ServeHTTP(response, request)
	require.Equal(t, 503, response.Code)
	require.NotContains(t, response.Body.String(), `"data"`)
	if path := os.Getenv("CAPACITY_FIXTURE_EVIDENCE_FILE"); path != "" {
		receipt, err := json.Marshal(map[string]any{"schemaVersion": 1, "component": "storefront-identity-fixture/v1", "passed": true, "evidenceClass": "publication-fixture", "publishedCatalog": "blocked", "sourceStateSha256": os.Getenv("CAPACITY_SOURCE_STATE_SHA256")})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, receipt, 0600))
	}
}
