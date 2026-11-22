// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package translation

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/schemavalidate"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/require"
)

func TestGraphQLClientOperationsMatchSchema(t *testing.T) {
	schemavalidate.Validate(t, []schemavalidate.Operation{
		{Name: "translationQuery", Query: translationQuery},
		{Name: "translationByIDQuery", Query: translationByIDQuery},
		{Name: "translationStatusMutation", Query: translationStatusMutation},
		{Name: "translationBindingQuery", Query: translationBindingQuery},
		{Name: "bindTranslationTargetMutation", Query: bindTranslationTargetMutation},
		{Name: "scanTranslationDependentsQuery", Query: scanTranslationDependentsQuery},
		{Name: "translationValidationSnapshotQuery", Query: translationValidationSnapshotQuery},
		{Name: "translationTargetObservationQuery", Query: translationTargetObservationQuery},
	})
}

func TestGraphQLClientValidationSnapshot(t *testing.T) {
	for _, invalid := range []string{"", "absent", "source", "version", "binding", "binding-namespace", "namespace", "target", "fields", "omitted", "observation"} {
		t.Run(invalid, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Variables map[string]any `json:"variables"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				require.Equal(t, "source-ID", request.Variables["sourceID"])
				require.Equal(t, "7", request.Variables["sourceVersion"])
				binding := map[string]any{"translationID": "source-ID", "sourceVersion": "7", "bindingVersion": "9", "referenceEpoch": "epoch", "namespaceUID": "namespace-UID"}
				evidence := map[string]any{"apiVersion": "catalog.gitstore.dev/v1beta1", "kind": "Product",
					"namespace": "shop", "name": "laptop", "uid": "target-UID", "generation": 2,
					"resourceVersion": "4", "revision": "target-revision", "contentDigest": "material"}
				target := map[string]any{"namespace": "shop", "evidence": evidence, "fields": map[string]any{"title": nil}}
				snapshot := map[string]any{"sourceID": "source-ID", "sourceVersion": "7", "sourceDigest": "source-digest",
					"namespaceUID": "namespace-UID", "binding": binding, "target": target}
				condition := map[string]any{"type": "Ready", "status": "TRUE", "observedGeneration": 2,
					"lastTransitionTime": "2026-01-01T00:00:00Z"}
				snapshot["observation"] = map[string]any{"observedGeneration": 2, "lastAppliedRevision": "accepted-source",
					"conditions": []any{condition}}
				switch invalid {
				case "absent":
					snapshot["target"] = nil
				case "source":
					snapshot["sourceID"] = "replacement"
				case "version":
					snapshot["sourceVersion"] = "8"
				case "binding":
					binding["sourceVersion"] = "6"
				case "binding-namespace":
					binding["namespaceUID"] = "recreated-namespace"
				case "namespace":
					evidence["namespace"] = "other"
				case "target":
					evidence["uid"] = ""
				case "fields":
					target["fields"] = nil
				case "omitted":
					delete(snapshot, "target")
				case "observation":
					condition["status"] = "True"
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
					"translationValidationSnapshot": snapshot,
				}}))
			}))
			defer server.Close()
			client := NewGraphQLClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("token")))
			snapshot, err := client.ValidationSnapshot(t.Context(),
				types.WorkItemKey{Kind: "Translation", Namespace: "shop", Name: "fr"}, "source-ID", "7")
			if invalid != "" && invalid != "absent" {
				require.Error(t, err)
				require.Nil(t, snapshot)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "source-digest", snapshot.SourceDigest)
			require.Equal(t, "7", snapshot.Observation.ResourceVersion)
			require.Equal(t, "TRUE", snapshot.Observation.Conditions[0].Status)
			require.Equal(t, "accepted-source", snapshot.Observation.LastAppliedRevision)
			if invalid == "absent" {
				require.Nil(t, snapshot.Target, "authorized absence must survive the API client boundary")
				return
			}
			require.Equal(t, "namespace-UID", snapshot.Target.Observation.Target.NamespaceUID)
			require.Equal(t, "target-UID", snapshot.Target.Observation.Target.UID)
			require.Contains(t, snapshot.Target.Fields, "title")
			require.Nil(t, snapshot.Target.Fields["title"])
			require.NotContains(t, snapshot.Target.Fields, "media")
		})
	}
}

func TestGraphQLClientTargetObservationRejectsIncompleteOrMisdirectedProof(t *testing.T) {
	for _, invalid := range []string{"", "omitted", "null", "namespace", "scope", "uid", "digest", "kind", "name", "version"} {
		t.Run(invalid, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				evidence := map[string]any{"apiVersion": "catalog.gitstore.dev/v1beta1", "kind": "Product",
					"namespace": "shop", "name": "laptop", "uid": "raw-target-UID", "generation": 2,
					"resourceVersion": "4", "revision": "target-revision", "contentDigest": "material"}
				observation := map[string]any{"namespaceUID": "namespace-UID", "evidence": evidence}
				response := map[string]any{"translationTargetObservation": observation}
				switch invalid {
				case "omitted":
					delete(response, "translationTargetObservation")
				case "null":
					observation["evidence"] = nil
				case "namespace":
					evidence["namespace"] = "other"
				case "scope":
					observation["namespaceUID"] = ""
				case "uid":
					evidence["uid"] = ""
				case "digest":
					evidence["contentDigest"] = ""
				case "kind":
					evidence["kind"] = "Collection"
				case "name":
					evidence["name"] = "other"
				case "version":
					evidence["resourceVersion"] = ""
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": response}))
			}))
			defer server.Close()
			client := NewGraphQLClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("token")))
			observed, err := client.ObserveTarget(t.Context(), "shop", "catalog.gitstore.dev/v1beta1", "Product", "laptop")
			if invalid != "" {
				require.Error(t, err)
				require.Nil(t, observed)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "raw-target-UID", observed.Target.UID)
			require.Equal(t, "namespace-UID", observed.Target.NamespaceUID)
			require.Equal(t, "material", observed.ContentDigest)
		})
	}
}

func TestGraphQLClientBindingAndDependentOperations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		field := "translationBinding"
		binding := map[string]any{"translationID": "source-ID", "sourceVersion": "7", "bindingVersion": "9", "referenceEpoch": "epoch", "namespaceUID": "namespace-UID"}
		if strings.Contains(request.Query, "bindTranslationTarget(") {
			field = "bindTranslationTarget"
			input := request.Variables["input"].(map[string]any)
			require.Equal(t, "source-ID", input["translationID"])
			require.Equal(t, "7", input["sourceVersion"])
			require.Equal(t, "9", input["bindingVersion"])
			require.Equal(t, "epoch", input["referenceEpoch"])
			require.NotContains(t, input, "epochs", "the API, not the client, owns historical binding preservation")
			binding["boundUID"] = "target-UID"
			binding["bindingVersion"] = "10"
		}
		if strings.Contains(request.Query, "scanTranslationDependents(") {
			target := request.Variables["target"].(map[string]any)
			require.Equal(t, "namespace-UID", target["namespaceUID"])
			require.Equal(t, "target-UID", target["uid"])
			require.Equal(t, float64(1), request.Variables["limit"])
			require.Equal(t, "prior", request.Variables["continuation"])
			require.NotContains(t, request.Variables, "first")
			require.NotContains(t, request.Variables, "after")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"scanTranslationDependents": map[string]any{"items": []any{
					map[string]any{"translationID": "source-ID", "sourceVersion": "7", "referenceEpoch": "epoch"},
				}, "continuation": "next"},
			}}))
			return
		}
		value := binding
		if field == "bindTranslationTarget" {
			value = map[string]any{"binding": binding}
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{field: value}}))
	}))
	defer server.Close()
	client := NewGraphQLClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("token")))
	state, err := client.GetBinding(t.Context(), "shop", "fr")
	require.NoError(t, err)
	require.Nil(t, state.BoundUID)
	bound, err := client.BindTarget(t.Context(), "shop", "fr", state, "target-UID")
	require.NoError(t, err)
	require.Equal(t, "target-UID", *bound.BoundUID)
	scan, err := client.ScanDependents(t.Context(), "shop", DependencyTarget{
		NamespaceUID: "namespace-UID", APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "target", UID: "target-UID",
	}, "prior", 1)
	require.NoError(t, err)
	require.Len(t, scan.Items, 1)
	require.Equal(t, "next", scan.Continuation)
	require.Equal(t, "source-ID", scan.Items[0].TranslationID)
}

func TestGraphQLClientDependentScanProgress(t *testing.T) {
	for _, scenario := range []string{"empty-progress", "exhausted", "stuck", "legacy-response"} {
		t.Run(scenario, func(t *testing.T) {
			turns := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Variables map[string]any `json:"variables"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				require.Equal(t, float64(1), request.Variables["limit"])
				var continuation any
				if turns == 0 {
					require.NotContains(t, request.Variables, "continuation")
					if scenario == "empty-progress" || scenario == "stuck" {
						continuation = "progress"
					}
				} else {
					require.Equal(t, "progress", request.Variables["continuation"])
					if scenario == "stuck" {
						continuation = "progress"
					}
				}
				turns++
				response := map[string]any{"scanTranslationDependents": map[string]any{
					"items": []any{}, "continuation": continuation,
				}}
				if scenario == "legacy-response" {
					response = map[string]any{"translationDependents": map[string]any{"items": []any{}, "next": nil}}
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": response}))
			}))
			defer server.Close()
			client := NewGraphQLClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("token")))
			target := DependencyTarget{NamespaceUID: "namespace-UID", APIVersion: "catalog.gitstore.dev/v1beta1",
				Kind: "Product", Name: "target", UID: "target-UID"}
			scan, err := client.ScanDependents(t.Context(), "shop", target, "", 1)
			if scenario == "legacy-response" {
				require.Error(t, err, "the old result must not be mistaken for an exhausted scan")
				return
			}
			require.NoError(t, err)
			require.Empty(t, scan.Items)
			if scenario == "exhausted" {
				require.Empty(t, scan.Continuation)
				return
			}
			require.Equal(t, "progress", scan.Continuation)
			scan, err = client.ScanDependents(t.Context(), "shop", target, scan.Continuation, 1)
			if scenario == "stuck" {
				require.Error(t, err, "empty batches must still advance")
				return
			}
			require.NoError(t, err)
			require.Empty(t, scan.Items)
			require.Empty(t, scan.Continuation, "only an exhausted continuation terminates the scan")
		})
	}
}

func TestGraphQLClientRejectsMissingOrUnwrappedBindingPayload(t *testing.T) {
	for _, shape := range []string{"null-payload", "missing-binding", "unwrapped-binding"} {
		t.Run(shape, func(t *testing.T) {
			observed := &BindingState{
				TranslationID: "source-ID", SourceVersion: "7", BindingVersion: "9",
				ReferenceEpoch: "epoch", NamespaceUID: "namespace-UID",
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				var payload any
				switch shape {
				case "missing-binding":
					payload = map[string]any{}
				case "unwrapped-binding":
					bound := *observed
					uid := "target-UID"
					bound.BoundUID = &uid
					payload = bound
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"bindTranslationTarget": payload}}))
			}))
			defer server.Close()
			client := NewGraphQLClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("token")))
			bound, err := client.BindTarget(t.Context(), "shop", "fr", observed, "target-UID")
			require.Error(t, err)
			require.Nil(t, bound)
		})
	}
}

func TestGraphQLClientPreservesSourceAndStatusBoundaries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer controller-token", r.Header.Get("Authorization"))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"translation": map[string]any{
				"metadata": map[string]any{"uid": "uid", "namespace": "shop", "name": "fr",
					"resourceVersion": "7", "generation": 3, "revision": "new-source"},
				"spec": map[string]any{"locale": "fr-CA", "targetRef": map[string]any{"name": "Product-ID"}},
				"body": "  # Bonjour\r\n\r\n",
				"status": map[string]any{"lastAppliedRevision": "old-source",
					"conditions": []any{map[string]any{"type": "Ready", "status": "UNKNOWN"}}},
			},
		}}))
	}))
	defer server.Close()
	client := NewGraphQLClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("controller-token")))
	resource, err := client.Get(t.Context(), "shop", "fr")
	require.NoError(t, err)
	require.Equal(t, "new-source", resource.Metadata.Revision)
	require.Equal(t, "old-source", resource.Status.LastAppliedRevision)
	require.Equal(t, "7", resource.Status.ResourceVersion)
	require.Equal(t, "UNKNOWN", resource.Status.Conditions[0].Status)
	require.Equal(t, "  # Bonjour\r\n\r\n", resource.Body)
	require.Contains(t, string(resource.Spec), "Product-ID")
	byID, err := client.GetByID(t.Context(), "uid")
	require.NoError(t, err)
	require.Equal(t, resource, byID)
	var invalid Resource
	require.Error(t, json.Unmarshal([]byte(`{"status":{"conditions":[{"status":"Unknown"}]}}`), &invalid),
		"YAML enum spellings must not be accepted as GraphQL aliases")
}

func TestGraphQLClientStatusCASAndFailureMapping(t *testing.T) {
	for _, code := range []string{"", "CONFLICT", "NOT_FOUND", "FORBIDDEN"} {
		t.Run(code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Variables struct {
						Input map[string]any `json:"input"`
					} `json:"variables"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				input := request.Variables.Input
				require.Equal(t, "source-lifetime-id", input["uid"])
				require.Equal(t, "7", input["resourceVersion"])
				require.Equal(t, "authored-revision", input["lastAppliedRevision"])
				require.Equal(t, float64(3), input["observedGeneration"])
				require.Equal(t, "UNKNOWN", input["conditions"].([]any)[0].(map[string]any)["status"])
				require.NotContains(t, input, "spec")
				require.NotContains(t, input, "body")
				if code != "" {
					require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"errors": []any{
						map[string]any{"message": "operation failed", "extensions": map[string]any{"code": code}},
					}}))
					return
				}
				_, _ = w.Write([]byte(`{"data":{"updateTranslationStatus":{"translation":{"metadata":{"resourceVersion":"8"}}}}}`))
			}))
			defer server.Close()
			client := NewGraphQLClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("token")))
			generation, revision := int64(3), "authored-revision"
			err := client.Apply(t.Context(), types.WorkItemKey{Kind: "Translation", Namespace: "shop", Name: "fr"},
				&status.StatusPatch{UID: "source-lifetime-id", ResourceVersion: "7", ObservedGeneration: &generation, LastAppliedRevision: &revision,
					Conditions: []*status.Condition{{Type: "Ready", Status: "UNKNOWN"}}})
			switch code {
			case "":
				require.NoError(t, err)
			case "CONFLICT":
				require.ErrorIs(t, err, types.ErrConflict)
			case "NOT_FOUND":
				require.ErrorIs(t, err, types.ErrNotFound)
			default:
				require.Error(t, err)
				require.False(t, errors.Is(err, types.ErrNotFound))
				require.False(t, errors.Is(err, types.ErrConflict))
			}
		})
	}
}

func TestGraphQLClientRejectsMalformedStatusBeforeRequest(t *testing.T) {
	client := NewGraphQLClient(graphqlclient.New("http://unused.invalid", graphqlclient.NewStaticToken("token")))
	for _, patch := range []*status.StatusPatch{nil, {}, {
		ResourceVersion: "1", ObservedGeneration: new(int64(1)), LastAppliedRevision: new("revision"),
		Conditions: []*status.Condition{{Status: "TRUE"}},
	}, {
		UID:             "source-lifetime-id",
		ResourceVersion: "1", ObservedGeneration: new(int64(1)), LastAppliedRevision: new("revision"),
		Conditions: []*status.Condition{{Status: "True"}},
	}} {
		require.Error(t, client.Apply(t.Context(), types.WorkItemKey{Kind: "Translation", Namespace: "shop", Name: "fr"}, patch))
	}
}
