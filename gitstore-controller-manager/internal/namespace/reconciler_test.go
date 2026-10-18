// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package namespace

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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
	uids  []string
}

func (f *fakeDeletionClient) CompleteDeletion(_ context.Context, namespace, resourceVersion, uid string) error {
	f.calls = append(f.calls, namespace)
	f.rvs = append(f.rvs, resourceVersion)
	f.uids = append(f.uids, uid)
	return f.err
}

func namespaceKey(name string) types.WorkItemKey {
	return types.WorkItemKey{Kind: "Namespace", Name: name}
}

func reconcileNamespaceSnapshot(t *testing.T, r *Reconciler, key types.WorkItemKey, recording bool) (types.ReconcileResult, []sdktrace.Event) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	sampler := sdktrace.NeverSample()
	if recording {
		sampler = sdktrace.AlwaysSample()
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sampler), sdktrace.WithSpanProcessor(recorder),
		sdktrace.WithRawSpanLimits(sdktrace.SpanLimits{
			AttributeValueLengthLimit: 256, AttributeCountLimit: 32,
			EventCountLimit: 32, LinkCountLimit: 8,
			AttributePerEventCountLimit: 8, AttributePerLinkCountLimit: 8,
		}))
	defer func() { require.NoError(t, provider.Shutdown(context.Background())) }()
	ctx, span := provider.Tracer("test").Start(t.Context(), "controller.reconcile")
	result := r.Reconcile(ctx, key)
	span.End()
	if !recording {
		require.Empty(t, recorder.Ended())
		return result, nil
	}
	require.Len(t, recorder.Ended(), 1)
	return result, recorder.Ended()[0].Events()
}

func requireNamespaceSnapshot(t *testing.T, events []sdktrace.Event, current Namespace, exact, invalid bool) {
	t.Helper()
	require.Len(t, events, 1)
	require.Equal(t, "controller.reconcile.snapshot", events[0].Name)
	uid, rv, revision := current.UID, current.ResourceVersion, current.Revision
	if invalid {
		uid, rv, revision = "[invalid]", "[invalid]", "[invalid]"
	}
	attributes := make(map[string]attribute.Value)
	for _, attr := range events[0].Attributes {
		attributes[string(attr.Key)] = attr.Value
	}
	require.Len(t, events[0].Attributes, 5)
	require.Equal(t, map[string]attribute.Value{
		"resource.uid":              attribute.StringValue(uid),
		"resource.resource_version": attribute.StringValue(rv),
		"resource.generation":       attribute.Int64Value(current.Generation),
		"resource.revision":         attribute.StringValue(revision),
		"resource.snapshot.exact":   attribute.BoolValue(exact),
	}, attributes)
}

func TestReconcileSnapshotUsesLookupAndAllowsCoalescing(t *testing.T) {
	key := namespaceKey("acme")
	current := Namespace{
		UID: "old-uid", Name: key.Name, Generation: 3, ResourceVersion: "7",
		Revision: "main@sha1:" + strings.Repeat("a", 40),
		Status:   status.ResourceStatus{LastAppliedRevision: "stale-status-revision"},
	}
	c := cache.New[Namespace]()
	c.Set(key, current)
	statuses, deletion := &fakeStatusClient{}, &fakeDeletionClient{}
	r := NewReconcilerWithLookup(cache.LookupFrom(cache.AsReadOnly(c)), statuses, &fakeRepositoryClient{}, deletion)
	result, events := reconcileNamespaceSnapshot(t, r, key, true)
	require.IsType(t, types.Success{}, result)
	requireNamespaceSnapshot(t, events, current, true, false)
	require.Len(t, statuses.patches, 1)
	require.Equal(t, current.ResourceVersion, statuses.patches[0].ResourceVersion)
	require.Equal(t, current.Generation, *statuses.patches[0].ObservedGeneration)
	require.Nil(t, statuses.patches[0].LastAppliedRevision)

	// The key is level-triggered: intermediate versions need not be reconciled.
	current.ResourceVersion, current.Revision = "8", "main@sha1:"+strings.Repeat("b", 40)
	c.Set(key, current)
	current.ResourceVersion, current.Revision = "9", "main@sha1:"+strings.Repeat("c", 40)
	c.Set(key, current)
	result, events = reconcileNamespaceSnapshot(t, r, key, true)
	require.IsType(t, types.Success{}, result)
	requireNamespaceSnapshot(t, events, current, true, false)
	require.Len(t, statuses.patches, 2)
	require.Equal(t, "9", statuses.patches[1].ResourceVersion)
	require.Equal(t, int64(3), *statuses.patches[1].ObservedGeneration)

	current.UID, current.ResourceVersion = "replacement-uid", "10"
	current.Finalizers = []string{ForegroundDeletionFinalizer}
	c.Set(key, current)
	result, events = reconcileNamespaceSnapshot(t, r, key, true)
	require.IsType(t, types.Success{}, result)
	requireNamespaceSnapshot(t, events, current, true, false)
	require.Equal(t, []string{current.UID}, deletion.uids)
	require.Equal(t, []string{current.ResourceVersion}, deletion.rvs)
}

func TestReconcileSnapshotPreservesOutcomes(t *testing.T) {
	failure := errors.New("dependency unavailable")
	for _, test := range []struct {
		name                                            string
		missing, bootstrap, deleting                    bool
		lookupErr, statusErr, provisionErr, deletionErr error
	}{
		{name: "active"},
		{name: "bootstrap", bootstrap: true},
		{name: "missing", missing: true},
		{name: "lookup I/O error", lookupErr: failure},
		{name: "lookup cancelled", lookupErr: context.Canceled},
		{name: "status error", statusErr: failure},
		{name: "provision error", provisionErr: failure},
		{name: "deletion", deleting: true},
		{name: "deletion error", deleting: true, deletionErr: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			var baseline types.ReconcileResult
			for _, recording := range []bool{false, true} {
				current := Namespace{UID: "uid", Name: "acme", ResourceVersion: "7", Generation: 3,
					Revision: "main@sha1:" + strings.Repeat("a", 40),
					Status:   status.ResourceStatus{Conditions: []*status.Condition{admittedCondition(3)}}}
				if test.bootstrap {
					current.Name = "default"
				}
				if test.deleting {
					current.Finalizers = []string{ForegroundDeletionFinalizer}
				}
				statuses, repos, deletion := &fakeStatusClient{err: test.statusErr}, &fakeRepositoryClient{ensureErr: test.provisionErr}, &fakeDeletionClient{err: test.deletionErr}
				r := NewReconcilerWithLookup(func(context.Context, types.WorkItemKey) (Namespace, bool, error) {
					return current, !test.missing, test.lookupErr
				}, statuses, repos, deletion)
				result, events := reconcileNamespaceSnapshot(t, r, namespaceKey(current.Name), recording)
				if recording && !test.missing && test.lookupErr == nil {
					requireNamespaceSnapshot(t, events, current, true, false)
				} else {
					require.Empty(t, events)
				}
				expectedErr := errors.Join(test.lookupErr, test.statusErr, test.provisionErr, test.deletionErr)
				if expectedErr == nil {
					require.IsType(t, types.Success{}, result)
				} else {
					require.IsType(t, types.TransientFailure{}, result)
					for _, err := range []error{test.lookupErr, test.statusErr, test.provisionErr, test.deletionErr} {
						if err != nil {
							require.ErrorIs(t, result.(types.TransientFailure).Err, err)
						}
					}
				}
				if recording {
					require.Equal(t, reflect.TypeOf(baseline), reflect.TypeOf(result))
				} else {
					baseline = result
				}
				if test.missing || test.lookupErr != nil || test.bootstrap {
					require.Empty(t, statuses.patches)
					require.Empty(t, repos.ensured)
					require.Empty(t, deletion.calls)
				}
			}
		})
	}
}

func TestReconcileSnapshotBoundsDoNotChangeConsumedObject(t *testing.T) {
	for _, test := range []struct {
		name              string
		uid, rv, revision string
		exact, invalid    bool
	}{
		{"maximum", strings.Repeat("u", 64), strings.Repeat("v", 128), strings.Repeat("r", 256), true, false},
		{"uid overflow", strings.Repeat("u", 65), "7", "revision", false, true},
		{"rv overflow", "uid", strings.Repeat("v", 129), "revision", false, true},
		{"revision overflow", "uid", "7", strings.Repeat("r", 257), false, true},
		{"invalid UTF-8", "uid", "7", "\xff", false, true},
		{"absent authored revision", "uid", "7", "", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := Namespace{UID: test.uid, Name: "acme", ResourceVersion: test.rv, Generation: 3, Revision: test.revision,
				Status: status.ResourceStatus{LastAppliedRevision: "not-authored"}}
			for _, recording := range []bool{false, true} {
				statuses := &fakeStatusClient{}
				r := NewReconcilerWithLookup(func(context.Context, types.WorkItemKey) (Namespace, bool, error) {
					return current, true, nil
				}, statuses, &fakeRepositoryClient{}, &fakeDeletionClient{})
				result, events := reconcileNamespaceSnapshot(t, r, namespaceKey(current.Name), recording)
				require.IsType(t, types.Success{}, result)
				require.Len(t, statuses.patches, 1)
				require.Equal(t, current.ResourceVersion, statuses.patches[0].ResourceVersion)
				require.Equal(t, current.Generation, *statuses.patches[0].ObservedGeneration)
				require.Nil(t, statuses.patches[0].LastAppliedRevision)
				if recording {
					requireNamespaceSnapshot(t, events, current, test.exact, test.invalid)
				}
			}
		})
	}
}

func TestReconcileSnapshotNonrecordingDoesNotAllocate(t *testing.T) {
	r := NewReconcilerWithLookup(func(context.Context, types.WorkItemKey) (Namespace, bool, error) {
		return Namespace{Name: "default"}, true, nil
	}, nil, nil, nil)
	require.Zero(t, testing.AllocsPerRun(100, func() {
		r.Reconcile(context.Background(), namespaceKey("default"))
	}))
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

func TestReconcileMissingNamespaceIsAlreadyReconciled(t *testing.T) {
	sc := &fakeStatusClient{}
	r := NewReconciler(seedNamespaceCache(t), sc, &fakeRepositoryClient{}, &fakeDeletionClient{})

	result := r.Reconcile(context.Background(), namespaceKey("missing"))

	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
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
// The controller uses the dedicated bootstrap operation, rather than the
// author-facing createRepository mutation. The API owns the idempotent
// lookup/create race, so a fresh Namespace reaches Success directly.
func TestReconcileFreshNamespaceProvisionsSystemRepositoryViaRealClient(t *testing.T) {
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
	repos := NewGraphQLRepositoryClient(graphqlclient.New(srv.URL, graphqlclient.NewStaticToken("token")))
	r := NewReconciler(seedNamespaceCache(t, current), sc, repos, &fakeDeletionClient{})

	result := r.Reconcile(context.Background(), namespaceKey("acme"))

	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %#v, want types.Success", result)
	}
	if mutations != 1 {
		t.Fatalf("mutations=%d, want 1", mutations)
	}
	if len(sc.patches) != 1 {
		t.Fatalf("status calls = %d, want 1", len(sc.patches))
	}
	if got := conditionByType(t, sc.patches[0].Conditions, "SystemRepoReady").Status; got != "TRUE" {
		t.Errorf("SystemRepoReady = %q, want TRUE", got)
	}
}

func TestReconcilePreservesAdmissionAcceptedWhileUpdatingControllerConditions(t *testing.T) {
	acceptedAt := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	admission := &status.Condition{
		Type:               "AdmissionAccepted",
		Status:             "TRUE",
		ObservedGeneration: 4,
		LastTransitionTime: acceptedAt,
		Reason:             "AdmittedByHookPipeline",
		Message:            "Namespace manifest admitted successfully.",
	}
	current := Namespace{
		Name:            "acme",
		Generation:      4,
		ResourceVersion: "11",
		Status: status.ResourceStatus{
			ResourceVersion:    "11",
			ObservedGeneration: 4,
			Conditions: []*status.Condition{
				admission,
				{Type: "SystemRepoReady", Status: "FALSE", ObservedGeneration: 3},
				{Type: "Ready", Status: "FALSE", ObservedGeneration: 3},
			},
		},
	}
	sc := &fakeStatusClient{}
	r := NewReconciler(seedNamespaceCache(t, current), sc, &fakeRepositoryClient{}, &fakeDeletionClient{})

	result := r.Reconcile(context.Background(), namespaceKey("acme"))

	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
	}
	if len(sc.patches) != 1 {
		t.Fatalf("status calls = %d, want 1", len(sc.patches))
	}
	patch := sc.patches[0]
	preserved := conditionByType(t, patch.Conditions, "AdmissionAccepted")
	if preserved == admission {
		t.Fatal("AdmissionAccepted condition was not copied")
	}
	if *preserved != *admission {
		t.Errorf("AdmissionAccepted = %+v, want %+v", preserved, admission)
	}
	if got := conditionByType(t, patch.Conditions, "SystemRepoReady"); got.Status != "TRUE" || got.ObservedGeneration != 4 {
		t.Errorf("SystemRepoReady = %+v, want TRUE at generation 4", got)
	}
	if got := conditionByType(t, patch.Conditions, "Ready"); got.Status != "TRUE" || got.ObservedGeneration != 4 {
		t.Errorf("Ready = %+v, want TRUE at generation 4", got)
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

func TestReconcileTerminatingNamespaceDelegatesSystemRepositoryCleanup(t *testing.T) {
	current := Namespace{
		Name:            "acme",
		ResourceVersion: "9",
		Finalizers:      []string{ForegroundDeletionFinalizer},
	}

	repos := &fakeRepositoryClient{hasRepos: true}
	deletion := &fakeDeletionClient{}
	r := NewReconciler(seedNamespaceCache(t, current), &fakeStatusClient{}, repos, deletion)

	result := r.Reconcile(context.Background(), namespaceKey("acme"))

	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %#v, want success", result)
	}
	if len(deletion.calls) != 1 {
		t.Fatalf("CompleteDeletion calls = %v, want API-owned system repository cleanup", deletion.calls)
	}
}

func TestReconcilePendingNamespaceResumesDeletionWithoutProvisioning(t *testing.T) {
	current := Namespace{
		UID: "namespace-incarnation", Name: "acme", ResourceVersion: "9",
		Status: status.ResourceStatus{Conditions: []*status.Condition{{Type: "DeletionPending", Status: "TRUE"}}},
	}
	repos := &fakeRepositoryClient{}
	deletion := &fakeDeletionClient{}
	r := NewReconciler(seedNamespaceCache(t, current), &fakeStatusClient{}, repos, deletion)
	if _, ok := r.Reconcile(context.Background(), namespaceKey("acme")).(types.Success); !ok {
		t.Fatal("pending deletion did not complete")
	}
	if len(deletion.calls) != 1 || deletion.uids[0] != current.UID || len(repos.ensured) != 0 {
		t.Fatalf("pending deletion calls=%v uids=%v provisioning=%v", deletion.calls, deletion.uids, repos.ensured)
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
