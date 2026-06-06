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
	txn := m.db.Txn(true)
	if raw, _ := txn.First("product", "id", p.UID); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: product uid %s", datastore.ErrAlreadyExists, p.UID)
	}
	if raw, _ := txn.First("product", "name_namespace", p.Namespace, p.Name); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: product %s/%s", datastore.ErrAlreadyExists, p.Namespace, p.Name)
	}
	if err := txn.Insert("product", p); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: insert product: %w", err)
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
	return raw.(*datastore.Product), nil
}

func (m *memdbDatastore) GetProductByName(_ context.Context, namespace, name string) (*datastore.Product, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("product", "name_namespace", namespace, name)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return raw.(*datastore.Product), nil
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
		all = append(all, obj.(*datastore.Product))
	}

	return paginateSlice(all, page, func(p *datastore.Product) (time.Time, string) {
		return p.CreationTimestamp, p.UID
	}), nil
}

func (m *memdbDatastore) UpdateProduct(_ context.Context, p *datastore.Product) error {
	txn := m.db.Txn(true)
	if raw, _ := txn.First("product", "id", p.UID); raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: product uid %s", datastore.ErrNotFound, p.UID)
	}
	if err := txn.Insert("product", p); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: update product: %w", err)
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
	if err := txn.Delete("product", raw); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: delete product: %w", err)
	}
	txn.Commit()
	return nil
}

// ── CategoryTaxonomy ──────────────────────────────────────────────────────────

func (m *memdbDatastore) CreateCategoryTaxonomy(_ context.Context, c *datastore.CategoryTaxonomy) error {
	txn := m.db.Txn(true)
	if raw, _ := txn.First("category_taxonomy", "id", c.UID); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: category_taxonomy uid %s", datastore.ErrAlreadyExists, c.UID)
	}
	if raw, _ := txn.First("category_taxonomy", "name_namespace", c.Namespace, c.Name); raw != nil {
		txn.Abort()
		return fmt.Errorf("%w: category_taxonomy %s/%s", datastore.ErrAlreadyExists, c.Namespace, c.Name)
	}
	if err := txn.Insert("category_taxonomy", c); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: insert category_taxonomy: %w", err)
	}
	txn.Commit()
	return nil
}

func (m *memdbDatastore) GetCategoryTaxonomyByName(_ context.Context, namespace, name string) (*datastore.CategoryTaxonomy, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("category_taxonomy", "name_namespace", namespace, name)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return raw.(*datastore.CategoryTaxonomy), nil
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
		all = append(all, obj.(*datastore.CategoryTaxonomy))
	}
	return paginateSlice(all, page, func(c *datastore.CategoryTaxonomy) (time.Time, string) {
		return c.CreationTimestamp, c.UID
	}), nil
}

func (m *memdbDatastore) UpdateCategoryTaxonomy(_ context.Context, c *datastore.CategoryTaxonomy) error {
	txn := m.db.Txn(true)
	if raw, _ := txn.First("category_taxonomy", "name_namespace", c.Namespace, c.Name); raw == nil {
		txn.Abort()
		return fmt.Errorf("%w: category_taxonomy %s/%s", datastore.ErrNotFound, c.Namespace, c.Name)
	}
	if err := txn.Insert("category_taxonomy", c); err != nil {
		txn.Abort()
		return fmt.Errorf("memdb: update category_taxonomy: %w", err)
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

func (m *memdbDatastore) ListCollections(_ context.Context, page datastore.PageParams) (*datastore.PageResult[datastore.Collection], error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get("collection", "id")
	if err != nil {
		return nil, fmt.Errorf("memdb: list collections: %w", err)
	}
	var all []*datastore.Collection
	for obj := it.Next(); obj != nil; obj = it.Next() {
		all = append(all, obj.(*datastore.Collection))
	}
	return paginateSlice(all, page, func(c *datastore.Collection) (time.Time, string) {
		return c.CreatedAt, c.ID
	}), nil
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

func (m *memdbDatastore) ListNamespaces(_ context.Context, page datastore.PageParams) (*datastore.PageResult[datastore.Namespace], error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get("namespaces", "id")
	if err != nil {
		return nil, fmt.Errorf("memdb: list namespaces: %w", err)
	}
	var all []*datastore.Namespace
	for obj := it.Next(); obj != nil; obj = it.Next() {
		all = append(all, obj.(*datastore.Namespace))
	}
	return paginateSlice(all, page, func(ns *datastore.Namespace) (time.Time, string) {
		return ns.CreatedAt, ns.ID
	}), nil
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

func (m *memdbDatastore) ListRepositoriesByNamespace(_ context.Context, namespaceID string, page datastore.PageParams) (*datastore.PageResult[datastore.Repository], error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get("repository", "namespace_id", namespaceID)
	if err != nil {
		return nil, fmt.Errorf("memdb: list repositories by namespace: %w", err)
	}
	var all []*datastore.Repository
	for obj := it.Next(); obj != nil; obj = it.Next() {
		all = append(all, obj.(*datastore.Repository))
	}
	return paginateSlice(all, page, func(r *datastore.Repository) (time.Time, string) {
		return r.CreatedAt, r.ID
	}), nil
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
