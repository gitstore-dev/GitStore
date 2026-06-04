// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/scylladb/gocqlx/v3/table"
)

// parsePageCursor decodes an opaque base64 keyset cursor into a PageCursor.
func parsePageCursor(cursor string) (*datastore.PageCursor, error) {
	decoded, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return nil, fmt.Errorf("invalid base64: %w", err)
	}
	parts := strings.SplitN(string(decoded), "|", 3)
	if len(parts) != 3 || parts[0] != "keyset" {
		return nil, fmt.Errorf("invalid cursor format")
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp: %w", err)
	}
	return &datastore.PageCursor{CreatedAt: ts, ID: parts[2]}, nil
}

// paginatedQuery holds the CQL statement and positional bind values
// for a paginated list query using keyset (tuple inequality) pagination.
type paginatedQuery struct {
	Stmt string
	Args []any
}

// clusterKeys names the two clustering columns used for keyset pagination.
// The default is ("created_at", "id") which matches categories/collections/namespaces/repositories.
// Products use ("creation_timestamp", "uid").
type clusterKeys struct {
	TimestampCol string
	IDCol        string
}

var defaultClusterKeys = clusterKeys{TimestampCol: "created_at", IDCol: "id"}
var productClusterKeys = clusterKeys{TimestampCol: "creation_timestamp", IDCol: "uid"}

// buildPaginatedSelect constructs a CQL SELECT with keyset pagination.
// It uses tuple inequality comparisons on the two clustering columns for forward
// pagination and the reverse predicate for backward, with reversed ORDER BY.
//
// partitionCol is the partition key column name (e.g. "bucket" or "namespace").
// partitionVal is the bind value for that column.
// ck specifies the clustering column names; pass defaultClusterKeys for all entities
// except products (which use productClusterKeys).
// extraWhere adds additional WHERE clauses and extraArgs provides their bind values.
func buildPaginatedSelect(tbl *table.Table, page datastore.PageParams, partitionCol string, partitionVal any, ck clusterKeys, extraWhere []string, extraArgs []any) paginatedQuery {
	limit := page.Limit()
	fetchLimit := limit + 1

	cols := strings.Join(tbl.Metadata().Columns, ", ")
	tableName := tbl.Metadata().Name

	var whereParts []string
	var args []any

	// Partition key predicate
	whereParts = append(whereParts, partitionCol+" = ?")
	args = append(args, partitionVal)

	// Additional WHERE clauses
	whereParts = append(whereParts, extraWhere...)
	args = append(args, extraArgs...)

	// Cursor-based tuple inequality
	backward := page.Last > 0
	ltPredicate := fmt.Sprintf("(%s, %s) < (?, ?)", ck.TimestampCol, ck.IDCol)
	gtPredicate := fmt.Sprintf("(%s, %s) > (?, ?)", ck.TimestampCol, ck.IDCol)
	if page.After != "" && !backward {
		cursor, err := parsePageCursor(page.After)
		if err == nil {
			whereParts = append(whereParts, ltPredicate)
			args = append(args, cursor.CreatedAt, mustParseUUID(cursor.ID))
		}
	} else if backward && page.Before != "" {
		cursor, err := parsePageCursor(page.Before)
		if err == nil {
			whereParts = append(whereParts, gtPredicate)
			args = append(args, cursor.CreatedAt, mustParseUUID(cursor.ID))
		}
	}

	orderClause := fmt.Sprintf("ORDER BY %s DESC, %s DESC", ck.TimestampCol, ck.IDCol)
	if backward {
		orderClause = fmt.Sprintf("ORDER BY %s ASC, %s ASC", ck.TimestampCol, ck.IDCol)
	}

	stmt := fmt.Sprintf("SELECT %s FROM %s WHERE %s %s LIMIT %d",
		cols, tableName,
		strings.Join(whereParts, " AND "),
		orderClause,
		fetchLimit,
	)

	return paginatedQuery{Stmt: stmt, Args: args}
}

// buildPageResult trims the N+1 row and computes HasNext/HasPrevious.
func buildPageResult[T any](items []*T, limit int, page datastore.PageParams) *datastore.PageResult[T] {
	hasExtra := len(items) > limit
	hasNext := false
	hasPrevious := false

	if page.Last > 0 {
		// Backward pagination
		if hasExtra {
			items = items[1:] // trim the extra at the start (it was reversed)
			hasPrevious = true
		}
		hasNext = page.Before != "" // if we used a before cursor, there are items after
	} else {
		// Forward pagination
		if hasExtra {
			items = items[:limit]
			hasNext = true
		}
		hasPrevious = page.After != "" // if we used an after cursor, there are items before
	}

	return &datastore.PageResult[T]{
		Items:       items,
		HasNext:     hasNext,
		HasPrevious: hasPrevious,
		TotalCount:  -1, // expensive to compute in ScyllaDB
	}
}

// paginateInMemory applies cursor-based keyset pagination to a pre-sorted slice
// (sorted by created_at DESC, id DESC). Used when ORDER BY is not supported in CQL
// (e.g., secondary index queries).
func paginateInMemory(items []*datastore.Repository, page datastore.PageParams) *datastore.PageResult[datastore.Repository] {
	total := len(items)
	limit := page.Limit()

	// Apply cursor filtering
	if page.After != "" {
		cursor, err := parsePageCursor(page.After)
		if err == nil {
			idx := -1
			for i, item := range items {
				if item.CreatedAt.Equal(cursor.CreatedAt) && item.ID == cursor.ID {
					idx = i
					break
				}
			}
			if idx >= 0 {
				items = items[idx+1:]
			}
		}
	} else if page.Before != "" {
		cursor, err := parsePageCursor(page.Before)
		if err == nil {
			idx := -1
			for i, item := range items {
				if item.CreatedAt.Equal(cursor.CreatedAt) && item.ID == cursor.ID {
					idx = i
					break
				}
			}
			if idx >= 0 {
				items = items[:idx]
			}
		}
	}

	hasNext := false
	hasPrevious := false

	if page.Last > 0 {
		// Take the last N items
		if len(items) > limit {
			items = items[len(items)-limit:]
			hasPrevious = true
		}
		hasNext = page.Before != ""
	} else {
		// Take the first N items
		if len(items) > limit {
			items = items[:limit]
			hasNext = true
		}
		hasPrevious = page.After != ""
	}

	return &datastore.PageResult[datastore.Repository]{
		Items:       items,
		HasNext:     hasNext,
		HasPrevious: hasPrevious,
		TotalCount:  int32(total),
	}
}
