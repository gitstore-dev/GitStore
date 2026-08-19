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

func TestGraphQLRepositoryClientCreatesMissingSystemRepository(t *testing.T) {
	var queries, mutations int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "mutation") {
			mutations++
			_, _ = w.Write([]byte(`{"data":{"createRepository":{"repository":{"metadata":{"name":"gitstore-system"}}}}}`))
			return
		}
		queries++
		_, _ = w.Write([]byte(`{"data":{"repository":null}}`))
	}))
	defer srv.Close()

	client := NewGraphQLRepositoryClient(graphqlclient.New(srv.URL, "token"))
	if err := client.EnsureSystemRepository(context.Background(), "acme"); err != nil {
		t.Fatalf("EnsureSystemRepository failed: %v", err)
	}
	if queries != 1 || mutations != 1 {
		t.Fatalf("queries=%d mutations=%d, want 1/1", queries, mutations)
	}
}

func TestGraphQLRepositoryClientTreatsExistingSystemRepositoryAsReady(t *testing.T) {
	var mutations int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "mutation") {
			mutations++
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"metadata":{"name":"gitstore-system"}}}}`))
	}))
	defer srv.Close()

	client := NewGraphQLRepositoryClient(graphqlclient.New(srv.URL, "token"))
	if err := client.EnsureSystemRepository(context.Background(), "acme"); err != nil {
		t.Fatalf("EnsureSystemRepository failed: %v", err)
	}
	if mutations != 0 {
		t.Fatalf("mutations=%d, want 0", mutations)
	}
}

func TestGraphQLRepositoryClientReportsRepositoryPresence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"repositories":{"totalCount":2}}}`))
	}))
	defer srv.Close()

	client := NewGraphQLRepositoryClient(graphqlclient.New(srv.URL, "token"))
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

	client := NewGraphQLDeletionClient(graphqlclient.New(srv.URL, "token"))
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

	client := NewGraphQLDeletionClient(graphqlclient.New(srv.URL, "token"))
	err := client.CompleteDeletion(context.Background(), "acme", "9")
	if !errors.Is(err, types.ErrConflict) {
		t.Fatalf("CompleteDeletion error = %v, want conflict", err)
	}
}
