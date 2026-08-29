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

// TestGraphQLRepositoryClientCreatesSystemRepositoryOnNotFoundError mirrors
// the actual gitstore-api response shape for repository(by: namespacePath)
// when no repository has been created yet: a real GraphQL error (message
// "repository not found", extensions.code "NOT_FOUND") rather than a clean
// null-data response. Before the fix, systemRepositoryExists treated any
// GraphQL error as a hard failure, so EnsureSystemRepository never fell
// through to createRepository — this reproduces the reported "never
// provisions gitstore-system on a fresh environment" bug.
func TestGraphQLRepositoryClientCreatesSystemRepositoryOnNotFoundError(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"data":{"repository":null},"errors":[{"message":"repository not found","extensions":{"code":"NOT_FOUND"}}]}`))
	}))
	defer srv.Close()

	client := NewGraphQLRepositoryClient(graphqlclient.New(srv.URL, "token"))
	if err := client.EnsureSystemRepository(context.Background(), "acme"); err != nil {
		t.Fatalf("EnsureSystemRepository failed: %v", err)
	}
	if queries != 1 || mutations != 1 {
		t.Fatalf("queries=%d mutations=%d, want 1/1 (createRepository must be reached)", queries, mutations)
	}
}

// TestGraphQLRepositoryClientPropagatesGenuineQueryErrors confirms that a
// non-"not found" GraphQL error (e.g. a genuine server error) still
// propagates as a hard failure and does not silently fall through to
// createRepository.
func TestGraphQLRepositoryClientPropagatesGenuineQueryErrors(t *testing.T) {
	var mutations int
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
		_, _ = w.Write([]byte(`{"errors":[{"message":"internal server error"}]}`))
	}))
	defer srv.Close()

	client := NewGraphQLRepositoryClient(graphqlclient.New(srv.URL, "token"))
	err := client.EnsureSystemRepository(context.Background(), "acme")
	if err == nil {
		t.Fatal("EnsureSystemRepository succeeded, want error for a genuine (non-not-found) failure")
	}
	if mutations != 0 {
		t.Fatalf("mutations=%d, want 0 — must not create on a genuine query error", mutations)
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
