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
		ID:                p.ID,
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
		ID:        c.ID,
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
		ID:        c.ID,
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
	return &model.Namespace{
		ID:                 ns.ID,
		Identifier:         ns.Identifier,
		DisplayName:        displayName,
		Tier:               datastoreNamespaceTierToModel(ns.Tier),
		ParentEnterpriseID: ns.ParentEnterpriseID,
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
