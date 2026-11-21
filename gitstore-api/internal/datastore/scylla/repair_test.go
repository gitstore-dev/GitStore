// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
)

func TestReadRBACWriteResourcesOverlapsReadsAndPreservesAbsence(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	write := rbac.TierWrite{TierID: rbac.ClusterTierID, Changes: make([]rbac.Change, 129)}
	for index := range write.Changes {
		write.Changes[index].Resource = &rbac.Resource{Kind: rbac.ClusterRole,
			Metadata: rbac.Metadata{Name: "role-" + strconv.Itoa(index)}}
	}
	entered, release := make(chan struct{}, len(write.Changes)), make(chan struct{})
	defer close(release)
	done := make(chan error, 1)
	var current map[rbac.ResourceKey]*rbac.Resource
	go func() {
		var err error
		current, err = readRBACWriteResources(ctx, write, func(ctx context.Context, keys []rbac.ResourceKey) (map[rbac.ResourceKey]*rbac.Resource, error) {
			if len(keys) == 0 || len(keys) > 32 {
				t.Errorf("unbounded candidate batch: %d", len(keys))
			}
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			resources := make(map[rbac.ResourceKey]*rbac.Resource)
			for _, key := range keys {
				if key.Name != "role-0" {
					resources[key] = &rbac.Resource{Kind: key.Kind, Metadata: rbac.Metadata{Name: key.Name}}
				}
			}
			return resources, nil
		})
		done <- err
	}()
	for range 4 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("independent candidate reads did not overlap")
		}
	}
	select {
	case <-entered:
		t.Fatal("more than four candidate batches overlapped")
	default:
	}
	for range 5 {
		release <- struct{}{}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for index, change := range write.Changes {
		key := rbac.ResourceKey{TierID: write.TierID, Kind: change.Resource.Kind, Name: change.Resource.Metadata.Name}
		resource, found := current[key]
		if !found || (index == 0) != (resource == nil) {
			t.Fatalf("candidate read lost presence/absence for %s", key.Name)
		}
		if resource != nil && resource.Metadata.Name != key.Name {
			t.Fatalf("candidate read substituted another resource for %s", key.Name)
		}
	}
}

func TestReadRBACWriteResourcesFailureCannotReturnPartialCandidate(t *testing.T) {
	want := errors.New("candidate lookup failed")
	write := rbac.TierWrite{TierID: rbac.ClusterTierID, Changes: []rbac.Change{
		{Resource: &rbac.Resource{Kind: rbac.ClusterRole, Metadata: rbac.Metadata{Name: "first"}}},
		{Resource: &rbac.Resource{Kind: rbac.ClusterRole, Metadata: rbac.Metadata{Name: "second"}}},
	}}
	current, err := readRBACWriteResources(t.Context(), write, func(context.Context, []rbac.ResourceKey) (map[rbac.ResourceKey]*rbac.Resource, error) {
		return nil, want
	})
	if !errors.Is(err, want) || current != nil {
		t.Fatalf("candidate lookup failure returned a partial model: current=%v err=%v", current, err)
	}
	write.Changes[1].Resource = nil
	current, err = readRBACWriteResources(t.Context(), write, func(context.Context, []rbac.ResourceKey) (map[rbac.ResourceKey]*rbac.Resource, error) {
		t.Error("malformed candidate initiated source I/O")
		return nil, nil
	})
	if !errors.Is(err, rbac.ErrInvalid) || current != nil {
		t.Fatalf("malformed candidate was not rejected before I/O: current=%v err=%v", current, err)
	}
}

func TestReadRBACWriteResourcesRejectsUnrelatedBatchRows(t *testing.T) {
	for _, name := range []string{"other", "requested"} {
		t.Run(name, func(t *testing.T) {
			write := rbac.TierWrite{TierID: rbac.ClusterTierID, Changes: []rbac.Change{
				{Resource: &rbac.Resource{Kind: rbac.ClusterRole, Metadata: rbac.Metadata{Name: "requested"}}},
			}}
			current, err := readRBACWriteResources(t.Context(), write, func(context.Context, []rbac.ResourceKey) (map[rbac.ResourceKey]*rbac.Resource, error) {
				key := rbac.ResourceKey{TierID: write.TierID, Kind: rbac.ClusterRole, Name: name}
				return map[rbac.ResourceKey]*rbac.Resource{key: {Kind: key.Kind, Metadata: rbac.Metadata{Name: "other"}}}, nil
			})
			if !errors.Is(err, rbac.ErrUnavailable) || current != nil {
				t.Fatalf("batch substituted an unrelated resource: current=%v err=%v", current, err)
			}
		})
	}
}

func TestRepairRBACStepsBoundsConcurrentWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	steps := make([]rbacRepairStep, 17)
	for index := range steps {
		steps[index].UID = strconv.Itoa(index)
	}
	entered, release := make(chan struct{}, len(steps)), make(chan struct{})
	defer close(release)
	var active, peak, completed atomic.Int64
	seen := make([]atomic.Int64, len(steps))
	done := make(chan error, 1)
	go func() {
		done <- runRBACSteps(ctx, steps, func(ctx context.Context, step rbacRepairStep) error {
			current := active.Add(1)
			for previous := peak.Load(); current > previous; previous = peak.Load() {
				if peak.CompareAndSwap(previous, current) {
					break
				}
			}
			defer active.Add(-1)
			index, err := strconv.Atoi(step.UID)
			if err != nil {
				return err
			}
			seen[index].Add(1)
			entered <- struct{}{}
			select {
			case <-release:
				completed.Add(1)
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	for range 4 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("independent repairs did not overlap")
		}
	}
	select {
	case <-entered:
		t.Fatal("more than four repairs started before any completed")
	case err := <-done:
		t.Fatalf("repair returned before its workers finished: %v", err)
	default:
	}
	for range len(steps) {
		release <- struct{}{}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 4 || active.Load() != 0 || completed.Load() != int64(len(steps)) {
		t.Fatalf("unbounded or unfinished repairs: peak=%d active=%d completed=%d", peak.Load(), active.Load(), completed.Load())
	}
	for index := range seen {
		if seen[index].Load() != 1 {
			t.Fatalf("resource %d repaired %d times", index, seen[index].Load())
		}
	}
}

func TestRepairRBACStepsFailureCancelsAndJoinsWorkers(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	steps := make([]rbacRepairStep, 17)
	for index := range steps {
		steps[index].UID = strconv.Itoa(index)
	}
	entered, fail := make(chan struct{}, len(steps)), make(chan struct{})
	var active, calls atomic.Int64
	want := errors.New("conditional lookup failed")
	done := make(chan error, 1)
	go func() {
		done <- runRBACSteps(ctx, steps, func(ctx context.Context, step rbacRepairStep) error {
			active.Add(1)
			calls.Add(1)
			defer active.Add(-1)
			entered <- struct{}{}
			if step.UID == "0" {
				select {
				case <-fail:
					return want
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	for range 4 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("repair workers did not start")
		}
	}
	close(fail)
	if err := <-done; !errors.Is(err, want) {
		t.Fatalf("lost repair failure: %v", err)
	}
	if active.Load() != 0 || calls.Load() != 4 {
		t.Fatalf("failed repair leaked or continued work: active=%d calls=%d", active.Load(), calls.Load())
	}
}

func TestRBACRepairCompletionRecognizesConcurrentCompletedRepair(t *testing.T) {
	before := rbac.TierState{TierID: rbac.ClusterTierID, Revision: 7, ProjectionPending: true,
		Projection: rbac.ProjectionIdentity{RepositoryID: "repository", GitRef: "refs/heads/main",
			GitRevision: "main@sha1:" + strings.Repeat("a", 40), TreeDigest: strings.Repeat("b", 64)}}
	for _, test := range []struct {
		name   string
		change func(*rbac.TierState)
		wantOK bool
	}{
		{"peer-completed-before-status-update", func(*rbac.TierState) {}, true},
		{"unchanged-revision", func(state *rbac.TierState) { state.Revision = before.Revision }, false},
		{"older-revision", func(state *rbac.TierState) { state.Revision = before.Revision - 1 }, false},
		{"repair-still-pending", func(state *rbac.TierState) { state.ProjectionPending = true }, false},
		{"new-canonical-origin", func(state *rbac.TierState) {
			state.Projection.GitRevision = "main@sha1:" + strings.Repeat("c", 40)
		}, false},
		{"namespace-retired", func(state *rbac.TierState) { state.NamespaceRetired = true }, false},
		{"different-tier", func(state *rbac.TierState) { state.TierID = "different" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := before
			current.Revision++
			current.ProjectionPending = false
			test.change(&current)
			reads := 0
			err := confirmRBACRepair(t.Context(), before, func(context.Context) (rbac.TierState, error) {
				reads++
				return current, nil
			})
			if test.wantOK {
				if err != nil {
					t.Fatalf("completed identical-origin repair was reported as failed: %v", err)
				}
			} else if !errors.Is(err, rbac.ErrConflict) {
				t.Fatalf("unconfirmed or superseded repair lost its conflict: %v", err)
			}
			if reads != 1 {
				t.Fatalf("repair completion used %d live confirmation reads, want one", reads)
			}
		})
	}
	want := errors.New("repair confirmation unavailable")
	err := confirmRBACRepair(t.Context(), before, func(context.Context) (rbac.TierState, error) {
		return rbac.TierState{}, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("repair confirmation suppressed its source failure: %v", err)
	}
}

func TestProductListingReservationIsNotDeletedBeforeAuthorityAppears(t *testing.T) {
	projection := ProjectionRecord{
		Table: productsByNamespaceShard, UID: "11111111-1111-1111-1111-111111111111",
		Namespace: "shop", CreationTimestamp: time.Now().UTC(), Shard: 1,
	}
	plan, err := BuildRepairPlan(ProjectionSnapshot{Projections: []ProjectionRecord{projection}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 0 || len(plan.Findings) != 1 || plan.Findings[0].Repairable {
		t.Fatalf("an in-flight pre-authoritative listing reservation must be retained: %+v", plan)
	}
}

func TestRepositoryCatalogProjectionIsIncludedInRepair(t *testing.T) {
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		t.Run(kind, func(t *testing.T) {
			resource := AuthoritativeResource{Kind: kind, UID: "11111111-1111-1111-1111-111111111111",
				RepositoryID: "22222222-2222-2222-2222-222222222222", Namespace: "shop", Name: "product",
				ResourceVersion: "1", CreationTimestamp: time.Now().UTC()}
			projections := expectedProjections(resource)
			expected := 3
			if kind == "Product" {
				expected++
			}
			if len(projections) != expected {
				t.Fatalf("got %d projections, want %d", len(projections), expected)
			}
			index := projections[len(projections)-1]
			if index.Table != repositoryResourcesTable || index.Kind != resource.Kind || index.RepositoryID != resource.RepositoryID || index.Shard != categoryProductShard(resource.UID) {
				t.Fatalf("unexpected repository index: %+v", index)
			}
			if !index.Equal(index) || index.Key() == "" {
				t.Fatal("repository index must be comparable and addressable")
			}
			plan, err := BuildRepairPlan(ProjectionSnapshot{Authoritative: []AuthoritativeResource{resource}, Projections: projections[:len(projections)-1]})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Actions) != 1 || actionTable(plan.Actions[0]) != repositoryResourcesTable {
				t.Fatalf("missing repository index not repaired: %+v", plan)
			}
			other := index
			other.Kind = "FutureResource"
			if other.Key() == index.Key() || index.Equal(other) || other.resourceKind() != "FutureResource" {
				t.Fatal("membership identity must include the kind without adding a table")
			}
			requirePlanErr := ValidateRepairPlan(plan)
			if requirePlanErr != nil {
				t.Fatal(requirePlanErr)
			}
		})
	}
}

func TestProjectionTableKinds(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"service_accounts_by_cluster_uid":    "ServiceAccount",
		"service_accounts_by_cluster_bucket": "ServiceAccount",
		"files_by_name":                      "File",
		"files_by_uid":                       "File",
		"namespaces_by_name":                 "Namespace",
		"namespaces_by_bucket":               "Namespace",
		"repositories_by_namespace":          "Repository",
		"repositories_by_bucket":             "Repository",
		"namespace_mappings":                 "Repository",
		"namespace_mappings_by_repository":   "Repository",
		"products_by_identity":               "Product",
		"products_by_uid":                    "Product",
		productsByNamespaceShard:             "Product",
		"category_products_by_category":      "Product",
		"category_products_by_product":       "Product",
		"category_taxonomies_by_name":        "CategoryTaxonomy",
		"category_taxonomies_by_uid":         "CategoryTaxonomy",
		"category_ancestor_index":            "CategoryTaxonomy",
		"collections_by_name":                "Collection",
		"collections_by_uid":                 "Collection",
		"product_variants_by_name":           "ProductVariant",
		"product_variants_by_uid":            "ProductVariant",
		"product_variants_by_sku":            "ProductVariant",
		"product_variants_by_product_ref":    "ProductVariant",
	}
	if len(tests) != len(projectionTableKinds) {
		t.Fatalf("projection table test coverage = %d tables, registry = %d", len(tests), len(projectionTableKinds))
	}

	for table, wantKind := range tests {
		t.Run(table, func(t *testing.T) {
			if got := projectionKind(table); got != wantKind {
				t.Errorf("projectionKind(%q) = %q, want %q", table, got, wantKind)
			}
			if !knownProjectionTable(table) {
				t.Errorf("knownProjectionTable(%q) = false, want true", table)
			}
		})
	}

	const unknown = "unknown_projection_table"
	if got := projectionKind(unknown); got != "" {
		t.Errorf("projectionKind(%q) = %q, want empty", unknown, got)
	}
	if knownProjectionTable(unknown) {
		t.Errorf("knownProjectionTable(%q) = true, want false", unknown)
	}
}

func TestServiceAccountRepairUsesGlobalAuthority(t *testing.T) {
	account := AuthoritativeResource{Kind: "ServiceAccount", UID: "11111111-1111-1111-1111-111111111111", Name: "controller", ResourceVersion: "1", CreationTimestamp: time.Unix(100, 0).UTC()}
	plan, err := BuildRepairPlan(ProjectionSnapshot{Authoritative: []AuthoritativeResource{account}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 2 {
		t.Fatalf("missing reservation and listing repairs: %+v", plan)
	}
	for _, action := range plan.Actions {
		if action.UID != account.UID || action.After.Name != account.Name || action.ExpectedResourceVersion != "1" {
			t.Fatalf("repair lost identity guard: %+v", action)
		}
	}
	if err := ValidateRepairPlan(plan); err != nil {
		t.Fatal(err)
	}
	projections := expectedProjections(account)
	plan, err = BuildRepairPlan(ProjectionSnapshot{Projections: projections})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 2 {
		t.Fatalf("interrupted deletion not repaired: %+v", plan)
	}
	for _, action := range plan.Actions {
		if !action.RequireAbsentResource || action.Type != RepairDelete {
			t.Fatalf("unguarded deletion: %+v", action)
		}
	}
}

func TestExpectedProductCategoryMembershipProjectionsAreSharded(t *testing.T) {
	resource := AuthoritativeResource{
		Kind: "Product", UID: "00000000-0000-0000-0000-000000000001", Namespace: "shop", Name: "widget",
		CreationTimestamp: time.Unix(100, 0).UTC(), CategoryUIDs: []string{"cat-a", "cat-b"},
	}
	rows := expectedProjections(resource)
	var forward, reverse int
	for _, row := range rows {
		switch row.Table {
		case "category_products_by_category":
			forward++
			if row.Shard != categoryProductShard(resource.UID) {
				t.Fatalf("forward shard = %d", row.Shard)
			}
		case "category_products_by_product":
			reverse++
		}
	}
	if forward != 2 || reverse != 2 {
		t.Fatalf("membership projections = forward %d reverse %d, want 2 each", forward, reverse)
	}
}

func TestBuildRepairPlanDeterministicFindings(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	namespace := AuthoritativeResource{
		Kind: "Namespace", UID: "11111111-1111-1111-1111-111111111111", Name: "current",
		ResourceVersion: "7", CreationTimestamp: created,
	}
	staleNamespace := AuthoritativeResource{
		Kind: "Namespace", UID: "22222222-2222-2222-2222-222222222222", Name: "stale-current",
		ResourceVersion: "3", CreationTimestamp: created.Add(time.Minute),
	}
	validName := expectedProjections(namespace)[0]
	duplicateName := validName
	duplicateName.Name = "old"
	dangling := validName
	dangling.Name = "orphan"
	dangling.UID = "99999999-9999-9999-9999-999999999999"
	staleName := expectedProjections(staleNamespace)[0]
	staleName.Name = "stale-old"
	staleBucket := expectedProjections(staleNamespace)[1]

	snapshot := ProjectionSnapshot{
		Authoritative: []AuthoritativeResource{namespace, staleNamespace},
		Projections:   []ProjectionRecord{dangling, duplicateName, staleName, staleBucket, validName},
	}
	first, err := BuildRepairPlan(snapshot)
	if err != nil {
		t.Fatalf("BuildRepairPlan() error = %v", err)
	}
	second, err := BuildRepairPlan(snapshot)
	if err != nil {
		t.Fatalf("second BuildRepairPlan() error = %v", err)
	}
	if stringifyPlan(first) != stringifyPlan(second) {
		t.Fatalf("plans are not deterministic:\nfirst:  %s\nsecond: %s", stringifyPlan(first), stringifyPlan(second))
	}

	gotTypes := make([]FindingType, 0, len(first.Findings))
	for _, finding := range first.Findings {
		gotTypes = append(gotTypes, finding.Type)
	}
	want := []FindingType{FindingMissing, FindingDuplicate, FindingDangling, FindingStale}
	for _, findingType := range want {
		if !containsFindingType(gotTypes, findingType) {
			t.Fatalf("findings = %v, want %q", gotTypes, findingType)
		}
	}
}

func TestProjectionRepairServiceDryRunDoesNotMutate(t *testing.T) {
	t.Parallel()
	store := newFakeRepairStore(missingNamespaceBucketSnapshot())
	service := &ProjectionRepairService{store: store}

	plan, err := service.Audit(context.Background())
	if err != nil {
		t.Fatalf("Audit() error = %v", err)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Type != RepairInsert {
		t.Fatalf("Audit() actions = %#v, want one insert", plan.Actions)
	}
	if store.applyCalls != 0 {
		t.Fatalf("dry-run audit applied %d mutations", store.applyCalls)
	}
}

func TestProjectionRepairServiceApplyAndVerify(t *testing.T) {
	t.Parallel()
	store := newFakeRepairStore(missingNamespaceBucketSnapshot())
	service := &ProjectionRepairService{store: store}
	plan, err := service.Audit(context.Background())
	if err != nil {
		t.Fatalf("Audit() error = %v", err)
	}

	result, err := service.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.AppliedActions != 1 {
		t.Fatalf("AppliedActions = %d, want 1", result.AppliedActions)
	}
	if len(result.Verification.Findings) != 0 {
		t.Fatalf("verification findings = %#v, want none", result.Verification.Findings)
	}
}

func TestProjectionRepairServiceClearsRetainedRepositoryFence(t *testing.T) {
	t.Parallel()
	fence := NamespaceRepositoryFenceRepair{
		Namespace: "demo", UID: "11111111-1111-1111-1111-111111111111",
		RepositoryCreationEpoch: 7, PendingRepositoryCreations: 1,
	}
	snapshot := missingNamespaceBucketSnapshot()
	snapshot.Projections = append(snapshot.Projections, expectedProjections(snapshot.Authoritative[0])[1])
	snapshot.NamespaceRepositoryFences = []NamespaceRepositoryFenceRepair{fence}
	store := newFakeRepairStore(snapshot)
	service := &ProjectionRepairService{store: store}

	plan, err := service.Audit(context.Background())
	if err != nil {
		t.Fatalf("Audit() error = %v", err)
	}
	if len(plan.NamespaceRepositoryFences) != 1 || plan.NamespaceRepositoryFences[0] != fence {
		t.Fatalf("Audit() fences = %#v, want %#v", plan.NamespaceRepositoryFences, fence)
	}
	result, err := service.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(result.Verification.NamespaceRepositoryFences) != 0 {
		t.Fatalf("verification fences = %#v, want none", result.Verification.NamespaceRepositoryFences)
	}
	if len(store.completedRepairs) != 1 || store.completedRepairs[0] != fence {
		t.Fatalf("completed repairs = %#v, want %#v", store.completedRepairs, fence)
	}
	if result.PlannedRepositoryFences != 1 || result.CompletedRepositoryFences != 1 {
		t.Fatalf(
			"repository fence result = planned %d completed %d, want 1 and 1",
			result.PlannedRepositoryFences,
			result.CompletedRepositoryFences,
		)
	}
}

func TestCatalogFenceRepairValidation(t *testing.T) {
	snapshot := missingNamespaceBucketSnapshot()
	snapshot.Projections = expectedProjections(snapshot.Authoritative[0])
	fence := NamespaceRepositoryFenceRepair{
		Namespace: snapshot.Authoritative[0].Name, UID: snapshot.Authoritative[0].UID,
		RepositoryCreationEpoch: 7, PendingRepositoryCreations: 1,
	}
	snapshot.NamespaceRepositoryFences = []NamespaceRepositoryFenceRepair{fence, fence}
	snapshot.NamespaceRepositoryFences[1].Catalog = true
	plan, err := BuildRepairPlan(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRepairPlan(plan); err != nil {
		t.Fatalf("independent Repository and catalog fences must coexist: %v", err)
	}
	plan.NamespaceRepositoryFences[1].PendingRepositoryCreations = 2
	if err := ValidateRepairPlan(plan); err == nil {
		t.Fatal("an exclusive catalog fence cannot have two holders")
	}
	plan.NamespaceRepositoryFences[1] = plan.NamespaceRepositoryFences[0]
	if err := ValidateRepairPlan(plan); err == nil {
		t.Fatal("duplicate fences of the same type must be rejected")
	}
	fence.Catalog = true
	plan, err = BuildRepairPlan(ProjectionSnapshot{NamespaceRepositoryFences: []NamespaceRepositoryFenceRepair{fence}})
	if err != nil {
		t.Fatalf("a held fence must remain repairable after Namespace deletion: %v", err)
	}
	if err := ValidateRepairPlan(plan); err != nil {
		t.Fatal(err)
	}
}

func TestProjectionRepairServiceRetainsFenceWhenClearFails(t *testing.T) {
	t.Parallel()
	fence := NamespaceRepositoryFenceRepair{
		Namespace: "demo", UID: "11111111-1111-1111-1111-111111111111",
		RepositoryCreationEpoch: 7, PendingRepositoryCreations: 1,
	}
	snapshot := missingNamespaceBucketSnapshot()
	snapshot.Projections = append(snapshot.Projections, expectedProjections(snapshot.Authoritative[0])[1])
	snapshot.NamespaceRepositoryFences = []NamespaceRepositoryFenceRepair{fence}
	store := newFakeRepairStore(snapshot)
	store.completeErr = datastore.ErrConflict
	service := &ProjectionRepairService{store: store}

	plan, err := service.Audit(context.Background())
	if err != nil {
		t.Fatalf("Audit() error = %v", err)
	}
	if _, err := service.Apply(context.Background(), plan); !errors.Is(err, datastore.ErrConflict) {
		t.Fatalf("Apply() error = %v, want conflict", err)
	}
	retryPlan, err := service.Audit(context.Background())
	if err != nil {
		t.Fatalf("retry Audit() error = %v", err)
	}
	if len(retryPlan.NamespaceRepositoryFences) != 1 || retryPlan.NamespaceRepositoryFences[0] != fence {
		t.Fatalf("retry Audit() fences = %#v, want %#v", retryPlan.NamespaceRepositoryFences, fence)
	}
}

func TestProjectionRepairServiceUsesAuditedFenceForCAS(t *testing.T) {
	t.Parallel()
	fence := NamespaceRepositoryFenceRepair{
		Namespace: "demo", UID: "11111111-1111-1111-1111-111111111111",
		RepositoryCreationEpoch: 7, PendingRepositoryCreations: 1,
	}
	snapshot := missingNamespaceBucketSnapshot()
	snapshot.Projections = append(snapshot.Projections, expectedProjections(snapshot.Authoritative[0])[1])
	snapshot.NamespaceRepositoryFences = []NamespaceRepositoryFenceRepair{fence}
	store := newFakeRepairStore(snapshot)
	service := &ProjectionRepairService{store: store}

	plan, err := service.Audit(context.Background())
	if err != nil {
		t.Fatalf("Audit() error = %v", err)
	}
	store.snapshot.NamespaceRepositoryFences[0].RepositoryCreationEpoch++
	store.expectedFence = &store.snapshot.NamespaceRepositoryFences[0]
	if _, err := service.Apply(context.Background(), plan); !errors.Is(err, datastore.ErrConflict) {
		t.Fatalf("Apply() error = %v, want conflict", err)
	}
	if len(store.completedRepairs) != 0 {
		t.Fatalf("completed repairs = %#v, want none", store.completedRepairs)
	}
}

func TestProjectionRepairServiceProtectsConcurrentWriter(t *testing.T) {
	t.Parallel()
	store := newFakeRepairStore(missingNamespaceBucketSnapshot())
	service := &ProjectionRepairService{store: store}
	plan, err := service.Audit(context.Background())
	if err != nil {
		t.Fatalf("Audit() error = %v", err)
	}
	store.concurrentVersion = "8"

	_, err = service.Apply(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "resource version changed") {
		t.Fatalf("Apply() error = %v, want concurrent resource-version error", err)
	}
	if store.applyCalls != 0 {
		t.Fatalf("concurrent writer protection applied %d mutations", store.applyCalls)
	}
}

func TestProjectionRepairServiceDeletesStaleProjectionForLiveResource(t *testing.T) {
	t.Parallel()
	store := newFakeRepairStore(staleNamespaceBucketSnapshot())
	service := &ProjectionRepairService{store: store}
	plan, err := service.Audit(context.Background())
	if err != nil {
		t.Fatalf("Audit() error = %v", err)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Type != RepairDelete {
		t.Fatalf("Audit() actions = %#v, want one delete", plan.Actions)
	}

	result, err := service.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.AppliedActions != 1 {
		t.Fatalf("AppliedActions = %d, want 1", result.AppliedActions)
	}
	if len(result.Verification.Findings) != 0 {
		t.Fatalf("verification findings = %#v, want none", result.Verification.Findings)
	}
}

func TestProjectionRepairServiceRejectsDeleteAfterResourceVersionAdvance(t *testing.T) {
	t.Parallel()
	store := newFakeRepairStore(staleNamespaceBucketSnapshot())
	service := &ProjectionRepairService{store: store}
	plan, err := service.Audit(context.Background())
	if err != nil {
		t.Fatalf("Audit() error = %v", err)
	}
	store.versionsByLookup = []string{"7", "8"}

	_, err = service.Apply(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "conditional mutation was not applied") {
		t.Fatalf("Apply() error = %v, want conditional mutation rejection", err)
	}
	if store.applyCalls != 1 {
		t.Fatalf("Apply() calls = %d, want 1", store.applyCalls)
	}
	if len(store.snapshot.Projections) != 3 {
		t.Fatalf("projections = %#v, want stale projection retained", store.snapshot.Projections)
	}
}

func TestBuildRepairPlanDoesNotOverwriteValidCompetingOwner(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	left := AuthoritativeResource{
		Kind: "Namespace", UID: "11111111-1111-1111-1111-111111111111", Name: "shared",
		ResourceVersion: "1", CreationTimestamp: created,
	}
	right := AuthoritativeResource{
		Kind: "Namespace", UID: "22222222-2222-2222-2222-222222222222", Name: "shared",
		ResourceVersion: "1", CreationTimestamp: created.Add(time.Second),
	}
	_, err := BuildRepairPlan(ProjectionSnapshot{Authoritative: []AuthoritativeResource{left, right}})
	if err == nil || !strings.Contains(err.Error(), "authoritative conflict") {
		t.Fatalf("BuildRepairPlan() error = %v, want authoritative conflict", err)
	}
}

func TestBuildRepairPlanDoesNotDeleteWriteReservation(t *testing.T) {
	t.Parallel()
	reservation := ProjectionRecord{
		Table:             "products_by_identity",
		UID:               "99999999-9999-9999-9999-999999999999",
		Namespace:         "shop",
		Name:              "pending-product",
		CreationTimestamp: time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC),
	}

	plan, err := BuildRepairPlan(ProjectionSnapshot{
		Projections: []ProjectionRecord{reservation},
	})
	if err != nil {
		t.Fatalf("BuildRepairPlan() error = %v", err)
	}
	if len(plan.Findings) != 1 {
		t.Fatalf("findings = %#v, want one", plan.Findings)
	}
	if plan.Findings[0].Repairable {
		t.Fatalf("reservation finding unexpectedly repairable: %#v", plan.Findings[0])
	}
	if len(plan.Actions) != 0 {
		t.Fatalf("actions = %#v, want none", plan.Actions)
	}
}

func TestValidateRepairPlanRejectsReservationProjectionDelete(t *testing.T) {
	t.Parallel()
	err := ValidateRepairPlan(RepairPlan{Actions: []RepairAction{{
		Type:                  RepairDelete,
		Kind:                  "Product",
		UID:                   "11111111-1111-1111-1111-111111111111",
		RequireAbsentResource: true,
		Before: &ProjectionRecord{
			Table: "products_by_identity", UID: "11111111-1111-1111-1111-111111111111", Namespace: "shop", Name: "name",
		},
	}}})
	if err == nil || !strings.Contains(err.Error(), "reservation projection") {
		t.Fatalf("ValidateRepairPlan() error = %v, want reservation rejection", err)
	}
}

func TestValidateRepairPlanRejectsUnsafeAction(t *testing.T) {
	t.Parallel()
	err := ValidateRepairPlan(RepairPlan{Actions: []RepairAction{{
		Type: RepairDelete, Kind: "Namespace", UID: "11111111-1111-1111-1111-111111111111",
		Before: &ProjectionRecord{
			Table: "namespaces_by_name", UID: "11111111-1111-1111-1111-111111111111", Name: "name",
		},
	}}})
	if err == nil || !strings.Contains(err.Error(), "expected resource version") {
		t.Fatalf("ValidateRepairPlan() error = %v, want version validation error", err)
	}
}

func missingNamespaceBucketSnapshot() ProjectionSnapshot {
	created := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	resource := AuthoritativeResource{
		Kind: "Namespace", UID: "11111111-1111-1111-1111-111111111111", Name: "demo",
		ResourceVersion: "7", CreationTimestamp: created,
	}
	return ProjectionSnapshot{
		Authoritative: []AuthoritativeResource{resource},
		Projections:   []ProjectionRecord{expectedProjections(resource)[0]},
	}
}

func staleNamespaceBucketSnapshot() ProjectionSnapshot {
	snapshot := missingNamespaceBucketSnapshot()
	stale := expectedProjections(snapshot.Authoritative[0])[1]
	stale.Bucket = "2026-07"
	snapshot.Projections = append(snapshot.Projections, stale, expectedProjections(snapshot.Authoritative[0])[1])
	return snapshot
}

func containsFindingType(findings []FindingType, want FindingType) bool {
	for _, finding := range findings {
		if finding == want {
			return true
		}
	}
	return false
}

func stringifyPlan(plan RepairPlan) string {
	var builder strings.Builder
	for _, finding := range plan.Findings {
		builder.WriteString(string(finding.Type))
		builder.WriteByte(':')
		builder.WriteString(finding.Table)
		builder.WriteByte(':')
		builder.WriteString(finding.Key)
		builder.WriteByte('\n')
	}
	for _, action := range plan.Actions {
		builder.WriteString(string(action.Type))
		builder.WriteByte(':')
		builder.WriteString(actionTable(action))
		builder.WriteByte(':')
		builder.WriteString(actionKey(action))
		builder.WriteByte('\n')
	}
	return builder.String()
}

type fakeRepairStore struct {
	snapshot          ProjectionSnapshot
	applyCalls        int
	completedRepairs  []NamespaceRepositoryFenceRepair
	completeErr       error
	expectedFence     *NamespaceRepositoryFenceRepair
	concurrentVersion string
	versionsByLookup  []string
	lookupCalls       int
}

func newFakeRepairStore(snapshot ProjectionSnapshot) *fakeRepairStore {
	return &fakeRepairStore{snapshot: snapshot}
}

func (f *fakeRepairStore) Snapshot(context.Context) (ProjectionSnapshot, error) {
	return f.snapshot, nil
}

func (f *fakeRepairStore) LookupResource(_ context.Context, action RepairAction) (*AuthoritativeResource, error) {
	defer func() { f.lookupCalls++ }()
	for i := range f.snapshot.Authoritative {
		resource := f.snapshot.Authoritative[i]
		if resource.Kind == action.Kind && resource.UID == action.UID {
			if f.lookupCalls < len(f.versionsByLookup) {
				resource.ResourceVersion = f.versionsByLookup[f.lookupCalls]
			} else if f.concurrentVersion != "" {
				resource.ResourceVersion = f.concurrentVersion
			}
			return &resource, nil
		}
	}
	return nil, nil
}

func (f *fakeRepairStore) ApplyAction(ctx context.Context, action RepairAction) (bool, error) {
	f.applyCalls++
	switch action.Type {
	case RepairInsert:
		f.snapshot.Projections = append(f.snapshot.Projections, *action.After)
	case RepairUpdate:
		for i := range f.snapshot.Projections {
			if f.snapshot.Projections[i].Equal(*action.Before) {
				f.snapshot.Projections[i] = *action.After
				return true, nil
			}
		}
		return false, nil
	case RepairDelete:
		resource, err := f.LookupResource(ctx, action)
		if err != nil {
			return false, err
		}
		if !repairDeleteResourceMatches(action, resource) {
			return false, nil
		}
		for i := range f.snapshot.Projections {
			if f.snapshot.Projections[i].Equal(*action.Before) {
				f.snapshot.Projections = append(f.snapshot.Projections[:i], f.snapshot.Projections[i+1:]...)
				return true, nil
			}
		}
		return false, nil
	}
	return true, nil
}

func (f *fakeRepairStore) CompleteRepositoryRepairs(
	_ context.Context,
	fences []NamespaceRepositoryFenceRepair,
) error {
	if f.completeErr != nil {
		return f.completeErr
	}
	if f.expectedFence != nil && (len(fences) != 1 || fences[0] != *f.expectedFence) {
		return datastore.ErrConflict
	}
	f.completedRepairs = append(f.completedRepairs, fences...)
	f.snapshot.NamespaceRepositoryFences = nil
	return nil
}

func (f *fakeRepairStore) Close() {}

func categoryAncestorResource(name, uid, resourceVersion string, path ...string) AuthoritativeResource {
	return AuthoritativeResource{
		Kind: "CategoryTaxonomy", UID: uid, Namespace: "demo", Name: name,
		ResourceVersion: resourceVersion, CreationTimestamp: time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC),
		ResolvedPath: path,
	}
}

func ancestorRows(resource AuthoritativeResource) []ProjectionRecord {
	var rows []ProjectionRecord
	for _, projection := range expectedProjections(resource) {
		if projection.Table == "category_ancestor_index" {
			rows = append(rows, projection)
		}
	}
	return rows
}

func ancestorSnapshot(resources []AuthoritativeResource, rows ...ProjectionRecord) ProjectionSnapshot {
	snapshot := ProjectionSnapshot{Authoritative: resources}
	for _, resource := range resources {
		for _, projection := range expectedProjections(resource) {
			if projection.Table != "category_ancestor_index" {
				snapshot.Projections = append(snapshot.Projections, projection)
			}
		}
	}
	snapshot.Projections = append(snapshot.Projections, rows...)
	return snapshot
}

func TestExpectedProjectionsDeriveCategoryAncestorRowsFromResolvedPath(t *testing.T) {
	t.Parallel()
	laptops := categoryAncestorResource("laptops", "11111111-1111-1111-1111-111111111111", "4", "electronics", "computers", "laptops")
	rows := ancestorRows(laptops)
	got := make([]string, 0, len(rows))
	for _, row := range rows {
		got = append(got, row.Key())
	}
	want := []string{"demo/electronics/2/laptops", "demo/computers/1/laptops", "demo/laptops/0/laptops"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ancestor rows = %v, want %v", got, want)
	}
	if rows := ancestorRows(categoryAncestorResource("draft", "22222222-2222-2222-2222-222222222222", "1")); len(rows) != 0 {
		t.Fatalf("category without status.resolved owns rows: %v", rows)
	}
}

func TestBuildRepairPlanReportsMissingDanglingAndStaleAncestorRows(t *testing.T) {
	t.Parallel()
	const laptopsUID = "11111111-1111-1111-1111-111111111111"
	laptops := categoryAncestorResource("laptops", laptopsUID, "4", "electronics", "computers", "laptops")
	rows := ancestorRows(laptops)

	// computers/1 is missing; a re-parent left electronics/2 intact but also
	// left a row under the old parent "gadgets"; an orphan references a
	// removed category; and one row points at a recreated (different) UID.
	oldParent := rows[0]
	oldParent.Ancestor, oldParent.Depth = "gadgets", 1
	orphan := rows[0]
	orphan.UID, orphan.Name, orphan.Ancestor, orphan.Depth = "99999999-9999-9999-9999-999999999999", "removed", "electronics", 1
	recreated := rows[2]
	recreated.UID = "88888888-8888-8888-8888-888888888888"

	snapshot := ancestorSnapshot([]AuthoritativeResource{laptops}, rows[0], oldParent, orphan, recreated)
	plan, err := BuildRepairPlan(snapshot)
	if err != nil {
		t.Fatalf("BuildRepairPlan() error = %v", err)
	}
	got := map[string]FindingType{}
	for _, finding := range plan.Findings {
		if finding.Table == "category_ancestor_index" {
			got[finding.Key] = finding.Type
		}
	}
	want := map[string]FindingType{
		"demo/computers/1/laptops":   FindingMissing,
		"demo/gadgets/1/laptops":     FindingStale,
		"demo/electronics/1/removed": FindingDangling,
		"demo/laptops/0/laptops":     FindingStale,
	}
	if len(got) != len(want) {
		t.Fatalf("findings = %v, want %v", got, want)
	}
	for key, findingType := range want {
		if got[key] != findingType {
			t.Fatalf("finding[%s] = %q, want %q (all: %v)", key, got[key], findingType, got)
		}
	}
	for _, finding := range plan.Findings {
		if !finding.Repairable {
			t.Fatalf("finding %s/%s is not repairable: %s", finding.Table, finding.Key, finding.Reason)
		}
	}
	if err := ValidateRepairPlan(plan); err != nil {
		t.Fatalf("ValidateRepairPlan() error = %v", err)
	}
}

func TestProjectionRepairServiceConvergesAncestorIndex(t *testing.T) {
	t.Parallel()
	laptops := categoryAncestorResource("laptops", "11111111-1111-1111-1111-111111111111", "4", "electronics", "computers", "laptops")
	rows := ancestorRows(laptops)
	oldParent := rows[0]
	oldParent.Ancestor, oldParent.Depth = "gadgets", 1
	orphan := rows[0]
	orphan.UID, orphan.Name, orphan.Depth = "99999999-9999-9999-9999-999999999999", "removed", 1

	store := newFakeRepairStore(ancestorSnapshot([]AuthoritativeResource{laptops}, rows[0], oldParent, orphan))
	service := &ProjectionRepairService{store: store}
	plan, err := service.Audit(context.Background())
	if err != nil {
		t.Fatalf("Audit() error = %v", err)
	}
	if len(plan.Actions) != 4 {
		t.Fatalf("actions = %s, want 4 (insert 2 missing, delete stale, delete dangling)", stringifyPlan(plan))
	}
	result, err := service.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.AppliedActions != 4 || len(result.Verification.Findings) != 0 {
		t.Fatalf("result = %+v, want 4 applied actions and a clean audit", result)
	}
	again, err := service.Audit(context.Background())
	if err != nil || len(again.Findings) != 0 || len(again.Actions) != 0 {
		t.Fatalf("second audit = %+v, %v; want clean", again, err)
	}
}

// TestRollingUpgradeAncestorIndexGapIsBackfilledByRepair models categories
// whose status was written by a replica that predates the ancestor index:
// status.resolved is set but no index row exists. Repair must backfill every
// row and leave a clean audit, so filtered lists are complete after rollout.
func TestRollingUpgradeAncestorIndexGapIsBackfilledByRepair(t *testing.T) {
	t.Parallel()
	resources := []AuthoritativeResource{
		categoryAncestorResource("electronics", "21111111-1111-1111-1111-111111111111", "3", "electronics"),
		categoryAncestorResource("computers", "22222222-2222-2222-2222-222222222222", "5", "electronics", "computers"),
		categoryAncestorResource("laptops", "23333333-3333-3333-3333-333333333333", "7", "electronics", "computers", "laptops"),
		// Never reconciled: owns no rows before or after repair.
		categoryAncestorResource("unreconciled", "24444444-4444-4444-4444-444444444444", "1"),
	}
	want := 0
	for _, resource := range resources {
		want += len(ancestorRows(resource))
	}
	if want != 6 {
		t.Fatalf("expected 6 derived rows, got %d", want)
	}

	service := &ProjectionRepairService{store: newFakeRepairStore(ancestorSnapshot(resources))}
	plan, err := service.Audit(context.Background())
	if err != nil {
		t.Fatalf("Audit() error = %v", err)
	}
	if len(plan.Actions) != want {
		t.Fatalf("actions = %s, want %d inserts", stringifyPlan(plan), want)
	}
	result, err := service.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(result.Verification.Findings) != 0 {
		t.Fatalf("verification findings = %+v, want none", result.Verification.Findings)
	}
}
