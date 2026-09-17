// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package product

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/categorytaxonomy"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

type fakeCompletionClient struct {
	calls int
	err   error
}

func (f *fakeCompletionClient) CompleteDeletion(context.Context, string, string, string) error {
	f.calls++
	return f.err
}

func productKey() types.WorkItemKey {
	return types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "widget"}
}
func reconcilerFor(t *testing.T, item categorytaxonomy.Product, client *fakeCompletionClient) *Reconciler {
	t.Helper()
	c := cache.New[categorytaxonomy.Product]()
	c.Set(productKey(), item)
	return NewReconciler(cache.AsReadOnly(c), client)
}

func TestReconcileCompletesTerminatingProduct(t *testing.T) {
	now := time.Now()
	client := &fakeCompletionClient{}
	r := reconcilerFor(t, categorytaxonomy.Product{Namespace: "acme", Name: "widget", ResourceVersion: "8", DeletionTimestamp: &now, Finalizers: []string{foregroundDeletionFinalizer}}, client)
	if _, ok := r.Reconcile(context.Background(), productKey()).(types.Success); !ok {
		t.Fatal("want success")
	}
	if client.calls != 1 {
		t.Fatalf("calls = %d, want 1", client.calls)
	}
}

func TestReconcileConflictRequeues(t *testing.T) {
	now := time.Now()
	client := &fakeCompletionClient{err: types.ErrConflict}
	r := reconcilerFor(t, categorytaxonomy.Product{Namespace: "acme", Name: "widget", ResourceVersion: "8", DeletionTimestamp: &now, Finalizers: []string{foregroundDeletionFinalizer}}, client)
	if _, ok := r.Reconcile(context.Background(), productKey()).(types.RequeueAfter); !ok {
		t.Fatal("want requeue")
	}
}

func TestReconcileActiveProductDoesNotComplete(t *testing.T) {
	client := &fakeCompletionClient{err: errors.New("must not call")}
	r := reconcilerFor(t, categorytaxonomy.Product{Namespace: "acme", Name: "widget", ResourceVersion: "8"}, client)
	if _, ok := r.Reconcile(context.Background(), productKey()).(types.Success); !ok {
		t.Fatal("want success")
	}
	if client.calls != 0 {
		t.Fatalf("calls = %d, want 0", client.calls)
	}
}
