// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	gomemdb "github.com/hashicorp/go-memdb"
)

// categoryAncestorRow is one row of the derived ancestor closure index. It is
// written in the same transaction as the category status or delete that
// produced it; callers hold categoryMutationMu, so there is no concurrent
// writer and the higher resource version trivially wins.
type categoryAncestorRow struct {
	ID              string // namespace/ancestor/depth/descendant
	Namespace       string
	Ancestor        string
	Depth           int
	Descendant      string
	DescendantUID   string
	ResourceVersion string
}

func categoryAncestorRowID(namespace, ancestor string, depth int, descendant string) string {
	return namespace + "/" + ancestor + "/" + strconv.Itoa(depth) + "/" + descendant
}

// syncCategoryAncestorIndex rewrites the category's rows to match
// status.resolved.path: upsert current rows, delete rows whose (ancestor,
// depth) is no longer present.
func syncCategoryAncestorIndex(txn *gomemdb.Txn, category *datastore.CategoryTaxonomy) error {
	path, err := datastore.CategoryResolvedPath(string(category.Status))
	if err != nil {
		return err
	}
	current, err := datastore.CategoryAncestorRows(category.Name, path)
	if err != nil {
		return err
	}
	if err := deleteStaleCategoryAncestorRows(txn, category, current); err != nil {
		return err
	}
	for _, row := range current {
		stored := &categoryAncestorRow{
			ID:        categoryAncestorRowID(category.Namespace, row.Ancestor, row.Depth, category.Name),
			Namespace: category.Namespace, Ancestor: row.Ancestor, Depth: row.Depth,
			Descendant: category.Name, DescendantUID: category.UID, ResourceVersion: category.ResourceVersion,
		}
		if err := txn.Insert("category_ancestor_index", stored); err != nil {
			datastore.CategoryAncestorIndexWritesTotal.WithLabelValues("failed").Inc()
			return fmt.Errorf("memdb: upsert category ancestor index: %w", err)
		}
		datastore.CategoryAncestorIndexWritesTotal.WithLabelValues("applied").Inc()
	}
	return nil
}

// deleteStaleCategoryAncestorRows removes the descendant's rows not in keep.
func deleteStaleCategoryAncestorRows(txn *gomemdb.Txn, category *datastore.CategoryTaxonomy, keep []datastore.CategoryAncestorRow) error {
	it, err := txn.Get("category_ancestor_index", "descendant", category.Namespace, category.Name)
	if err != nil {
		return fmt.Errorf("memdb: read category ancestor index: %w", err)
	}
	keepSet := make(map[datastore.CategoryAncestorRow]struct{}, len(keep))
	for _, row := range keep {
		keepSet[row] = struct{}{}
	}
	var stale []*categoryAncestorRow
	for obj := it.Next(); obj != nil; obj = it.Next() {
		row := obj.(*categoryAncestorRow)
		if _, ok := keepSet[datastore.CategoryAncestorRow{Ancestor: row.Ancestor, Depth: row.Depth}]; !ok {
			stale = append(stale, row)
		}
	}
	for _, row := range stale {
		if err := txn.Delete("category_ancestor_index", row); err != nil {
			return fmt.Errorf("memdb: delete category ancestor index: %w", err)
		}
	}
	return nil
}

// deleteCategoryAncestorRows removes every row owned by the category.
func deleteCategoryAncestorRows(txn *gomemdb.Txn, category *datastore.CategoryTaxonomy) error {
	return deleteStaleCategoryAncestorRows(txn, category, nil)
}

func (m *memdbDatastore) ListCategoryDescendants(_ context.Context, q datastore.CategoryDescendantQuery) (*datastore.PageResult[datastore.CategoryDescendant], error) {
	lo, hi, cursorDepth, cursorName, err := datastore.CategoryDescendantBounds(q)
	if err != nil {
		return nil, err
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get("category_ancestor_index", "ancestor", q.Namespace, q.Ancestor)
	if err != nil {
		return nil, fmt.Errorf("memdb: list category descendants: %w", err)
	}
	var rows []*categoryAncestorRow
	for obj := it.Next(); obj != nil; obj = it.Next() {
		row := obj.(*categoryAncestorRow)
		if row.Depth >= lo && row.Depth <= hi {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Depth != rows[j].Depth {
			return rows[i].Depth < rows[j].Depth
		}
		return rows[i].Descendant < rows[j].Descendant
	})
	after := func(r *categoryAncestorRow) bool { // strictly after the cursor
		return r.Depth > cursorDepth || (r.Depth == cursorDepth && r.Descendant > cursorName)
	}
	limit := q.Page.Limit()
	backward := q.Page.Last > 0
	var window []*categoryAncestorRow
	for _, row := range rows {
		switch {
		case cursorDepth < 0:
			window = append(window, row)
		case !backward && after(row):
			window = append(window, row)
		case backward && !after(row) && !(row.Depth == cursorDepth && row.Descendant == cursorName):
			window = append(window, row)
		}
	}
	hasNext, hasPrevious := false, false
	if backward {
		if len(window) > limit {
			window = window[len(window)-limit:]
			hasPrevious = true
		}
		hasNext = q.Page.Before != ""
	} else {
		if len(window) > limit {
			window = window[:limit]
			hasNext = true
		}
		hasPrevious = q.Page.After != ""
	}
	items := make([]*datastore.CategoryDescendant, 0, len(window))
	for _, row := range window {
		items = append(items, &datastore.CategoryDescendant{Name: row.Descendant, UID: row.DescendantUID, Depth: row.Depth})
	}
	return &datastore.PageResult[datastore.CategoryDescendant]{Items: items, HasNext: hasNext, HasPrevious: hasPrevious}, nil
}
