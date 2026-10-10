// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admission

import (
	"context"
	"errors"
)

// ErrCommittedManifestSuperseded means a committed file was replaced before
// it could be admitted. Callers must begin a new authoring attempt.
var ErrCommittedManifestSuperseded = errors.New("committed manifest superseded")

// CommittedManifestRequest describes one already-committed resource manifest.
// Both the post-receive batch path and synchronous GraphQL mutations use this
// contract after the Git write has completed. The commit is authoritative; the
// datastore is only a materialized admission result.
type CommittedManifestRequest struct {
	RepositoryID string
	Namespace    string
	ActorSubject string
	CommitSHA    string
	RefName      string
	Path         string
	Content      []byte
	Operation    Operation
	// Kind and Name identify a removed resource, whose manifest no longer
	// exists at CommitSHA. They are used only for OperationDelete.
	Kind string
	Name string
	// ExpectedUID binds infrastructure deletion to the admitted incarnation.
	ExpectedUID string
}

// CommittedManifestResult identifies the resource admitted from a committed
// manifest. Callers hydrate the typed datastore projection they require.
type CommittedManifestResult struct {
	Kind      string
	Namespace string
	Name      string
	CommitSHA string
	// NoOp is set when admission found nothing to change, so the stored
	// record may carry an earlier commit with identical content.
	NoOp bool
	// Warnings are non-fatal post-receive diagnostics.
	Warnings []Diagnostic
}

// EntryOutcome classifies the admission of one manifest.
type EntryOutcome string

const (
	EntryAccepted EntryOutcome = "ACCEPTED"
	EntryNoOp     EntryOutcome = "NO_OP"
	EntryDenied   EntryOutcome = "DENIED"
	EntryFailed   EntryOutcome = "FAILED"
)

// EntryDecision is the admission result for one manifest. Denied carries
// diagnostics; Failed carries the underlying error.
type EntryDecision struct {
	Kind        string
	Namespace   string
	Name        string
	Path        string
	Outcome     EntryOutcome
	Diagnostics []Diagnostic
	Warnings    []Diagnostic
	Err         error
}

// CommittedManifestAdmitter is implemented by the catalog admission runtime.
// Keeping the boundary here lets GraphQL and gRPC share it without either
// package depending on the other.
type CommittedManifestAdmitter interface {
	AdmitCommittedManifest(context.Context, CommittedManifestRequest) (*CommittedManifestResult, error)
}
