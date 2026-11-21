// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
)

const namespaceCatalogFencesTable = "namespace_catalog_fences_by_uid"

type namespaceCatalogFence struct {
	Epoch   int64 `db:"epoch"`
	Pending int64 `db:"pending"`
}

func (s *scyllaDatastore) loadNamespaceCatalogFence(ctx context.Context, uid gocql.UUID) (namespaceCatalogFence, error) {
	fence, err := s.readNamespaceCatalogFence(ctx, uid)
	if !errors.Is(err, gocql.ErrNotFound) {
		return fence, err
	}
	applied, err := s.session.Query("INSERT INTO "+namespaceCatalogFencesTable+" (uid,epoch,pending) VALUES (?,?,?) IF NOT EXISTS", nil).
		WithContext(ctx).Idempotent(false).RetryPolicy(&gocql.SimpleRetryPolicy{}).
		Bind(uid, int64(0), int64(0)).ExecCASRelease()
	if err != nil || applied {
		return fence, err
	}
	return s.readNamespaceCatalogFence(ctx, uid)
}

func (s *scyllaDatastore) readNamespaceCatalogFence(ctx context.Context, uid gocql.UUID) (namespaceCatalogFence, error) {
	var row struct {
		Epoch   *int64 `db:"epoch"`
		Pending *int64 `db:"pending"`
	}
	err := s.session.Query("SELECT epoch,pending FROM "+namespaceCatalogFencesTable+" WHERE uid=?", nil).
		WithContext(ctx).Bind(uid).GetRelease(&row)
	if err != nil {
		return namespaceCatalogFence{}, err
	}
	if row.Epoch == nil || row.Pending == nil || *row.Epoch < 0 || *row.Pending < 0 || *row.Pending > 1 {
		return namespaceCatalogFence{}, fmt.Errorf("%w: namespace catalog fence %s has invalid state", datastore.ErrRepairRequired, uid)
	}
	return namespaceCatalogFence{Epoch: *row.Epoch, Pending: *row.Pending}, nil
}

func (s *scyllaDatastore) releaseNamespaceCatalog(ctx context.Context, namespaceUID string, epoch int64) error {
	// A retry could hide an applied release behind a later not-applied result.
	applied, err := s.session.Query(
		"UPDATE "+namespaceCatalogFencesTable+" SET pending=? WHERE uid=? IF epoch=? AND pending=?", nil,
	).WithContext(ctx).Idempotent(false).RetryPolicy(&gocql.SimpleRetryPolicy{}).
		Bind(int64(0), mustParseUUID(namespaceUID), epoch, int64(1)).ExecCASRelease()
	if err != nil {
		return fmt.Errorf("scylla: release namespace catalog reservation: %w", err)
	}
	if applied {
		return nil
	}
	current, err := s.readNamespaceCatalogFence(ctx, mustParseUUID(namespaceUID))
	if err != nil {
		return fmt.Errorf("scylla: confirm namespace catalog release: %w", err)
	}
	if current.Epoch > epoch || (current.Epoch == epoch && current.Pending == 0) {
		return nil
	}
	return fmt.Errorf("scylla: namespace catalog release did not match epoch %d: %w", epoch, datastore.ErrConflict)
}
