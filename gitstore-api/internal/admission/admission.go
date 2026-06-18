// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package admission defines the GitStore admission control framework.
// Resources are run through a Chain of MutatingAdmissionPolicy,
// MutatingAdmissionWebhook, ValidatingAdmissionPolicy, and
// ValidatingAdmissionWebhook extension points before being hydrated into
// the datastore.
package admission

import (
	"encoding/json"
	"time"
)

// Operation is the action being performed on the resource.
type Operation string

const (
	OperationCreate Operation = "CREATE"
	OperationUpdate Operation = "UPDATE"
	OperationDelete Operation = "DELETE"
)

// Trigger identifies the code path that initiated admission.
type Trigger string

const (
	TriggerGitPush    Trigger = "GIT_PUSH"
	TriggerGraphQL    Trigger = "GRAPHQL"
	TriggerCommitFile Trigger = "COMMIT_FILE" // hook point defined; not wired in spec 027
)

// GitAdmissionContext is populated when Trigger == TriggerGitPush.
type GitAdmissionContext struct {
	RepositoryID string
	CommitSHA    string
	RefName      string
	Revision     string
}

// AdmissionCondition carries the result of a named admission check.
// Type matches catalog.ConditionType constants (e.g. "ProductResolved").
type AdmissionCondition struct {
	Type    string
	Status  bool   // true = condition satisfied
	Reason  string // optional machine-readable reason code
	Message string // optional human-readable detail
}

// AdmissionRequest is the input to the admission chain.
// It is generic across all resource types and trigger paths.
type AdmissionRequest struct {
	Object     any // decoded resource; concrete struct or map[string]any
	OldObject  any // nil for creates
	Operation  Operation
	Kind       string
	Name       string
	Namespace  string
	Trigger    Trigger
	GitContext *GitAdmissionContext // nil for non-git triggers
	PushSet    []AdmissionRequest   // sibling resources in the same push; nil outside git-push
	Now        time.Time            // admission timestamp set once for the entire batch
}

// AdmissionDecision is the sealed result of the chain or any extension point.
// Only Allowed and Denied satisfy this interface.
type AdmissionDecision interface{ admissionDecision() }

// Allowed signals the resource may proceed.
// Conditions carries named check results for status hydration.
// Patches carries JSON Merge Patch fragments from mutating policies (nil from validators).
type Allowed struct {
	Conditions []AdmissionCondition
	Patches    []json.RawMessage
}

// Denied signals the resource is rejected.
type Denied struct {
	Reason string
	Field  string // optional dotted field path, e.g. "spec.productRef.name"
}

func (Allowed) admissionDecision() {}
func (Denied) admissionDecision()  {}

// DecisionAllow returns an Allowed decision with the given conditions.
func DecisionAllow(conditions ...AdmissionCondition) AdmissionDecision {
	return Allowed{Conditions: conditions}
}

// DecisionDeny returns a Denied decision.
func DecisionDeny(reason, field string) AdmissionDecision {
	return Denied{Reason: reason, Field: field}
}
