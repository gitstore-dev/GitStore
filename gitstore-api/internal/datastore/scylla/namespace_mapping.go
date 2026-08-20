// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v3/qb"
	"go.uber.org/zap"
)

const (
	repositorySagaReserveTarget       = "reserve_target"
	repositorySagaUpdateAuthoritative = "update_authoritative"
	repositorySagaUpdateReverse       = "update_reverse_mapping"
	repositorySagaCleanupOldPath      = "cleanup_old_path"
)

type namespaceMappingRow struct {
	Namespace    string     `db:"namespace"`
	Name         string     `db:"name"`
	RepositoryID gocql.UUID `db:"repository_id"`
}

type repositoryPath struct {
	namespace string
	name      string
}

func (p repositoryPath) key() string {
	return p.namespace + "/" + p.name
}

type repositorySagaVersion int

const (
	repositorySagaSpecVersion repositorySagaVersion = iota
	repositorySagaSystemVersion
)

type repositorySagaOperations struct {
	injector      failureInjector
	reserve       func(context.Context, repositoryPath, string) error
	release       func(context.Context, repositoryPath, string) error
	commit        func(context.Context, *datastore.Repository, repositoryPath, repositoryPath, repositorySagaVersion) (bool, error)
	updateReverse func(context.Context, repositoryPath, repositoryPath, string) error
	cleanup       func(context.Context, repositoryPath, string) error
	emit          func(datastore.ProjectionFinding)
}

func executeRepositoryMappingSaga(
	ctx context.Context,
	operations repositorySagaOperations,
	repository *datastore.Repository,
	source, target repositoryPath,
	version repositorySagaVersion,
) error {
	if operations.injector == nil {
		operations.injector = noopFailureInjector{}
	}
	authoritativeCommitted := repository.Namespace == target.namespace && repository.Name == target.name

	if err := operations.injector.Inject(repositorySagaReserveTarget, failureBefore); err != nil {
		return err
	}
	if err := operations.reserve(ctx, target, repository.UID); err != nil {
		return err
	}
	if err := operations.injector.Inject(repositorySagaReserveTarget, failureAfter); err != nil {
		if authoritativeCommitted {
			return err
		}
		return compensateRepositoryTarget(ctx, operations, repository, target, err)
	}

	if err := operations.injector.Inject(repositorySagaUpdateAuthoritative, failureBefore); err != nil {
		if authoritativeCommitted {
			return err
		}
		return compensateRepositoryTarget(ctx, operations, repository, target, err)
	}
	committed, err := operations.commit(ctx, repository, source, target, version)
	if err != nil {
		if committed {
			return repositorySagaRollForwardRequired(
				operations, repository, target, "namespace_mappings_by_repository",
				repositorySagaUpdateAuthoritative, datastore.FindingStale, err,
			)
		}
		return compensateRepositoryTarget(ctx, operations, repository, target, err)
	}
	if !committed {
		return compensateRepositoryTarget(
			ctx,
			operations,
			repository,
			target,
			fmt.Errorf("authoritative repository update did not commit"),
		)
	}
	if err := operations.injector.Inject(repositorySagaUpdateAuthoritative, failureAfter); err != nil {
		return repositorySagaRollForwardRequired(
			operations, repository, target, "namespace_mappings_by_repository",
			repositorySagaUpdateAuthoritative, datastore.FindingStale, err,
		)
	}

	if err := operations.injector.Inject(repositorySagaUpdateReverse, failureBefore); err != nil {
		return repositorySagaRollForwardRequired(
			operations, repository, target, "namespace_mappings_by_repository",
			repositorySagaUpdateReverse, datastore.FindingStale, err,
		)
	}
	if err := operations.updateReverse(ctx, source, target, repository.UID); err != nil {
		return repositorySagaRollForwardRequired(
			operations, repository, target, "namespace_mappings_by_repository",
			repositorySagaUpdateReverse, datastore.FindingStale, err,
		)
	}
	if err := operations.injector.Inject(repositorySagaUpdateReverse, failureAfter); err != nil {
		return repositorySagaRollForwardRequired(
			operations, repository, target, "namespace_mappings_by_repository",
			repositorySagaUpdateReverse, datastore.FindingStale, err,
		)
	}

	if err := operations.injector.Inject(repositorySagaCleanupOldPath, failureBefore); err != nil {
		return repositorySagaRollForwardRequired(
			operations, repository, source, "namespace_mappings",
			repositorySagaCleanupOldPath, datastore.FindingStale, err,
		)
	}
	if source != target {
		if err := operations.cleanup(ctx, source, repository.UID); err != nil {
			return repositorySagaRollForwardRequired(
				operations, repository, source, "namespace_mappings",
				repositorySagaCleanupOldPath, datastore.FindingStale, err,
			)
		}
	}
	if err := operations.injector.Inject(repositorySagaCleanupOldPath, failureAfter); err != nil {
		return repositorySagaRollForwardRequired(
			operations, repository, source, "namespace_mappings",
			repositorySagaCleanupOldPath, datastore.FindingStale, err,
		)
	}
	return nil
}

func repositorySagaRollForwardRequired(
	operations repositorySagaOperations,
	repository *datastore.Repository,
	path repositoryPath,
	projection, action string,
	findingType datastore.FindingType,
	primary error,
) error {
	if operations.emit != nil {
		operations.emit(datastore.ProjectionFinding{
			ResourceKind: "Repository",
			ResourceUID:  repository.UID,
			Projection:   projection,
			LookupKey:    path.key(),
			Operation:    "repository_mapping_saga",
			Type:         findingType,
		})
	}
	return datastore.NewRepairRequiredError(
		datastore.MutationStep{
			Operation:    "repository_mapping_saga",
			ResourceKind: "Repository",
			ResourceUID:  repository.UID,
			Projection:   projection,
			LookupKey:    path.key(),
			Action:       action,
		},
		primary,
		fmt.Errorf("authoritative repository version is committed; projections require roll-forward"),
	)
}

func compensateRepositoryTarget(
	ctx context.Context,
	operations repositorySagaOperations,
	repository *datastore.Repository,
	target repositoryPath,
	primary error,
) error {
	if err := operations.release(ctx, target, repository.UID); err != nil {
		return datastore.NewRepairRequiredError(
			datastore.MutationStep{
				Operation:    "repository_mapping_saga",
				ResourceKind: "Repository",
				ResourceUID:  repository.UID,
				Projection:   "namespace_mappings",
				LookupKey:    target.key(),
				Action:       repositorySagaReserveTarget,
			},
			primary,
			err,
		)
	}
	return primary
}

func (s *scyllaDatastore) CreateNamespaceMapping(ctx context.Context, mapping *datastore.NamespaceMapping) error {
	if mapping == nil {
		return fmt.Errorf("%w: namespace mapping is nil", datastore.ErrInvalidArgument)
	}
	datastore.NormalizeNamespaceMappingContract(mapping)
	if mapping.Namespace == "" || mapping.Name == "" || mapping.RepositoryID == "" {
		return fmt.Errorf("%w: namespace mapping fields are required", datastore.ErrInvalidArgument)
	}
	if _, err := gocql.ParseUUID(mapping.RepositoryID); err != nil {
		return fmt.Errorf("%w: invalid repository id %s", datastore.ErrInvalidArgument, mapping.RepositoryID)
	}

	path := repositoryPath{namespace: mapping.Namespace, name: mapping.Name}
	created, err := s.reserveRepositoryPathOwned(ctx, path, mapping.RepositoryID)
	if err != nil {
		return err
	}
	if err := s.reserveRepositoryReverseMapping(ctx, path, mapping.RepositoryID); err != nil {
		if !created {
			return err
		}
		if compensationErr := s.releaseRepositoryPath(ctx, path, mapping.RepositoryID); compensationErr != nil {
			return datastore.NewRepairRequiredError(
				datastore.MutationStep{
					Operation:    "create_namespace_mapping",
					ResourceKind: "Repository",
					ResourceUID:  mapping.RepositoryID,
					Projection:   "namespace_mappings_by_repository",
					LookupKey:    path.key(),
					Action:       "reserve",
				},
				err,
				compensationErr,
			)
		}
		return err
	}
	return nil
}

func (s *scyllaDatastore) LookupRepository(ctx context.Context, namespace, name string) (*datastore.NamespaceMapping, error) {
	path := repositoryPath{namespace: namespace, name: name}
	mapping, err := s.lookupRepositoryPath(ctx, path)
	if err != nil {
		return nil, err
	}
	repository, err := s.GetRepository(ctx, mapping.RepositoryID)
	if errors.Is(err, datastore.ErrNotFound) {
		reverse, reverseErr := s.lookupRepositoryReverseMapping(ctx, mapping.RepositoryID)
		if reverseErr != nil {
			return nil, reverseErr
		}
		if reverse.Namespace != namespace || reverse.Name != name {
			return nil, s.repositoryMappingRepairError(
				"lookup_repository", mapping.RepositoryID, "namespace_mappings_by_repository", path, datastore.FindingStale,
				fmt.Errorf("reverse mapping points to %s/%s", reverse.Namespace, reverse.Name),
			)
		}
		return mapping, nil
	}
	if err != nil {
		return nil, s.repositoryMappingRepairError(
			"lookup_repository", mapping.RepositoryID, "namespace_mappings", path, datastore.FindingStale, err,
		)
	}
	if repository.Namespace != namespace || repository.Name != name {
		return nil, s.repositoryMappingRepairError(
			"lookup_repository", mapping.RepositoryID, "namespace_mappings", path, datastore.FindingStale,
			fmt.Errorf("authoritative repository path is %s/%s", repository.Namespace, repository.Name),
		)
	}
	reverse, err := s.lookupRepositoryReverseMapping(ctx, mapping.RepositoryID)
	if errors.Is(err, datastore.ErrNotFound) {
		return nil, s.repositoryMappingRepairError(
			"lookup_repository", mapping.RepositoryID, "namespace_mappings_by_repository", path, datastore.FindingMissing, err,
		)
	}
	if err != nil {
		return nil, err
	}
	if reverse.Namespace != namespace || reverse.Name != name {
		return nil, s.repositoryMappingRepairError(
			"lookup_repository", mapping.RepositoryID, "namespace_mappings_by_repository", path, datastore.FindingStale,
			fmt.Errorf("reverse mapping points to %s/%s", reverse.Namespace, reverse.Name),
		)
	}
	return mapping, nil
}

func (s *scyllaDatastore) LookupNamespaceByRepoID(ctx context.Context, repositoryID string) (*datastore.NamespaceMapping, error) {
	mapping, err := s.lookupRepositoryReverseMapping(ctx, repositoryID)
	if err != nil {
		return nil, err
	}
	repository, err := s.GetRepository(ctx, repositoryID)
	if errors.Is(err, datastore.ErrNotFound) {
		path := repositoryPath{namespace: mapping.Namespace, name: mapping.Name}
		forward, forwardErr := s.lookupRepositoryPath(ctx, path)
		if forwardErr != nil {
			return nil, forwardErr
		}
		if forward.RepositoryID != repositoryID {
			return nil, s.repositoryMappingRepairError(
				"lookup_namespace_by_repository", repositoryID, "namespace_mappings", path, datastore.FindingDuplicate,
				fmt.Errorf("path is owned by repository %s", forward.RepositoryID),
			)
		}
		return mapping, nil
	}
	if err != nil {
		return nil, s.repositoryMappingRepairError(
			"lookup_namespace_by_repository", repositoryID, "namespace_mappings_by_repository",
			repositoryPath{namespace: mapping.Namespace, name: mapping.Name}, datastore.FindingStale, err,
		)
	}
	authoritative := repositoryPath{namespace: repository.Namespace, name: repository.Name}
	if mapping.Namespace != authoritative.namespace || mapping.Name != authoritative.name {
		return nil, s.repositoryMappingRepairError(
			"lookup_namespace_by_repository", repositoryID, "namespace_mappings_by_repository",
			authoritative, datastore.FindingStale,
			fmt.Errorf("reverse mapping points to %s/%s", mapping.Namespace, mapping.Name),
		)
	}
	forward, err := s.lookupRepositoryPath(ctx, authoritative)
	if errors.Is(err, datastore.ErrNotFound) {
		return nil, s.repositoryMappingRepairError(
			"lookup_namespace_by_repository", repositoryID, "namespace_mappings",
			authoritative, datastore.FindingMissing, err,
		)
	}
	if err != nil {
		return nil, err
	}
	if forward.RepositoryID != repositoryID {
		return nil, s.repositoryMappingRepairError(
			"lookup_namespace_by_repository", repositoryID, "namespace_mappings",
			authoritative, datastore.FindingDuplicate,
			fmt.Errorf("path is owned by repository %s", forward.RepositoryID),
		)
	}
	return mapping, nil
}

func (s *scyllaDatastore) LookupNamespaceByRepositoryID(ctx context.Context, repositoryID string) (*datastore.NamespaceMapping, error) {
	return s.LookupNamespaceByRepoID(ctx, repositoryID)
}

func (s *scyllaDatastore) RenameRepository(ctx context.Context, namespace, oldName, newName string) error {
	if namespace == "" || oldName == "" || newName == "" {
		return fmt.Errorf("%w: namespace, old name, and new name are required", datastore.ErrInvalidArgument)
	}
	if oldName == newName {
		_, err := s.LookupRepository(ctx, namespace, oldName)
		return err
	}

	source := repositoryPath{namespace: namespace, name: oldName}
	target := repositoryPath{namespace: namespace, name: newName}
	sourceMapping, sourceErr := s.lookupRepositoryPath(ctx, source)
	targetMapping, targetErr := s.lookupRepositoryPath(ctx, target)
	if sourceErr != nil && !errors.Is(sourceErr, datastore.ErrNotFound) {
		return sourceErr
	}
	if targetErr != nil && !errors.Is(targetErr, datastore.ErrNotFound) {
		return targetErr
	}
	if sourceMapping == nil && targetMapping == nil {
		return fmt.Errorf("%w: namespace mapping %s", datastore.ErrNotFound, source.key())
	}

	repositoryID := ""
	if sourceMapping != nil {
		repositoryID = sourceMapping.RepositoryID
	}
	if targetMapping != nil {
		if repositoryID != "" && targetMapping.RepositoryID != repositoryID {
			s.emitRepositoryMappingFinding(datastore.ProjectionFinding{
				ResourceKind: "Repository",
				ResourceUID:  targetMapping.RepositoryID,
				Projection:   "namespace_mappings",
				LookupKey:    target.key(),
				Operation:    "rename_repository",
				Type:         datastore.FindingDuplicate,
			})
			return fmt.Errorf("%w: namespace mapping %s", datastore.ErrAlreadyExists, target.key())
		}
		if sourceMapping != nil {
			s.emitRepositoryMappingFinding(datastore.ProjectionFinding{
				ResourceKind: "Repository",
				ResourceUID:  targetMapping.RepositoryID,
				Projection:   "namespace_mappings",
				LookupKey:    source.key(),
				Operation:    "rename_repository",
				Type:         datastore.FindingStale,
			})
		}
		repositoryID = targetMapping.RepositoryID
	}
	repository, err := s.GetRepository(ctx, repositoryID)
	if errors.Is(err, datastore.ErrNotFound) {
		return s.moveRepositoryMappingWithoutAuthoritative(ctx, repositoryID, source, target)
	}
	if err != nil {
		return s.repositoryMappingRepairError(
			"rename_repository", repositoryID, "namespace_mappings", source, datastore.FindingDangling, err,
		)
	}
	if !repositoryHasPath(repository, source) && !repositoryHasPath(repository, target) {
		return s.repositoryMappingRepairError(
			"rename_repository", repositoryID, "repositories_by_uid", target, datastore.FindingStale,
			fmt.Errorf("authoritative repository path is %s/%s", repository.Namespace, repository.Name),
		)
	}

	return executeRepositoryMappingSaga(
		ctx,
		s.repositorySagaOperations(),
		repository,
		source,
		target,
		repositorySagaSpecVersion,
	)
}

func (s *scyllaDatastore) TransferRepository(ctx context.Context, repositoryID, fromNamespace, toNamespace string) error {
	if repositoryID == "" || fromNamespace == "" || toNamespace == "" {
		return fmt.Errorf("%w: repository id, source namespace, and target namespace are required", datastore.ErrInvalidArgument)
	}
	if _, err := gocql.ParseUUID(repositoryID); err != nil {
		return fmt.Errorf("%w: invalid repository id %s", datastore.ErrInvalidArgument, repositoryID)
	}
	repository, err := s.GetRepository(ctx, repositoryID)
	if errors.Is(err, datastore.ErrNotFound) {
		return s.transferRepositoryMappingWithoutAuthoritative(ctx, repositoryID, fromNamespace, toNamespace)
	}
	if err != nil {
		return err
	}
	namespace, err := s.GetNamespaceByName(ctx, toNamespace)
	if err != nil {
		return fmt.Errorf("scylla: validate target namespace %s: %w", toNamespace, err)
	}
	if namespace.Name != toNamespace {
		return fmt.Errorf("%w: target namespace lookup returned %s", datastore.ErrConflict, namespace.Name)
	}
	source := repositoryPath{namespace: fromNamespace, name: repository.Name}
	target := repositoryPath{namespace: toNamespace, name: repository.Name}
	if fromNamespace == toNamespace {
		_, err := s.LookupNamespaceByRepoID(ctx, repositoryID)
		return err
	}
	if !repositoryHasPath(repository, source) && !repositoryHasPath(repository, target) {
		return fmt.Errorf(
			"%w: repository %s authoritative namespace is %s",
			datastore.ErrConflict,
			repositoryID,
			repository.Namespace,
		)
	}

	reverse, reverseErr := s.lookupRepositoryReverseMapping(ctx, repositoryID)
	if reverseErr != nil && !errors.Is(reverseErr, datastore.ErrNotFound) {
		return reverseErr
	}
	if reverse != nil &&
		(reverse.Namespace != source.namespace || reverse.Name != source.name) &&
		(reverse.Namespace != target.namespace || reverse.Name != target.name) {
		return s.repositoryMappingRepairError(
			"transfer_repository", repositoryID, "namespace_mappings_by_repository", target, datastore.FindingStale,
			fmt.Errorf("reverse mapping points to %s/%s", reverse.Namespace, reverse.Name),
		)
	}

	sourceMapping, sourceErr := s.lookupRepositoryPath(ctx, source)
	if sourceErr != nil && !errors.Is(sourceErr, datastore.ErrNotFound) {
		return sourceErr
	}
	if sourceMapping != nil && sourceMapping.RepositoryID != repositoryID {
		return s.repositoryMappingRepairError(
			"transfer_repository", repositoryID, "namespace_mappings", source, datastore.FindingDuplicate,
			fmt.Errorf("source path is owned by repository %s", sourceMapping.RepositoryID),
		)
	}
	targetMapping, targetErr := s.lookupRepositoryPath(ctx, target)
	if targetErr != nil && !errors.Is(targetErr, datastore.ErrNotFound) {
		return targetErr
	}
	if targetMapping != nil && targetMapping.RepositoryID != repositoryID {
		s.emitRepositoryMappingFinding(datastore.ProjectionFinding{
			ResourceKind: "Repository",
			ResourceUID:  targetMapping.RepositoryID,
			Projection:   "namespace_mappings",
			LookupKey:    target.key(),
			Operation:    "transfer_repository",
			Type:         datastore.FindingDuplicate,
		})
		return fmt.Errorf("%w: namespace mapping %s", datastore.ErrAlreadyExists, target.key())
	}
	if sourceMapping != nil && targetMapping != nil {
		s.emitRepositoryMappingFinding(datastore.ProjectionFinding{
			ResourceKind: "Repository",
			ResourceUID:  repositoryID,
			Projection:   "namespace_mappings",
			LookupKey:    source.key(),
			Operation:    "transfer_repository",
			Type:         datastore.FindingStale,
		})
	}

	return executeRepositoryMappingSaga(
		ctx,
		s.repositorySagaOperations(),
		repository,
		source,
		target,
		repositorySagaSystemVersion,
	)
}

func (s *scyllaDatastore) transferRepositoryMappingWithoutAuthoritative(
	ctx context.Context,
	repositoryID, fromNamespace, toNamespace string,
) error {
	reverse, err := s.lookupRepositoryReverseMapping(ctx, repositoryID)
	if err != nil {
		return err
	}
	if reverse.Namespace != fromNamespace && reverse.Namespace != toNamespace {
		return fmt.Errorf(
			"%w: repository mapping %s is in namespace %s",
			datastore.ErrNotFound,
			repositoryID,
			reverse.Namespace,
		)
	}
	source := repositoryPath{namespace: fromNamespace, name: reverse.Name}
	target := repositoryPath{namespace: toNamespace, name: reverse.Name}
	if source == target {
		return nil
	}
	return s.moveRepositoryMappingWithoutAuthoritative(ctx, repositoryID, source, target)
}

func (s *scyllaDatastore) moveRepositoryMappingWithoutAuthoritative(
	ctx context.Context,
	repositoryID string,
	source, target repositoryPath,
) error {
	created, err := s.reserveRepositoryPathOwned(ctx, target, repositoryID)
	if err != nil {
		return err
	}
	if err := s.setRepositoryReverseMapping(ctx, source, target, repositoryID); err != nil {
		if !created {
			return err
		}
		if compensationErr := s.releaseRepositoryPath(ctx, target, repositoryID); compensationErr != nil {
			return datastore.NewRepairRequiredError(
				datastore.MutationStep{
					Operation:    "move_repository_mapping",
					ResourceKind: "Repository",
					ResourceUID:  repositoryID,
					Projection:   "namespace_mappings_by_repository",
					LookupKey:    target.key(),
					Action:       repositorySagaUpdateReverse,
				},
				err,
				compensationErr,
			)
		}
		return err
	}
	if err := s.deleteRepositoryPath(ctx, source, repositoryID); err != nil {
		s.emitRepositoryMappingFinding(datastore.ProjectionFinding{
			ResourceKind: "Repository",
			ResourceUID:  repositoryID,
			Projection:   "namespace_mappings",
			LookupKey:    source.key(),
			Operation:    "move_repository_mapping",
			Type:         datastore.FindingStale,
		})
		return datastore.NewRepairRequiredError(
			datastore.MutationStep{
				Operation:    "move_repository_mapping",
				ResourceKind: "Repository",
				ResourceUID:  repositoryID,
				Projection:   "namespace_mappings",
				LookupKey:    source.key(),
				Action:       repositorySagaCleanupOldPath,
			},
			err,
			fmt.Errorf("target mapping is committed; old path requires cleanup"),
		)
	}
	return nil
}

func (s *scyllaDatastore) DeleteNamespaceMapping(ctx context.Context, namespace, name string) error {
	path := repositoryPath{namespace: namespace, name: name}
	mapping, err := s.lookupRepositoryPath(ctx, path)
	if err != nil {
		return err
	}
	if err := s.deleteRepositoryPath(ctx, path, mapping.RepositoryID); err != nil {
		return err
	}
	if err := s.deleteRepositoryReverseMapping(ctx, path, mapping.RepositoryID); err != nil {
		if restoreErr := s.reserveRepositoryPath(ctx, path, mapping.RepositoryID); restoreErr != nil {
			return datastore.NewRepairRequiredError(
				datastore.MutationStep{
					Operation:    "delete_namespace_mapping",
					ResourceKind: "Repository",
					ResourceUID:  mapping.RepositoryID,
					Projection:   "namespace_mappings_by_repository",
					LookupKey:    path.key(),
					Action:       "delete",
				},
				err,
				restoreErr,
			)
		}
		return err
	}
	return nil
}

func (s *scyllaDatastore) repositorySagaOperations() repositorySagaOperations {
	injector := failureInjector(noopFailureInjector{})
	if s.mutations != nil && s.mutations.injector != nil {
		injector = s.mutations.injector
	}
	return repositorySagaOperations{
		injector: injector,
		reserve:  s.reserveRepositoryPath,
		release:  s.releaseRepositoryPath,
		commit:   s.commitRepositorySaga,
		updateReverse: func(ctx context.Context, source, target repositoryPath, repositoryID string) error {
			return s.setRepositoryReverseMapping(ctx, source, target, repositoryID)
		},
		cleanup: s.deleteRepositoryPath,
		emit:    s.emitRepositoryMappingFinding,
	}
}

func (s *scyllaDatastore) commitRepositorySaga(
	ctx context.Context,
	initial *datastore.Repository,
	source, target repositoryPath,
	version repositorySagaVersion,
) (bool, error) {
	targetMapping, err := s.lookupRepositoryPath(ctx, target)
	if err != nil {
		return false, err
	}
	if targetMapping.RepositoryID != initial.UID {
		return false, s.repositoryMappingRepairError(
			"repository_mapping_saga", initial.UID, "namespace_mappings", target, datastore.FindingDuplicate,
			fmt.Errorf("target path is owned by repository %s", targetMapping.RepositoryID),
		)
	}
	repository, err := s.GetRepository(ctx, initial.UID)
	if err != nil {
		return false, err
	}
	if repositoryHasPath(repository, target) {
		if version == repositorySagaSystemVersion {
			previous := *repository
			previous.Namespace = source.namespace
			if err := s.syncRepositoryNamespaceProjection(ctx, &previous, repository); err != nil {
				return true, err
			}
		}
		return true, nil
	}
	if !repositoryHasPath(repository, source) {
		return false, s.repositoryMappingRepairError(
			"repository_mapping_saga", repository.UID, "repositories_by_uid", target, datastore.FindingStale,
			fmt.Errorf("authoritative repository path is %s/%s", repository.Namespace, repository.Name),
		)
	}
	if repository.ResourceVersion != initial.ResourceVersion {
		return false, datastore.ErrConflict
	}

	updated := *repository
	updated.Namespace = target.namespace
	updated.Name = target.name
	if version == repositorySagaSystemVersion {
		datastore.AdvanceRepositorySystemVersion(&updated)
	} else {
		datastore.AdvanceRepositorySpecVersion(&updated)
	}
	err = s.updateRepositoryAuthoritative(ctx, &updated, repository.CreationTimestamp, repository.ResourceVersion)
	if err != nil {
		current, readErr := s.GetRepository(ctx, repository.UID)
		if readErr == nil && repositoryHasPath(current, target) {
			if version == repositorySagaSystemVersion {
				if projectionErr := s.syncRepositoryNamespaceProjection(ctx, repository, current); projectionErr != nil {
					return true, projectionErr
				}
			}
			return true, nil
		}
		return false, err
	}
	if version == repositorySagaSystemVersion {
		if err := s.syncRepositoryNamespaceProjection(ctx, repository, &updated); err != nil {
			return true, err
		}
	}
	return true, nil
}

func repositoryHasPath(repository *datastore.Repository, path repositoryPath) bool {
	return repository != nil && repository.Namespace == path.namespace && repository.Name == path.name
}

func (s *scyllaDatastore) reserveRepositoryPath(ctx context.Context, path repositoryPath, repositoryID string) error {
	_, err := s.reserveRepositoryPathOwned(ctx, path, repositoryID)
	return err
}

func (s *scyllaDatastore) reserveRepositoryPathOwned(
	ctx context.Context,
	path repositoryPath,
	repositoryID string,
) (bool, error) {
	uid, err := gocql.ParseUUID(repositoryID)
	if err != nil {
		return false, fmt.Errorf("%w: invalid repository id %s", datastore.ErrInvalidArgument, repositoryID)
	}
	const statement = "INSERT INTO namespace_mappings (namespace, name, repository_id) VALUES (?, ?, ?) IF NOT EXISTS"
	applied, err := s.session.Query(statement, nil).WithContext(ctx).
		Bind(path.namespace, path.name, uid).ExecCASRelease()
	if err != nil {
		return false, fmt.Errorf("scylla: reserve namespace mapping %s: %w", path.key(), err)
	}
	if applied {
		return true, nil
	}
	existing, lookupErr := s.lookupRepositoryPath(ctx, path)
	if lookupErr != nil {
		return false, fmt.Errorf("scylla: inspect namespace mapping reservation %s: %w", path.key(), lookupErr)
	}
	if existing.RepositoryID == repositoryID {
		return false, nil
	}
	s.emitRepositoryMappingFinding(datastore.ProjectionFinding{
		ResourceKind: "Repository",
		ResourceUID:  existing.RepositoryID,
		Projection:   "namespace_mappings",
		LookupKey:    path.key(),
		Operation:    "reserve_repository_path",
		Type:         datastore.FindingDuplicate,
	})
	return false, fmt.Errorf("%w: namespace mapping %s", datastore.ErrAlreadyExists, path.key())
}

func (s *scyllaDatastore) reserveRepositoryReverseMapping(ctx context.Context, path repositoryPath, repositoryID string) error {
	_, err := s.reserveRepositoryReverseMappingOwned(ctx, path, repositoryID)
	return err
}

func (s *scyllaDatastore) reserveRepositoryReverseMappingOwned(
	ctx context.Context,
	path repositoryPath,
	repositoryID string,
) (bool, error) {
	uid := mustParseUUID(repositoryID)
	const statement = "INSERT INTO namespace_mappings_by_repository (repository_id, namespace, name) VALUES (?, ?, ?) IF NOT EXISTS"
	applied, err := s.session.Query(statement, nil).WithContext(ctx).
		Bind(uid, path.namespace, path.name).ExecCASRelease()
	if err != nil {
		return false, fmt.Errorf("scylla: reserve reverse namespace mapping: %w", err)
	}
	if applied {
		return true, nil
	}
	existing, lookupErr := s.lookupRepositoryReverseMapping(ctx, repositoryID)
	if lookupErr != nil {
		return false, fmt.Errorf("scylla: inspect reverse namespace mapping reservation: %w", lookupErr)
	}
	if existing.Namespace == path.namespace && existing.Name == path.name {
		return false, nil
	}
	return false, s.repositoryMappingRepairError(
		"create_namespace_mapping", repositoryID, "namespace_mappings_by_repository", path, datastore.FindingDuplicate,
		fmt.Errorf("repository already maps to %s/%s", existing.Namespace, existing.Name),
	)
}

func (s *scyllaDatastore) setRepositoryReverseMapping(
	ctx context.Context,
	source, target repositoryPath,
	repositoryID string,
) error {
	existing, err := s.lookupRepositoryReverseMapping(ctx, repositoryID)
	if errors.Is(err, datastore.ErrNotFound) {
		s.emitRepositoryMappingFinding(datastore.ProjectionFinding{
			ResourceKind: "Repository",
			ResourceUID:  repositoryID,
			Projection:   "namespace_mappings_by_repository",
			LookupKey:    target.key(),
			Operation:    "repository_mapping_saga",
			Type:         datastore.FindingMissing,
		})
		return s.reserveRepositoryReverseMapping(ctx, target, repositoryID)
	}
	if err != nil {
		return err
	}
	current := repositoryPath{namespace: existing.Namespace, name: existing.Name}
	if current == target {
		return nil
	}
	if current != source {
		return s.repositoryMappingRepairError(
			"repository_mapping_saga", repositoryID, "namespace_mappings_by_repository", target, datastore.FindingStale,
			fmt.Errorf("reverse mapping points to %s", current.key()),
		)
	}

	const statement = "UPDATE namespace_mappings_by_repository SET namespace=?, name=? WHERE repository_id=? IF namespace=? AND name=?"
	applied, err := s.session.Query(statement, nil).WithContext(ctx).
		Bind(target.namespace, target.name, mustParseUUID(repositoryID), source.namespace, source.name).ExecCASRelease()
	if err != nil {
		return fmt.Errorf("scylla: update reverse namespace mapping: %w", err)
	}
	if applied {
		return nil
	}
	currentMapping, lookupErr := s.lookupRepositoryReverseMapping(ctx, repositoryID)
	if lookupErr == nil && currentMapping.Namespace == target.namespace && currentMapping.Name == target.name {
		return nil
	}
	if lookupErr != nil {
		return lookupErr
	}
	return s.repositoryMappingRepairError(
		"repository_mapping_saga", repositoryID, "namespace_mappings_by_repository", target, datastore.FindingStale,
		fmt.Errorf("reverse mapping changed concurrently to %s/%s", currentMapping.Namespace, currentMapping.Name),
	)
}

func (s *scyllaDatastore) releaseRepositoryPath(ctx context.Context, path repositoryPath, repositoryID string) error {
	return s.deleteRepositoryPath(ctx, path, repositoryID)
}

func (s *scyllaDatastore) deleteRepositoryPath(ctx context.Context, path repositoryPath, repositoryID string) error {
	const statement = "DELETE FROM namespace_mappings WHERE namespace=? AND name=? IF repository_id=?"
	applied, err := s.session.Query(statement, nil).WithContext(ctx).
		Bind(path.namespace, path.name, mustParseUUID(repositoryID)).ExecCASRelease()
	if err != nil {
		return fmt.Errorf("scylla: conditionally delete namespace mapping %s: %w", path.key(), err)
	}
	if applied {
		return nil
	}
	existing, lookupErr := s.lookupRepositoryPath(ctx, path)
	if errors.Is(lookupErr, datastore.ErrNotFound) {
		return nil
	}
	if lookupErr != nil {
		return lookupErr
	}
	return s.repositoryMappingRepairError(
		"delete_repository_path", repositoryID, "namespace_mappings", path, datastore.FindingDuplicate,
		fmt.Errorf("path is owned by repository %s", existing.RepositoryID),
	)
}

func (s *scyllaDatastore) deleteRepositoryReverseMapping(
	ctx context.Context,
	path repositoryPath,
	repositoryID string,
) error {
	const statement = "DELETE FROM namespace_mappings_by_repository WHERE repository_id=? IF namespace=? AND name=?"
	applied, err := s.session.Query(statement, nil).WithContext(ctx).
		Bind(mustParseUUID(repositoryID), path.namespace, path.name).ExecCASRelease()
	if err != nil {
		return fmt.Errorf("scylla: conditionally delete reverse namespace mapping: %w", err)
	}
	if applied {
		return nil
	}
	existing, lookupErr := s.lookupRepositoryReverseMapping(ctx, repositoryID)
	if errors.Is(lookupErr, datastore.ErrNotFound) {
		return nil
	}
	if lookupErr != nil {
		return lookupErr
	}
	return s.repositoryMappingRepairError(
		"delete_namespace_mapping", repositoryID, "namespace_mappings_by_repository", path, datastore.FindingStale,
		fmt.Errorf("reverse mapping points to %s/%s", existing.Namespace, existing.Name),
	)
}

func (s *scyllaDatastore) lookupRepositoryPath(ctx context.Context, path repositoryPath) (*datastore.NamespaceMapping, error) {
	stmt, names := s.namespaceMappingTable.Get()
	var row namespaceMappingRow
	if err := s.session.Query(stmt, names).WithContext(ctx).
		BindMap(qb.M{"namespace": path.namespace, "name": path.name}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: namespace mapping %s", datastore.ErrNotFound, path.key())
		}
		return nil, fmt.Errorf("scylla: lookup repository path %s: %w", path.key(), err)
	}
	return fromNamespaceMappingRow(&row), nil
}

func (s *scyllaDatastore) lookupRepositoryReverseMapping(ctx context.Context, repositoryID string) (*datastore.NamespaceMapping, error) {
	repositoryUUID, err := gocql.ParseUUID(repositoryID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid repository id %s", datastore.ErrNotFound, repositoryID)
	}
	stmt, names := s.namespaceMappingByRepositoryTable.Get()
	var row namespaceMappingRow
	if err := s.session.Query(stmt, names).WithContext(ctx).
		BindMap(qb.M{"repository_id": repositoryUUID}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: repository mapping %s", datastore.ErrNotFound, repositoryID)
		}
		return nil, fmt.Errorf("scylla: reverse lookup namespace mapping: %w", err)
	}
	return fromNamespaceMappingRow(&row), nil
}

func (s *scyllaDatastore) repositoryMappingRepairError(
	operation, repositoryID, projection string,
	path repositoryPath,
	findingType datastore.FindingType,
	primary error,
) error {
	s.emitRepositoryMappingFinding(datastore.ProjectionFinding{
		ResourceKind: "Repository",
		ResourceUID:  repositoryID,
		Projection:   projection,
		LookupKey:    path.key(),
		Operation:    operation,
		Type:         findingType,
	})
	return datastore.NewRepairRequiredError(
		datastore.MutationStep{
			Operation:    operation,
			ResourceKind: "Repository",
			ResourceUID:  repositoryID,
			Projection:   projection,
			LookupKey:    path.key(),
			Action:       string(findingType),
		},
		primary,
		fmt.Errorf("repository mapping projections require repair"),
	)
}

func (s *scyllaDatastore) emitRepositoryMappingFinding(finding datastore.ProjectionFinding) {
	if s.log == nil {
		return
	}
	s.log.Warn("datastore projection inconsistency",
		zap.String("operation", finding.Operation),
		zap.String("resource_kind", finding.ResourceKind),
		zap.String("resource_uid", finding.ResourceUID),
		zap.String("projection", finding.Projection),
		zap.String("lookup_key", finding.LookupKey),
		zap.String("finding_type", string(finding.Type)),
	)
}

func fromNamespaceMappingRow(row *namespaceMappingRow) *datastore.NamespaceMapping {
	mapping := &datastore.NamespaceMapping{
		Namespace:    row.Namespace,
		Name:         row.Name,
		RepositoryID: row.RepositoryID.String(),
	}
	datastore.NormalizeNamespaceMappingContract(mapping)
	return mapping
}
