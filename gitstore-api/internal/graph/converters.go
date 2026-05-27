// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Type converters between catalog and GraphQL models

package graph

import (
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/scalar"
	"github.com/shopspring/decimal"
)

// CatalogProductToGraphQL converts a catalog product to a GraphQL product
func CatalogProductToGraphQL(p *catalog.Product) *model.Product {
	if p == nil {
		return nil
	}

	// Convert inventory quantity from *int to *int32
	var invQty *int32
	if p.InventoryQuantity != nil {
		qty32 := int32(*p.InventoryQuantity)
		invQty = &qty32
	}

	return &model.Product{
		ID:                mustEncodeNodeID(nodeKindProduct, p.ID),
		Title:             p.Title,
		Sku:               p.SKU,
		Price:             scalar.Decimal{Decimal: decimal.NewFromFloat(p.Price)},
		Currency:          p.Currency,
		Body:              &p.Body,
		InventoryStatus:   model.InventoryStatus(p.InventoryStatus),
		InventoryQuantity: invQty,
		Category:          nil,                   // TODO: lookup category if needed
		Collections:       []*model.Collection{}, // TODO: lookup collections if needed
		Images:            p.Images,
		Metadata:          p.Metadata,
		CreatedAt:         p.CreatedAt,
		UpdatedAt:         p.UpdatedAt,
	}
}

// CatalogCategoryToGraphQL converts a catalog category to a GraphQL category
func CatalogCategoryToGraphQL(c *catalog.Category) *model.Category {
	if c == nil {
		return nil
	}

	return &model.Category{
		ID:        mustEncodeNodeID(nodeKindCategory, c.ID),
		Name:      c.Name,
		Slug:      c.Slug,
		Body:      &c.Body,
		Parent:    nil,                 // TODO: lookup parent if needed
		Children:  []*model.Category{}, // TODO: lookup children if needed
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}

// CatalogCollectionToGraphQL converts a catalog collection to a GraphQL collection
func CatalogCollectionToGraphQL(c *catalog.Collection) *model.Collection {
	if c == nil {
		return nil
	}

	return &model.Collection{
		ID:        mustEncodeNodeID(nodeKindCollection, c.ID),
		Name:      c.Name,
		Slug:      c.Slug,
		Body:      &c.Body,
		Products:  nil, // TODO: Will be resolved by GraphQL field resolver
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}

// datastoreNamespaceToModel converts a datastore Namespace to a GraphQL model Namespace.
func datastoreNamespaceToModel(ns *datastore.Namespace) *model.Namespace {
	if ns == nil {
		return nil
	}
	var displayName *string
	if ns.DisplayName != "" {
		dn := ns.DisplayName
		displayName = &dn
	}
	var parentEnterpriseID *string
	if ns.ParentEnterpriseID != nil {
		encoded := mustEncodeNodeID(nodeKindNamespace, *ns.ParentEnterpriseID)
		parentEnterpriseID = &encoded
	}
	return &model.Namespace{
		ID:                 mustEncodeNodeID(nodeKindNamespace, ns.ID),
		Identifier:         ns.Identifier,
		DisplayName:        displayName,
		Tier:               datastoreNamespaceTierToModel(ns.Tier),
		ParentEnterpriseID: parentEnterpriseID,
		CreatedAt:          ns.CreatedAt,
		CreatedBy:          ns.CreatedBy,
		UpdatedAt:          ns.UpdatedAt,
		UpdatedBy:          ns.UpdatedBy,
	}
}

func datastoreNamespaceTierToModel(t datastore.NamespaceTier) model.NamespaceTier {
	switch t {
	case datastore.NamespaceTierOrganisation:
		return model.NamespaceTierOrganisation
	case datastore.NamespaceTierEnterprise:
		return model.NamespaceTierEnterprise
	default:
		return model.NamespaceTierUser
	}
}

// datastoreRepositoryToModel converts a datastore Repository to the GraphQL model.
// ns may be nil if the namespace resolver has not been called yet; in that case
// the Namespace field is left nil and must be resolved via a field resolver.
func datastoreRepositoryToModel(r *datastore.Repository, ns *datastore.Namespace, dataDir string) *model.Repository {
	if r == nil {
		return nil
	}
	repo := &model.Repository{
		ID:            mustEncodeNodeID(nodeKindRepository, r.ID),
		Name:          r.Name,
		DefaultBranch: r.DefaultBranch,
		StorageClass:  r.StorageClass,
		StoragePath:   fanoutStoragePath(dataDir, r.ID),
		CreatedAt:     r.CreatedAt,
		CreatedBy:     r.CreatedBy,
		UpdatedAt:     r.UpdatedAt,
		UpdatedBy:     r.UpdatedBy,
	}
	if ns != nil {
		repo.Namespace = datastoreNamespaceToModel(ns)
	}
	return repo
}
