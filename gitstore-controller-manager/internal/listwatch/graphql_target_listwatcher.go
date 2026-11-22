// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch

import (
	"fmt"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/translation"
)

const targetMetadataFields = `
  apiVersion kind
  metadata {
    uid name namespace resourceVersion generation revision
    finalizers deletionTimestamp
  }
`

var translationTargetQueries = map[string]struct {
	list, watch, object string
}{
	"ProductVariant": {"productVariants", "watchProductVariants", "productVariant"},
	"Collection":     {"collections", "watchCollections", "collection"},
	"File":           {"files", "watchFiles", "file"},
}

// TargetMetadataResource is an invalidation hint, not proof of target material.
type TargetMetadataResource struct {
	APIVersion string               `json:"apiVersion"`
	Kind       string               `json:"kind"`
	Metadata   translation.Metadata `json:"metadata"`
}

type TranslationTargetListWatcher struct {
	*resourceListWatcher[TargetMetadataResource]
}

// Product and CategoryTaxonomy reuse their existing runners instead.
func NewTranslationTargetListWatcher(client *graphqlclient.Client, kind string) (*TranslationTargetListWatcher, error) {
	fields, ok := translationTargetQueries[kind]
	if !ok {
		return nil, fmt.Errorf("listwatch: unsupported metadata-only Translation target %q", kind)
	}
	list := fmt.Sprintf(`
query($namespace: String!, $after: String) {
  resources: %s(namespace: $namespace, first: 100, after: $after) {
    edges { node { %s } }
    pageInfo { hasNextPage endCursor }
  }
}`, fields.list, targetMetadataFields)
	watch := fmt.Sprintf(`
subscription($resourceVersion: String) {
  event: %s(resourceVersion: $resourceVersion) {
    type namespace name resourceVersion
    resource: %s { %s }
  }
}`, fields.watch, fields.object, targetMetadataFields)
	return &TranslationTargetListWatcher{&resourceListWatcher[TargetMetadataResource]{
		client: client, kind: kind, listQuery: list, watchQuery: watch,
		identity: func(resource TargetMetadataResource) (string, string, error) {
			metadata := resource.Metadata
			if resource.Kind != kind || resource.APIVersion == "" ||
				metadata.UID == "" || metadata.Namespace == "" || metadata.Name == "" ||
				metadata.ResourceVersion == "" || metadata.Generation < 1 {
				return "", "", fmt.Errorf("listwatch: incomplete %s metadata response", kind)
			}
			return metadata.Namespace, metadata.Name, nil
		},
		tombstone: func(namespace, name string) TargetMetadataResource {
			return TargetMetadataResource{Kind: kind, Metadata: translation.Metadata{Namespace: namespace, Name: name}}
		},
	}}, nil
}

var _ PagedListWatcher[TargetMetadataResource] = (*TranslationTargetListWatcher)(nil)
