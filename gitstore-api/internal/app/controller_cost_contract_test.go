// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	goast "go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/99designs/gqlgen/complexity"
	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/validator/rules"
)

func TestControllerListDocumentsFitDefaultOperationBudget(t *testing.T) {
	// Read the real consumer constants so this cannot pass using a smaller
	// fixture selection while an unchanged controller is unable to recover.
	files, err := filepath.Glob("../../../gitstore-controller-manager/internal/listwatch/*.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	constants := map[string]goast.Expr{}
	for _, name := range files {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			group, ok := decl.(*goast.GenDecl)
			if !ok || group.Tok != token.CONST {
				continue
			}
			for _, spec := range group.Specs {
				value := spec.(*goast.ValueSpec)
				if len(value.Names) == 1 && len(value.Values) == 1 {
					constants[value.Names[0].Name] = value.Values[0]
				}
			}
		}
	}
	var evaluate func(goast.Expr) string
	evaluate = func(expr goast.Expr) string {
		switch value := expr.(type) {
		case *goast.BasicLit:
			result, err := strconv.Unquote(value.Value)
			require.NoError(t, err)
			return result
		case *goast.Ident:
			return evaluate(constants[value.Name])
		case *goast.BinaryExpr:
			require.Equal(t, token.ADD, value.Op)
			return evaluate(value.X) + evaluate(value.Y)
		default:
			t.Fatalf("unsupported query constant %T", expr)
			return ""
		}
	}
	limits := config.DefaultEndpoint("admin").Limits
	executor := security.LimitSchema(generated.NewExecutableSchema(generated.Config{}), limits)
	for _, name := range []string{"categoriesListQuery", "productsListQueryByNamespace", "namespacesListQuery", "namespacesControllerListQuery", "repositoriesControllerListQuery"} {
		t.Run(name, func(t *testing.T) {
			document, errs := gqlparser.LoadQueryWithRules(executor.Schema(), evaluate(constants[name]), rules.NewDefaultRules())
			require.Empty(t, errs)
			cost := complexity.Calculate(t.Context(), executor, document.Operations[0], map[string]any{"namespace": "owned", "after": nil}, security.ComplexityOptions(executor.Schema())...)
			require.Positive(t, cost)
			require.LessOrEqual(t, cost, limits.Complexity)
		})
	}
	// Related resource traversal still multiplies requested cardinality.
	document, errs := gqlparser.LoadQueryWithRules(executor.Schema(), `{categories(namespace:"owned",first:250){edges{node{products(first:250){edges{node{id}}}}}}}`, rules.NewDefaultRules())
	require.Empty(t, errs)
	require.Greater(t, complexity.Calculate(context.Background(), executor, document.Operations[0], nil, security.ComplexityOptions(executor.Schema())...), limits.Complexity)
}

func TestZeroCostResourceEnvelopesContainOnlyInlineValues(t *testing.T) {
	schema := generated.NewExecutableSchema(generated.Config{}).Schema()
	var visit func(string, map[string]bool)
	visit = func(name string, seen map[string]bool) {
		if seen[name] {
			return
		}
		seen[name] = true
		definition := schema.Types[name]
		require.NotNil(t, definition, name)
		require.NotEqual(t, "Node", name, "resource lookup inside a stored envelope must have an explicit cost")
		require.NotContains(t, definition.Interfaces, "Node", name)
		for _, field := range definition.Fields {
			visit(field.Type.Name(), seen)
		}
		for _, member := range definition.Types {
			visit(member, seen)
		}
	}
	for _, definition := range schema.Types {
		node := false
		for _, iface := range definition.Interfaces {
			node = node || iface == "Node"
		}
		if !node {
			continue
		}
		for _, name := range []string{"metadata", "spec", "status"} {
			if field := definition.Fields.ForName(name); field != nil {
				visit(field.Type.Name(), map[string]bool{})
			}
		}
	}
}
