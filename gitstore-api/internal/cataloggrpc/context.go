// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import "time"

// ValidationContext carries per-request state for the pre-receive validation
// phase. It is populated once per ValidateResources call and threaded through
// all per-file validators, avoiding repeated DB lookups per blob.
type ValidationContext struct {
	RepositoryID string
	Namespace    string // resolved namespace identifier (e.g. "my-store")
}

// AdmissionContext carries per-request state for the post-receive admission
// phase. It is populated once per AdmitResources call from a single DB lookup
// (repository → namespace) and threaded through all per-resource admit helpers.
type AdmissionContext struct {
	RepositoryID string
	Namespace    string    // resolved namespace identifier
	CommitSHA    string    // full SHA of the accepted push commit
	RefName      string    // fully-qualified ref, e.g. "refs/heads/main"
	Revision     string    // human revision label, e.g. "main@sha1:abc123"
	Now          time.Time // admission timestamp, set once for the entire push
}
