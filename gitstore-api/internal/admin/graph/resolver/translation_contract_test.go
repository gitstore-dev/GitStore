// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

func newTranslationReadFixture(t *testing.T) (*Resolver, context.Context, *datastore.Translation) {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	namespace := &datastore.Namespace{UID: uuid.NewString(), Name: "acme", CreationActor: "alice"}
	require.NoError(t, store.CreateNamespace(t.Context(), namespace))
	require.NoError(t, store.CreateNamespace(t.Context(), &datastore.Namespace{UID: uuid.NewString(), Name: "other", CreationActor: "alice"}))
	source := &datastore.Translation{
		APIVersion: "i18n.gitstore.dev/v1beta1", Kind: "Translation",
		Namespace: namespace.Name, NamespaceID: namespace.UID, Name: "laptop-title",
		UID: uuid.NewString(), Generation: 1, ResourceVersion: "1",
		CreationActor: "alice", CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		Revision: "main@sha1:older-observation",
		Spec:     json.RawMessage(`{"locale":"iw-IL","targetRef":{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"Product","name":"laptop"},"patches":[{"fieldPath":"spec.options{color}.title","translation":""},{"fieldPath":"spec.title","translation":"Titre"}]}`),
		Body:     "old body", RawSource: []byte("complete accepted source\r\n"),
	}
	translations := store.(datastore.TranslationCapable).TranslationStore()
	require.NoError(t, translations.CreateTranslation(t.Context(), source))
	source, err = translations.UpdateTranslationStatus(t.Context(), source.UID, source.ResourceVersion,
		json.RawMessage(`{"observedGeneration":1,"lastAppliedRevision":"main@sha1:older-observation","conditions":[{"type":"Ready","status":"True"}],"resolved":{"canonicalLocale":"he-IL","descriptorDigest":"private-evidence"}}`))
	require.NoError(t, err)
	expected := source.ResourceVersion
	source.Body, source.Revision = "\r\n# Texte \u00e9crit\n\n---\n", "main@sha1:accepted-source"
	source.Generation, source.ResourceVersion = 2, "3"
	require.NoError(t, translations.UpdateTranslation(t.Context(), source, expected))
	resolver, err := NewResolver(ResolverDeps{
		Store: store, Logger: zap.NewNop(), Registry: auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil),
	})
	require.NoError(t, err)
	resolver.translationActivation.Enabled = true
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"})
	return resolver, ctx, source
}

func TestTranslationStatusRequiresSourceLifetime(t *testing.T) {
	root, ctx, source := newTranslationReadFixture(t)
	ctx = auth.ContextWithPrincipal(ctx, translationEnrolledController(t, root))
	generation := int32(source.Generation)
	_, err := (&mutationResolver{root}).UpdateResourceStatus(ctx, model.UpdateResourceStatusInput{
		Kind: "Translation", Namespace: source.Namespace, Name: source.Name, ResourceVersion: source.ResourceVersion,
		ObservedGeneration: &generation, LastAppliedRevision: &source.Revision,
	})
	require.Error(t, err, "name/version/generation/revision cannot identify a Translation lifetime")
}

func TestTranslationStatusRejectsRecreatedSourceWithIdenticalVersions(t *testing.T) {
	for _, path := range []string{"typed", "generic"} {
		t.Run(path, func(t *testing.T) {
			root, ctx, source := newTranslationReadFixture(t)
			controller := &auth.Principal{Subject: "system:serviceaccount:gitstore-controller-manager",
				ServiceAccountUID: uuid.NewString(), AuthMethod: "serviceaccount-jwt"}
			root.rbacValidator = &admission.RBACValidator{ManagedStore: root.store, ControllerSubject: controller.Subject}
			require.NoError(t, root.store.CreateServiceAccount(ctx, &datastore.ServiceAccount{
				UID: controller.ServiceAccountUID, Name: "gitstore-controller-manager",
			}))
			store := root.store.(datastore.TranslationCapable).TranslationStore()
			require.NoError(t, store.DeleteTranslation(ctx, source.UID, source.ResourceVersion))
			replacement := *source
			replacement.UID = uuid.NewString()
			replacement.CreationTimestamp = source.CreationTimestamp.Add(time.Millisecond)
			replacement.Generation, replacement.ResourceVersion, replacement.Status = 1, "1", nil
			replacement.Body, replacement.Revision = "old body", "main@sha1:older-observation"
			require.NoError(t, store.CreateTranslation(ctx, &replacement))
			second, err := store.UpdateTranslationStatus(ctx, replacement.UID, "1",
				json.RawMessage(`{"observedGeneration":1,"lastAppliedRevision":"main@sha1:older-observation","conditions":[]}`))
			require.NoError(t, err)
			second.Generation, second.ResourceVersion, second.Body, second.Revision = source.Generation, source.ResourceVersion, source.Body, source.Revision
			require.NoError(t, store.UpdateTranslation(ctx, second, "2"))
			before, err := store.GetTranslation(ctx, replacement.UID)
			require.NoError(t, err)
			require.Equal(t, source.ResourceVersion, before.ResourceVersion)
			require.Equal(t, source.Generation, before.Generation)
			require.Equal(t, source.Revision, before.Revision)
			oldID := mustEncodeNodeID(nodeKindTranslation, source.UID)
			newID := mustEncodeNodeID(nodeKindTranslation, replacement.UID)
			apply := func(id string) error {
				input := map[string]any{
					"namespace": source.Namespace, "name": source.Name, "uid": id, "resourceVersion": source.ResourceVersion,
					"observedGeneration": source.Generation, "lastAppliedRevision": source.Revision, "conditions": []any{},
				}
				var rejected []*gqlerror.Error
				if path == "typed" {
					_, rejected = managedContractRequest(t, root, controller, `mutation($input: UpdateTranslationStatusInput!) {
						updateTranslationStatus(input:$input) { translation { metadata { resourceVersion } } }
					}`, input)
				} else {
					input["kind"] = "Translation"
					_, rejected = managedContractRequest(t, root, controller, `mutation($input: UpdateResourceStatusInput!) {
						updateResourceStatus(input:$input) { object }
					}`, input)
				}
				if len(rejected) != 0 {
					return rejected[0]
				}
				return nil
			}
			var failure *gqlerror.Error
			require.ErrorAs(t, apply(oldID), &failure)
			require.Equal(t, "CONFLICT", failure.Extensions["code"])
			after, err := store.GetTranslation(ctx, replacement.UID)
			require.NoError(t, err)
			require.Equal(t, before, after, "a delayed worker must not alter a replacement source")
			require.NoError(t, apply(newID))
		})
	}
}

func TestTranslationSourceReadsAgreeWithoutCurrentObservation(t *testing.T) {
	r, ctx, source := newTranslationReadFixture(t)
	named, err := (&queryResolver{r}).Translation(ctx, model.TranslationBy{
		NamespacePath: &model.TranslationNamespacePath{Namespace: source.Namespace, Name: source.Name},
	})
	require.NoError(t, err)
	byID, err := (&queryResolver{r}).Translation(ctx, model.TranslationBy{ID: &named.ID})
	require.NoError(t, err)
	require.Equal(t, named, byID)
	node, err := (&queryResolver{r}).resolveNode(ctx, nodeKindTranslation, source.UID)
	require.NoError(t, err)
	require.Equal(t, named, node)
	require.Equal(t, named.ID, named.Metadata.UID)
	require.NotEqual(t, source.UID, named.ID)
	require.Equal(t, source.Revision, *named.Metadata.Revision)
	require.Equal(t, int32(source.Generation), named.Metadata.Generation)
	require.Equal(t, source.Body, *named.Body)
	require.Equal(t, "iw-IL", named.Spec.Locale, "do not replace authored locale with canonical comparison form")
	require.Nil(t, named.Spec.TargetRef.Namespace, "preserve an omitted namespace instead of rewriting authored spec")
	require.Equal(t, "spec.options{color}.title", named.Spec.Patches[0].FieldPath)
	require.Empty(t, named.Spec.Patches[0].Translation)
	require.Equal(t, "spec.title", named.Spec.Patches[1].FieldPath)
	require.NotNil(t, named.Status)
	require.Equal(t, model.ConditionStatusFalse, named.Status.Conditions[0].Status, "stored stale True must be masked")
	require.Nil(t, named.Status.Resolved)

	params := datastore.PageParams{First: 1}
	first := int32(1)
	var listed []*model.Translation
	for turn := 0; ; turn++ {
		require.Less(t, turn, 16, "bounded sparse continuations must finish")
		page, err := (&queryResolver{r}).Translations(ctx, source.Namespace, &first, stringPointer(params.After), nil, nil)
		require.NoError(t, err)
		for _, edge := range page.Edges {
			listed = append(listed, edge.Node)
			require.NotEmpty(t, edge.Cursor)
		}
		if !page.PageInfo.HasNextPage {
			break
		}
		require.NotNil(t, page.PageInfo.EndCursor, "empty sparse pages must retain their continuation")
		require.NotEqual(t, params.After, *page.PageInfo.EndCursor)
		params.After = *page.PageInfo.EndCursor
		_, err = r.readTranslations(ctx, "other", params)
		require.Error(t, err, "cursors must not cross namespace boundaries")
	}
	require.Equal(t, []*model.Translation{named}, listed)
	var before *string
	var backward []*model.Translation
	for turn := 0; ; turn++ {
		require.Less(t, turn, 16)
		page, err := (&queryResolver{r}).Translations(ctx, source.Namespace, nil, nil, &first, before)
		require.NoError(t, err)
		for _, edge := range page.Edges {
			backward = append(backward, edge.Node)
		}
		if !page.PageInfo.HasPreviousPage {
			break
		}
		require.NotNil(t, page.PageInfo.StartCursor, "reverse empty pages must retain their continuation")
		require.NotEqual(t, before, page.PageInfo.StartCursor)
		before = page.PageInfo.StartCursor
	}
	require.Equal(t, []*model.Translation{named}, backward)
	stored, err := r.store.(datastore.TranslationCapable).TranslationStore().GetTranslation(ctx, source.UID)
	require.NoError(t, err)
	require.Equal(t, source.RawSource, stored.RawSource)
	require.Equal(t, source.Status, stored.Status, "masking a response cannot rewrite controller evidence")
	_, err = r.store.(datastore.TranslationCapable).TranslationStore().UpdateTranslationStatus(ctx, stored.UID, stored.ResourceVersion,
		json.RawMessage(`{"observedGeneration":2,"lastAppliedRevision":"main@sha1:accepted-source","conditions":[{"type":"Ready","status":"True"}]}`))
	require.NoError(t, err)
	current, err := r.readTranslation(ctx, model.TranslationBy{ID: &named.ID})
	require.NoError(t, err)
	require.NotNil(t, current.Status)
	require.Equal(t, model.ConditionStatusFalse, current.Status.Conditions[0].Status, "generation alone is not target proof")
	require.Nil(t, current.Status.Resolved)
}

func TestTranslationBodyOnlyReadPreservesPinsAndAbsentPatches(t *testing.T) {
	r, ctx, source := newTranslationReadFixture(t)
	expected := source.ResourceVersion
	pin := uuid.NewString()
	source.Spec = json.RawMessage(`{"locale":"fr-FR","targetRef":{"apiVersion":"storage.gitstore.dev/v1beta1","kind":"File","namespace":"acme","name":"hero","uid":"` + pin + `","resourceVersion":"7"}}`)
	source.Generation, source.ResourceVersion = 3, "4"
	source.Body = "\r\nDescription.\r\n---\r\n"
	require.NoError(t, r.store.(datastore.TranslationCapable).TranslationStore().UpdateTranslation(ctx, source, expected))
	value, err := (&queryResolver{r}).Translation(ctx, model.TranslationBy{
		NamespacePath: &model.TranslationNamespacePath{Namespace: source.Namespace, Name: source.Name},
	})
	require.NoError(t, err)
	require.NotNil(t, value.Spec.Patches)
	require.Empty(t, value.Spec.Patches, "body-only documents must still satisfy the non-null GraphQL patch list")
	require.Equal(t, "File", *value.Spec.TargetRef.Kind)
	require.Equal(t, source.Namespace, *value.Spec.TargetRef.Namespace)
	require.Equal(t, pin, *value.Spec.TargetRef.UID, "authored pin identity must not be rewritten")
	require.Equal(t, "7", *value.Spec.TargetRef.ResourceVersion)
	require.Equal(t, source.Body, *value.Body)
	require.NotNil(t, value.Status)
	require.Equal(t, model.ConditionStatusFalse, value.Status.Conditions[0].Status)
	require.Nil(t, value.Status.Resolved)
}

type translationSourceReadAuthorizer struct {
	auth.AuthZProvider
	fail, denyRead, denyStatus, denyWatch bool
}

func (a translationSourceReadAuthorizer) Authorize(ctx context.Context, principal *auth.Principal, action string, resource auth.ResourceContext) (auth.Decision, error) {
	if a.fail {
		return auth.Decision{}, errors.New("private provider failure")
	}
	if a.denyRead && action == auth.ActionTranslationRead {
		return auth.Decision{Outcome: auth.OutcomeDeny}, nil
	}
	if a.denyStatus && action == auth.ActionTranslationStatusWrite || a.denyWatch && action == auth.ActionTranslationWatch {
		return auth.Decision{Outcome: auth.OutcomeDeny}, nil
	}
	return a.AuthZProvider.Authorize(ctx, principal, action, resource)
}

func TestTranslationTypedAndGenericStatusCASPreserveSource(t *testing.T) {
	for _, path := range []string{"typed", "generic"} {
		t.Run(path, func(t *testing.T) {
			r, ctx, source := newTranslationReadFixture(t)
			ctx = auth.ContextWithPrincipal(ctx, translationEnrolledController(t, r))
			conditions := []*model.ConditionInput{{
				Type: "Ready", Status: model.ConditionStatusTrue, ObservedGeneration: 2,
				LastTransitionTime: time.Now().UTC().Truncate(time.Millisecond),
			}}
			resolved := &model.ResolvedTranslationInput{
				CanonicalLocale: stringPointer("he-IL"), DescriptorDigest: "private-descriptor", SourceDigest: "private-source",
				ValidatedFieldPaths: []string{"spec.options{color}.title", "spec.title"},
			}
			mutation := &mutationResolver{r}
			sourceID := mustEncodeNodeID(nodeKindTranslation, source.UID)
			if path == "typed" {
				result, err := mutation.UpdateTranslationStatus(ctx, model.UpdateTranslationStatusInput{
					Namespace: source.Namespace, Name: source.Name, UID: sourceID, ResourceVersion: source.ResourceVersion,
					ObservedGeneration: 2, LastAppliedRevision: source.Revision, Conditions: conditions, Resolved: resolved,
				})
				require.NoError(t, err)
				require.Equal(t, "4", result.Translation.Metadata.ResourceVersion)
				require.NotNil(t, result.Translation.Status)
				require.Equal(t, model.ConditionStatusFalse, result.Translation.Status.Conditions[0].Status)
				require.Nil(t, result.Translation.Status.Resolved)
			} else {
				generation := int32(2)
				result, err := mutation.UpdateResourceStatus(ctx, model.UpdateResourceStatusInput{
					Kind: "Translation", Namespace: source.Namespace, Name: source.Name, UID: &sourceID, ResourceVersion: source.ResourceVersion,
					ObservedGeneration: &generation, LastAppliedRevision: &source.Revision, Conditions: conditions,
					Resolved: map[string]any{"canonicalLocale": "he-IL", "descriptorDigest": "private-descriptor",
						"sourceDigest": "private-source", "validatedFieldPaths": []any{"spec.options{color}.title", "spec.title"}},
				})
				require.NoError(t, err)
				status := result.Object["status"].(map[string]any)
				require.Nil(t, status["resolved"])
				require.Equal(t, "FALSE", status["conditions"].([]any)[0].(map[string]any)["status"])
				require.Equal(t, "4", result.Object["metadata"].(map[string]any)["resourceVersion"])
			}
			store := r.store.(datastore.TranslationCapable).TranslationStore()
			stored, err := store.GetTranslation(ctx, source.UID)
			require.NoError(t, err)
			require.Equal(t, source.Spec, stored.Spec)
			require.Equal(t, source.RawSource, stored.RawSource)
			require.Equal(t, source.Body, stored.Body)
			require.Equal(t, source.Revision, stored.Revision)
			require.Equal(t, source.Generation, stored.Generation)
			var status catalog.TranslationStatus
			require.NoError(t, json.Unmarshal(stored.Status, &status))
			require.Equal(t, source.Revision, status.LastAppliedRevision)
			require.Equal(t, catalog.ConditionTrue, status.Conditions[0].Status)
			require.Equal(t, conditions[0].LastTransitionTime, status.Conditions[0].LastTransitionTime)
			require.Equal(t, "private-source", status.Resolved.SourceDigest)

			_, err = mutation.UpdateResourceStatus(ctx, model.UpdateResourceStatusInput{
				Kind: "Translation", Namespace: source.Namespace, Name: source.Name, UID: &sourceID,
				ResourceVersion: stored.ResourceVersion, Conditions: []*model.ConditionInput{{
					Type: "Ready", Status: model.ConditionStatusUnknown, ObservedGeneration: 2,
					LastTransitionTime: conditions[0].LastTransitionTime,
				}},
			})
			require.NoError(t, err)
			stored, err = store.GetTranslation(ctx, source.UID)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(stored.Status, &status))
			require.Equal(t, catalog.ConditionUnknown, status.Conditions[0].Status)
			require.Equal(t, "private-source", status.Resolved.SourceDigest, "omitted fields must survive a generic status patch")

			for _, invalid := range []model.UpdateTranslationStatusInput{
				{ResourceVersion: source.ResourceVersion, ObservedGeneration: 2, LastAppliedRevision: source.Revision},
				{ResourceVersion: stored.ResourceVersion, ObservedGeneration: 3, LastAppliedRevision: source.Revision},
				{ResourceVersion: stored.ResourceVersion, ObservedGeneration: 2, LastAppliedRevision: "main@sha1:older-observation"},
			} {
				invalid.Namespace, invalid.Name = source.Namespace, source.Name
				invalid.UID = sourceID
				result, err := mutation.UpdateTranslationStatus(ctx, invalid)
				require.Nil(t, result)
				var failure *gqlerror.Error
				require.ErrorAs(t, err, &failure)
				require.Equal(t, "CONFLICT", failure.Extensions["code"])
			}
		})
	}
}

func TestTranslationStatusFailuresDoNotWrite(t *testing.T) {
	for _, mode := range []string{"denied", "provider", "canceled", "unknown-resolved-field", "invalid-enum", "finalizers"} {
		t.Run(mode, func(t *testing.T) {
			r, ctx, source := newTranslationReadFixture(t)
			ctx = auth.ContextWithPrincipal(ctx, translationEnrolledController(t, r))
			r.registry = auth.NewProviderRegistry(nil, translationSourceReadAuthorizer{
				AuthZProvider: testutil.NewAllowAllAuthZ(), fail: mode == "provider", denyStatus: mode == "denied",
			}, nil)
			generation := int32(source.Generation)
			sourceID := mustEncodeNodeID(nodeKindTranslation, source.UID)
			input := model.UpdateResourceStatusInput{Kind: "Translation", Namespace: source.Namespace, Name: source.Name,
				UID: &sourceID, ResourceVersion: source.ResourceVersion, ObservedGeneration: &generation, LastAppliedRevision: &source.Revision}
			switch mode {
			case "canceled":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			case "unknown-resolved-field":
				input.Resolved = map[string]any{"uncheckedEvidence": true}
			case "invalid-enum":
				input.Conditions = []*model.ConditionInput{{Type: "Ready", Status: "invalid"}}
			case "finalizers":
				input.Finalizers = []string{"unauthorized-finalizer"}
			}
			value, err := (&mutationResolver{r}).UpdateResourceStatus(ctx, input)
			require.Nil(t, value)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "private provider failure")
			stored, err := r.store.(datastore.TranslationCapable).TranslationStore().GetTranslation(t.Context(), source.UID)
			require.NoError(t, err)
			require.Equal(t, source.ResourceVersion, stored.ResourceVersion)
			require.Equal(t, source.Status, stored.Status)
		})
	}
}

func TestTranslationWatchesReplaySharedJournalAcrossReplicas(t *testing.T) {
	r, ctx, source := newTranslationReadFixture(t)
	journal := r.store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	newReplica := func() *Resolver {
		deps := repositoryWatchResolverDeps(r.store, journal)
		deps.Registry = r.registry
		replica, err := NewResolver(deps)
		require.NoError(t, err)
		replica.translationActivation.Enabled = true
		return replica
	}
	a, b := newReplica(), newReplica()
	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	bootstrap := watchjournal.BootstrapCursor
	bootstrapCtx, stopBootstrap := context.WithCancel(ctx)
	t.Cleanup(stopBootstrap)
	bootstrapStream, err := a.Subscription().WatchTranslations(bootstrapCtx, &source.Namespace, nil, &bootstrap)
	require.NoError(t, err)
	receive := func(events <-chan *model.TranslationWatchEvent) *model.TranslationWatchEvent {
		t.Helper()
		select {
		case event, open := <-events:
			require.True(t, open)
			return event
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for shared Translation journal")
			return nil
		}
	}
	bookmark := receive(bootstrapStream)
	require.Equal(t, model.WatchEventTypeBookmark, bookmark.Type)
	require.Nil(t, bookmark.Translation)
	// This bookmark-only stream must not schedule live revalidation during replay.
	stopBootstrap()
	select {
	case _, open := <-bootstrapStream:
		require.False(t, open)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out closing the bootstrap-only stream")
	}
	expected := source.ResourceVersion
	source.Body, source.ResourceVersion, source.Generation = "new accepted body\r\n", "4", 3
	store := r.store.(datastore.TranslationCapable).TranslationStore()
	require.NoError(t, store.UpdateTranslation(ctx, source, expected))
	typed, err := b.Subscription().WatchTranslations(ctx, &source.Namespace, nil, &bookmark.ResourceVersion)
	require.NoError(t, err)
	generic, err := a.Subscription().WatchResources(ctx, "Translation", &source.Namespace, nil, &bookmark.ResourceVersion)
	require.NoError(t, err)
	event := receive(typed)
	require.Equal(t, model.WatchEventTypeModified, event.Type)
	require.True(t, event.Historical)
	require.Equal(t, source.Body, *event.Translation.Body)
	require.Nil(t, event.Translation.Status)
	boxed := receiveGenericRepositoryEvent(t, generic)
	require.Equal(t, event.ResourceVersion, boxed.ResourceVersion)
	require.Equal(t, event.Translation.ID, boxed.Object["id"])
	require.Equal(t, true, boxed.Object["historical"])
	require.NotContains(t, boxed.Object, "status")
	require.NoError(t, store.DeleteTranslation(ctx, source.UID, source.ResourceVersion))
	replacement := newReplica()
	replayed, err := replacement.Subscription().WatchTranslations(ctx, &source.Namespace, nil, &event.ResourceVersion)
	require.NoError(t, err)
	deleted := receive(replayed)
	require.Equal(t, model.WatchEventTypeDeleted, deleted.Type)
	require.Nil(t, deleted.Translation)
	require.Equal(t, model.WatchEventTypeDeleted, receiveGenericRepositoryEvent(t, generic).Type)
}

func TestTranslationWatchRequiresPermissionAndJournal(t *testing.T) {
	for _, mode := range []string{"denied", "provider", "missing-journal"} {
		t.Run(mode, func(t *testing.T) {
			r, ctx, source := newTranslationReadFixture(t)
			r.registry = auth.NewProviderRegistry(nil, translationSourceReadAuthorizer{
				AuthZProvider: testutil.NewAllowAllAuthZ(), fail: mode == "provider", denyWatch: mode == "denied",
			}, nil)
			stream, err := r.Subscription().WatchTranslations(ctx, &source.Namespace, nil, nil)
			require.Nil(t, stream)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "private provider failure")
			generic, err := r.Subscription().WatchResources(ctx, "Translation", &source.Namespace, nil, nil)
			require.Nil(t, generic)
			require.Error(t, err)
		})
	}
}

func TestTranslationWatchGrantCannotExposeDeniedSource(t *testing.T) {
	r, ctx, source := newTranslationReadFixture(t)
	provider := translationSourceReadAuthorizer{AuthZProvider: testutil.NewAllowAllAuthZ(), denyRead: true}
	deps := repositoryWatchResolverDeps(r.store, r.store.(datastore.ResourceWatchCapable).ResourceWatchJournal())
	deps.Registry = auth.NewProviderRegistry(nil, provider, nil)
	replica, err := NewResolver(deps)
	require.NoError(t, err)
	replica.translationActivation.Enabled = true
	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	bootstrap := watchjournal.BootstrapCursor
	initial, err := replica.Subscription().WatchTranslations(ctx, &source.Namespace, nil, &bootstrap)
	require.NoError(t, err)
	var cursor string
	select {
	case event := <-initial:
		require.NotNil(t, event)
		require.Equal(t, model.WatchEventTypeBookmark, event.Type)
		cursor = event.ResourceVersion
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for shared-journal bookmark")
	}
	expected := source.ResourceVersion
	source.Body, source.ResourceVersion, source.Generation = "denied body", "4", 3
	require.NoError(t, r.store.(datastore.TranslationCapable).TranslationStore().UpdateTranslation(ctx, source, expected))
	typed, err := replica.Subscription().WatchTranslations(ctx, &source.Namespace, nil, &cursor)
	require.NoError(t, err)
	generic, err := replica.Subscription().WatchResources(ctx, "Translation", &source.Namespace, nil, &cursor)
	require.NoError(t, err)
	select {
	case event, open := <-typed:
		require.False(t, open)
		require.Nil(t, event)
	case <-time.After(3 * time.Second):
		t.Fatal("typed watch did not close on source denial")
	}
	select {
	case event, open := <-generic:
		require.False(t, open)
		require.Nil(t, event)
	case <-time.After(3 * time.Second):
		t.Fatal("generic watch did not close on source denial")
	}
}

func TestTranslationSourceReadsFailClosedOnProviderOrItemDenial(t *testing.T) {
	for _, mode := range []string{"provider-error", "item-denied", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			r, ctx, source := newTranslationReadFixture(t)
			r.registry = auth.NewProviderRegistry(nil, translationSourceReadAuthorizer{
				AuthZProvider: testutil.NewAllowAllAuthZ(), fail: mode == "provider-error", denyRead: mode == "item-denied",
			}, nil)
			if mode == "canceled" {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			value, err := r.readTranslation(ctx, model.TranslationBy{
				NamespacePath: &model.TranslationNamespacePath{Namespace: source.Namespace, Name: source.Name},
			})
			require.Nil(t, value)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "private provider failure")
			params := datastore.PageParams{First: 1}
			for turn := 0; ; turn++ {
				require.Less(t, turn, 16)
				page, err := r.readTranslations(ctx, source.Namespace, params)
				if err != nil {
					require.Nil(t, page, "item denial must not return partial source data")
					require.NotContains(t, err.Error(), "private provider failure")
					break
				}
				require.Empty(t, page.Edges)
				require.True(t, page.PageInfo.HasNextPage)
				require.NotNil(t, page.PageInfo.EndCursor)
				params.After = *page.PageInfo.EndCursor
			}
		})
	}
}

func TestTranslationSourceReadAuthorizationAndSelectors(t *testing.T) {
	r, ctx, source := newTranslationReadFixture(t)
	valid := model.TranslationBy{NamespacePath: &model.TranslationNamespacePath{Namespace: source.Namespace, Name: source.Name}}
	id := "wrong-kind-or-malformed"
	for _, by := range []model.TranslationBy{{}, {ID: &id}, {ID: &id, NamespacePath: valid.NamespacePath},
		{NamespacePath: &model.TranslationNamespacePath{Name: source.Name}}} {
		value, err := r.readTranslation(ctx, by)
		require.Nil(t, value)
		require.Error(t, err)
	}
	r.registry = auth.NewProviderRegistry(nil, testutil.NewDenyAllAuthZ(t), nil)
	var denied *gqlerror.Error
	for _, name := range []string{source.Name, "absent"} {
		value, err := r.readTranslation(ctx, model.TranslationBy{
			NamespacePath: &model.TranslationNamespacePath{Namespace: source.Namespace, Name: name},
		})
		require.Nil(t, value)
		var failure *gqlerror.Error
		require.ErrorAs(t, err, &failure)
		require.Equal(t, "FORBIDDEN", failure.Extensions["code"])
		if denied != nil {
			require.Equal(t, denied.Message, failure.Message)
			require.Equal(t, denied.Extensions, failure.Extensions, "denial must not reveal existence")
		}
		denied = failure
	}
	page, err := r.readTranslations(ctx, source.Namespace, datastore.PageParams{First: 1})
	require.Nil(t, page)
	require.Error(t, err)
	r.registry = nil
	value, err := r.readTranslation(ctx, valid)
	require.Nil(t, value)
	require.Error(t, err, "missing provider cannot fall back to unguarded source")
}
