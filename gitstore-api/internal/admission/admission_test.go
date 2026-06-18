// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admission_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOperation_Constants(t *testing.T) {
	assert.Equal(t, admission.Operation("CREATE"), admission.OperationCreate)
	assert.Equal(t, admission.Operation("UPDATE"), admission.OperationUpdate)
	assert.Equal(t, admission.Operation("DELETE"), admission.OperationDelete)
}

func TestTrigger_Constants(t *testing.T) {
	assert.Equal(t, admission.Trigger("GIT_PUSH"), admission.TriggerGitPush)
	assert.Equal(t, admission.Trigger("GRAPHQL"), admission.TriggerGraphQL)
	assert.Equal(t, admission.Trigger("COMMIT_FILE"), admission.TriggerCommitFile)
}

func TestAdmissionCondition_ZeroValue(t *testing.T) {
	var c admission.AdmissionCondition
	assert.Empty(t, c.Type)
	assert.False(t, c.Status)
	assert.Empty(t, c.Reason)
	assert.Empty(t, c.Message)
}

func TestAdmissionRequest_ZeroValue(t *testing.T) {
	var r admission.AdmissionRequest
	assert.Nil(t, r.Object)
	assert.Nil(t, r.OldObject)
	assert.Empty(t, r.Operation)
	assert.Empty(t, r.Kind)
	assert.Empty(t, r.Name)
	assert.Empty(t, r.Namespace)
	assert.Empty(t, r.Trigger)
	assert.Nil(t, r.GitContext)
	assert.Nil(t, r.PushSet)
	assert.True(t, r.Now.IsZero())
}

func TestAdmissionRequest_WithGitContext(t *testing.T) {
	r := admission.AdmissionRequest{
		Kind:      "Product",
		Name:      "my-product",
		Namespace: "store",
		Operation: admission.OperationCreate,
		Trigger:   admission.TriggerGitPush,
		GitContext: &admission.GitAdmissionContext{
			RepositoryID: "repo-001",
			CommitSHA:    "abc123",
			RefName:      "refs/heads/main",
			Revision:     "main@sha1:abc123",
		},
		Now: time.Now(),
	}
	require.NotNil(t, r.GitContext)
	assert.Equal(t, "repo-001", r.GitContext.RepositoryID)
	assert.Equal(t, "refs/heads/main", r.GitContext.RefName)
}

func TestDecisionAllow_NoConditions(t *testing.T) {
	d := admission.DecisionAllow()
	allowed, ok := d.(admission.Allowed)
	require.True(t, ok, "DecisionAllow must return Allowed")
	assert.Empty(t, allowed.Conditions)
	assert.Nil(t, allowed.Patches)
}

func TestDecisionAllow_WithConditions(t *testing.T) {
	c1 := admission.AdmissionCondition{Type: "ProductResolved", Status: true}
	c2 := admission.AdmissionCondition{Type: "OptionsAccepted", Status: false, Reason: "IncompatibleOptions"}
	d := admission.DecisionAllow(c1, c2)
	allowed, ok := d.(admission.Allowed)
	require.True(t, ok)
	require.Len(t, allowed.Conditions, 2)
	assert.Equal(t, "ProductResolved", allowed.Conditions[0].Type)
	assert.True(t, allowed.Conditions[0].Status)
	assert.Equal(t, "OptionsAccepted", allowed.Conditions[1].Type)
	assert.False(t, allowed.Conditions[1].Status)
	assert.Equal(t, "IncompatibleOptions", allowed.Conditions[1].Reason)
}

func TestDecisionDeny(t *testing.T) {
	d := admission.DecisionDeny("invalid spec", "spec.title")
	denied, ok := d.(admission.Denied)
	require.True(t, ok, "DecisionDeny must return Denied")
	assert.Equal(t, "invalid spec", denied.Reason)
	assert.Equal(t, "spec.title", denied.Field)
}

func TestDecisionDeny_EmptyField(t *testing.T) {
	d := admission.DecisionDeny("bad resource", "")
	denied, ok := d.(admission.Denied)
	require.True(t, ok)
	assert.Equal(t, "bad resource", denied.Reason)
	assert.Empty(t, denied.Field)
}

func TestAllowed_Patches(t *testing.T) {
	patch := json.RawMessage(`{"spec":{"title":"patched"}}`)
	a := admission.Allowed{Patches: []json.RawMessage{patch}}
	assert.Len(t, a.Patches, 1)
}

// TestSealedInterface ensures AdmissionDecision is satisfied only by Allowed and Denied.
// This is a compile-time check — if the interface changes, this test will fail to compile.
func TestSealedInterface_CompileTimeCheck(t *testing.T) {
	var _ admission.AdmissionDecision = admission.Allowed{}
	var _ admission.AdmissionDecision = admission.Denied{}
}
