// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v3/table"
)

// encodeKeysetCursor encodes a keyset position as an opaque base64 cursor.
// The format mirrors the graph-layer EncodeKeysetCursor so cursors are interchangeable.
func encodeKeysetCursor(ts time.Time, id string) string {
	payload := fmt.Sprintf("keyset|%s|%s", ts.Format(time.RFC3339Nano), id)
	return base64.StdEncoding.EncodeToString([]byte(payload))
}

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
	if _, err := gocql.ParseUUID(parts[2]); err != nil {
		return nil, fmt.Errorf("invalid cursor id: %w", err)
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
type clusterKeys struct {
	TimestampCol string
	IDCol        string
}

var namespaceClusterKeys = clusterKeys{TimestampCol: "creation_timestamp", IDCol: "uid"}
var productClusterKeys = clusterKeys{TimestampCol: "creation_timestamp", IDCol: "uid"}
var collectionClusterKeys = clusterKeys{TimestampCol: "creation_timestamp", IDCol: "uid"}

const namespaceBucketLayout = "2006-01"

var namespaceListingEpoch = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

func namespaceBucket(timestamp time.Time) string {
	return timestamp.UTC().Format(namespaceBucketLayout)
}

func cursorInNamespaceBucket(cursor, bucket string) bool {
	parsed, err := parsePageCursor(cursor)
	return err == nil && namespaceBucket(parsed.CreatedAt) == bucket
}

func namespaceBucketsForPage(page datastore.PageParams, now time.Time) []string {
	current := monthStart(now)
	epoch := monthStart(namespaceListingEpoch)
	backward := page.Last > 0

	if backward {
		start := epoch
		if page.Before != "" {
			if cursor, err := parsePageCursor(page.Before); err == nil {
				start = monthStart(cursor.CreatedAt)
			}
		}
		if start.After(current) {
			return nil
		}
		buckets := make([]string, 0, monthsBetween(start, current)+1)
		for month := start; !month.After(current); month = month.AddDate(0, 1, 0) {
			buckets = append(buckets, namespaceBucket(month))
		}
		return buckets
	}

	start := current
	if page.After != "" {
		if cursor, err := parsePageCursor(page.After); err == nil {
			start = monthStart(cursor.CreatedAt)
		}
	}
	if start.Before(epoch) {
		return nil
	}
	buckets := make([]string, 0, monthsBetween(epoch, start)+1)
	for month := start; !month.Before(epoch); month = month.AddDate(0, -1, 0) {
		buckets = append(buckets, namespaceBucket(month))
	}
	return buckets
}

func monthStart(timestamp time.Time) time.Time {
	utc := timestamp.UTC()
	return time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func monthsBetween(start, end time.Time) int {
	return (end.Year()-start.Year())*12 + int(end.Month()-start.Month())
}

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
