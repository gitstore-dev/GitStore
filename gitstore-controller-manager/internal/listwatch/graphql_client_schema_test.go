// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch

import (
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/schemavalidate"
)

// TestGraphQLOperationsMatchSchema validates every list/watch query,
// mutation and subscription string this package sends against the real
// schema (shared/schemas/*.graphqls), the same way gqlgen validates it
// server-side.
func TestGraphQLOperationsMatchSchema(t *testing.T) {
	schemavalidate.Validate(t, []schemavalidate.Operation{
		{Name: "categoriesListQuery", Query: categoriesListQuery},
		{Name: "watchCategoriesSubscription", Query: watchCategoriesSubscription},
		{Name: "namespacesListQuery", Query: namespacesListQuery},
		{Name: "productsListQueryByNamespace", Query: productsListQueryByNamespace},
		{Name: "watchProductsSubscription", Query: watchProductsSubscription},
		{Name: "namespacesControllerListQuery", Query: namespacesControllerListQuery},
		{Name: "watchNamespacesSubscription", Query: watchNamespacesSubscription},
		{Name: "repositoriesControllerListQuery", Query: repositoriesControllerListQuery},
		{Name: "watchRepositoriesSubscription", Query: watchRepositoriesSubscription},
	})
}
