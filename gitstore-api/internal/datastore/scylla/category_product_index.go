package scylla

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"sync"
	"time"

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
	CategoryUID string `db:"category_uid"`
	Shard       int8   `db:"shard"`
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
	if err := s.session.Query("SELECT category_uid FROM "+categoryProductsByProductTable+" WHERE namespace=? AND product_uid=?", nil).WithContext(ctx).Bind(product.Namespace, uid).SelectRelease(&old); err != nil {
		return fmt.Errorf("scylla: list product category memberships: %w", err)
	}
	for _, row := range old {
		if err := s.session.Query("DELETE FROM "+categoryProductsByCategoryTable+" WHERE namespace=? AND category_uid=? AND shard=? AND creation_timestamp=? AND product_uid=?", nil).WithContext(ctx).Bind(product.Namespace, row.CategoryUID, row.Shard, product.CreationTimestamp, uid).ExecRelease(); err != nil {
			return fmt.Errorf("scylla: remove category membership: %w", err)
		}
	}
	if len(old) > 0 {
		if err := s.session.Query("DELETE FROM "+categoryProductsByProductTable+" WHERE namespace=? AND product_uid=?", nil).WithContext(ctx).Bind(product.Namespace, uid).ExecRelease(); err != nil {
			return fmt.Errorf("scylla: remove category reverse membership: %w", err)
		}
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
		if err := s.session.Query("INSERT INTO "+categoryProductsByCategoryTable+" (namespace, category_uid, shard, creation_timestamp, product_uid) VALUES (?, ?, ?, ?, ?)", nil).WithContext(ctx).Bind(product.Namespace, categoryUID, shard, product.CreationTimestamp, uid).ExecRelease(); err != nil {
			return fmt.Errorf("scylla: add category membership: %w", err)
		}
		if err := s.session.Query("INSERT INTO "+categoryProductsByProductTable+" (namespace, product_uid, category_uid, shard) VALUES (?, ?, ?, ?)", nil).WithContext(ctx).Bind(product.Namespace, uid, categoryUID, shard).ExecRelease(); err != nil {
			return fmt.Errorf("scylla: add category reverse membership: %w", err)
		}
	}
	return nil
}

func (s *scyllaDatastore) ListCategoryProducts(ctx context.Context, namespace, categoryUID string, page datastore.PageParams) (*datastore.PageResult[datastore.Product], error) {
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
	rows := make([]categoryProductRow, 0, categoryProductShardCount*(limit+1))
	var rowsMu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	var cursorTime time.Time
	var cursorID gocql.UUID
	if cursor != "" {
		c, err := parsePageCursor(cursor)
		if err != nil {
			return nil, datastore.ErrInvalidArgument
		}
		cursorID, err = gocql.ParseUUID(c.ID)
		if err != nil {
			return nil, datastore.ErrInvalidArgument
		}
		cursorTime = c.CreatedAt
	}
	for shard := 0; shard < categoryProductShardCount; shard++ {
		shard := int8(shard)
		wg.Add(1)
		go func() {
			defer wg.Done()
			stmt := "SELECT creation_timestamp, product_uid FROM " + categoryProductsByCategoryTable + " WHERE namespace=? AND category_uid=? AND shard=?"
			args := []any{namespace, categoryUID, shard}
			if cursor != "" {
				stmt += " AND (creation_timestamp, product_uid) " + relation + " (?, ?)"
				args = append(args, cursorTime, cursorID)
			}
			stmt += " ORDER BY creation_timestamp " + order + ", product_uid " + order + " LIMIT ?"
			args = append(args, limit+1)
			var shardRows []categoryProductRow
			if err := s.session.Query(stmt, nil).WithContext(ctx).Bind(args...).SelectRelease(&shardRows); err != nil {
				rowsMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				rowsMu.Unlock()
				return
			}
			rowsMu.Lock()
			rows = append(rows, shardRows...)
			rowsMu.Unlock()
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, fmt.Errorf("scylla: list category products: %w", firstErr)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreationTimestamp.Equal(rows[j].CreationTimestamp) {
			return rows[i].ProductUID.String() > rows[j].ProductUID.String()
		}
		return rows[i].CreationTimestamp.After(rows[j].CreationTimestamp)
	})
	if page.Last > 0 && len(rows) > limit+1 {
		rows = rows[len(rows)-(limit+1):]
	}
	if page.Last == 0 && len(rows) > limit+1 {
		rows = rows[:limit+1]
	}
	items := make([]*datastore.Product, 0, len(rows))
	for _, row := range rows {
		p, err := s.GetProduct(ctx, row.ProductUID.String())
		if err == nil && p.Namespace == namespace {
			items = append(items, p)
		} else if err != nil && err != datastore.ErrNotFound {
			return nil, err
		}
	}
	if page.Last > 0 {
		sort.Slice(items, func(i, j int) bool {
			if items[i].CreationTimestamp.Equal(items[j].CreationTimestamp) {
				return items[i].UID > items[j].UID
			}
			return items[i].CreationTimestamp.After(items[j].CreationTimestamp)
		})
	}
	return buildPageResult(items, limit, page), nil
}
