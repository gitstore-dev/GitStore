// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	gomemdb "github.com/hashicorp/go-memdb"
)

// paginateSlice applies keyset pagination to an in-memory slice.
// Items are sorted by (created_at DESC, id DESC) — newest first.
// The getKey function extracts (createdAt, id) from each item.
func paginateSlice[T any](items []*T, page datastore.PageParams, getKey func(*T) (time.Time, string)) *datastore.PageResult[T] {
	totalCount := int32(len(items))

	// Sort by created_at DESC, id DESC (newest first)
	sort.Slice(items, func(i, j int) bool {
		iTime, iID := getKey(items[i])
		jTime, jID := getKey(items[j])
		cmp := iTime.Compare(jTime)
		if cmp != 0 {
			return cmp > 0 // DESC
		}
		return iID > jID // DESC
	})

	if len(items) == 0 {
		return &datastore.PageResult[T]{Items: []*T{}, TotalCount: totalCount}
	}

	limit := page.Limit()
	start, end := 0, len(items)

	// Apply "after" cursor: skip items until we pass the cursor position
	// In DESC order, "after" means items that are OLDER (come after in the list)
	if page.After != "" {
		cursor, err := decodeCursor(page.After)
		if err == nil {
			found := false
			for i, item := range items {
				itemTime, itemID := getKey(item)
				if compareKeyset(itemTime, itemID, cursor.CreatedAt, cursor.ID) < 0 {
					start = i
					found = true
					break
				}
			}
			if !found {
				start = end
			}
		}
	}

	// Apply "before" cursor: stop items before we reach the cursor position
	// In DESC order, "before" means items that are NEWER (come before in the list)
	if page.Before != "" {
		cursor, err := decodeCursor(page.Before)
		if err == nil {
			for i, item := range items {
				itemTime, itemID := getKey(item)
				if compareKeyset(itemTime, itemID, cursor.CreatedAt, cursor.ID) <= 0 {
					end = i
					break
				}
			}
		}
	}

	if start >= end {
		return &datastore.PageResult[T]{
			Items:       []*T{},
			HasPrevious: start > 0,
			TotalCount:  totalCount,
		}
	}

	window := items[start:end]
	hasNext := false
	hasPrevious := start > 0

	if page.Last > 0 {
		// Backward pagination: take last N items from the window
		if len(window) > limit {
			window = window[len(window)-limit:]
			hasPrevious = true
		}
		hasNext = end < len(items)
	} else {
		// Forward pagination: take first N items from the window
		if len(window) > limit {
			window = window[:limit]
			hasNext = true
		}
		hasPrevious = start > 0
	}

	return &datastore.PageResult[T]{
		Items:       window,
		HasNext:     hasNext,
		HasPrevious: hasPrevious,
		TotalCount:  totalCount,
	}
}

// compareKeyset compares two keyset positions in DESC order.
// Returns < 0 if (aTime, aID) is "after" (older than) (bTime, bID) in DESC order.
func compareKeyset(aTime time.Time, aID string, bTime time.Time, bID string) int {
	cmp := aTime.Compare(bTime)
	if cmp != 0 {
		return cmp
	}
	switch {
	case aID < bID:
		return -1
	case aID > bID:
		return 1
	default:
		return 0
	}
}

// decodeCursor decodes an opaque base64 keyset cursor.
func decodeCursor(cursor string) (*datastore.PageCursor, error) {
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

// memdbDatastore implements datastore.Datastore using hashicorp/go-memdb.
type memdbDatastore struct {
	db *gomemdb.MemDB
}

// New creates an empty in-memory datastore backed by go-memdb.
func New() (datastore.Datastore, error) {
	db, err := gomemdb.NewMemDB(schema)
	if err != nil {
		return nil, fmt.Errorf("memdb: failed to initialise: %w", err)
	}
	return &memdbDatastore{db: db}, nil
}

func (m *memdbDatastore) Close() error { return nil }

// ── helpers ───────────────────────────────────────────────────────────────────

// notFoundOrErr converts a nil result from txn.First into ErrNotFound,
// or propagates any actual error from the transaction.
func notFoundOrErr(err error) error {
	if err != nil {
		return fmt.Errorf("%w: %s", datastore.ErrNotFound, err.Error())
	}
	return datastore.ErrNotFound
}

// ── Product ───────────────────────────────────────────────────────────────────

func (m *memdbDatastore) CreateProduct(_ context.Context, p *datastore.Product) error {
	if p == nil {
		return fmt.Errorf("%w: product is nil", datastore.ErrInvalidArgument)
	}
	stored := cloneProduct(p)
	txn := m.db.Txn(true)
	if raw, _ := txn.First("product", "id", p.UID); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: product uid %s", datastore.ErrAlreadyExists, p.UID)
	}
	if raw, _ := txn.First("product", "name_namespace", p.Namespace, p.Name); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: product %s/%s", datastore.ErrAlreadyExists, p.Namespace, p.Name)
	}
	if err := txn.Insert("product", stored); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: insert product: %w", err)
	}
	if err := syncOwnerReferenceProjections(txn, stored.Namespace, stored.RepositoryID, "Product", stored.UID, stored.Name, stored.ResourceVersion, stored.OwnerReferences); err != nil {
		txn.Abort()
		return err
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) GetProduct(_ context.Context, uid string) (*datastore.Product, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("product", "id", uid)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return cloneProduct(raw.(*datastore.Product)), nil
}

func (m *memdbDatastore) GetProductByName(_ context.Context, namespace, name string) (*datastore.Product, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("product", "name_namespace", namespace, name)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return cloneProduct(raw.(*datastore.Product)), nil
}

func (m *memdbDatastore) ListProducts(_ context.Context, namespace string, page datastore.PageParams) (*datastore.PageResult[datastore.Product], error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	var it gomemdb.ResultIterator
	var err error
	if namespace != "" {
		it, err = txn.Get("product", "namespace", namespace)
	} else {
		it, err = txn.Get("product", "id")
	}
	if err != nil {
		return nil, fmt.Errorf("memdb: list products: %w", err)
	}

	var all []*datastore.Product
	for obj := it.Next(); obj != nil; obj = it.Next() {
		all = append(all, cloneProduct(obj.(*datastore.Product)))
	}

	return paginateSlice(all, page, func(p *datastore.Product) (time.Time, string) {
		return p.CreationTimestamp, p.UID
	}), nil
}

func (m *memdbDatastore) UpdateProduct(_ context.Context, p *datastore.Product) error {
	if p == nil {
		return fmt.Errorf("%w: product is nil", datastore.ErrInvalidArgument)
	}
	txn := m.db.Txn(true)
	if raw, _ := txn.First("product", "id", p.UID); raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: product uid %s", datastore.ErrNotFound, p.UID)
	}
	if raw, _ := txn.First("product", "name_namespace", p.Namespace, p.Name); raw != nil && raw.(*datastore.Product).UID != p.UID {
		txn.Abort()
		return fmt.Errorf("%w: product %s/%s", datastore.ErrAlreadyExists, p.Namespace, p.Name)
	}
	if err := txn.Insert("product", cloneProduct(p)); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: update product: %w", err)
	}
	if err := syncOwnerReferenceProjections(txn, p.Namespace, p.RepositoryID, "Product", p.UID, p.Name, p.ResourceVersion, p.OwnerReferences); err != nil {
		txn.Abort()
		return err
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) DeleteProduct(_ context.Context, uid string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("product", "id", uid)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: product uid %s", datastore.ErrNotFound, uid)
	}
	if err := deleteOwnerReferenceProjections(txn, "Product", uid); err != nil {
		txn.Abort()
		return err
	}
	if err := txn.Delete("product", raw); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: delete product: %w", err)
	}
	txn.Commit()
	return nil
}

// ── ProductVariant ────────────────────────────────────────────────────────────

func (m *memdbDatastore) CreateProductVariant(_ context.Context, v *datastore.ProductVariant) error {
	if v == nil {
		return fmt.Errorf("%w: product variant is nil", datastore.ErrInvalidArgument)
	}
	stored := cloneProductVariant(v)
	txn := m.db.Txn(true)
	if raw, _ := txn.First("product_variant", "id", v.UID); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: product_variant uid %s", datastore.ErrAlreadyExists, v.UID)
	}
	if raw, _ := txn.First("product_variant", "name_namespace", v.Namespace, v.Name); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: product_variant %s/%s", datastore.ErrAlreadyExists, v.Namespace, v.Name)
	}
	if v.SKU != "" {
		if raw, _ := txn.First("product_variant", "sku_namespace", v.Namespace, v.SKU); raw != nil {
			txn.Abort()
			return fmt.Errorf("%w: product_variant sku %s in namespace %s", datastore.ErrAlreadyExists, v.SKU, v.Namespace)
		}
	}
	if err := txn.Insert("product_variant", stored); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: insert product_variant: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) GetProductVariant(_ context.Context, uid string) (*datastore.ProductVariant, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("product_variant", "id", uid)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return cloneProductVariant(raw.(*datastore.ProductVariant)), nil
}

func (m *memdbDatastore) GetProductVariantByName(_ context.Context, namespace, name string) (*datastore.ProductVariant, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("product_variant", "name_namespace", namespace, name)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return cloneProductVariant(raw.(*datastore.ProductVariant)), nil
}

func (m *memdbDatastore) GetProductVariantBySKU(_ context.Context, namespace, sku string) (*datastore.ProductVariant, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("product_variant", "sku_namespace", namespace, sku)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return cloneProductVariant(raw.(*datastore.ProductVariant)), nil
}

func (m *memdbDatastore) ListProductVariants(_ context.Context, namespace string, page datastore.PageParams) (*datastore.PageResult[datastore.ProductVariant], error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	var it gomemdb.ResultIterator
	var err error
	if namespace != "" {
		it, err = txn.Get("product_variant", "namespace", namespace)
	} else {
		it, err = txn.Get("product_variant", "id")
	}
	if err != nil {
		return nil, fmt.Errorf("memdb: list product_variants: %w", err)
	}

	var all []*datastore.ProductVariant
	for obj := it.Next(); obj != nil; obj = it.Next() {
		all = append(all, cloneProductVariant(obj.(*datastore.ProductVariant)))
	}
	return paginateSlice(all, page, func(v *datastore.ProductVariant) (time.Time, string) {
		return v.CreationTimestamp, v.UID
	}), nil
}

func (m *memdbDatastore) ListProductVariantsByProductRef(_ context.Context, namespace, productRefName string) ([]*datastore.ProductVariant, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get("product_variant", "product_ref", namespace, productRefName)
	if err != nil {
		return nil, fmt.Errorf("memdb: list product_variants by product_ref: %w", err)
	}
	var result []*datastore.ProductVariant
	for obj := it.Next(); obj != nil; obj = it.Next() {
		result = append(result, cloneProductVariant(obj.(*datastore.ProductVariant)))
	}
	return result, nil
}

func (m *memdbDatastore) UpdateProductVariant(_ context.Context, v *datastore.ProductVariant) error {
	if v == nil {
		return fmt.Errorf("%w: product variant is nil", datastore.ErrInvalidArgument)
	}
	txn := m.db.Txn(true)
	if raw, _ := txn.First("product_variant", "id", v.UID); raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: product_variant uid %s", datastore.ErrNotFound, v.UID)
	}
	if raw, _ := txn.First("product_variant", "name_namespace", v.Namespace, v.Name); raw != nil && raw.(*datastore.ProductVariant).UID != v.UID {
		txn.Abort()
		return fmt.Errorf("%w: product_variant %s/%s", datastore.ErrAlreadyExists, v.Namespace, v.Name)
	}
	if v.SKU != "" {
		if raw, _ := txn.First("product_variant", "sku_namespace", v.Namespace, v.SKU); raw != nil && raw.(*datastore.ProductVariant).UID != v.UID {
			txn.Abort()
			return fmt.Errorf("%w: product_variant sku %s in namespace %s", datastore.ErrAlreadyExists, v.SKU, v.Namespace)
		}
	}
	if err := txn.Insert("product_variant", cloneProductVariant(v)); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: update product_variant: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) DeleteProductVariant(_ context.Context, uid string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("product_variant", "id", uid)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: product_variant uid %s", datastore.ErrNotFound, uid)
	}
	if err := txn.Delete("product_variant", raw); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: delete product_variant: %w", err)
	}
	txn.Commit()
	return nil
}

// ── CategoryTaxonomy ──────────────────────────────────────────────────────────

func (m *memdbDatastore) CreateCategoryTaxonomy(_ context.Context, c *datastore.CategoryTaxonomy) error {
	if c == nil {
		return fmt.Errorf("%w: category taxonomy is nil", datastore.ErrInvalidArgument)
	}
	stored := cloneCategoryTaxonomy(c)
	txn := m.db.Txn(true)
	if raw, _ := txn.First("category_taxonomy", "id", c.UID); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: category_taxonomy uid %s", datastore.ErrAlreadyExists, c.UID)
	}
	if raw, _ := txn.First("category_taxonomy", "name_namespace", c.Namespace, c.Name); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: category_taxonomy %s/%s", datastore.ErrAlreadyExists, c.Namespace, c.Name)
	}
	if err := txn.Insert("category_taxonomy", stored); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: insert category_taxonomy: %w", err)
	}
	if err := syncOwnerReferenceProjections(txn, stored.Namespace, stored.RepositoryID, "CategoryTaxonomy", stored.UID, stored.Name, stored.ResourceVersion, stored.OwnerReferences); err != nil {
		txn.Abort()
		return err
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) GetCategoryTaxonomy(_ context.Context, uid string) (*datastore.CategoryTaxonomy, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("category_taxonomy", "id", uid)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return cloneCategoryTaxonomy(raw.(*datastore.CategoryTaxonomy)), nil
}

func (m *memdbDatastore) GetCategoryTaxonomyByName(_ context.Context, namespace, name string) (*datastore.CategoryTaxonomy, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("category_taxonomy", "name_namespace", namespace, name)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return cloneCategoryTaxonomy(raw.(*datastore.CategoryTaxonomy)), nil
}

func (m *memdbDatastore) ListCategoryTaxonomies(_ context.Context, namespace string, page datastore.PageParams) (*datastore.PageResult[datastore.CategoryTaxonomy], error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	var it gomemdb.ResultIterator
	var err error
	if namespace != "" {
		it, err = txn.Get("category_taxonomy", "namespace", namespace)
	} else {
		it, err = txn.Get("category_taxonomy", "id")
	}
	if err != nil {
		return nil, fmt.Errorf("memdb: list category_taxonomies: %w", err)
	}
	var all []*datastore.CategoryTaxonomy
	for obj := it.Next(); obj != nil; obj = it.Next() {
		all = append(all, cloneCategoryTaxonomy(obj.(*datastore.CategoryTaxonomy)))
	}
	return paginateSlice(all, page, func(c *datastore.CategoryTaxonomy) (time.Time, string) {
		return c.CreationTimestamp, c.UID
	}), nil
}

func (m *memdbDatastore) UpdateCategoryTaxonomy(_ context.Context, c *datastore.CategoryTaxonomy) error {
	if c == nil {
		return fmt.Errorf("%w: category taxonomy is nil", datastore.ErrInvalidArgument)
	}
	txn := m.db.Txn(true)
	if raw, _ := txn.First("category_taxonomy", "id", c.UID); raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: category_taxonomy uid %s", datastore.ErrNotFound, c.UID)
	}
	if raw, _ := txn.First("category_taxonomy", "name_namespace", c.Namespace, c.Name); raw != nil && raw.(*datastore.CategoryTaxonomy).UID != c.UID {
		txn.Abort()
		return fmt.Errorf("%w: category_taxonomy %s/%s", datastore.ErrAlreadyExists, c.Namespace, c.Name)
	}
	if err := txn.Insert("category_taxonomy", cloneCategoryTaxonomy(c)); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: update category_taxonomy: %w", err)
	}
	if err := syncOwnerReferenceProjections(txn, c.Namespace, c.RepositoryID, "CategoryTaxonomy", c.UID, c.Name, c.ResourceVersion, c.OwnerReferences); err != nil {
		txn.Abort()
		return err
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) UpdateCategoryTaxonomyStatus(_ context.Context, namespace, name string, patch datastore.CategoryTaxonomyStatusPatch) (*datastore.CategoryTaxonomy, error) {
	txn := m.db.Txn(true)
	raw, err := txn.First("category_taxonomy", "name_namespace", namespace, name)
	if err != nil || raw == nil {
		txn.Abort()
		return nil, fmt.Errorf("%w: category_taxonomy %s/%s", datastore.ErrNotFound, namespace, name)
	}
	updated := cloneCategoryTaxonomy(raw.(*datastore.CategoryTaxonomy))

	if applyErr := datastore.ApplyCategoryTaxonomyStatusPatch(updated, patch); applyErr != nil {
		txn.Abort()
		return nil, applyErr
	}

	if insErr := txn.Insert("category_taxonomy", updated); insErr != nil {
		txn.Abort()
		return nil, fmt.Errorf("memdb: update category_taxonomy status: %w", insErr)
	}
	if err := syncOwnerReferenceProjections(txn, updated.Namespace, updated.RepositoryID, "CategoryTaxonomy", updated.UID, updated.Name, updated.ResourceVersion, updated.OwnerReferences); err != nil {
		txn.Abort()
		return nil, err
	}
	txn.Commit()
	return cloneCategoryTaxonomy(updated), nil
}

func (m *memdbDatastore) DeleteCategoryTaxonomy(_ context.Context, uid string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("category_taxonomy", "id", uid)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: category_taxonomy uid %s", datastore.ErrNotFound, uid)
	}
	if err := deleteOwnerReferenceProjections(txn, "CategoryTaxonomy", uid); err != nil {
		txn.Abort()
		return err
	}
	if err := txn.Delete("category_taxonomy", raw); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: delete category_taxonomy: %w", err)
	}
	txn.Commit()
	return nil
}

// ── Collection ────────────────────────────────────────────────────────────────

func (m *memdbDatastore) CreateCollection(_ context.Context, c *datastore.Collection) error {
	if c == nil {
		return fmt.Errorf("%w: collection is nil", datastore.ErrInvalidArgument)
	}
	stored := cloneCollection(c)
	txn := m.db.Txn(true)
	if raw, _ := txn.First("collection", "id", c.UID); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: collection uid %s", datastore.ErrAlreadyExists, c.UID)
	}
	if raw, _ := txn.First("collection", "name_namespace", c.Namespace, c.Name); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: collection %s/%s", datastore.ErrAlreadyExists, c.Namespace, c.Name)
	}
	if err := txn.Insert("collection", stored); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: insert collection: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) GetCollection(_ context.Context, uid string) (*datastore.Collection, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("collection", "id", uid)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return cloneCollection(raw.(*datastore.Collection)), nil
}

func (m *memdbDatastore) GetCollectionByName(_ context.Context, namespace, name string) (*datastore.Collection, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("collection", "name_namespace", namespace, name)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return cloneCollection(raw.(*datastore.Collection)), nil
}

func (m *memdbDatastore) ListCollections(_ context.Context, namespace string, page datastore.PageParams) (*datastore.PageResult[datastore.Collection], error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get("collection", "namespace", namespace)
	if err != nil {
		return nil, fmt.Errorf("memdb: list collections: %w", err)
	}
	var all []*datastore.Collection
	for obj := it.Next(); obj != nil; obj = it.Next() {
		all = append(all, cloneCollection(obj.(*datastore.Collection)))
	}
	return paginateSlice(all, page, func(c *datastore.Collection) (time.Time, string) {
		return c.CreationTimestamp, c.UID
	}), nil
}

func (m *memdbDatastore) UpdateCollection(_ context.Context, c *datastore.Collection) error {
	if c == nil {
		return fmt.Errorf("%w: collection is nil", datastore.ErrInvalidArgument)
	}
	txn := m.db.Txn(true)
	if raw, _ := txn.First("collection", "id", c.UID); raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: collection uid %s", datastore.ErrNotFound, c.UID)
	}
	if raw, _ := txn.First("collection", "name_namespace", c.Namespace, c.Name); raw != nil && raw.(*datastore.Collection).UID != c.UID {
		txn.Abort()
		return fmt.Errorf("%w: collection %s/%s", datastore.ErrAlreadyExists, c.Namespace, c.Name)
	}
	if err := txn.Insert("collection", cloneCollection(c)); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: update collection: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) DeleteCollection(_ context.Context, uid string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("collection", "id", uid)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: collection uid %s", datastore.ErrNotFound, uid)
	}
	if err := txn.Delete("collection", raw); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: delete collection: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) ListProductsByLabelSelector(_ context.Context, namespace string, selector catalog.LabelSelector) ([]*datastore.Product, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get("product", "namespace", namespace)
	if err != nil {
		return nil, fmt.Errorf("memdb: list products by selector: %w", err)
	}
	var result []*datastore.Product
	for obj := it.Next(); obj != nil; obj = it.Next() {
		p := obj.(*datastore.Product)
		if catalog.MatchesLabels(&selector, p.Labels) {
			result = append(result, cloneProduct(p))
		}
	}
	return result, nil
}

// ── Namespace ─────────────────────────────────────────────────────────────────

func (m *memdbDatastore) CreateNamespace(_ context.Context, ns *datastore.Namespace) error {
	if ns == nil {
		return fmt.Errorf("%w: namespace is nil", datastore.ErrInvalidArgument)
	}
	datastore.NormalizeNamespaceContract(ns)
	if ns.UID == "" {
		return fmt.Errorf("%w: namespace uid is empty", datastore.ErrInvalidArgument)
	}
	stored := normalizedNamespaceCopy(ns)
	txn := m.db.Txn(true)
	if raw, _ := txn.First("namespaces", "id", ns.UID); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: namespace uid %s", datastore.ErrAlreadyExists, ns.UID)
	}
	if raw, _ := txn.First("namespaces", "name", ns.Name); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: namespace name %s", datastore.ErrAlreadyExists, ns.Name)
	}
	if err := txn.Insert("namespaces", stored); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: insert namespace: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) GetNamespace(_ context.Context, uid string) (*datastore.Namespace, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("namespaces", "id", uid)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return normalizedNamespaceCopy(raw.(*datastore.Namespace)), nil
}

func (m *memdbDatastore) GetNamespaceByName(_ context.Context, name string) (*datastore.Namespace, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("namespaces", "name", name)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return normalizedNamespaceCopy(raw.(*datastore.Namespace)), nil
}

func (m *memdbDatastore) ListNamespaces(_ context.Context, page datastore.PageParams) (*datastore.PageResult[datastore.Namespace], error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get("namespaces", "id")
	if err != nil {
		return nil, fmt.Errorf("memdb: list namespaces: %w", err)
	}
	var all []*datastore.Namespace
	for obj := it.Next(); obj != nil; obj = it.Next() {
		all = append(all, normalizedNamespaceCopy(obj.(*datastore.Namespace)))
	}
	return paginateSlice(all, page, func(ns *datastore.Namespace) (time.Time, string) {
		return ns.CreationTimestamp, ns.UID
	}), nil
}

func (m *memdbDatastore) UpdateNamespace(_ context.Context, ns *datastore.Namespace, expectedResourceVersion string) error {
	if ns == nil {
		return fmt.Errorf("%w: namespace is nil", datastore.ErrInvalidArgument)
	}
	datastore.NormalizeNamespaceContract(ns)
	txn := m.db.Txn(true)
	raw, _ := txn.First("namespaces", "id", ns.UID)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: namespace uid %s", datastore.ErrNotFound, ns.UID)
	}
	current := normalizedNamespaceCopy(raw.(*datastore.Namespace))
	if current.ResourceVersion != expectedResourceVersion {
		txn.Abort()
		return datastore.ErrConflict
	}
	if raw, _ := txn.First("namespaces", "name", ns.Name); raw != nil && raw.(*datastore.Namespace).UID != ns.UID {
		txn.Abort()
		return fmt.Errorf("%w: namespace name %s", datastore.ErrAlreadyExists, ns.Name)
	}
	if err := txn.Insert("namespaces", normalizedNamespaceCopy(ns)); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: update namespace: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) DeleteNamespace(_ context.Context, uid string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("namespaces", "id", uid)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: namespace uid %s", datastore.ErrNotFound, uid)
	}
	if err := txn.Delete("namespaces", raw); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: delete namespace: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) DeleteNamespaceWithResourceVersion(_ context.Context, uid, expectedResourceVersion string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("namespaces", "id", uid)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: namespace uid %s", datastore.ErrNotFound, uid)
	}
	current := raw.(*datastore.Namespace)
	if current.ResourceVersion != expectedResourceVersion {
		txn.Abort()
		return datastore.ErrConflict
	}
	if err := txn.Delete("namespaces", raw); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: delete namespace with resource version: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) HasRepositories(_ context.Context, namespace string) (bool, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("repository", "namespace", namespace)
	if err != nil {
		return false, fmt.Errorf("memdb: has repositories: %w", err)
	}
	return raw != nil, nil
}

func normalizedNamespaceCopy(namespace *datastore.Namespace) *datastore.Namespace {
	if namespace == nil {
		return nil
	}
	clone := *namespace
	clone.Labels = cloneStringMap(namespace.Labels)
	clone.Annotations = cloneStringMap(namespace.Annotations)
	clone.OwnerReferences = append([]byte(nil), namespace.OwnerReferences...)
	clone.Spec = append([]byte(nil), namespace.Spec...)
	clone.Status = append([]byte(nil), namespace.Status...)
	clone.Finalizers = append([]string(nil), namespace.Finalizers...)
	clone.DeletionTimestamp = cloneTimePointer(namespace.DeletionTimestamp)
	datastore.NormalizeNamespaceContract(&clone)
	return &clone
}

// ── Repository ────────────────────────────────────────────────────────────────

func (m *memdbDatastore) CreateRepository(_ context.Context, r *datastore.Repository) error {
	if r == nil {
		return fmt.Errorf("%w: repository is nil", datastore.ErrInvalidArgument)
	}
	datastore.NormalizeRepositoryContract(r)
	if r.UID == "" {
		return fmt.Errorf("%w: repository uid is empty", datastore.ErrInvalidArgument)
	}
	stored := normalizedRepositoryCopy(r)
	txn := m.db.Txn(true)
	if raw, _ := txn.First("repository", "id", r.UID); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: repository uid %s", datastore.ErrAlreadyExists, r.UID)
	}
	if raw, _ := txn.First("repository", "name_namespace", r.Namespace, r.Name); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: repository %s/%s", datastore.ErrAlreadyExists, r.Namespace, r.Name)
	}
	if err := txn.Insert("repository", stored); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: insert repository: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) GetRepository(_ context.Context, uid string) (*datastore.Repository, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("repository", "id", uid)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return normalizedRepositoryCopy(raw.(*datastore.Repository)), nil
}

func (m *memdbDatastore) ListRepositoriesByNamespace(_ context.Context, namespace string, page datastore.PageParams) (*datastore.PageResult[datastore.Repository], error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get("repository", "namespace", namespace)
	if err != nil {
		return nil, fmt.Errorf("memdb: list repositories by namespace: %w", err)
	}
	var all []*datastore.Repository
	for obj := it.Next(); obj != nil; obj = it.Next() {
		all = append(all, normalizedRepositoryCopy(obj.(*datastore.Repository)))
	}
	return paginateSlice(all, page, func(r *datastore.Repository) (time.Time, string) {
		return r.CreationTimestamp, r.UID
	}), nil
}

func (m *memdbDatastore) ListRepositories(_ context.Context, page datastore.PageParams) (*datastore.PageResult[datastore.Repository], error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get("repository", "id")
	if err != nil {
		return nil, fmt.Errorf("memdb: list repositories: %w", err)
	}
	var all []*datastore.Repository
	for obj := it.Next(); obj != nil; obj = it.Next() {
		all = append(all, normalizedRepositoryCopy(obj.(*datastore.Repository)))
	}
	return paginateSlice(all, page, func(r *datastore.Repository) (time.Time, string) {
		return r.CreationTimestamp, r.UID
	}), nil
}

func (m *memdbDatastore) UpdateRepository(_ context.Context, r *datastore.Repository, expectedResourceVersion string) error {
	if r == nil {
		return fmt.Errorf("%w: repository is nil", datastore.ErrInvalidArgument)
	}
	datastore.NormalizeRepositoryContract(r)
	txn := m.db.Txn(true)
	raw, _ := txn.First("repository", "id", r.UID)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: repository uid %s", datastore.ErrNotFound, r.UID)
	}
	current := normalizedRepositoryCopy(raw.(*datastore.Repository))
	if current.ResourceVersion != expectedResourceVersion {
		txn.Abort()
		return datastore.ErrConflict
	}
	if raw, _ := txn.First("repository", "name_namespace", r.Namespace, r.Name); raw != nil && raw.(*datastore.Repository).UID != r.UID {
		txn.Abort()
		return fmt.Errorf("%w: repository %s/%s", datastore.ErrAlreadyExists, r.Namespace, r.Name)
	}
	if err := txn.Insert("repository", normalizedRepositoryCopy(r)); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: update repository: %w", err)
	}
	txn.Commit()
	return nil
}

func normalizedRepositoryCopy(repository *datastore.Repository) *datastore.Repository {
	if repository == nil {
		return nil
	}
	clone := *repository
	clone.Labels = cloneStringMap(repository.Labels)
	clone.Annotations = cloneStringMap(repository.Annotations)
	clone.OwnerReferences = append([]byte(nil), repository.OwnerReferences...)
	clone.Finalizers = append([]string(nil), repository.Finalizers...)
	clone.Spec = append([]byte(nil), repository.Spec...)
	clone.Status = append([]byte(nil), repository.Status...)
	clone.DeletionTimestamp = cloneTimePointer(repository.DeletionTimestamp)
	datastore.NormalizeRepositoryContract(&clone)
	return &clone
}

func (m *memdbDatastore) DeleteRepository(_ context.Context, uid string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("repository", "id", uid)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: repository uid %s", datastore.ErrNotFound, uid)
	}
	if err := txn.Delete("repository", raw); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: delete repository: %w", err)
	}
	txn.Commit()
	return nil
}

// catalogTablesWithRepositoryID lists every table indexed on RepositoryID,
// checked in order by HasCatalogResources with short-circuit on first match.
var catalogTablesWithRepositoryID = []string{"product", "product_variant", "category_taxonomy", "collection"}

func (m *memdbDatastore) HasCatalogResources(_ context.Context, repositoryID string) (bool, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	for _, table := range catalogTablesWithRepositoryID {
		raw, err := txn.First(table, "repository_id", repositoryID)
		if err != nil {
			return false, fmt.Errorf("memdb: has catalog resources (%s): %w", table, err)
		}
		if raw != nil {
			return true, nil
		}
	}
	return false, nil
}

// ── NamespaceMapping ──────────────────────────────────────────────────────────

func (m *memdbDatastore) CreateNamespaceMapping(_ context.Context, mp *datastore.NamespaceMapping) error {
	if mp == nil {
		return fmt.Errorf("%w: namespace mapping is nil", datastore.ErrInvalidArgument)
	}
	datastore.NormalizeNamespaceMappingContract(mp)
	stored := *mp
	txn := m.db.Txn(true)
	if raw, _ := txn.First("namespace_mapping", "id", mp.Namespace, mp.Name); raw != nil {
		existing := normalizedNamespaceMappingCopy(raw.(*datastore.NamespaceMapping))
		if existing.RepositoryID == mp.RepositoryID {
			txn.Abort()
			return nil
		}
		txn.Abort()
		return fmt.Errorf("%w: namespace_mapping (%s, %s)", datastore.ErrAlreadyExists, mp.Namespace, mp.Name)
	}
	if raw, _ := txn.First("namespace_mapping", "repository_id", mp.RepositoryID); raw != nil {
		existing := normalizedNamespaceMappingCopy(raw.(*datastore.NamespaceMapping))
		if existing.Namespace == mp.Namespace && existing.Name == mp.Name {
			txn.Abort()
			return nil
		}
		txn.Abort()
		return fmt.Errorf("%w: namespace_mapping repository_id %s", datastore.ErrAlreadyExists, mp.RepositoryID)
	}
	if err := txn.Insert("namespace_mapping", &stored); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: insert namespace_mapping: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) LookupRepository(_ context.Context, namespace, name string) (*datastore.NamespaceMapping, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("namespace_mapping", "id", namespace, name)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return normalizedNamespaceMappingCopy(raw.(*datastore.NamespaceMapping)), nil
}

func (m *memdbDatastore) LookupNamespaceByRepoID(_ context.Context, repositoryID string) (*datastore.NamespaceMapping, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("namespace_mapping", "repository_id", repositoryID)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return normalizedNamespaceMappingCopy(raw.(*datastore.NamespaceMapping)), nil
}

func (m *memdbDatastore) LookupNamespaceByRepositoryID(ctx context.Context, repositoryID string) (*datastore.NamespaceMapping, error) {
	return m.LookupNamespaceByRepoID(ctx, repositoryID)
}

func (m *memdbDatastore) RenameRepository(_ context.Context, namespace, oldName, newName string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("namespace_mapping", "id", namespace, oldName)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: namespace_mapping (%s, %s)", datastore.ErrNotFound, namespace, oldName)
	}
	old := raw.(*datastore.NamespaceMapping)
	if oldName == newName {
		txn.Abort()
		return nil
	}
	if target, _ := txn.First("namespace_mapping", "id", namespace, newName); target != nil {
		txn.Abort()
		return fmt.Errorf("%w: namespace_mapping (%s, %s)", datastore.ErrAlreadyExists, namespace, newName)
	}
	if err := txn.Delete("namespace_mapping", old); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: rename delete old mapping: %w", err)
	}
	updated := &datastore.NamespaceMapping{
		Namespace:    namespace,
		Name:         newName,
		RepositoryID: old.RepositoryID,
	}
	datastore.NormalizeNamespaceMappingContract(updated)
	if err := txn.Insert("namespace_mapping", updated); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: rename insert new mapping: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) TransferRepository(_ context.Context, repositoryID, fromNamespace, toNamespace string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("namespace_mapping", "repository_id", repositoryID)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: namespace_mapping repository_id %s", datastore.ErrNotFound, repositoryID)
	}
	old := raw.(*datastore.NamespaceMapping)
	if old.Namespace != fromNamespace {
		txn.Abort()
		return fmt.Errorf("%w: namespace_mapping repository_id %s in namespace %s", datastore.ErrNotFound, repositoryID, fromNamespace)
	}
	if fromNamespace == toNamespace {
		txn.Abort()
		return nil
	}
	if target, _ := txn.First("namespace_mapping", "id", toNamespace, old.Name); target != nil {
		txn.Abort()
		return fmt.Errorf("%w: namespace_mapping (%s, %s)", datastore.ErrAlreadyExists, toNamespace, old.Name)
	}
	if err := txn.Delete("namespace_mapping", old); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: transfer delete old mapping: %w", err)
	}
	updated := &datastore.NamespaceMapping{
		Namespace:    toNamespace,
		Name:         old.Name,
		RepositoryID: repositoryID,
	}
	datastore.NormalizeNamespaceMappingContract(updated)
	if err := txn.Insert("namespace_mapping", updated); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: transfer insert new mapping: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) DeleteNamespaceMapping(_ context.Context, namespace, name string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("namespace_mapping", "id", namespace, name)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: namespace_mapping (%s, %s)", datastore.ErrNotFound, namespace, name)
	}
	if err := txn.Delete("namespace_mapping", raw); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: delete namespace_mapping: %w", err)
	}
	txn.Commit()
	return nil
}

func normalizedNamespaceMappingCopy(mapping *datastore.NamespaceMapping) *datastore.NamespaceMapping {
	if mapping == nil {
		return nil
	}
	clone := *mapping
	datastore.NormalizeNamespaceMappingContract(&clone)
	return &clone
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func cloneProduct(product *datastore.Product) *datastore.Product {
	if product == nil {
		return nil
	}
	clone := *product
	clone.Labels = cloneStringMap(product.Labels)
	clone.Annotations = cloneStringMap(product.Annotations)
	clone.OwnerReferences = append([]byte(nil), product.OwnerReferences...)
	clone.Finalizers = append([]string(nil), product.Finalizers...)
	clone.Spec = append([]byte(nil), product.Spec...)
	clone.Status = append([]byte(nil), product.Status...)
	clone.DeletionTimestamp = cloneTimePointer(product.DeletionTimestamp)
	return &clone
}

func cloneProductVariant(variant *datastore.ProductVariant) *datastore.ProductVariant {
	if variant == nil {
		return nil
	}
	clone := *variant
	clone.Labels = cloneStringMap(variant.Labels)
	clone.Annotations = cloneStringMap(variant.Annotations)
	clone.OwnerReferences = append([]byte(nil), variant.OwnerReferences...)
	clone.Finalizers = append([]string(nil), variant.Finalizers...)
	clone.Spec = append([]byte(nil), variant.Spec...)
	clone.Status = append([]byte(nil), variant.Status...)
	clone.DeletionTimestamp = cloneTimePointer(variant.DeletionTimestamp)
	return &clone
}

func cloneCategoryTaxonomy(category *datastore.CategoryTaxonomy) *datastore.CategoryTaxonomy {
	if category == nil {
		return nil
	}
	clone := *category
	clone.Labels = cloneStringMap(category.Labels)
	clone.Annotations = cloneStringMap(category.Annotations)
	clone.OwnerReferences = append([]byte(nil), category.OwnerReferences...)
	clone.Finalizers = append([]string(nil), category.Finalizers...)
	clone.Spec = append([]byte(nil), category.Spec...)
	clone.Status = append([]byte(nil), category.Status...)
	clone.DeletionTimestamp = cloneTimePointer(category.DeletionTimestamp)
	return &clone
}

func cloneCollection(collection *datastore.Collection) *datastore.Collection {
	if collection == nil {
		return nil
	}
	clone := *collection
	clone.Labels = cloneStringMap(collection.Labels)
	clone.Annotations = cloneStringMap(collection.Annotations)
	clone.OwnerReferences = append([]byte(nil), collection.OwnerReferences...)
	clone.Finalizers = append([]string(nil), collection.Finalizers...)
	clone.Spec = append([]byte(nil), collection.Spec...)
	clone.Status = append([]byte(nil), collection.Status...)
	clone.DeletionTimestamp = cloneTimePointer(collection.DeletionTimestamp)
	return &clone
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
