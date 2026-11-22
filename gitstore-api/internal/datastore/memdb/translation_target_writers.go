// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/google/uuid"
	gomemdb "github.com/hashicorp/go-memdb"
)

func translationMaterialView(resource any) (*datastore.Product, *datastore.TranslationTargetSourceIdentity) {
	switch resource := resource.(type) {
	case *datastore.Product:
		return resource, &resource.TranslationTargetSource
	case *datastore.File:
		return (*datastore.Product)(resource), &resource.TranslationTargetSource
	case *datastore.Collection:
		return (*datastore.Product)(resource), &resource.TranslationTargetSource
	case *datastore.CategoryTaxonomy:
		return &datastore.Product{
			UID: resource.UID, Namespace: resource.Namespace, Name: resource.Name,
			APIVersion: resource.APIVersion, Kind: resource.Kind, Generation: resource.Generation,
			ResourceVersion: resource.ResourceVersion, CreationTimestamp: resource.CreationTimestamp,
			CreationActor: resource.CreationActor, Revision: resource.Revision,
			Labels: resource.Labels, Annotations: resource.Annotations,
			OwnerReferences: resource.OwnerReferences, Finalizers: resource.Finalizers, DeletionTimestamp: resource.DeletionTimestamp,
			RepositoryID: resource.RepositoryID, SourcePath: resource.SourcePath, GitCommitSHA: resource.GitCommitSHA, GitRef: resource.GitRef,
			Spec: resource.Spec, Body: resource.Body,
		}, &resource.TranslationTargetSource
	case *datastore.ProductVariant:
		return &datastore.Product{
			UID: resource.UID, Namespace: resource.Namespace, Name: resource.Name,
			APIVersion: resource.APIVersion, Kind: resource.Kind, Generation: resource.Generation,
			ResourceVersion: resource.ResourceVersion, CreationTimestamp: resource.CreationTimestamp,
			CreationActor: resource.CreationActor, Revision: resource.Revision,
			Labels: resource.Labels, Annotations: resource.Annotations,
			OwnerReferences: resource.OwnerReferences, Finalizers: resource.Finalizers, DeletionTimestamp: resource.DeletionTimestamp,
			RepositoryID: resource.RepositoryID, SourcePath: resource.SourcePath, GitCommitSHA: resource.GitCommitSHA, GitRef: resource.GitRef,
			Spec: resource.Spec, Body: resource.Body,
		}, &resource.TranslationTargetSource
	default:
		return nil, nil
	}
}

func sameTranslationMaterial(before, after *datastore.Product) bool {
	return before.UID == after.UID && before.Namespace == after.Namespace && before.Name == after.Name &&
		before.APIVersion == after.APIVersion && before.Kind == after.Kind &&
		before.Generation == after.Generation && before.Revision == after.Revision &&
		before.CreationTimestamp.Equal(after.CreationTimestamp) && before.CreationActor == after.CreationActor &&
		before.RepositoryID == after.RepositoryID && before.SourcePath == after.SourcePath &&
		before.GitCommitSHA == after.GitCommitSHA && before.GitRef == after.GitRef &&
		maps.Equal(before.Labels, after.Labels) && maps.Equal(before.Annotations, after.Annotations) &&
		bytes.Equal(before.OwnerReferences, after.OwnerReferences) && slices.Equal(before.Finalizers, after.Finalizers) &&
		((before.DeletionTimestamp == nil && after.DeletionTimestamp == nil) ||
			(before.DeletionTimestamp != nil && after.DeletionTimestamp != nil && before.DeletionTimestamp.Equal(*after.DeletionTimestamp))) &&
		bytes.Equal(before.Spec, after.Spec) && before.Body == after.Body
}

type translationTargetMutationClock struct {
	Namespace     string
	Epoch         string
	CoverageEpoch string
}

func advanceTranslationTargetMutationClock(txn *gomemdb.Txn, namespace string, unproved bool) error {
	if namespace == "" {
		return nil
	}
	epoch := uuid.NewString()
	coverage := epoch
	raw, err := txn.First("translation_target_clock", "id", namespace)
	if err != nil {
		return err
	}
	if !unproved {
		coverage = uuid.Nil.String()
		if raw != nil {
			coverage = raw.(*translationTargetMutationClock).CoverageEpoch
		}
	}
	return txn.Insert("translation_target_clock", &translationTargetMutationClock{
		Namespace: namespace, Epoch: epoch, CoverageEpoch: coverage,
	})
}

// Source evidence and its head are committed in the same memdb transaction.
// Legacy envelopes and unsupported transitions lose proof rather than inheriting
// a caller-supplied token. They cannot establish positive target evidence.
func insertTranslationMaterialTarget(ctx context.Context, txn *gomemdb.Txn, table string, resource any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	after, proof := translationMaterialView(resource)
	if after == nil {
		return datastore.ErrInvalidArgument
	}
	*proof = datastore.TranslationTargetSourceIdentity{}
	kind := ""
	switch table {
	case "product":
		kind = "Product"
	case "file":
		kind = "File"
	case "collection":
		kind = "Collection"
	case "category_taxonomy":
		kind = "CategoryTaxonomy"
	case "product_variant":
		kind = "ProductVariant"
	default:
		return datastore.ErrInvalidArgument
	}
	old, err := txn.First(table, "id", after.UID)
	if err != nil {
		return err
	}
	var before *datastore.Product
	var previousProof *datastore.TranslationTargetSourceIdentity
	if old != nil {
		before, previousProof = translationMaterialView(old)
	} else {
		namespace, err := txn.First("namespaces", "name", after.Namespace)
		if err != nil {
			return err
		}
		if namespace != nil {
			proof.NamespaceUID = namespace.(*datastore.Namespace).UID
		}
	}
	insert := func() error {
		unproved := proof.NamespaceUID == "" || proof.WriteToken == "" || proof.ContentDigest == ""
		if !unproved {
			namespace, err := txn.First("namespaces", "name", after.Namespace)
			if err != nil {
				return err
			}
			unproved = namespace == nil || namespace.(*datastore.Namespace).UID != proof.NamespaceUID
		}
		if err := advanceTranslationTargetMutationClock(txn, after.Namespace, unproved); err != nil {
			return err
		}
		return txn.Insert(table, resource)
	}
	insertUnproved := func() error {
		if previousProof != nil && previousProof.WriteToken != "" {
			if err := retireTranslationTargetName(txn, kind, before, previousProof); err != nil {
				return err
			}
		}
		return insert()
	}
	if before != nil {
		if sameTranslationMaterial(before, after) {
			*proof = *previousProof
			return insert()
		}
		proof.NamespaceUID = previousProof.NamespaceUID
		if before.Namespace != after.Namespace || before.Name != after.Name {
			if err := retireTranslationTargetName(txn, kind, before, previousProof); err != nil {
				return err
			}
			return insert()
		}
		if previousProof.WriteToken == "" || before.ResourceVersion == after.ResourceVersion {
			return insertUnproved()
		}
	}
	if after.CreationTimestamp.IsZero() || after.Kind != kind {
		return insertUnproved()
	}
	namespace, err := txn.First("namespaces", "name", after.Namespace)
	if err != nil {
		return err
	}
	if namespace == nil {
		return insertUnproved()
	}
	target := datastore.TranslationTargetKey{
		NamespaceUID: namespace.(*datastore.Namespace).UID,
		APIVersion:   after.APIVersion, Kind: after.Kind, Name: after.Name,
	}
	proof.NamespaceUID = target.NamespaceUID
	if previousProof != nil && previousProof.NamespaceUID != target.NamespaceUID {
		return datastore.ErrRepairRequired
	}
	id, err := datastore.TranslationTargetFenceID(target)
	if err != nil {
		return insertUnproved()
	}
	digest, err := translation.TargetContentDigest(ctx, after.APIVersion, after.Kind, after.Spec, after.Body)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return insertUnproved()
	}
	raw, err := txn.First("translation_target_fence", "id", id)
	if err != nil {
		return err
	}
	var current *datastore.TranslationTargetFence
	expected := ""
	if raw != nil {
		current = raw.(*translationTargetFenceRow).Fence
		if current == nil {
			return datastore.ErrRepairRequired
		}
		expected = current.Token
	}
	if previousProof != nil && previousProof.WriteToken != "" {
		if current == nil {
			return datastore.ErrRepairRequired
		}
		if current.Phase == datastore.TranslationTargetStable && current.Token != previousProof.WriteToken {
			return datastore.ErrConflict
		}
	}
	intent := datastore.TranslationTargetWriteIntent{
		ID: uuid.NewString(), NextUID: after.UID, SourceDigest: digest, CreationTimestamp: after.CreationTimestamp,
	}
	if before != nil {
		intent.PreviousUID, intent.ExpectedResourceVersion = before.UID, before.ResourceVersion
	}
	pending, err := datastore.PlanTranslationTargetWrite(current, target, intent, expected)
	if errors.Is(err, datastore.ErrInvalidArgument) {
		return insertUnproved()
	}
	if err != nil {
		return err
	}
	evidence := &catalog.TranslationTargetEvidence{
		APIVersion: after.APIVersion, Kind: after.Kind, Namespace: after.Namespace, Name: after.Name,
		UID: after.UID, Generation: after.Generation, Revision: after.Revision,
		ResourceVersion: after.ResourceVersion, ContentDigest: digest,
	}
	stable, err := datastore.PlanTranslationTargetCompletion(pending, pending.Token, evidence)
	if errors.Is(err, datastore.ErrInvalidArgument) {
		return insertUnproved()
	}
	if err != nil {
		return err
	}
	*proof = datastore.TranslationTargetSourceIdentity{
		NamespaceUID: target.NamespaceUID, WriteToken: stable.Token, ContentDigest: digest,
	}
	if err := txn.Insert("translation_target_fence", &translationTargetFenceRow{ID: id, Fence: stable}); err != nil {
		return err
	}
	return insert()
}

type deletedTranslationTargetUID struct {
	ID string
}

func checkTranslationTargetUID(txn *gomemdb.Txn, kind, uid string) error {
	raw, err := txn.First("deleted_target_uid", "id", kind+"/"+uid)
	if err != nil {
		return err
	}
	if raw != nil {
		return datastore.ErrAlreadyExists
	}
	return nil
}

func deleteTranslationTarget(txn *gomemdb.Txn, kind, table, uid string, resource any) error {
	source, proof := translationMaterialView(resource)
	if source != nil {
		if err := advanceTranslationTargetMutationClock(txn, source.Namespace, false); err != nil {
			return err
		}
		if err := retireTranslationTargetName(txn, kind, source, proof); err != nil {
			return err
		}
	}
	if err := txn.Insert("deleted_target_uid", &deletedTranslationTargetUID{ID: kind + "/" + uid}); err != nil {
		return err
	}
	return txn.Delete(table, resource)
}

func retireTranslationTargetName(txn *gomemdb.Txn, kind string, source *datastore.Product, proof *datastore.TranslationTargetSourceIdentity) error {
	namespaceUID := proof.NamespaceUID
	if namespaceUID == "" {
		namespace, err := txn.First("namespaces", "name", source.Namespace)
		if err != nil {
			return err
		}
		if namespace == nil {
			return nil
		}
		namespaceUID = namespace.(*datastore.Namespace).UID
	}
	version := "catalog.gitstore.dev/v1beta1"
	if kind == "File" {
		version = "storage.gitstore.dev/v1beta1"
	}
	target := datastore.TranslationTargetKey{
		NamespaceUID: namespaceUID, APIVersion: version, Kind: kind, Name: source.Name,
	}
	id, err := datastore.TranslationTargetFenceID(target)
	if err != nil {
		return nil
	}
	raw, err := txn.First("translation_target_fence", "id", id)
	if err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	current := raw.(*translationTargetFenceRow).Fence
	if current == nil || datastore.ValidateTranslationTargetFence(current) != nil {
		return datastore.ErrRepairRequired
	}
	if current.Phase != datastore.TranslationTargetStable {
		return datastore.ErrConflict
	}
	if proof.WriteToken != "" && current.Token != proof.WriteToken {
		return datastore.ErrConflict
	}
	if current.Evidence == nil {
		return nil
	}
	if current.Evidence.UID != source.UID {
		return datastore.ErrRepairRequired
	}
	// Deletion or name movement and this vacancy become visible atomically;
	// no externally observable UPDATING phase needs replay.
	return txn.Insert("translation_target_fence", &translationTargetFenceRow{
		ID: id, Fence: &datastore.TranslationTargetFence{
			Target: target, Token: uuid.NewString(), Phase: datastore.TranslationTargetStable,
		},
	})
}
