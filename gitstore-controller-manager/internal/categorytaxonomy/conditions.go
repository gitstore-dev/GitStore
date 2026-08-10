// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import (
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

const (
	conditionParentResolved = "ParentResolved"
	conditionAcyclic        = "Acyclic"
	conditionReady          = "Ready"
	conditionFileRefPrefix  = "FileRefConfirmed"

	statusTrue    = "True"
	statusFalse   = "False"
	statusUnknown = "Unknown"
)

// computeParentResolved implements FR-006: True when self.ParentRefName is
// absent or resolves to an existing CategoryTaxonomy in the same namespace;
// False (with a reason/message identifying the problem) otherwise.
func computeParentResolved(c cache.CacheAccessor[CategoryTaxonomy], self CategoryTaxonomy) status.Condition {
	if self.ParentRefName == "" {
		return status.Condition{Type: conditionParentResolved, Status: statusTrue, LastTransitionTime: time.Now()}
	}
	_, ok := c.Get(types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: self.Namespace, Name: self.ParentRefName})
	if !ok {
		return status.Condition{
			Type:               conditionParentResolved,
			Status:             statusFalse,
			LastTransitionTime: time.Now(),
			Reason:             "ParentNotFound",
			Message:            fmt.Sprintf("parentRef %q does not resolve to an existing CategoryTaxonomy in namespace %q", self.ParentRefName, self.Namespace),
		}
	}
	return status.Condition{Type: conditionParentResolved, Status: statusTrue, LastTransitionTime: time.Now()}
}

// computeAcyclic implements FR-007: False for every cycle participant
// (including self-reference), True otherwise.
func computeAcyclic(inCycle bool) status.Condition {
	if inCycle {
		return status.Condition{
			Type:               conditionAcyclic,
			Status:             statusFalse,
			LastTransitionTime: time.Now(),
			Reason:             "CycleDetected",
			Message:            "this category participates in a parent-reference cycle",
		}
	}
	return status.Condition{Type: conditionAcyclic, Status: statusTrue, LastTransitionTime: time.Now()}
}

// computeReady implements FR-009: True only when parentResolved, acyclic,
// and (if present) fileRef are all True. fileRef may be nil when no
// optional:false media entry exists (US3) — its absence does not block
// Ready.
func computeReady(parentResolved, acyclic status.Condition, fileRef *status.Condition) status.Condition {
	ready := parentResolved.Status == statusTrue && acyclic.Status == statusTrue
	if fileRef != nil && fileRef.Status != statusTrue {
		ready = false
	}
	if !ready {
		return status.Condition{
			Type:               conditionReady,
			Status:             statusFalse,
			LastTransitionTime: time.Now(),
			Reason:             "ConditionsNotSatisfied",
			Message:            "one or more of ParentResolved/Acyclic/FileRefConfirmed is not True",
		}
	}
	return status.Condition{Type: conditionReady, Status: statusTrue, LastTransitionTime: time.Now()}
}
