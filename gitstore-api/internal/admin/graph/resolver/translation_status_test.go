// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gitstore-dev/gitstore/observability/observabilitytest"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTranslationStatusRouteFixture(t *testing.T) (*Resolver, context.Context, *datastore.Translation, *datastore.Product) {
	t.Helper()
	root, ctx, source := newTranslationReadFixture(t)
	target := &datastore.Product{
		UID: uuid.NewString(), Namespace: source.Namespace, Name: "laptop",
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product",
		CreationActor: "target-owner", Generation: 1, ResourceVersion: "1", Revision: "target-revision",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond), Spec: json.RawMessage(`{"title":"Laptop"}`),
	}
	require.NoError(t, root.store.CreateProduct(ctx, target))
	evidence, err := root.store.(datastore.TranslationTargetFenceStore).ReadTargetEvidence(ctx, datastore.TranslationTargetKey{
		NamespaceUID: source.NamespaceID, APIVersion: target.APIVersion, Kind: target.Kind, Name: target.Name,
	})
	require.NoError(t, err)
	store := root.store.(datastore.TranslationCapable).TranslationStore()
	expected := source.ResourceVersion
	source.ResourceVersion = nextTranslationRouteVersion(t, expected)
	source.SourceDigest = "private-route-source-digest"
	require.NoError(t, store.UpdateTranslation(ctx, source, expected))
	descriptor, err := translation.DescriptorFor(target.APIVersion, target.Kind)
	require.NoError(t, err)
	status, err := json.Marshal(catalog.TranslationStatus{
		ObservedGeneration: source.Generation, LastAppliedRevision: source.Revision,
		Conditions: []catalog.Condition{
			{Type: catalog.ConditionReady, Status: catalog.ConditionTrue, ObservedGeneration: source.Generation,
				LastTransitionTime: source.CreationTimestamp},
			{Type: "TargetResolved", Status: catalog.ConditionTrue, ObservedGeneration: source.Generation,
				LastTransitionTime: source.CreationTimestamp, Message: "private-target-validation-detail"},
		},
		Resolved: &catalog.ResolvedTranslation{
			CanonicalLocale: "he-IL", Target: evidence, ValidatedFieldPaths: []string{"spec.title"},
			SourceDigest: source.SourceDigest, DescriptorDigest: descriptor.Digest,
		},
	})
	require.NoError(t, err)
	source, err = store.UpdateTranslationStatus(ctx, source.UID, source.ResourceVersion, status)
	require.NoError(t, err)
	return root, ctx, source, target
}

func nextTranslationRouteVersion(t *testing.T, version string) string {
	t.Helper()
	next, err := datastore.AdvanceTranslationIndexVersion(version)
	require.NoError(t, err)
	return next
}

type translationUnavailableProofStore struct {
	*datastore.InstrumentedDatastore
}

func (s translationUnavailableProofStore) GetTranslationTargetFence(context.Context, datastore.TranslationTargetKey) (*datastore.TranslationTargetFence, error) {
	return nil, datastore.ErrRepairRequired
}

const translationPublicStatusSelection = `{
	metadata { resourceVersion revision generation }
	status {
		observedGeneration lastAppliedRevision
		conditions { type status reason message lastTransitionTime observedGeneration }
		resolved { canonicalLocale validatedFieldPaths target {
			apiVersion kind namespace name uid generation revision resourceVersion contentDigest
		} }
	}
}`

func translationStatusReadRoute(t *testing.T, root *Resolver, principal *auth.Principal, source *datastore.Translation, route string) map[string]any {
	t.Helper()
	id := mustEncodeNodeID(nodeKindTranslation, source.UID)
	switch route {
	case "named":
		data, failures := managedContractRequest(t, root, principal, fmt.Sprintf(`query {
			result: translation(by:{namespacePath:{namespace:%q,name:%q}}) %s
		}`, source.Namespace, source.Name, translationPublicStatusSelection), nil)
		require.Empty(t, failures)
		return data["result"]
	case "node":
		data, failures := managedContractRequest(t, root, principal, fmt.Sprintf(`query {
			result: node(id:%q) { ... on Translation %s }
		}`, id, translationPublicStatusSelection), nil)
		require.Empty(t, failures)
		return data["result"]
	case "list":
		after := ""
		for range 16 {
			data, failures := managedContractRequest(t, root, principal, fmt.Sprintf(`query {
				result: translations(namespace:%q,first:256,after:%q) {
					edges { node %s } pageInfo { hasNextPage endCursor }
				}
			}`, source.Namespace, after, translationPublicStatusSelection), nil)
			require.Empty(t, failures)
			page := data["result"]
			if edges := page["edges"].([]any); len(edges) > 0 {
				require.Len(t, edges, 1)
				return edges[0].(map[string]any)["node"].(map[string]any)
			}
			info := page["pageInfo"].(map[string]any)
			require.Equal(t, true, info["hasNextPage"], "source disappeared from its bounded list")
			next := info["endCursor"].(string)
			require.NotEqual(t, after, next)
			after = next
		}
	}
	t.Fatalf("no status from route %s", route)
	return nil
}

func TestTranslationStatusReadRoutesGuardEnumsAndPrivateEvidence(t *testing.T) {
	for _, scenario := range []string{"current", "material-changed", "target-denied", "target-status-only", "unavailable"} {
		for _, route := range []string{"named", "node", "list"} {
			t.Run(scenario+"/"+route, func(t *testing.T) {
				root, ctx, source, target := newTranslationStatusRouteFixture(t)
				want, reason := "TRUE", ""
				switch scenario {
				case "material-changed":
					next := *target
					next.Generation, next.ResourceVersion, next.Spec = 2, "2", json.RawMessage(`{"title":"Changed"}`)
					require.NoError(t, root.store.UpdateProduct(ctx, &next, target.ResourceVersion))
					want, reason = "FALSE", "StaleEvidence"
				case "target-denied":
					root.registry = auth.NewProviderRegistry(nil, &translationControllerAuthorizer{
						AuthZProvider: root.registry.AuthZ(), denyTarget: true,
					}, nil)
					want, reason = "UNKNOWN", "Forbidden"
				case "target-status-only":
					_, err := root.store.UpdateProductStatus(ctx, target.Namespace, target.Name,
						datastore.ProductStatusPatch{ResourceVersion: target.ResourceVersion})
					require.NoError(t, err)
				case "unavailable":
					root.store = translationUnavailableProofStore{
						datastore.NewInstrumentedDatastore(root.store, "contract", zap.NewNop()).(*datastore.InstrumentedDatastore),
					}
					want, reason = "UNKNOWN", "Unavailable"
				}

				value := translationStatusReadRoute(t, root, auth.PrincipalFromContext(ctx), source, route)
				require.NotNil(t, value["status"], "the service must return guarded status, not hide every observation")
				status := value["status"].(map[string]any)
				require.Len(t, status, 4)
				require.Equal(t, source.Revision, status["lastAppliedRevision"])
				for _, item := range status["conditions"].([]any) {
					condition := item.(map[string]any)
					require.Contains(t, []string{"TRUE", "FALSE", "UNKNOWN"}, condition["status"])
					if condition["type"] == "Ready" {
						require.Equal(t, want, condition["status"])
						if reason != "" {
							require.Equal(t, reason, condition["reason"])
						}
					}
				}
				raw, err := json.Marshal(value)
				require.NoError(t, err)
				require.NotContains(t, string(raw), "sourceDigest")
				require.NotContains(t, string(raw), "descriptorDigest")
				require.NotContains(t, string(raw), source.SourceDigest)
				if want == "TRUE" {
					resolved := status["resolved"].(map[string]any)
					require.Equal(t, "he-IL", resolved["canonicalLocale"])
					require.Equal(t, "1", resolved["target"].(map[string]any)["resourceVersion"],
						"status-only target churn must not relabel an older matching observation")
				} else {
					require.Nil(t, status["resolved"])
					require.NotContains(t, string(raw), "private-target-validation-detail")
					require.NotContains(t, string(raw), target.UID)
				}
				stored, err := root.store.(datastore.TranslationCapable).TranslationStore().GetTranslation(ctx, source.UID)
				require.NoError(t, err)
				require.Equal(t, source, stored, "read masking and revalidation cannot alter the persisted resource")
			})
		}
	}
}

type translationStatusWriteInterleaving struct {
	datastore.TranslationStore
	after func()
}

func (s translationStatusWriteInterleaving) UpdateTranslationStatus(ctx context.Context, uid, version string, status json.RawMessage) (*datastore.Translation, error) {
	source, err := s.TranslationStore.UpdateTranslationStatus(ctx, uid, version, status)
	if err == nil {
		s.after()
	}
	return source, err
}

type translationStatusRouteStore struct {
	*datastore.InstrumentedDatastore
	sources datastore.TranslationStore
}

func (s translationStatusRouteStore) TranslationStore() datastore.TranslationStore { return s.sources }

func TestTranslationStatusWatchGuardsLiveAndKeepsReplayHistorical(t *testing.T) {
	for _, route := range []string{"typed", "generic"} {
		for _, scenario := range []string{"current", "material-changed", "target-denied", "source-changed", "unavailable"} {
			t.Run(route+"/"+scenario, func(t *testing.T) {
				root, ctx, source, target := newTranslationStatusRouteFixture(t)
				registry := prometheus.NewRegistry()
				readMetrics := newTranslationReadMetrics(observabilitytest.Meter(t, registry, "gitstore/translation/read"))
				journal := root.store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
				if scenario == "unavailable" {
					root.store = translationUnavailableProofStore{
						datastore.NewInstrumentedDatastore(root.store, "contract", zap.NewNop()).(*datastore.InstrumentedDatastore),
					}
				}
				if scenario == "material-changed" {
					next := *target
					next.Generation, next.ResourceVersion, next.Spec = 2, "2", json.RawMessage(`{"title":"New material"}`)
					require.NoError(t, root.store.UpdateProduct(ctx, &next, target.ResourceVersion))
				}
				if scenario == "target-denied" {
					root.registry = auth.NewProviderRegistry(nil, &translationControllerAuthorizer{
						AuthZProvider: root.registry.AuthZ(), denyTarget: true,
					}, nil)
				}
				if scenario == "source-changed" {
					store := root.store.(datastore.TranslationCapable).TranslationStore()
					root.registry = auth.NewProviderRegistry(nil, &translationControllerAuthorizer{
						AuthZProvider: root.registry.AuthZ(),
						onTarget: func() {
							next := *source
							next.ResourceVersion = nextTranslationRouteVersion(t, source.ResourceVersion)
							next.Generation++
							next.Body, next.SourceDigest = "new source during proof", "new-source-digest"
							require.NoError(t, store.UpdateTranslation(ctx, &next, source.ResourceVersion))
						},
					}, nil)
				}
				newReplica := func() *Resolver {
					deps := repositoryWatchResolverDeps(root.store, journal)
					deps.Registry = root.registry
					replica, err := NewResolver(deps)
					require.NoError(t, err)
					replica.translationActivation.Enabled = true
					replica.translationMetrics = readMetrics
					return replica
				}
				watch := func(ctx context.Context, replica *Resolver, cursor string) func() (string, bool, map[string]any) {
					if route == "typed" {
						stream, err := replica.Subscription().WatchTranslations(ctx, &source.Namespace, nil, &cursor)
						require.NoError(t, err)
						return func() (string, bool, map[string]any) {
							select {
							case event, open := <-stream:
								require.True(t, open)
								var value map[string]any
								if event.Translation != nil {
									value, err = translationToJSONMap(event.Translation)
									require.NoError(t, err)
								}
								return event.ResourceVersion, event.Historical, value
							case <-time.After(3 * time.Second):
								t.Fatal("Translation watch did not deliver")
								return "", false, nil
							}
						}
					}
					stream, err := replica.Subscription().WatchResources(ctx, "Translation", &source.Namespace, nil, &cursor)
					require.NoError(t, err)
					return func() (string, bool, map[string]any) {
						event := receiveGenericRepositoryEvent(t, stream)
						historical, _ := event.Object["historical"].(bool)
						return event.ResourceVersion, historical, event.Object
					}
				}
				liveCtx, stopLive := context.WithCancel(ctx)
				t.Cleanup(stopLive)
				receive := watch(liveCtx, newReplica(), watchjournal.BootstrapCursor)
				bookmark, _, empty := receive()
				require.Nil(t, empty)
				requested, err := root.store.(datastore.TranslationCapable).TranslationStore().RequestTranslationRevalidation(ctx, source)
				require.NoError(t, err)
				require.True(t, requested)
				cursor, historical, value := receive()
				require.NotEqual(t, bookmark, cursor)
				require.Equal(t, source.ResourceVersion, value["metadata"].(map[string]any)["resourceVersion"])
				require.Equal(t, string(source.Body), value["body"], "a source retry cannot replace the original event body")
				raw, err := json.Marshal(value)
				require.NoError(t, err)
				require.NotContains(t, string(raw), "sourceDigest")
				require.NotContains(t, string(raw), "descriptorDigest")
				require.NotContains(t, string(raw), source.SourceDigest)
				if scenario == "source-changed" {
					require.True(t, historical)
					require.NotContains(t, value, "status", "a newer guard result cannot be relabeled as the old source event")
				} else {
					require.False(t, historical, "an unchanged live source must use the current-status guard")
					status := value["status"].(map[string]any)
					want := map[string]string{"current": "TRUE", "material-changed": "FALSE", "target-denied": "UNKNOWN", "unavailable": "UNKNOWN"}[scenario]
					conditions := status["conditions"].([]any)
					require.NotEmpty(t, conditions)
					require.Equal(t, "Ready", conditions[0].(map[string]any)["type"])
					require.Equal(t, want, conditions[0].(map[string]any)["status"])
					if want != "TRUE" {
						require.Nil(t, status["resolved"])
						require.Len(t, conditions, 1, "mask other conditions containing target details")
						require.NotContains(t, string(raw), target.UID)
					}
				}
				stopLive()
				replayCtx, stopReplay := context.WithCancel(ctx)
				t.Cleanup(stopReplay)
				replayedCursor, historical, replay := watch(replayCtx, newReplica(), bookmark)()
				require.Equal(t, cursor, replayedCursor)
				require.True(t, historical)
				require.Equal(t, source.ResourceVersion, replay["metadata"].(map[string]any)["resourceVersion"])
				require.NotContains(t, replay, "status", "historical Ready must not masquerade as current success")
				families, err := registry.Gather()
				require.NoError(t, err)
				counts := map[string]float64{}
				for _, family := range families {
					if family.GetName() != "gitstore_translation_watch_events_total" {
						continue
					}
					for _, sample := range family.Metric {
						labels := map[string]string{}
						for _, label := range sample.Label {
							labels[label.GetName()] = label.GetValue()
						}
						require.Len(t, labels, 2)
						counts[labels["mode"]+"/"+labels["outcome"]] += sample.GetCounter().GetValue()
					}
				}
				require.GreaterOrEqual(t, counts["historical/converted"], float64(1))
				if scenario != "source-changed" {
					require.GreaterOrEqual(t, counts["live/converted"], float64(1))
				}
			})
		}
	}
}

func TestTranslationStatusMutationRejectsYAMLConditionAliases(t *testing.T) {
	for _, route := range []string{"typed", "generic"} {
		for _, alias := range []string{"True", "true", "Unknown"} {
			t.Run(route+"/"+alias, func(t *testing.T) {
				root, ctx, source, _ := newTranslationStatusRouteFixture(t)
				input := map[string]any{
					"namespace": source.Namespace, "name": source.Name,
					"uid": mustEncodeNodeID(nodeKindTranslation, source.UID), "resourceVersion": source.ResourceVersion,
					"observedGeneration": source.Generation, "lastAppliedRevision": source.Revision,
					"conditions": []any{map[string]any{"type": "Ready", "status": alias,
						"observedGeneration": source.Generation, "lastTransitionTime": source.CreationTimestamp.Format("2006-01-02T15:04:05.000Z07:00")}},
				}
				query := `mutation($input:UpdateTranslationStatusInput!) {
					result:updateTranslationStatus(input:$input) { translation { id } } }`
				if route == "generic" {
					input["kind"] = "Translation"
					query = `mutation($input:UpdateResourceStatusInput!) { result:updateResourceStatus(input:$input) { object } }`
				}
				_, failures := managedContractRequest(t, root, auth.PrincipalFromContext(ctx), query, input)
				require.NotEmpty(t, failures)
				require.Contains(t, fmt.Sprint(failures), "ConditionStatus", "reject the wire enum before entering authorization or mutation")
				stored, err := root.store.(datastore.TranslationCapable).TranslationStore().GetTranslation(ctx, source.UID)
				require.NoError(t, err)
				require.Equal(t, source, stored)
			})
		}
	}
}

func TestTranslationStatusMutationResponsesRecheckAfterCAS(t *testing.T) {
	for _, route := range []string{"typed", "generic"} {
		for _, scenario := range []string{"current", "changed-after-CAS", "target-denied", "unavailable"} {
			t.Run(route+"/"+scenario, func(t *testing.T) {
				root, ctx, source, target := newTranslationStatusRouteFixture(t)
				controller := &auth.Principal{Subject: "system:serviceaccount:gitstore-controller-manager",
					ServiceAccountUID: uuid.NewString(), AuthMethod: "serviceaccount-jwt"}
				root.rbacValidator = &admission.RBACValidator{ManagedStore: root.store, ControllerSubject: controller.Subject}
				require.NoError(t, root.store.CreateServiceAccount(ctx, &datastore.ServiceAccount{
					UID: controller.ServiceAccountUID, Name: "gitstore-controller-manager",
				}))
				want := "TRUE"
				if scenario == "unavailable" {
					root.store = translationUnavailableProofStore{
						datastore.NewInstrumentedDatastore(root.store, "contract", zap.NewNop()).(*datastore.InstrumentedDatastore),
					}
					want = "UNKNOWN"
				}
				if scenario == "target-denied" {
					root.registry = auth.NewProviderRegistry(nil, &translationControllerAuthorizer{
						AuthZProvider: root.registry.AuthZ(), denyTarget: true,
					}, nil)
					want = "UNKNOWN"
				}
				if scenario == "changed-after-CAS" {
					want = "FALSE"
					root.store = translationStatusRouteStore{
						InstrumentedDatastore: datastore.NewInstrumentedDatastore(root.store, "contract", zap.NewNop()).(*datastore.InstrumentedDatastore),
						sources: translationStatusWriteInterleaving{
							TranslationStore: root.store.(datastore.TranslationCapable).TranslationStore(),
							after: func() {
								next := *target
								next.Generation, next.ResourceVersion, next.Spec = 2, "2", json.RawMessage(`{"title":"Changed after status CAS"}`)
								require.NoError(t, root.store.UpdateProduct(ctx, &next, target.ResourceVersion))
							},
						},
					}
				}
				input := map[string]any{
					"namespace": source.Namespace, "name": source.Name,
					"uid": mustEncodeNodeID(nodeKindTranslation, source.UID), "resourceVersion": source.ResourceVersion,
					"observedGeneration": source.Generation, "lastAppliedRevision": source.Revision,
					"conditions": []any{map[string]any{"type": "Ready", "status": "TRUE",
						"observedGeneration": source.Generation, "lastTransitionTime": source.CreationTimestamp.Format("2006-01-02T15:04:05.000Z07:00")}},
				}
				query := `mutation($input:UpdateTranslationStatusInput!) {
					result:updateTranslationStatus(input:$input) { translation ` + translationPublicStatusSelection + ` } }`
				if route == "generic" {
					input["kind"] = "Translation"
					query = `mutation($input:UpdateResourceStatusInput!) { result:updateResourceStatus(input:$input) { object } }`
				}
				data, failures := managedContractRequest(t, root, controller, query, input)
				require.Empty(t, failures)
				key := "translation"
				if route == "generic" {
					key = "object"
				}
				value := data["result"][key].(map[string]any)
				status := value["status"].(map[string]any)
				for _, item := range status["conditions"].([]any) {
					condition := item.(map[string]any)
					require.Contains(t, []string{"TRUE", "FALSE", "UNKNOWN"}, condition["status"])
					if condition["type"] == "Ready" {
						require.Equal(t, want, condition["status"])
					}
				}
				raw, err := json.Marshal(value)
				require.NoError(t, err)
				require.NotContains(t, string(raw), "sourceDigest")
				require.NotContains(t, string(raw), "descriptorDigest")
				if want != "TRUE" {
					require.Nil(t, status["resolved"])
					require.NotContains(t, string(raw), "private-target-validation-detail")
					require.NotContains(t, string(raw), target.UID)
				}
				stored, err := root.store.(datastore.TranslationCapable).TranslationStore().GetTranslation(ctx, source.UID)
				require.NoError(t, err)
				var observed catalog.TranslationStatus
				require.NoError(t, json.Unmarshal(stored.Status, &observed))
				require.Equal(t, catalog.ConditionTrue, observed.Conditions[0].Status, "a masked CAS response cannot rewrite the stored observation")
				require.Equal(t, source.RawSource, stored.RawSource)
				require.Equal(t, source.Body, stored.Body)
			})
		}
	}
}
