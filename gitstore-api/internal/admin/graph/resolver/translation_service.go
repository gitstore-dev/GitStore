// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func translationNotActivated() error {
	return admission.NewError(admission.CodeFailedPrecondition, "TRANSLATION_NOT_ACTIVATED",
		"Translation operations are not activated").ToGQLError()
}

func translationReadUnavailable(cause ...error) error {
	result := &gqlerror.Error{
		Message:    "Translation read unavailable",
		Extensions: map[string]any{"code": "TRANSLATION_UNAVAILABLE", "retryable": true},
	}
	if len(cause) > 0 {
		result.Err = cause[0]
	}
	return result
}

func (r *Resolver) guardTranslationStatus(ctx context.Context, source *datastore.Translation) (current *datastore.Translation, observed *catalog.TranslationStatus, err error) {
	started := time.Now()
	defer func() {
		var items []guardedTranslation
		if err == nil && current != nil {
			items = []guardedTranslation{{current, observed}}
		}
		r.translationMetrics.observeGuard(ctx, "single", started, items, err)
	}()
	if source == nil {
		return nil, nil, translationReadUnavailable()
	}
	if err := r.authorizeTranslationSource(ctx, "read", source.Namespace, source.Name, source.EffectiveOwnerSub()); err != nil {
		return nil, nil, err
	}
	store, err := r.translationSourceStore()
	if err != nil {
		return nil, nil, err
	}
	namespace, name := source.Namespace, source.Name
	return guardTranslationObservation(ctx, source,
		func(ctx context.Context) (*datastore.Translation, error) {
			after, err := store.GetTranslationByName(ctx, namespace, name)
			if err != nil || after == nil {
				return nil, err
			}
			if err := r.authorizeTranslationSource(ctx, "read", after.Namespace, after.Name, after.EffectiveOwnerSub()); err != nil {
				return nil, err
			}
			return after, nil
		},
		func(ctx context.Context, target datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error) {
			return r.readTranslationGuardTarget(ctx, namespace, target)
		})
}

func (r *Resolver) readTranslationGuardTarget(ctx context.Context, namespace string, target datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error) {
	facts, err := r.authorizeTranslationTargetRead(ctx, namespace, target, func() (*translationTargetIdentity, error) {
		facts, err := r.translationTargetOwner(ctx, namespace, target)
		if err == nil {
			return facts, nil
		}
		scope, err := r.translationValidationNamespace(ctx, namespace, target.NamespaceUID)
		if err != nil {
			return nil, err
		}
		return r.translationValidationTargetOwner(ctx, scope, target)
	})
	if err != nil {
		return nil, err
	}
	return &facts.evidence, nil
}

func (r *Resolver) guardTranslationRead(ctx context.Context, source *datastore.Translation) (*datastore.Translation, *catalog.TranslationStatus, error) {
	current, status, err := r.guardTranslationStatus(ctx, source)
	if err != nil {
		return nil, nil, err
	}
	if err := r.requestGuardRevalidation(ctx, guardedTranslation{current, status}); err != nil {
		return nil, nil, err
	}
	return current, status, nil
}

func (r *Resolver) requestGuardRevalidation(ctx context.Context, item guardedTranslation) error {
	if item.status == nil {
		return translationReadUnavailable()
	}
	for _, condition := range item.status.Conditions {
		if condition.Type != catalog.ConditionReady || condition.Status == catalog.ConditionTrue {
			continue
		}
		switch condition.Reason {
		case "StaleEvidence", "Unavailable", "Forbidden":
			store, err := r.translationSourceStore()
			if err != nil {
				return err
			}
			_, err = store.RequestTranslationRevalidation(ctx, item.source)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil && !errors.Is(err, datastore.ErrConflict) && !errors.Is(err, datastore.ErrNotFound) {
				return translationReadUnavailable(err)
			}
			return nil
		}
	}
	return nil
}

func (r *Resolver) guardTranslationStatusPage(ctx context.Context, sources []*datastore.Translation) (result []guardedTranslation, err error) {
	started := time.Now()
	defer func() {
		r.translationMetrics.observeGuard(ctx, "page", started, result, err)
	}()
	store, err := r.translationSourceStore()
	if err != nil {
		return nil, err
	}
	if len(sources) > datastore.MaxTranslationPageSize {
		return nil, datastore.ErrInvalidArgument
	}
	for _, source := range sources {
		if source == nil {
			return nil, translationReadUnavailable()
		}
		if err := r.authorizeTranslationSource(ctx, "read", source.Namespace, source.Name, source.EffectiveOwnerSub()); err != nil {
			return nil, err
		}
	}
	return guardTranslationPage(ctx, sources,
		func(ctx context.Context, source *datastore.Translation) (*datastore.Translation, error) {
			after, err := store.GetTranslationByName(ctx, source.Namespace, source.Name)
			if err != nil || after == nil {
				return nil, err
			}
			if err := r.authorizeTranslationSource(ctx, "read", after.Namespace, after.Name, after.EffectiveOwnerSub()); err != nil {
				return nil, err
			}
			return after, nil
		},
		func(ctx context.Context, namespace string, target datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error) {
			return r.readTranslationGuardTarget(ctx, namespace, target)
		}, r.requestGuardRevalidation)
}

const translationGuardPageWorkers = 8

type translationGuardAttemptKey struct{}

type guardedTranslation struct {
	source *datastore.Translation
	status *catalog.TranslationStatus
}

type translationPageTarget struct {
	done     chan struct{}
	evidence *catalog.TranslationTargetEvidence
	err      error
}

// All initial sources must have been fetched before entering this page scope.
// Retry observations bypass it: a newer source can postdate the shared proof.
func guardTranslationPage(
	ctx context.Context,
	sources []*datastore.Translation,
	readSource func(context.Context, *datastore.Translation) (*datastore.Translation, error),
	readTarget func(context.Context, string, datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error),
	finish func(context.Context, guardedTranslation) error,
) ([]guardedTranslation, error) {
	if len(sources) > datastore.MaxTranslationPageSize || readSource == nil || readTarget == nil {
		return nil, datastore.ErrInvalidArgument
	}
	for _, source := range sources {
		if source == nil {
			return nil, translationReadUnavailable()
		}
	}
	type targetKey struct {
		namespace string
		target    datastore.TranslationTargetKey
	}
	var mu sync.Mutex
	targets := make(map[targetKey]*translationPageTarget)
	readShared := func(ctx context.Context, namespace string, target datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error) {
		if attempt, _ := ctx.Value(translationGuardAttemptKey{}).(int); attempt > 0 {
			return readTarget(ctx, namespace, target)
		}
		key := targetKey{namespace, target}
		mu.Lock()
		if prior := targets[key]; prior != nil {
			mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-prior.done:
				return prior.evidence, prior.err
			}
		}
		pending := &translationPageTarget{done: make(chan struct{})}
		targets[key] = pending
		mu.Unlock()
		pending.evidence, pending.err = readTarget(ctx, namespace, target)
		close(pending.done)
		return pending.evidence, pending.err
	}
	result := make([]guardedTranslation, len(sources))
	errs := make([]error, len(sources))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(translationGuardPageWorkers, len(sources)) {
		workers.Go(func() {
			for i := range jobs {
				source := sources[i]
				result[i].source, result[i].status, errs[i] = guardTranslationObservation(ctx, source,
					func(ctx context.Context) (*datastore.Translation, error) { return readSource(ctx, source) },
					func(ctx context.Context, target datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error) {
						return readShared(ctx, source.Namespace, target)
					})
				if errs[i] == nil && finish != nil {
					errs[i] = finish(ctx, result[i])
				}
			}
		})
	}
dispatch:
	for i := range sources {
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- i:
		}
	}
	close(jobs)
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return result, nil
}

// readSource must reauthorize any changed source; readTarget must authorize the
// serialized fence/metadata/fence observation and never return target content.
func guardTranslationObservation(
	ctx context.Context,
	source *datastore.Translation,
	readSource func(context.Context) (*datastore.Translation, error),
	readTarget func(context.Context, datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error),
) (*datastore.Translation, *catalog.TranslationStatus, error) {
	if source == nil || readSource == nil || readTarget == nil {
		return nil, nil, translationReadUnavailable()
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		snapshot := *source
		snapshot.Spec = bytes.Clone(source.Spec)
		snapshot.Status = bytes.Clone(source.Status)
		source = &snapshot
		var observed catalog.TranslationStatus
		if len(source.Status) == 0 || json.Unmarshal(source.Status, &observed) != nil {
			return source, maskTranslationObservation(&observed, catalog.ConditionUnknown, "Unavailable"), nil
		}
		ready, readyCount, conditionMatches := false, 0, true
		for _, condition := range observed.Conditions {
			switch condition.Status {
			case catalog.ConditionTrue, catalog.ConditionFalse, catalog.ConditionUnknown:
			default:
				return source, maskTranslationObservation(&observed, catalog.ConditionUnknown, "Unavailable"), nil
			}
			if condition.Type == catalog.ConditionReady {
				readyCount++
				if condition.Status == catalog.ConditionTrue {
					ready = true
					conditionMatches = conditionMatches && condition.ObservedGeneration == source.Generation
				}
			}
		}
		if readyCount > 1 {
			return source, maskTranslationObservation(&observed, catalog.ConditionUnknown, "Unavailable"), nil
		}
		var spec translation.Spec
		if json.Unmarshal(source.Spec, &spec) != nil {
			return source, maskTranslationObservation(&observed, catalog.ConditionUnknown, "Unavailable"), nil
		}
		descriptor, err := translation.DescriptorFor(spec.TargetRef.APIVersion, spec.TargetRef.Kind)
		resolved := observed.Resolved
		if err != nil || source.DeletionTimestamp != nil ||
			(spec.TargetRef.Namespace != "" && spec.TargetRef.Namespace != source.Namespace) {
			return source, maskTranslationObservation(&observed, catalog.ConditionFalse, "StaleEvidence"), nil
		}
		if ready && (!conditionMatches ||
			observed.ObservedGeneration != source.Generation || observed.LastAppliedRevision != source.Revision ||
			resolved == nil || resolved.Target == nil || source.SourceDigest == "" ||
			resolved.SourceDigest != source.SourceDigest || resolved.DescriptorDigest != descriptor.Digest) {
			return source, maskTranslationObservation(&observed, catalog.ConditionFalse, "StaleEvidence"), nil
		}
		ref := spec.TargetRef
		if source.UID == "" || source.NamespaceID == "" || source.ResourceVersion == "" {
			return source, maskTranslationObservation(&observed, catalog.ConditionFalse, "StaleEvidence"), nil
		}
		var recorded catalog.TranslationTargetEvidence
		if ready {
			recorded = *resolved.Target
			if recorded.APIVersion != ref.APIVersion || recorded.Kind != ref.Kind || recorded.Name != ref.Name ||
				recorded.Namespace != source.Namespace || recorded.UID == "" || recorded.Generation < 1 || recorded.ContentDigest == "" ||
				(ref.UID != "" && ref.UID != recorded.UID) ||
				(ref.ResourceVersion != "" && ref.ResourceVersion != recorded.ResourceVersion) {
				return source, maskTranslationObservation(&observed, catalog.ConditionFalse, "StaleEvidence"), nil
			}
		}
		current, err := readTarget(context.WithValue(ctx, translationGuardAttemptKey{}, attempt), datastore.TranslationTargetKey{
			NamespaceUID: source.NamespaceID, APIVersion: ref.APIVersion, Kind: ref.Kind, Name: ref.Name,
		})
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if err != nil || current == nil {
			reason := "Unavailable"
			if translationGuardForbidden(err) {
				reason = "Forbidden"
			}
			return source, maskTranslationObservation(&observed, catalog.ConditionUnknown, reason), nil
		}
		if !ready {
			observed.Resolved = nil
			return source, &observed, nil
		}
		if current.APIVersion != recorded.APIVersion || current.Kind != recorded.Kind ||
			current.Namespace != recorded.Namespace || current.Name != recorded.Name || current.UID != recorded.UID ||
			current.Generation != recorded.Generation || current.ContentDigest != recorded.ContentDigest ||
			(ref.ResourceVersion != "" && current.ResourceVersion != ref.ResourceVersion) {
			return source, maskTranslationObservation(&observed, catalog.ConditionFalse, "StaleEvidence"), nil
		}
		after, err := readSource(ctx)
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if translationGuardForbidden(err) {
			return nil, nil, err
		}
		if err != nil || after == nil || after.UID != source.UID || after.NamespaceID != source.NamespaceID ||
			after.Namespace != source.Namespace || after.Name != source.Name ||
			!after.CreationTimestamp.Equal(source.CreationTimestamp) {
			return source, maskTranslationObservation(&observed, catalog.ConditionUnknown, "Unavailable"), nil
		}
		if after.ResourceVersion == source.ResourceVersion {
			if after.Generation != source.Generation || after.Revision != source.Revision ||
				after.SourceDigest != source.SourceDigest || after.DeletionTimestamp != nil ||
				!bytes.Equal(after.Status, source.Status) || !bytes.Equal(after.Spec, source.Spec) {
				return source, maskTranslationObservation(&observed, catalog.ConditionUnknown, "Unavailable"), nil
			}
			return source, &observed, nil
		}
		if attempt == 1 {
			return source, maskTranslationObservation(&observed, catalog.ConditionUnknown, "Unavailable"), nil
		}
		source = after
	}
	return nil, nil, translationReadUnavailable()
}

func translationGuardForbidden(err error) bool {
	var graphError *gqlerror.Error
	return errors.As(err, &graphError) && graphError.Extensions["code"] == string(admission.CodeForbidden)
}

func maskTranslationObservation(observed *catalog.TranslationStatus, value, reason string) *catalog.TranslationStatus {
	observed.Resolved = nil
	// A masked read is not a persisted condition transition or a freshness lease.
	ready := catalog.Condition{Type: catalog.ConditionReady}
	for _, condition := range observed.Conditions {
		if condition.Type == catalog.ConditionReady {
			ready = condition
			break
		}
	}
	ready.Status, ready.Reason = value, reason
	ready.Message = "Current validation evidence could not be established"
	observed.Conditions = []catalog.Condition{ready}
	return observed
}

func (r *Resolver) translationSourceStore() (datastore.TranslationStore, error) {
	capable, ok := r.store.(datastore.TranslationCapable)
	if !ok || capable.TranslationStore() == nil {
		return nil, translationReadUnavailable()
	}
	return capable.TranslationStore(), nil
}

func (r *Resolver) authorizeTranslationSource(ctx context.Context, verb, namespace, name, owner string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.registry == nil || r.registry.AuthZ() == nil {
		return translationReadUnavailable()
	}
	principal := auth.PrincipalFromContext(ctx)
	if principal == nil {
		principal = auth.Anonymous()
	}
	decision, err := r.registry.AuthZ().Authorize(ctx, principal, "translation."+verb,
		auth.ResourceContext{Kind: "translation", Name: name, OwnerSub: owner, Attrs: map[string]any{"namespace": namespace}})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return translationReadUnavailable(err)
	}
	if decision.Outcome != auth.OutcomeAllow {
		return admission.NewError(admission.CodeForbidden, "TRANSLATION_PERMISSION_REQUIRED", "forbidden").ToGQLError()
	}
	return nil
}

func (r *Resolver) readTranslation(ctx context.Context, by model.TranslationBy) (*model.Translation, error) {
	if (by.ID == nil) == (by.NamespacePath == nil) ||
		(by.NamespacePath != nil && (by.NamespacePath.Namespace == "" || by.NamespacePath.Name == "")) {
		return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_SELECTOR", "TranslationBy: exactly one selector required").ToGQLError()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	uid, namespace, name := "", "", ""
	var err error
	if by.ID != nil {
		uid, err = decodeNodeIDAs(nodeKindTranslation, *by.ID)
		if err != nil {
			return nil, err
		}
	} else {
		namespace, name = by.NamespacePath.Namespace, by.NamespacePath.Name
	}
	source, authorized := security.AuthorizedTranslationRead(ctx)
	if !authorized && by.ID != nil {
		source, authorized = security.AuthorizedTranslationNode(ctx, uid)
	}
	if !authorized {
		store, storeErr := r.translationSourceStore()
		if storeErr != nil {
			return nil, storeErr
		}
		if by.ID != nil {
			source, err = store.GetTranslation(ctx, uid)
		} else {
			source, err = store.GetTranslationByName(ctx, namespace, name)
		}
		if err != nil && !errors.Is(err, datastore.ErrNotFound) {
			return nil, translationReadUnavailable(err)
		}
		if errors.Is(err, datastore.ErrNotFound) {
			source = nil
		}
	}
	owner := ""
	if source != nil {
		if (by.ID != nil && source.UID != uid) ||
			(by.NamespacePath != nil && (source.Namespace != namespace || source.Name != name)) {
			return nil, translationReadUnavailable()
		}
		namespace, name, owner = source.Namespace, source.Name, source.EffectiveOwnerSub()
	}
	if !authorized {
		if err := r.authorizeTranslationSource(ctx, "read", namespace, name, owner); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil {
		return nil, nodeLookupError(nodeKindTranslation, datastore.ErrNotFound)
	}
	current, status, err := r.guardTranslationRead(ctx, source)
	if err != nil {
		return nil, err
	}
	return translationGuardedToGraphQL(current, status)
}

func (r *Resolver) readTranslations(ctx context.Context, namespace string, params datastore.PageParams) (*model.TranslationConnection, error) {
	if namespace == "" {
		return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_NAMESPACE", "namespace is required").ToGQLError()
	}
	params, err := params.BoundedTranslationPage()
	if err != nil {
		return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_PAGINATION", "invalid Translation pagination").ToGQLError()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store, err := r.translationSourceStore()
	if err != nil {
		return nil, err
	}
	parent, err := r.store.GetNamespaceByName(ctx, namespace)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return nil, translationReadUnavailable(err)
	}
	owner := ""
	if parent != nil {
		if parent.Name != namespace {
			return nil, translationReadUnavailable()
		}
		owner = parent.EffectiveOwnerSub()
	}
	if err := r.authorizeTranslationSource(ctx, "list", namespace, "", owner); err != nil {
		return nil, err
	}
	if parent == nil {
		return nil, nodeLookupError(nodeKindNamespace, datastore.ErrNotFound)
	}
	page, err := store.ListTranslations(ctx, namespace, params)
	if errors.Is(err, datastore.ErrInvalidArgument) {
		return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_CURSOR", "invalid Translation cursor").ToGQLError()
	}
	if err != nil || page == nil || len(page.Items) > params.Limit() ||
		(page.HasNext && page.NextCursor == "") || (page.HasPrevious && page.PreviousCursor == "") {
		return nil, translationReadUnavailable(err)
	}
	result := &model.TranslationConnection{
		Edges:    make([]*model.TranslationEdge, 0, len(page.Items)),
		PageInfo: &model.PageInfo{HasNextPage: page.HasNext, HasPreviousPage: page.HasPrevious},
	}
	for _, source := range page.Items {
		if source == nil || source.Namespace != namespace || source.NamespaceID != parent.UID || page.ItemCursors[source.UID] == "" {
			return nil, translationReadUnavailable()
		}
	}
	guarded, err := r.guardTranslationStatusPage(ctx, page.Items)
	if err != nil {
		return nil, err
	}
	for _, item := range guarded {
		node, err := translationGuardedToGraphQL(item.source, item.status)
		if err != nil {
			return nil, err
		}
		result.Edges = append(result.Edges, &model.TranslationEdge{Node: node, Cursor: page.ItemCursors[item.source.UID]})
	}
	if page.PreviousCursor != "" {
		result.PageInfo.StartCursor = &page.PreviousCursor
	}
	if page.NextCursor != "" {
		result.PageInfo.EndCursor = &page.NextCursor
	}
	if len(result.Edges) > 0 {
		if result.PageInfo.StartCursor == nil {
			result.PageInfo.StartCursor = &result.Edges[0].Cursor
		}
		if result.PageInfo.EndCursor == nil {
			result.PageInfo.EndCursor = &result.Edges[len(result.Edges)-1].Cursor
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func translationSourceToGraphQL(source *datastore.Translation) (*model.Translation, error) {
	if source == nil || source.Kind != nodeKindTranslation || source.APIVersion != "i18n.gitstore.dev/v1beta1" ||
		source.Namespace == "" || source.Name == "" || source.Generation < 1 || source.Generation > math.MaxInt32 {
		return nil, translationReadUnavailable()
	}
	id, err := EncodeNodeID(nodeKindTranslation, source.UID)
	if err != nil {
		return nil, translationReadUnavailable()
	}
	spec := &model.TranslationSpec{}
	if json.Unmarshal(source.Spec, spec) != nil || spec.TargetRef == nil || spec.Locale == "" {
		return nil, translationReadUnavailable()
	}
	if spec.Patches == nil {
		spec.Patches = []*model.TranslationPatch{}
	}
	owners, err := ownerRefsFromJSONStrict(source.OwnerReferences)
	if err != nil {
		return nil, translationReadUnavailable()
	}
	metadata := &model.ObjectMeta{
		Name: source.Name, Namespace: stringPointer(source.Namespace), UID: id,
		Labels: stringMapToJSONMap(source.Labels), Annotations: stringMapToJSONMap(source.Annotations),
		ResourceVersion: source.ResourceVersion, Generation: int32(source.Generation),
		CreationTimestamp: source.CreationTimestamp, Owner: resourceOwnerFromSub(source.EffectiveOwnerSub()),
		OwnerReferences: owners, Finalizers: append([]string{}, source.Finalizers...), DeletionTimestamp: source.DeletionTimestamp,
	}
	if source.Revision != "" {
		metadata.Revision = &source.Revision
	}
	return &model.Translation{ID: id, APIVersion: source.APIVersion, Kind: source.Kind, Metadata: metadata, Spec: spec, Body: &source.Body}, nil
}

func translationGuardedToGraphQL(source *datastore.Translation, observed *catalog.TranslationStatus) (*model.Translation, error) {
	value, err := translationSourceToGraphQL(source)
	if err != nil {
		return nil, err
	}
	if observed == nil || observed.ObservedGeneration < 0 || observed.ObservedGeneration > math.MaxInt32 {
		return nil, translationReadUnavailable()
	}
	status := &model.TranslationStatus{
		ObservedGeneration: int32(observed.ObservedGeneration), LastAppliedRevision: observed.LastAppliedRevision,
		Conditions: make([]*model.Condition, 0, len(observed.Conditions)),
	}
	for _, condition := range observed.Conditions {
		if condition.ObservedGeneration < 0 || condition.ObservedGeneration > math.MaxInt32 {
			return nil, translationReadUnavailable()
		}
		converted := conditionFromRaw(rawCondition{
			Type: condition.Type, Status: condition.Status, ObservedGeneration: int32(condition.ObservedGeneration),
			LastTransitionTime: condition.LastTransitionTime, Reason: condition.Reason, Message: condition.Message,
		})
		if !converted.Status.IsValid() {
			return nil, translationReadUnavailable()
		}
		status.Conditions = append(status.Conditions, converted)
	}
	if resolved := observed.Resolved; resolved != nil {
		status.Resolved = &model.ResolvedTranslation{ValidatedFieldPaths: append([]string{}, resolved.ValidatedFieldPaths...)}
		if resolved.CanonicalLocale != "" {
			status.Resolved.CanonicalLocale = &resolved.CanonicalLocale
		}
		if target := resolved.Target; target != nil {
			if target.Generation < 1 || target.Generation > math.MaxInt32 {
				return nil, translationReadUnavailable()
			}
			status.Resolved.Target = &model.TranslationTargetEvidence{
				APIVersion: target.APIVersion, Kind: target.Kind, Namespace: target.Namespace, Name: target.Name,
				UID: target.UID, Generation: int32(target.Generation), Revision: target.Revision,
				ResourceVersion: target.ResourceVersion, ContentDigest: target.ContentDigest,
			}
		}
	}
	value.Status = status
	return value, nil
}

func translationToJSONMap(value *model.Translation) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, translationReadUnavailable(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, translationReadUnavailable(err)
	}
	return object, nil
}

func (r *Resolver) writeTranslationStatus(ctx context.Context, input model.UpdateResourceStatusInput) (*model.Translation, error) {
	if err := r.requireTranslationController(ctx); err != nil {
		return nil, err
	}
	if input.Namespace == "" || input.Name == "" || input.UID == nil || *input.UID == "" || input.ResourceVersion == "" || input.Finalizers != nil {
		return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_STATUS_INPUT", "invalid Translation status input").ToGQLError()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store, err := r.translationSourceStore()
	if err != nil {
		return nil, err
	}
	source, err := store.GetTranslationByName(ctx, input.Namespace, input.Name)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return nil, translationReadUnavailable(err)
	}
	owner := ""
	if errors.Is(err, datastore.ErrNotFound) {
		source = nil
	}
	if source != nil {
		if source.Namespace != input.Namespace || source.Name != input.Name {
			return nil, translationReadUnavailable()
		}
		owner = source.EffectiveOwnerSub()
	}
	if err := r.authorizeTranslationSource(ctx, "status.write", input.Namespace, input.Name, owner); err != nil {
		return nil, err
	}
	if source == nil {
		return nil, nodeLookupError(nodeKindTranslation, datastore.ErrNotFound)
	}
	uid, err := decodeNodeIDAs(nodeKindTranslation, *input.UID)
	if err != nil {
		return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_SOURCE_UID", "invalid Translation source UID").ToGQLError()
	}
	if source.UID != uid || source.ResourceVersion != input.ResourceVersion {
		return nil, statusConflictError(nodeKindTranslation, input.Namespace, input.Name, source.ResourceVersion)
	}
	var status catalog.TranslationStatus
	if len(source.Status) > 0 && json.Unmarshal(source.Status, &status) != nil {
		return nil, translationReadUnavailable()
	}
	if input.ObservedGeneration != nil {
		status.ObservedGeneration = int64(*input.ObservedGeneration)
	}
	if input.LastAppliedRevision != nil {
		status.LastAppliedRevision = *input.LastAppliedRevision
	}
	if input.Conditions != nil {
		for _, condition := range input.Conditions {
			if condition == nil || !condition.Status.IsValid() {
				return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_CONDITION", "invalid Translation condition").ToGQLError()
			}
		}
		status.Conditions = mergeProductConditions(status.Conditions, toConditions(input.Conditions))
	}
	if input.Resolved != nil {
		raw, err := json.Marshal(input.Resolved)
		if err != nil || len(raw) > translation.MaxDocumentBytes {
			return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_RESOLVED", "invalid Translation resolved status").ToGQLError()
		}
		resolved := &catalog.ResolvedTranslation{}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(resolved) != nil ||
			len(resolved.ValidatedFieldPaths) > translation.MaxPatches+1 ||
			(len(resolved.ValidatedFieldPaths) > translation.MaxPatches && !slices.Contains(resolved.ValidatedFieldPaths, "{body}")) {
			return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_RESOLVED", "invalid Translation resolved status").ToGQLError()
		}
		status.Resolved = resolved
	}
	raw, err := json.Marshal(status)
	if err != nil || len(raw) > translation.MaxDocumentBytes {
		return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_STATUS_INPUT", "invalid Translation status input").ToGQLError()
	}
	updated, err := store.UpdateTranslationStatus(ctx, source.UID, input.ResourceVersion, raw)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	switch {
	case errors.Is(err, datastore.ErrConflict):
		return nil, admission.NewError(admission.CodeConflict, "RESOURCE_VERSION_CONFLICT", "Translation status update conflict").ToGQLError()
	case errors.Is(err, datastore.ErrNotFound):
		return nil, nodeLookupError(nodeKindTranslation, err)
	case errors.Is(err, datastore.ErrInvalidArgument):
		return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_STATUS_INPUT", "invalid Translation status input").ToGQLError()
	case err != nil:
		return nil, translationReadUnavailable(err)
	}
	current, guarded, err := r.guardTranslationRead(ctx, updated)
	if err != nil {
		return nil, err
	}
	return translationGuardedToGraphQL(current, guarded)
}
