// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func endpointSchema(t *testing.T, endpoint string) *ast.Schema {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..", "shared", "schemas")
	var sources []*ast.Source
	for _, dir := range []string{"common", endpoint} {
		names, err := filepath.Glob(filepath.Join(root, dir, "*.graphqls"))
		require.NoError(t, err)
		require.NotEmpty(t, names, "missing %s schema source set", dir)
		for _, name := range names {
			body, err := os.ReadFile(name)
			require.NoError(t, err)
			sources = append(sources, &ast.Source{Name: name, Input: string(body)})
		}
	}
	schema, err := gqlparser.LoadSchema(sources...)
	require.NoError(t, err)
	return schema
}

func TestEndpointSchemaBoundaries(t *testing.T) {
	admin := endpointSchema(t, "admin")
	require.NotNil(t, admin.Mutation)
	require.NotNil(t, admin.Subscription)
	require.NotNil(t, admin.Types["Product"])
	require.Nil(t, admin.Types["StorefrontProduct"])
	public := endpointSchema(t, "storefront")
	require.Nil(t, public.Mutation)
	require.Nil(t, public.Subscription)
	require.NotNil(t, public.Query.Fields.ForName("node"))
	require.NotNil(t, public.Types["StorefrontProduct"].Fields.ForName("id"))
	for _, name := range []string{"Product", "Repository", "Namespace", "ServiceAccount", "ObjectMeta", "Mutation", "Subscription"} {
		require.Nil(t, public.Types[name], "%s leaked into public schema", name)
	}
	for _, name := range []string{"products", "nodes", "watchResources", "createProduct"} {
		require.Nil(t, public.Query.Fields.ForName(name))
	}
}
