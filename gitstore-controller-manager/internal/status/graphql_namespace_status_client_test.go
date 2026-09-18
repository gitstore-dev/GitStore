// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package status_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

func namespaceTestKey() types.WorkItemKey {
	return types.WorkItemKey{Kind: "Namespace", Name: "acme"}
}

func namespaceTestPatch() *status.StatusPatch {
	gen := int64(3)
	now := time.Now()
	return &status.StatusPatch{
		ResourceVersion:    "1",
		ObservedGeneration: &gen,
		Conditions: []*status.Condition{{
			Type:               "AdmissionAccepted",
			Status:             "TRUE",
			ObservedGeneration: gen,
			LastTransitionTime: now,
		}},
	}
}

func TestGraphQLNamespaceStatusClient_SendsUpdateNamespaceStatusMutation(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"updateNamespaceStatus":{"namespace":{"metadata":{"resourceVersion":"2"}}}}}`))
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("test-token"))
	sc := status.NewGraphQLNamespaceStatusClient(client)
	patch := namespaceTestPatch()

	if err := sc.Apply(context.Background(), namespaceTestKey(), patch); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	query, _ := gotBody["query"].(string)
	if !strings.Contains(query, "updateNamespaceStatus") {
		t.Errorf("query = %q, want it to call updateNamespaceStatus", query)
	}

	vars, ok := gotBody["variables"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing 'variables', got %+v", gotBody)
	}
	input, ok := vars["input"].(map[string]any)
	if !ok {
		t.Fatalf("variables missing 'input', got %+v", vars)
	}
	if input["name"] != "acme" {
		t.Errorf("input name = %v, want acme", input["name"])
	}
	if _, hasNamespace := input["namespace"]; hasNamespace {
		t.Errorf("input must not include a namespace field for cluster-scoped Namespace, got %+v", input)
	}
	if input["resourceVersion"] != "1" {
		t.Errorf("input resourceVersion = %v, want %q", input["resourceVersion"], "1")
	}

	conds, ok := input["conditions"].([]any)
	if !ok || len(conds) != 1 {
		t.Fatalf("input conditions = %+v, want one condition", input["conditions"])
	}
}

func TestGraphQLNamespaceStatusClient_ConflictExtensionMapsToErrConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"resource version conflict","extensions":{"code":"RESOURCE_VERSION_CONFLICT","resourceVersion":"5"}}]}`))
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("test-token"))
	sc := status.NewGraphQLNamespaceStatusClient(client)

	err := sc.Apply(context.Background(), namespaceTestKey(), namespaceTestPatch())
	if !errors.Is(err, types.ErrConflict) {
		t.Fatalf("Apply err = %v, want errors.Is(..., types.ErrConflict)", err)
	}
}

func TestGraphQLNamespaceStatusClient_NotFoundExtensionMapsToErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"Namespace acme not found","extensions":{"code":"NOT_FOUND"}}]}`))
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("test-token"))
	sc := status.NewGraphQLNamespaceStatusClient(client)

	err := sc.Apply(context.Background(), namespaceTestKey(), namespaceTestPatch())
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("Apply err = %v, want errors.Is(..., types.ErrNotFound)", err)
	}
}

