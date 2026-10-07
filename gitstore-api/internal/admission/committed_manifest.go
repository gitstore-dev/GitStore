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
}

// CommittedManifestResult identifies the resource admitted from a committed
// manifest. Callers hydrate the typed datastore projection they require.
type CommittedManifestResult struct {
	Kind      string
	Namespace string
	Name      string
	CommitSHA string
}

// CommittedManifestAdmitter is implemented by the catalog admission runtime.
// Keeping the boundary here lets GraphQL and gRPC share it without either
// package depending on the other.
type CommittedManifestAdmitter interface {
	AdmitCommittedManifest(context.Context, CommittedManifestRequest) (*CommittedManifestResult, error)
}
