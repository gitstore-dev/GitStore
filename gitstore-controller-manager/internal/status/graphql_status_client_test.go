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
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

func testKey() types.WorkItemKey {
	return types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: "acme", Name: "electronics"}
}

func testPatch() *status.StatusPatch {
	gen := int64(2)
	now := time.Now()
	return &status.StatusPatch{
		ResourceVersion:    "1",
		ObservedGeneration: &gen,
		Conditions: []*status.Condition{{
			Type:               "Ready",
			Status:             "True",
			ObservedGeneration: gen,
			LastTransitionTime: now,
		}},
		Resolved: json.RawMessage(`{"depth":0,"path":["electronics"],"childCount":0,"productCount":0}`),
	}
}

func TestApply_SendsUpdateCategoryStatusMutation(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"updateCategoryStatus":{"category":{"metadata":{"resourceVersion":"2"}},"conflict":null}}}`))
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, "test-token")
	sc := status.NewGraphQLStatusClient(client)
	patch := testPatch()

	if err := sc.Apply(context.Background(), testKey(), patch); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	vars, ok := gotBody["variables"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing 'variables', got %+v", gotBody)
	}
	input, ok := vars["input"].(map[string]any)
	if !ok {
		t.Fatalf("variables missing 'input', got %+v", vars)
	}
	if input["name"] != "electronics" || input["namespace"] != "acme" {
		t.Errorf("input name/namespace = %v/%v, want electronics/acme", input["name"], input["namespace"])
	}
	if input["resourceVersion"] != "1" {
		t.Errorf("input resourceVersion = %v, want %q", input["resourceVersion"], "1")
	}
	resolved, ok := input["resolved"].(map[string]any)
	if !ok {
		t.Fatalf("input missing 'resolved', got %+v", input)
	}
	if resolved["depth"] != float64(0) {
		t.Errorf("resolved.depth = %v, want 0", resolved["depth"])
	}

	conds, ok := input["conditions"].([]any)
	if !ok {
		t.Fatalf("input missing 'conditions', got %+v", input)
	}
	if len(conds) != 1 {
		t.Fatalf("conditions length = %d, want 1", len(conds))
	}
	cond, ok := conds[0].(map[string]any)
	if !ok {
		t.Fatalf("condition entry not an object: %+v", conds[0])
	}
	if cond["status"] != "TRUE" {
		t.Errorf("condition status = %v, want %q", cond["status"], "TRUE")
	}
	wantTransitionTime := patch.Conditions[0].LastTransitionTime.Format("2006-01-02T15:04:05.000Z07:00")
	if cond["lastTransitionTime"] != wantTransitionTime {
		t.Errorf("condition lastTransitionTime = %v, want %q", cond["lastTransitionTime"], wantTransitionTime)
	}
}

func TestApply_NonNullConflictMapsToErrConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"updateCategoryStatus":{"category":null,"conflict":{"currentResourceVersion":"5"}}}}`))
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, "test-token")
	sc := status.NewGraphQLStatusClient(client)

	err := sc.Apply(context.Background(), testKey(), testPatch())
	if !errors.Is(err, types.ErrConflict) {
		t.Fatalf("Apply err = %v, want errors.Is(..., types.ErrConflict)", err)
	}
}

func TestApply_NotFoundExtensionMapsToErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"CategoryTaxonomy acme/electronics not found","extensions":{"code":"NOT_FOUND"}}]}`))
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, "test-token")
	sc := status.NewGraphQLStatusClient(client)

	err := sc.Apply(context.Background(), testKey(), testPatch())
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("Apply err = %v, want errors.Is(..., types.ErrNotFound)", err)
	}
}

func TestApply_ForbiddenExtensionReturnsPlainWrappedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"not authorized","extensions":{"code":"FORBIDDEN"}}]}`))
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, "test-token")
	sc := status.NewGraphQLStatusClient(client)

	err := sc.Apply(context.Background(), testKey(), testPatch())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, types.ErrConflict) || errors.Is(err, types.ErrNotFound) {
		t.Errorf("FORBIDDEN must not map to a sentinel, got %v", err)
	}
	var gqlErr *graphqlclient.Error
	if !errors.As(err, &gqlErr) {
		t.Fatalf("expected the underlying *graphqlclient.Error to be unwrappable, got %T", err)
	}
}
