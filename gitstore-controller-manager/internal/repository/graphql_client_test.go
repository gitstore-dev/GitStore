// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

func TestGraphQLCompletionClientCompletesRepositoryDeletion(t *testing.T) {
	var gotInput map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		gotInput, _ = request.Variables["input"].(map[string]any)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"completeRepositoryDeletion":{"id":"repo-1"}}}`))
	}))
	defer srv.Close()

	client := NewGraphQLCompletionClient(graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("token")))
	if err := client.CompleteDeletion(context.Background(), "acme", "catalog", "7"); err != nil {
		t.Fatalf("CompleteDeletion() error = %v", err)
	}
	if gotInput["namespace"] != "acme" || gotInput["name"] != "catalog" || gotInput["resourceVersion"] != "7" {
		t.Fatalf("input = %#v, want namespace/name/resourceVersion", gotInput)
	}
}

func TestGraphQLCompletionClientTreatsNotFoundAsIdempotentSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"repository not found","extensions":{"code":"NOT_FOUND"}}]}`))
	}))
	defer srv.Close()

	client := NewGraphQLCompletionClient(graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("token")))
	if err := client.CompleteDeletion(context.Background(), "acme", "catalog", "7"); err != nil {
		t.Fatalf("CompleteDeletion() error = %v, want nil", err)
	}
}

func TestGraphQLCompletionClientReturnsConflictForRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"resource version conflict","extensions":{"code":"RESOURCE_VERSION_CONFLICT","currentResourceVersion":"8"}}]}`))
	}))
	defer srv.Close()

	client := NewGraphQLCompletionClient(graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("token")))
	err := client.CompleteDeletion(context.Background(), "acme", "catalog", "7")
	if err == nil || !errors.Is(err, types.ErrConflict) {
		t.Fatalf("CompleteDeletion() error = %v, want conflict", err)
	}
}

func TestGraphQLStorageClientProvisionsByNamespaceAndName(t *testing.T) {
	var gotInput map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		gotInput, _ = request.Variables["input"].(map[string]any)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"provisionRepositoryStorage":{"repository":{"metadata":{"namespace":"acme","name":"catalog"},"status":{"resolved":{"storagePath":"/data/acme/catalog.git","storageClass":"standard"}}}}}}`))
	}))
	defer srv.Close()

	client := NewGraphQLStorageClient(graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("token")))
	resolved, err := client.EnsureStorage(context.Background(), "acme", "catalog")
	if err != nil {
		t.Fatalf("EnsureStorage() error = %v", err)
	}
	if gotInput["namespace"] != "acme" || gotInput["name"] != "catalog" {
		t.Fatalf("input = %#v, want namespace/name", gotInput)
	}
	want := ResolvedStorage{StoragePath: "/data/acme/catalog.git", StorageClass: "standard"}
	if resolved != want {
		t.Fatalf("EnsureStorage() resolved = %#v, want %#v", resolved, want)
	}
}

func TestGraphQLStorageClientPreservesAuthorizationAndRetryErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "authorization", body: `{"data":null,"errors":[{"message":"forbidden","extensions":{"code":"FORBIDDEN"}}]}`},
		{name: "transient", body: `{"data":null,"errors":[{"message":"temporarily unavailable","extensions":{"code":"INTERNAL"}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, err := NewGraphQLStorageClient(graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("token"))).EnsureStorage(context.Background(), "acme", "catalog")
			if err == nil {
				t.Fatal("EnsureStorage() error = nil, want propagated API error")
			}
		})
	}
}

func TestGraphQLStorageClientTwoReplicaProvisioningIsIdempotent(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"provisionRepositoryStorage":{"repository":{"metadata":{"namespace":"acme","name":"catalog"}}}}}`))
	}))
	defer srv.Close()
	first := NewGraphQLStorageClient(graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("replica-a")))
	second := NewGraphQLStorageClient(graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("replica-b")))
	if _, err := first.EnsureStorage(context.Background(), "acme", "catalog"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.EnsureStorage(context.Background(), "acme", "catalog"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("provision calls = %d, want one idempotent request from each replica", calls)
	}
}
