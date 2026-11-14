// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admission

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
)

// ProjectCommittedRBAC receives the whole candidate already authorized before
// its Git commit. Post-commit repair must not reauthorize or roll back that ref.
func ProjectCommittedRBAC(ctx context.Context, store datastore.RBACStore, write datastore.RBACTierWrite) (*CommittedRBACResult, error) {
	if store == nil || write.Projection == nil || write.Projection.GitRevision == "" {
		return nil, datastore.RBACError(datastore.ErrRBACInvalid)
	}
	identity := *write.Projection
	write.Projection = &identity
	ctx = datastore.ContextWithRBACAuditActor(ctx, identity.Actor)
	result := &CommittedRBACResult{CommitSHA: identity.GitRevision, ProjectionPending: true}
	applyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	state, err := store.ApplyRBAC(applyCtx, write)
	cancel()
	if err == nil && state.Projection == identity && !state.ProjectionPending {
		result.State, result.ProjectionPending = state, false
		return result, nil
	}
	if err == nil {
		err = datastore.RBACError(datastore.ErrRBACUnavailable)
	}
	// Match the existing committed-manifest repair boundary: survive request
	// cancellation, but never start an unbounded retry or background worker.
	repairCtx, repairCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer repairCancel()
	for attempt := 0; attempt < 3 && repairCtx.Err() == nil; attempt++ {
		current, readErr := store.ReadRBACTier(repairCtx, write.TierID)
		if readErr != nil {
			err = readErr
		} else if current.Projection == identity {
			result.State = current
			if current.ProjectionPending {
				if err = store.RepairRBACProjection(repairCtx, write.TierID, current.Revision); err != nil {
					continue
				}
				current, err = store.ReadRBACTier(repairCtx, write.TierID)
				if err != nil {
					continue
				}
			}
			if current.Projection == identity && !current.ProjectionPending {
				result.State, result.ProjectionPending = current, false
				return result, nil
			}
			err = datastore.RBACError(datastore.ErrRBACUnavailable)
		} else if current.Projection.GitRevision != write.ExpectedGitRevision {
			return result, fmt.Errorf("%w: %w", ErrCommittedRBACProjectionPending, ErrCommittedManifestSuperseded)
		} else if current.Revision != write.ExpectedRevision {
			rebased, rebaseErr := rebaseCommittedRBAC(repairCtx, store, current, write)
			if rebaseErr != nil {
				err = rebaseErr
				continue
			}
			write = rebased
		}
		if attempt < 2 {
			state, applyErr := store.ApplyRBAC(repairCtx, write)
			if applyErr == nil && state.Projection == identity && !state.ProjectionPending {
				result.State, result.ProjectionPending = state, false
				return result, nil
			}
			if applyErr != nil {
				err = applyErr
			}
		}
	}
	return result, fmt.Errorf("%w: %w", ErrCommittedRBACProjectionPending, err)
}

func rebaseCommittedRBAC(ctx context.Context, store datastore.RBACStore, state datastore.RBACTierState, write datastore.RBACTierWrite) (datastore.RBACTierWrite, error) {
	if state.Projection.RepositoryID != write.Projection.RepositoryID || state.Projection.GitRef != write.Projection.GitRef {
		return write, ErrCommittedManifestSuperseded
	}
	changes := make([]datastore.RBACChange, len(write.Changes))
	for index, change := range write.Changes {
		resource, err := rbac.CloneResource(change.Resource)
		if err != nil {
			return write, err
		}
		current, err := store.GetRBAC(ctx, datastore.RBACResourceKey{TierID: write.TierID, Kind: resource.Kind, Name: resource.Metadata.Name})
		if change.ExpectedUID == "" {
			if !errors.Is(err, datastore.ErrRBACNotFound) {
				if err != nil {
					return write, err
				}
				return write, datastore.ErrRBACConflict
			}
		} else {
			if err != nil {
				return write, err
			}
			if current.Metadata.UID != change.ExpectedUID || current.Metadata.Generation != resource.Metadata.Generation {
				return write, datastore.ErrRBACConflict
			}
			labels, annotations := resource.Metadata.Labels, resource.Metadata.Annotations
			resource.Metadata = current.Metadata
			resource.Metadata.Labels, resource.Metadata.Annotations = labels, annotations
			resource.Status = current.Status
			change.ExpectedResourceVersion = current.Metadata.ResourceVersion
		}
		change.Resource = resource
		changes[index] = change
	}
	write.Changes, write.ExpectedRevision = changes, state.Revision
	return write, nil
}
