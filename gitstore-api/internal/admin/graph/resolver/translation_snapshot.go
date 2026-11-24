// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/gitstore-dev/gitstore/translation"
)

func (r *Resolver) readTranslationValidationSnapshot(ctx context.Context, namespace, name, sourceID, sourceVersion string) (*model.TranslationValidationSnapshot, error) {
	source, binding, err := r.translationControllerBinding(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	uid, err := decodeNodeIDAs(nodeKindTranslation, sourceID)
	if err != nil || uid != source.UID || sourceVersion != source.ResourceVersion || source.DeletionTimestamp != nil {
		return nil, translationControllerError(datastore.ErrConflict)
	}
	last := binding.Epochs[len(binding.Epochs)-1]
	if source.SourceDigest == "" || source.NamespaceID == "" || last.Target.NamespaceUID != source.NamespaceID {
		return nil, translationReadUnavailable()
	}
	scope, err := r.translationValidationNamespace(ctx, namespace, source.NamespaceID)
	if err != nil {
		return nil, err
	}
	facts, err := r.authorizeTranslationTargetRead(ctx, namespace, last.Target, func() (*translationTargetIdentity, error) {
		return r.translationValidationTargetOwner(ctx, scope, last.Target)
	})
	if err != nil {
		return nil, err
	}
	if facts == nil {
		return nil, translationReadUnavailable()
	}
	var target *model.TranslationValidationTarget
	var evidence catalog.TranslationTargetEvidence
	if !facts.absent {
		target, evidence, err = r.translationValidationObservedTarget(ctx, namespace, last.Target, last.VersionPin, facts)
		if err != nil {
			return nil, err
		}
	}
	after, afterBinding, err := r.translationControllerBinding(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	if after.UID != source.UID || after.NamespaceID != source.NamespaceID ||
		!after.CreationTimestamp.Equal(source.CreationTimestamp) || after.ResourceVersion != sourceVersion ||
		after.SourceDigest != source.SourceDigest || after.Generation != source.Generation || after.Revision != source.Revision ||
		after.DeletionTimestamp != nil || afterBinding.ResourceVersion != binding.ResourceVersion {
		return nil, translationControllerError(datastore.ErrConflict)
	}
	afterScope, err := r.translationValidationNamespace(ctx, namespace, source.NamespaceID)
	if err != nil {
		return nil, err
	}
	if *scope != *afterScope {
		return nil, translationControllerError(datastore.ErrConflict)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	observation, err := translationStoredObservation(source.Status, evidence)
	if err != nil {
		return nil, err
	}
	return &model.TranslationValidationSnapshot{
		SourceID: sourceID, SourceVersion: sourceVersion, SourceDigest: source.SourceDigest, NamespaceUID: source.NamespaceID,
		Binding:     translationBindingModel(binding),
		Observation: observation,
		Target:      target,
	}, nil
}

func (r *Resolver) translationValidationTargetOwner(ctx context.Context, scope *datastore.TranslationTargetNamespaceMetadata, target datastore.TranslationTargetKey) (*translationTargetIdentity, error) {
	fences, ok := r.store.(interface {
		GetTranslationTargetFence(context.Context, datastore.TranslationTargetKey) (*datastore.TranslationTargetFence, error)
	})
	if !ok {
		return nil, translationReadUnavailable()
	}
	head, err := fences.GetTranslationTargetFence(ctx, target)
	if (head == nil && errors.Is(err, datastore.ErrNotFound)) ||
		(err == nil && head != nil && head.Phase == datastore.TranslationTargetStable && head.Evidence == nil) {
		facts := &translationTargetIdentity{owner: scope.OwnerSub, absent: true}
		coverage, ok := r.store.(datastore.TranslationTargetCoverageReader)
		if !ok {
			return facts, translationReadUnavailable()
		}
		if err := datastore.ReadTranslationTargetAbsence(ctx, target, coverage, fences.GetTranslationTargetFence); err != nil {
			return facts, translationReadUnavailable(err)
		}
		return facts, nil
	}
	if err != nil {
		return nil, translationReadUnavailable(err)
	}
	return r.translationTargetOwner(ctx, scope.Name, target)
}

func (r *Resolver) translationValidationObservedTarget(ctx context.Context, namespace string, key datastore.TranslationTargetKey, versionPin string, facts *translationTargetIdentity) (*model.TranslationValidationTarget, catalog.TranslationTargetEvidence, error) {
	fail := func(err error) (*model.TranslationValidationTarget, catalog.TranslationTargetEvidence, error) {
		return nil, catalog.TranslationTargetEvidence{}, err
	}
	material, err := r.translationValidationMaterial(ctx, key.Kind, facts.uid)
	if err != nil {
		return fail(translationReadUnavailable(err))
	}
	if material == nil || material.deletion != nil || material.evidence.Generation > math.MaxInt32 {
		return fail(translationReadUnavailable())
	}
	digest, err := translation.TargetContentDigest(ctx, material.evidence.APIVersion, material.evidence.Kind, material.spec, material.body)
	if err != nil {
		return fail(translationReadUnavailable(err))
	}
	material.evidence.ContentDigest = digest
	same := func(left, right catalog.TranslationTargetEvidence) bool {
		if versionPin == "" {
			left.ResourceVersion, right.ResourceVersion = "", ""
		}
		return left == right
	}
	if !same(material.evidence, facts.evidence) {
		return fail(translationControllerError(datastore.ErrConflict))
	}
	fields, err := validate.TranslationTargetFields(ctx, material.spec, material.schema)
	if err != nil {
		return fail(translationReadUnavailable(err))
	}
	afterTarget, err := r.authorizeTranslationTarget(ctx, namespace, key)
	if err != nil {
		return fail(err)
	}
	if !same(afterTarget.evidence, facts.evidence) {
		return fail(translationControllerError(datastore.ErrConflict))
	}
	evidence := material.evidence
	return &model.TranslationValidationTarget{
		Namespace: namespace, Fields: fields,
		Evidence: &model.TranslationTargetEvidence{
			APIVersion: evidence.APIVersion, Kind: evidence.Kind, Namespace: evidence.Namespace, Name: evidence.Name,
			UID: evidence.UID, Generation: int32(evidence.Generation), Revision: evidence.Revision,
			ResourceVersion: evidence.ResourceVersion, ContentDigest: evidence.ContentDigest,
		},
	}, evidence, nil
}

func translationStoredObservation(raw json.RawMessage, authorized catalog.TranslationTargetEvidence) (*model.TranslationValidationObservation, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > translation.MaxDocumentBytes {
		return nil, translationReadUnavailable()
	}
	var stored struct {
		ObservedGeneration  int32                        `json:"observedGeneration"`
		LastAppliedRevision string                       `json:"lastAppliedRevision"`
		Conditions          []rawCondition               `json:"conditions"`
		Resolved            *catalog.ResolvedTranslation `json:"resolved"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, translationReadUnavailable(err)
	}
	var resolved map[string]any
	if stored.Resolved != nil {
		if target := stored.Resolved.Target; target != nil && *target != authorized {
			stored.Resolved.Target = nil
		}
		encoded, err := json.Marshal(stored.Resolved)
		if err != nil {
			return nil, translationReadUnavailable(err)
		}
		if err := json.Unmarshal(encoded, &resolved); err != nil {
			return nil, translationReadUnavailable(err)
		}
	}
	result := &model.TranslationValidationObservation{
		ObservedGeneration: stored.ObservedGeneration, LastAppliedRevision: stored.LastAppliedRevision,
		Conditions: make([]*model.Condition, 0, len(stored.Conditions)), Resolved: resolved,
	}
	for _, condition := range stored.Conditions {
		converted := conditionFromRaw(condition)
		if !converted.Status.IsValid() {
			return nil, translationReadUnavailable()
		}
		result.Conditions = append(result.Conditions, converted)
	}
	return result, nil
}

func (r *Resolver) translationValidationNamespace(ctx context.Context, name, uid string) (*datastore.TranslationTargetNamespaceMetadata, error) {
	namespaces, ok := r.store.(datastore.TranslationTargetNamespaceMetadataStore)
	if !ok {
		return nil, translationReadUnavailable()
	}
	namespace, err := namespaces.ReadTranslationTargetNamespaceMetadata(ctx, name)
	if err != nil || namespace == nil || namespace.Name != name || namespace.UID != uid || !namespace.Active {
		return nil, translationReadUnavailable(err)
	}
	return namespace, nil
}

type translationValidationMaterial struct {
	evidence catalog.TranslationTargetEvidence
	spec     json.RawMessage
	body     string
	deletion *time.Time
	schema   reflect.Type
}

func (r *Resolver) translationValidationMaterial(ctx context.Context, kind, uid string) (*translationValidationMaterial, error) {
	result := &translationValidationMaterial{}
	set := func(apiVersion, kind, namespace, name, uid, version string, generation int64, revision string, spec json.RawMessage, body string, deletion *time.Time, schema reflect.Type) {
		result.evidence = catalog.TranslationTargetEvidence{
			APIVersion: apiVersion, Kind: kind, Namespace: namespace, Name: name, UID: uid,
			ResourceVersion: version, Generation: generation, Revision: revision,
		}
		result.spec, result.body, result.deletion, result.schema = spec, body, deletion, schema
	}
	switch kind {
	case "Product":
		target, err := r.store.GetProduct(ctx, uid)
		if err != nil || target == nil {
			return nil, err
		}
		set(target.APIVersion, target.Kind, target.Namespace, target.Name, target.UID, target.ResourceVersion,
			target.Generation, target.Revision, target.Spec, target.Body, target.DeletionTimestamp, reflect.TypeFor[catalog.ProductSpec]())
	case "ProductVariant":
		target, err := r.store.GetProductVariant(ctx, uid)
		if err != nil || target == nil {
			return nil, err
		}
		set(target.APIVersion, target.Kind, target.Namespace, target.Name, target.UID, target.ResourceVersion,
			target.Generation, target.Revision, target.Spec, target.Body, target.DeletionTimestamp, reflect.TypeFor[catalog.ProductVariantSpec]())
	case "CategoryTaxonomy":
		target, err := r.store.GetCategoryTaxonomy(ctx, uid)
		if err != nil || target == nil {
			return nil, err
		}
		set(target.APIVersion, target.Kind, target.Namespace, target.Name, target.UID, target.ResourceVersion,
			target.Generation, target.Revision, target.Spec, target.Body, target.DeletionTimestamp, reflect.TypeFor[catalog.CategoryTaxonomySpec]())
	case "Collection":
		target, err := r.store.GetCollection(ctx, uid)
		if err != nil || target == nil {
			return nil, err
		}
		set(target.APIVersion, target.Kind, target.Namespace, target.Name, target.UID, target.ResourceVersion,
			target.Generation, target.Revision, target.Spec, target.Body, target.DeletionTimestamp, reflect.TypeFor[catalog.CollectionSpec]())
	case "File":
		target, err := r.store.GetFile(ctx, uid)
		if err != nil || target == nil {
			return nil, err
		}
		set(target.APIVersion, target.Kind, target.Namespace, target.Name, target.UID, target.ResourceVersion,
			target.Generation, target.Revision, target.Spec, target.Body, target.DeletionTimestamp, reflect.TypeFor[catalog.FileSpec]())
	default:
		return nil, datastore.ErrInvalidArgument
	}
	return result, nil
}
