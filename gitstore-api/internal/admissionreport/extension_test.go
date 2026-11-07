// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admissionreport_test

import (
	"context"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/admissionreport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

func fieldContext(ctx context.Context, alias string) context.Context {
	return graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Field: graphql.CollectedField{Field: &ast.Field{Alias: alias, Name: "createCategory"}},
	})
}

var warning = admission.Diagnostic{Reason: "MEDIA_UNRESOLVED", Message: "media not found", Level: admission.LevelWarning, File: "categories/tv.md", Field: "spec.media"}

func TestExtensionReportsAliasedMutationsAndKeepsOtherKeys(t *testing.T) {
	response := admissionreport.Extension{}.InterceptResponse(context.Background(), func(ctx context.Context) *graphql.Response {
		admissionreport.Report(fieldContext(ctx, "a"), "", nil)
		admissionreport.Report(fieldContext(ctx, "b"), "9f2c", []admission.Diagnostic{warning})
		admissionreport.Report(fieldContext(ctx, "c"), "", []admission.Diagnostic{{Reason: "NOTE", Message: "n", Level: admission.LevelNotice}})
		return &graphql.Response{Extensions: map[string]any{"cost": 3}}
	})
	require.NotNil(t, response)
	assert.Equal(t, 3, response.Extensions["cost"])
	entries, ok := response.Extensions[admissionreport.ExtensionKey].([]any)
	require.True(t, ok)
	require.Len(t, entries, 2)
	first := entries[0].(map[string]any)
	assert.Equal(t, []any{"b"}, first["path"])
	assert.Equal(t, "9f2c", first["commit"])
	assert.Equal(t, []map[string]any{{
		"reason": "MEDIA_UNRESOLVED", "message": "media not found", "level": "WARNING",
		"file": "categories/tv.md", "field": "spec.media",
	}}, first["diagnostics"])
	second := entries[1].(map[string]any)
	assert.Equal(t, []any{"c"}, second["path"])
	assert.NotContains(t, second, "commit")
}

func TestExtensionOmitsKeyWhenNothingReported(t *testing.T) {
	response := admissionreport.Extension{}.InterceptResponse(context.Background(), func(context.Context) *graphql.Response {
		return &graphql.Response{}
	})
	require.NotNil(t, response)
	assert.NotContains(t, response.Extensions, admissionreport.ExtensionKey)
}

func TestExtensionMergesWithExistingAdmissionEntries(t *testing.T) {
	existing := map[string]any{"path": []any{"z"}}
	response := admissionreport.Extension{}.InterceptResponse(context.Background(), func(ctx context.Context) *graphql.Response {
		admissionreport.Report(fieldContext(ctx, "b"), "", []admission.Diagnostic{warning})
		return &graphql.Response{Extensions: map[string]any{admissionreport.ExtensionKey: []any{existing}}}
	})
	entries := response.Extensions[admissionreport.ExtensionKey].([]any)
	require.Len(t, entries, 2)
	assert.Equal(t, existing, entries[0])
}

func TestReportOutsideRequestIsNoOp(t *testing.T) {
	admissionreport.Report(context.Background(), "", []admission.Diagnostic{warning})
	assert.Nil(t, admissionreport.FromContext(context.Background()))
}
