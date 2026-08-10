// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package status provides typed status-writeback primitives for reconcilers.
package status

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// Condition mirrors the common condition shape from the GitStore GraphQL schema.
type Condition struct {
	Type               string
	Status             string
	ObservedGeneration int64
	LastTransitionTime time.Time
	Reason             string
	Message            string
}

// ResourceStatus holds the observed status fields read from the informer cache.
type ResourceStatus struct {
	ResourceVersion     string
	ObservedGeneration  int64
	LastAppliedRevision string
	Conditions          []*Condition
	// Resolved holds the kind-specific resolved payload as observed
	// (JSON-encoded), for comparison in IsNoOp. nil means the currently
	// observed resource has no resolved payload set.
	Resolved json.RawMessage
}

// StatusPatch is a partial-merge update applied to a resource's .status sub-resource.
// Only non-nil pointer fields are included in the API request.
type StatusPatch struct {
	ResourceVersion     string
	ObservedGeneration  *int64
	LastAppliedRevision *string
	Conditions          []*Condition
	// Resolved is the kind-specific resolved payload (spec 040 research.md
	// R8) -- NOT part of the generic patch conceptually, but threaded
	// through as JSON bytes so this package has no compile-time
	// dependency on any particular kind's resolved struct. nil = unchanged.
	// The reconciler marshals its own known type before calling Apply;
	// the StatusClient implementation unmarshals it into the wire
	// request's kind-specific argument.
	Resolved json.RawMessage
}

// IsNoOp returns true when every non-nil field in the patch matches the corresponding
// field in current. A nil ObservedGeneration when current.ObservedGeneration != 0
// always returns false (reconciler MUST set it on success).
func (p *StatusPatch) IsNoOp(current ResourceStatus) bool {
	if p.ResourceVersion != current.ResourceVersion {
		return false
	}
	if p.ObservedGeneration == nil {
		if current.ObservedGeneration != 0 {
			return false
		}
	} else if *p.ObservedGeneration != current.ObservedGeneration {
		return false
	}
	if p.LastAppliedRevision != nil && *p.LastAppliedRevision != current.LastAppliedRevision {
		return false
	}
	if p.Conditions != nil && !conditionsEqual(p.Conditions, current.Conditions) {
		return false
	}
	if p.Resolved != nil && !bytes.Equal(p.Resolved, current.Resolved) {
		return false
	}
	return true
}

func conditionsEqual(a, b []*Condition) bool {
	if len(a) != len(b) {
		return false
	}
	return slices.EqualFunc(a, b, func(x, y *Condition) bool {
		if x == nil || y == nil {
			return x == y
		}
		return x.Type == y.Type &&
			x.Status == y.Status &&
			x.ObservedGeneration == y.ObservedGeneration &&
			x.LastTransitionTime.Equal(y.LastTransitionTime) &&
			x.Reason == y.Reason &&
			x.Message == y.Message
	})
}

// StatusClient applies a StatusPatch to the remote API.
type StatusClient interface {
	Apply(ctx context.Context, key types.WorkItemKey, patch *StatusPatch) error
}
