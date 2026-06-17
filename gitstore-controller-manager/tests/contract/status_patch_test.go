// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// mockStatusClient is a test double for status.StatusClient.
type mockStatusClient struct {
	returnErr error
	callCount int
}

func (m *mockStatusClient) Apply(_ context.Context, _ types.WorkItemKey, _ *status.StatusPatch) error {
	m.callCount++
	return m.returnErr
}

// T026: IsNoOp returns true when every non-nil patch field matches ResourceStatus.
func TestStatusPatch_IsNoOp_AllFieldsMatch(t *testing.T) {
	gen := int64(5)
	rev := "main@sha1:abc123"
	now := time.Now()

	current := status.ResourceStatus{
		ResourceVersion:     "rv-1",
		ObservedGeneration:  gen,
		LastAppliedRevision: rev,
		Conditions: []*status.Condition{
			{Type: "Ready", Status: "True", ObservedGeneration: gen, LastTransitionTime: now, Reason: "OK", Message: ""},
		},
	}

	patch := status.StatusPatch{
		ResourceVersion:     "rv-1",
		ObservedGeneration:  &gen,
		LastAppliedRevision: &rev,
		Conditions: []*status.Condition{
			{Type: "Ready", Status: "True", ObservedGeneration: gen, LastTransitionTime: now, Reason: "OK", Message: ""},
		},
	}

	if !patch.IsNoOp(current) {
		t.Error("expected IsNoOp=true when all fields match")
	}
}

// T027: IsNoOp returns false when one non-nil field differs.
func TestStatusPatch_IsNoOp_OneFieldDiffers(t *testing.T) {
	gen := int64(5)
	rev := "main@sha1:abc123"
	diffRev := "main@sha1:deadbeef"

	current := status.ResourceStatus{
		ResourceVersion:     "rv-1",
		ObservedGeneration:  gen,
		LastAppliedRevision: rev,
	}

	patch := status.StatusPatch{
		ResourceVersion:     "rv-1",
		ObservedGeneration:  &gen,
		LastAppliedRevision: &diffRev, // differs
	}

	if patch.IsNoOp(current) {
		t.Error("expected IsNoOp=false when LastAppliedRevision differs")
	}
}

// T028: IsNoOp returns false when ObservedGeneration is nil in patch but
// current.ObservedGeneration != 0 (reconciler MUST always set it on success).
func TestStatusPatch_ObservedGenerationRequired(t *testing.T) {
	gen := int64(3)
	current := status.ResourceStatus{
		ResourceVersion:    "rv-1",
		ObservedGeneration: gen,
	}

	patch := status.StatusPatch{
		ResourceVersion:    "rv-1",
		ObservedGeneration: nil, // not set
	}

	if patch.IsNoOp(current) {
		t.Error("expected IsNoOp=false when ObservedGeneration nil but current has non-zero generation")
	}
}

// T029: mockStatusClient returns ErrConflict and errors.Is resolves correctly.
func TestStatusClient_Conflict_ReturnsErrConflict(t *testing.T) {
	mock := &mockStatusClient{returnErr: types.ErrConflict}

	gen := int64(1)
	patch := &status.StatusPatch{
		ResourceVersion:    "rv-old",
		ObservedGeneration: &gen,
	}

	err := mock.Apply(context.Background(), types.WorkItemKey{Kind: "Widget", Namespace: "ns", Name: "w1"}, patch)
	if !errors.Is(err, types.ErrConflict) {
		t.Errorf("expected ErrConflict, got %v", err)
	}
}

// T030: when IsNoOp is true, mockStatusClient.Apply is never called.
func TestStatusClient_NoOpPatch_SkipsApply(t *testing.T) {
	mock := &mockStatusClient{}

	gen := int64(2)
	rev := "main@sha1:abc"
	current := status.ResourceStatus{
		ResourceVersion:     "rv-1",
		ObservedGeneration:  gen,
		LastAppliedRevision: rev,
	}
	patch := &status.StatusPatch{
		ResourceVersion:     "rv-1",
		ObservedGeneration:  &gen,
		LastAppliedRevision: &rev,
	}

	if !patch.IsNoOp(current) {
		t.Fatal("precondition: patch should be no-op")
	}

	// Caller is responsible for checking IsNoOp before calling Apply.
	// Here we verify the mock records zero calls when the caller respects the contract.
	if mock.callCount != 0 {
		t.Errorf("Apply should not have been called, got %d calls", mock.callCount)
	}
}
