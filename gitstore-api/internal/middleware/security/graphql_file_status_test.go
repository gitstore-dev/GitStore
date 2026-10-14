// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

// TestGraphQLFieldAuthorizerUpdateResourceStatusFileUsesFileStatusWriteAction
// exercises the REAL production authorization boundary for File status
// writes: GraphQLFieldAuthorizer resolves the exact registered status action.
// For kind "File" that action is "file.status.write", with no ownership suffix.
// This replaces an earlier version of this
// coverage that exercised invented action strings
// ("file.status.write.own"/".any") against the rbac-local policy engine in
// isolation, which never matched what this middleware actually derives or
// calls (spec 051 T041).
func TestGraphQLFieldAuthorizerUpdateResourceStatusFileUsesFileStatusWriteAction(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(nil, authz, nil)

	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "controller-manager", AuthMethod: "static-users"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "updateResourceStatus"}},
		Args: map[string]any{
			"input": model.UpdateResourceStatusInput{
				Kind:            "File",
				Name:            "hero",
				Namespace:       "acme-store",
				ResourceVersion: "1",
			},
		},
	})

	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		called = true
		return "ok", nil
	})
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "file.status.write", authz.Action)
	assert.Equal(t, "file", authz.Resource.Kind)
	assert.Equal(t, "hero", authz.Resource.Name)
}

// TestGraphQLFieldAuthorizerUpdateResourceStatusFileDenyReturnsForbidden
// proves the real boundary actually blocks the resolver: a policy denial
// for "file.status.write" must stop updateResourceStatus from ever reaching
// the File status-write resolver, surfaced as a FORBIDDEN GraphQL error
// (spec 051 T041).
func TestGraphQLFieldAuthorizerUpdateResourceStatusFileDenyReturnsForbidden(t *testing.T) {
	authz := testutil.NewDenyAllAuthZ(t)
	registry := auth.NewProviderRegistry(nil, authz, nil)

	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "eve", AuthMethod: "static-users"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "updateResourceStatus"}},
		Args: map[string]any{
			"input": model.UpdateResourceStatusInput{
				Kind:            "File",
				Name:            "hero",
				Namespace:       "acme-store",
				ResourceVersion: "1",
			},
		},
	})

	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		called = true
		return "ok", nil
	})
	require.Error(t, err)
	assert.False(t, called)
	var gqlErr *gqlerror.Error
	require.True(t, errors.As(err, &gqlErr))
	assert.Equal(t, "FORBIDDEN", gqlErr.Extensions["code"])
	assert.Equal(t, "file.status.write", authz.Action)
}

// TestGraphQLFieldAuthorizerRejectsUnauthorizedFileWatch proves the
// subscription field cannot reach its resolver unless the caller has the
// namespace-scoped file.watch permission.
func TestGraphQLFieldAuthorizerRejectsUnauthorizedFileWatch(t *testing.T) {
	authz := testutil.NewDenyAllAuthZ(t)
	registry := auth.NewProviderRegistry(nil, authz, nil)

	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), auth.Anonymous())
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Subscription",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "watchFiles"}},
		Args:   map[string]any{"namespace": "acme-store"},
	})

	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		called = true
		return "ok", nil
	})
	require.Error(t, err)
	assert.False(t, called)
	assert.Equal(t, "file.watch", authz.Action)
	assert.Equal(t, "file", authz.Resource.Kind)
	assert.Equal(t, "acme-store", authz.Resource.Attrs["namespace"])
	var gqlErr *gqlerror.Error
	require.True(t, errors.As(err, &gqlErr))
	assert.Equal(t, "FORBIDDEN", gqlErr.Extensions["code"])
}

func TestGraphQLFieldAuthorizerAuthorizesGenericFileWatch(t *testing.T) {
	authz := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(nil, authz, nil)
	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "controller", AuthMethod: "bearer"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Subscription",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "watchResources"}},
		Args:   map[string]any{"kind": "File", "namespace": "acme-store"},
	})

	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		called = true
		return "ok", nil
	})
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "file.watch", authz.Action)
	assert.Equal(t, "acme-store", authz.Resource.Attrs["namespace"])
}

func TestFileReadsAuthorizeLookupAndRelayNodesBeforeResolver(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	file := &datastore.File{UID: "00000000-0000-0000-0000-000000000123", Namespace: "private", Name: "hero"}
	require.NoError(t, store.CreateFile(t.Context(), file))
	id := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/File/" + file.UID))
	for _, field := range []string{"file", "node", "nodes"} {
		t.Run(field, func(t *testing.T) {
			authz := testutil.NewDenyAllAuthZ(t)
			mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), store, zap.NewNop())
			args := map[string]any{"namespace": "private", "name": "hero", "id": id, "ids": []string{id}}
			ctx := graphql.WithFieldContext(t.Context(), &graphql.FieldContext{
				Object: "Query", Field: graphql.CollectedField{Field: &ast.Field{Name: field}}, Args: args,
			})
			called := false
			_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { called = true; return nil, nil })
			require.Error(t, err)
			require.False(t, called)
			require.Equal(t, "file.read", authz.Action)
			require.Equal(t, "file", authz.Resource.Kind)
			require.Equal(t, "private", authz.Resource.Attrs["namespace"])
		})
	}
}

type fileNodePreflightStore struct {
	datastore.Datastore
	started   chan string
	release   <-chan struct{}
	active    atomic.Int64
	nameReads atomic.Int64
	failure   error
}

func (s *fileNodePreflightStore) GetFile(ctx context.Context, uid string) (*datastore.File, error) {
	s.active.Add(1)
	defer s.active.Add(-1)
	s.started <- uid
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		if s.failure != nil {
			return nil, s.failure
		}
		return &datastore.File{UID: uid, Name: uid, Namespace: "private", CreationActor: "exact-owner"}, nil
	}
}

func (s *fileNodePreflightStore) GetFileSnapshots(ctx context.Context, ids []string) ([]*datastore.File, error) {
	return testutil.ReadFileSnapshots(ctx, ids, s.GetFile)
}

func (s *fileNodePreflightStore) GetFileByName(context.Context, string, string) (*datastore.File, error) {
	s.nameReads.Add(1)
	return nil, errors.New("File must be authorized from its loaded UID, not reloaded by name")
}

func fileNodePreflightContext(ctx context.Context, count int) context.Context {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("gid://GitStore/File/00000000-0000-0000-0000-%012d", i)))
	}
	return graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Query", Field: graphql.CollectedField{Field: &ast.Field{Name: "nodes"}},
		Args: map[string]any{"ids": ids},
	})
}

type fileNodeSnapshotStore struct {
	datastore.Datastore
	read  func(context.Context, string) (*datastore.File, error)
	batch func(context.Context, []string) ([]*datastore.File, error)
}

func (s *fileNodeSnapshotStore) GetFile(ctx context.Context, uid string) (*datastore.File, error) {
	return s.read(ctx, uid)
}

func (s *fileNodeSnapshotStore) GetFileSnapshots(ctx context.Context, ids []string) ([]*datastore.File, error) {
	if s.batch != nil {
		return s.batch(ctx, ids)
	}
	return testutil.ReadFileSnapshots(ctx, ids, s.GetFile)
}

func TestFileNodeBatchContractFailsClosed(t *testing.T) {
	for _, mode := range []string{"short", "long", "identity", "storage", "cancellation"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			store := &fileNodeSnapshotStore{batch: func(_ context.Context, ids []string) ([]*datastore.File, error) {
				files := make([]*datastore.File, len(ids))
				switch mode {
				case "short":
					return files[:len(files)-1], nil
				case "long":
					return append(files, nil), nil
				case "identity":
					files[0] = &datastore.File{UID: ids[1], Namespace: "private"}
				case "storage":
					return files, errors.New("private failure details")
				case "cancellation":
					cancel()
				}
				return files, nil
			}}
			authz := testutil.NewAllowAllAuthZ()
			mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), store, zap.NewNop())
			called := false
			_, err := mw.GraphQLFieldAuthorizer(fileNodePreflightContext(ctx, 3), func(context.Context) (any, error) {
				called = true
				return nil, nil
			})
			require.ErrorContains(t, err, "authorization error")
			require.NotContains(t, err.Error(), "private failure details")
			require.False(t, called)
			require.Empty(t, authz.Calls)
		})
	}
}

func TestFileNodeBatchPreservesMixedPositionsAndBackendBound(t *testing.T) {
	base, err := memdb.New()
	require.NoError(t, err)
	defer base.Close()
	store := &fileNodeSnapshotStore{Datastore: base, batch: func(_ context.Context, ids []string) ([]*datastore.File, error) {
		require.LessOrEqual(t, len(ids), datastore.FileSnapshotBatchLimit)
		files := make([]*datastore.File, len(ids))
		for i, uid := range ids {
			require.NotEmpty(t, uid)
			files[i] = &datastore.File{UID: uid, Name: uid, Namespace: "stored", CreationActor: uid}
		}
		return files, nil
	}}
	authz := testutil.NewAllowAllAuthZ()
	mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), store, zap.NewNop())
	ctx := fileNodePreflightContext(t.Context(), datastore.FileSnapshotBatchLimit+1)
	fc := graphql.GetFieldContext(ctx)
	ids := fc.Args["ids"].([]string)
	ids = append(ids, base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Product/00000000-0000-0000-0000-000000999999")), ids[0])
	fc.Args["ids"] = ids
	_, err = mw.GraphQLFieldAuthorizer(ctx, func(ctx context.Context) (any, error) {
		file, authorized := AuthorizedFileNode(ctx, "00000000-0000-0000-0000-000000000250")
		require.True(t, authorized)
		require.Equal(t, file.UID, file.CreationActor)
		_, authorized = AuthorizedFileNode(ctx, "other")
		require.False(t, authorized)
		return nil, nil
	})
	require.NoError(t, err)
	require.Len(t, authz.Calls, datastore.FileSnapshotBatchLimit+2)
}

func TestFileNodePreflightRetainsExactAuthoritativeSnapshots(t *testing.T) {
	store := &fileNodeSnapshotStore{read: func(_ context.Context, uid string) (*datastore.File, error) {
		return &datastore.File{UID: uid, Name: uid, Namespace: "stored", CreationActor: "stored-owner"}, nil
	}}
	authz := testutil.NewAllowAllAuthZ()
	mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), store, zap.NewNop())
	_, err := mw.GraphQLFieldAuthorizer(fileNodePreflightContext(t.Context(), 19), func(ctx context.Context) (any, error) {
		file, authorized := AuthorizedFileNode(ctx, "00000000-0000-0000-0000-000000000018")
		require.True(t, authorized)
		require.NotNil(t, file)
		require.Equal(t, "stored-owner", file.CreationActor)
		return "ok", nil
	})
	require.NoError(t, err)
	require.Len(t, authz.Calls, 19)
	for _, call := range authz.Calls {
		require.Equal(t, "file.read", call.Action)
		require.Equal(t, "stored", call.Resource.Attrs["namespace"])
		require.Equal(t, "stored-owner", call.Resource.OwnerSub)
	}
}

func TestFileNodePreflightSnapshotFailuresAndAbsence(t *testing.T) {
	for _, mode := range []string{"storage", "nil", "identity", "cancellation", "missing"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			store := &fileNodeSnapshotStore{read: func(_ context.Context, uid string) (*datastore.File, error) {
				switch mode {
				case "storage":
					return nil, errors.New("unavailable")
				case "identity":
					return &datastore.File{UID: "other"}, nil
				case "cancellation":
					cancel()
					return nil, ctx.Err()
				case "missing":
					return nil, datastore.ErrNotFound
				}
				return nil, nil
			}}
			mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil), store, zap.NewNop())
			called := false
			_, err := mw.GraphQLFieldAuthorizer(fileNodePreflightContext(ctx, 19), func(ctx context.Context) (any, error) {
				called = true
				file, authorized := AuthorizedFileNode(ctx, "00000000-0000-0000-0000-000000000000")
				require.True(t, authorized)
				require.Nil(t, file)
				return nil, nil
			})
			if mode == "missing" {
				require.NoError(t, err)
				require.True(t, called)
			} else {
				require.Error(t, err)
				require.False(t, called)
			}
		})
	}
}
func TestFileNodePreflightBoundsReadsAndUsesExactStoredOwner(t *testing.T) {
	release := make(chan struct{})
	store := &fileNodePreflightStore{started: make(chan string, 32), release: release}
	authz := testutil.NewAllowAllAuthZ()
	mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), store, zap.NewNop())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	var called atomic.Bool
	go func() {
		_, err := mw.GraphQLFieldAuthorizer(fileNodePreflightContext(ctx, 32), func(context.Context) (any, error) {
			called.Store(true)
			return "ok", nil
		})
		done <- err
	}()
	for range 8 {
		select {
		case <-store.started:
		case <-time.After(time.Second):
			t.Fatal("File preflight did not start its bounded reads")
		}
	}
	require.EqualValues(t, 8, store.active.Load())
	require.False(t, called.Load(), "result hydration must wait for all policy checks")
	select {
	case <-store.started:
		t.Fatal("File authorization exceeded eight active reads")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("File preflight did not complete")
	}
	require.True(t, called.Load())
	require.Zero(t, store.active.Load())
	require.Zero(t, store.nameReads.Load())
	require.Len(t, authz.Calls, 32)
	for i, call := range authz.Calls {
		require.Equal(t, "file.read", call.Action)
		require.Equal(t, "private", call.Resource.Attrs["namespace"])
		require.Equal(t, "exact-owner", call.Resource.OwnerSub)
		require.Equal(t, fmt.Sprintf("00000000-0000-0000-0000-%012d", i), call.Resource.Name)
	}
}

func TestFileNodePreflightCancellationAndStorageErrorsFailBeforeResolver(t *testing.T) {
	for _, mode := range []string{"cancellation", "storage-error", "not-found"} {
		t.Run(mode, func(t *testing.T) {
			release := make(chan struct{})
			store := &fileNodePreflightStore{started: make(chan string, 32), release: release}
			if mode == "storage-error" {
				store.failure = errors.New("unavailable")
			} else if mode == "not-found" {
				store.failure = datastore.ErrNotFound
			}
			authz := testutil.NewAllowAllAuthZ()
			mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), store, zap.NewNop())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			var called atomic.Bool
			go func() {
				_, err := mw.GraphQLFieldAuthorizer(fileNodePreflightContext(ctx, 32), func(context.Context) (any, error) {
					called.Store(true)
					return nil, nil
				})
				done <- err
			}()
			for range 8 {
				select {
				case <-store.started:
				case <-time.After(time.Second):
					t.Fatal("File preflight did not start")
				}
			}
			if mode == "cancellation" {
				cancel()
			} else {
				close(release)
			}
			select {
			case err := <-done:
				if mode == "not-found" {
					require.NoError(t, err)
					require.True(t, called.Load(), "missing IDs retain null-result semantics")
				} else {
					require.ErrorContains(t, err, "authorization error")
					require.False(t, called.Load())
				}
			case <-time.After(time.Second):
				t.Fatal("File preflight retained active reads")
			}
			require.Zero(t, store.active.Load())
			require.Zero(t, store.nameReads.Load())
			require.Empty(t, authz.Calls)
		})
	}
}

func TestGraphQLFieldAuthorizerAuthorizesProductWatches(t *testing.T) {
	authz := testutil.NewDenyAllAuthZ(t)
	registry := auth.NewProviderRegistry(nil, authz, nil)
	mw := NewAuthorizeWithStore(registry, &testutil.StubStore{}, zap.NewNop())
	ctx := graphql.WithFieldContext(context.Background(), &graphql.FieldContext{
		Object: "Subscription",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "watchProducts"}},
		Args:   map[string]any{"namespace": "acme-store"},
	})

	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
		called = true
		return "ok", nil
	})
	require.Error(t, err)
	assert.False(t, called)
	assert.Equal(t, "product.watch", authz.Action)
}

func TestCanonicalWatchAndStatusBoundaryMatrix(t *testing.T) {
	principal := &auth.Principal{Subject: "alice", AuthMethod: "static-users"}
	for _, tc := range []struct{ kind, canonical, watch, status string }{
		{"Namespace", "namespace", "watchNamespaces", "updateNamespaceStatus"},
		{"Repository", "repository", "watchRepositories", "updateRepositoryStatus"},
		{"CategoryTaxonomy", "categoryTaxonomy", "watchCategories", "updateCategoryStatus"},
		{"Product", "product", "watchProducts", "updateProductStatus"},
		{"File", "file", "watchFiles", ""},
	} {
		for _, field := range []string{tc.watch, "watchResources", tc.status, "updateResourceStatus"} {
			if field == "" || (tc.kind == "Product" && field == "updateResourceStatus") {
				continue
			}
			for _, allowed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/allow=%t", tc.kind, field, allowed), func(t *testing.T) {
					recorder := testutil.NewAllowAllAuthZ()
					if !allowed {
						recorder = testutil.NewDenyAllAuthZ(t)
					}
					mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, recorder, nil), &testutil.StubStore{}, zap.NewNop())
					object, verb := "Subscription", "watch"
					args := map[string]any{"kind": tc.kind, "namespace": "acme"}
					want := auth.ResourceContext{Kind: tc.canonical, Attrs: map[string]any{}}
					if tc.kind != "Namespace" {
						want.Attrs["namespace"] = "acme"
					}
					if field == tc.status || field == "updateResourceStatus" {
						object, verb = "Mutation", "status.write"
						args = map[string]any{"input": model.UpdateResourceStatusInput{Kind: tc.kind, Namespace: "acme", Name: "item"}}
						want.Name = "item"
						if tc.kind == "Namespace" && field != "updateResourceStatus" {
							want.Attrs = nil
						} else {
							want.Attrs["namespace"] = "acme"
						}
					}
					ctx := auth.ContextWithPrincipal(t.Context(), principal)
					ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: object, Field: graphql.CollectedField{Field: &ast.Field{Name: field, Alias: "aliased"}}, Args: args})
					called := false
					_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { called = true; return nil, nil })
					require.Equal(t, allowed, called)
					if allowed {
						require.NoError(t, err)
					} else {
						require.Error(t, err)
					}
					assertAuthorizationCalls(t, recorder, testutil.AuthorizationCall{Subject: "alice", Action: tc.canonical + "." + verb, Resource: want})
				})
			}
		}
	}
}

func TestGenericStatusRejectsUnknownAndUnimplementedKindsBeforeAuthorizer(t *testing.T) {
	for _, kind := range []string{"Unknown", "ServiceAccount", "Collection", "ProductVariant", "Product", "category", ""} {
		t.Run(kind, func(t *testing.T) {
			recorder := testutil.NewAllowAllAuthZ()
			mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, recorder, nil), &testutil.StubStore{}, zap.NewNop())
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "admin", AuthMethod: "static-users"})
			ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: "Mutation", Field: graphql.CollectedField{Field: &ast.Field{Name: "updateResourceStatus"}}, Args: map[string]any{"input": model.UpdateResourceStatusInput{Kind: kind}}})
			_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { t.Fatal("unsupported status reached resolver"); return nil, nil })
			require.Error(t, err)
			require.Empty(t, recorder.Calls)
			var gqlErr *gqlerror.Error
			require.ErrorAs(t, err, &gqlErr)
			require.Equal(t, "UNSUPPORTED_KIND", gqlErr.Extensions["code"])
		})
	}
}
