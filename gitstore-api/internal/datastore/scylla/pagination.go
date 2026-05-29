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

// buildPaginatedSelect constructs a CQL SELECT with keyset pagination.
// It uses tuple inequality comparisons (created_at, id) < (?, ?) for forward
// pagination and (created_at, id) > (?, ?) for backward, with reversed ORDER BY.
//
// extraWhere adds additional WHERE clauses (e.g., "namespace_id = ?") and
// extraArgs provides corresponding bind values.
func buildPaginatedSelect(tbl *table.Table, page datastore.PageParams, extraWhere []string, extraArgs []any) paginatedQuery {
	limit := page.Limit()
	fetchLimit := limit + 1

	cols := strings.Join(tbl.Metadata().Columns, ", ")
	tableName := tbl.Metadata().Name

	var whereParts []string
	var args []any

	// Partition key: bucket = ?
	whereParts = append(whereParts, "bucket = ?")
	args = append(args, BucketAll)

	// Additional WHERE clauses (e.g., namespace_id = ?)
	whereParts = append(whereParts, extraWhere...)
	args = append(args, extraArgs...)

	// Cursor-based tuple inequality
	backward := page.Last > 0 && page.Before != ""
	if page.After != "" && !backward {
		cursor, err := parsePageCursor(page.After)
		if err == nil {
			whereParts = append(whereParts, "(created_at, id) < (?, ?)")
			args = append(args, cursor.CreatedAt, mustParseUUID(cursor.ID))
		}
	} else if backward {
		cursor, err := parsePageCursor(page.Before)
		if err == nil {
			whereParts = append(whereParts, "(created_at, id) > (?, ?)")
			args = append(args, cursor.CreatedAt, mustParseUUID(cursor.ID))
		}
	}

	orderClause := "ORDER BY created_at DESC, id DESC"
	if backward {
		orderClause = "ORDER BY created_at ASC, id ASC"
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
