// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package product

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/schemavalidate"
)

func TestCompletionClientSendsObservedNodeID(t *testing.T) {
	const id = "Z2lkOi8vR2l0U3RvcmUvUHJvZHVjdC9wcm9kdWN0LTE="
	var input map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Variables struct {
				Input map[string]any `json:"input"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		input = request.Variables.Input
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"completeProductDeletion":{"id":"` + id + `"}}}`))
	}))
	defer server.Close()
	client := NewGraphQLCompletionClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("token")))
	if err := client.CompleteDeletion(t.Context(), "acme", "widget", "7", id); err != nil {
		t.Fatal(err)
	}
	if input["id"] != id || input["namespace"] != "acme" || input["name"] != "widget" || input["resourceVersion"] != "7" {
		t.Fatalf("unexpected completion input: %#v", input)
	}
	if _, exists := input["uid"]; exists {
		t.Fatal("completion must not send the removed uid input field")
	}
}

// TestGraphQLOperationsMatchSchema validates every operation string this
// package sends against the real schema (shared/schemas/*.graphqls), the
// same way gqlgen validates it server-side.
func TestGraphQLOperationsMatchSchema(t *testing.T) {
	schemavalidate.Validate(t, []schemavalidate.Operation{
		{Name: "completeProductDeletionMutation", Query: completeProductDeletionMutation},
	})
}
