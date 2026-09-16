// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package namespace

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

func TestGraphQLRepositoryClientProvisionsMissingSystemRepository(t *testing.T) {
	var mutations int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "mutation") {
			mutations++
			_, _ = w.Write([]byte(`{"data":{"provisionNamespaceSystemRepository":{"repository":{"metadata":{"name":"gitstore-system"}}}}}`))
			return
		}
		t.Fatalf("unexpected query: %s", req.Query)
	}))
	defer srv.Close()

	client := NewGraphQLRepositoryClient(graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("token")))
	if err := client.EnsureSystemRepository(context.Background(), "acme"); err != nil {
		t.Fatalf("EnsureSystemRepository failed: %v", err)
	}
	if mutations != 1 {
		t.Fatalf("mutations=%d, want 1", mutations)
	}
}

func TestGraphQLRepositoryClientPropagatesProvisioningError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"storage unavailable"}]}`))
	}))
	defer srv.Close()

	client := NewGraphQLRepositoryClient(graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("token")))
	err := client.EnsureSystemRepository(context.Background(), "acme")
	if err == nil {
		t.Fatal("EnsureSystemRepository succeeded, want provisioning error")
	}
}

func TestGraphQLRepositoryClientAcceptsIdempotentProvisioning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"provisionNamespaceSystemRepository":{"repository":{"metadata":{"name":"gitstore-system"}}}}}`))
	}))
	defer srv.Close()

	client := NewGraphQLRepositoryClient(graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("token")))
	if err := client.EnsureSystemRepository(context.Background(), "acme"); err != nil {
		t.Fatalf("EnsureSystemRepository failed: %v", err)
	}
}

func TestGraphQLRepositoryClientReportsRepositoryPresence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"repositories":{"totalCount":2}}}`))
	}))
	defer srv.Close()

	client := NewGraphQLRepositoryClient(graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("token")))
	hasRepositories, err := client.HasRepositories(context.Background(), "acme")
	if err != nil {
		t.Fatalf("HasRepositories failed: %v", err)
	}
	if !hasRepositories {
		t.Fatal("HasRepositories = false, want true")
	}
}

func TestGraphQLDeletionClientCompletesDeletion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"completeNamespaceDeletion":{"deletedIdentifier":"acme","conflict":null}}}`))
	}))
	defer srv.Close()

	client := NewGraphQLDeletionClient(graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("token")))
	if err := client.CompleteDeletion(context.Background(), "acme", "9"); err != nil {
		t.Fatalf("CompleteDeletion failed: %v", err)
	}
}

func TestGraphQLDeletionClientReturnsConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"completeNamespaceDeletion":{"deletedIdentifier":null,"conflict":{"currentResourceVersion":"10"}}}}`))
	}))
	defer srv.Close()

	client := NewGraphQLDeletionClient(graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("token")))
	err := client.CompleteDeletion(context.Background(), "acme", "9")
	if !errors.Is(err, types.ErrConflict) {
		t.Fatalf("CompleteDeletion error = %v, want conflict", err)
	}
}
