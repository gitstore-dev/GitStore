// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

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
		Table:             "products_by_name",
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
			Table: "products_by_name", UID: "11111111-1111-1111-1111-111111111111", Namespace: "shop", Name: "name",
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
