// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package repository

import (
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/schemavalidate"
)

// TestGraphQLOperationsMatchSchema validates every operation string this
// package sends against the real schema (shared/schemas/*.graphqls), the
// same way gqlgen validates it server-side.
func TestGraphQLOperationsMatchSchema(t *testing.T) {
	schemavalidate.Validate(t, []schemavalidate.Operation{
		{Name: "completeRepositoryDeletionMutation", Query: completeRepositoryDeletionMutation},
		{Name: "provisionRepositoryStorageMutation", Query: provisionRepositoryStorageMutation},
	})
}
