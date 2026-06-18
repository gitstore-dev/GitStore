// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admission_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// --- stub helpers ---

type stubValidatingPolicy struct {
	name      string
	decision  admission.AdmissionDecision
	callOrder *[]string
}

func (p *stubValidatingPolicy) Name() string { return p.name }
func (p *stubValidatingPolicy) Validate(_ context.Context, _ admission.AdmissionRequest) admission.AdmissionDecision {
	if p.callOrder != nil {
		*p.callOrder = append(*p.callOrder, "validate:"+p.name)
	}
	return p.decision
}

type stubMutatingPolicy struct {
	name      string
	decision  admission.AdmissionDecision
	callOrder *[]string
}

func (p *stubMutatingPolicy) Name() string { return p.name }
func (p *stubMutatingPolicy) Mutate(_ context.Context, _ admission.AdmissionRequest) admission.AdmissionDecision {
	if p.callOrder != nil {
		*p.callOrder = append(*p.callOrder, "mutate:"+p.name)
	}
	return p.decision
}

type stubValidatingWebhook struct {
	name      string
	decision  admission.AdmissionDecision
	callOrder *[]string
}

func (w *stubValidatingWebhook) Name() string { return w.name }
func (w *stubValidatingWebhook) Validate(_ context.Context, _ admission.AdmissionRequest) admission.AdmissionDecision {
	if w.callOrder != nil {
		*w.callOrder = append(*w.callOrder, "validatewebhook:"+w.name)
	}
	return w.decision
}

type stubMutatingWebhook struct {
	name      string
	decision  admission.AdmissionDecision
	callOrder *[]string
}

func (w *stubMutatingWebhook) Name() string { return w.name }
func (w *stubMutatingWebhook) Mutate(_ context.Context, _ admission.AdmissionRequest) admission.AdmissionDecision {
	if w.callOrder != nil {
		*w.callOrder = append(*w.callOrder, "mutatewebhook:"+w.name)
	}
	return w.decision
}

type panicPolicy struct{ name string }

func (p *panicPolicy) Name() string { return p.name }
func (p *panicPolicy) Validate(_ context.Context, _ admission.AdmissionRequest) admission.AdmissionDecision {
	panic("test panic from " + p.name)
}

func newChain(t *testing.T) *admission.Chain {
	t.Helper()
	return admission.NewChain(zap.NewNop())
}

func baseReq() admission.AdmissionRequest {
	return admission.AdmissionRequest{Kind: "Product", Name: "p", Namespace: "ns", Operation: admission.OperationCreate}
}

// --- tests ---

func TestChain_EmptyChain_ReturnsAllowed(t *testing.T) {
	c := newChain(t)
	d := c.Admit(context.Background(), baseReq())
	_, ok := d.(admission.Allowed)
	assert.True(t, ok, "empty chain must return Allowed")
}

func TestChain_SingleValidatingPolicy_Allowed(t *testing.T) {
	c := newChain(t)
	cond := admission.AdmissionCondition{Type: "MyCheck", Status: true}
	c.RegisterValidatingPolicy(&stubValidatingPolicy{name: "p1", decision: admission.DecisionAllow(cond)})
	d := c.Admit(context.Background(), baseReq())
	allowed, ok := d.(admission.Allowed)
	require.True(t, ok)
	require.Len(t, allowed.Conditions, 1)
	assert.Equal(t, "MyCheck", allowed.Conditions[0].Type)
}

func TestChain_SingleValidatingPolicy_Denied(t *testing.T) {
	c := newChain(t)
	c.RegisterValidatingPolicy(&stubValidatingPolicy{name: "p1", decision: admission.DecisionDeny("bad", "spec.title")})
	d := c.Admit(context.Background(), baseReq())
	denied, ok := d.(admission.Denied)
	require.True(t, ok, "chain must propagate Denied")
	assert.Equal(t, "bad", denied.Reason)
}

func TestChain_DenialShortCircuits(t *testing.T) {
	c := newChain(t)
	var order []string
	c.RegisterValidatingPolicy(&stubValidatingPolicy{name: "first", decision: admission.DecisionDeny("stop", ""), callOrder: &order})
	c.RegisterValidatingPolicy(&stubValidatingPolicy{name: "second", decision: admission.DecisionAllow(), callOrder: &order})
	d := c.Admit(context.Background(), baseReq())
	_, ok := d.(admission.Denied)
	require.True(t, ok)
	assert.Equal(t, []string{"validate:first"}, order, "second policy must not be called after denial")
}

func TestChain_ConditionsAccumulatedFromMultiplePolicies(t *testing.T) {
	c := newChain(t)
	c.RegisterValidatingPolicy(&stubValidatingPolicy{name: "p1", decision: admission.DecisionAllow(
		admission.AdmissionCondition{Type: "CheckA", Status: true},
	)})
	c.RegisterValidatingPolicy(&stubValidatingPolicy{name: "p2", decision: admission.DecisionAllow(
		admission.AdmissionCondition{Type: "CheckB", Status: false},
	)})
	d := c.Admit(context.Background(), baseReq())
	allowed, ok := d.(admission.Allowed)
	require.True(t, ok)
	types := make(map[string]bool)
	for _, cond := range allowed.Conditions {
		types[cond.Type] = cond.Status
	}
	assert.True(t, types["CheckA"])
	assert.False(t, types["CheckB"])
}

func TestChain_PanicInPolicy_RecoveredAndDenied(t *testing.T) {
	c := newChain(t)
	c.RegisterValidatingPolicy(&panicPolicy{name: "panicker"})
	d := c.Admit(context.Background(), baseReq())
	denied, ok := d.(admission.Denied)
	require.True(t, ok, "panic must be recovered as Denied")
	assert.Equal(t, "InternalError", denied.Reason)
}

func TestChain_NilDecisionTreatedAsAllowed(t *testing.T) {
	c := newChain(t)
	c.RegisterValidatingPolicy(&stubValidatingPolicy{name: "nil-returner", decision: nil})
	d := c.Admit(context.Background(), baseReq())
	_, ok := d.(admission.Allowed)
	assert.True(t, ok)
}

// TestChain_MutatingPolicyBeforeValidating — US3 acceptance scenario 1
func TestChain_MutatingPolicyBeforeValidating(t *testing.T) {
	c := newChain(t)
	var order []string
	c.RegisterMutatingPolicy(&stubMutatingPolicy{name: "m1", decision: admission.DecisionAllow(), callOrder: &order})
	c.RegisterValidatingPolicy(&stubValidatingPolicy{name: "v1", decision: admission.DecisionAllow(), callOrder: &order})
	c.Admit(context.Background(), baseReq())
	require.Len(t, order, 2)
	assert.Equal(t, "mutate:m1", order[0], "mutating policy must run before validating policy")
	assert.Equal(t, "validate:v1", order[1])
}

// TestChain_PatchPropagation — US3 acceptance scenario 2
func TestChain_PatchPropagation(t *testing.T) {
	c := newChain(t)
	patch := json.RawMessage(`{"spec":{"title":"default"}}`)
	c.RegisterMutatingPolicy(&stubMutatingPolicy{name: "defaulter", decision: admission.Allowed{Patches: []json.RawMessage{patch}}})
	d := c.Admit(context.Background(), baseReq())
	allowed, ok := d.(admission.Allowed)
	require.True(t, ok)
	require.Len(t, allowed.Patches, 1)
	assert.JSONEq(t, `{"spec":{"title":"default"}}`, string(allowed.Patches[0]))
}

// TestChain_MutatingWebhookPhase — US4 acceptance scenario 2
// Mutating webhook runs after built-in mutating policies and before validating policies.
func TestChain_MutatingWebhookPhase(t *testing.T) {
	c := newChain(t)
	var order []string
	c.RegisterMutatingPolicy(&stubMutatingPolicy{name: "mp", decision: admission.DecisionAllow(), callOrder: &order})
	c.RegisterMutatingWebhook(&stubMutatingWebhook{name: "mw", decision: admission.DecisionAllow(), callOrder: &order})
	c.RegisterValidatingPolicy(&stubValidatingPolicy{name: "vp", decision: admission.DecisionAllow(), callOrder: &order})
	c.Admit(context.Background(), baseReq())
	require.Len(t, order, 3)
	assert.Equal(t, "mutate:mp", order[0])
	assert.Equal(t, "mutatewebhook:mw", order[1])
	assert.Equal(t, "validate:vp", order[2])
}

// TestChain_ValidatingWebhookAfterPolicies — US4 acceptance scenario 1
// Validating webhook runs after all built-in validating policies.
func TestChain_ValidatingWebhookAfterPolicies(t *testing.T) {
	c := newChain(t)
	var order []string
	c.RegisterValidatingPolicy(&stubValidatingPolicy{name: "vp1", decision: admission.DecisionAllow(), callOrder: &order})
	c.RegisterValidatingPolicy(&stubValidatingPolicy{name: "vp2", decision: admission.DecisionAllow(), callOrder: &order})
	c.RegisterValidatingWebhook(&stubValidatingWebhook{name: "vw", decision: admission.DecisionAllow(), callOrder: &order})
	c.Admit(context.Background(), baseReq())
	require.Len(t, order, 3)
	assert.Equal(t, "validate:vp1", order[0])
	assert.Equal(t, "validate:vp2", order[1])
	assert.Equal(t, "validatewebhook:vw", order[2], "validating webhook must run after all validating policies")
}

func TestChain_ValidatingWebhookDenial_ShortCircuits(t *testing.T) {
	c := newChain(t)
	var order []string
	c.RegisterValidatingPolicy(&stubValidatingPolicy{name: "vp", decision: admission.DecisionAllow(), callOrder: &order})
	c.RegisterValidatingWebhook(&stubValidatingWebhook{name: "wh-deny", decision: admission.DecisionDeny("webhook rejected", ""), callOrder: &order})
	c.RegisterValidatingWebhook(&stubValidatingWebhook{name: "wh-after", decision: admission.DecisionAllow(), callOrder: &order})
	d := c.Admit(context.Background(), baseReq())
	denied, ok := d.(admission.Denied)
	require.True(t, ok)
	assert.Equal(t, "webhook rejected", denied.Reason)
	assert.NotContains(t, order, "validatewebhook:wh-after")
}

// TestChain_MutatingPolicyConditionsPropagated verifies that conditions returned
// by mutating policies are preserved in the final Allowed result alongside patches.
func TestChain_MutatingPolicyConditionsPropagated(t *testing.T) {
	c := newChain(t)
	cond := admission.AdmissionCondition{Type: "MutationApplied", Status: true}
	patch := json.RawMessage(`{"spec":{"title":"default"}}`)
	c.RegisterMutatingPolicy(&stubMutatingPolicy{
		name:     "defaulter",
		decision: admission.Allowed{Conditions: []admission.AdmissionCondition{cond}, Patches: []json.RawMessage{patch}},
	})
	d := c.Admit(context.Background(), baseReq())
	allowed, ok := d.(admission.Allowed)
	require.True(t, ok)
	require.Len(t, allowed.Patches, 1, "patch must be preserved")
	require.Len(t, allowed.Conditions, 1, "condition from mutating phase must be propagated")
	assert.Equal(t, "MutationApplied", allowed.Conditions[0].Type)
	assert.True(t, allowed.Conditions[0].Status)
}

// TestChain_MutatingWebhookConditionsPropagated verifies that conditions returned
// by mutating webhooks are preserved in the final Allowed result.
func TestChain_MutatingWebhookConditionsPropagated(t *testing.T) {
	c := newChain(t)
	cond := admission.AdmissionCondition{Type: "WebhookMutated", Status: true}
	c.RegisterMutatingWebhook(&stubMutatingWebhook{
		name:     "wh",
		decision: admission.Allowed{Conditions: []admission.AdmissionCondition{cond}},
	})
	d := c.Admit(context.Background(), baseReq())
	allowed, ok := d.(admission.Allowed)
	require.True(t, ok)
	require.Len(t, allowed.Conditions, 1)
	assert.Equal(t, "WebhookMutated", allowed.Conditions[0].Type)
}

// TestChain_MutatingConditionsMergedWithValidatingConditions verifies last-writer-wins
// when a mutating and a validating phase both set a condition of the same Type.
func TestChain_MutatingConditionsMergedWithValidatingConditions(t *testing.T) {
	c := newChain(t)
	c.RegisterMutatingPolicy(&stubMutatingPolicy{
		name:     "mutator",
		decision: admission.Allowed{Conditions: []admission.AdmissionCondition{{Type: "SharedCheck", Status: false}}},
	})
	c.RegisterValidatingPolicy(&stubValidatingPolicy{
		name:     "validator",
		decision: admission.DecisionAllow(admission.AdmissionCondition{Type: "SharedCheck", Status: true}),
	})
	d := c.Admit(context.Background(), baseReq())
	allowed, ok := d.(admission.Allowed)
	require.True(t, ok)
	require.Len(t, allowed.Conditions, 1, "duplicate Type must be merged (last-writer-wins)")
	assert.True(t, allowed.Conditions[0].Status, "validating phase must override mutating phase value")
}
