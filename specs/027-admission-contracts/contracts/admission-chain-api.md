# Contract: Admission Chain API

**Package**: `github.com/gitstore-dev/gitstore/api/internal/admission`  
**Stability**: Stable

## Overview

The admission chain runs resources through an ordered pipeline of mutating and validating extension points before they are hydrated into the datastore. The chain is the single orchestration point for all admission logic — callers never invoke individual policies directly.

## Types

```go
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
type AdmissionCondition struct {
    Type    string // matches catalog.ConditionType constants
    Status  bool   // true = condition satisfied
    Reason  string // optional machine-readable reason code
    Message string // optional human-readable detail
}

// AdmissionRequest is the input to the admission chain.
// Generic across all resource types and trigger paths.
type AdmissionRequest struct {
    Object     any                  // decoded resource; concrete struct or map[string]any
    OldObject  any                  // nil for creates
    Operation  Operation
    Kind       string
    Name       string
    Namespace  string
    Trigger    Trigger
    GitContext *GitAdmissionContext  // nil for non-git triggers
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
```

## Constructor Functions

```go
// DecisionAllow returns an Allowed decision with the given conditions.
func DecisionAllow(conditions ...AdmissionCondition) AdmissionDecision

// DecisionDeny returns a Denied decision.
func DecisionDeny(reason, field string) AdmissionDecision
```

## Extension-Point Interfaces

```go
// MutatingAdmissionPolicy is a built-in mutating controller (phase 1).
type MutatingAdmissionPolicy interface {
    Name() string
    Mutate(ctx context.Context, req AdmissionRequest) AdmissionDecision
}

// ValidatingAdmissionPolicy is a built-in validating controller (phase 3).
type ValidatingAdmissionPolicy interface {
    Name() string
    Validate(ctx context.Context, req AdmissionRequest) AdmissionDecision
}

// MutatingAdmissionWebhook is an external mutating extension point (phase 2).
// HTTP transport is not implemented in spec 027.
type MutatingAdmissionWebhook interface {
    Name() string
    Mutate(ctx context.Context, req AdmissionRequest) AdmissionDecision
}

// ValidatingAdmissionWebhook is an external validating extension point (phase 4).
// HTTP transport is not implemented in spec 027.
type ValidatingAdmissionWebhook interface {
    Name() string
    Validate(ctx context.Context, req AdmissionRequest) AdmissionDecision
}
```

## Chain

```go
// Chain runs admission in phase order:
//   1. Mutating policies  (built-in, registration order)
//   2. Mutating webhooks  (external, registration order)
//   3. Validating policies (built-in, registration order)
//   4. Validating webhooks (external, registration order)
//
// A Denied from any step short-circuits all remaining phases.
// Policy panics are recovered and treated as Denied{Reason: "InternalError"}.
type Chain struct { /* unexported fields */ }

// NewChain constructs an empty Chain with no registered extension points.
func NewChain(log *zap.Logger) *Chain

// RegisterMutatingPolicy appends a built-in mutating policy to phase 1.
func (c *Chain) RegisterMutatingPolicy(p MutatingAdmissionPolicy)

// RegisterMutatingWebhook appends an external mutating webhook to phase 2.
func (c *Chain) RegisterMutatingWebhook(w MutatingAdmissionWebhook)

// RegisterValidatingPolicy appends a built-in validating policy to phase 3.
func (c *Chain) RegisterValidatingPolicy(p ValidatingAdmissionPolicy)

// RegisterValidatingWebhook appends an external validating webhook to phase 4.
func (c *Chain) RegisterValidatingWebhook(w ValidatingAdmissionWebhook)

// Admit runs the full chain against req.
// Returns Allowed (with accumulated conditions from all validating phases) or
// the first Denied encountered.
func (c *Chain) Admit(ctx context.Context, req AdmissionRequest) AdmissionDecision
```

## Behavioural Invariants

1. An empty chain (no registered extension points) returns `Allowed{}` immediately.
2. Phases execute in order 1 → 2 → 3 → 4. Within a phase, extension points execute in registration order.
3. A `Denied` result from any extension point short-circuits all subsequent extension points and phases.
4. `Conditions` from all validating policies (phases 3 and 4) are accumulated and merged into the final `Allowed`. The last writer wins for duplicate `Type` values.
5. `Patches` from all mutating policies (phases 1 and 2) are accumulated in order. Patch application is the caller's responsibility.
6. A policy that panics is recovered; the decision is `Denied{Reason: "InternalError", Field: ""}` and the panic is logged with stack trace via `zap.Logger`.
7. The `PushSet` field in `AdmissionRequest` is read-only; policies MUST NOT modify it.
8. A policy that returns `nil` is treated as `Allowed{}`.
