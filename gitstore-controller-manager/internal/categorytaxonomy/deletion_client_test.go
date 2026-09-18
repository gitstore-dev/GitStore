// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/categorytaxonomy"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// TestDecoupleProducts_QueryOnlyRequestsFieldsThePayloadDeclares is a
// regression test for a P1-adjacent finding on PR #426: the mutation this
// client sends previously selected a `conflict` field that
// UpdateCategoryStatusPayload has never declared, so every call failed
// GraphQL schema validation with HTTP 422 against a real server — masked in
// this package's own tests because they mock HTTP responses without real
// schema validation, and only surfaced once a live-stack integration test
// (spec 062) actually exercised category deletion with dependent Products
// end-to-end. This test locks in the corrected query shape and the
// GraphQL-error-based conflict mapping that replaces the removed field.
func TestDecoupleProducts_QueryOnlyRequestsFieldsThePayloadDeclares(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotQuery = req.Query
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"updateCategoryStatus":{"hasMoreProductDependents":true}}}`))
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("test-token"))
	dc := categorytaxonomy.NewGraphQLDeletionClient(client)

	hasMore, err := dc.DecoupleProducts(context.Background(), "acme", "laptops", "1")
	if err != nil {
		t.Fatalf("DecoupleProducts failed: %v", err)
	}
	if !hasMore {
		t.Errorf("hasMore = false, want true")
	}
	if strings.Contains(gotQuery, "conflict") {
		t.Errorf("query still selects a non-existent 'conflict' field: %s", gotQuery)
	}
}

func TestDecoupleProducts_ConflictExtensionMapsToErrConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"CategoryTaxonomy acme/laptops status update conflict","extensions":{"code":"RESOURCE_VERSION_CONFLICT","resourceVersion":"5"}}]}`))
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("test-token"))
	dc := categorytaxonomy.NewGraphQLDeletionClient(client)

	_, err := dc.DecoupleProducts(context.Background(), "acme", "laptops", "1")
	if !errors.Is(err, types.ErrConflict) {
		t.Fatalf("DecoupleProducts err = %v, want errors.Is(..., types.ErrConflict)", err)
	}
}

func TestCompleteDeletion_ConflictExtensionMapsToErrConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"CategoryTaxonomy acme/laptops status update conflict","extensions":{"code":"RESOURCE_VERSION_CONFLICT","resourceVersion":"5"}}]}`))
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("test-token"))
	dc := categorytaxonomy.NewGraphQLDeletionClient(client)

	err := dc.CompleteDeletion(context.Background(), "acme", "laptops", "1")
	if !errors.Is(err, types.ErrConflict) {
		t.Fatalf("CompleteDeletion err = %v, want errors.Is(..., types.ErrConflict)", err)
	}
}
