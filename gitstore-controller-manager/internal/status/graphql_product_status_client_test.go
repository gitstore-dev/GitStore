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

func TestResourceStatusJSONPreservesAbsentResolvedPayload(t *testing.T) {
	for _, tc := range []struct {
		name, data, want string
	}{
		{"missing", `{"ResourceVersion":"240"}`, ""},
		{"null", `{"ResourceVersion":"240","Resolved":null}`, ""},
		{"lowercase-null", `{"resourceVersion":"240","resolved": null }`, ""},
		{"empty-object", `{"ResourceVersion":"240","Resolved":{}}`, "{}"},
		{"category", `{"ResourceVersion":"240","Resolved":{"category":{"name":"tools"}}}`, `{"category":{"name":"tools"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var current status.ResourceStatus
			if err := json.Unmarshal([]byte(tc.data), &current); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if current.ResourceVersion != "240" || string(current.Resolved) != tc.want {
					t.Fatalf("decoded status: %+v, want resolved %q", current, tc.want)
				}
				if tc.want == "" && current.Resolved != nil {
					t.Fatalf("absent payload must be nil, got %q", current.Resolved)
				}
				data, err := json.Marshal(current)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, &current); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	var malformed status.ResourceStatus
	if err := json.Unmarshal([]byte(`{"ObservedGeneration":"invalid"}`), &malformed); err == nil {
		t.Fatal("malformed status must fail decoding")
	}
	partial := status.ResourceStatus{ResourceVersion: "240", ObservedGeneration: 1, Resolved: json.RawMessage(`{}`)}
	if err := json.Unmarshal([]byte(`{"Resolved":null}`), &partial); err != nil {
		t.Fatal(err)
	}
	if partial.ResourceVersion != "240" || partial.ObservedGeneration != 1 || partial.Resolved != nil {
		t.Fatalf("partial decode changed unspecified fields or failed to clear resolved: %+v", partial)
	}
}

func productTestKey() types.WorkItemKey {
	return types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "widget"}
}

func productTestPatch() *status.StatusPatch {
	gen := int64(2)
	now := time.Now()
	return &status.StatusPatch{
		ResourceVersion:    "1",
		ObservedGeneration: &gen,
		Conditions: []*status.Condition{{
			Type:               "CategoryResolved",
			Status:             "TRUE",
			ObservedGeneration: gen,
			LastTransitionTime: now,
			Reason:             "CategoryFound",
		}},
		Resolved: json.RawMessage(`{"category":{"name":"laptops","uid":"Q2F0ZWdvcnk6MQ=="}}`),
	}
}

func TestProductApply_SendsUpdateProductStatusMutation(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"updateProductStatus":{"product":{"metadata":{"resourceVersion":"2"}}}}}`))
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("test-token"))
	sc := status.NewGraphQLProductStatusClient(client)
	patch := productTestPatch()

	if err := sc.Apply(context.Background(), productTestKey(), patch); err != nil {
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
	if input["name"] != "widget" || input["namespace"] != "acme" {
		t.Errorf("input name/namespace = %v/%v, want widget/acme", input["name"], input["namespace"])
	}
	resolved, ok := input["resolved"].(map[string]any)
	if !ok {
		t.Fatalf("input missing 'resolved', got %+v", input)
	}
	category, ok := resolved["category"].(map[string]any)
	if !ok {
		t.Fatalf("resolved missing 'category', got %+v", resolved)
	}
	if category["name"] != "laptops" || category["uid"] != "Q2F0ZWdvcnk6MQ==" {
		t.Errorf("resolved.category = %+v, want name=laptops uid=Q2F0ZWdvcnk6MQ==", category)
	}
}

func TestProductApply_ConflictExtensionMapsToErrConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"resource version conflict","extensions":{"code":"RESOURCE_VERSION_CONFLICT","resourceVersion":"5"}}]}`))
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("test-token"))
	sc := status.NewGraphQLProductStatusClient(client)

	err := sc.Apply(context.Background(), productTestKey(), productTestPatch())
	if !errors.Is(err, types.ErrConflict) {
		t.Fatalf("Apply err = %v, want errors.Is(..., types.ErrConflict)", err)
	}
}

func TestProductApply_NotFoundExtensionMapsToErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"Product acme/widget not found","extensions":{"code":"NOT_FOUND"}}]}`))
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("test-token"))
	sc := status.NewGraphQLProductStatusClient(client)

	err := sc.Apply(context.Background(), productTestKey(), productTestPatch())
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("Apply err = %v, want errors.Is(..., types.ErrNotFound)", err)
	}
}
