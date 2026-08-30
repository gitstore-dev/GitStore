// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import "time"

// AdmissionContext carries per-request state for the post-receive admission
// phase. It is populated once per AdmitResources call from a single DB lookup
// (repository → namespace) and threaded through all per-resource admit helpers.
type AdmissionContext struct {
	RepositoryID string
	Namespace    string    // resolved namespace identifier
	ActorSubject string    // authenticated principal that performed the push
	CommitSHA    string    // full SHA of the accepted push commit
	RefName      string    // fully-qualified ref, e.g. "refs/heads/main"
	Revision     string    // human revision label, e.g. "main@sha1:abc123"
	Now          time.Time // admission timestamp, set once for the entire push
	superseded   *bool
}

func (c AdmissionContext) markSuperseded() {
	if c.superseded != nil {
		*c.superseded = true
	}
}

func (c AdmissionContext) wasSuperseded() bool {
	return c.superseded != nil && *c.superseded
}
