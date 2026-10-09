// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
)

const (
	categoryAncestorIndexTable = "category_ancestor_index"

	// categoryAncestorWriteAttempts bounds the read/compare/CAS loop used when
	// concurrent writers touch the same index row.
	categoryAncestorWriteAttempts = 5
)

// Concurrent-writer policy: every index row carries the resource_version of
// the category write that produced it. A writer reads the row, skips if the
// stored version is already >= its own (for the same UID), and otherwise
// applies a conditional write (INSERT IF NOT EXISTS / UPDATE IF
// resource_version = <read value>), retrying when the condition fails. The
// comparison is numeric because resource versions are decimal strings, which
// a CQL `IF resource_version < ?` would order lexicographically. The higher
// version therefore wins regardless of the order projection writes land in.

type categoryAncestorQueryRow struct {
	Depth         int8       `db:"depth"`
	Descendant    string     `db:"descendant"`
	DescendantUID gocql.UUID `db:"descendant_uid"`
}

type categoryAncestorStoredRow struct {
	DescendantUID   gocql.UUID `db:"descendant_uid"`
	ResourceVersion string     `db:"resource_version"`
}

// compareResourceVersions orders decimal resource versions numerically,
// falling back to string order for non-numeric values.
func compareResourceVersions(a, b string) int {
	left, leftOK := new(big.Int).SetString(a, 10)
	right, rightOK := new(big.Int).SetString(b, 10)
	if leftOK && rightOK {
		return left.Cmp(right)
	}
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (s *scyllaDatastore) readCategoryAncestorRow(ctx context.Context, namespace, ancestor string, depth int, descendant string) (*categoryAncestorStoredRow, error) {
	const stmt = `SELECT descendant_uid, resource_version FROM category_ancestor_index
		WHERE namespace=? AND ancestor=? AND depth=? AND descendant=?`
	var row categoryAncestorStoredRow
	if err := s.session.Query(stmt, nil).WithContext(ctx).Bind(namespace, ancestor, int8(depth), descendant).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// upsertCategoryAncestorRow writes one row unless a same-UID row with a
// resource version >= resourceVersion is already present.
func (s *scyllaDatastore) upsertCategoryAncestorRow(ctx context.Context, namespace, ancestor string, depth int, descendant string, uid gocql.UUID, resourceVersion string) error {
	for attempt := 0; attempt < categoryAncestorWriteAttempts; attempt++ {
		current, err := s.readCategoryAncestorRow(ctx, namespace, ancestor, depth, descendant)
		if err != nil {
			return fmt.Errorf("scylla: read category ancestor index: %w", err)
		}
		var applied bool
		if current == nil {
			const insert = `INSERT INTO category_ancestor_index
				(namespace, ancestor, depth, descendant, descendant_uid, resource_version)
				VALUES (?, ?, ?, ?, ?, ?) IF NOT EXISTS`
			applied, err = s.session.Query(insert, nil).WithContext(ctx).Bind(
				namespace, ancestor, int8(depth), descendant, uid, resourceVersion,
			).ExecCASRelease()
		} else {
			if current.DescendantUID == uid && compareResourceVersions(current.ResourceVersion, resourceVersion) >= 0 {
				datastore.CategoryAncestorIndexWritesTotal.WithLabelValues("skipped_newer").Inc()
				return nil
			}
			const update = `UPDATE category_ancestor_index SET descendant_uid=?, resource_version=?
				WHERE namespace=? AND ancestor=? AND depth=? AND descendant=? IF resource_version=?`
			applied, err = s.session.Query(update, nil).WithContext(ctx).Bind(
				uid, resourceVersion, namespace, ancestor, int8(depth), descendant, current.ResourceVersion,
			).ExecCASRelease()
		}
		if err != nil {
			return fmt.Errorf("scylla: upsert category ancestor index: %w", err)
		}
		if applied {
			datastore.CategoryAncestorIndexWritesTotal.WithLabelValues("applied").Inc()
			return nil
		}
	}
	return fmt.Errorf("scylla: upsert category ancestor index %s/%s/%d/%s: contention", namespace, ancestor, depth, descendant)
}

// deleteObsoleteCategoryAncestorRow removes a row left by an older path. It
// is skipped when the row belongs to another UID or was rewritten by a write
// with a version >= resourceVersion (a newer path that still contains it).
func (s *scyllaDatastore) deleteObsoleteCategoryAncestorRow(ctx context.Context, namespace, ancestor string, depth int, descendant string, uid gocql.UUID, resourceVersion string) error {
	for attempt := 0; attempt < categoryAncestorWriteAttempts; attempt++ {
		current, err := s.readCategoryAncestorRow(ctx, namespace, ancestor, depth, descendant)
		if err != nil {
			return fmt.Errorf("scylla: read category ancestor index: %w", err)
		}
		if current == nil || current.DescendantUID != uid || compareResourceVersions(current.ResourceVersion, resourceVersion) >= 0 {
			return nil
		}
		const stmt = `DELETE FROM category_ancestor_index
			WHERE namespace=? AND ancestor=? AND depth=? AND descendant=? IF resource_version=?`
		applied, err := s.session.Query(stmt, nil).WithContext(ctx).Bind(
			namespace, ancestor, int8(depth), descendant, current.ResourceVersion,
		).ExecCASRelease()
		if err != nil {
			return fmt.Errorf("scylla: delete category ancestor index: %w", err)
		}
		if applied {
			datastore.CategoryAncestorIndexWritesTotal.WithLabelValues("applied").Inc()
			return nil
		}
	}
	return fmt.Errorf("scylla: delete category ancestor index %s/%s/%d/%s: contention", namespace, ancestor, depth, descendant)
}

// syncCategoryAncestorRows upserts the current rows and deletes the obsolete
// ones. It is idempotent, so executeUpdate may retry it.
func (s *scyllaDatastore) syncCategoryAncestorRows(ctx context.Context, c *datastore.CategoryTaxonomy, current, obsolete []datastore.CategoryAncestorRow) error {
	uid := mustParseUUID(c.UID)
	for _, row := range current {
		if err := s.upsertCategoryAncestorRow(ctx, c.Namespace, row.Ancestor, row.Depth, c.Name, uid, c.ResourceVersion); err != nil {
			datastore.CategoryAncestorIndexWritesTotal.WithLabelValues("failed").Inc()
			return err
		}
	}
	for _, row := range obsolete {
		if err := s.deleteObsoleteCategoryAncestorRow(ctx, c.Namespace, row.Ancestor, row.Depth, c.Name, uid, c.ResourceVersion); err != nil {
			datastore.CategoryAncestorIndexWritesTotal.WithLabelValues("failed").Inc()
			return err
		}
	}
	return nil
}

// categoryAncestorRowsFor derives the rows a category currently owns from its
// stored status.
func categoryAncestorRowsFor(c *datastore.CategoryTaxonomy) ([]datastore.CategoryAncestorRow, error) {
	path, err := datastore.CategoryResolvedPath(string(c.Status))
	if err != nil {
		return nil, err
	}
	return datastore.CategoryAncestorRows(c.Name, path)
}

// removeCategoryAncestorRows deletes every row the category owns (projection
// side of final removal). Rows are removed only while they still belong to
// the category's UID.
func (s *scyllaDatastore) removeCategoryAncestorRows(ctx context.Context, c *datastore.CategoryTaxonomy) error {
	rows, err := categoryAncestorRowsFor(c)
	if err != nil {
		return err
	}
	uid := mustParseUUID(c.UID)
	const stmt = `DELETE FROM category_ancestor_index
		WHERE namespace=? AND ancestor=? AND depth=? AND descendant=? IF descendant_uid=?`
	for _, row := range rows {
		if _, err := s.session.Query(stmt, nil).WithContext(ctx).Bind(
			c.Namespace, row.Ancestor, int8(row.Depth), c.Name, uid,
		).ExecCASRelease(); err != nil {
			return fmt.Errorf("scylla: delete category ancestor index: %w", err)
		}
	}
	return nil
}

// restoreCategoryAncestorRows re-inserts the rows removed by a delete whose
// later step failed before the authoritative row was removed.
func (s *scyllaDatastore) restoreCategoryAncestorRows(ctx context.Context, c *datastore.CategoryTaxonomy) error {
	rows, err := categoryAncestorRowsFor(c)
	if err != nil {
		return err
	}
	uid := mustParseUUID(c.UID)
	for _, row := range rows {
		if err := s.upsertCategoryAncestorRow(ctx, c.Namespace, row.Ancestor, row.Depth, c.Name, uid, c.ResourceVersion); err != nil {
			return err
		}
	}
	return nil
}

// ListCategoryDescendants reads an ancestor's subtree ordered by (depth,
// descendant). Scylla rejects mixing a tuple relation with a single-column
// restriction on the same clustering column, so a cursor is applied as two
// ordinary range segments instead of one `(depth, descendant) > (?, ?)`
// predicate: the remainder of the cursor's depth, then the following depths.
func (s *scyllaDatastore) ListCategoryDescendants(ctx context.Context, q datastore.CategoryDescendantQuery) (*datastore.PageResult[datastore.CategoryDescendant], error) {
	lo, hi, cursorDepth, cursorName, err := datastore.CategoryDescendantBounds(q)
	if err != nil {
		return nil, err
	}
	backward := q.Page.Last > 0
	limit := q.Page.Limit()

	type segment struct {
		where string
		args  []any
	}
	var segments []segment
	rangeSegment := func(from, to int) {
		if from <= to {
			segments = append(segments, segment{"depth >= ? AND depth <= ?", []any{int8(from), int8(to)}})
		}
	}
	switch {
	case cursorDepth < 0:
		rangeSegment(lo, hi)
	case !backward:
		if cursorDepth >= lo && cursorDepth <= hi {
			segments = append(segments, segment{"depth = ? AND descendant > ?", []any{int8(cursorDepth), cursorName}})
		}
		rangeSegment(max(cursorDepth+1, lo), hi)
	default:
		if cursorDepth >= lo && cursorDepth <= hi {
			segments = append(segments, segment{"depth = ? AND descendant < ?", []any{int8(cursorDepth), cursorName}})
		}
		rangeSegment(lo, min(cursorDepth-1, hi))
	}

	order := "ORDER BY depth ASC, descendant ASC"
	if backward {
		order = "ORDER BY depth DESC, descendant DESC"
	}
	items := make([]*datastore.CategoryDescendant, 0, limit+1)
	for _, seg := range segments {
		remaining := limit + 1 - len(items)
		if remaining <= 0 {
			break
		}
		stmt := "SELECT depth, descendant, descendant_uid FROM " + categoryAncestorIndexTable +
			" WHERE namespace=? AND ancestor=? AND " + seg.where + " " + order + " LIMIT ?"
		args := append([]any{q.Namespace, q.Ancestor}, seg.args...)
		args = append(args, remaining)
		var rows []categoryAncestorQueryRow
		if err := s.session.Query(stmt, nil).WithContext(ctx).Bind(args...).SelectRelease(&rows); err != nil {
			return nil, fmt.Errorf("scylla: list category descendants: %w", err)
		}
		for _, row := range rows {
			items = append(items, &datastore.CategoryDescendant{
				Name: row.Descendant, UID: row.DescendantUID.String(), Depth: int(row.Depth),
			})
		}
	}
	if backward {
		for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
			items[i], items[j] = items[j], items[i]
		}
	}
	return buildPageResult(items, limit, q.Page), nil
}
