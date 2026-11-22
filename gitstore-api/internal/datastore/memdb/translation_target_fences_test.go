// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestProductMaterialWritesCommitSourceAndFenceTogether(t *testing.T) {
	store, source := newTranslationSourceFixture(t)
	product := &datastore.Product{
		UID: uuid.NewString(), Namespace: source.Namespace, Name: "material-product",
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		Spec: json.RawMessage(`{"title":"Original"}`), Body: "original\n",
	}
	require.NoError(t, store.CreateProduct(t.Context(), product))
	readProof := func() datastore.TranslationTargetSourceIdentity {
		txn := store.db.Txn(false)
		defer txn.Abort()
		raw, err := txn.First("product", "id", product.UID)
		require.NoError(t, err)
		return raw.(*datastore.Product).TranslationTargetSource
	}
	key := datastore.TranslationTargetKey{
		NamespaceUID: source.NamespaceID, APIVersion: product.APIVersion, Kind: product.Kind, Name: product.Name,
	}
	fence, err := store.GetTranslationTargetFence(t.Context(), key)
	require.NoError(t, err)
	stored, err := store.GetProduct(t.Context(), product.UID)
	require.NoError(t, err)
	digest, err := translation.TargetContentDigest(t.Context(), product.APIVersion, product.Kind, product.Spec, product.Body)
	require.NoError(t, err)
	require.Equal(t, datastore.TranslationTargetStable, fence.Phase)
	require.Equal(t, digest, fence.Evidence.ContentDigest)
	require.Equal(t, datastore.TranslationTargetSourceIdentity{
		NamespaceUID: source.NamespaceID, WriteToken: fence.Token, ContentDigest: digest,
	}, readProof())
	require.Zero(t, stored.TranslationTargetSource, "private source proof must not escape through ordinary DTO clones")

	stored.ResourceVersion = "2"
	stored.Status = json.RawMessage(`{"observedGeneration":1}`)
	stored.TranslationTargetSource = datastore.TranslationTargetSourceIdentity{
		NamespaceUID: uuid.NewString(), WriteToken: uuid.NewString(), ContentDigest: "caller-supplied",
	}
	require.NoError(t, store.UpdateProduct(t.Context(), stored, "1"))
	unchanged, err := store.GetTranslationTargetFence(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, fence, unchanged, "status-only source changes retain the material token")
	stored, err = store.GetProduct(t.Context(), product.UID)
	require.NoError(t, err)
	require.Equal(t, fence.Token, readProof().WriteToken)
	stored.Body, stored.ResourceVersion, stored.Generation = "changed\r\n", "3", 2
	require.NoError(t, store.UpdateProduct(t.Context(), stored, "2"))
	changed, err := store.GetTranslationTargetFence(t.Context(), key)
	require.NoError(t, err)
	require.NotEqual(t, fence.Token, changed.Token)
	require.NotEqual(t, digest, changed.Evidence.ContentDigest)
	stored, err = store.GetProduct(t.Context(), product.UID)
	require.NoError(t, err)
	require.Equal(t, "changed\r\n", stored.Body)
	require.Equal(t, changed.Token, readProof().WriteToken)
	require.Equal(t, changed.Evidence.ContentDigest, readProof().ContentDigest)
	_, err = store.BeginTranslationTargetWrite(t.Context(), key, datastore.TranslationTargetWriteIntent{
		ID: uuid.NewString(), PreviousUID: product.UID, NextUID: product.UID,
		ExpectedResourceVersion: "3", SourceDigest: "another-writer",
	}, changed.Token)
	require.NoError(t, err)
	stored.Body, stored.ResourceVersion, stored.Generation = "must not commit", "4", 3
	require.ErrorIs(t, store.UpdateProduct(t.Context(), stored, "3"), datastore.ErrConflict)
	preserved, err := store.GetProduct(t.Context(), product.UID)
	require.NoError(t, err)
	require.Equal(t, "changed\r\n", preserved.Body)
	require.Equal(t, "3", preserved.ResourceVersion)
	require.Equal(t, changed.Token, readProof().WriteToken)
}

func TestAllTargetKindsCommitMaterialUpdatesAndRetirementFences(t *testing.T) {
	store, source := newTranslationSourceFixture(t)
	makeProduct := func(kind string) datastore.Product {
		version := "catalog.gitstore.dev/v1beta1"
		if kind == "File" {
			version = "storage.gitstore.dev/v1beta1"
		}
		return datastore.Product{
			UID: uuid.NewString(), Namespace: source.Namespace, Name: "material-" + kind,
			APIVersion: version, Kind: kind, Generation: 1, ResourceVersion: "1",
			CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
			Spec:              json.RawMessage(`{"title":"Original"}`), Body: "original\n",
		}
	}
	product := makeProduct("Product")
	file := datastore.File(makeProduct("File"))
	collection := datastore.Collection(makeProduct("Collection"))
	categoryBase, variantBase := makeProduct("CategoryTaxonomy"), makeProduct("ProductVariant")
	category := datastore.CategoryTaxonomy{
		UID: categoryBase.UID, Namespace: source.Namespace, Name: categoryBase.Name,
		APIVersion: categoryBase.APIVersion, Kind: categoryBase.Kind, Generation: 1, ResourceVersion: "1",
		CreationTimestamp: categoryBase.CreationTimestamp, Spec: categoryBase.Spec, Body: categoryBase.Body,
	}
	variant := datastore.ProductVariant{
		UID: variantBase.UID, Namespace: source.Namespace, Name: variantBase.Name,
		APIVersion: variantBase.APIVersion, Kind: variantBase.Kind, Generation: 1, ResourceVersion: "1",
		CreationTimestamp: variantBase.CreationTimestamp, Spec: variantBase.Spec, Body: variantBase.Body,
		SKU: variantBase.UID, ProductRefName: "parent",
	}
	for _, test := range []struct {
		table    string
		resource any
		create   func() error
		update   func() error
		remove   func() error
	}{
		{"product", &product,
			func() error { return store.CreateProduct(t.Context(), &product) },
			func() error {
				product.Body, product.ResourceVersion, product.Generation = "changed", "2", 2
				return store.UpdateProduct(t.Context(), &product, "1")
			},
			func() error { return store.DeleteProduct(t.Context(), product.UID) }},
		{"file", &file,
			func() error { return store.CreateFile(t.Context(), &file) },
			func() error {
				file.Body, file.ResourceVersion, file.Generation = "changed", "2", 2
				return store.UpdateFile(t.Context(), &file, "1")
			},
			func() error { return store.DeleteFile(t.Context(), file.UID) }},
		{"collection", &collection,
			func() error { return store.CreateCollection(t.Context(), &collection) },
			func() error {
				collection.Body, collection.ResourceVersion, collection.Generation = "changed", "2", 2
				return store.UpdateCollection(t.Context(), &collection, "1")
			},
			func() error { return store.DeleteCollection(t.Context(), collection.UID) }},
		{"category_taxonomy", &category,
			func() error { return store.CreateCategoryTaxonomy(t.Context(), &category) },
			func() error {
				category.Body, category.ResourceVersion, category.Generation = "changed", "2", 2
				return store.UpdateCategoryTaxonomy(t.Context(), &category, "1")
			},
			func() error { return store.DeleteCategoryTaxonomy(t.Context(), category.UID) }},
		{"product_variant", &variant,
			func() error { return store.CreateProductVariant(t.Context(), &variant) },
			func() error {
				variant.Body, variant.ResourceVersion, variant.Generation = "changed", "2", 2
				return store.UpdateProductVariant(t.Context(), &variant, "1")
			},
			func() error { return store.DeleteProductVariant(t.Context(), variant.UID) }},
	} {
		t.Run(test.table, func(t *testing.T) {
			view, _ := translationMaterialView(test.resource)
			key := datastore.TranslationTargetKey{
				NamespaceUID: source.NamespaceID, APIVersion: view.APIVersion, Kind: view.Kind, Name: view.Name,
			}
			require.NoError(t, test.create())
			initial, err := store.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.Equal(t, view.UID, initial.Evidence.UID)
			require.NoError(t, test.update())
			changed, err := store.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.NotEqual(t, initial.Token, changed.Token)
			txn := store.db.Txn(false)
			raw, err := txn.First(test.table, "id", view.UID)
			require.NoError(t, err)
			stored, proof := translationMaterialView(raw)
			require.Equal(t, "changed", stored.Body)
			require.Equal(t, changed.Token, proof.WriteToken)
			require.Equal(t, source.NamespaceID, proof.NamespaceUID)
			require.Equal(t, changed.Evidence.ContentDigest, proof.ContentDigest)
			txn.Abort()
			require.NoError(t, test.remove())
			absent, err := store.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.Equal(t, datastore.TranslationTargetStable, absent.Phase)
			require.Nil(t, absent.Evidence)
			require.NotEqual(t, changed.Token, absent.Token)
		})
	}
}

func TestTargetFenceRollbackAndLegacyNameReuse(t *testing.T) {
	store, source := newTranslationSourceFixture(t)
	product := &datastore.Product{
		UID: uuid.NewString(), Namespace: source.Namespace, Name: "move-and-reuse",
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		Spec: json.RawMessage(`{"title":"Original"}`), Body: "original",
		OwnerReferences: json.RawMessage("invalid"),
	}
	key := datastore.TranslationTargetKey{
		NamespaceUID: source.NamespaceID, APIVersion: product.APIVersion, Kind: product.Kind, Name: product.Name,
	}
	require.Error(t, store.CreateProduct(t.Context(), product))
	_, err := store.GetProduct(t.Context(), product.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	_, err = store.GetTranslationTargetFence(t.Context(), key)
	require.ErrorIs(t, err, datastore.ErrNotFound, "a later projection failure rolls back the source and head together")
	product.OwnerReferences = nil
	require.NoError(t, store.CreateProduct(t.Context(), product))
	product.Name, product.ResourceVersion = "moved", "2"
	require.NoError(t, store.UpdateProduct(t.Context(), product, "1"))
	old, err := store.GetTranslationTargetFence(t.Context(), key)
	require.NoError(t, err)
	require.Nil(t, old.Evidence, "legacy name movement must not leave the old name reserved")
	txn := store.db.Txn(false)
	raw, err := txn.First("product", "id", product.UID)
	require.NoError(t, err)
	require.Empty(t, raw.(*datastore.Product).TranslationTargetSource.WriteToken,
		"unsupported movement cannot inherit positive source proof")
	txn.Abort()
	replacement := *product
	replacement.UID, replacement.Name, replacement.ResourceVersion = uuid.NewString(), key.Name, "1"
	require.NoError(t, store.CreateProduct(t.Context(), &replacement))
	current, err := store.GetTranslationTargetFence(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, replacement.UID, current.Evidence.UID)
	moved, err := store.GetProductByName(t.Context(), product.Namespace, product.Name)
	require.NoError(t, err)
	require.Equal(t, product.UID, moved.UID)
}

func TestLegacyFileVersionUpdateStillPermitsDeletionAndNameReplacement(t *testing.T) {
	store, source := newTranslationSourceFixture(t)
	file := &datastore.File{
		UID: uuid.NewString(), Namespace: source.Namespace, Name: "legacy-file-version",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		Spec: json.RawMessage(`{}`), Body: "original",
	}
	key := datastore.TranslationTargetKey{
		NamespaceUID: source.NamespaceID, APIVersion: file.APIVersion, Kind: file.Kind, Name: file.Name,
	}
	require.NoError(t, store.CreateFile(t.Context(), file))
	file.Body, file.Generation = "changed", 2
	require.NoError(t, store.UpdateFile(t.Context(), file, "1"), "retain the existing legacy File version transition")
	require.NoError(t, store.DeleteFileWithResourceVersion(t.Context(), file.UID, "1"))
	absent, err := store.GetTranslationTargetFence(t.Context(), key)
	require.NoError(t, err)
	require.Nil(t, absent.Evidence)
	file.UID = uuid.NewString()
	require.NoError(t, store.CreateFile(t.Context(), file))
	replacement, err := store.GetTranslationTargetFence(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, file.UID, replacement.Evidence.UID)
}

func TestUnprovableFileUpdatesRevokeOnlyOwnedEvidence(t *testing.T) {
	for _, transition := range []string{"same-version", "unsupported-api-version", "pending-same-version"} {
		t.Run(transition, func(t *testing.T) {
			store, source := newTranslationSourceFixture(t)
			file := &datastore.File{
				UID: uuid.NewString(), Namespace: source.Namespace, Name: "unprovable-file",
				APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
				Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
				Spec: json.RawMessage(`{}`), Body: "original",
			}
			require.NoError(t, store.CreateFile(t.Context(), file))
			key := datastore.TranslationTargetKey{
				NamespaceUID: source.NamespaceID, APIVersion: file.APIVersion, Kind: file.Kind, Name: file.Name,
			}
			initial, err := store.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			if transition == "pending-same-version" {
				_, err := store.BeginTranslationTargetWrite(t.Context(), key, datastore.TranslationTargetWriteIntent{
					ID: uuid.NewString(), PreviousUID: file.UID, NextUID: file.UID,
					ExpectedResourceVersion: "1", SourceDigest: "pending-material",
				}, initial.Token)
				require.NoError(t, err)
			}
			file.Body, file.Generation = "changed", 2
			if transition == "unsupported-api-version" {
				file.APIVersion, file.ResourceVersion = "storage.gitstore.dev/v9999", "2"
			}
			err = store.UpdateFile(t.Context(), file, "1")
			if transition == "pending-same-version" {
				require.ErrorIs(t, err, datastore.ErrConflict)
				preserved, err := store.GetFile(t.Context(), file.UID)
				require.NoError(t, err)
				require.Equal(t, "original", preserved.Body)
				current, err := store.GetTranslationTargetFence(t.Context(), key)
				require.NoError(t, err)
				require.Equal(t, datastore.TranslationTargetUpdating, current.Phase)
				return
			}
			require.NoError(t, err, "unprovable evidence must not remove supported legacy File CRUD")
			current, err := store.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.NotEqual(t, initial.Token, current.Token)
			require.Equal(t, datastore.TranslationTargetStable, current.Phase)
			require.Nil(t, current.Evidence, "the old material can no longer be advertised by its name fence")
			stored, err := store.GetFile(t.Context(), file.UID)
			require.NoError(t, err)
			require.Equal(t, file.Body, stored.Body)
			txn := store.db.Txn(false)
			defer txn.Abort()
			raw, err := txn.First("file", "id", file.UID)
			require.NoError(t, err)
			require.Empty(t, raw.(*datastore.File).TranslationTargetSource.WriteToken)
		})
	}
}

func TestTranslationTargetFenceMissingPayloadFailsClosed(t *testing.T) {
	for _, operation := range []string{"read", "begin", "complete"} {
		t.Run(operation, func(t *testing.T) {
			store, _ := newTranslationSourceFixture(t)
			target := datastore.TranslationTargetKey{
				NamespaceUID: uuid.NewString(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "corrupt",
			}
			id, err := datastore.TranslationTargetFenceID(target)
			require.NoError(t, err)
			txn := store.db.Txn(true)
			require.NoError(t, txn.Insert("translation_target_fence", &translationTargetFenceRow{ID: id}))
			txn.Commit()
			run := func(ctx context.Context) error {
				switch operation {
				case "read":
					_, err := store.GetTranslationTargetFence(ctx, target)
					return err
				case "begin":
					_, err := store.BeginTranslationTargetWrite(ctx, target, datastore.TranslationTargetWriteIntent{
						ID: uuid.NewString(), NextUID: uuid.NewString(), SourceDigest: "material",
					}, "")
					return err
				default:
					return store.CompleteTranslationTargetWrite(ctx, target, uuid.NewString(), nil)
				}
			}
			require.ErrorIs(t, run(t.Context()), datastore.ErrRepairRequired)
			read := store.db.Txn(false)
			defer read.Abort()
			raw, err := read.First("translation_target_fence", "id", id)
			require.NoError(t, err)
			require.Nil(t, raw.(*translationTargetFenceRow).Fence, "corruption cannot silently reset retained identity history")
		})
	}
}
