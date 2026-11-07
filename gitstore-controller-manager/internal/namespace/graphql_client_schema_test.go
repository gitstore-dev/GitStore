// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package namespace

import (
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/schemavalidate"
)

// TestGraphQLOperationsMatchSchema is the regression guard for the
// completeNamespaceDeletion `conflict` selection bug (84b7bb4 / #394):
// gqlgen rejects a selection on a field the schema no longer declares, so
// every operation string this package sends must stay valid against
// shared/schemas/*.graphqls.
func TestGraphQLOperationsMatchSchema(t *testing.T) {
	schemavalidate.Validate(t, []schemavalidate.Operation{
		{Name: "provisionNamespaceSystemRepositoryMutation", Query: provisionNamespaceSystemRepositoryMutation},
		{Name: "repositoriesExistQuery", Query: repositoriesExistQuery},
		{Name: "completeNamespaceDeletionMutation", Query: completeNamespaceDeletionMutation},
	})
}
