// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Type converters between datastore and GraphQL models

package graph

import (
	"encoding/json"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"go.uber.org/zap"
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

// converterLogger is a package-level logger for blob-unmarshal warnings.
// It is initialised to a no-op logger by default; callers that have a real
// logger can replace it via SetConverterLogger.
var converterLogger *zap.Logger = zap.NewNop()

// SetConverterLogger replaces the package-level logger used by converter helpers.
func SetConverterLogger(l *zap.Logger) { converterLogger = l }

// specFromJSON deserialises a ProductSpec blob. A nil/empty blob returns a
// non-nil empty spec (FR-001). Unmarshal errors are logged at WARN level and
// also return the empty spec.
func specFromJSON(raw json.RawMessage) *model.ProductSpec {
	empty := &model.ProductSpec{
		Tags:    []string{},
		Media:   []*model.MediaDefinition{},
		Options: []*model.ProductOptionDefinition{},
	}
	if len(raw) == 0 {
		return empty
	}
	var s model.ProductSpec
	if err := json.Unmarshal(raw, &s); err != nil {
		converterLogger.Warn("product blob unmarshal error", zap.String("field", "spec"), zap.Error(err))
		return empty
	}
	if s.Tags == nil {
		s.Tags = []string{}
	}
	if s.Media == nil {
		s.Media = []*model.MediaDefinition{}
	}
	if s.Options == nil {
		s.Options = []*model.ProductOptionDefinition{}
	}
	return &s
}

// statusFromJSON deserialises a ProductStatus blob. A nil/empty blob returns
// nil (FR-002). Unmarshal errors are logged at WARN and also return nil.
func statusFromJSON(raw json.RawMessage) *model.ProductStatus {
	if len(raw) == 0 {
		return nil
	}
	var s model.ProductStatus
	if err := json.Unmarshal(raw, &s); err != nil {
		converterLogger.Warn("product blob unmarshal error", zap.String("field", "status"), zap.Error(err))
		return nil
	}
	return &s
}

// ownerRefsFromJSON deserialises an OwnerRefs blob. Nil/empty or unmarshal
// errors return an empty (never nil) slice.
func ownerRefsFromJSON(raw json.RawMessage) []*model.OwnerReference {
	empty := []*model.OwnerReference{}
	if len(raw) == 0 {
		return empty
	}
	var refs []*model.OwnerReference
	if err := json.Unmarshal(raw, &refs); err != nil {
		converterLogger.Warn("product blob unmarshal error", zap.String("field", "ownerRefs"), zap.Error(err))
		return empty
	}
	if refs == nil {
		return empty
	}
	return refs
}

// DatastoreProductToGraphQL converts a datastore Product to a GraphQL model Product.
func DatastoreProductToGraphQL(p *datastore.Product) *model.Product {
	if p == nil {
		return nil
	}
	gen := int32(p.Generation)
	meta := &model.ProductObjectMeta{
		Name:              p.Name,
		Namespace:         p.Namespace,
		UID:               mustEncodeNodeID(nodeKindProduct, p.UID),
		ResourceVersion:   p.ResourceVersion,
		Generation:        gen,
		CreationTimestamp: p.CreationTimestamp,
		OwnerReferences:   ownerRefsFromJSON(p.OwnerRefs),
	}
	if p.Revision != "" {
		meta.Revision = &p.Revision
	}
	if len(p.Labels) > 0 {
		labels := make(map[string]any, len(p.Labels))
		for k, v := range p.Labels {
			labels[k] = v
		}
		meta.Labels = labels
	}
	if len(p.Annotations) > 0 {
		annotations := make(map[string]any, len(p.Annotations))
		for k, v := range p.Annotations {
			annotations[k] = v
		}
		meta.Annotations = annotations
	}
	return &model.Product{
		ID:         mustEncodeNodeID(nodeKindProduct, p.UID),
		APIVersion: p.APIVersion,
		Kind:       p.Kind,
		Metadata:   meta,
		Spec:       specFromJSON(p.Spec),
		Status:     statusFromJSON(p.Status),
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
