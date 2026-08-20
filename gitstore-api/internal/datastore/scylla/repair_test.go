// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"strings"
	"testing"
	"time"
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
	concurrentVersion string
}

func newFakeRepairStore(snapshot ProjectionSnapshot) *fakeRepairStore {
	return &fakeRepairStore{snapshot: snapshot}
}

func (f *fakeRepairStore) Snapshot(context.Context) (ProjectionSnapshot, error) {
	return f.snapshot, nil
}

func (f *fakeRepairStore) LookupResource(_ context.Context, action RepairAction) (*AuthoritativeResource, error) {
	for i := range f.snapshot.Authoritative {
		resource := f.snapshot.Authoritative[i]
		if resource.Kind == action.Kind && resource.UID == action.UID {
			if f.concurrentVersion != "" {
				resource.ResourceVersion = f.concurrentVersion
			}
			return &resource, nil
		}
	}
	return nil, nil
}

func (f *fakeRepairStore) ApplyAction(_ context.Context, action RepairAction) (bool, error) {
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

func (f *fakeRepairStore) Close() {}
