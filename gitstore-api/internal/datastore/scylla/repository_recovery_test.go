// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sagaTestRepositoryID = "11111111-1111-1111-1111-111111111111"

type repositorySagaState struct {
	repository *datastore.Repository
	paths      map[repositoryPath]string
	reverse    *repositoryPath
	fail       map[string]error
	calls      []string
}

func newRepositorySagaState(source repositoryPath) *repositorySagaState {
	reverse := source
	return &repositorySagaState{
		repository: &datastore.Repository{
			UID:             sagaTestRepositoryID,
			Namespace:       source.namespace,
			Name:            source.name,
			Generation:      1,
			ResourceVersion: "1",
		},
		paths:   map[repositoryPath]string{source: sagaTestRepositoryID},
		reverse: &reverse,
		fail:    make(map[string]error),
	}
}

func (s *repositorySagaState) operations(injector failureInjector) repositorySagaOperations {
	return repositorySagaOperations{
		injector: injector,
		reserve: func(_ context.Context, path repositoryPath, owner string) error {
			s.calls = append(s.calls, repositorySagaReserveTarget)
			if err := s.takeFailure(repositorySagaReserveTarget); err != nil {
				return err
			}
			if existing, ok := s.paths[path]; ok && existing != owner {
				return datastore.ErrAlreadyExists
			}
			s.paths[path] = owner
			return nil
		},
		release: func(_ context.Context, path repositoryPath, owner string) error {
			if err := s.takeFailure("release_target"); err != nil {
				return err
			}
			if existing, ok := s.paths[path]; ok {
				if existing != owner {
					return datastore.ErrRepairRequired
				}
				delete(s.paths, path)
			}
			return nil
		},
		commit: func(
			_ context.Context,
			_ *datastore.Repository,
			source, target repositoryPath,
			version repositorySagaVersion,
		) (bool, error) {
			s.calls = append(s.calls, repositorySagaUpdateAuthoritative)
			if err := s.takeFailure(repositorySagaUpdateAuthoritative); err != nil {
				return false, err
			}
			if repositoryHasPath(s.repository, target) {
				return true, nil
			}
			if !repositoryHasPath(s.repository, source) {
				return false, datastore.ErrRepairRequired
			}
			s.repository.Namespace = target.namespace
			s.repository.Name = target.name
			if version == repositorySagaSystemVersion {
				datastore.AdvanceRepositorySystemVersion(s.repository)
			} else {
				datastore.AdvanceRepositorySpecVersion(s.repository)
			}
			return true, nil
		},
		updateReverse: func(_ context.Context, source, target repositoryPath, _ string) error {
			s.calls = append(s.calls, repositorySagaUpdateReverse)
			if err := s.takeFailure(repositorySagaUpdateReverse); err != nil {
				return err
			}
			if s.reverse != nil && *s.reverse != source && *s.reverse != target {
				return datastore.ErrRepairRequired
			}
			updated := target
			s.reverse = &updated
			return nil
		},
		cleanup: func(_ context.Context, path repositoryPath, owner string) error {
			s.calls = append(s.calls, repositorySagaCleanupOldPath)
			if err := s.takeFailure(repositorySagaCleanupOldPath); err != nil {
				return err
			}
			if existing, ok := s.paths[path]; ok {
				if existing != owner {
					return datastore.ErrRepairRequired
				}
				delete(s.paths, path)
			}
			return nil
		},
	}
}

func (s *repositorySagaState) takeFailure(step string) error {
	err := s.fail[step]
	delete(s.fail, step)
	return err
}

func (s *repositorySagaState) assertConverged(t *testing.T, target repositoryPath) {
	t.Helper()
	require.Len(t, s.paths, 1)
	assert.Equal(t, sagaTestRepositoryID, s.paths[target])
	require.NotNil(t, s.reverse)
	assert.Equal(t, target, *s.reverse)
	assert.True(t, repositoryHasPath(s.repository, target))
}

func TestRepositoryMappingSagaOrdersAllAwaitedSteps(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "source", name: "renamed"}
	state := newRepositorySagaState(source)

	err := executeRepositoryMappingSaga(
		context.Background(),
		state.operations(nil),
		state.repository,
		source,
		target,
		repositorySagaSpecVersion,
	)

	require.NoError(t, err)
	assert.Equal(t, []string{
		repositorySagaReserveTarget,
		repositorySagaUpdateAuthoritative,
		repositorySagaUpdateReverse,
		repositorySagaCleanupOldPath,
	}, state.calls)
	state.assertConverged(t, target)
}

func TestRepositoryMappingSagaRejectsTargetOwnedByAnotherRepository(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "source", name: "renamed"}
	state := newRepositorySagaState(source)
	state.paths[target] = "22222222-2222-2222-2222-222222222222"

	err := executeRepositoryMappingSaga(
		context.Background(),
		state.operations(nil),
		state.repository,
		source,
		target,
		repositorySagaSpecVersion,
	)

	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
	assert.True(t, repositoryHasPath(state.repository, source))
	assert.Equal(t, "1", state.repository.ResourceVersion)
	assert.Equal(t, "22222222-2222-2222-2222-222222222222", state.paths[target])
}

func TestRepositoryMappingSagaFailureInjectionRetryConverges(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "source", name: "renamed"}
	for _, step := range []string{
		repositorySagaReserveTarget,
		repositorySagaUpdateAuthoritative,
		repositorySagaUpdateReverse,
		repositorySagaCleanupOldPath,
	} {
		for _, point := range []failurePoint{failureBefore, failureAfter} {
			t.Run(step+"_"+string(point), func(t *testing.T) {
				state := newRepositorySagaState(source)
				injector := newTestFailureInjector()
				injector.fail(step, point)

				err := executeRepositoryMappingSaga(
					context.Background(),
					state.operations(injector),
					state.repository,
					source,
					target,
					repositorySagaSpecVersion,
				)
				require.Error(t, err)

				require.NoError(t, executeRepositoryMappingSaga(
					context.Background(),
					state.operations(nil),
					state.repository,
					source,
					target,
					repositorySagaSpecVersion,
				))
				state.assertConverged(t, target)
				assert.Equal(t, int64(2), state.repository.Generation)
				assert.Equal(t, "2", state.repository.ResourceVersion)
			})
		}
	}
}

func TestRepositoryMappingSagaApplyFailuresRetryConverges(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "target", name: "catalog"}
	for _, step := range []string{
		repositorySagaReserveTarget,
		repositorySagaUpdateAuthoritative,
		repositorySagaUpdateReverse,
		repositorySagaCleanupOldPath,
	} {
		t.Run(step, func(t *testing.T) {
			state := newRepositorySagaState(source)
			state.fail[step] = errors.New("injected apply failure")

			err := executeRepositoryMappingSaga(
				context.Background(),
				state.operations(nil),
				state.repository,
				source,
				target,
				repositorySagaSystemVersion,
			)
			require.Error(t, err)

			require.NoError(t, executeRepositoryMappingSaga(
				context.Background(),
				state.operations(nil),
				state.repository,
				source,
				target,
				repositorySagaSystemVersion,
			))
			state.assertConverged(t, target)
			assert.Equal(t, int64(1), state.repository.Generation)
			assert.Equal(t, "2", state.repository.ResourceVersion)
		})
	}
}

func TestRepositoryMappingSagaCompensatesReservationBeforeCommit(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "source", name: "renamed"}
	state := newRepositorySagaState(source)
	state.fail[repositorySagaUpdateAuthoritative] = errors.New("authoritative write failed")

	err := executeRepositoryMappingSaga(
		context.Background(),
		state.operations(nil),
		state.repository,
		source,
		target,
		repositorySagaSpecVersion,
	)

	require.Error(t, err)
	assert.NotContains(t, state.paths, target)
	assert.Contains(t, state.paths, source)
	assert.Equal(t, source, *state.reverse)
	assert.Equal(t, "1", state.repository.ResourceVersion)
}

func TestRepositoryMappingSagaCompensationFailureRequiresRepair(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "source", name: "renamed"}
	state := newRepositorySagaState(source)
	state.fail[repositorySagaUpdateAuthoritative] = errors.New("authoritative write failed")
	state.fail["release_target"] = errors.New("target release failed")

	err := executeRepositoryMappingSaga(
		context.Background(),
		state.operations(nil),
		state.repository,
		source,
		target,
		repositorySagaSpecVersion,
	)

	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	assert.Equal(t, sagaTestRepositoryID, state.paths[target])
	assert.True(t, repositoryHasPath(state.repository, source))
}

func TestRepositoryMappingSagaPostCommitFailureRequiresRollForward(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "source", name: "renamed"}
	state := newRepositorySagaState(source)
	injector := newTestFailureInjector()
	injector.fail(repositorySagaUpdateReverse, failureBefore)

	err := executeRepositoryMappingSaga(
		context.Background(),
		state.operations(injector),
		state.repository,
		source,
		target,
		repositorySagaSpecVersion,
	)

	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	assert.True(t, repositoryHasPath(state.repository, target))
	assert.Equal(t, source, *state.reverse)
	assert.Equal(t, sagaTestRepositoryID, state.paths[source])
	assert.Equal(t, sagaTestRepositoryID, state.paths[target])
}

func TestRepositoryMappingSagaNeverDeletesAnotherOwnersOldPath(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "source", name: "renamed"}
	state := newRepositorySagaState(source)
	state.repository.Namespace = target.namespace
	state.repository.Name = target.name
	state.repository.Generation = 2
	state.repository.ResourceVersion = "2"
	state.paths[target] = sagaTestRepositoryID
	state.paths[source] = "22222222-2222-2222-2222-222222222222"
	reverse := target
	state.reverse = &reverse

	err := executeRepositoryMappingSaga(
		context.Background(),
		state.operations(nil),
		state.repository,
		source,
		target,
		repositorySagaSpecVersion,
	)

	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	assert.Equal(t, "22222222-2222-2222-2222-222222222222", state.paths[source])
	assert.Equal(t, sagaTestRepositoryID, state.paths[target])
}

func TestRepositoryMappingSagaRepeatedRenameAndTransferAreIdempotent(t *testing.T) {
	tests := []struct {
		name               string
		source             repositoryPath
		target             repositoryPath
		version            repositorySagaVersion
		expectedGeneration int64
	}{
		{
			name:               "rename advances spec version once",
			source:             repositoryPath{namespace: "source", name: "catalog"},
			target:             repositoryPath{namespace: "source", name: "renamed"},
			version:            repositorySagaSpecVersion,
			expectedGeneration: 2,
		},
		{
			name:               "transfer advances system version once",
			source:             repositoryPath{namespace: "source", name: "catalog"},
			target:             repositoryPath{namespace: "target", name: "catalog"},
			version:            repositorySagaSystemVersion,
			expectedGeneration: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := newRepositorySagaState(test.source)
			operations := state.operations(nil)

			require.NoError(t, executeRepositoryMappingSaga(
				context.Background(), operations, state.repository, test.source, test.target, test.version,
			))
			require.NoError(t, executeRepositoryMappingSaga(
				context.Background(), operations, state.repository, test.source, test.target, test.version,
			))

			state.assertConverged(t, test.target)
			assert.Equal(t, test.expectedGeneration, state.repository.Generation)
			assert.Equal(t, "2", state.repository.ResourceVersion)
		})
	}
}

func TestRepositoryMappingSagaRemovesStaleOldMappingOwnedByRepository(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "target", name: "catalog"}
	state := newRepositorySagaState(source)
	state.repository.Namespace = target.namespace
	state.repository.Name = target.name
	state.repository.ResourceVersion = "2"
	state.paths[target] = sagaTestRepositoryID
	reverse := target
	state.reverse = &reverse

	require.NoError(t, executeRepositoryMappingSaga(
		context.Background(),
		state.operations(nil),
		state.repository,
		source,
		target,
		repositorySagaSystemVersion,
	))

	state.assertConverged(t, target)
	assert.Equal(t, "2", state.repository.ResourceVersion)
}

func TestRepositoryBucketsForPageIncludesAdjacentFutureBucket(t *testing.T) {
	now := time.Date(2026, time.August, 19, 20, 0, 0, 0, time.UTC)
	cursorTime := time.Date(2026, time.May, 11, 12, 0, 0, 0, time.UTC)
	buckets := repositoryBucketsForPage(datastore.PageParams{
		Last:   100,
		Before: encodeKeysetCursor(cursorTime, sagaTestRepositoryID),
	}, now)

	assert.Equal(t, []string{"2026-05", "2026-06", "2026-07", "2026-08", "2026-09"}, buckets)
}

func TestCollectRepositoryPageContinuesPastDanglingRows(t *testing.T) {
	created := time.Date(2026, time.August, 19, 20, 0, 0, 0, time.UTC)
	danglingOne := "11111111-1111-1111-1111-111111111111"
	danglingTwo := "22222222-2222-2222-2222-222222222222"
	liveOne := "33333333-3333-3333-3333-333333333333"
	liveTwo := "44444444-4444-4444-4444-444444444444"
	calls := 0

	items, err := collectRepositoryPage(
		context.Background(),
		[]string{"2026-08"},
		datastore.PageParams{First: 1},
		func(_ context.Context, bucket string, page datastore.PageParams) ([]repositoryIndexRow, error) {
			assert.Equal(t, "2026-08", bucket)
			calls++
			switch calls {
			case 1:
				assert.Empty(t, page.After)
				return []repositoryIndexRow{
					{Bucket: bucket, CreationTimestamp: created, UID: mustParseUUID(danglingOne)},
					{Bucket: bucket, CreationTimestamp: created.Add(-time.Second), UID: mustParseUUID(danglingTwo)},
				}, nil
			case 2:
				assert.NotEmpty(t, page.After)
				return []repositoryIndexRow{
					{Bucket: bucket, CreationTimestamp: created.Add(-2 * time.Second), UID: mustParseUUID(liveOne)},
					{Bucket: bucket, CreationTimestamp: created.Add(-3 * time.Second), UID: mustParseUUID(liveTwo)},
				}, nil
			default:
				t.Fatalf("unexpected fetch call %d", calls)
				return nil, nil
			}
		},
		func(_ context.Context, uid string) (*datastore.Repository, error) {
			switch uid {
			case danglingOne, danglingTwo:
				return nil, datastore.ErrNotFound
			default:
				return &datastore.Repository{UID: uid}, nil
			}
		},
	)

	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, liveOne, items[0].UID)
	assert.Equal(t, liveTwo, items[1].UID)
	assert.Equal(t, 2, calls)
}
