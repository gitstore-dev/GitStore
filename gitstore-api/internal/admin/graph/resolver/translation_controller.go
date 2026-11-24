// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/google/uuid"
)

func (r *Resolver) readTranslationTargetObservation(ctx context.Context, namespace, apiVersion, kind, name string) (*model.TranslationTargetObservation, error) {
	if err := r.requireTranslationController(ctx); err != nil {
		return nil, err
	}
	if namespace == "" || name == "" || len(namespace) > 2048 || len(name) > 2048 {
		return nil, translationControllerError(datastore.ErrInvalidArgument)
	}
	if _, err := translation.DescriptorFor(apiVersion, kind); err != nil {
		return nil, translationControllerError(datastore.ErrInvalidArgument)
	}
	if err := r.authorizeTranslationSource(ctx, "status.write", namespace, "", ""); err != nil {
		return nil, err
	}
	namespaces, ok := r.store.(datastore.TranslationTargetNamespaceMetadataStore)
	if !ok {
		return nil, translationReadUnavailable()
	}
	before, err := namespaces.ReadTranslationTargetNamespaceMetadata(ctx, namespace)
	if err != nil || before == nil || before.Name != namespace || before.UID == "" || !before.Active {
		return nil, translationReadUnavailable(err)
	}
	snapshot := *before
	facts, err := r.authorizeTranslationTarget(ctx, namespace, datastore.TranslationTargetKey{
		NamespaceUID: snapshot.UID, APIVersion: apiVersion, Kind: kind, Name: name,
	})
	if err != nil {
		return nil, err
	}
	after, err := namespaces.ReadTranslationTargetNamespaceMetadata(ctx, namespace)
	if err != nil || after == nil || snapshot != *after || ctx.Err() != nil {
		return nil, translationReadUnavailable(err)
	}
	if facts == nil || facts.evidence.Generation > math.MaxInt32 {
		return nil, translationReadUnavailable()
	}
	evidence := facts.evidence
	return &model.TranslationTargetObservation{NamespaceUID: snapshot.UID, Evidence: &model.TranslationTargetEvidence{
		APIVersion: evidence.APIVersion, Kind: evidence.Kind, Namespace: evidence.Namespace, Name: evidence.Name,
		UID: evidence.UID, Generation: int32(evidence.Generation), Revision: evidence.Revision,
		ResourceVersion: evidence.ResourceVersion, ContentDigest: evidence.ContentDigest,
	}}, nil
}

func (r *Resolver) requireTranslationController(ctx context.Context) error {
	if !r.translationActivation.Enabled {
		return translationNotActivated()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	subject := r.translationControllerSubject
	if subject == "" && r.rbacValidator != nil {
		subject = r.rbacValidator.ControllerSubject
	}
	authority := admission.RBACValidator{ManagedStore: r.store, ControllerSubject: subject}
	if err := authority.RequireController(ctx, auth.PrincipalFromContext(ctx)); err != nil {
		return rbacAuthorError(err)
	}
	return nil
}

func translationControllerError(err error) error {
	switch {
	case errors.Is(err, datastore.ErrConflict):
		return admission.NewError(admission.CodeConflict, "RESOURCE_VERSION_CONFLICT", "Translation controller observation changed").ToGQLError()
	case errors.Is(err, datastore.ErrInvalidArgument):
		return admission.NewError(admission.CodeBadUserInput, "INVALID_TRANSLATION_CONTROLLER_INPUT", "invalid Translation controller input").ToGQLError()
	case errors.Is(err, datastore.ErrNotFound):
		return nodeLookupError(nodeKindTranslation, err)
	default:
		return translationReadUnavailable(err)
	}
}

func (r *Resolver) translationControllerBinding(ctx context.Context, namespace, name string) (*datastore.Translation, *datastore.TranslationBinding, error) {
	if err := r.requireTranslationController(ctx); err != nil {
		return nil, nil, err
	}
	if namespace == "" || name == "" {
		return nil, nil, translationControllerError(datastore.ErrInvalidArgument)
	}
	store, err := r.translationSourceStore()
	if err != nil {
		return nil, nil, err
	}
	source, readErr := store.GetTranslationByName(ctx, namespace, name)
	if readErr != nil && !errors.Is(readErr, datastore.ErrNotFound) {
		return nil, nil, translationReadUnavailable(readErr)
	}
	owner := ""
	if source != nil {
		owner = source.EffectiveOwnerSub()
	}
	if err := r.authorizeTranslationSource(ctx, "status.write", namespace, name, owner); err != nil {
		return nil, nil, err
	}
	if readErr != nil || source == nil {
		return nil, nil, translationControllerError(datastore.ErrNotFound)
	}
	if source.Namespace != namespace || source.Name != name {
		return nil, nil, translationReadUnavailable()
	}
	capable, ok := r.store.(datastore.TranslationBindingCapable)
	if !ok || capable.TranslationBindingStore() == nil {
		return nil, nil, translationReadUnavailable()
	}
	binding, err := capable.TranslationBindingStore().GetTranslationBinding(ctx, source.UID)
	if err != nil {
		return nil, nil, translationControllerError(err)
	}
	if binding == nil || binding.TranslationUID != source.UID || len(binding.Epochs) == 0 ||
		len(binding.Epochs) > datastore.MaxTranslationEpochs ||
		binding.Epochs[len(binding.Epochs)-1].Epoch != binding.CurrentEpoch {
		return nil, nil, translationReadUnavailable()
	}
	if binding.SourceVersion != source.ResourceVersion {
		return nil, nil, translationControllerError(datastore.ErrConflict)
	}
	return source, binding, nil
}

func translationBindingModel(binding *datastore.TranslationBinding) *model.TranslationBinding {
	result := &model.TranslationBinding{
		TranslationID: mustEncodeNodeID(nodeKindTranslation, binding.TranslationUID),
		SourceVersion: binding.SourceVersion, BindingVersion: binding.ResourceVersion, ReferenceEpoch: binding.CurrentEpoch,
		NamespaceUID: binding.Epochs[len(binding.Epochs)-1].Target.NamespaceUID,
	}
	if uid := binding.Epochs[len(binding.Epochs)-1].BoundUID; uid != "" {
		result.BoundUID = &uid
	}
	return result
}

type translationTargetIdentity struct {
	owner, uid, version string
	evidence            catalog.TranslationTargetEvidence
	absent              bool
}

func (r *Resolver) authorizeTranslationTarget(ctx context.Context, namespace string, target datastore.TranslationTargetKey) (*translationTargetIdentity, error) {
	return r.authorizeTranslationTargetRead(ctx, namespace, target, func() (*translationTargetIdentity, error) {
		return r.translationTargetOwner(ctx, namespace, target)
	})
}

func (r *Resolver) authorizeTranslationTargetRead(ctx context.Context, namespace string, target datastore.TranslationTargetKey, read func() (*translationTargetIdentity, error)) (*translationTargetIdentity, error) {
	if _, err := translation.DescriptorFor(target.APIVersion, target.Kind); err != nil {
		return nil, translationControllerError(datastore.ErrInvalidArgument)
	}
	actions := map[string]string{
		"Product": auth.ActionProductRead, "ProductVariant": auth.ActionProductVariantRead,
		"CategoryTaxonomy": auth.ActionCategoryTaxonomyRead, "Collection": auth.ActionCollectionRead, "File": auth.ActionFileRead,
	}
	action := actions[target.Kind]
	resource, ok := auth.LookupAction(action)
	if !ok || r.registry == nil || r.registry.AuthZ() == nil {
		return nil, translationReadUnavailable()
	}
	facts, proofErr := read()
	owner := ""
	if facts != nil {
		owner = facts.owner
	}
	decision, err := r.registry.AuthZ().Authorize(ctx, auth.PrincipalFromContext(ctx), action,
		auth.ResourceContext{Kind: resource.Resource, Name: target.Name, OwnerSub: owner, Attrs: map[string]any{"namespace": namespace}})
	if err != nil {
		return nil, translationReadUnavailable(err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if decision.Outcome != auth.OutcomeAllow {
		return nil, admission.NewError(admission.CodeForbidden, "TRANSLATION_TARGET_PERMISSION_REQUIRED", "forbidden").ToGQLError()
	}
	if proofErr != nil {
		return nil, proofErr
	}
	return facts, nil
}

func (r *Resolver) translationTargetOwner(ctx context.Context, namespace string, target datastore.TranslationTargetKey) (*translationTargetIdentity, error) {
	fences, ok := r.store.(datastore.TranslationTargetWriteFenceStore)
	metadata, hasMetadata := r.store.(datastore.TranslationTargetMetadataStore)
	if !ok || !hasMetadata {
		return nil, translationReadUnavailable()
	}
	var observed *datastore.TranslationTargetMetadata
	evidence, err := datastore.ReadTranslationTargetEvidence(ctx, target, fences.GetTranslationTargetFence,
		func(ctx context.Context, fence *datastore.TranslationTargetFence) (*datastore.TranslationTargetMetadata, error) {
			point, err := metadata.ReadTranslationTargetMetadata(ctx, fence)
			observed = point
			return point, err
		})
	if err != nil || evidence == nil || observed == nil || evidence.Namespace != namespace {
		return nil, translationReadUnavailable(err)
	}
	return &translationTargetIdentity{owner: observed.OwnerSub, uid: evidence.UID, version: evidence.ResourceVersion, evidence: *evidence}, nil
}

func (r *Resolver) readTranslationBinding(ctx context.Context, namespace, name string) (*model.TranslationBinding, error) {
	source, binding, err := r.translationControllerBinding(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	last := binding.Epochs[len(binding.Epochs)-1]
	if source.NamespaceID != last.Target.NamespaceUID {
		return nil, translationReadUnavailable()
	}
	scope, err := r.translationValidationNamespace(ctx, namespace, source.NamespaceID)
	if err != nil {
		return nil, err
	}
	if binding.Epochs[len(binding.Epochs)-1].BoundUID != "" {
		if _, err := r.authorizeTranslationTargetRead(ctx, namespace, last.Target, func() (*translationTargetIdentity, error) {
			return r.translationValidationTargetOwner(ctx, scope, last.Target)
		}); err != nil {
			return nil, err
		}
	}
	after, err := r.translationValidationNamespace(ctx, namespace, source.NamespaceID)
	if err != nil || *scope != *after {
		return nil, translationReadUnavailable(err)
	}
	return translationBindingModel(binding), nil
}

func (r *Resolver) bindTranslationTarget(ctx context.Context, input model.BindTranslationTargetInput) (*model.TranslationBinding, error) {
	source, binding, err := r.translationControllerBinding(ctx, input.Namespace, input.Name)
	if err != nil {
		return nil, err
	}
	uid, err := decodeNodeIDAs(nodeKindTranslation, input.TranslationID)
	if err != nil || uid != source.UID || input.SourceVersion != source.ResourceVersion ||
		input.BindingVersion != binding.ResourceVersion || input.ReferenceEpoch != binding.CurrentEpoch {
		return nil, translationControllerError(datastore.ErrConflict)
	}
	targetUID, err := uuid.Parse(input.TargetUID)
	if err != nil || targetUID.String() != input.TargetUID {
		return nil, translationControllerError(datastore.ErrInvalidArgument)
	}
	last := &binding.Epochs[len(binding.Epochs)-1]
	if last.Target.NamespaceUID != source.NamespaceID {
		return nil, translationReadUnavailable()
	}
	facts, err := r.authorizeTranslationTarget(ctx, source.Namespace, last.Target)
	if err != nil {
		return nil, err
	}
	if last.BoundUID != "" {
		if last.BoundUID != input.TargetUID {
			return nil, translationControllerError(datastore.ErrConflict)
		}
		return translationBindingModel(binding), nil
	}
	targets, ok := r.store.(datastore.TranslationTargetFenceStore)
	if !ok {
		return nil, translationReadUnavailable()
	}
	evidence, err := targets.ReadTargetEvidence(ctx, last.Target)
	if err != nil {
		return nil, translationControllerError(err)
	}
	if evidence == nil || evidence.UID != input.TargetUID || facts.uid != evidence.UID ||
		facts.version != evidence.ResourceVersion ||
		(last.VersionPin != "" && last.VersionPin != evidence.ResourceVersion) {
		return nil, translationControllerError(datastore.ErrConflict)
	}
	last.BoundUID = input.TargetUID
	binding.ResourceVersion, err = datastore.AdvanceTranslationIndexVersion(binding.ResourceVersion)
	if err != nil {
		return nil, translationReadUnavailable(err)
	}
	bindings := r.store.(datastore.TranslationBindingCapable).TranslationBindingStore()
	if err := bindings.CompareAndSwapTranslationBinding(ctx, binding, input.BindingVersion); err != nil {
		return nil, translationControllerError(err)
	}
	return r.readTranslationBinding(ctx, input.Namespace, input.Name)
}

func (r *Resolver) scanTranslationDependents(ctx context.Context, input model.TranslationDependencyInput, limit *int32, continuation *string) (*model.TranslationDependentScan, error) {
	if err := r.requireTranslationController(ctx); err != nil {
		return nil, err
	}
	size := 256
	if limit != nil {
		size = int(*limit)
	}
	if size < 1 || size > 256 || input.Namespace == "" {
		return nil, translationControllerError(datastore.ErrInvalidArgument)
	}
	if err := r.authorizeTranslationSource(ctx, "status.write", input.Namespace, "", ""); err != nil {
		return nil, err
	}
	if err := r.authorizeTranslationSource(ctx, "list", input.Namespace, "", ""); err != nil {
		return nil, err
	}
	namespace, err := r.store.GetNamespaceByName(ctx, input.Namespace)
	if err != nil {
		return nil, translationControllerError(err)
	}
	if namespace == nil || namespace.Name != input.Namespace || namespace.UID != input.NamespaceUID {
		return nil, translationControllerError(datastore.ErrConflict)
	}
	capable, ok := r.store.(datastore.TranslationIndexCapable)
	if !ok || capable.TranslationIndexStore() == nil {
		return nil, translationReadUnavailable()
	}
	target := datastore.TranslationTargetKey{NamespaceUID: namespace.UID, APIVersion: input.APIVersion, Kind: input.Kind, Name: input.Name}
	if _, err := datastore.TranslationTargetFenceID(target); err != nil {
		return nil, translationControllerError(err)
	}
	if _, err := translation.DescriptorFor(target.APIVersion, target.Kind); err != nil {
		return nil, translationControllerError(datastore.ErrInvalidArgument)
	}
	cursor, targetUID := "", ""
	if continuation != nil {
		cursor = *continuation
	}
	if input.UID != nil {
		targetUID = *input.UID
	}
	page, err := capable.TranslationIndexStore().ListTranslationDependents(ctx, target, targetUID, cursor, size)
	if err != nil {
		return nil, translationControllerError(err)
	}
	result := &model.TranslationDependentScan{Items: make([]*model.TranslationDependent, 0, len(page.Items))}
	for _, entry := range page.Items {
		result.Items = append(result.Items, &model.TranslationDependent{
			TranslationID: mustEncodeNodeID(nodeKindTranslation, entry.TranslationUID),
			SourceVersion: entry.SourceVersion, ReferenceEpoch: entry.ReferenceEpoch,
		})
	}
	if page.Next != "" {
		result.Continuation = &page.Next
	}
	return result, nil
}

func translationControllerContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 30*time.Second)
}
