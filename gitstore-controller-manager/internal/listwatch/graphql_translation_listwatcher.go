// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch

import (
	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/translation"
)

const translationsListQuery = `
query($namespace: String!, $after: String) {
  resources: translations(namespace: $namespace, first: 100, after: $after) {
    edges { node {
` + translation.GraphQLFields + `
    } }
    pageInfo { hasNextPage endCursor }
  }
}`

const watchTranslationsSubscription = `
subscription($resourceVersion: String) {
  event: watchTranslations(resourceVersion: $resourceVersion) {
    type namespace name resourceVersion historical
    resource: translation {
` + translation.GraphQLFields + `
    }
  }
}`

type TranslationListWatcher struct {
	*resourceListWatcher[translation.Resource]
}

func NewTranslationListWatcher(client *graphqlclient.Client) *TranslationListWatcher {
	return &TranslationListWatcher{&resourceListWatcher[translation.Resource]{
		client: client, kind: "Translation",
		listQuery: translationsListQuery, watchQuery: watchTranslationsSubscription,
		identity: func(resource translation.Resource) (string, string, error) {
			return resource.Metadata.Namespace, resource.Metadata.Name, resource.Validate()
		},
		tombstone: func(namespace, name string) translation.Resource {
			return translation.Resource{Metadata: translation.Metadata{Namespace: namespace, Name: name}}
		},
	}}
}

var _ PagedListWatcher[translation.Resource] = (*TranslationListWatcher)(nil)
