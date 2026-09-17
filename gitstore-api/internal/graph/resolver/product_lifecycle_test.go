// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
)

// productLifecycleFixture is deliberately resource-complete. Subsequent
// lifecycle tests use it to distinguish Git-owned provenance from system-owned
// deletion metadata without relying on production fixtures.
func productLifecycleFixture(namespace, name string) *datastore.Product {
	now := time.Now().UTC()
	return &datastore.Product{
		UID:               uuid.NewString(),
		Namespace:         namespace,
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Product",
		Generation:        1,
		ResourceVersion:   "1",
		CreationTimestamp: now,
		UpdateTimestamp:   now,
		RepositoryID:      uuid.NewString(),
		SourcePath:        "products/" + name + ".yaml",
		GitRef:            "refs/heads/main",
		GitCommitSHA:      "0123456789abcdef",
	}
}
