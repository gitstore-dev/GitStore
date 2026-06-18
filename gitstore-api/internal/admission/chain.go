// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"

	"go.uber.org/zap"
)

// Chain runs resources through an ordered admission pipeline:
//
//  1. Mutating policies  (built-in, registration order)
//  2. Mutating webhooks  (external, registration order)
//  3. Validating policies (built-in, registration order)
//  4. Validating webhooks (external, registration order)
//
// A Denied result from any step short-circuits all remaining phases.
// Policy panics are recovered and treated as Denied{Reason: "InternalError"}.
type Chain struct {
	mutatingPolicies   []MutatingAdmissionPolicy
	mutatingWebhooks   []MutatingAdmissionWebhook
	validatingPolicies []ValidatingAdmissionPolicy
	validatingWebhooks []ValidatingAdmissionWebhook
	log                *zap.Logger
}

// NewChain constructs an empty Chain with no registered extension points.
func NewChain(log *zap.Logger) *Chain {
	return &Chain{log: log}
}

// RegisterMutatingPolicy appends a built-in mutating policy to phase 1.
func (c *Chain) RegisterMutatingPolicy(p MutatingAdmissionPolicy) {
	c.mutatingPolicies = append(c.mutatingPolicies, p)
}

// RegisterMutatingWebhook appends an external mutating webhook to phase 2.
func (c *Chain) RegisterMutatingWebhook(w MutatingAdmissionWebhook) {
	c.mutatingWebhooks = append(c.mutatingWebhooks, w)
}

// RegisterValidatingPolicy appends a built-in validating policy to phase 3.
func (c *Chain) RegisterValidatingPolicy(p ValidatingAdmissionPolicy) {
	c.validatingPolicies = append(c.validatingPolicies, p)
}

// RegisterValidatingWebhook appends an external validating webhook to phase 4.
func (c *Chain) RegisterValidatingWebhook(w ValidatingAdmissionWebhook) {
	c.validatingWebhooks = append(c.validatingWebhooks, w)
}

// Admit runs the full chain against req.
// Returns Allowed (with accumulated conditions from all validating phases) or
// the first Denied encountered.
func (c *Chain) Admit(ctx context.Context, req AdmissionRequest) AdmissionDecision {
	var accPatches []json.RawMessage
	var accConditions []AdmissionCondition

	// Phase 1: mutating policies
	for _, p := range c.mutatingPolicies {
		d, err := safeCall(func() AdmissionDecision { return p.Mutate(ctx, req) })
		if err != nil {
			c.log.Error("admission: mutating policy panic",
				zap.String("policy", p.Name()),
				zap.String("kind", req.Kind),
				zap.String("name", req.Name),
				zap.String("stack", err.Error()))
			return Denied{Reason: "InternalError"}
		}
		d = normalise(d)
		switch v := d.(type) {
		case Denied:
			return v
		case Allowed:
			accPatches = append(accPatches, v.Patches...)
			accConditions = mergeConditions(accConditions, v.Conditions)
		}
	}

	// Phase 2: mutating webhooks
	for _, w := range c.mutatingWebhooks {
		d, err := safeCall(func() AdmissionDecision { return w.Mutate(ctx, req) })
		if err != nil {
			c.log.Error("admission: mutating webhook panic",
				zap.String("webhook", w.Name()),
				zap.String("kind", req.Kind),
				zap.String("name", req.Name),
				zap.String("stack", err.Error()))
			return Denied{Reason: "InternalError"}
		}
		d = normalise(d)
		switch v := d.(type) {
		case Denied:
			return v
		case Allowed:
			accPatches = append(accPatches, v.Patches...)
			accConditions = mergeConditions(accConditions, v.Conditions)
		}
	}

	// Phase 3: validating policies
	for _, p := range c.validatingPolicies {
		d, err := safeCall(func() AdmissionDecision { return p.Validate(ctx, req) })
		if err != nil {
			c.log.Error("admission: validating policy panic",
				zap.String("policy", p.Name()),
				zap.String("kind", req.Kind),
				zap.String("name", req.Name),
				zap.String("stack", err.Error()))
			return Denied{Reason: "InternalError"}
		}
		d = normalise(d)
		switch v := d.(type) {
		case Denied:
			return v
		case Allowed:
			accConditions = mergeConditions(accConditions, v.Conditions)
		}
	}

	// Phase 4: validating webhooks
	for _, w := range c.validatingWebhooks {
		d, err := safeCall(func() AdmissionDecision { return w.Validate(ctx, req) })
		if err != nil {
			c.log.Error("admission: validating webhook panic",
				zap.String("webhook", w.Name()),
				zap.String("kind", req.Kind),
				zap.String("name", req.Name),
				zap.String("stack", err.Error()))
			return Denied{Reason: "InternalError"}
		}
		d = normalise(d)
		switch v := d.(type) {
		case Denied:
			return v
		case Allowed:
			accConditions = mergeConditions(accConditions, v.Conditions)
		}
	}

	return Allowed{Conditions: accConditions, Patches: accPatches}
}

// safeCall invokes f and recovers from any panic, returning the panic as an error.
func safeCall(f func() AdmissionDecision) (d AdmissionDecision, err error) {
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			err = fmt.Errorf("panic: %v\n%s", r, stack)
		}
	}()
	return f(), nil
}

// normalise converts nil to Allowed{}.
func normalise(d AdmissionDecision) AdmissionDecision {
	if d == nil {
		return Allowed{}
	}
	return d
}

// mergeConditions appends src conditions to dst, with last-writer-wins for duplicate Type values.
func mergeConditions(dst, src []AdmissionCondition) []AdmissionCondition {
	for _, s := range src {
		found := false
		for i, d := range dst {
			if d.Type == s.Type {
				dst[i] = s
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, s)
		}
	}
	return dst
}
