// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

// productLifecycleFixture is deliberately resource-complete. Subsequent
// lifecycle tests use it to distinguish Git-owned provenance from system-owned
// deletion metadata without relying on production fixtures.
func productLifecycleFixture(namespace, name string) *datastore.Product {
	now := time.Now().UTC()
	return &datastore.Product{
		UID:               uuid.NewString(),
		Namespace:         namespace,
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Product",
		Generation:        1,
		ResourceVersion:   "1",
		CreationTimestamp: now,
		UpdateTimestamp:   now,
		RepositoryID:      uuid.NewString(),
		SourcePath:        "products/" + name + ".yaml",
		GitRef:            "refs/heads/main",
		GitCommitSHA:      "0123456789abcdef",
	}
}

func productLifecycleContext(ctx context.Context, action string) context.Context {
	ctx = auth.ContextWithPrincipal(ctx, &auth.Principal{Subject: "product-author", AuthMethod: "test"})
	return graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: action}},
	})
}

func requireProductLifecycleFixture(t *testing.T, store datastore.Datastore, namespace, name string) *datastore.Product {
	t.Helper()
	product := productLifecycleFixture(namespace, name)
	require.NoError(t, store.CreateProduct(context.Background(), product))
	return product
}
