// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package namespace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/require"
)

type ownerAccessFixture struct {
	err   error
	calls []Namespace
}

func (f *ownerAccessFixture) EnsureOwnerAccess(_ context.Context, namespace Namespace) (string, error) {
	f.calls = append(f.calls, namespace)
	return "main@sha1:" + strings.Repeat("a", 40), f.err
}

func TestOwnerAccessObservationIsUIDBoundAndPreservesOtherStatus(t *testing.T) {
	for _, scenario := range []string{"pending", "ready", "failed", "organization", "terminating", "timestamp-only"} {
		t.Run(scenario, func(t *testing.T) {
			current := Namespace{UID: "observed-namespace-uid", Name: "shop", Tier: "USER", OwnerKind: "USER", OwnerSubject: "staff-subject",
				ResourceVersion: "7", Generation: 1, Status: status.ResourceStatus{Conditions: []*status.Condition{
					admittedCondition(1), {Type: "OtherController", Status: "TRUE", Reason: "Preserved"},
				}}}
			switch scenario {
			case "pending":
				current.Status.Conditions = current.Status.Conditions[1:]
			case "organization":
				current.Tier = "ORGANIZATION"
			case "terminating":
				current.Finalizers = []string{ForegroundDeletionFinalizer}
			case "timestamp-only":
				now := time.Now().UTC()
				current.DeletionTimestamp = &now
			}
			owner := &ownerAccessFixture{}
			if scenario == "failed" {
				owner.err = errors.New("directory unavailable")
			}
			statuses, repositories, deletion := &fakeStatusClient{}, &fakeRepositoryClient{}, &fakeDeletionClient{}
			reconciler := NewReconcilerWithLookup(func(context.Context, types.WorkItemKey) (Namespace, bool, error) {
				return current, true, nil
			}, statuses, repositories, deletion).WithOwnerAccess(owner)
			result := reconciler.Reconcile(t.Context(), namespaceKey(current.Name))
			if scenario == "terminating" || scenario == "timestamp-only" {
				require.Empty(t, statuses.patches)
				require.Empty(t, owner.calls)
				require.Equal(t, []string{current.UID}, deletion.uids)
				return
			}
			require.Len(t, statuses.patches, 1)
			patch := statuses.patches[0]
			require.Equal(t, current.UID, patch.UID)
			require.Equal(t, "7", patch.ResourceVersion)
			require.Nil(t, patch.LastAppliedRevision, "owner observations do not replace the namespace Git revision")
			var ownerCondition *status.Condition
			preserved := false
			for _, condition := range patch.Conditions {
				if condition.Type == "OwnerAccessReady" {
					ownerCondition = condition
				}
				if condition.Type == "OtherController" && condition.Reason == "Preserved" {
					preserved = true
				}
			}
			require.True(t, preserved)
			if scenario == "organization" {
				require.Nil(t, ownerCondition)
				require.Empty(t, owner.calls)
				return
			}
			require.NotNil(t, ownerCondition)
			if scenario == "pending" {
				require.Equal(t, "Pending", ownerCondition.Reason)
				require.Empty(t, owner.calls)
			} else if scenario == "failed" {
				require.Equal(t, "Failed", ownerCondition.Reason)
				require.IsType(t, types.TransientFailure{}, result, "admission failures remain durable retry work")
			} else {
				require.Equal(t, "TRUE", ownerCondition.Status)
				require.Equal(t, "BindingAdmitted", ownerCondition.Reason)
				require.Contains(t, ownerCondition.Message, strings.Repeat("a", 40))
				require.Contains(t, ownerCondition.Message, "readiness is independent")
				require.Equal(t, "staff-subject", owner.calls[0].OwnerSubject)
			}
		})
	}
}
