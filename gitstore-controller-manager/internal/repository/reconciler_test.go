// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

type fakeStatusClient struct {
	patches []*status.StatusPatch
	err     error
}

func (f *fakeStatusClient) Apply(_ context.Context, _ types.WorkItemKey, patch *status.StatusPatch) error {
	f.patches = append(f.patches, patch)
	return f.err
}

type fakeStorageClient struct {
	calls    []struct{ namespace, name string }
	err      error
	resolved ResolvedStorage
}

func (f *fakeStorageClient) EnsureStorage(_ context.Context, namespace, name string) (ResolvedStorage, error) {
	f.calls = append(f.calls, struct{ namespace, name string }{namespace, name})
	return f.resolved, f.err
}

type fakeCompletionClient struct {
	calls []struct{ namespace, name, resourceVersion string }
	err   error
}

func (f *fakeCompletionClient) CompleteDeletion(_ context.Context, namespace, name, resourceVersion string) error {
	f.calls = append(f.calls, struct{ namespace, name, resourceVersion string }{namespace, name, resourceVersion})
	return f.err
}

func repositoryKey(namespace, name string) types.WorkItemKey {
	return types.WorkItemKey{Kind: "Repository", Namespace: namespace, Name: name}
}

func seedRepositoryCache(t *testing.T, items ...Repository) cache.CacheAccessor[Repository] {
	t.Helper()
	c := cache.New[Repository]()
	for _, item := range items {
		c.Set(repositoryKey(item.Namespace, item.Name), item)
	}
	return cache.AsReadOnly(c)
}

func admissionAccepted(generation int64) *status.Condition {
	return &status.Condition{Type: conditionAdmissionAccepted, Status: statusTrue, ObservedGeneration: generation}
}

func condition(t *testing.T, conditions []*status.Condition, conditionType string) *status.Condition {
	t.Helper()
	for _, candidate := range conditions {
		if candidate != nil && candidate.Type == conditionType {
			return candidate
		}
	}
	t.Fatalf("condition %q not found in %#v", conditionType, conditions)
	return nil
}

func TestReconcileMissingRepositoryIsAlreadyReconciled(t *testing.T) {
	r := NewReconciler(seedRepositoryCache(t), &fakeStatusClient{}, &fakeStorageClient{}, &fakeCompletionClient{})
	result := r.Reconcile(context.Background(), repositoryKey("acme", "missing"))
	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
	}
}

func TestReconcileStatusConflictRequeuesForFreshCacheState(t *testing.T) {
	current := repositoryFixture()
	r := NewReconciler(seedRepositoryCache(t, current), &fakeStatusClient{err: types.ErrConflict}, &fakeStorageClient{}, &fakeCompletionClient{})

	result := r.Reconcile(context.Background(), repositoryKey("acme", "catalog"))
	requeue, ok := result.(types.RequeueAfter)
	if !ok || requeue.After != conflictRequeueDelay {
		t.Fatalf("Reconcile result = %#v, want RequeueAfter(%s)", result, conflictRequeueDelay)
	}
}

func TestReconcileRateLimitRequeuesWithoutConsumingPoisonRetryBudget(t *testing.T) {
	for _, test := range []struct {
		name     string
		statuses *fakeStatusClient
		storage  *fakeStorageClient
	}{
		{name: "status", statuses: &fakeStatusClient{err: types.ErrRateLimited}, storage: &fakeStorageClient{}},
		{name: "storage", statuses: &fakeStatusClient{}, storage: &fakeStorageClient{err: types.ErrRateLimited}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := NewReconciler(seedRepositoryCache(t, repositoryFixture()), test.statuses, test.storage, &fakeCompletionClient{}).
				Reconcile(context.Background(), repositoryKey("acme", "catalog"))
			requeue, ok := result.(types.RequeueAfter)
			if !ok || requeue.After != rateLimitRequeueDelay {
				t.Fatalf("Reconcile result = %#v, want RequeueAfter(%s)", result, rateLimitRequeueDelay)
			}
		})
	}
}

func TestReconcileAdmittedRepositoryProvisionsStorageAndMarksReady(t *testing.T) {
	current := repositoryFixture(func(repository *Repository) {
		repository.StorageClass = "premium"
		repository.Generation = 3
		repository.ResourceVersion = "7"
		repository.Status = status.ResourceStatus{ResourceVersion: "7", Conditions: []*status.Condition{admissionAccepted(3)}}
	})
	statuses := &fakeStatusClient{}
	storage := &fakeStorageClient{resolved: ResolvedStorage{StoragePath: "/data/acme/catalog.git", StorageClass: "premium"}}
	r := NewReconciler(seedRepositoryCache(t, current), statuses, storage, &fakeCompletionClient{})

	result := r.Reconcile(context.Background(), repositoryKey("acme", "catalog"))
	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
	}
	if len(storage.calls) != 1 || storage.calls[0].namespace != "acme" || storage.calls[0].name != "catalog" {
		t.Fatalf("storage calls = %#v, want acme/catalog", storage.calls)
	}
	if len(statuses.patches) != 1 {
		t.Fatalf("status patches = %d, want 1", len(statuses.patches))
	}
	patch := statuses.patches[0]
	if patch.ResourceVersion != "7" || patch.ObservedGeneration == nil || *patch.ObservedGeneration != 3 {
		t.Fatalf("patch version fields = %#v, want rv=7 generation=3", patch)
	}
	if got := condition(t, patch.Conditions, conditionStorageProvisioned).Status; got != statusTrue {
		t.Errorf("StorageProvisioned = %q, want TRUE", got)
	}
	if got := condition(t, patch.Conditions, conditionReady).Status; got != statusTrue {
		t.Errorf("Ready = %q, want TRUE", got)
	}
	var gotResolved ResolvedStorage
	if err := json.Unmarshal(patch.Resolved, &gotResolved); err != nil {
		t.Fatalf("unmarshal patch.Resolved: %v", err)
	}
	if gotResolved != storage.resolved {
		t.Errorf("patch.Resolved = %#v, want %#v", gotResolved, storage.resolved)
	}
}

func TestReconcileSkipsNamespaceBootstrapRepository(t *testing.T) {
	current := Repository{
		UID: "bootstrap", Namespace: "acme", Name: SystemRepositoryName, Generation: 1, ResourceVersion: "1",
		Status: status.ResourceStatus{ResourceVersion: "1", Conditions: []*status.Condition{admissionAccepted(1)}},
	}
	statuses := &fakeStatusClient{}
	storage := &fakeStorageClient{}
	result := NewReconciler(seedRepositoryCache(t, current), statuses, storage, &fakeCompletionClient{}).
		Reconcile(context.Background(), repositoryKey("acme", SystemRepositoryName))
	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
	}
	if len(storage.calls) != 0 || len(statuses.patches) != 0 {
		t.Fatalf("bootstrap reconciliation made storage/status calls: storage=%d status=%d", len(storage.calls), len(statuses.patches))
	}
}

func TestReconcileProvisionFailureMarksNotReadyAndRetries(t *testing.T) {
	current := repositoryFixture(func(repository *Repository) {
		repository.StorageClass = "default"
		repository.Status.ResourceVersion = ""
	})
	statuses := &fakeStatusClient{}
	r := NewReconciler(seedRepositoryCache(t, current), statuses, &fakeStorageClient{err: errors.New("git service unavailable")}, &fakeCompletionClient{})

	result := r.Reconcile(context.Background(), repositoryKey("acme", "catalog"))
	if _, ok := result.(types.TransientFailure); !ok {
		t.Fatalf("Reconcile result = %T, want types.TransientFailure", result)
	}
	if len(statuses.patches) != 1 {
		t.Fatalf("status patches = %d, want 1", len(statuses.patches))
	}
	if got := condition(t, statuses.patches[0].Conditions, conditionStorageProvisioned).Status; got != statusFalse {
		t.Errorf("StorageProvisioned = %q, want FALSE", got)
	}
	if got := condition(t, statuses.patches[0].Conditions, conditionReady).Status; got != statusFalse {
		t.Errorf("Ready = %q, want FALSE", got)
	}
}

func TestReconcileWithoutAdmissionDoesNotProvisionStorage(t *testing.T) {
	current := repositoryFixture(func(repository *Repository) { repository.Status = status.ResourceStatus{} })
	storage := &fakeStorageClient{}
	r := NewReconciler(seedRepositoryCache(t, current), &fakeStatusClient{}, storage, &fakeCompletionClient{})

	result := r.Reconcile(context.Background(), repositoryKey("acme", "catalog"))
	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
	}
	if len(storage.calls) != 0 {
		t.Fatalf("storage calls = %#v, want none", storage.calls)
	}
}

func TestReconcileTerminatingRepositoryCompletesDeletion(t *testing.T) {
	current := repositoryFixture(func(repository *Repository) {
		repository.ResourceVersion = "9"
		repository.Finalizers = []string{ForegroundDeletionFinalizer}
	})
	completion := &fakeCompletionClient{}
	r := NewReconciler(seedRepositoryCache(t, current), &fakeStatusClient{}, &fakeStorageClient{}, completion)

	result := r.Reconcile(context.Background(), repositoryKey("acme", "catalog"))
	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
	}
	if len(completion.calls) != 1 || completion.calls[0] != (struct{ namespace, name, resourceVersion string }{"acme", "catalog", "9"}) {
		t.Fatalf("completion calls = %#v, want acme/catalog/9", completion.calls)
	}
}

func TestReconcileTerminatingRepositoryRetriesCompletionFailure(t *testing.T) {
	current := repositoryFixture(func(repository *Repository) {
		repository.ResourceVersion = "9"
		repository.Finalizers = []string{ForegroundDeletionFinalizer}
	})
	r := NewReconciler(seedRepositoryCache(t, current), &fakeStatusClient{}, &fakeStorageClient{}, &fakeCompletionClient{err: errors.New("git service unavailable")})
	result := r.Reconcile(context.Background(), repositoryKey("acme", "catalog"))
	if _, ok := result.(types.TransientFailure); !ok {
		t.Fatalf("Reconcile result = %T, want types.TransientFailure", result)
	}
}

func TestReconcileTerminatingRepositoryConflictRequeuesForFreshCacheState(t *testing.T) {
	current := repositoryFixture(func(repository *Repository) {
		repository.Finalizers = []string{ForegroundDeletionFinalizer}
	})
	r := NewReconciler(seedRepositoryCache(t, current), &fakeStatusClient{}, &fakeStorageClient{}, &fakeCompletionClient{err: types.ErrConflict})

	result := r.Reconcile(context.Background(), repositoryKey("acme", "catalog"))
	if _, ok := result.(types.RequeueAfter); !ok {
		t.Fatalf("Reconcile result = %T, want types.RequeueAfter", result)
	}
}
