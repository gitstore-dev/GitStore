// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Type converters between datastore and GraphQL models

package graph

import (
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/shopspring/decimal"
)

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

// DatastoreNamespaceToGraphQL is the exported version of datastoreNamespaceToModel.
func DatastoreNamespaceToGraphQL(ns *datastore.Namespace) *model.Namespace {
	return datastoreNamespaceToModel(ns)
}

// DatastoreProductToGraphQL converts a datastore Product to a GraphQL model Product.
func DatastoreProductToGraphQL(p *datastore.Product) *model.Product {
	if p == nil {
		return nil
	}
	var invQty *int32
	if p.InventoryQuantity != nil {
		qty32 := int32(*p.InventoryQuantity)
		invQty = &qty32
	}
	return &model.Product{
		ID:                mustEncodeNodeID(nodeKindProduct, p.ID),
		Title:             p.Title,
		Sku:               p.SKU,
		Price:             decimal.NewFromFloat(p.Price),
		Currency:          p.Currency,
		Body:              &p.Body,
		InventoryStatus:   model.InventoryStatus(p.InventoryStatus),
		InventoryQuantity: invQty,
		Category:          nil,
		Collections:       []*model.Collection{},
		Images:            p.Images,
		Metadata:          p.Metadata,
		CreatedAt:         p.CreatedAt,
		UpdatedAt:         p.UpdatedAt,
	}
}

// DatastoreCategoryToGraphQL converts a datastore Category to a GraphQL model Category.
func DatastoreCategoryToGraphQL(c *datastore.Category) *model.Category {
	if c == nil {
		return nil
	}
	return &model.Category{
		ID:        mustEncodeNodeID(nodeKindCategory, c.ID),
		Name:      c.Name,
		Slug:      c.Slug,
		Body:      &c.Body,
		Parent:    nil,
		Children:  []*model.Category{},
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}

// DatastoreCollectionToGraphQL converts a datastore Collection to a GraphQL model Collection.
func DatastoreCollectionToGraphQL(c *datastore.Collection) *model.Collection {
	if c == nil {
		return nil
	}
	return &model.Collection{
		ID:        mustEncodeNodeID(nodeKindCollection, c.ID),
		Name:      c.Name,
		Slug:      c.Slug,
		Body:      &c.Body,
		Products:  nil,
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}

// DatastoreRepositoryToGraphQL converts a datastore Repository to the GraphQL model
// without namespace (namespace is resolved separately via field resolver).
func DatastoreRepositoryToGraphQL(r *datastore.Repository) *model.Repository {
	if r == nil {
		return nil
	}
	return &model.Repository{
		ID:            mustEncodeNodeID(nodeKindRepository, r.ID),
		Name:          r.Name,
		DefaultBranch: r.DefaultBranch,
		StorageClass:  r.StorageClass,
		CreatedAt:     r.CreatedAt,
		CreatedBy:     r.CreatedBy,
		UpdatedAt:     r.UpdatedAt,
		UpdatedBy:     r.UpdatedBy,
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
