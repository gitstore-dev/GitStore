// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/validator/rules"
)

type boundedTestSchema struct{ schema *ast.Schema }

func (s boundedTestSchema) Schema() *ast.Schema { return s.schema }
func (boundedTestSchema) Complexity(context.Context, string, string, int, map[string]any) (int, bool) {
	return 0, false
}
func (boundedTestSchema) Exec(context.Context) graphql.ResponseHandler { panic("must not execute") }

func limitOperation(t *testing.T, query string) *graphql.OperationContext {
	t.Helper()
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: `type Query { ping: String products(first:Int,last:Int): [Product!]! nodes(ids:[ID!]!):[Product] } type Product { id:ID! children(first:Int):[Product!]! }`})
	require.NoError(t, err)
	doc, errs := gqlparser.LoadQueryWithRules(schema, query, rules.NewDefaultRules())
	require.Empty(t, errs)
	return &graphql.OperationContext{RawQuery: query, Doc: doc, Operation: doc.Operations[0], Variables: map[string]any{}}
}

func TestStructuralLimitsRejectBeforeExecution(t *testing.T) {
	limits := config.DefaultEndpoint("admin").Limits
	for _, query := range []string{`{ products(first:251){id} }`, `{ products(first:-1){id} }`, "{ " + strings.Join(func() []string {
		var fields []string
		for i := 0; i < 31; i++ {
			fields = append(fields, fmt.Sprintf("a%d:ping", i))
		}
		return fields
	}(), " ") + " }"} {
		err := ValidateOperation(limitOperation(t, query), limits)
		require.NotNil(t, err, query)
	}
	require.Nil(t, ValidateOperation(limitOperation(t, `{products(first:250){id}}`), limits))
}

func TestPaginationComplexityUsesCardinalityAndSaturates(t *testing.T) {
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: `type Query{products(first:Int):[Product!]!} type Product{id:ID!}`})
	require.NoError(t, err)
	bounded := LimitSchema(boundedTestSchema{schema}, config.DefaultEndpoint("admin").Limits)
	cost, ok := bounded.Complexity(context.Background(), "Query", "products", 3, map[string]any{"first": 250})
	require.True(t, ok)
	require.Equal(t, 1000, cost)
	cost, _ = bounded.Complexity(context.Background(), "Query", "products", 3, map[string]any{"first": 1, "last": 250})
	require.Equal(t, 1000, cost, "both pagination directions must not undercharge the larger page")
	cost, _ = bounded.Complexity(context.Background(), "Query", "products", 1000000000, map[string]any{"first": 250})
	require.Equal(t, 1001, cost)
}

func TestEveryStructuralBudgetAtBoundary(t *testing.T) {
	base := config.DefaultEndpoint("admin").Limits
	for _, tc := range []struct {
		name, at, over, code string
		adjust               func(*config.GraphQLLimits)
	}{
		{"depth", `{products{ id }}`, `{products{children{id}}}`, "MAX_DEPTH_EXCEEDED", func(l *config.GraphQLLimits) { l.Depth = 2 }},
		{"aliases", `{a:ping}`, `{a:ping b:ping}`, "MAX_ALIASES_EXCEEDED", func(l *config.GraphQLLimits) { l.Aliases = 1 }},
		{"roots", `{ping}`, `{ping products{id}}`, "MAX_ROOT_FIELDS_EXCEEDED", func(l *config.GraphQLLimits) { l.RootFields = 1 }},
		{"directives", `{ping @skip(if:false)}`, `{ping @skip(if:false) @include(if:true)}`, "MAX_DIRECTIVES_EXCEEDED", func(l *config.GraphQLLimits) { l.Directives = 1 }},
		{"visits", `{products{id}}`, `{products{id children{id}}}`, "MAX_FRAGMENT_VISITS_EXCEEDED", func(l *config.GraphQLLimits) { l.FragmentVisits = 2 }},
		{"ids", `{nodes(ids:["a"]){id}}`, `{nodes(ids:["a","b"]){id}}`, "MAX_NODE_IDS_EXCEEDED", func(l *config.GraphQLLimits) { l.NodeIDs = 1 }},
		{"bytes", `{ping}`, `{ ping }`, "MAX_DOCUMENT_SIZE_EXCEEDED", func(l *config.GraphQLLimits) { l.DocumentBytes = 6 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := base
			tc.adjust(&l)
			require.Nil(t, ValidateOperation(limitOperation(t, tc.at), l))
			err := ValidateOperation(limitOperation(t, tc.over), l)
			require.NotNil(t, err)
			require.Equal(t, tc.code, err.Extensions["code"])
		})
	}
}

func TestVariablesFragmentsAndOmittedPaginationAreCharged(t *testing.T) {
	l := config.DefaultEndpoint("admin").Limits
	op := limitOperation(t, `query($size:Int){products(first:$size){...P ...P}} fragment P on Product{id}`)
	op.Variables = map[string]any{"size": 251}
	require.NotNil(t, ValidateOperation(op, l))
	op.Variables["size"] = 250
	require.Nil(t, ValidateOperation(op, l))
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: `type Query{products(first:Int,last:Int):[Product!]!} type Product{id:ID!}`})
	require.NoError(t, err)
	bounded := LimitSchema(boundedTestSchema{schema}, l)
	cost, _ := bounded.Complexity(context.Background(), "Query", "products", 1, nil)
	require.Equal(t, 200, cost)
	_, errs := gqlparser.LoadQueryWithRules(schema, `{products{...P}} fragment P on Product {...P}`, rules.NewDefaultRules())
	require.NotEmpty(t, errs)
}

func TestValidationErrorsAreBoundedWithoutDisablingRules(t *testing.T) {
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: `type Query{ping:String}`})
	require.NoError(t, err)
	_, errs := gqlparser.LoadQueryWithRules(schema, `{unknownA unknownB unknownC unknownD}`, BoundedValidationRules(2)())
	require.Len(t, errs, 2)
	_, errs = gqlparser.LoadQueryWithRules(schema, `{ping}`, BoundedValidationRules(2)())
	require.Empty(t, errs)
}

func TestConfiguredPageBoundIncludesDatastoreFallback(t *testing.T) {
	limits := config.DefaultEndpoint("admin").Limits
	for _, query := range []string{
		`{products{id}}`,
		`{products(first:0){id}}`,
		`{products(last:0){id}}`,
		`{products(first:null,last:null){id}}`,
		`query($size:Int){products(first:$size){id}}`,
	} {
		op := limitOperation(t, query)
		limits.PageSize = 100
		require.Nil(t, ValidateOperation(op, limits), query)
		limits.PageSize = 99
		err := ValidateOperation(op, limits)
		require.NotNil(t, err, query)
		require.Equal(t, "MAX_PAGE_SIZE_EXCEEDED", err.Extensions["code"])
	}
	limits.PageSize = 50
	for _, query := range []string{`{ping}`, `{products(first:50){id}}`, `{products(last:50){id}}`, `{products(first:0,last:50){id}}`} {
		require.Nil(t, ValidateOperation(limitOperation(t, query), limits), query)
	}
}
