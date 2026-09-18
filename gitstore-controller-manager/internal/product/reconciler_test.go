// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package product

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/categorytaxonomy"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
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

type fakeStatusClient struct {
	patches []*status.StatusPatch
	err     error
}

func (f *fakeStatusClient) Apply(_ context.Context, _ types.WorkItemKey, patch *status.StatusPatch) error {
	f.patches = append(f.patches, patch)
	return f.err
}

func productKey() types.WorkItemKey {
	return types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "widget"}
}

// reconcilerFor builds a Reconciler for the deletion-completion tests, which
// never reach the resolve path — a nil category cache/status client is safe.
func reconcilerFor(t *testing.T, item categorytaxonomy.Product, client *fakeCompletionClient) *Reconciler {
	t.Helper()
	c := cache.New[categorytaxonomy.Product]()
	c.Set(productKey(), item)
	return NewReconciler(cache.AsReadOnly(c), nil, nil, client)
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

func TestReconcileTransientCompletionFailureUsesRetryBudget(t *testing.T) {
	now := time.Now()
	client := &fakeCompletionClient{err: errors.New("temporary API outage")}
	r := reconcilerFor(t, categorytaxonomy.Product{Namespace: "acme", Name: "widget", ResourceVersion: "8", DeletionTimestamp: &now, Finalizers: []string{foregroundDeletionFinalizer}}, client)
	result := r.Reconcile(context.Background(), productKey())
	transient, ok := result.(types.TransientFailure)
	if !ok {
		t.Fatalf("result = %T, want transient failure", result)
	}
	if !errors.Is(transient.Err, client.err) {
		t.Fatalf("retry error = %v, want wrapped %v", transient.Err, client.err)
	}
}

// resolveReconcilerFor builds a Reconciler exercising the active resolve
// path (T010-T015): a Product cache pre-populated with item, a
// CategoryTaxonomy cache pre-populated with categories, and statusClient
// capturing every Apply call.
func resolveReconcilerFor(t *testing.T, item categorytaxonomy.Product, categories []categorytaxonomy.CategoryTaxonomy, statusClient status.StatusClient) *Reconciler {
	t.Helper()
	productCache := cache.New[categorytaxonomy.Product]()
	productCache.Set(productKey(), item)
	categoryCache := cache.New[categorytaxonomy.CategoryTaxonomy]()
	for _, c := range categories {
		categoryCache.Set(types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: c.Namespace, Name: c.Name}, c)
	}
	return NewReconciler(cache.AsReadOnly(productCache), cache.AsReadOnly(categoryCache), statusClient, &fakeCompletionClient{err: errors.New("must not call")})
}

func decodeResolvedCategory(t *testing.T, raw json.RawMessage) resolvedCategory {
	t.Helper()
	var out resolvedCategory
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode resolved.category: %v", err)
	}
	return out
}

func admissionAcceptedCondition() *status.Condition {
	return &status.Condition{Type: "AdmissionAccepted", Status: statusTrue}
}

// T010: a Product whose CategoryRefName matches a cached, non-Terminating
// CategoryTaxonomy resolves True/CategoryFound and Ready True/ProductReady.
func TestReconcileActive_CategoryFoundResolvesReadyTrue(t *testing.T) {
	statusClient := &fakeStatusClient{}
	item := categorytaxonomy.Product{
		Namespace: "acme", Name: "widget", ResourceVersion: "1", Generation: 3,
		CategoryRefName: "laptops",
		Status:          status.ResourceStatus{ResourceVersion: "1", Conditions: []*status.Condition{admissionAcceptedCondition()}},
	}
	category := categorytaxonomy.CategoryTaxonomy{Namespace: "acme", Name: "laptops", UID: "Q2F0ZWdvcnk6MQ=="}
	r := resolveReconcilerFor(t, item, []categorytaxonomy.CategoryTaxonomy{category}, statusClient)

	result := r.Reconcile(context.Background(), productKey())
	if _, ok := result.(types.Success); !ok {
		t.Fatalf("result = %T, want success", result)
	}
	if len(statusClient.patches) != 1 {
		t.Fatalf("patches = %d, want 1", len(statusClient.patches))
	}
	patch := statusClient.patches[0]
	resolved := decodeResolvedCategory(t, patch.Resolved)
	if resolved.Category == nil || resolved.Category.Name != "laptops" || resolved.Category.UID != "Q2F0ZWdvcnk6MQ==" {
		t.Fatalf("resolved.category = %+v, want {laptops Q2F0ZWdvcnk6MQ==}", resolved.Category)
	}
	assertCondition(t, patch.Conditions, "CategoryResolved", statusTrue, "CategoryFound")
	assertCondition(t, patch.Conditions, "Ready", statusTrue, "ProductReady")
}

// T011: no matching CategoryTaxonomy cache entry (including empty
// CategoryRefName) resolves False/CategoryNotFound, Ready False.
func TestReconcileActive_CategoryNotFoundResolvesReadyFalse(t *testing.T) {
	statusClient := &fakeStatusClient{}
	item := categorytaxonomy.Product{
		Namespace: "acme", Name: "widget", ResourceVersion: "1", Generation: 1,
		CategoryRefName: "gadgets",
		Status:          status.ResourceStatus{ResourceVersion: "1", Conditions: []*status.Condition{admissionAcceptedCondition()}},
	}
	r := resolveReconcilerFor(t, item, nil, statusClient)

	result := r.Reconcile(context.Background(), productKey())
	if _, ok := result.(types.RequeueAfter); !ok {
		t.Fatalf("result = %T, want requeue (FR-011 bounded retry)", result)
	}
	patch := statusClient.patches[0]
	resolved := decodeResolvedCategory(t, patch.Resolved)
	if resolved.Category != nil {
		t.Fatalf("resolved.category = %+v, want nil", resolved.Category)
	}
	assertCondition(t, patch.Conditions, "CategoryResolved", statusFalse, "CategoryNotFound")
	assertCondition(t, patch.Conditions, "Ready", statusFalse, "CategoryUnresolved")
}

// A Product with no categoryRef at all (spec.categoryRef is nullable) is a
// valid, uncategorized Product — CategoryResolved must be True (vacuously,
// reason NoCategoryReference) so Ready can become True, not treated the same
// as a set-but-unresolvable categoryRef. Covers both the create and update
// path, since the reconciler runs the same resolve logic either way.
func TestReconcileActive_NoCategoryRefResolvesReadyTrue(t *testing.T) {
	statusClient := &fakeStatusClient{}
	item := categorytaxonomy.Product{
		Namespace: "acme", Name: "widget", ResourceVersion: "1", Generation: 1,
		CategoryRefName: "",
		Status:          status.ResourceStatus{ResourceVersion: "1", Conditions: []*status.Condition{admissionAcceptedCondition()}},
	}
	r := resolveReconcilerFor(t, item, nil, statusClient)

	result := r.Reconcile(context.Background(), productKey())
	if _, ok := result.(types.Success); !ok {
		t.Fatalf("result = %T, want success (no categoryRef has nothing to retry)", result)
	}
	patch := statusClient.patches[0]
	resolved := decodeResolvedCategory(t, patch.Resolved)
	if resolved.Category != nil {
		t.Fatalf("resolved.category = %+v, want nil", resolved.Category)
	}
	assertCondition(t, patch.Conditions, "CategoryResolved", statusTrue, "NoCategoryReference")
	assertCondition(t, patch.Conditions, "Ready", statusTrue, "ProductReady")
}

// T012: a matching CategoryTaxonomy that is Terminating (DeletionTimestamp
// set) resolves the same as not-found — never CategoryFound.
func TestReconcileActive_TerminatingCategoryResolvesAsNotFound(t *testing.T) {
	statusClient := &fakeStatusClient{}
	now := time.Now()
	item := categorytaxonomy.Product{
		Namespace: "acme", Name: "widget", ResourceVersion: "1", Generation: 1,
		CategoryRefName: "laptops",
		Status:          status.ResourceStatus{ResourceVersion: "1", Conditions: []*status.Condition{admissionAcceptedCondition()}},
	}
	category := categorytaxonomy.CategoryTaxonomy{Namespace: "acme", Name: "laptops", UID: "Q2F0ZWdvcnk6MQ==", DeletionTimestamp: &now}
	r := resolveReconcilerFor(t, item, []categorytaxonomy.CategoryTaxonomy{category}, statusClient)

	r.Reconcile(context.Background(), productKey())
	patch := statusClient.patches[0]
	assertCondition(t, patch.Conditions, "CategoryResolved", statusFalse, "CategoryNotFound")
}

// T013: re-reconciling an already-CategoryResolved=True Product whose spec
// changed but categoryRef did not preserves lastTransitionTime (FR-007).
func TestReconcileActive_PreservesLastTransitionTimeOnNoChange(t *testing.T) {
	statusClient := &fakeStatusClient{}
	priorTransition := time.Now().Add(-time.Hour).UTC()
	item := categorytaxonomy.Product{
		Namespace: "acme", Name: "widget", ResourceVersion: "2", Generation: 4,
		CategoryRefName: "laptops",
		Status: status.ResourceStatus{
			ResourceVersion:    "2",
			ObservedGeneration: 4,
			Conditions: []*status.Condition{
				admissionAcceptedCondition(),
				{Type: "CategoryResolved", Status: statusTrue, Reason: "CategoryFound", Message: "spec.categoryRef resolves to an existing CategoryTaxonomy in this namespace.", ObservedGeneration: 4, LastTransitionTime: priorTransition},
				{Type: "Ready", Status: statusTrue, Reason: "ProductReady", Message: "AdmissionAccepted and CategoryResolved are both True.", ObservedGeneration: 4, LastTransitionTime: priorTransition},
			},
			Resolved: json.RawMessage(`{"category":{"name":"laptops","uid":"Q2F0ZWdvcnk6MQ=="}}`),
		},
	}
	category := categorytaxonomy.CategoryTaxonomy{Namespace: "acme", Name: "laptops", UID: "Q2F0ZWdvcnk6MQ=="}
	r := resolveReconcilerFor(t, item, []categorytaxonomy.CategoryTaxonomy{category}, statusClient)

	result := r.Reconcile(context.Background(), productKey())
	if _, ok := result.(types.Success); !ok {
		t.Fatalf("result = %T, want success", result)
	}
	// IsNoOp: nothing changed, so Apply must not have been called at all.
	if len(statusClient.patches) != 0 {
		t.Fatalf("patches = %d, want 0 (no-op reconcile must not write)", len(statusClient.patches))
	}
}

// T014: a RESOURCE_VERSION_CONFLICT from statusClient.Apply requeues rather
// than terminally failing (PR-001).
func TestReconcileActive_StatusConflictRequeues(t *testing.T) {
	statusClient := &fakeStatusClient{err: types.ErrConflict}
	item := categorytaxonomy.Product{
		Namespace: "acme", Name: "widget", ResourceVersion: "1", Generation: 1,
		CategoryRefName: "laptops",
		Status:          status.ResourceStatus{ResourceVersion: "1", Conditions: []*status.Condition{admissionAcceptedCondition()}},
	}
	category := categorytaxonomy.CategoryTaxonomy{Namespace: "acme", Name: "laptops", UID: "Q2F0ZWdvcnk6MQ=="}
	r := resolveReconcilerFor(t, item, []categorytaxonomy.CategoryTaxonomy{category}, statusClient)

	result := r.Reconcile(context.Background(), productKey())
	if _, ok := result.(types.RequeueAfter); !ok {
		t.Fatalf("result = %T, want requeue on conflict", result)
	}
}

// T015: a Product with a foreground-deletion finalizer set never has
// CategoryResolved/Ready computed or written (FR-008).
func TestReconcileActive_TerminatingProductNeverWritesCategoryStatus(t *testing.T) {
	statusClient := &fakeStatusClient{}
	now := time.Now()
	completion := &fakeCompletionClient{}
	productCache := cache.New[categorytaxonomy.Product]()
	productCache.Set(productKey(), categorytaxonomy.Product{
		Namespace: "acme", Name: "widget", ResourceVersion: "1",
		DeletionTimestamp: &now, Finalizers: []string{foregroundDeletionFinalizer},
		CategoryRefName: "laptops",
	})
	r := NewReconciler(cache.AsReadOnly(productCache), nil, statusClient, completion)

	r.Reconcile(context.Background(), productKey())
	if completion.calls != 1 {
		t.Fatalf("completion calls = %d, want 1", completion.calls)
	}
	if len(statusClient.patches) != 0 {
		t.Fatalf("patches = %d, want 0 (deletion path must not write CategoryResolved/Ready)", len(statusClient.patches))
	}
}

func assertCondition(t *testing.T, conditions []*status.Condition, conditionType, wantStatus, wantReason string) {
	t.Helper()
	for _, c := range conditions {
		if c != nil && c.Type == conditionType {
			if c.Status != wantStatus {
				t.Errorf("%s.Status = %q, want %q", conditionType, c.Status, wantStatus)
			}
			if c.Reason != wantReason {
				t.Errorf("%s.Reason = %q, want %q", conditionType, c.Reason, wantReason)
			}
			return
		}
	}
	t.Fatalf("condition %s not found in %+v", conditionType, conditions)
}
