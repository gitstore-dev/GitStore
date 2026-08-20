// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package status_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

func TestResourceStatusApplySendsKindAwareMutation(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"updateResourceStatus":{"object":{"metadata":{"resourceVersion":"3"}},"conflict":null}}}`))
	}))
	defer srv.Close()

	generation := int64(2)
	client := status.NewGraphQLResourceStatusClient(graphqlclient.New(srv.URL, "token"))
	err := client.Apply(context.Background(), types.WorkItemKey{Kind: "Namespace", Name: "acme"}, &status.StatusPatch{
		ResourceVersion:    "2",
		ObservedGeneration: &generation,
		Conditions: []*status.Condition{{
			Type:               "SystemRepoReady",
			Status:             "True",
			ObservedGeneration: generation,
		}},
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	vars := body["variables"].(map[string]any)
	input := vars["input"].(map[string]any)
	if input["kind"] != "Namespace" || input["name"] != "acme" {
		t.Fatalf("input identity = %+v, want Namespace/acme", input)
	}
	if input["namespace"] != "" {
		t.Fatalf("input.namespace = %v, want empty for cluster-scoped Namespace", input["namespace"])
	}
}

func TestResourceStatusApplyConflictMapsToErrConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"updateResourceStatus":{"object":null,"conflict":{"currentResourceVersion":"4"}}}}`))
	}))
	defer srv.Close()

	client := status.NewGraphQLResourceStatusClient(graphqlclient.New(srv.URL, "token"))
	err := client.Apply(context.Background(), types.WorkItemKey{Kind: "Namespace", Name: "acme"}, &status.StatusPatch{ResourceVersion: "3"})
	if !errors.Is(err, types.ErrConflict) {
		t.Fatalf("Apply error = %v, want types.ErrConflict", err)
	}
}

func TestResourceStatusApplyNotFoundMapsToErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"kind is not registered for status writes","extensions":{"code":"NOT_FOUND"}}]}`))
	}))
	defer srv.Close()

	client := status.NewGraphQLResourceStatusClient(graphqlclient.New(srv.URL, "token"))
	err := client.Apply(context.Background(), types.WorkItemKey{Kind: "Namespace", Name: "acme"}, &status.StatusPatch{ResourceVersion: "3"})
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("Apply error = %v, want types.ErrNotFound", err)
	}
}
