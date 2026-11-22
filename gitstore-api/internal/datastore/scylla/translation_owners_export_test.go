// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

func ReplayTranslationOwnersForTest(ctx context.Context, backend datastore.Datastore, coordination string, previous, current *datastore.Translation) error {
	return backend.(*scyllaDatastore).syncTranslationOwners(ctx, coordination, previous, current)
}

func ReplayTranslationProjectionsForTest(ctx context.Context, backend datastore.Datastore, coordination string, previous, current *datastore.Translation) error {
	return backend.(*scyllaDatastore).syncTranslationSourceProjections(ctx, coordination, previous, current)
}
