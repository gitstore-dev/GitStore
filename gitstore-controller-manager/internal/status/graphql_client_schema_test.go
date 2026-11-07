// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package status

import (
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/schemavalidate"
)

// TestGraphQLOperationsMatchSchema validates every status mutation string
// this package sends against the real schema (shared/schemas/*.graphqls),
// the same way gqlgen validates it server-side.
func TestGraphQLOperationsMatchSchema(t *testing.T) {
	schemavalidate.Validate(t, []schemavalidate.Operation{
		{Name: "updateCategoryStatusMutation", Query: updateCategoryStatusMutation},
		{Name: "updateProductStatusMutation", Query: updateProductStatusMutation},
		{Name: "updateRepositoryStatusMutation", Query: updateRepositoryStatusMutation},
		{Name: "updateNamespaceStatusMutation", Query: updateNamespaceStatusMutation},
		{Name: "updateResourceStatusMutation", Query: updateResourceStatusMutation},
	})
}
