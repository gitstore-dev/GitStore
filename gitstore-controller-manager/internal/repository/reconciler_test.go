// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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
	calls []struct{ namespace, name, resourceVersion, uid string }
	err   error
}

func (f *fakeCompletionClient) CompleteDeletion(_ context.Context, namespace, name, resourceVersion, uid string) error {
	f.calls = append(f.calls, struct{ namespace, name, resourceVersion, uid string }{namespace, name, resourceVersion, uid})
	return f.err
}

func repositoryKey(namespace, name string) types.WorkItemKey {
	return types.WorkItemKey{Kind: "Repository", Namespace: namespace, Name: name}
}

func reconcileRepositorySnapshot(t *testing.T, r *Reconciler, key types.WorkItemKey, recording bool) (types.ReconcileResult, []sdktrace.Event) {
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

func requireRepositorySnapshot(t *testing.T, events []sdktrace.Event, current Repository, exact, invalid bool) {
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
	key := repositoryKey("acme", "catalog")
	current := Repository{UID: "old-uid", Namespace: key.Namespace, Name: key.Name, Generation: 3, ResourceVersion: "7",
		Revision: "main@sha1:" + strings.Repeat("a", 40),
		Status:   status.ResourceStatus{LastAppliedRevision: "stale-status-revision"}}
	c := cache.New[Repository]()
	c.Set(key, current)
	statuses, completion := &fakeStatusClient{}, &fakeCompletionClient{}
	r := NewReconcilerWithLookup(cache.LookupFrom(cache.AsReadOnly(c)), statuses, &fakeStorageClient{}, completion)
	result, events := reconcileRepositorySnapshot(t, r, key, true)
	require.IsType(t, types.Success{}, result)
	requireRepositorySnapshot(t, events, current, true, false)
	require.Len(t, statuses.patches, 1)
	require.Equal(t, current.ResourceVersion, statuses.patches[0].ResourceVersion)
	require.Equal(t, current.Generation, *statuses.patches[0].ObservedGeneration)
	require.Nil(t, statuses.patches[0].LastAppliedRevision)

	// The key is level-triggered: intermediate versions need not be reconciled.
	current.ResourceVersion, current.Revision = "8", "main@sha1:"+strings.Repeat("b", 40)
	c.Set(key, current)
	current.ResourceVersion, current.Revision = "9", "main@sha1:"+strings.Repeat("c", 40)
	c.Set(key, current)
	result, events = reconcileRepositorySnapshot(t, r, key, true)
	require.IsType(t, types.Success{}, result)
	requireRepositorySnapshot(t, events, current, true, false)
	require.Len(t, statuses.patches, 2)
	require.Equal(t, "9", statuses.patches[1].ResourceVersion)
	require.Equal(t, int64(3), *statuses.patches[1].ObservedGeneration)

	current.UID, current.ResourceVersion = "replacement-uid", "10"
	current.Finalizers = []string{ForegroundDeletionFinalizer}
	c.Set(key, current)
	result, events = reconcileRepositorySnapshot(t, r, key, true)
	require.IsType(t, types.Success{}, result)
	requireRepositorySnapshot(t, events, current, true, false)
	require.Len(t, completion.calls, 1)
	require.Equal(t, current.UID, completion.calls[0].uid)
	require.Equal(t, current.ResourceVersion, completion.calls[0].resourceVersion)
}

func TestReconcileSnapshotPreservesOutcomes(t *testing.T) {
	failure := errors.New("dependency unavailable")
	for _, test := range []struct {
		name                                              string
		missing, system, deleting                         bool
		lookupErr, statusErr, provisionErr, completionErr error
		requeue                                           time.Duration
	}{
		{name: "active"},
		{name: "system", system: true},
		{name: "missing", missing: true},
		{name: "lookup I/O error", lookupErr: failure},
		{name: "lookup cancelled", lookupErr: context.Canceled},
		{name: "status error", statusErr: failure},
		{name: "provision error", provisionErr: failure},
		{name: "status conflict", statusErr: types.ErrConflict, requeue: conflictRequeueDelay},
		{name: "status rate limit", statusErr: types.ErrRateLimited, requeue: rateLimitRequeueDelay},
		{name: "provision rate limit", provisionErr: types.ErrRateLimited, requeue: rateLimitRequeueDelay},
		{name: "deletion", deleting: true},
		{name: "deletion error", deleting: true, completionErr: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, recording := range []bool{false, true} {
				current := Repository{UID: "uid", Namespace: "acme", Name: "catalog", ResourceVersion: "7", Generation: 3,
					Revision: "main@sha1:" + strings.Repeat("a", 40),
					Status:   status.ResourceStatus{Conditions: []*status.Condition{admissionAccepted(3)}}}
				if test.system {
					current.Name = SystemRepositoryName
				}
				if test.deleting {
					current.Finalizers = []string{ForegroundDeletionFinalizer}
				}
				statuses, storage, completion := &fakeStatusClient{err: test.statusErr}, &fakeStorageClient{err: test.provisionErr}, &fakeCompletionClient{err: test.completionErr}
				r := NewReconcilerWithLookup(func(context.Context, types.WorkItemKey) (Repository, bool, error) {
					return current, !test.missing, test.lookupErr
				}, statuses, storage, completion)
				result, events := reconcileRepositorySnapshot(t, r, repositoryKey(current.Namespace, current.Name), recording)
				if recording && !test.missing && test.lookupErr == nil {
					requireRepositorySnapshot(t, events, current, true, false)
				} else {
					require.Empty(t, events)
				}
				switch {
				case test.requeue > 0:
					require.Equal(t, types.RequeueAfter{After: test.requeue}, result)
				case errors.Join(test.lookupErr, test.statusErr, test.provisionErr, test.completionErr) != nil:
					require.IsType(t, types.TransientFailure{}, result)
					for _, err := range []error{test.lookupErr, test.statusErr, test.provisionErr, test.completionErr} {
						if err != nil {
							require.ErrorIs(t, result.(types.TransientFailure).Err, err)
						}
					}
				default:
					require.IsType(t, types.Success{}, result)
				}
				if test.missing || test.lookupErr != nil || test.system {
					require.Empty(t, statuses.patches)
					require.Empty(t, storage.calls)
					require.Empty(t, completion.calls)
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
			current := Repository{UID: test.uid, Namespace: "acme", Name: "catalog", ResourceVersion: test.rv, Generation: 3, Revision: test.revision,
				Status: status.ResourceStatus{LastAppliedRevision: "not-authored"}}
			for _, recording := range []bool{false, true} {
				statuses := &fakeStatusClient{}
				r := NewReconcilerWithLookup(func(context.Context, types.WorkItemKey) (Repository, bool, error) {
					return current, true, nil
				}, statuses, &fakeStorageClient{}, &fakeCompletionClient{})
				result, events := reconcileRepositorySnapshot(t, r, repositoryKey(current.Namespace, current.Name), recording)
				require.IsType(t, types.Success{}, result)
				require.Len(t, statuses.patches, 1)
				require.Equal(t, current.ResourceVersion, statuses.patches[0].ResourceVersion)
				require.Equal(t, current.Generation, *statuses.patches[0].ObservedGeneration)
				require.Nil(t, statuses.patches[0].LastAppliedRevision)
				if recording {
					requireRepositorySnapshot(t, events, current, test.exact, test.invalid)
				}
			}
		})
	}
}

func TestReconcileSnapshotNonrecordingDoesNotAllocate(t *testing.T) {
	r := NewReconcilerWithLookup(func(context.Context, types.WorkItemKey) (Repository, bool, error) {
		return Repository{Name: SystemRepositoryName}, true, nil
	}, nil, nil, nil)
	require.Zero(t, testing.AllocsPerRun(100, func() {
		r.Reconcile(context.Background(), repositoryKey("acme", SystemRepositoryName))
	}))
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

func repositoryFixtureAlreadyProvisioned(t *testing.T) (Repository, json.RawMessage) {
	t.Helper()
	resolved := ResolvedStorage{StoragePath: "/data/acme/catalog.git", StorageClass: "standard"}
	resolvedJSON, err := json.Marshal(resolved)
	if err != nil {
		t.Fatalf("marshal resolved: %v", err)
	}
	current := repositoryFixture(func(repository *Repository) {
		repository.Status = status.ResourceStatus{
			ResourceVersion:    "2",
			ObservedGeneration: 1,
			Conditions: []*status.Condition{
				admissionAccepted(1),
				{Type: conditionStorageProvisioned, Status: statusTrue, ObservedGeneration: 1, Reason: "StorageReady", Message: "bare Git repository exists"},
				{Type: conditionReady, Status: statusTrue, ObservedGeneration: 1, Reason: "RepositoryReady", Message: "repository admission and storage provisioning are complete"},
			},
			Resolved: resolvedJSON,
		}
	})
	return current, resolvedJSON
}

func TestReconcileSkipsStorageProvisioningWhenRecentlyVerified(t *testing.T) {
	current, _ := repositoryFixtureAlreadyProvisioned(t)
	storage := &fakeStorageClient{}
	statuses := &fakeStatusClient{}
	r := NewReconciler(seedRepositoryCache(t, current), statuses, storage, &fakeCompletionClient{})
	key := repositoryKey("acme", "catalog")
	// Simulate having already verified storage recently (e.g. the previous
	// reconcile): storageVerificationDue must not fire again immediately.
	r.lastStorageVerification.Store(key, current.UID, time.Now())

	result := r.Reconcile(context.Background(), key)
	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
	}
	if len(storage.calls) != 0 {
		t.Fatalf("storage calls = %#v, want none: a recently verified repository must not re-invoke EnsureStorage", storage.calls)
	}
	if len(statuses.patches) != 0 {
		t.Fatalf("status patches = %#v, want none: an unchanged status must not be re-applied", statuses.patches)
	}
}

func TestReconcileRevalidatesStorageWhenNeverVerifiedByThisProcess(t *testing.T) {
	current, resolvedJSON := repositoryFixtureAlreadyProvisioned(t)
	var gotResolved ResolvedStorage
	if err := json.Unmarshal(resolvedJSON, &gotResolved); err != nil {
		t.Fatalf("unmarshal resolvedJSON: %v", err)
	}
	storage := &fakeStorageClient{resolved: gotResolved}
	statuses := &fakeStatusClient{}
	// A freshly constructed reconciler (e.g. right after a controller
	// restart) has never verified this key in-process, even though the
	// cached condition says StorageProvisioned=True. It must call
	// EnsureStorage once to confirm the storage still exists (a lost git-
	// service volume would otherwise leave Ready=True forever with no
	// self-heal path).
	r := NewReconciler(seedRepositoryCache(t, current), statuses, storage, &fakeCompletionClient{})

	result := r.Reconcile(context.Background(), repositoryKey("acme", "catalog"))
	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
	}
	if len(storage.calls) != 1 {
		t.Fatalf("storage calls = %#v, want exactly one revalidation call", storage.calls)
	}
}

func TestReconcileReplacementCannotBorrowPreviousUIDVerification(t *testing.T) {
	for _, provisionErr := range []error{nil, errors.New("storage unavailable")} {
		t.Run(fmt.Sprint(provisionErr), func(t *testing.T) {
			current, resolvedJSON := repositoryFixtureAlreadyProvisioned(t)
			current.UID = "replacement"
			var resolved ResolvedStorage
			if err := json.Unmarshal(resolvedJSON, &resolved); err != nil {
				t.Fatal(err)
			}
			storage := &fakeStorageClient{resolved: resolved, err: provisionErr}
			r := NewReconciler(seedRepositoryCache(t, current), &fakeStatusClient{}, storage, &fakeCompletionClient{})
			key := repositoryKey(current.Namespace, current.Name)
			r.lastStorageVerification.Store(key, "retired", time.Now())
			result := r.Reconcile(t.Context(), key)
			if len(storage.calls) != 1 {
				t.Fatalf("replacement borrowed another UID's verification: calls=%v", storage.calls)
			}
			_, verified := r.lastStorageVerification.Load(key, current.UID)
			if provisionErr != nil {
				if _, ok := result.(types.TransientFailure); !ok || verified {
					t.Fatalf("failed replacement verification returned %T, verified=%v", result, verified)
				}
			} else if _, ok := result.(types.Success); !ok || !verified {
				t.Fatalf("successful replacement verification returned %T, verified=%v", result, verified)
			}
			r.Reconcile(t.Context(), key)
			want := 1
			if provisionErr != nil {
				want = 2
			}
			if len(storage.calls) != want {
				t.Fatalf("subsequent replacement verification calls=%d, want %d", len(storage.calls), want)
			}
		})
	}
}

func TestStorageVerificationCacheKeepsBoundAndReplacesUIDInPlace(t *testing.T) {
	var c storageVerificationCache
	for i := range 4097 {
		key := repositoryKey("acme", fmt.Sprint(i))
		c.Store(key, "first", time.Now())
		if len(c.entries) > 4096 {
			t.Fatalf("verification cache exceeded its bound: %d", len(c.entries))
		}
	}
	key := repositoryKey("acme", "4096")
	c.Store(key, "replacement", time.Now())
	if len(c.entries) != 4096 {
		t.Fatalf("UID replacement changed the cache cardinality: %d", len(c.entries))
	}
	if _, ok := c.Load(key, "first"); ok {
		t.Fatal("a retired UID borrowed the replacement's verification")
	}
	if _, ok := c.Load(key, "replacement"); !ok {
		t.Fatal("replacement verification was not retained")
	}
	for i := range 4097 {
		key := repositoryKey("acme", fmt.Sprint(i))
		if _, retained := c.entries[key]; !retained {
			if _, verified := c.Load(key, "first"); verified {
				t.Fatal("eviction retained a trusted verification")
			}
			return
		}
	}
	t.Fatal("bounded cache did not evict an entry")
}

func TestReconcileRevalidatesStorageAfterRevalidationWindowElapses(t *testing.T) {
	current, resolvedJSON := repositoryFixtureAlreadyProvisioned(t)
	var gotResolved ResolvedStorage
	if err := json.Unmarshal(resolvedJSON, &gotResolved); err != nil {
		t.Fatalf("unmarshal resolvedJSON: %v", err)
	}
	storage := &fakeStorageClient{resolved: gotResolved}
	statuses := &fakeStatusClient{}
	r := NewReconciler(seedRepositoryCache(t, current), statuses, storage, &fakeCompletionClient{})
	key := repositoryKey("acme", "catalog")
	r.lastStorageVerification.Store(key, current.UID, time.Now().Add(-storageRevalidationInterval-time.Second))

	result := r.Reconcile(context.Background(), key)
	if _, ok := result.(types.Success); !ok {
		t.Fatalf("Reconcile result = %T, want types.Success", result)
	}
	if len(storage.calls) != 1 {
		t.Fatalf("storage calls = %#v, want exactly one revalidation call once the window elapsed", storage.calls)
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

	if len(completion.calls) != 1 || completion.calls[0] != (struct{ namespace, name, resourceVersion, uid string }{"acme", "catalog", "9", current.UID}) {
		t.Fatalf("completion calls = %#v, want acme/catalog/9", completion.calls)
	}
}

func TestReconcilePendingRepositoryResumesDeletionWithoutProvisioning(t *testing.T) {
	current := repositoryFixture(func(repository *Repository) {
		repository.Status.Conditions = []*status.Condition{{Type: "DeletionPending", Status: "TRUE"}}
	})
	storage := &fakeStorageClient{}
	completion := &fakeCompletionClient{}
	r := NewReconciler(seedRepositoryCache(t, current), &fakeStatusClient{}, storage, completion)
	if _, ok := r.Reconcile(context.Background(), repositoryKey(current.Namespace, current.Name)).(types.Success); !ok {
		t.Fatal("pending deletion did not complete")
	}
	if len(completion.calls) != 1 || completion.calls[0].uid != current.UID || len(storage.calls) != 0 {
		t.Fatalf("completion=%v storage=%v", completion.calls, storage.calls)
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
