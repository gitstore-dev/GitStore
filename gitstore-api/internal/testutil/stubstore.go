// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package testutil provides shared test helpers for gitstore-api unit tests.
package testutil

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

// StubStore is a configurable Datastore stub. Only the three fields used by
// githttp and security middleware tests have override hooks; all other methods
// are no-ops that satisfy the full datastore.Datastore interface.
type StubStore struct {
	GetNamespaceByIdentifierFunc func(ctx context.Context, identifier string) (*datastore.Namespace, error)
	LookupRepositoryFunc         func(ctx context.Context, namespaceID, name string) (*datastore.NamespaceMapping, error)
	GetRepositoryFunc            func(ctx context.Context, id string) (*datastore.Repository, error)
}

func (s *StubStore) GetNamespaceByIdentifier(ctx context.Context, identifier string) (*datastore.Namespace, error) {
	if s.GetNamespaceByIdentifierFunc != nil {
		return s.GetNamespaceByIdentifierFunc(ctx, identifier)
	}
	return nil, datastore.ErrNotFound
}

func (s *StubStore) LookupRepository(ctx context.Context, namespaceID, name string) (*datastore.NamespaceMapping, error) {
	if s.LookupRepositoryFunc != nil {
		return s.LookupRepositoryFunc(ctx, namespaceID, name)
	}
	return nil, datastore.ErrNotFound
}

func (s *StubStore) GetRepository(ctx context.Context, id string) (*datastore.Repository, error) {
	if s.GetRepositoryFunc != nil {
		return s.GetRepositoryFunc(ctx, id)
	}
	return nil, datastore.ErrNotFound
}

func (s *StubStore) CreateProduct(_ context.Context, _ *datastore.Product) error { return nil }
func (s *StubStore) GetProduct(_ context.Context, _ string) (*datastore.Product, error) {
	return nil, datastore.ErrNotFound
}
func (s *StubStore) GetProductByName(_ context.Context, _, _ string) (*datastore.Product, error) {
	return nil, datastore.ErrNotFound
}
func (s *StubStore) ListProducts(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.Product], error) {
	return &datastore.PageResult[datastore.Product]{}, nil
}
func (s *StubStore) UpdateProduct(_ context.Context, _ *datastore.Product) error { return nil }
func (s *StubStore) DeleteProduct(_ context.Context, _ string) error             { return nil }
func (s *StubStore) CreateCategoryTaxonomy(_ context.Context, _ *datastore.CategoryTaxonomy) error {
	return nil
}
func (s *StubStore) GetCategoryTaxonomy(_ context.Context, _ string) (*datastore.CategoryTaxonomy, error) {
	return nil, datastore.ErrNotFound
}
func (s *StubStore) GetCategoryTaxonomyByName(_ context.Context, _, _ string) (*datastore.CategoryTaxonomy, error) {
	return nil, datastore.ErrNotFound
}
func (s *StubStore) ListCategoryTaxonomies(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.CategoryTaxonomy], error) {
	return &datastore.PageResult[datastore.CategoryTaxonomy]{}, nil
}
func (s *StubStore) UpdateCategoryTaxonomy(_ context.Context, _ *datastore.CategoryTaxonomy) error {
	return nil
}
func (s *StubStore) DeleteCategoryTaxonomy(_ context.Context, _ string) error { return nil }
func (s *StubStore) CreateProductVariant(_ context.Context, _ *datastore.ProductVariant) error {
	return nil
}
func (s *StubStore) GetProductVariant(_ context.Context, _ string) (*datastore.ProductVariant, error) {
	return nil, datastore.ErrNotFound
}
func (s *StubStore) GetProductVariantByName(_ context.Context, _, _ string) (*datastore.ProductVariant, error) {
	return nil, datastore.ErrNotFound
}
func (s *StubStore) GetProductVariantBySKU(_ context.Context, _, _ string) (*datastore.ProductVariant, error) {
	return nil, datastore.ErrNotFound
}
func (s *StubStore) ListProductVariants(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.ProductVariant], error) {
	return &datastore.PageResult[datastore.ProductVariant]{}, nil
}
func (s *StubStore) ListProductVariantsByProductRef(_ context.Context, _, _ string) ([]*datastore.ProductVariant, error) {
	return nil, nil
}
func (s *StubStore) UpdateProductVariant(_ context.Context, _ *datastore.ProductVariant) error {
	return nil
}
func (s *StubStore) DeleteProductVariant(_ context.Context, _ string) error { return nil }
func (s *StubStore) CreateCollection(_ context.Context, _ *datastore.Collection) error {
	return nil
}
func (s *StubStore) GetCollection(_ context.Context, _ string) (*datastore.Collection, error) {
	return nil, datastore.ErrNotFound
}
func (s *StubStore) GetCollectionByName(_ context.Context, _, _ string) (*datastore.Collection, error) {
	return nil, datastore.ErrNotFound
}
func (s *StubStore) ListCollections(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.Collection], error) {
	return &datastore.PageResult[datastore.Collection]{}, nil
}
func (s *StubStore) UpdateCollection(_ context.Context, _ *datastore.Collection) error { return nil }
func (s *StubStore) DeleteCollection(_ context.Context, _ string) error                { return nil }
func (s *StubStore) ListProductsByLabelSelector(_ context.Context, _ string, _ catalog.LabelSelector) ([]*datastore.Product, error) {
	return nil, nil
}
func (s *StubStore) CreateNamespace(_ context.Context, _ *datastore.Namespace) error { return nil }
func (s *StubStore) GetNamespace(_ context.Context, _ string) (*datastore.Namespace, error) {
	return nil, datastore.ErrNotFound
}
func (s *StubStore) ListNamespaces(_ context.Context, _ datastore.PageParams) (*datastore.PageResult[datastore.Namespace], error) {
	return &datastore.PageResult[datastore.Namespace]{}, nil
}
func (s *StubStore) DeleteNamespace(_ context.Context, _ string) error                 { return nil }
func (s *StubStore) CreateRepository(_ context.Context, _ *datastore.Repository) error { return nil }
func (s *StubStore) ListRepositoriesByNamespace(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.Repository], error) {
	return &datastore.PageResult[datastore.Repository]{}, nil
}
func (s *StubStore) UpdateRepository(_ context.Context, _ *datastore.Repository) error { return nil }
func (s *StubStore) DeleteRepository(_ context.Context, _ string) error                { return nil }
func (s *StubStore) CreateNamespaceMapping(_ context.Context, _ *datastore.NamespaceMapping) error {
	return nil
}
func (s *StubStore) LookupNamespaceByRepoID(_ context.Context, _ string) (*datastore.NamespaceMapping, error) {
	return nil, datastore.ErrNotFound
}
func (s *StubStore) RenameRepository(_ context.Context, _, _, _ string) error    { return nil }
func (s *StubStore) TransferRepository(_ context.Context, _, _, _ string) error  { return nil }
func (s *StubStore) DeleteNamespaceMapping(_ context.Context, _, _ string) error { return nil }
func (s *StubStore) Close() error                                                { return nil }
