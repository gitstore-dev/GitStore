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

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

type fakeStatusClient struct {
	err     error
	keys    []types.WorkItemKey
	patches []*status.StatusPatch
}

func (f *fakeStatusClient) Apply(_ context.Context, key types.WorkItemKey, patch *status.StatusPatch) error {
	f.keys = append(f.keys, key)
	f.patches = append(f.patches, patch)
	return f.err
}

type fakeRepositoryClient struct {
	ensureErr  error
	hasRepos   bool
	hasRepoErr error
	ensured    []string
	checked    []string
}

func (f *fakeRepositoryClient) EnsureSystemRepository(_ context.Context, namespace string) error {
	f.ensured = append(f.ensured, namespace)
	return f.ensureErr
}

func (f *fakeRepositoryClient) HasRepositories(_ context.Context, namespace string) (bool, error) {
	f.checked = append(f.checked, namespace)
	return f.hasRepos, f.hasRepoErr
}

type fakeDeletionClient struct {
	err   error
	calls []string
	rvs   []string
}

func (f *fakeDeletionClient) CompleteDeletion(_ context.Context, namespace, resourceVersion string) error {
	f.calls = append(f.calls, namespace)
	f.rvs = append(f.rvs, resourceVersion)
	return f.err
}

func namespaceKey(name string) types.WorkItemKey {
	return types.WorkItemKey{Kind: "Namespace", Name: name}
}

func seedNamespaceCache(t *testing.T, items ...Namespace) cache.CacheAccessor[Namespace] {
	t.Helper()
	c := cache.New[Namespace]()
	for _, item := range items {
		c.Set(namespaceKey(item.Name), item)
	}
	return cache.AsReadOnly(c)
}

func admittedCondition(generation int64) *status.Condition {
	return &status.Condition{Type: "AdmissionAccepted", Status: "TRUE", ObservedGeneration: generation}
}

func conditionByType(t *testing.T, conditions []*status.Condition, conditionType string) *status.Condition {
	t.Helper()
	for _, condition := range conditions {
		if condition != nil && condition.Type == conditionType {
			return condition
		}
	}
	t.Fatalf("condition %q not found in %+v", conditionType, conditions)
	return nil
}

func TestReconcileMissingNamespaceReturnsTerminal(t *testing.T) {
	sc := &fakeStatusClient{}
	r := NewReconciler(seedNamespaceCache(t), sc, &fakeRepositoryClient{}, &fakeDeletionClient{})

	result := r.Reconcile(context.Background(), namespaceKey("missing"))

	if _, ok := result.(types.TerminalFailure); !ok {
		t.Fatalf("Reconcile result = %T, want types.TerminalFailure", result)
	}
	if len(sc.patches) != 0 {
		t.Fatalf("status calls = %d, want 0", len(sc.patches))
	}
}

func TestReconcileAdmittedNamespaceProvisionsSystemRepositoryAndMarksReady(t *testing.T) {
	current := Namespace{
		Name:            "acme",
		Generation:      3,
		ResourceVersion: "7",
		Status: status.ResourceStatus{
			ResourceVersion: "7",
			Conditions:      []*status.Condition{admittedCondition(3)},
		},
	}
	sc := &fakeStatusClient{}
	repos := &fakeRepositoryClient{}
	r := NewReconciler(seedNamespaceCache(t, current), sc, repos, &fakeDeletionClient{})

	result := r.Reconcile(context.Background(), namespaceKey("acme"))

	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
	}
	if len(repos.ensured) != 1 || repos.ensured[0] != "acme" {
		t.Fatalf("ensured = %v, want [acme]", repos.ensured)
	}
	if len(sc.patches) != 1 {
		t.Fatalf("status calls = %d, want 1", len(sc.patches))
	}
	patch := sc.patches[0]
	if patch.ResourceVersion != "7" || patch.ObservedGeneration == nil || *patch.ObservedGeneration != 3 {
		t.Fatalf("patch version fields = %+v, want rv=7 generation=3", patch)
	}
	if got := conditionByType(t, patch.Conditions, "AdmissionAccepted").Status; got != "TRUE" {
		t.Errorf("AdmissionAccepted = %q, want TRUE", got)
	}
	if got := conditionByType(t, patch.Conditions, "SystemRepoReady").Status; got != "TRUE" {
		t.Errorf("SystemRepoReady = %q, want TRUE", got)
	}
	if got := conditionByType(t, patch.Conditions, "Ready").Status; got != "TRUE" {
		t.Errorf("Ready = %q, want TRUE", got)
	}
}

// TestReconcileFreshNamespaceProvisionsSystemRepositoryViaRealClient exercises
// the full Reconcile path with the real GraphQLRepositoryClient (not the
// fake) against a server that reproduces gitstore-api's actual response for
// a genuinely fresh environment: repository(by: namespacePath) returns a
// "repository not found" / NOT_FOUND GraphQL error because gitstore-system
// has never been created. Before the fix, systemRepositoryExists treated
// that error as a hard failure and EnsureSystemRepository never reached
// createRepository, so this reconcile would end in TransientFailure with
// SystemRepoReady=FALSE forever. After the fix, it must fall through to
// createRepository and reach Success with SystemRepoReady=TRUE.
func TestReconcileFreshNamespaceProvisionsSystemRepositoryViaRealClient(t *testing.T) {
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

	current := Namespace{
		Name:            "acme",
		Generation:      1,
		ResourceVersion: "1",
		Status: status.ResourceStatus{
			ResourceVersion: "1",
			Conditions:      []*status.Condition{admittedCondition(1)},
		},
	}
	sc := &fakeStatusClient{}
	repos := NewGraphQLRepositoryClient(graphqlclient.New(srv.URL, "token"))
	r := NewReconciler(seedNamespaceCache(t, current), sc, repos, &fakeDeletionClient{})

	result := r.Reconcile(context.Background(), namespaceKey("acme"))

	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %#v, want types.Success", result)
	}
	if queries != 1 || mutations != 1 {
		t.Fatalf("queries=%d mutations=%d, want 1/1 (createRepository must actually be called)", queries, mutations)
	}
	if len(sc.patches) != 1 {
		t.Fatalf("status calls = %d, want 1", len(sc.patches))
	}
	if got := conditionByType(t, sc.patches[0].Conditions, "SystemRepoReady").Status; got != "TRUE" {
		t.Errorf("SystemRepoReady = %q, want TRUE", got)
	}
}

func TestReconcileProvisionFailureWritesFalseConditionsAndRetries(t *testing.T) {
	provisionErr := errors.New("git service unavailable")
	current := Namespace{
		Name:            "acme",
		Generation:      1,
		ResourceVersion: "2",
		Status: status.ResourceStatus{
			ResourceVersion: "2",
			Conditions:      []*status.Condition{admittedCondition(1)},
		},
	}
	sc := &fakeStatusClient{}
	r := NewReconciler(
		seedNamespaceCache(t, current),
		sc,
		&fakeRepositoryClient{ensureErr: provisionErr},
		&fakeDeletionClient{},
	)

	result := r.Reconcile(context.Background(), namespaceKey("acme"))

	transient, ok := result.(types.TransientFailure)
	if !ok {
		t.Fatalf("Reconcile result = %T, want types.TransientFailure", result)
	}
	if !errors.Is(transient.Err, provisionErr) {
		t.Errorf("TransientFailure.Err = %v, want provision error", transient.Err)
	}
	if len(sc.patches) != 1 {
		t.Fatalf("status calls = %d, want 1", len(sc.patches))
	}
	if got := conditionByType(t, sc.patches[0].Conditions, "SystemRepoReady").Status; got != "FALSE" {
		t.Errorf("SystemRepoReady = %q, want FALSE", got)
	}
	if got := conditionByType(t, sc.patches[0].Conditions, "Ready").Status; got != "FALSE" {
		t.Errorf("Ready = %q, want FALSE", got)
	}
}

func TestReconcileReadyStatusNoOpDoesNotWriteAgain(t *testing.T) {
	current := Namespace{
		Name:            "acme",
		Generation:      2,
		ResourceVersion: "5",
		Status: status.ResourceStatus{
			ResourceVersion:    "5",
			ObservedGeneration: 2,
			Conditions: []*status.Condition{
				admittedCondition(2),
				{
					Type:               "SystemRepoReady",
					Status:             "TRUE",
					ObservedGeneration: 2,
					Reason:             "RepositoryReady",
					Message:            "per-namespace gitstore-system repository exists",
				},
				{
					Type:               "Ready",
					Status:             "TRUE",
					ObservedGeneration: 2,
					Reason:             "NamespaceReady",
					Message:            "namespace admission and system repository provisioning are complete",
				},
			},
		},
	}
	sc := &fakeStatusClient{}
	r := NewReconciler(seedNamespaceCache(t, current), sc, &fakeRepositoryClient{}, &fakeDeletionClient{})

	result := r.Reconcile(context.Background(), namespaceKey("acme"))

	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
	}
	if len(sc.patches) != 0 {
		t.Fatalf("status calls = %d, want 0 for a no-op patch", len(sc.patches))
	}
}

func TestReconcileWithoutAdmissionDoesNotProvision(t *testing.T) {
	current := Namespace{
		Name:            "acme",
		Generation:      1,
		ResourceVersion: "1",
		Status:          status.ResourceStatus{ResourceVersion: "1"},
	}
	sc := &fakeStatusClient{}
	repos := &fakeRepositoryClient{}
	r := NewReconciler(seedNamespaceCache(t, current), sc, repos, &fakeDeletionClient{})

	result := r.Reconcile(context.Background(), namespaceKey("acme"))

	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
	}
	if len(repos.ensured) != 0 {
		t.Fatalf("ensured = %v, want no provisioning before admission", repos.ensured)
	}
	if got := conditionByType(t, sc.patches[0].Conditions, "SystemRepoReady").Status; got != "FALSE" {
		t.Errorf("SystemRepoReady = %q, want FALSE", got)
	}
}

func TestReconcileBootstrapNamespacesAreIgnored(t *testing.T) {
	for _, name := range []string{"gitstore-system", "default"} {
		t.Run(name, func(t *testing.T) {
			current := Namespace{Name: name, Generation: 1, ResourceVersion: "1"}
			sc := &fakeStatusClient{}
			repos := &fakeRepositoryClient{}
			r := NewReconciler(seedNamespaceCache(t, current), sc, repos, &fakeDeletionClient{})

			result := r.Reconcile(context.Background(), namespaceKey(name))

			if _, ok := result.(types.Success); !ok {
				t.Fatalf("Reconcile result = %T, want types.Success", result)
			}
			if len(repos.ensured) != 0 || len(sc.patches) != 0 {
				t.Fatalf("bootstrap reconcile ensured=%v statusCalls=%d, want no controller writes", repos.ensured, len(sc.patches))
			}
		})
	}
}

func TestReconcileTerminatingNamespaceRequeuesWhileRepositoriesRemain(t *testing.T) {
	current := Namespace{
		Name:            "acme",
		ResourceVersion: "9",
		Finalizers:      []string{ForegroundDeletionFinalizer},
	}
	repos := &fakeRepositoryClient{hasRepos: true}
	deletion := &fakeDeletionClient{}
	r := NewReconciler(seedNamespaceCache(t, current), &fakeStatusClient{}, repos, deletion)

	result := r.Reconcile(context.Background(), namespaceKey("acme"))

	requeue, ok := result.(types.RequeueAfter)
	if !ok || requeue.After <= 0 {
		t.Fatalf("Reconcile result = %#v, want positive types.RequeueAfter", result)
	}
	if len(deletion.calls) != 0 {
		t.Fatalf("CompleteDeletion calls = %v, want none while repositories remain", deletion.calls)
	}
}

func TestReconcileTerminatingNamespaceCompletesDeletionWhenEmpty(t *testing.T) {
	current := Namespace{
		Name:            "acme",
		ResourceVersion: "9",
		Finalizers:      []string{ForegroundDeletionFinalizer},
	}
	repos := &fakeRepositoryClient{}
	deletion := &fakeDeletionClient{}
	r := NewReconciler(seedNamespaceCache(t, current), &fakeStatusClient{}, repos, deletion)

	result := r.Reconcile(context.Background(), namespaceKey("acme"))

	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
	}
	if len(deletion.calls) != 1 || deletion.calls[0] != "acme" || deletion.rvs[0] != "9" {
		t.Fatalf("CompleteDeletion calls=%v rvs=%v, want acme/9", deletion.calls, deletion.rvs)
	}
}

func TestReconcileTerminatingNamespaceDeletionFailureIsTransient(t *testing.T) {
	completeErr := errors.New("completion unavailable")
	current := Namespace{
		Name:            "acme",
		ResourceVersion: "9",
		Finalizers:      []string{ForegroundDeletionFinalizer},
	}
	r := NewReconciler(
		seedNamespaceCache(t, current),
		&fakeStatusClient{},
		&fakeRepositoryClient{},
		&fakeDeletionClient{err: completeErr},
	)

	result := r.Reconcile(context.Background(), namespaceKey("acme"))

	transient, ok := result.(types.TransientFailure)
	if !ok || !errors.Is(transient.Err, completeErr) {
		t.Fatalf("Reconcile result = %#v, want transient completion error", result)
	}
}
