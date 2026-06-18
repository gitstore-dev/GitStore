// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admission

import "context"

// MutatingAdmissionPolicy is a built-in mutating controller (chain phase 1).
// Implementations may return Allowed with Patches to modify the resource before
// validation, or Denied to halt the chain.
type MutatingAdmissionPolicy interface {
	Name() string
	Mutate(ctx context.Context, req AdmissionRequest) AdmissionDecision
}

// ValidatingAdmissionPolicy is a built-in validating controller (chain phase 3).
// Implementations return Allowed (with optional Conditions) or Denied.
// Patches in an Allowed result from a validating policy are ignored.
type ValidatingAdmissionPolicy interface {
	Name() string
	Validate(ctx context.Context, req AdmissionRequest) AdmissionDecision
}

// MutatingAdmissionWebhook is an external mutating extension point (chain phase 2).
// HTTP transport is not implemented in spec 027; this interface exists as a
// named extension point so future specs can wire external callouts without
// redefining the chain.
type MutatingAdmissionWebhook interface {
	Name() string
	Mutate(ctx context.Context, req AdmissionRequest) AdmissionDecision
}

// ValidatingAdmissionWebhook is an external validating extension point (chain phase 4).
// HTTP transport is not implemented in spec 027.
type ValidatingAdmissionWebhook interface {
	Name() string
	Validate(ctx context.Context, req AdmissionRequest) AdmissionDecision
}
