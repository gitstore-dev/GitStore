// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"fmt"

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
	return results, nil
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
