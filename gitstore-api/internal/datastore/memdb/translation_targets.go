// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

func (m *memdbDatastore) ReadTranslationTargetNamespaceMetadata(ctx context.Context, name string) (*datastore.TranslationTargetNamespaceMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name == "" || len(name) > 253 {
		return nil, datastore.ErrInvalidArgument
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("namespaces", "name", name)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	namespace := raw.(*datastore.Namespace)
	return &datastore.TranslationTargetNamespaceMetadata{
		UID: namespace.UID, Name: namespace.Name, OwnerSub: namespace.EffectiveOwnerSub(),
		ResourceVersion: namespace.ResourceVersion, Active: namespace.DeletionTimestamp == nil,
	}, nil
}

func (m *memdbDatastore) ReadTargetEvidence(ctx context.Context, target datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error) {
	return datastore.ReadTranslationTargetEvidence(ctx, target, m.GetTranslationTargetFence, m.ReadTranslationTargetMetadata)
}

func (m *memdbDatastore) ReadTranslationTargetMetadata(ctx context.Context, fence *datastore.TranslationTargetFence) (*datastore.TranslationTargetMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if datastore.ValidateTranslationTargetFence(fence) != nil || fence.Phase != datastore.TranslationTargetStable ||
		fence.Evidence == nil || fence.CreationTimestamp.IsZero() {
		return nil, datastore.ErrInvalidArgument
	}
	var table string
	switch fence.Target.Kind {
	case "Product":
		table = "product"
	case "ProductVariant":
		table = "product_variant"
	case "CategoryTaxonomy":
		table = "category_taxonomy"
	case "Collection":
		table = "collection"
	case "File":
		table = "file"
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First(table, "id", fence.Evidence.UID)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	source, identity := translationMaterialView(raw)
	if source == nil || identity == nil {
		return nil, datastore.ErrRepairRequired
	}
	return &datastore.TranslationTargetMetadata{
		Evidence: catalog.TranslationTargetEvidence{
			APIVersion: source.APIVersion, Kind: source.Kind, Namespace: source.Namespace, Name: source.Name,
			UID: source.UID, Generation: source.Generation, Revision: source.Revision,
			ResourceVersion: source.ResourceVersion, ContentDigest: identity.ContentDigest,
		},
		SourceIdentity: *identity, CreationTimestamp: source.CreationTimestamp, Active: source.DeletionTimestamp == nil,
		OwnerSub: source.EffectiveOwnerSub(),
	}, nil
}

var _ datastore.TranslationTargetMetadataStore = (*memdbDatastore)(nil)
var _ datastore.TranslationTargetFenceStore = (*memdbDatastore)(nil)
