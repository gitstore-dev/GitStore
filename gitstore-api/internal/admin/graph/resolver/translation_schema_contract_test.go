// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTranslationDependentScanSchemaContract(t *testing.T) {
	schema := productContractSchema(t)
	requireGraphQLField(t, schema, "Query", "scanTranslationDependents", "TranslationDependentScan!")
	scan := schema.Query.Fields.ForName("scanTranslationDependents")
	require.Len(t, scan.Arguments, 3)
	require.Equal(t, "TranslationDependencyInput!", scan.Arguments.ForName("target").Type.String())
	require.Equal(t, "Int", scan.Arguments.ForName("limit").Type.String())
	require.Equal(t, "String", scan.Arguments.ForName("continuation").Type.String())
	requireGraphQLField(t, schema, "TranslationDependentScan", "items", "[TranslationDependent!]!")
	requireGraphQLField(t, schema, "TranslationDependentScan", "continuation", "String")
	require.Len(t, schema.Types["TranslationDependentScan"].Fields, 2)
	require.Nil(t, schema.Query.Fields.ForName("translationDependents"))
	require.Nil(t, schema.Types["TranslationDependentPage"])
}

func TestTranslationSchemaContract(t *testing.T) {
	schema := productContractSchema(t)
	require.NotNil(t, schema.Types["Translation"])
	require.Contains(t, schema.Types["Translation"].Interfaces, "Node")
	for field, shape := range map[string]string{
		"id": "ID!", "apiVersion": "String!", "kind": "String!", "metadata": "ObjectMeta!",
		"spec": "TranslationSpec!", "body": "String", "status": "TranslationStatus",
	} {
		requireGraphQLField(t, schema, "Translation", field, shape)
	}

	for field, shape := range map[string]string{
		"observedGeneration": "Int!", "lastAppliedRevision": "String!", "conditions": "[Condition!]!",
		"resolved": "ResolvedTranslation",
	} {
		requireGraphQLField(t, schema, "TranslationStatus", field, shape)
	}
	require.Len(t, schema.Types["TranslationStatus"].Fields, 4)
	requireGraphQLField(t, schema, "TranslationSpec", "locale", "String!")
	requireGraphQLField(t, schema, "TranslationSpec", "targetRef", "CatalogObjectReference!")
	requireGraphQLField(t, schema, "TranslationSpec", "patches", "[TranslationPatch!]!")
	requireGraphQLField(t, schema, "TranslationPatch", "fieldPath", "String!")
	requireGraphQLField(t, schema, "TranslationPatch", "translation", "String!")
	requireGraphQLField(t, schema, "ResolvedTranslation", "validatedFieldPaths", "[String!]!")
	requireGraphQLField(t, schema, "ResolvedTranslation", "canonicalLocale", "String")
	require.Nil(t, schema.Types["ResolvedTranslation"].Fields.ForName("descriptorDigest"))
	require.Nil(t, schema.Types["ResolvedTranslation"].Fields.ForName("sourceDigest"))
	requireGraphQLField(t, schema, "ResolvedTranslationInput", "descriptorDigest", "String!")
	requireGraphQLField(t, schema, "ResolvedTranslationInput", "sourceDigest", "String!")
	require.Len(t, schema.Types["UpdateTranslationStatusInput"].Fields, 8)
	requireGraphQLField(t, schema, "UpdateTranslationStatusInput", "uid", "ID!")
	requireGraphQLField(t, schema, "UpdateResourceStatusInput", "uid", "ID")
	requireGraphQLField(t, schema, "Query", "translation", "Translation")
	requireGraphQLField(t, schema, "Query", "translations", "TranslationConnection!")
	requireGraphQLField(t, schema, "Subscription", "watchTranslations", "TranslationWatchEvent!")
	requireGraphQLField(t, schema, "Mutation", "updateTranslationStatus", "UpdateTranslationStatusPayload!")
	requireGraphQLField(t, schema, "Mutation", "bindTranslationTarget", "BindTranslationTargetPayload!")
	requireGraphQLField(t, schema, "BindTranslationTargetPayload", "binding", "TranslationBinding!")
	bind := schema.Mutation.Fields.ForName("bindTranslationTarget")
	require.Len(t, bind.Arguments, 1)
	require.Equal(t, "BindTranslationTargetInput!", bind.Arguments.ForName("input").Type.String())
	requireGraphQLField(t, schema, "TranslationConnection", "edges", "[TranslationEdge!]!")
	requireGraphQLField(t, schema, "TranslationConnection", "pageInfo", "PageInfo!")
	require.Nil(t, schema.Types["TranslationConnection"].Fields.ForName("resourceVersion"))
	requireGraphQLField(t, schema, "ObjectMeta", "resourceVersion", "String!")
	requireGraphQLField(t, schema, "TranslationWatchEvent", "resourceVersion", "String!")
	requireGraphQLField(t, schema, "Condition", "lastTransitionTime", "DateTime!")
	requireGraphQLField(t, schema, "SubjectAccessReviewRevision", "capturedAt", "DateTime!")
	for _, field := range []string{"freshness", "checkedAt", "status"} {
		require.Nil(t, schema.Types["TranslationSpec"].Fields.ForName(field))
	}
	for _, field := range []string{"freshness", "checkedAt", "spec", "body"} {
		require.Nil(t, schema.Types["UpdateTranslationStatusInput"].Fields.ForName(field))
	}
	require.Nil(t, schema.Types["TranslationStatus"].Fields.ForName("observedRevision"))
	require.Nil(t, schema.Types["TranslationStatus"].Fields.ForName("checkedAt"))
	require.Nil(t, schema.Types["ResolvedTranslation"].Fields.ForName("appliedFieldPaths"))
	require.Nil(t, schema.Query.Fields.ForName("createTranslation"))
	require.Nil(t, schema.Mutation.Fields.ForName("createTranslation"))
	require.Nil(t, schema.Types["TranslationFreshness"])
	require.Nil(t, schema.Types["TranslationObservation"])
}
