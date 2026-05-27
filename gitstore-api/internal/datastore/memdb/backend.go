// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	gomemdb "github.com/hashicorp/go-memdb"
)

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
	txn := m.db.Txn(true)
	// Check duplicate ID
	if raw, _ := txn.First("product", "id", p.ID); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: product id %s", datastore.ErrAlreadyExists, p.ID)
	}
	// Check duplicate SKU
	if raw, _ := txn.First("product", "sku", p.SKU); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: product sku %s", datastore.ErrAlreadyExists, p.SKU)
	}
	if err := txn.Insert("product", p); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: insert product: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) GetProduct(_ context.Context, id string) (*datastore.Product, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("product", "id", id)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return raw.(*datastore.Product), nil
}

func (m *memdbDatastore) GetProductBySKU(_ context.Context, sku string) (*datastore.Product, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("product", "sku", sku)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return raw.(*datastore.Product), nil
}

func (m *memdbDatastore) ListProducts(_ context.Context, filter datastore.ProductFilter) ([]*datastore.Product, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	var it gomemdb.ResultIterator
	var err error
	if filter.CategoryID != "" {
		it, err = txn.Get("product", "category_id", filter.CategoryID)
	} else {
		it, err = txn.Get("product", "id")
	}
	if err != nil {
		return nil, fmt.Errorf("memdb: list products: %w", err)
	}

	var results []*datastore.Product
	for obj := it.Next(); obj != nil; obj = it.Next() {
		results = append(results, obj.(*datastore.Product))
	}

	// Sort by created_at + id for stable keyset pagination
	sortByCreatedAtThenID(results)

	// Apply keyset pagination
	results = applyKeysetPagination(results, filter.After, filter.Before, filter.First, filter.Last)

	return results, nil
}

// sortByCreatedAtThenID sorts products by created_at ascending, then by id as tie-breaker
func sortByCreatedAtThenID(products []*datastore.Product) {
	for i := 0; i < len(products); i++ {
		for j := i + 1; j < len(products); j++ {
			pi, pj := products[i], products[j]
			cmpTime := pi.CreatedAt.Compare(pj.CreatedAt)
			if cmpTime > 0 || (cmpTime == 0 && pi.ID > pj.ID) {
				products[i], products[j] = products[j], products[i]
			}
		}
	}
}

// applyKeysetPagination applies forward/backward pagination using keyset cursors
func applyKeysetPagination(products []*datastore.Product, after, before string, first, last int) []*datastore.Product {
	if len(products) == 0 {
		return products
	}

	start, end := 0, len(products)

	// Apply "after" cursor: find first product after the cursor
	if after != "" {
		for i, p := range products {
			if compareKeysetPosition(p, after) > 0 {
				start = i
				break
			}
		}
	}

	// Apply "before" cursor: find first product before the cursor
	if before != "" {
		for i, p := range products {
			if compareKeysetPosition(p, before) >= 0 {
				end = i
				break
			}
		}
	}

	if start >= end {
		return []*datastore.Product{}
	}

	// Apply "first" limit
	if first > 0 && first < end-start {
		end = start + first
	}

	// Apply "last" limit
	if last > 0 && last < end-start {
		start = end - last
	}

	return products[start:end]
}

// compareKeysetPosition returns:
// < 0 if product is before cursor
// = 0 if product is at cursor
// > 0 if product is after cursor
func compareKeysetPosition(product *datastore.Product, cursor string) int {
	kc, err := decodeKeysetCursorInternal(cursor)
	if err != nil {
		return 0 // Treat invalid cursors as "at position"
	}

	cmpTime := product.CreatedAt.Compare(kc.CreatedAt)
	if cmpTime != 0 {
		return cmpTime
	}
	if product.ID < kc.ID {
		return -1
	}
	if product.ID > kc.ID {
		return 1
	}
	return 0
}

// decodeKeysetCursorInternal is internal to memdb; callers use the graph package's DecodeKeysetCursor
type keysetCursor struct {
	CreatedAt time.Time
	ID        string
}

func decodeKeysetCursorInternal(cursor string) (*keysetCursor, error) {
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
	return &keysetCursor{CreatedAt: ts, ID: parts[2]}, nil
}

func (m *memdbDatastore) UpdateProduct(_ context.Context, p *datastore.Product) error {
	txn := m.db.Txn(true)
	if raw, _ := txn.First("product", "id", p.ID); raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: product id %s", datastore.ErrNotFound, p.ID)
	}
	if err := txn.Insert("product", p); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: update product: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) DeleteProduct(_ context.Context, id string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("product", "id", id)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: product id %s", datastore.ErrNotFound, id)
	}
	if err := txn.Delete("product", raw); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: delete product: %w", err)
	}
	txn.Commit()
	return nil
}

// ── Category ──────────────────────────────────────────────────────────────────

func (m *memdbDatastore) CreateCategory(_ context.Context, c *datastore.Category) error {
	txn := m.db.Txn(true)
	if raw, _ := txn.First("category", "id", c.ID); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: category id %s", datastore.ErrAlreadyExists, c.ID)
	}
	if raw, _ := txn.First("category", "slug", c.Slug); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: category slug %s", datastore.ErrAlreadyExists, c.Slug)
	}
	if err := txn.Insert("category", c); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: insert category: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) GetCategory(_ context.Context, id string) (*datastore.Category, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("category", "id", id)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return raw.(*datastore.Category), nil
}

func (m *memdbDatastore) GetCategoryBySlug(_ context.Context, slug string) (*datastore.Category, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("category", "slug", slug)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return raw.(*datastore.Category), nil
}

func (m *memdbDatastore) ListCategories(_ context.Context) ([]*datastore.Category, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get("category", "id")
	if err != nil {
		return nil, fmt.Errorf("memdb: list categories: %w", err)
	}
	var results []*datastore.Category
	for obj := it.Next(); obj != nil; obj = it.Next() {
		results = append(results, obj.(*datastore.Category))
	}
	sortCategoriesByCreatedAtThenID(results)
	return results, nil
}

func (m *memdbDatastore) UpdateCategory(_ context.Context, c *datastore.Category) error {
	txn := m.db.Txn(true)
	if raw, _ := txn.First("category", "id", c.ID); raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: category id %s", datastore.ErrNotFound, c.ID)
	}
	if err := txn.Insert("category", c); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: update category: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) DeleteCategory(_ context.Context, id string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("category", "id", id)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: category id %s", datastore.ErrNotFound, id)
	}
	if err := txn.Delete("category", raw); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: delete category: %w", err)
	}
	txn.Commit()
	return nil
}

// ── Collection ────────────────────────────────────────────────────────────────

func (m *memdbDatastore) CreateCollection(_ context.Context, c *datastore.Collection) error {
	txn := m.db.Txn(true)
	if raw, _ := txn.First("collection", "id", c.ID); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: collection id %s", datastore.ErrAlreadyExists, c.ID)
	}
	if raw, _ := txn.First("collection", "slug", c.Slug); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: collection slug %s", datastore.ErrAlreadyExists, c.Slug)
	}
	if err := txn.Insert("collection", c); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: insert collection: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) GetCollection(_ context.Context, id string) (*datastore.Collection, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("collection", "id", id)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return raw.(*datastore.Collection), nil
}

func (m *memdbDatastore) GetCollectionBySlug(_ context.Context, slug string) (*datastore.Collection, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("collection", "slug", slug)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return raw.(*datastore.Collection), nil
}

func (m *memdbDatastore) ListCollections(_ context.Context) ([]*datastore.Collection, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get("collection", "id")
	if err != nil {
		return nil, fmt.Errorf("memdb: list collections: %w", err)
	}
	var results []*datastore.Collection
	for obj := it.Next(); obj != nil; obj = it.Next() {
		results = append(results, obj.(*datastore.Collection))
	}
	sortCollectionsByCreatedAtThenID(results)
	return results, nil
}

func (m *memdbDatastore) UpdateCollection(_ context.Context, c *datastore.Collection) error {
	txn := m.db.Txn(true)
	if raw, _ := txn.First("collection", "id", c.ID); raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: collection id %s", datastore.ErrNotFound, c.ID)
	}
	if err := txn.Insert("collection", c); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: update collection: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) DeleteCollection(_ context.Context, id string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("collection", "id", id)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: collection id %s", datastore.ErrNotFound, id)
	}
	if err := txn.Delete("collection", raw); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: delete collection: %w", err)
	}
	txn.Commit()
	return nil
}

// ── Namespace ─────────────────────────────────────────────────────────────────

func (m *memdbDatastore) CreateNamespace(_ context.Context, ns *datastore.Namespace) error {
	if ns == nil {
		return fmt.Errorf("%w: namespace is nil", datastore.ErrInvalidArgument)
	}
	if ns.ID == "" {
		return fmt.Errorf("%w: namespace id is empty", datastore.ErrInvalidArgument)
	}
	txn := m.db.Txn(true)
	if raw, _ := txn.First("namespaces", "id", ns.ID); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: namespace id %s", datastore.ErrAlreadyExists, ns.ID)
	}
	if raw, _ := txn.First("namespaces", "identifier", ns.Identifier); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: namespace identifier %s", datastore.ErrAlreadyExists, ns.Identifier)
	}
	if err := txn.Insert("namespaces", ns); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: insert namespace: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) GetNamespace(_ context.Context, id string) (*datastore.Namespace, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("namespaces", "id", id)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return raw.(*datastore.Namespace), nil
}

func (m *memdbDatastore) GetNamespaceByIdentifier(_ context.Context, identifier string) (*datastore.Namespace, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("namespaces", "identifier", identifier)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return raw.(*datastore.Namespace), nil
}

func (m *memdbDatastore) ListNamespaces(_ context.Context) ([]*datastore.Namespace, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get("namespaces", "id")
	if err != nil {
		return nil, fmt.Errorf("memdb: list namespaces: %w", err)
	}
	var results []*datastore.Namespace
	for obj := it.Next(); obj != nil; obj = it.Next() {
		results = append(results, obj.(*datastore.Namespace))
	}
	return results, nil
}

func (m *memdbDatastore) DeleteNamespace(_ context.Context, id string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("namespaces", "id", id)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: namespace id %s", datastore.ErrNotFound, id)
	}
	if err := txn.Delete("namespaces", raw); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: delete namespace: %w", err)
	}
	txn.Commit()
	return nil
}

// ── Repository ────────────────────────────────────────────────────────────────

func (m *memdbDatastore) CreateRepository(_ context.Context, r *datastore.Repository) error {
	txn := m.db.Txn(true)
	if raw, _ := txn.First("repository", "id", r.ID); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: repository id %s", datastore.ErrAlreadyExists, r.ID)
	}
	if err := txn.Insert("repository", r); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: insert repository: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) GetRepository(_ context.Context, id string) (*datastore.Repository, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("repository", "id", id)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return raw.(*datastore.Repository), nil
}

func (m *memdbDatastore) ListRepositoriesByNamespace(_ context.Context, namespaceID string) ([]*datastore.Repository, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get("repository", "namespace_id", namespaceID)
	if err != nil {
		return nil, fmt.Errorf("memdb: list repositories by namespace: %w", err)
	}
	var results []*datastore.Repository
	for obj := it.Next(); obj != nil; obj = it.Next() {
		results = append(results, obj.(*datastore.Repository))
	}
	return results, nil
}

func (m *memdbDatastore) UpdateRepository(_ context.Context, r *datastore.Repository) error {
	txn := m.db.Txn(true)
	if raw, _ := txn.First("repository", "id", r.ID); raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: repository id %s", datastore.ErrNotFound, r.ID)
	}
	if err := txn.Insert("repository", r); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: update repository: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) DeleteRepository(_ context.Context, id string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("repository", "id", id)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: repository id %s", datastore.ErrNotFound, id)
	}
	if err := txn.Delete("repository", raw); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: delete repository: %w", err)
	}
	txn.Commit()
	return nil
}

// ── NamespaceMapping ──────────────────────────────────────────────────────────

func (m *memdbDatastore) CreateNamespaceMapping(_ context.Context, mp *datastore.NamespaceMapping) error {
	txn := m.db.Txn(true)
	if raw, _ := txn.First("namespace_mapping", "id", mp.NamespaceID, mp.Name); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: namespace_mapping (%s, %s)", datastore.ErrAlreadyExists, mp.NamespaceID, mp.Name)
	}
	if err := txn.Insert("namespace_mapping", mp); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: insert namespace_mapping: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) LookupRepository(_ context.Context, namespaceID, name string) (*datastore.NamespaceMapping, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("namespace_mapping", "id", namespaceID, name)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return raw.(*datastore.NamespaceMapping), nil
}

func (m *memdbDatastore) LookupNamespaceByRepoID(_ context.Context, repoID string) (*datastore.NamespaceMapping, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("namespace_mapping", "repo_id", repoID)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return raw.(*datastore.NamespaceMapping), nil
}

func (m *memdbDatastore) RenameRepository(_ context.Context, namespaceID, oldName, newName string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("namespace_mapping", "id", namespaceID, oldName)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: namespace_mapping (%s, %s)", datastore.ErrNotFound, namespaceID, oldName)
	}
	old := raw.(*datastore.NamespaceMapping)
	if err := txn.Delete("namespace_mapping", old); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: rename delete old mapping: %w", err)
	}
	updated := &datastore.NamespaceMapping{
		NamespaceID: namespaceID,
		Name:        newName,
		RepoID:      old.RepoID,
	}
	if err := txn.Insert("namespace_mapping", updated); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: rename insert new mapping: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) TransferRepository(_ context.Context, repoID, fromNamespaceID, toNamespaceID string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("namespace_mapping", "repo_id", repoID)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: namespace_mapping repo_id %s", datastore.ErrNotFound, repoID)
	}
	old := raw.(*datastore.NamespaceMapping)
	if err := txn.Delete("namespace_mapping", old); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: transfer delete old mapping: %w", err)
	}
	updated := &datastore.NamespaceMapping{
		NamespaceID: toNamespaceID,
		Name:        old.Name,
		RepoID:      repoID,
	}
	if err := txn.Insert("namespace_mapping", updated); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: transfer insert new mapping: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) DeleteNamespaceMapping(_ context.Context, namespaceID, name string) error {
	txn := m.db.Txn(true)
	raw, _ := txn.First("namespace_mapping", "id", namespaceID, name)
	if raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: namespace_mapping (%s, %s)", datastore.ErrNotFound, namespaceID, name)
	}
	if err := txn.Delete("namespace_mapping", raw); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: delete namespace_mapping: %w", err)
	}
	txn.Commit()
	return nil
}

// sortCategoriesByCreatedAtThenID sorts categories by created_at ascending, then by id as tie-breaker
func sortCategoriesByCreatedAtThenID(categories []*datastore.Category) {
	for i := 0; i < len(categories); i++ {
		for j := i + 1; j < len(categories); j++ {
			ci, cj := categories[i], categories[j]
			cmpTime := ci.CreatedAt.Compare(cj.CreatedAt)
			if cmpTime > 0 || (cmpTime == 0 && ci.ID > cj.ID) {
				categories[i], categories[j] = categories[j], categories[i]
			}
		}
	}
}

// sortCollectionsByCreatedAtThenID sorts collections by created_at ascending, then by id as tie-breaker
func sortCollectionsByCreatedAtThenID(collections []*datastore.Collection) {
	for i := 0; i < len(collections); i++ {
		for j := i + 1; j < len(collections); j++ {
			ci, cj := collections[i], collections[j]
			cmpTime := ci.CreatedAt.Compare(cj.CreatedAt)
			if cmpTime > 0 || (cmpTime == 0 && ci.ID > cj.ID) {
				collections[i], collections[j] = collections[j], collections[i]
			}
		}
	}
}
