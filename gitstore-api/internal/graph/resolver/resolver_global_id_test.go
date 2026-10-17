// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

const (
	globalIDTestCategoryUID  = "00000000-0000-0000-0000-000000000001"
	globalIDTestCollectionID = "00000000-0000-0000-0000-000000000002"
	globalIDTestProductUID   = "00000000-0000-0000-0000-000000000003"
	globalIDTestNamespaceID  = "00000000-0000-0000-0000-000000000004"
	globalIDTestVariantUID   = "00000000-0000-0000-0000-000000000005"
	globalIDTestRepositoryID = "00000000-0000-0000-0000-000000000006"
	globalIDTestFileUID      = "00000000-0000-0000-0000-000000000007"
)

func TestReadNotFoundUsesSharedEnvelopeOverTransport(t *testing.T) {
	type lookup struct{ name, query, kind string }
	var lookups []lookup
	for kind := range supportedNodeKinds {
		id := mustEncodeNodeID(kind, globalIDTestProductUID)
		lookups = append(lookups, lookup{
			"node/" + kind, fmt.Sprintf(`{ detail: node(id:%q) { id } }`, id), kind,
		})
		if kind == nodeKindFile {
			lookups = append(lookups, lookup{"file/path", `{ detail: file(namespace:"acme-store",name:"laptops"){id} }`, kind})
			continue
		}
		field := map[string]string{
			nodeKindProduct: "product", nodeKindCategory: "category", nodeKindCollection: "collection",
			nodeKindNamespace: "namespace", nodeKindRepository: "repository", nodeKindProductVariant: "productVariant",
		}[kind]
		lookups = append(lookups, lookup{
			field + "/id", fmt.Sprintf(`{ detail: %s(by:{id:%q}) { id } }`, field, id), kind,
		})
		selector := `namespacePath:{namespace:"acme-store",name:"laptops"}`
		if kind == nodeKindNamespace {
			selector = `name:"acme-store"`
		}
		missingKind := kind
		if kind == nodeKindRepository {
			missingKind = nodeKindNamespace
		}
		lookups = append(lookups, lookup{
			field + "/path", fmt.Sprintf(`{ detail: %s(by:{%s}) { id } }`, field, selector), missingKind,
		})
	}
	for _, lookup := range lookups {
		t.Run(lookup.name, func(t *testing.T) {
			store, _ := newGlobalIDTestResolver(t)
			for range 2 {
				root, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
				require.NoError(t, err)
				middleware := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil), store, zap.NewNop())
				server := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: root}))
				server.AroundFields(middleware.GraphQLFieldAuthorizer)
				payload, err := json.Marshal(map[string]string{"query": lookup.query})
				require.NoError(t, err)
				request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload))
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				server.ServeHTTP(response, request)
				var result struct {
					Data   map[string]any `json:"data"`
					Errors gqlerror.List  `json:"errors"`
				}
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
				require.Len(t, result.Errors, 1, response.Body.String())
				require.Equal(t, ast.Path{ast.PathName("detail")}, result.Errors[0].Path)
				require.Nil(t, result.Data["detail"])
				requireReadNotFoundEnvelope(t, result.Errors[0], lookup.kind)
			}
		})
	}
}

func requireReadNotFoundEnvelope(t *testing.T, err error, kind string) {
	t.Helper()
	var graphErr *gqlerror.Error
	require.ErrorAs(t, err, &graphErr)
	require.Len(t, graphErr.Extensions, 2)
	require.Equal(t, string(admission.CodeNotFound), graphErr.Extensions["code"])
	encoded, marshalErr := json.Marshal(graphErr.Extensions["diagnostics"])
	require.NoError(t, marshalErr)
	var diagnostics []map[string]any
	require.NoError(t, json.Unmarshal(encoded, &diagnostics))
	reasons := map[string]string{
		nodeKindProduct: "PRODUCT_NOT_FOUND", nodeKindNamespace: "NAMESPACE_NOT_FOUND",
		nodeKindCategory: "CATEGORY_NOT_FOUND", nodeKindRepository: "REPOSITORY_NOT_FOUND",
		nodeKindCollection: "COLLECTION_NOT_FOUND", nodeKindProductVariant: "PRODUCT_VARIANT_NOT_FOUND", nodeKindFile: "FILE_NOT_FOUND",
	}
	require.Equal(t, []map[string]any{{
		"reason": reasons[kind], "message": graphErr.Message, "level": string(admission.LevelFailure),
	}}, diagnostics)
}

func TestNodesLookupErrorsRetainInputPositionsAndAuthorizedResults(t *testing.T) {
	store, root := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, t.Context(), store)
	kinds := []string{nodeKindNamespace, nodeKindProduct, nodeKindCategory, nodeKindRepository, nodeKindCollection, nodeKindProductVariant, nodeKindFile}
	found := mustEncodeNodeID(nodeKindProduct, globalIDTestProductUID)
	ids := []string{found}
	for _, kind := range kinds {
		ids = append(ids, mustEncodeNodeID(kind, "00000000-0000-0000-0000-000000000999"))
	}
	ids = append(ids, "not-base64!", found)
	middleware := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil), store, zap.NewNop())
	server := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: root}))
	server.AroundFields(middleware.GraphQLFieldAuthorizer)
	payload, err := json.Marshal(map[string]any{
		"query":     `query($ids:[ID!]!){ selected:nodes(ids:$ids){id} }`,
		"variables": map[string]any{"ids": ids},
	})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	var result struct {
		Data struct {
			Selected []*struct{ ID string } `json:"selected"`
		} `json:"data"`
		Errors gqlerror.List `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.Len(t, result.Data.Selected, len(ids), response.Body.String())
	require.Equal(t, found, result.Data.Selected[0].ID)
	require.Equal(t, found, result.Data.Selected[len(ids)-1].ID)
	require.Len(t, result.Errors, len(kinds)+1)
	for i, kind := range kinds {
		require.Nil(t, result.Data.Selected[i+1])
		require.Equal(t, ast.Path{ast.PathName("selected"), ast.PathIndex(i + 1)}, result.Errors[i].Path)
		requireReadNotFoundEnvelope(t, result.Errors[i], kind)
	}
	last := result.Errors[len(kinds)]
	require.Equal(t, ast.Path{ast.PathName("selected"), ast.PathIndex(len(kinds) + 1)}, last.Path)
	require.Equal(t, string(admission.CodeBadUserInput), last.Extensions["code"])
}

func TestNodeLookupErrorPreservesStructuredEnvelopeAndCause(t *testing.T) {
	for _, original := range []*gqlerror.Error{
		admission.ResourceNotFound("Namespace", "namespace missing"),
		admission.NewError(admission.CodeConflict, "SUPERSEDED", "resource changed").ToGQLError(),
		admission.NewError(admission.CodeBadUserInput, "INVALID_GLOBAL_ID", "invalid global ID").ToGQLError(),
		admission.NewError(admission.CodeForbidden, "PRIVATE_REASON", "private resource detail").ToGQLError(),
	} {
		t.Run(fmt.Sprint(original.Extensions["code"]), func(t *testing.T) {
			original.Err = datastore.ErrNotFound
			cause := fmt.Errorf("wrapped lookup: %w", original)
			result := nodeLookupError(nodeKindProduct, cause)
			require.ErrorIs(t, result, cause)
			var structured *gqlerror.Error
			require.ErrorAs(t, result, &structured)
			require.NotSame(t, original, structured)
			require.Equal(t, original.Extensions, structured.Extensions)
			require.Equal(t, original.Message, structured.Message)
			require.Nil(t, original.Path)
		})
	}
	cause := fmt.Errorf("wrapped domain: %w", admission.NewError(admission.CodeNotFound, "CATEGORY_NOT_FOUND", "category missing"))
	result := nodeLookupError(nodeKindCategory, cause)
	require.ErrorIs(t, result, cause)
	requireReadNotFoundEnvelope(t, result, nodeKindCategory)
}

func TestInvalidReadIDsUseSharedEnvelopeOverTransport(t *testing.T) {
	for _, field := range []string{"node", "namespace", "product", "category", "repository", "collection", "productVariant"} {
		t.Run(field, func(t *testing.T) {
			store, root := newGlobalIDTestResolver(t)
			middleware := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil), store, zap.NewNop())
			server := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: root}))
			server.AroundFields(middleware.GraphQLFieldAuthorizer)
			argument := `by:{id:"not-base64!"}`
			if field == "node" {
				argument = `id:"not-base64!"`
			}
			payload, err := json.Marshal(map[string]string{"query": fmt.Sprintf(`{ detail:%s(%s){id} }`, field, argument)})
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			var result struct {
				Errors gqlerror.List `json:"errors"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			require.Len(t, result.Errors, 1, response.Body.String())
			require.Equal(t, ast.Path{ast.PathName("detail")}, result.Errors[0].Path)
			require.Len(t, result.Errors[0].Extensions, 2)
			require.Equal(t, string(admission.CodeBadUserInput), result.Errors[0].Extensions["code"])
			require.Contains(t, response.Body.String(), `"reason":"INVALID_GLOBAL_ID"`)
		})
	}
}

func TestNodeMissingAuthorizationSnapshotDoesNotExposeLaterCreate(t *testing.T) {
	for _, kind := range []string{nodeKindNamespace, nodeKindProduct, nodeKindCategory} {
		for _, field := range []string{"node", "nodes"} {
			t.Run(kind+"/"+field, func(t *testing.T) {
				const uid = "00000000-0000-0000-0000-000000000999"
				reads := 0
				absent := func() bool {
					reads++
					return reads == 1
				}
				store := &testutil.StubStore{
					GetNamespaceFunc: func(context.Context, string) (*datastore.Namespace, error) {
						if absent() {
							return nil, datastore.ErrNotFound
						}
						return &datastore.Namespace{ID: uid, UID: uid, Name: "late", CreationActor: "other-owner"}, nil
					},
					GetProductFunc: func(context.Context, string) (*datastore.Product, error) {
						if absent() {
							return nil, datastore.ErrNotFound
						}
						return &datastore.Product{UID: uid, Namespace: "private", Name: "late", CreationActor: "other-owner"}, nil
					},
					GetCategoryTaxonomyFunc: func(context.Context, string) (*datastore.CategoryTaxonomy, error) {
						if absent() {
							return nil, datastore.ErrNotFound
						}
						return &datastore.CategoryTaxonomy{UID: uid, Namespace: "private", Name: "late", CreationActor: "other-owner"}, nil
					},
				}
				root, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
				require.NoError(t, err)
				provider := testutil.NewAllowAllAuthZ()
				middleware := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, provider, nil), store, zap.NewNop())
				id := mustEncodeNodeID(kind, uid)
				ctx := graphql.WithResponseContext(t.Context(), graphql.DefaultErrorPresenter, graphql.DefaultRecover)
				ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
					Object: "Query", Field: graphql.CollectedField{Field: &ast.Field{Name: field}},
					Args: map[string]any{"id": id, "ids": []string{id}},
				})
				query := &queryResolver{Resolver: root}
				_, err = middleware.GraphQLFieldAuthorizer(ctx, func(authorized context.Context) (any, error) {
					if field == "node" {
						node, err := query.Node(authorized, id)
						require.Nil(t, node)
						requireReadNotFoundEnvelope(t, err, kind)
					} else {
						nodes, err := query.Nodes(authorized, []string{id})
						require.NoError(t, err)
						require.Equal(t, []model.Node{nil}, nodes)
						require.Len(t, graphql.GetErrors(authorized), 1)
						requireReadNotFoundEnvelope(t, graphql.GetErrors(authorized)[0], kind)
					}
					sibling := graphql.WithFieldContext(authorized, &graphql.FieldContext{Object: "Query"})
					require.Nil(t, security.AuthorizedNodeError(sibling, kind, uid))
					return nil, nil
				})
				require.NoError(t, err)
				require.Equal(t, 1, reads)
				require.Empty(t, provider.Calls)
				_, err = middleware.GraphQLFieldAuthorizer(ctx, func(authorized context.Context) (any, error) {
					node, err := query.Node(authorized, id)
					require.NoError(t, err)
					require.NotNil(t, node)
					return nil, nil
				})
				require.NoError(t, err)
				require.Equal(t, "other-owner", provider.Resource.OwnerSub)
			})
		}
	}
}

func TestCompletionResolversRejectInvalidNodeIDs(t *testing.T) {
	mutation := &mutationResolver{}
	for _, kind := range []string{nodeKindNamespace, nodeKindRepository, nodeKindProduct, nodeKindCategory} {
		t.Run(kind, func(t *testing.T) {
			for _, id := range []string{"", "not-a-node-id", globalIDTestProductUID, mustEncodeNodeID(nodeKindFile, globalIDTestFileUID)} {
				var err error
				switch kind {
				case nodeKindNamespace:
					_, err = mutation.CompleteNamespaceDeletion(t.Context(), model.CompleteNamespaceDeletionInput{ID: id, Name: "acme", ResourceVersion: "1"})
				case nodeKindRepository:
					_, err = mutation.CompleteRepositoryDeletion(t.Context(), model.CompleteRepositoryDeletionInput{ID: id, Namespace: "acme", Name: "item", ResourceVersion: "1"})
				case nodeKindProduct:
					_, err = mutation.CompleteProductDeletion(t.Context(), model.CompleteProductDeletionInput{ID: id, Namespace: "acme", Name: "item", ResourceVersion: "1"})
				case nodeKindCategory:
					_, err = mutation.CompleteCategoryDeletion(t.Context(), model.CompleteCategoryDeletionInput{ID: id, Namespace: "acme", Name: "item", ResourceVersion: "1"})
				}
				require.ErrorContains(t, err, "invalid global ID", "completion must reject invalid or wrong-kind IDs before accessing storage")
			}
		})
	}
}

func TestQueryNodeResolvesByGlobalID(t *testing.T) {
	ctx := context.Background()
	store, resolver := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, ctx, store)
	query := &queryResolver{Resolver: resolver}

	node, err := query.Node(ctx, mustEncodeNodeID(nodeKindProduct, globalIDTestProductUID))
	require.NoError(t, err)
	product, ok := node.(*model.Product)
	require.True(t, ok)
	assert.Equal(t, mustEncodeNodeID(nodeKindProduct, globalIDTestProductUID), product.ID)

	variantNode, err := query.Node(ctx, mustEncodeNodeID(nodeKindProductVariant, globalIDTestVariantUID))
	require.NoError(t, err)
	variant, ok := variantNode.(*model.ProductVariant)
	require.True(t, ok)
	assert.Equal(t, mustEncodeNodeID(nodeKindProductVariant, globalIDTestVariantUID), variant.ID)

	fileNode, err := query.Node(ctx, mustEncodeNodeID(nodeKindFile, globalIDTestFileUID))
	require.NoError(t, err)
	file, ok := fileNode.(*model.File)
	require.True(t, ok)
	typedFile, err := query.File(ctx, "test-store", "file-1")
	require.NoError(t, err)
	require.Equal(t, typedFile, file)
}

func TestQueryNodesPreservesOrderAndReportsInvalidIDs(t *testing.T) {
	ctx := graphql.WithResponseContext(t.Context(), graphql.DefaultErrorPresenter, graphql.DefaultRecover)
	store, resolver := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, ctx, store)
	query := &queryResolver{Resolver: resolver}

	nodes, err := query.Nodes(ctx, []string{
		mustEncodeNodeID(nodeKindNamespace, globalIDTestNamespaceID),
		"not-base64!",
		mustEncodeNodeID(nodeKindCategory, globalIDTestCategoryUID),
		mustEncodeNodeID(nodeKindCollection, globalIDTestCollectionID),
		mustEncodeNodeID(nodeKindProduct, globalIDTestProductUID),
		mustEncodeNodeID(nodeKindProductVariant, globalIDTestVariantUID),
		mustEncodeNodeID(nodeKindProduct, "missing"),
		mustEncodeNodeID(nodeKindFile, globalIDTestFileUID),
		mustEncodeNodeID(nodeKindFile, "00000000-0000-0000-0000-000000000008"),
	})
	require.NoError(t, err)
	require.Len(t, nodes, 9)
	assert.IsType(t, &model.Namespace{}, nodes[0])
	assert.Nil(t, nodes[1])
	assert.IsType(t, &model.Category{}, nodes[2])
	assert.IsType(t, &model.Collection{}, nodes[3])
	assert.IsType(t, &model.Product{}, nodes[4])
	assert.IsType(t, &model.ProductVariant{}, nodes[5])
	assert.Nil(t, nodes[6])
	assert.IsType(t, &model.File{}, nodes[7])
	assert.Nil(t, nodes[8])
	errs := graphql.GetErrors(ctx)
	require.Len(t, errs, 3)
	require.Equal(t, ast.Path{ast.PathIndex(1)}, errs[0].Path)
	require.Equal(t, string(admission.CodeBadUserInput), errs[0].Extensions["code"])
	for i, position := range []int{6, 8} {
		require.Equal(t, ast.Path{ast.PathIndex(position)}, errs[i+1].Path)
		require.Equal(t, string(admission.CodeNotFound), errs[i+1].Extensions["code"])
	}
}

func TestFileNodesObserveSharedDatastoreUpdatesAcrossResolvers(t *testing.T) {
	store, first := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, t.Context(), store)
	second, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	file, err := store.GetFile(t.Context(), globalIDTestFileUID)
	require.NoError(t, err)
	version := file.ResourceVersion
	file.Annotations = map[string]string{"sequence": "1"}
	datastore.AdvanceFileSystemVersion(file)
	require.NoError(t, store.UpdateFile(t.Context(), file, version))
	id := mustEncodeNodeID(nodeKindFile, globalIDTestFileUID)
	for _, root := range []*Resolver{first, second} {
		nodes, err := (&queryResolver{Resolver: root}).Nodes(t.Context(), []string{id, id})
		require.NoError(t, err)
		require.Len(t, nodes, 2)
		for _, node := range nodes {
			file, ok := node.(*model.File)
			require.True(t, ok)
			require.Equal(t, id, file.ID)
			require.Equal(t, "1", file.Metadata.Annotations["sequence"])
		}
	}
}

type globalIDFileFailureStore struct{ datastore.Datastore }

type globalIDCountedFileStore struct {
	datastore.Datastore
	reads atomic.Int64
}

func (s *globalIDCountedFileStore) GetFile(ctx context.Context, uid string) (*datastore.File, error) {
	s.reads.Add(1)
	return s.Datastore.GetFile(ctx, uid)
}

func (s *globalIDCountedFileStore) GetFileSnapshots(ctx context.Context, ids []string) ([]*datastore.File, error) {
	return testutil.ReadFileSnapshots(ctx, ids, s.GetFile)
}

func TestFileNodeResolverReturnsExactAuthorizedFieldSnapshot(t *testing.T) {
	for _, field := range []string{"node", "nodes"} {
		t.Run(field, func(t *testing.T) {
			store, root := newGlobalIDTestResolver(t)
			seedGlobalIDTestData(t, t.Context(), store)
			counted := &globalIDCountedFileStore{Datastore: store}
			root.store = counted
			query := &queryResolver{Resolver: root}
			authz := testutil.NewAllowAllAuthZ()
			mw := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), counted, zap.NewNop())
			id := mustEncodeNodeID(nodeKindFile, globalIDTestFileUID)
			ctx := graphql.WithFieldContext(t.Context(), &graphql.FieldContext{
				Object: "Query", Field: graphql.CollectedField{Field: &ast.Field{Name: field}},
				Args: map[string]any{"id": id, "ids": []string{id, id}},
			})
			_, err := mw.GraphQLFieldAuthorizer(ctx, func(authorizedCtx context.Context) (any, error) {
				current, err := store.GetFile(t.Context(), globalIDTestFileUID)
				require.NoError(t, err)
				expected := current.ResourceVersion
				current.Annotations = map[string]string{"sequence": "changed-after-authorization"}
				current.CreationActor = "different-owner"
				datastore.AdvanceFileSystemVersion(current)
				require.NoError(t, store.UpdateFile(t.Context(), current, expected))
				var nodes []model.Node
				if field == "node" {
					node, err := query.Node(authorizedCtx, id)
					require.NoError(t, err)
					nodes = []model.Node{node}
				} else {
					nodes, err = query.Nodes(authorizedCtx, []string{id, id})
					require.NoError(t, err)
					require.Len(t, nodes, 2)
				}
				for _, node := range nodes {
					file, ok := node.(*model.File)
					require.True(t, ok)
					require.NotEqual(t, "changed-after-authorization", file.Metadata.Annotations["sequence"])
				}
				return nil, nil
			})
			require.NoError(t, err)
			wantReads := int64(1)
			if field == "nodes" {
				wantReads = 2
			}
			require.Equal(t, wantReads, counted.reads.Load(), "hydration must not refetch an authorized File")
			_, err = mw.GraphQLFieldAuthorizer(ctx, func(freshCtx context.Context) (any, error) {
				node, err := query.Node(freshCtx, id)
				require.NoError(t, err)
				require.Equal(t, "changed-after-authorization", node.(*model.File).Metadata.Annotations["sequence"])
				return nil, nil
			})
			require.NoError(t, err)
			require.Equal(t, 2*wantReads, counted.reads.Load(), "another field must perform fresh authorization reads")
			require.Equal(t, "different-owner", authz.Resource.OwnerSub)
		})
	}
}

func TestFileNodeMissingSnapshotDoesNotExposeLaterUnapprovedCreate(t *testing.T) {
	store, root := newGlobalIDTestResolver(t)
	counted := &globalIDCountedFileStore{Datastore: store}
	root.store = counted
	query := &queryResolver{Resolver: root}
	authz := testutil.NewAllowAllAuthZ()
	mw := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), counted, zap.NewNop())
	id := mustEncodeNodeID(nodeKindFile, globalIDTestFileUID)
	ctx := graphql.WithFieldContext(t.Context(), &graphql.FieldContext{
		Object: "Query", Field: graphql.CollectedField{Field: &ast.Field{Name: "node"}},
		Args: map[string]any{"id": id},
	})
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(authorizedCtx context.Context) (any, error) {
		require.NoError(t, store.CreateFile(t.Context(), &datastore.File{
			UID: globalIDTestFileUID, Namespace: "private", Name: "late-file", CreationActor: "other-owner",
		}))
		node, err := query.Node(authorizedCtx, id)
		requireReadNotFoundEnvelope(t, err, nodeKindFile)
		require.Nil(t, node)
		return nil, nil
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, counted.reads.Load())
	require.Empty(t, authz.Calls, "missing rows must not become authorized by a later lookup")
	_, err = mw.GraphQLFieldAuthorizer(ctx, func(freshCtx context.Context) (any, error) {
		node, err := query.Node(freshCtx, id)
		require.NoError(t, err)
		require.NotNil(t, node)
		return nil, nil
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, counted.reads.Load())
	require.Equal(t, "other-owner", authz.Resource.OwnerSub)
}

func (globalIDFileFailureStore) GetFile(context.Context, string) (*datastore.File, error) {
	return nil, errors.New("file storage unavailable")
}

func (s globalIDFileFailureStore) GetFileSnapshots(ctx context.Context, ids []string) ([]*datastore.File, error) {
	return testutil.ReadFileSnapshots(ctx, ids, s.GetFile)
}

func TestFileNodeStorageErrorsAreNotReportedAsMissing(t *testing.T) {
	store, root := newGlobalIDTestResolver(t)
	root.store = globalIDFileFailureStore{store}
	query := &queryResolver{Resolver: root}
	id := mustEncodeNodeID(nodeKindFile, globalIDTestFileUID)
	_, err := query.Node(t.Context(), id)
	require.ErrorContains(t, err, "retrieve File")
	require.NotContains(t, err.Error(), "file storage unavailable")
	_, err = query.Nodes(t.Context(), []string{id})
	require.ErrorContains(t, err, "retrieve File")
	require.NotContains(t, err.Error(), "file storage unavailable")
}

type globalIDPointFailureStore struct {
	datastore.Datastore
	err     error
	repo    *datastore.Repository
	enabled *atomic.Bool
}

func (s globalIDPointFailureStore) GetFile(context.Context, string) (*datastore.File, error) {
	return nil, s.err
}
func (s globalIDPointFailureStore) GetFileSnapshots(ctx context.Context, ids []string) ([]*datastore.File, error) {
	return testutil.ReadFileSnapshots(ctx, ids, s.GetFile)
}
func (s globalIDPointFailureStore) GetProduct(ctx context.Context, uid string) (*datastore.Product, error) {
	if s.enabled != nil && !s.enabled.Load() {
		return s.Datastore.GetProduct(ctx, uid)
	}
	return nil, s.err
}
func (s globalIDPointFailureStore) GetProductByName(ctx context.Context, namespace, name string) (*datastore.Product, error) {
	if s.enabled != nil && !s.enabled.Load() {
		return s.Datastore.GetProductByName(ctx, namespace, name)
	}
	return nil, s.err
}
func (s globalIDPointFailureStore) GetProductVariant(ctx context.Context, uid string) (*datastore.ProductVariant, error) {
	if s.enabled != nil && !s.enabled.Load() {
		return s.Datastore.GetProductVariant(ctx, uid)
	}
	return nil, s.err
}
func (s globalIDPointFailureStore) GetProductVariantByName(ctx context.Context, namespace, name string) (*datastore.ProductVariant, error) {
	if s.enabled != nil && !s.enabled.Load() {
		return s.Datastore.GetProductVariantByName(ctx, namespace, name)
	}
	return nil, s.err
}
func (s globalIDPointFailureStore) GetCategoryTaxonomy(ctx context.Context, uid string) (*datastore.CategoryTaxonomy, error) {
	if s.enabled != nil && !s.enabled.Load() {
		return s.Datastore.GetCategoryTaxonomy(ctx, uid)
	}
	return nil, s.err
}
func (s globalIDPointFailureStore) GetCategoryTaxonomyByName(ctx context.Context, namespace, name string) (*datastore.CategoryTaxonomy, error) {
	if s.enabled != nil && !s.enabled.Load() {
		return s.Datastore.GetCategoryTaxonomyByName(ctx, namespace, name)
	}
	return nil, s.err
}
func (s globalIDPointFailureStore) GetCollection(ctx context.Context, uid string) (*datastore.Collection, error) {
	if s.enabled != nil && !s.enabled.Load() {
		return s.Datastore.GetCollection(ctx, uid)
	}
	return nil, s.err
}
func (s globalIDPointFailureStore) GetCollectionByName(ctx context.Context, namespace, name string) (*datastore.Collection, error) {
	if s.enabled != nil && !s.enabled.Load() {
		return s.Datastore.GetCollectionByName(ctx, namespace, name)
	}
	return nil, s.err
}
func (s globalIDPointFailureStore) GetNamespace(ctx context.Context, uid string) (*datastore.Namespace, error) {
	if s.enabled != nil && !s.enabled.Load() {
		return s.Datastore.GetNamespace(ctx, uid)
	}
	return nil, s.err
}
func (s globalIDPointFailureStore) GetNamespaceByName(ctx context.Context, name string) (*datastore.Namespace, error) {
	if s.enabled != nil && !s.enabled.Load() {
		return s.Datastore.GetNamespaceByName(ctx, name)
	}
	return nil, s.err
}
func (s globalIDPointFailureStore) GetRepository(context.Context, string) (*datastore.Repository, error) {
	if s.repo != nil {
		return s.repo, nil
	}
	return nil, s.err
}

func TestNodeReadsDistinguishAbsenceFromInfrastructureFailure(t *testing.T) {
	for kind := range supportedNodeKinds {
		for _, failure := range []error{
			datastore.ErrNotFound,
			fmt.Errorf("wrapped absence: %w", datastore.ErrNotFound),
			errors.New("private storage failure detail"),
			context.Canceled,
			context.DeadlineExceeded,
		} {
			t.Run(kind+"/"+failure.Error(), func(t *testing.T) {
				store, root := newGlobalIDTestResolver(t)
				failed := globalIDPointFailureStore{Datastore: store, err: failure}
				root.store, root.service.store = failed, failed
				query := &queryResolver{Resolver: root}
				id := mustEncodeNodeID(kind, globalIDTestProductUID)
				node, err := query.Node(t.Context(), id)
				require.Nil(t, node)
				if errors.Is(failure, datastore.ErrNotFound) {
					requireReadNotFoundEnvelope(t, err, kind)
				} else {
					require.ErrorIs(t, err, failure)
					require.NotContains(t, err.Error(), "private storage failure detail")
					encoded, marshalErr := json.Marshal(err)
					require.NoError(t, marshalErr)
					require.Contains(t, string(encoded), `"message":"retrieve `+kind+`"`)
					require.NotContains(t, string(encoded), "private storage failure detail")
				}
				ctx := graphql.WithResponseContext(t.Context(), graphql.DefaultErrorPresenter, graphql.DefaultRecover)
				nodes, err := query.Nodes(ctx, []string{id, id})
				if errors.Is(failure, datastore.ErrNotFound) {
					require.NoError(t, err)
					require.Equal(t, []model.Node{nil, nil}, nodes)
					require.Len(t, graphql.GetErrors(ctx), 2)
					for _, item := range graphql.GetErrors(ctx) {
						requireReadNotFoundEnvelope(t, item, kind)
					}
				} else {
					require.ErrorIs(t, err, failure)
					require.Nil(t, nodes, "an infrastructure failure returned a success-shaped partial result")
				}
			})
		}
	}
}

func TestTypedCatalogReadsDistinguishAbsenceFromInfrastructureFailure(t *testing.T) {
	readers := []struct {
		kind string
		read func(*queryResolver, string) (model.Node, error)
	}{
		{nodeKindNamespace, func(q *queryResolver, selector string) (model.Node, error) {
			name := "test-store"
			by := model.NamespaceBy{Name: &name}
			if selector == "id" {
				id := mustEncodeNodeID(nodeKindNamespace, globalIDTestNamespaceID)
				by = model.NamespaceBy{ID: &id}
			}
			return q.Namespace(t.Context(), by)
		}},
		{nodeKindProduct, func(q *queryResolver, selector string) (model.Node, error) {
			by := model.ProductBy{NamespacePath: &model.ProductNamespacePath{Namespace: "test-store", Name: "item"}}
			if selector == "id" {
				id := mustEncodeNodeID(nodeKindProduct, globalIDTestProductUID)
				by = model.ProductBy{ID: &id}
			}
			return q.Product(t.Context(), by)
		}},
		{nodeKindCategory, func(q *queryResolver, selector string) (model.Node, error) {
			by := model.CategoryBy{NamespacePath: &model.CategoryNamespacePath{Namespace: "test-store", Name: "item"}}
			if selector == "id" {
				id := mustEncodeNodeID(nodeKindCategory, globalIDTestCategoryUID)
				by = model.CategoryBy{ID: &id}
			}
			return q.Category(t.Context(), by)
		}},
		{nodeKindCollection, func(q *queryResolver, selector string) (model.Node, error) {
			by := model.CollectionBy{NamespacePath: &model.CollectionNamespacePath{Namespace: "test-store", Name: "item"}}
			if selector == "id" {
				id := mustEncodeNodeID(nodeKindCollection, globalIDTestCollectionID)
				by = model.CollectionBy{ID: &id}
			}
			return q.Collection(t.Context(), by)
		}},
		{nodeKindProductVariant, func(q *queryResolver, selector string) (model.Node, error) {
			by := model.ProductVariantBy{NamespacePath: &model.ProductVariantNamespacePath{Namespace: "test-store", Name: "item"}}
			if selector == "id" {
				id := mustEncodeNodeID(nodeKindProductVariant, globalIDTestVariantUID)
				by = model.ProductVariantBy{ID: &id}
			}
			return q.ProductVariant(t.Context(), by)
		}},
	}
	for _, reader := range readers {
		for _, selector := range []string{"id", "path"} {
			for _, failure := range []error{
				datastore.ErrNotFound,
				fmt.Errorf("wrapped absence: %w", datastore.ErrNotFound),
				errors.New("private catalog storage detail"),
				context.Canceled,
				context.DeadlineExceeded,
			} {
				t.Run(reader.kind+"/"+selector+"/"+failure.Error(), func(t *testing.T) {
					store, _ := newGlobalIDTestResolver(t)
					failed := globalIDPointFailureStore{Datastore: store, err: failure}
					for range 2 {
						root, err := NewResolver(ResolverDeps{Store: failed, Logger: zap.NewNop()})
						require.NoError(t, err)
						node, err := reader.read(&queryResolver{Resolver: root}, selector)
						require.Nil(t, node)
						if errors.Is(failure, datastore.ErrNotFound) {
							requireReadNotFoundEnvelope(t, err, reader.kind)
						} else {
							require.ErrorIs(t, err, failure)
							require.NotContains(t, err.Error(), "private catalog storage detail")
							encoded, marshalErr := json.Marshal(err)
							require.NoError(t, marshalErr)
							require.Contains(t, string(encoded), `"message":"retrieve `+reader.kind+`"`)
							require.NotContains(t, string(encoded), "private catalog storage detail")
						}
					}
				})
			}
		}
	}
}

func TestTypedCatalogReadTransportAfterAuthorization(t *testing.T) {
	resources := []struct {
		field, kind, input, uid, name, action string
	}{
		{"namespace", nodeKindNamespace, "NamespaceBy", globalIDTestNamespaceID, "namespace-1", "namespace.read"},
		{"product", nodeKindProduct, "ProductBy", globalIDTestProductUID, "product-1", "product.read"},
		{"category", nodeKindCategory, "CategoryBy", globalIDTestCategoryUID, "category-1", "categoryTaxonomy.read"},
		{"collection", nodeKindCollection, "CollectionBy", globalIDTestCollectionID, "collection-1", "collection.read"},
		{"productVariant", nodeKindProductVariant, "ProductVariantBy", globalIDTestVariantUID, "variant-1", "productVariant.read"},
	}
	for _, resource := range resources {
		for _, selector := range []string{"id", "path"} {
			for _, failure := range []error{
				datastore.ErrNotFound,
				fmt.Errorf("wrapped absence: %w", datastore.ErrNotFound),
				errors.New("private catalog storage detail"),
				context.Canceled,
				context.DeadlineExceeded,
			} {
				t.Run(resource.field+"/"+selector+"/"+failure.Error(), func(t *testing.T) {
					store, _ := newGlobalIDTestResolver(t)
					seedGlobalIDTestData(t, t.Context(), store)
					var enabled atomic.Bool
					failed := globalIDPointFailureStore{Datastore: store, err: failure, enabled: &enabled}
					by := map[string]any{"id": mustEncodeNodeID(resource.kind, resource.uid)}
					if selector == "path" {
						by = map[string]any{"namespacePath": map[string]string{"namespace": "test-store", "name": resource.name}}
						if resource.kind == nodeKindNamespace {
							by = map[string]any{"name": resource.name}
						}
					}
					for _, allowed := range []bool{true, false} {
						for range 2 {
							enabled.Store(false)
							root, err := NewResolver(ResolverDeps{Store: failed, Logger: zap.NewNop()})
							require.NoError(t, err)
							provider := testutil.NewAllowAllAuthZ()
							if !allowed {
								provider = testutil.NewDenyAllAuthZ(t)
							}
							middleware := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, provider, nil), failed, zap.NewNop())
							server := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: root}))
							server.AroundFields(func(ctx context.Context, next graphql.Resolver) (any, error) {
								return middleware.GraphQLFieldAuthorizer(ctx, func(nextContext context.Context) (any, error) {
									enabled.Store(true)
									return next(nextContext)
								})
							})
							payload, err := json.Marshal(map[string]any{
								"query":     "query($by:" + resource.input + "!){ " + resource.field + "(by:$by){id}}",
								"variables": map[string]any{"by": by},
							})
							require.NoError(t, err)
							request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload))
							request.Header.Set("Content-Type", "application/json")
							request = request.WithContext(auth.ContextWithPrincipal(request.Context(), &auth.Principal{Subject: "reader", AuthMethod: "bearer"}))
							response := httptest.NewRecorder()
							server.ServeHTTP(response, request)
							require.Equal(t, http.StatusOK, response.Code)
							expectedAction := resource.action
							if !allowed && (resource.kind == nodeKindProduct || resource.kind == nodeKindProductVariant) {
								expectedAction = resource.field + ".management.read"
							}
							require.Equal(t, expectedAction, provider.Action)
							require.NotContains(t, response.Body.String(), "private catalog storage detail")
							column := len("query($by:"+resource.input+"!){ ") + 1
							if !allowed {
								require.False(t, enabled.Load(), "a denied field must not reach the resolver")
								require.JSONEq(t, fmt.Sprintf(`{"errors":[{"message":"permission denied","locations":[{"line":1,"column":%d}],"path":["%s"],"extensions":{"code":"FORBIDDEN"}}],"data":{"%s":null}}`, column, resource.field, resource.field), response.Body.String())
							} else if errors.Is(failure, datastore.ErrNotFound) {
								var body struct {
									Errors gqlerror.List `json:"errors"`
								}
								require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
								require.Len(t, body.Errors, 1)
								require.Equal(t, ast.Path{ast.PathName(resource.field)}, body.Errors[0].Path)
								requireReadNotFoundEnvelope(t, body.Errors[0], resource.kind)
							} else {
								require.JSONEq(t, fmt.Sprintf(`{"errors":[{"message":"retrieve %s","locations":[{"line":1,"column":%d}],"path":["%s"]}],"data":{"%s":null}}`, resource.kind, column, resource.field, resource.field), response.Body.String())
							}
						}
					}
				})
			}
		}
	}
}

func TestRepositoryNodeOwnerLookupDoesNotHideInfrastructureFailure(t *testing.T) {
	for _, failure := range []error{datastore.ErrNotFound, errors.New("private owner lookup failure")} {
		t.Run(failure.Error(), func(t *testing.T) {
			store, root := newGlobalIDTestResolver(t)
			failed := globalIDPointFailureStore{
				Datastore: store,
				err:       failure,
				repo:      &datastore.Repository{ID: globalIDTestRepositoryID, NamespaceID: globalIDTestNamespaceID},
			}
			root.store, root.service.store = failed, failed
			id := mustEncodeNodeID(nodeKindRepository, globalIDTestRepositoryID)
			query := &queryResolver{Resolver: root}
			node, err := query.Node(t.Context(), id)
			require.Nil(t, node)
			ctx := graphql.WithResponseContext(t.Context(), graphql.DefaultErrorPresenter, graphql.DefaultRecover)
			nodes, pluralErr := query.Nodes(ctx, []string{id, id})
			if errors.Is(failure, datastore.ErrNotFound) {
				requireReadNotFoundEnvelope(t, err, nodeKindNamespace)
				require.NoError(t, pluralErr)
				require.Equal(t, []model.Node{nil, nil}, nodes)
				require.Len(t, graphql.GetErrors(ctx), 2)
			} else {
				require.ErrorIs(t, err, failure)
				require.ErrorIs(t, pluralErr, failure)
				require.NotContains(t, err.Error(), "private owner lookup failure")
				require.Nil(t, nodes)
			}
		})
	}
}

type globalIDBlockingFileStore struct {
	datastore.Datastore
	started chan string
	release <-chan struct{}
	active  atomic.Int64
}

func (s *globalIDBlockingFileStore) GetFile(ctx context.Context, uid string) (*datastore.File, error) {
	s.active.Add(1)
	defer s.active.Add(-1)
	s.started <- uid
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return &datastore.File{UID: uid, Namespace: "test-store", Name: uid}, nil
	}
}

func (s *globalIDBlockingFileStore) GetFileSnapshots(ctx context.Context, ids []string) ([]*datastore.File, error) {
	return testutil.ReadFileSnapshots(ctx, ids, s.GetFile)
}

func TestQueryNodesBoundsConcurrentHydrationAndPreservesPositions(t *testing.T) {
	store, root := newGlobalIDTestResolver(t)
	release := make(chan struct{})
	blocked := &globalIDBlockingFileStore{Datastore: store, started: make(chan string, 100), release: release}
	root.store = blocked
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = mustEncodeNodeID(nodeKindFile, fmt.Sprintf("00000000-0000-0000-0000-%012d", i))
	}
	type result struct {
		nodes []model.Node
		err   error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		nodes, err := (&queryResolver{Resolver: root}).Nodes(ctx, ids)
		done <- result{nodes, err}
	}()
	for range nodeFetchFanout {
		select {
		case <-blocked.started:
		case <-time.After(time.Second):
			t.Fatal("node hydration did not fill the bounded worker pool")
		}
	}
	require.EqualValues(t, nodeFetchFanout, blocked.active.Load())
	select {
	case <-blocked.started:
		t.Fatal("node hydration exceeded eight active reads")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.Len(t, got.nodes, len(ids))
		for i, node := range got.nodes {
			require.Equal(t, ids[i], node.GetID())
		}
	case <-time.After(time.Second):
		t.Fatal("node hydration did not finish")
	}
	require.Zero(t, blocked.active.Load())
}

func TestQueryNodesCancellationDrainsReadsAndStopsScheduling(t *testing.T) {
	store, root := newGlobalIDTestResolver(t)
	blocked := &globalIDBlockingFileStore{Datastore: store, started: make(chan string, 100)}
	root.store = blocked
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = mustEncodeNodeID(nodeKindFile, globalIDTestFileUID)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		nodes, err := (&queryResolver{Resolver: root}).Nodes(ctx, ids)
		if nodes != nil {
			done <- errors.New("canceled node query returned partial success")
			return
		}
		done <- err
	}()
	for range nodeFetchFanout {
		select {
		case <-blocked.started:
		case <-time.After(time.Second):
			t.Fatal("node hydration did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("canceled node query retained active reads")
	}
	require.Zero(t, blocked.active.Load())
	require.Empty(t, blocked.started, "cancellation must not schedule the remaining IDs")
	_, err := (&queryResolver{Resolver: root}).Nodes(ctx, ids)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, blocked.started)
}

func TestLookupQueriesAcceptGlobalIDs(t *testing.T) {
	ctx := context.Background()
	store, resolver := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, ctx, store)
	query := &queryResolver{Resolver: resolver}

	product, err := query.Product(ctx, model.ProductBy{NamespacePath: &model.ProductNamespacePath{Namespace: "test-store", Name: "product-1"}})
	require.NoError(t, err)
	require.NotNil(t, product)
	assert.Equal(t, mustEncodeNodeID(nodeKindProduct, globalIDTestProductUID), product.ID)

	categoryID := mustEncodeNodeID(nodeKindCategory, globalIDTestCategoryUID)
	category, err := query.Category(ctx, model.CategoryBy{ID: &categoryID})
	require.NoError(t, err)
	require.NotNil(t, category)
	assert.Equal(t, categoryID, category.ID)

	collectionID := mustEncodeNodeID(nodeKindCollection, globalIDTestCollectionID)
	collection, err := query.Collection(ctx, model.CollectionBy{ID: &collectionID})
	require.NoError(t, err)
	require.NotNil(t, collection)
	assert.Equal(t, mustEncodeNodeID(nodeKindCollection, globalIDTestCollectionID), collection.ID)

	namespaceID := mustEncodeNodeID(nodeKindNamespace, globalIDTestNamespaceID)
	namespace, err := query.Namespace(ctx, model.NamespaceBy{ID: &namespaceID})
	require.NoError(t, err)
	require.NotNil(t, namespace)
	assert.Equal(t, mustEncodeNodeID(nodeKindNamespace, globalIDTestNamespaceID), namespace.ID)
	require.NotNil(t, namespace.Metadata)
	assert.Equal(t, namespace.ID, namespace.Metadata.UID)
	assert.Equal(t, "1", namespace.Metadata.ResourceVersion)
	assert.Equal(t, int32(1), namespace.Metadata.Generation)
	assert.Equal(t, "namespace-1", namespace.Metadata.Name)
}

func TestLookupProductByName(t *testing.T) {
	ctx := context.Background()
	store, resolver := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, ctx, store)
	query := &queryResolver{Resolver: resolver}

	product, err := query.Product(ctx, model.ProductBy{NamespacePath: &model.ProductNamespacePath{Namespace: "test-store", Name: "product-1"}})
	require.NoError(t, err)
	require.NotNil(t, product)
	assert.Equal(t, mustEncodeNodeID(nodeKindProduct, globalIDTestProductUID), product.ID)
	assert.Equal(t, "catalog.gitstore.dev/v1beta1", product.APIVersion)
}

func TestLookupProduct_NotFound(t *testing.T) {
	_, resolver := newGlobalIDTestResolver(t)
	query := &queryResolver{Resolver: resolver}

	product, err := query.Product(context.Background(), model.ProductBy{NamespacePath: &model.ProductNamespacePath{Namespace: "test-store", Name: "no-such-product"}})
	assert.Nil(t, product)
	requireReadNotFoundEnvelope(t, err, nodeKindProduct)
}

func TestRepositoryNodeIdentityUsesStableRelayIDAndMetadataUID(t *testing.T) {
	ctx := context.Background()
	store, resolver := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, ctx, store)
	query := &queryResolver{Resolver: resolver}

	node, err := query.Node(ctx, mustEncodeNodeID(nodeKindRepository, globalIDTestRepositoryID))
	require.NoError(t, err)
	repository, ok := node.(*model.Repository)
	require.True(t, ok)
	assert.Equal(t, mustEncodeNodeID(nodeKindRepository, globalIDTestRepositoryID), repository.ID)
	require.NotNil(t, repository.Metadata)
	assert.Equal(t, repository.ID, repository.Metadata.UID)
	assert.Equal(t, "1", repository.Metadata.ResourceVersion)
	assert.Equal(t, int32(1), repository.Metadata.Generation)
}

func newGlobalIDTestResolver(t *testing.T) (datastore.Datastore, *Resolver) {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	r, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	return store, r
}

func seedGlobalIDTestData(t *testing.T, ctx context.Context, store datastore.Datastore) {
	t.Helper()
	now := time.Now()
	require.NoError(t, store.CreateFile(ctx, &datastore.File{
		UID: globalIDTestFileUID, Namespace: "test-store", Name: "file-1",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: now,
		Spec: json.RawMessage(`{"contentType":"image/jpeg","type":"gitstore.dev/media"}`),
	}))
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, &datastore.CategoryTaxonomy{
		UID:               globalIDTestCategoryUID,
		Namespace:         "test-store",
		Name:              "category-1",
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "CategoryTaxonomy",
		Generation:        1,
		ResourceVersion:   "1",
		CreationTimestamp: now,
	}))
	require.NoError(t, store.CreateCollection(ctx, &datastore.Collection{
		UID:               globalIDTestCollectionID,
		Namespace:         "test-store",
		Name:              "collection-1",
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Collection",
		CreationTimestamp: now,
	}))
	require.NoError(t, store.CreateProduct(ctx, &datastore.Product{
		UID:               globalIDTestProductUID,
		Namespace:         "test-store",
		Name:              "product-1",
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Product",
		CreationTimestamp: now,
	}))
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{
		ID:                globalIDTestNamespaceID,
		Name:              "namespace-1",
		Tier:              datastore.NamespaceTierUser,
		CreationTimestamp: now,
		CreationActor:     "tester",
		UpdateTimestamp:   now,
		UpdateActor:       "tester",
	}))
	require.NoError(t, store.CreateProductVariant(ctx, &datastore.ProductVariant{
		UID:               globalIDTestVariantUID,
		Namespace:         "test-store",
		Name:              "variant-1",
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "ProductVariant",
		CreationTimestamp: now,
		SKU:               "sku-1",
		ProductRefName:    "product-1",
	}))
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{
		UID:               globalIDTestRepositoryID,
		Namespace:         "namespace-1",
		RepositoryID:      globalIDTestRepositoryID,
		Name:              "repository-1",
		DefaultBranch:     "main",
		StorageClass:      "default",
		CreationTimestamp: now,
		CreationActor:     "tester",
		UpdateTimestamp:   now,
		UpdateActor:       "tester",
	}))
}
