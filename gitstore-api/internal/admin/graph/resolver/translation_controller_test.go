// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type translationControllerAuthorizer struct {
	auth.AuthZProvider
	onTarget   func()
	owner      string
	denyTarget bool
}

func (a *translationControllerAuthorizer) Authorize(ctx context.Context, principal *auth.Principal, action string, resource auth.ResourceContext) (auth.Decision, error) {
	if action == auth.ActionProductRead {
		a.owner = resource.OwnerSub
		if a.denyTarget {
			return auth.Decision{Outcome: auth.OutcomeDeny}, nil
		}
		if callback := a.onTarget; callback != nil {
			a.onTarget = nil
			callback()
		}
	}
	return a.AuthZProvider.Authorize(ctx, principal, action, resource)
}

func TestTranslationControllerBindingRequiresEnrollmentAndFencesSourceEpoch(t *testing.T) {
	root, human, source := newTranslationReadFixture(t)
	_, err := (&queryResolver{root}).TranslationBinding(human, source.Namespace, source.Name)
	require.Error(t, err, "even an allow-all provider must not enroll a human as a controller")
	controller := &auth.Principal{Subject: "system:serviceaccount:gitstore-controller-manager",
		ServiceAccountUID: uuid.NewString(), AuthMethod: "serviceaccount-jwt"}
	root.rbacValidator = &admission.RBACValidator{ManagedStore: root.store, ControllerSubject: controller.Subject}
	require.NoError(t, root.store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{
		UID: controller.ServiceAccountUID, Name: "gitstore-controller-manager",
	}))
	ctx := auth.ContextWithPrincipal(t.Context(), controller)
	state, err := (&queryResolver{root}).TranslationBinding(ctx, source.Namespace, source.Name)
	require.NoError(t, err)
	require.Equal(t, source.ResourceVersion, state.SourceVersion)
	require.NotEmpty(t, state.ReferenceEpoch)
	require.Nil(t, state.BoundUID)
	bindings := root.store.(datastore.TranslationBindingCapable).TranslationBindingStore()
	before, err := bindings.GetTranslationBinding(ctx, source.UID)
	require.NoError(t, err)
	// Target evidence is deliberately absent: enrollment is not target authority.
	_, err = (&mutationResolver{root}).BindTranslationTarget(ctx, model.BindTranslationTargetInput{
		Namespace: source.Namespace, Name: source.Name, TranslationID: state.TranslationID,
		SourceVersion: state.SourceVersion, BindingVersion: state.BindingVersion, ReferenceEpoch: state.ReferenceEpoch,
		TargetUID: uuid.NewString(),
	})
	require.Error(t, err)
	after, err := bindings.GetTranslationBinding(ctx, source.UID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	targetUID := uuid.NewString()
	require.NoError(t, root.store.CreateProduct(ctx, &datastore.Product{
		UID: targetUID, Namespace: source.Namespace, Name: "laptop", APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product",
		CreationActor: "target-owner", Generation: 1, ResourceVersion: "1",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond), Spec: json.RawMessage(`{"title":"Laptop"}`),
	}))
	provider := &translationControllerAuthorizer{AuthZProvider: root.registry.AuthZ()}
	root.registry = auth.NewProviderRegistry(nil, provider, nil)
	provider.onTarget = func() {
		product, readErr := root.store.GetProduct(ctx, targetUID)
		require.NoError(t, readErr)
		product.ResourceVersion = "2"
		require.NoError(t, root.store.UpdateProduct(ctx, product, "1"))
	}
	_, err = (&mutationResolver{root}).BindTranslationTarget(ctx, model.BindTranslationTargetInput{
		Namespace: source.Namespace, Name: source.Name, TranslationID: state.TranslationID,
		SourceVersion: state.SourceVersion, BindingVersion: state.BindingVersion, ReferenceEpoch: state.ReferenceEpoch,
		TargetUID: targetUID,
	})
	require.Error(t, err, "target authorization facts must match the serialized target observation")
	require.Equal(t, "target-owner", provider.owner)
	payload, err := (&mutationResolver{root}).BindTranslationTarget(ctx, model.BindTranslationTargetInput{
		Namespace: source.Namespace, Name: source.Name, TranslationID: state.TranslationID,
		SourceVersion: state.SourceVersion, BindingVersion: state.BindingVersion, ReferenceEpoch: state.ReferenceEpoch,
		TargetUID: targetUID,
	})
	require.NoError(t, err)
	require.NotNil(t, payload)
	bound := payload.Binding
	require.NotNil(t, bound)
	require.Equal(t, &targetUID, bound.BoundUID)
	require.NotEqual(t, state.BindingVersion, bound.BindingVersion)
	data, rejected := managedContractRequest(t, root, controller, `query {
		translationBinding(namespace:"acme",name:"laptop-title") { translationID sourceVersion bindingVersion referenceEpoch namespaceUID boundUID }
	}`, nil)
	require.Empty(t, rejected)
	require.Equal(t, targetUID, data["translationBinding"]["boundUID"])
	require.Equal(t, source.NamespaceID, data["translationBinding"]["namespaceUID"])
	data, rejected = managedContractRequest(t, root, controller, `mutation($input:BindTranslationTargetInput!) {
		bindTranslationTarget(input:$input) { binding { translationID sourceVersion bindingVersion referenceEpoch boundUID } }
	}`, map[string]any{
		"namespace": source.Namespace, "name": source.Name, "translationID": bound.TranslationID,
		"sourceVersion": bound.SourceVersion, "bindingVersion": bound.BindingVersion,
		"referenceEpoch": bound.ReferenceEpoch, "targetUID": targetUID,
	})
	require.Empty(t, rejected, "a retried completed bind must not create another epoch or version")
	returned := data["bindTranslationTarget"]["binding"].(map[string]any)
	require.Equal(t, targetUID, returned["boundUID"])
	require.Equal(t, bound.BindingVersion, returned["bindingVersion"])
	provider.denyTarget = true
	_, err = (&queryResolver{root}).TranslationBinding(ctx, source.Namespace, source.Name)
	require.Error(t, err, "enrollment and status permission cannot expose a target-read-denied binding")
	provider.denyTarget = false
	require.NoError(t, root.store.DeleteProduct(ctx, targetUID))
	audit := root.store.(datastore.TranslationTargetAuditStore)
	for turn := 0; ; turn++ {
		require.Less(t, turn, 64)
		progress, err := audit.RepairTranslationTargetAuditTurn(ctx, source.NamespaceID, 1)
		require.NoError(t, err)
		if progress.Phase == "Verified" {
			break
		}
	}
	retained, err := (&queryResolver{root}).TranslationBinding(ctx, source.Namespace, source.Name)
	require.NoError(t, err, "certified target deletion cannot erase the sticky dependency identity")
	require.Equal(t, &targetUID, retained.BoundUID)
	provider.denyTarget = true
	_, err = (&queryResolver{root}).TranslationBinding(ctx, source.Namespace, source.Name)
	require.Error(t, err, "certified absence does not bypass target-read authorization")
	provider.denyTarget = false
	store := root.store.(datastore.TranslationCapable).TranslationStore()
	current, err := store.GetTranslation(ctx, source.UID)
	require.NoError(t, err)
	require.Equal(t, source, current, "binding does not rewrite source, observations or authored bytes")
	current.ResourceVersion = "4"
	current.Generation++
	current.Spec = json.RawMessage(`{"locale":"fr","targetRef":{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"Product","name":"replacement"},"patches":[{"fieldPath":"spec.title","translation":"Titre"}]}`)
	require.NoError(t, store.UpdateTranslation(ctx, current, source.ResourceVersion))
	_, err = (&mutationResolver{root}).BindTranslationTarget(ctx, model.BindTranslationTargetInput{
		Namespace: source.Namespace, Name: source.Name, TranslationID: state.TranslationID,
		SourceVersion: state.SourceVersion, BindingVersion: state.BindingVersion, ReferenceEpoch: state.ReferenceEpoch,
		TargetUID: uuid.NewString(),
	})
	require.Error(t, err, "a prior epoch cannot bind the replacement reference")
	root.translationActivation.Enabled = false
	_, err = (&queryResolver{root}).TranslationBinding(ctx, source.Namespace, source.Name)
	require.Error(t, err)
}

func TestTranslationControllerDependentScansRemainNamespaceBounded(t *testing.T) {
	root, human, source := newTranslationReadFixture(t)
	controller := &auth.Principal{Subject: "system:serviceaccount:gitstore-controller-manager",
		ServiceAccountUID: uuid.NewString(), AuthMethod: "serviceaccount-jwt"}
	root.rbacValidator = &admission.RBACValidator{ManagedStore: root.store, ControllerSubject: controller.Subject}
	require.NoError(t, root.store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{
		UID: controller.ServiceAccountUID, Name: "gitstore-controller-manager",
	}))
	ctx := auth.ContextWithPrincipal(t.Context(), controller)
	input := model.TranslationDependencyInput{Namespace: source.Namespace, NamespaceUID: source.NamespaceID,
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "laptop"}
	data, rejected := managedContractRequest(t, root, controller, `
		query($input: TranslationDependencyInput!) {
			scanTranslationDependents(target: $input) {
				items { translationID sourceVersion referenceEpoch }
				continuation
			}
		}`, input)
	require.Empty(t, rejected)
	require.NotNil(t, data["scanTranslationDependents"]["items"])
	require.Contains(t, data["scanTranslationDependents"], "continuation")
	require.NotContains(t, data["scanTranslationDependents"], "next")
	size := int32(1)
	cursor := (*string)(nil)
	found := false
	exhausted := false
	emptyBatches := 0
	for turn := 0; turn < 64; turn++ {
		page, err := (&queryResolver{root}).ScanTranslationDependents(ctx, input, &size, cursor)
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.Items), 1)
		if len(page.Items) == 0 {
			emptyBatches++
		}
		for _, item := range page.Items {
			uid, err := decodeNodeIDAs(nodeKindTranslation, item.TranslationID)
			require.NoError(t, err)
			if uid == source.UID {
				found = true
				require.Equal(t, source.ResourceVersion, item.SourceVersion)
			}
		}
		if page.Continuation == nil {
			exhausted = true
			break
		}
		if cursor != nil {
			require.NotEqual(t, *cursor, *page.Continuation, "each nonterminal scan turn must advance")
		}
		cursor = page.Continuation
	}
	require.True(t, found)
	require.True(t, exhausted)
	require.Positive(t, emptyBatches, "scan progress must not require a returned hint")
	_, err := (&queryResolver{root}).ScanTranslationDependents(human, input, &size, nil)
	require.Error(t, err, "scan discovery is controller-only")
	for _, invalid := range []int32{-1, 0, 257} {
		_, err := (&queryResolver{root}).ScanTranslationDependents(ctx, input, &invalid, nil)
		require.Error(t, err)
	}
	input.NamespaceUID = uuid.NewString()
	_, err = (&queryResolver{root}).ScanTranslationDependents(ctx, input, &size, cursor)
	require.Error(t, err)
}

func TestTranslationControllerTargetObservationUsesOnlyAuthorizedThinProof(t *testing.T) {
	root, human, source := newTranslationReadFixture(t)
	controller := &auth.Principal{Subject: "system:serviceaccount:gitstore-controller-manager",
		ServiceAccountUID: uuid.NewString(), AuthMethod: "serviceaccount-jwt"}
	root.rbacValidator = &admission.RBACValidator{ManagedStore: root.store, ControllerSubject: controller.Subject}
	require.NoError(t, root.store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{
		UID: controller.ServiceAccountUID, Name: "gitstore-controller-manager",
	}))
	product := &datastore.Product{UID: uuid.NewString(), Namespace: source.Namespace, Name: "laptop",
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Generation: 1, ResourceVersion: "1",
		CreationActor: "target-owner", CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		Spec: json.RawMessage(`{"title":"Laptop"}`), Body: "exact\r\n"}
	require.NoError(t, root.store.CreateProduct(t.Context(), product))
	thin := &freshnessMetadataStore{Datastore: root.store,
		TranslationTargetWriteFenceStore: root.store.(datastore.TranslationTargetWriteFenceStore),
		TranslationTargetMetadataStore:   root.store.(datastore.TranslationTargetMetadataStore)}
	root.store = struct {
		*freshnessMetadataStore
		datastore.TranslationTargetNamespaceMetadataStore
	}{thin, root.store.(datastore.TranslationTargetNamespaceMetadataStore)}
	ctx := auth.ContextWithPrincipal(t.Context(), controller)
	query := `query {
					translationTargetObservation(namespace:"acme",apiVersion:"catalog.gitstore.dev/v1beta1",kind:"Product",name:"laptop") {
						namespaceUID evidence { apiVersion kind namespace name uid generation revision resourceVersion contentDigest }
					}
				}`
	data, rejected := managedContractRequest(t, root, controller, query, nil)
	require.Empty(t, rejected)
	require.Equal(t, source.NamespaceID, data["translationTargetObservation"]["namespaceUID"])
	evidence := data["translationTargetObservation"]["evidence"].(map[string]any)
	require.Equal(t, product.UID, evidence["uid"], "private target identity must not be a Relay source ID")
	require.NotEmpty(t, evidence["contentDigest"])
	require.Equal(t, 3, thin.reads, "one serialized fence/metadata/fence observation")
	require.Zero(t, thin.projectionReads, "invalidation classification must not read target content")
	_, err := (&queryResolver{root}).TranslationTargetObservation(human, source.Namespace, product.APIVersion, product.Kind, product.Name)
	require.Error(t, err, "humans are not enrolled controllers")
	provider := &translationControllerAuthorizer{AuthZProvider: root.registry.AuthZ(), denyTarget: true}
	root.registry = auth.NewProviderRegistry(nil, provider, nil)
	result, err := (&queryResolver{root}).TranslationTargetObservation(ctx, source.Namespace, product.APIVersion, product.Kind, product.Name)
	require.Error(t, err)
	require.Nil(t, result, "denied target evidence cannot be used as a material no-op certificate")
	require.Zero(t, thin.projectionReads)
	provider.denyTarget = false
	provider.onTarget = func() {
		namespace, err := thin.Datastore.GetNamespaceByName(ctx, source.Namespace)
		require.NoError(t, err)
		previous := namespace.ResourceVersion
		namespace.ResourceVersion = "20"
		require.NoError(t, thin.Datastore.UpdateNamespace(ctx, namespace, previous))
	}
	result, err = (&queryResolver{root}).TranslationTargetObservation(ctx, source.Namespace, product.APIVersion, product.Kind, product.Name)
	require.Error(t, err, "the namespace observation must remain unchanged across authorization")
	require.Nil(t, result)
}
