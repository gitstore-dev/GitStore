// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
)

const categoryProductsByCategoryTable = "category_products_by_category"
const categoryProductsByProductTable = "category_products_by_product"

const categoryProductShardCount = 32

func categoryProductShard(uid string) int8 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(uid))
	return int8(h.Sum32() % categoryProductShardCount)
}

type categoryProductRow struct {
	CreationTimestamp time.Time  `db:"creation_timestamp"`
	ProductUID        gocql.UUID `db:"product_uid"`
}
type categoryProductReverseRow struct {
	CategoryUID     string `db:"category_uid"`
	Shard           int8   `db:"shard"`
	ResourceVersion string `db:"resource_version"`
}

// ReplaceCategoryProductMembership replaces the small, resolved ancestor set
// for one Product. Product status is the authoritative fenced write; this
// projection operation is idempotent and repairable after a partial failure.
func (s *scyllaDatastore) ReplaceCategoryProductMembership(ctx context.Context, product *datastore.Product, categoryUIDs []string) error {
	if product == nil {
		return fmt.Errorf("%w: product is nil", datastore.ErrInvalidArgument)
	}
	uid, err := gocql.ParseUUID(product.UID)
	if err != nil {
		return fmt.Errorf("%w: invalid product uid", datastore.ErrInvalidArgument)
	}
	var old []categoryProductReverseRow
	if err := s.session.Query("SELECT category_uid, shard, resource_version FROM "+categoryProductsByProductTable+" WHERE namespace=? AND product_uid=?", nil).WithContext(ctx).Bind(product.Namespace, uid).SelectRelease(&old); err != nil {
		return fmt.Errorf("scylla: list product category memberships: %w", err)
	}
	seen := make(map[string]struct{}, len(categoryUIDs))
	shard := categoryProductShard(product.UID)
	for _, categoryUID := range categoryUIDs {
		if categoryUID == "" {
			continue
		}
		if _, ok := seen[categoryUID]; ok {
			continue
		}
		seen[categoryUID] = struct{}{}
		if err := s.upsertCategoryProductMembership(ctx, product, uid, categoryUID, shard); err != nil {
			return err
		}
	}
	for _, row := range old {
		if _, wanted := seen[row.CategoryUID]; wanted {
			continue
		}
		if err := s.deleteCategoryProductMembership(ctx, product, uid, row); err != nil {
			return err
		}
	}
	return s.fenceCategoryProductMembership(ctx, product, uid, seen)
}

const categoryProductWriteAttempts = 5

func (s *scyllaDatastore) upsertCategoryProductMembership(ctx context.Context, product *datastore.Product, uid gocql.UUID, categoryUID string, shard int8) error {
	for attempt := 0; attempt < categoryProductWriteAttempts; attempt++ {
		var current struct {
			ResourceVersion string `db:"resource_version"`
		}
		err := s.session.Query("SELECT resource_version FROM "+categoryProductsByProductTable+" WHERE namespace=? AND product_uid=? AND category_uid=?", nil).WithContext(ctx).Bind(product.Namespace, uid, categoryUID).GetRelease(&current)
		if err != nil && !errors.Is(err, gocql.ErrNotFound) {
			return fmt.Errorf("scylla: read category membership fence: %w", err)
		}
		if err == nil {
			switch compareResourceVersions(current.ResourceVersion, product.ResourceVersion) {
			case 1:
				return nil
			case 0:
				// The reverse write may have survived an interrupted forward write.
				return s.upsertCategoryProductForward(ctx, product, uid, categoryUID, shard)
			}
		}
		var applied bool
		if errors.Is(err, gocql.ErrNotFound) {
			applied, err = s.session.Query("INSERT INTO "+categoryProductsByProductTable+" (namespace,product_uid,category_uid,shard,resource_version) VALUES (?,?,?,?,?) IF NOT EXISTS", nil).WithContext(ctx).Bind(product.Namespace, uid, categoryUID, shard, product.ResourceVersion).ExecCASRelease()
		} else {
			applied, err = s.session.Query("UPDATE "+categoryProductsByProductTable+" SET shard=?,resource_version=? WHERE namespace=? AND product_uid=? AND category_uid=? IF resource_version=?", nil).WithContext(ctx).Bind(shard, product.ResourceVersion, product.Namespace, uid, categoryUID, current.ResourceVersion).ExecCASRelease()
		}
		if err != nil {
			return fmt.Errorf("scylla: write category reverse membership: %w", err)
		}
		if !applied {
			continue
		}
		return s.upsertCategoryProductForward(ctx, product, uid, categoryUID, shard)
	}
	return fmt.Errorf("scylla: category membership fence contention")
}

func (s *scyllaDatastore) upsertCategoryProductForward(ctx context.Context, product *datastore.Product, uid gocql.UUID, categoryUID string, shard int8) error {
	for attempt := 0; attempt < categoryProductWriteAttempts; attempt++ {
		var current struct {
			ResourceVersion string `db:"resource_version"`
		}
		err := s.session.Query("SELECT resource_version FROM "+categoryProductsByCategoryTable+" WHERE namespace=? AND category_uid=? AND shard=? AND creation_timestamp=? AND product_uid=?", nil).WithContext(ctx).Bind(product.Namespace, categoryUID, shard, product.CreationTimestamp, uid).GetRelease(&current)
		if err != nil && !errors.Is(err, gocql.ErrNotFound) {
			return fmt.Errorf("scylla: read category forward membership fence: %w", err)
		}
		if err == nil && compareResourceVersions(current.ResourceVersion, product.ResourceVersion) >= 0 {
			return nil
		}
		var applied bool
		if errors.Is(err, gocql.ErrNotFound) {
			applied, err = s.session.Query("INSERT INTO "+categoryProductsByCategoryTable+" (namespace,category_uid,shard,creation_timestamp,product_uid,resource_version) VALUES (?,?,?,?,?,?) IF NOT EXISTS", nil).WithContext(ctx).Bind(product.Namespace, categoryUID, shard, product.CreationTimestamp, uid, product.ResourceVersion).ExecCASRelease()
		} else {
			applied, err = s.session.Query("UPDATE "+categoryProductsByCategoryTable+" SET resource_version=? WHERE namespace=? AND category_uid=? AND shard=? AND creation_timestamp=? AND product_uid=? IF resource_version=?", nil).WithContext(ctx).Bind(product.ResourceVersion, product.Namespace, categoryUID, shard, product.CreationTimestamp, uid, current.ResourceVersion).ExecCASRelease()
		}
		if err != nil {
			return fmt.Errorf("scylla: write category forward membership: %w", err)
		}
		if applied {
			return nil
		}
	}
	return fmt.Errorf("scylla: category forward membership fence contention")
}

func (s *scyllaDatastore) deleteCategoryProductMembership(ctx context.Context, product *datastore.Product, uid gocql.UUID, row categoryProductReverseRow) error {
	if compareResourceVersions(row.ResourceVersion, product.ResourceVersion) > 0 {
		return nil
	}
	// Retain the reverse row until forward removal succeeds so a retry can find it.
	if _, err := s.session.Query("DELETE FROM "+categoryProductsByCategoryTable+" WHERE namespace=? AND category_uid=? AND shard=? AND creation_timestamp=? AND product_uid=? IF resource_version=?", nil).WithContext(ctx).Bind(product.Namespace, row.CategoryUID, row.Shard, product.CreationTimestamp, uid, row.ResourceVersion).ExecCASRelease(); err != nil {
		return fmt.Errorf("scylla: remove category membership: %w", err)
	}
	if _, err := s.session.Query("DELETE FROM "+categoryProductsByProductTable+" WHERE namespace=? AND product_uid=? AND category_uid=? IF resource_version=?", nil).WithContext(ctx).Bind(product.Namespace, uid, row.CategoryUID, row.ResourceVersion).ExecCASRelease(); err != nil {
		return fmt.Errorf("scylla: remove category reverse membership: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) fenceCategoryProductMembership(ctx context.Context, product *datastore.Product, uid gocql.UUID, written map[string]struct{}) error {
	latest, err := s.GetProduct(ctx, product.UID)
	if errors.Is(err, datastore.ErrNotFound) {
		for categoryUID := range written {
			if err := s.deleteCategoryProductMembership(ctx, product, uid, categoryProductReverseRow{
				CategoryUID: categoryUID, Shard: categoryProductShard(product.UID), ResourceVersion: product.ResourceVersion,
			}); err != nil {
				return err
			}
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("scylla: re-read product for category membership fence: %w", err)
	}
	if latest.ResourceVersion == product.ResourceVersion {
		return nil
	}
	var status catalog.ProductStatus
	if err := json.Unmarshal(latest.Status, &status); err != nil {
		return fmt.Errorf("scylla: decode product category fence status: %w", err)
	}
	current := map[string]struct{}{}
	if status.Resolved != nil && status.Resolved.Category != nil {
		for _, name := range status.Resolved.Category.Path {
			category, getErr := s.GetCategoryTaxonomyByName(ctx, latest.Namespace, name)
			if errors.Is(getErr, datastore.ErrNotFound) {
				continue
			}
			if getErr != nil {
				return fmt.Errorf("scylla: resolve category for membership fence: %w", getErr)
			}
			current[category.UID] = struct{}{}
		}
	}
	for categoryUID := range written {
		if _, retained := current[categoryUID]; !retained {
			if err := s.deleteCategoryProductMembership(ctx, latest, uid, categoryProductReverseRow{CategoryUID: categoryUID, Shard: categoryProductShard(latest.UID), ResourceVersion: product.ResourceVersion}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *scyllaDatastore) ListCategoryProducts(ctx context.Context, namespace, categoryUID string, page datastore.PageParams) (*datastore.PageResult[datastore.Product], error) {
	if err := s.refreshCategoryProductProjectionReady(ctx); err != nil {
		return nil, err
	}
	if !s.categoryProductProjectionReady.Load() {
		return nil, datastore.ErrProjectionNotReady
	}
	limit := page.Limit()
	if limit > datastore.DefaultPageSize {
		limit = datastore.DefaultPageSize
	}
	order, relation, cursor := "DESC", "<", page.After
	if page.Last > 0 {
		order, relation, cursor = "ASC", ">", page.Before
	}
	var position *categoryProductRow
	if cursor != "" {
		c, err := parsePageCursor(cursor)
		if err != nil {
			return nil, datastore.ErrInvalidArgument
		}
		cursorID, err := gocql.ParseUUID(c.ID)
		if err != nil {
			return nil, datastore.ErrInvalidArgument
		}
		position = &categoryProductRow{CreationTimestamp: c.CreatedAt, ProductUID: cursorID}
	}
	items, err := collectCategoryProductPage(ctx, namespace, page.Last > 0, limit, position,
		func(ctx context.Context, shard int8, position *categoryProductRow) ([]categoryProductRow, error) {
			stmt := "SELECT creation_timestamp, product_uid FROM " + categoryProductsByCategoryTable + " WHERE namespace=? AND category_uid=? AND shard=?"
			args := []any{namespace, categoryUID, shard}
			if position != nil {
				stmt += " AND (creation_timestamp, product_uid) " + relation + " (?, ?)"
				args = append(args, position.CreationTimestamp, position.ProductUID)
			}
			stmt += " ORDER BY creation_timestamp " + order + ", product_uid " + order + " LIMIT ?"
			args = append(args, limit+1)
			var shardRows []categoryProductRow
			if err := s.session.Query(stmt, nil).WithContext(ctx).Bind(args...).SelectRelease(&shardRows); err != nil {
				return nil, fmt.Errorf("scylla: list category product shard: %w", err)
			}
			return shardRows, nil
		}, s.GetProduct)
	if err != nil {
		return nil, err
	}
	return buildPageResult(items, limit, page), nil
}

func categoryProductRowBefore(left, right categoryProductRow) bool {
	if left.CreationTimestamp.Equal(right.CreationTimestamp) {
		return bytes.Compare(left.ProductUID[:], right.ProductUID[:]) < 0
	}
	return left.CreationTimestamp.Before(right.CreationTimestamp)
}

func collectCategoryProductPage(
	ctx context.Context, namespace string, backward bool, limit int, position *categoryProductRow,
	fetch func(context.Context, int8, *categoryProductRow) ([]categoryProductRow, error),
	hydrate func(context.Context, string) (*datastore.Product, error),
) ([]*datastore.Product, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type shardPage struct {
		rows []categoryProductRow
		next int
		end  bool
	}
	var shards [categoryProductShardCount]shardPage
	var firstErr error
	var errMu sync.Mutex
	var wg sync.WaitGroup
	for shard := range shards {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, err := fetch(ctx, int8(shard), position)
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				return
			}
			shards[shard] = shardPage{rows: rows, end: len(rows) < limit+1}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	queries, lookups := categoryProductShardCount, 0
	items := make([]*datastore.Product, 0, limit+1)
	for len(items) < limit+1 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		selected := -1
		for shard := range shards {
			state := &shards[shard]
			if state.next == len(state.rows) && !state.end {
				if queries >= projectionListQueryBudget {
					return nil, fmt.Errorf("%w: category product listing exceeds query budget; repair stale projections", datastore.ErrProjectionNotReady)
				}
				last := state.rows[len(state.rows)-1]
				rows, err := fetch(ctx, int8(shard), &last)
				queries++
				if err != nil {
					return nil, err
				}
				*state = shardPage{rows: rows, end: len(rows) < limit+1}
			}
			if state.next == len(state.rows) {
				continue
			}
			if selected < 0 ||
				(backward && categoryProductRowBefore(state.rows[state.next], shards[selected].rows[shards[selected].next])) ||
				(!backward && categoryProductRowBefore(shards[selected].rows[shards[selected].next], state.rows[state.next])) {
				selected = shard
			}
		}
		if selected < 0 {
			break
		}
		if lookups >= projectionListLookupBudget {
			return nil, fmt.Errorf("%w: category product listing exceeds lookup budget; repair stale projections", datastore.ErrProjectionNotReady)
		}
		state := &shards[selected]
		row := state.rows[state.next]
		state.next++
		product, err := hydrate(ctx, row.ProductUID.String())
		lookups++
		if errors.Is(err, datastore.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("scylla: hydrate listed category product: %w", err)
		}
		if product.Namespace == namespace && product.CreationTimestamp.Equal(row.CreationTimestamp) {
			items = append(items, product)
		}
	}
	if backward {
		reverseRows(items)
	}
	return items, nil
}
