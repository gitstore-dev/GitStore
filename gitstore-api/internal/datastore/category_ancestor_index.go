// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const closureCursorPrefix = "closure"

var (
	// CategoryAncestorIndexWritesTotal counts ancestor-index projection
	// writes by outcome (applied, skipped_newer, failed).
	CategoryAncestorIndexWritesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gitstore_category_ancestor_index_writes_total",
		Help: "CategoryTaxonomy ancestor index row writes by result.",
	}, []string{"result"})

	// CategoryAncestorIndexRepairRequiredTotal counts status or delete
	// mutations whose index projection writes failed after retry.
	CategoryAncestorIndexRepairRequiredTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gitstore_category_ancestor_index_repair_required_total",
		Help: "CategoryTaxonomy ancestor index mutations that require projection repair.",
	})
)

// EncodeClosureCursor encodes a ListCategoryDescendants position.
func EncodeClosureCursor(depth int, name string) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s|%d|%s", closureCursorPrefix, depth, name)))
}

// DecodeClosureCursor decodes a closure cursor. Any other cursor (including a
// keyset cursor) fails with ErrInvalidArgument.
func DecodeClosureCursor(cursor string) (depth int, name string, err error) {
	decoded, decodeErr := base64.StdEncoding.DecodeString(cursor)
	if decodeErr != nil {
		return 0, "", fmt.Errorf("%w: invalid cursor", ErrInvalidArgument)
	}
	parts := strings.SplitN(string(decoded), "|", 3)
	if len(parts) != 3 || parts[0] != closureCursorPrefix || parts[2] == "" {
		return 0, "", fmt.Errorf("%w: cursor is not a category descendant cursor", ErrInvalidArgument)
	}
	depth, convErr := strconv.Atoi(parts[1])
	if convErr != nil || depth < 0 || depth > MaxCategoryHierarchyDepth {
		return 0, "", fmt.Errorf("%w: invalid category descendant cursor depth", ErrInvalidArgument)
	}
	return depth, parts[2], nil
}

// IsClosureCursor reports whether cursor is a well-formed closure cursor.
func IsClosureCursor(cursor string) bool {
	_, _, err := DecodeClosureCursor(cursor)
	return err == nil
}

// RejectClosureCursors fails with ErrInvalidArgument if a closure cursor is
// supplied to a keyset-paginated list. Keyset readers otherwise ignore cursors
// they cannot parse, which would silently restart the listing.
func RejectClosureCursors(page PageParams) error {
	if IsClosureCursor(page.After) || IsClosureCursor(page.Before) {
		return fmt.Errorf("%w: category descendant cursor supplied to a keyset list", ErrInvalidArgument)
	}
	return nil
}

// CategoryDescendantBounds validates q and returns its inclusive depth range
// and decoded cursor. cursorDepth is -1 when no cursor was supplied.
func CategoryDescendantBounds(q CategoryDescendantQuery) (lo, hi, cursorDepth int, cursorName string, err error) {
	if q.Namespace == "" || q.Ancestor == "" {
		return 0, 0, 0, "", fmt.Errorf("%w: namespace and ancestor are required", ErrInvalidArgument)
	}
	if q.MaxDepth < 0 || q.MaxDepth > MaxCategoryHierarchyDepth {
		return 0, 0, 0, "", fmt.Errorf("%w: maxDepth must be between 0 and %d", ErrInvalidArgument, MaxCategoryHierarchyDepth)
	}
	if q.Page.After != "" && q.Page.Before != "" {
		return 0, 0, 0, "", fmt.Errorf("%w: after and before are mutually exclusive", ErrInvalidArgument)
	}
	lo, hi = 1, MaxCategoryHierarchyDepth
	if q.IncludeSelf {
		lo = 0
	}
	if q.MaxDepth > 0 {
		hi = q.MaxDepth
	}
	cursorDepth = -1
	cursor := q.Page.After
	if cursor == "" {
		cursor = q.Page.Before
	}
	if cursor != "" {
		cursorDepth, cursorName, err = DecodeClosureCursor(cursor)
		if err != nil {
			return 0, 0, 0, "", err
		}
	}
	return lo, hi, cursorDepth, cursorName, nil
}

// CategoryAncestorRow is one (ancestor, depth) membership of a descendant.
type CategoryAncestorRow struct {
	Ancestor string
	Depth    int
}

// CategoryAncestorRows derives the index rows owned by the category name with
// root-to-self status.resolved.path. For path [a0..ak] the rows are
// {(ai, k-i)}; the self row (depth 0) always uses name. An empty path owns
// no rows.
func CategoryAncestorRows(name string, path []string) ([]CategoryAncestorRow, error) {
	if len(path) == 0 {
		return nil, nil
	}
	k := len(path) - 1
	if k > MaxCategoryHierarchyDepth {
		return nil, fmt.Errorf("%w: category path depth %d exceeds %d", ErrInvalidArgument, k, MaxCategoryHierarchyDepth)
	}
	rows := make([]CategoryAncestorRow, 0, len(path))
	for i, ancestor := range path {
		if i == k {
			ancestor = name
		}
		if ancestor == "" {
			return nil, fmt.Errorf("%w: category path contains an empty segment", ErrInvalidArgument)
		}
		rows = append(rows, CategoryAncestorRow{Ancestor: ancestor, Depth: k - i})
	}
	return rows, nil
}

// CategoryResolvedPath extracts status.resolved.path from a stored status
// document. Empty or resolved-less status yields nil.
func CategoryResolvedPath(status string) ([]string, error) {
	if status == "" || status == "null" {
		return nil, nil
	}
	var parsed catalog.CategoryTaxonomyStatus
	if err := json.Unmarshal([]byte(status), &parsed); err != nil {
		return nil, fmt.Errorf("datastore: decode category status: %w", err)
	}
	if parsed.Resolved == nil {
		return nil, nil
	}
	return parsed.Resolved.Path, nil
}

// ObsoleteCategoryAncestorRows returns rows in previous whose (ancestor,
// depth) key is absent from current.
func ObsoleteCategoryAncestorRows(previous, current []CategoryAncestorRow) []CategoryAncestorRow {
	keep := make(map[CategoryAncestorRow]struct{}, len(current))
	for _, row := range current {
		keep[row] = struct{}{}
	}
	var obsolete []CategoryAncestorRow
	for _, row := range previous {
		if _, ok := keep[row]; !ok {
			obsolete = append(obsolete, row)
		}
	}
	return obsolete
}
