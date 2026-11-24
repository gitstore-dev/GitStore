// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/opa"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

func translationEnrolledController(t *testing.T, root *Resolver) *auth.Principal {
	t.Helper()
	controller := &auth.Principal{Subject: "system:serviceaccount:gitstore-controller-manager",
		ServiceAccountUID: uuid.NewString(), AuthMethod: "serviceaccount-jwt"}
	root.rbacValidator = &admission.RBACValidator{ManagedStore: root.store, ControllerSubject: controller.Subject}
	require.NoError(t, root.store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{
		UID: controller.ServiceAccountUID, Name: "gitstore-controller-manager",
	}))
	return controller
}

func translationConfiguredProvider(t *testing.T, name string, source *datastore.Translation) auth.AuthZProvider {
	t.Helper()
	grants := map[string][]string{
		"translation-reader":        {"translation.read", "translation.list", "translation.watch"},
		"translation-target-reader": {"translation.read", "translation.list", "translation.watch", "product.read"},
		"translation-status-human":  {"translation.read", "translation.status.write"},
	}
	if name == "rbac-local" {
		policy, err := os.ReadFile("../../../../../config/policy.yaml")
		require.NoError(t, err)
		path := filepath.Join(t.TempDir(), "policy.yaml")
		require.NoError(t, os.WriteFile(path, policy, 0600))
		for actor, actions := range grants {
			require.NoError(t, rbaclocal.AddRole(path, actor, rbaclocal.RolePolicy{Allow: actions},
				rbaclocal.RoleOptions{Scope: "namespace"}))
			_, err := rbaclocal.AssignRole(path, actor, actor, rbaclocal.BindingOptions{
				Scope: "namespace", Namespace: source.Namespace, SubjectKind: "User", RoleKind: "Role",
			})
			require.NoError(t, err)
		}
		provider, err := rbaclocal.New(config.RBACConfig{PolicyFile: path}, zap.NewNop())
		require.NoError(t, err)
		return provider
	}
	provider, err := opa.NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	var resources []*rbac.Resource
	for actor, actions := range grants {
		spec := rbac.RoleSpec{}
		for _, action := range actions {
			definition, ok := auth.LookupAction(action)
			require.True(t, ok)
			spec.Rules = append(spec.Rules, rbac.Rule{Resources: []string{definition.Resource}, Verbs: []string{definition.Verb}})
		}
		raw, err := json.Marshal(spec)
		require.NoError(t, err)
		role := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.Role, NamespaceUID: source.NamespaceID,
			Metadata: rbac.Metadata{Name: actor, Namespace: source.Namespace, UID: uuid.NewString()}, Spec: raw}
		raw, err = json.Marshal(rbac.BindingSpec{RoleRef: rbac.RoleRef{Kind: rbac.Role, Name: actor},
			Subjects: []rbac.Subject{{Kind: "User", Name: actor}}})
		require.NoError(t, err)
		resources = append(resources, role, &rbac.Resource{
			APIVersion: rbac.APIVersion, Kind: rbac.RoleBinding, NamespaceUID: source.NamespaceID,
			Metadata: rbac.Metadata{Name: actor, Namespace: source.Namespace, UID: uuid.NewString()},
			Spec:     raw,
			Status:   json.RawMessage(`{"resolved":{"roleRef":{"uid":"` + role.Metadata.UID + `"}}}`),
		})
	}
	for _, tier := range []auth.AuthorizationTier{
		{State: rbac.TierState{TierID: rbac.ClusterTierID}},
		{Namespace: source.Namespace, State: rbac.TierState{TierID: source.NamespaceID}, Resources: resources},
	} {
		tier.NamespaceVersion, tier.Active = "1", true
		tier.CatalogDigest, tier.BuiltinDigest = auth.AuthorizationCatalogDigest(), builtins.Digest()
		tier.State.Revision = 1
		tier.State.Projection = rbac.ProjectionIdentity{
			GitRevision: "main@sha1:" + strings.Repeat("a", 40), TreeDigest: strings.Repeat("a", 64),
		}
		for _, resource := range tier.Resources {
			if resource.Kind.IsRole() {
				tier.State.RoleCount++
			} else {
				tier.State.BindingCount++
			}
		}
		raw, err := json.Marshal(tier)
		require.NoError(t, err)
		digest := sha256.Sum256(raw)
		tier.ContentDigest = hex.EncodeToString(digest[:])
		require.NoError(t, provider.PublishTier(t.Context(), tier))
	}
	return provider
}

func TestTranslationConfiguredProviderRoutes(t *testing.T) {
	for _, name := range []string{"rbac-local", "opa"} {
		t.Run(name, func(t *testing.T) {
			root, ctx, source, target := newTranslationStatusRouteFixture(t)
			provider := translationConfiguredProvider(t, name, source)
			root.registry = auth.NewProviderRegistry(nil, provider, nil)
			controller := translationEnrolledController(t, root)
			for _, actor := range []struct {
				principal *auth.Principal
				ready     string
			}{
				{&auth.Principal{Subject: "translation-reader", AuthMethod: "static-users"}, "UNKNOWN"},
				{&auth.Principal{Subject: "translation-target-reader", AuthMethod: "static-users"}, "TRUE"},
				{controller, "TRUE"},
			} {
				for _, route := range []string{"named", "node", "list"} {
					t.Run(actor.principal.Subject+"/"+route, func(t *testing.T) {
						value := translationStatusReadRoute(t, root, actor.principal, source, route)
						status := value["status"].(map[string]any)
						require.Equal(t, actor.ready, status["conditions"].([]any)[0].(map[string]any)["status"])
						if actor.ready == "UNKNOWN" {
							require.Nil(t, status["resolved"])
							require.Len(t, status["conditions"], 1)
							require.NotContains(t, fmt.Sprint(value), target.UID)
						}
					})
				}
			}
			query := fmt.Sprintf(`query { result:translation(by:{namespacePath:{namespace:%q,name:%q}}) %s }`,
				source.Namespace, source.Name, translationPublicStatusSelection)
			for _, actor := range []*auth.Principal{
				{Subject: "unbound-human", AuthMethod: "static-users"},
				{Subject: "system:serviceaccount:unbound", ServiceAccountUID: uuid.NewString(), AuthMethod: "serviceaccount-jwt"},
			} {
				for _, deniedQuery := range []string{
					query,
					fmt.Sprintf(`query { result:node(id:%q){...on Translation %s}}`,
						mustEncodeNodeID(nodeKindTranslation, source.UID), translationPublicStatusSelection),
					fmt.Sprintf(`query { result:translations(namespace:%q,first:1){edges{node %s}}}`,
						source.Namespace, translationPublicStatusSelection),
				} {
					data, failures := managedContractRequest(t, root, actor, deniedQuery, nil)
					require.NotEmpty(t, failures)
					require.NotContains(t, fmt.Sprint(data, failures), target.UID)
				}
				deniedCtx := auth.ContextWithPrincipal(ctx, actor)
				_, err := root.Subscription().WatchTranslations(deniedCtx, &source.Namespace, nil, nil)
				require.Error(t, err)
				_, err = root.Subscription().WatchResources(deniedCtx, "Translation", &source.Namespace, nil, nil)
				require.Error(t, err)
			}
			store := root.store.(datastore.TranslationCapable).TranslationStore()
			deps := repositoryWatchResolverDeps(root.store, root.store.(datastore.ResourceWatchCapable).ResourceWatchJournal())
			deps.Registry = root.registry
			replica, err := NewResolver(deps)
			require.NoError(t, err)
			replica.translationActivation.Enabled = true
			watchCtx, stopWatch := context.WithCancel(auth.ContextWithPrincipal(ctx, controller))
			t.Cleanup(stopWatch)
			bootstrap := watchjournal.BootstrapCursor
			stream, err := replica.Subscription().WatchResources(watchCtx, "Translation", &source.Namespace, nil, &bootstrap)
			require.NoError(t, err)
			require.Equal(t, model.WatchEventTypeBookmark, receiveGenericRepositoryEvent(t, stream).Type)
			source, err = store.UpdateTranslationStatus(ctx, source.UID, source.ResourceVersion, source.Status)
			require.NoError(t, err)
			event := receiveGenericRepositoryEvent(t, stream)
			require.Equal(t, false, event.Object["historical"])
			status := event.Object["status"].(map[string]any)
			require.Equal(t, "TRUE", status["conditions"].([]any)[0].(map[string]any)["status"])
			stopWatch()
			for _, route := range []string{"typed", "generic"} {
				input := map[string]any{
					"namespace": source.Namespace, "name": source.Name,
					"uid": mustEncodeNodeID(nodeKindTranslation, source.UID), "resourceVersion": source.ResourceVersion,
					"observedGeneration": source.Generation, "lastAppliedRevision": source.Revision,
					"conditions": []any{map[string]any{"type": "Ready", "status": "TRUE",
						"observedGeneration": source.Generation, "lastTransitionTime": source.CreationTimestamp.Format("2006-01-02T15:04:05.000Z07:00")}},
				}
				mutation := `mutation($input:UpdateTranslationStatusInput!){
					result:updateTranslationStatus(input:$input){translation {id}}}`
				if route == "generic" {
					input["kind"] = "Translation"
					mutation = `mutation($input:UpdateResourceStatusInput!){result:updateResourceStatus(input:$input){object}}`
				}
				for _, actor := range []*auth.Principal{
					{Subject: "translation-status-human", AuthMethod: "static-users"},
					{Subject: controller.Subject, ServiceAccountUID: uuid.NewString(), AuthMethod: controller.AuthMethod},
				} {
					_, failures := managedContractRequest(t, root, actor, mutation, input)
					require.NotEmpty(t, failures, "policy grants do not prove current controller enrollment")
					stored, err := store.GetTranslation(ctx, source.UID)
					require.NoError(t, err)
					require.Equal(t, source, stored)
				}
				_, failures := managedContractRequest(t, root, controller, mutation, input)
				require.Empty(t, failures)
				latest, err := store.GetTranslation(ctx, source.UID)
				require.NoError(t, err)
				source = latest
			}
			storedTarget, err := root.store.GetProduct(ctx, target.UID)
			require.NoError(t, err)
			require.Equal(t, target, storedTarget)
			if dynamic, ok := provider.(*opa.Provider); ok {
				dynamic.InvalidateTier(source.NamespaceID)
				_, failures := managedContractRequest(t, root, controller, query, nil)
				require.NotEmpty(t, failures, "loaded cluster grants cannot bypass unavailable namespace authority")
			}
		})
	}
}

func TestTranslationCanonicalActions(t *testing.T) {
	for _, verb := range []string{"create", "read", "list", "watch", "update", "delete", "status.write"} {
		action, ok := auth.LookupAction("translation." + verb)
		require.True(t, ok)
		require.Equal(t, "translation", action.Kind)
		require.Equal(t, "namespace", action.Scope)
		require.False(t, action.Reserved)
	}
	_, legacy := auth.LookupAction("translation.updateStatus")
	require.False(t, legacy)
}

func TestTranslationRoutesFailClosedBeforeActivation(t *testing.T) {
	r := &Resolver{}
	check := func(value any, err error) {
		t.Helper()
		require.Nil(t, value)
		var failure *gqlerror.Error
		require.ErrorAs(t, err, &failure)
		require.Equal(t, "FAILED_PRECONDITION", failure.Extensions["code"])
	}
	result, err := (&queryResolver{r}).Translation(t.Context(), model.TranslationBy{})
	check(result, err)
	page, err := (&queryResolver{r}).Translations(t.Context(), "acme", nil, nil, nil, nil)
	check(page, err)
	updated, err := (&mutationResolver{r}).UpdateTranslationStatus(t.Context(), model.UpdateTranslationStatusInput{})
	check(updated, err)
	stream, err := (&subscriptionResolver{r}).WatchTranslations(t.Context(), nil, nil, nil)
	check(stream, err)
	node, err := (&queryResolver{r}).resolveNode(t.Context(), nodeKindTranslation, "retained-uid")
	check(node, err)
}

func TestTranslationControllerEnrollmentWorksWithoutDynamicRBAC(t *testing.T) {
	fixture, human, source := newTranslationReadFixture(t)
	controller := translationEnrolledController(t, fixture)
	provider := translationConfiguredProvider(t, "rbac-local", source)
	root, err := NewResolver(ResolverDeps{
		Store: fixture.store, Logger: zap.NewNop(), Registry: auth.NewProviderRegistry(nil, provider, nil),
		TranslationActivation:        cataloggrpc.TranslationActivation{Enabled: true, DescriptorDigest: translation.CapabilityDigest()},
		TranslationControllerSubject: controller.Subject,
	})
	require.NoError(t, err)
	require.Nil(t, root.rbacValidator, "static policy must not initialize the dynamic RBAC writer")
	ctx := auth.ContextWithPrincipal(t.Context(), controller)
	require.NoError(t, root.requireTranslationController(ctx))
	require.NoError(t, root.authorizeTranslationSource(ctx, "status.write", source.Namespace, source.Name, ""))
	require.Error(t, root.requireTranslationController(human))
	forged := *controller
	forged.ServiceAccountUID = uuid.NewString()
	require.Error(t, root.requireTranslationController(auth.ContextWithPrincipal(t.Context(), &forged)))
	require.NoError(t, root.store.SetServiceAccountDisabled(ctx, controller.ServiceAccountUID, true))
	require.Error(t, root.requireTranslationController(ctx), "policy permission cannot replace live enrollment")
}

func TestTranslationResolverRequiresPinnedCapability(t *testing.T) {
	fixture, _, _ := newTranslationReadFixture(t)
	for _, activation := range []cataloggrpc.TranslationActivation{
		{Enabled: true},
		{DescriptorDigest: "unsupported-descriptors"},
	} {
		result, err := NewResolver(ResolverDeps{
			Store: fixture.store, Logger: zap.NewNop(), TranslationActivation: activation,
		})
		require.Nil(t, result)
		require.Error(t, err, "read activation must require a compatible explicit capability pin")
	}
	for _, enabled := range []bool{false, true} {
		result, err := NewResolver(ResolverDeps{
			Store: fixture.store, Logger: zap.NewNop(),
			TranslationActivation: cataloggrpc.TranslationActivation{Enabled: enabled, DescriptorDigest: translation.CapabilityDigest()},
		})
		require.NoError(t, err)
		require.Equal(t, enabled, result.translationActivation.Enabled)
	}
}

func TestTranslationRollingReadersRefuseIncapableRollback(t *testing.T) {
	fixture, ctx, source := newTranslationReadFixture(t)
	makeReader := func(activation cataloggrpc.TranslationActivation) *Resolver {
		reader, err := NewResolver(ResolverDeps{
			Store: fixture.store, Logger: zap.NewNop(),
			Registry:              auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil),
			TranslationActivation: activation,
		})
		require.NoError(t, err)
		return reader
	}
	by := model.TranslationBy{NamespacePath: &model.TranslationNamespacePath{Namespace: source.Namespace, Name: source.Name}}
	accepted, err := (&queryResolver{fixture}).Translation(ctx, by)
	require.NoError(t, err)
	for _, activation := range []cataloggrpc.TranslationActivation{
		{}, {DescriptorDigest: translation.CapabilityDigest()},
	} {
		reader := makeReader(activation)
		check := func(value any, err error) {
			t.Helper()
			require.Nil(t, value)
			var failure *gqlerror.Error
			require.ErrorAs(t, err, &failure)
			require.Equal(t, "FAILED_PRECONDITION", failure.Extensions["code"],
				"an incapable reader cannot silently hide an accepted kind during rollback")
		}
		value, err := (&queryResolver{reader}).Translation(ctx, by)
		check(value, err)
		page, err := (&queryResolver{reader}).Translations(ctx, source.Namespace, nil, nil, nil, nil)
		check(page, err)
		node, err := (&queryResolver{reader}).resolveNode(ctx, nodeKindTranslation, source.UID)
		check(node, err)
		stream, err := (&subscriptionResolver{reader}).WatchTranslations(ctx, &source.Namespace, nil, nil)
		check(stream, err)
	}
	capable := makeReader(cataloggrpc.TranslationActivation{Enabled: true, DescriptorDigest: translation.CapabilityDigest()})
	retained, err := (&queryResolver{capable}).Translation(ctx, by)
	require.NoError(t, err)
	require.Equal(t, accepted, retained, "capable replacement must retain the exact accepted source and guarded status")
	stored, err := fixture.store.(datastore.TranslationCapable).TranslationStore().GetTranslation(ctx, source.UID)
	require.NoError(t, err)
	require.Equal(t, source, stored, "rejected rollback readers cannot modify retained source or observations")
}

func TestTranslationGenericBackendsRemainInactive(t *testing.T) {
	r := &Resolver{}
	updated, err := (&mutationResolver{r}).UpdateResourceStatus(t.Context(), model.UpdateResourceStatusInput{Kind: "Translation"})
	require.Nil(t, updated)
	var failure *gqlerror.Error
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "FAILED_PRECONDITION", failure.Extensions["code"])

	stream, err := (&subscriptionResolver{r}).WatchResources(t.Context(), "Translation", nil, nil, nil)
	require.Nil(t, stream)
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "FAILED_PRECONDITION", failure.Extensions["code"])
}
