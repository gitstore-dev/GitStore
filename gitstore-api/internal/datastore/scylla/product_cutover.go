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

// Migration creates new tables, not a data transfer. Refuse to serve a
// success-shaped empty catalog when legacy authoritative rows still exist.
func validateProductCutover(ctx context.Context, session *gocql.Session, keyspace string) error {
	var table string
	err := session.Query("SELECT table_name FROM system_schema.tables WHERE keyspace_name=? AND table_name=?",
		keyspace, "products_by_namespace").WithContext(ctx).Scan(&table)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scylla: inspect Product cutover: %w", err)
	}
	var uid gocql.UUID
	err = session.Query("SELECT uid FROM products_by_namespace LIMIT 1").WithContext(ctx).Scan(&uid)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scylla: validate Product cutover: %w", err)
	}
	return fmt.Errorf("%w: legacy Product rows require an explicit offline cutover to a fresh keyspace; migration does not transfer Product data", datastore.ErrRepairRequired)
}
