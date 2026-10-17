// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"testing/synctest"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/scylladb/gocqlx/v3/migrate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type fileCASPhaseStore struct {
	datastore.Datastore
}

func (*fileCASPhaseStore) WithCatalogMutationBatch(ctx context.Context, _ string, operation func(context.Context) error) error {
	return operation(ctx)
}

func filePhaseObservation(t *testing.T, registry *prometheus.Registry, operation string) (uint64, float64, float64) {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	var count uint64
	var elapsed, failures float64
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() != "operation" || label.GetValue() != operation {
					continue
				}
				switch family.GetName() {
				case "gitstore_datastore_operation_duration_seconds":
					count += metric.GetHistogram().GetSampleCount()
					elapsed += metric.GetHistogram().GetSampleSum()
				case "gitstore_datastore_operation_errors_total":
					failures += metric.GetCounter().GetValue()
				}
			}
		}
	}
	return count, elapsed, failures
}

func TestFileProjectionReaderSeparatesQueueFromPhysicalReadAndCanceledKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := prometheus.NewRegistry()
		wrapped := datastore.NewInstrumentedDatastoreWithRegistry(&fileCASPhaseStore{}, "scylla", zap.NewNop(), registry)
		require.NoError(t, wrapped.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(t.Context(), "private-namespace", func(ctx context.Context) error {
			started, release := make(chan struct{}), make(chan struct{})
			var unblocked sync.Once
			unblock := func() { unblocked.Do(func() { close(release) }) }
			var calls int
			failure := errors.New("physical read failed")
			reader := newFileProjectionReader(func(_ context.Context, keys []fileProjectionKey) ([]*ProjectionRecord, error) {
				calls++
				if calls == 1 {
					require.Len(t, keys, 1)
					close(started)
					<-release
					return []*ProjectionRecord{{UID: keys[0].uid.String()}}, nil
				}
				require.Len(t, keys, 2)
				for _, key := range keys {
					require.NotEqual(t, byte(2), key.uid[15])
				}
				time.Sleep(5 * time.Millisecond)
				return nil, failure
			})
			defer func() { unblock(); reader.close() }()
			results := make([]chan error, 4)
			canceled, cancel := context.WithCancel(ctx)
			defer cancel()
			for i := range results {
				results[i] = make(chan error, 1)
				caller := ctx
				if i == 1 {
					caller = canceled
				}
				go func() {
					_, err := reader.submit(caller, fileProjectionKey{uid: gocql.UUID{15: byte(i + 1)}})
					results[i] <- err
				}()
				if i == 0 {
					<-started
				}
			}
			synctest.Wait()
			count, elapsed, failures := filePhaseObservation(t, registry, "FileProjectionReadQueue")
			require.EqualValues(t, 1, count, "queue observation must finish before physical work completes")
			require.Zero(t, elapsed)
			require.Zero(t, failures)
			count, _, _ = filePhaseObservation(t, registry, "FileProjectionReadBatch1")
			require.Zero(t, count, "physical observations must wait for the query result")
			time.Sleep(7 * time.Millisecond)
			cancel()
			synctest.Wait()
			time.Sleep(4 * time.Millisecond)
			unblock()
			require.NoError(t, <-results[0])
			require.ErrorIs(t, <-results[1], context.Canceled)
			for _, result := range results[2:] {
				require.ErrorIs(t, <-result, failure)
			}
			reader.close()
			count, elapsed, failures = filePhaseObservation(t, registry, "FileProjectionReadQueue")
			require.EqualValues(t, 4, count)
			require.InDelta(t, .033, elapsed, 1e-9, "three queued callers wait eleven milliseconds each")
			require.EqualValues(t, 1, failures, "only the canceled queued caller has a queue error")
			count, elapsed, failures = filePhaseObservation(t, registry, "FileProjectionReadBatch1")
			require.EqualValues(t, 1, count)
			require.InDelta(t, .011, elapsed, 1e-9)
			require.Zero(t, failures)
			count, elapsed, failures = filePhaseObservation(t, registry, "FileProjectionReadBatch2")
			require.EqualValues(t, 1, count, "two live keys share one physical read and one failure observation")
			require.InDelta(t, .005, elapsed, 1e-9)
			require.EqualValues(t, 1, failures)
			return nil
		}))
	})
}

func TestFileProjectionReaderBatchesOnlyReadyRequestsAndNeverCaches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		var calls [][]fileProjectionKey
		reader := newFileProjectionReader(func(ctx context.Context, keys []fileProjectionKey) ([]*ProjectionRecord, error) {
			calls = append(calls, slices.Clone(keys))
			if len(calls) == 1 {
				close(started)
				<-release
			}
			results := make([]*ProjectionRecord, len(keys))
			for i, key := range keys {
				results[i] = &ProjectionRecord{UID: key.uid.String(), Name: fmt.Sprint(len(calls))}
			}
			return results, nil
		})
		defer reader.close()
		results := make([]chan fileProjectionResult, 13)
		for i := range results {
			results[i] = make(chan fileProjectionResult, 1)
			go func() {
				record, err := reader.submit(t.Context(), fileProjectionKey{uid: gocql.UUID{15: byte(i + 1)}})
				results[i] <- fileProjectionResult{record: record, err: err}
			}()
			if i == 0 {
				<-started
			}
		}
		synctest.Wait()
		require.Len(t, reader.requests, 12)
		require.Len(t, calls, 1, "one request must execute without an arrival barrier")
		close(release)
		for i, result := range results {
			got := <-result
			require.NoError(t, got.err)
			require.Equal(t, (gocql.UUID{15: byte(i + 1)}).String(), got.record.UID)
		}
		require.Len(t, calls, 2)
		require.Len(t, calls[1], 12)
		current, err := reader.submit(t.Context(), calls[0][0])
		require.NoError(t, err)
		require.Equal(t, "3", current.Name, "repeated keys require a new physical read, not cached cohort facts")
		reader.close()
		_, err = reader.submit(t.Context(), calls[0][0])
		require.ErrorContains(t, err, "closed")
	})
}

func TestFileProjectionReaderCancellationAndFailures(t *testing.T) {
	for _, allCanceled := range []bool{false, true} {
		t.Run(fmt.Sprint("allCanceled=", allCanceled), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				firstStarted, releaseFirst := make(chan struct{}), make(chan struct{})
				batchStarted, releaseBatch := make(chan struct{}), make(chan struct{})
				var calls int
				canceled, cancel := context.WithCancel(t.Context())
				defer cancel()
				peer, cancelPeer := context.WithCancel(t.Context())
				defer cancelPeer()
				reader := newFileProjectionReader(func(ctx context.Context, keys []fileProjectionKey) ([]*ProjectionRecord, error) {
					calls++
					if calls == 1 {
						close(firstStarted)
						<-releaseFirst
						return []*ProjectionRecord{{UID: keys[0].uid.String()}}, nil
					}
					close(batchStarted)
					select {
					case <-ctx.Done():
						require.True(t, allCanceled)
						return nil, ctx.Err()
					case <-releaseBatch:
						require.NoError(t, ctx.Err(), "canceling one reader must not cancel its live peer")
						return []*ProjectionRecord{{UID: keys[0].uid.String()}, {UID: keys[1].uid.String()}}, nil
					}
				})
				defer reader.close()
				results := make([]chan error, 3)
				for i, ctx := range []context.Context{t.Context(), canceled, peer} {
					results[i] = make(chan error, 1)
					go func() {
						_, err := reader.submit(ctx, fileProjectionKey{uid: gocql.UUID{15: byte(i + 1)}})
						results[i] <- err
					}()
					if i == 0 {
						<-firstStarted
					}
				}
				synctest.Wait()
				close(releaseFirst)
				require.NoError(t, <-results[0])
				<-batchStarted
				cancel()
				synctest.Wait()
				select {
				case <-results[1]:
					t.Fatal("accepted reads must join physical work before returning")
				default:
				}
				if allCanceled {
					cancelPeer()
				} else {
					close(releaseBatch)
				}
				require.ErrorIs(t, <-results[1], context.Canceled)
				if allCanceled {
					require.ErrorIs(t, <-results[2], context.Canceled)
				} else {
					require.NoError(t, <-results[2])
				}
			})
		})
	}
	for _, tc := range []struct {
		name    string
		records []*ProjectionRecord
		err     error
		want    error
	}{
		{"missing", []*ProjectionRecord{nil}, nil, datastore.ErrNotFound},
		{"storage error", nil, errors.New("physical read failed"), nil},
		{"invalid result size", nil, nil, datastore.ErrRepairRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := newFileProjectionReader(func(context.Context, []fileProjectionKey) ([]*ProjectionRecord, error) {
				return tc.records, tc.err
			})
			defer reader.close()
			_, err := reader.submit(t.Context(), fileProjectionKey{})
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			} else {
				require.ErrorIs(t, err, tc.want)
			}
		})
	}
}

func TestFileProjectionReaderBoundsCloseAndQueuedCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		failure := errors.New("shared projection read failed")
		var sizes []int
		reader := newFileProjectionReader(func(ctx context.Context, keys []fileProjectionKey) ([]*ProjectionRecord, error) {
			sizes = append(sizes, len(keys))
			if len(sizes) == 1 {
				close(started)
				<-release
				return []*ProjectionRecord{{UID: keys[0].uid.String()}}, nil
			}
			for _, key := range keys {
				require.NotEqual(t, byte(2), key.uid[15], "canceled queued reads must not issue queries")
			}
			return nil, failure
		})
		results := make([]chan error, 17)
		canceled, cancel := context.WithCancel(t.Context())
		defer cancel()
		for i := range results {
			results[i] = make(chan error, 1)
			ctx := t.Context()
			if i == 1 {
				ctx = canceled
			}
			go func() {
				_, err := reader.submit(ctx, fileProjectionKey{uid: gocql.UUID{15: byte(i + 1)}})
				results[i] <- err
			}()
			if i == 0 {
				<-started
			}
		}
		synctest.Wait()
		require.Len(t, reader.requests, 16)
		_, err := reader.submit(t.Context(), fileProjectionKey{})
		require.ErrorContains(t, err, "queue exhausted")
		cancel()
		closed := make(chan struct{})
		go func() { reader.close(); close(closed) }()
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("close must wait for accepted physical work")
		default:
		}
		close(release)
		require.NoError(t, <-results[0])
		require.ErrorIs(t, <-results[1], context.Canceled)
		for _, result := range results[2:] {
			require.ErrorIs(t, <-result, failure)
		}
		<-closed
		require.Equal(t, []int{1, 15}, sizes)
		reader.close()
	})
}

func TestFileCASWriterCoalescesRunningPreparationsWithoutTimer(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(fmt.Sprintf("prepared-%t", prepared), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const count = 12
				var singles, batches atomic.Int64
				writer := newFileCASWriter(func(*fileCASRequest) error {
					singles.Add(1)
					time.Sleep(20 * time.Millisecond)
					return nil
				}, func(_ context.Context, group []*fileCASRequest) (bool, error) {
					batches.Add(1)
					expected := count - 1
					if prepared {
						expected = count
					}
					assert.Len(t, group, expected)
					time.Sleep(20 * time.Millisecond)
					return true, nil
				})
				contexts := make([]context.Context, count)
				finishes := make([]func(), 0, count)
				t.Cleanup(func() {
					for _, finish := range finishes {
						finish()
					}
					writer.close()
				})
				for i := range contexts {
					contexts[i] = t.Context()
					if prepared {
						ctx, finish, err := writer.prepare(contexts[i])
						require.NoError(t, err)
						contexts[i] = ctx
						finishes = append(finishes, finish)
					}
				}
				done := make(chan error, count)
				started := time.Now()
				go func() { done <- writer.submit(contexts[0], gocql.UUID{15: 1}, nil) }()
				synctest.Wait()
				if prepared {
					require.Zero(t, singles.Load(), "already-running preparation must join before the first physical CAS")
				} else {
					require.EqualValues(t, 1, singles.Load())
				}
				time.Sleep(7 * time.Millisecond)
				for i := 1; i < count; i++ {
					go func() { done <- writer.submit(contexts[i], gocql.UUID{15: byte(i + 1)}, nil) }()
				}
				for range count {
					require.NoError(t, <-done)
				}
				require.EqualValues(t, 1, batches.Load())
				if prepared {
					require.Zero(t, singles.Load())
					require.Equal(t, 27*time.Millisecond, time.Since(started), "only preparation plus one physical exchange, without a batching timer")
				} else {
					require.EqualValues(t, 1, singles.Load())
					require.Equal(t, 40*time.Millisecond, time.Since(started), "the initial single serializes a second physical exchange")
				}
			})
		})
	}
}

func TestFileCASWriterPreparationPreservesSoloLatencyAndPayloadBound(t *testing.T) {
	for _, count := range []int{1, 8} {
		t.Run(fmt.Sprintf("rows-%d", count), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var singles, batches atomic.Int64
				writer := newFileCASWriter(func(*fileCASRequest) error {
					singles.Add(1)
					return nil
				}, func(_ context.Context, group []*fileCASRequest) (bool, error) {
					batches.Add(1)
					assert.Len(t, group, 4, "large values retain the existing four-row fallback")
					return true, nil
				})
				contexts, finishes := make([]context.Context, count), make([]func(), 0, count)
				t.Cleanup(func() {
					for _, finish := range finishes {
						finish()
					}
					writer.close()
				})
				for i := range contexts {
					ctx, finish, err := writer.prepare(t.Context())
					require.NoError(t, err)
					contexts[i] = ctx
					finishes = append(finishes, finish)
				}
				done := make(chan error, count)
				values := []any{strings.Repeat("x", fileCASBatchPayloadLimit)}
				started := time.Now()
				for i := range contexts {
					go func() { done <- writer.submit(contexts[i], gocql.UUID{15: byte(i + 1)}, values) }()
				}
				for range contexts {
					require.NoError(t, <-done)
				}
				if count == 1 {
					require.EqualValues(t, 1, singles.Load())
					require.Zero(t, batches.Load())
				} else {
					require.Zero(t, singles.Load())
					require.EqualValues(t, 2, batches.Load())
				}
				require.Zero(t, time.Since(started), "ready writes never wait for a batching deadline")
			})
		})
	}
}

func TestFileCASWriterPreparationCompletionAndCloseUnblockAcceptedRows(t *testing.T) {
	for _, closing := range []bool{false, true} {
		t.Run(fmt.Sprintf("closing-%t", closing), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var singles atomic.Int64
				writer := newFileCASWriter(func(*fileCASRequest) error {
					singles.Add(1)
					return nil
				}, func(context.Context, []*fileCASRequest) (bool, error) {
					return false, errors.New("unexpected batch")
				})
				ctx, finish, err := writer.prepare(t.Context())
				require.NoError(t, err)
				_, abandon, err := writer.prepare(t.Context())
				require.NoError(t, err)
				t.Cleanup(func() { finish(); abandon(); writer.close() })
				done := make(chan error, 1)
				go func() { done <- writer.submit(ctx, gocql.UUID{15: 1}, nil) }()
				synctest.Wait()
				require.Zero(t, singles.Load())
				if closing {
					writer.close()
				} else {
					abandon()
					abandon()
				}
				require.NoError(t, <-done)
				require.EqualValues(t, 1, singles.Load())
				finish()
				abandon()
				writer.mu.Lock()
				remaining := writer.preparing
				writer.mu.Unlock()
				require.Zero(t, remaining, "completion and deferred cleanup must retire each producer exactly once")
				writer.close()
				_, _, err = writer.prepare(t.Context())
				require.ErrorContains(t, err, "closed")
			})
		})
	}
}

func TestFileCASWriterPreparationCancellationDoesNotWaitForPeer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var singles atomic.Int64
		writer := newFileCASWriter(func(*fileCASRequest) error {
			singles.Add(1)
			return nil
		}, func(context.Context, []*fileCASRequest) (bool, error) {
			return false, errors.New("unexpected batch")
		})
		canceled, cancel := context.WithCancel(t.Context())
		defer cancel()
		ctx, finish, err := writer.prepare(canceled)
		require.NoError(t, err)
		_, peer, err := writer.prepare(t.Context())
		require.NoError(t, err)
		t.Cleanup(func() { finish(); peer(); writer.close() })
		done := make(chan error, 1)
		go func() { done <- writer.submit(ctx, gocql.UUID{15: 1}, nil) }()
		synctest.Wait()
		require.Zero(t, singles.Load())
		cancel()
		require.ErrorIs(t, <-done, context.Canceled, "a canceled accepted row must drain while another producer is still preparing")
		require.Zero(t, singles.Load(), "cancellation during preparation must not execute a physical write")
		_, _, err = writer.prepare(canceled)
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestFileCASWriterPreparationCannotExceedBatchBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var batches atomic.Int64
		writer := newFileCASWriter(func(*fileCASRequest) error {
			return errors.New("unexpected single")
		}, func(_ context.Context, group []*fileCASRequest) (bool, error) {
			batches.Add(1)
			assert.Len(t, group, 16)
			return true, nil
		})
		contexts, finishes := make([]context.Context, 17), make([]func(), 0, 17)
		t.Cleanup(func() {
			for _, finish := range finishes {
				finish()
			}
			writer.close()
		})
		for i := range contexts {
			ctx, finish, err := writer.prepare(t.Context())
			require.NoError(t, err)
			contexts[i] = ctx
			finishes = append(finishes, finish)
		}
		done := make(chan error, 16)
		for i := range 16 {
			go func() { done <- writer.submit(contexts[i], gocql.UUID{15: byte(i + 1)}, nil) }()
		}
		for range 16 {
			require.NoError(t, <-done)
		}
		require.EqualValues(t, 1, batches.Load(), "a full batch must execute without waiting for additional producers")
	})
}

func TestFileCASWriterPreparationKeepsDuplicateUIDsIndependent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var singles, batches atomic.Int64
		writer := newFileCASWriter(func(*fileCASRequest) error {
			singles.Add(1)
			return nil
		}, func(_ context.Context, group []*fileCASRequest) (bool, error) {
			batches.Add(1)
			assert.Len(t, group, 2)
			assert.NotEqual(t, group[0].uid, group[1].uid)
			return true, nil
		})
		contexts, finishes := make([]context.Context, 3), make([]func(), 0, 3)
		t.Cleanup(func() {
			for _, finish := range finishes {
				finish()
			}
			writer.close()
		})
		for i := range contexts {
			ctx, finish, err := writer.prepare(t.Context())
			require.NoError(t, err)
			contexts[i] = ctx
			finishes = append(finishes, finish)
		}
		done := make(chan error, 3)
		go func() { done <- writer.submit(contexts[0], gocql.UUID{15: 1}, nil) }()
		go func() { done <- writer.submit(contexts[1], gocql.UUID{15: 1}, nil) }()
		synctest.Wait()
		require.EqualValues(t, 1, singles.Load(), "a repeated UID must force an independent condition, not wait behind a batch it cannot join")
		require.NoError(t, <-done)
		go func() { done <- writer.submit(contexts[2], gocql.UUID{15: 2}, nil) }()
		for range 2 {
			require.NoError(t, <-done)
		}
		require.EqualValues(t, 1, batches.Load())
	})
}

func TestFileCASWriterReportsAcceptedQueueUntilExecutionOrCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := prometheus.NewRegistry()
		wrapped := datastore.NewInstrumentedDatastoreWithRegistry(&fileCASPhaseStore{}, "scylla", zap.NewNop(), registry)
		observation := func() (uint64, float64, float64) {
			return filePhaseObservation(t, registry, "FileConditionalQueue")
		}
		require.NoError(t, wrapped.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(t.Context(), "private-namespace", func(ctx context.Context) error {
			started, release := make(chan struct{}), make(chan struct{})
			var unblocked sync.Once
			unblock := func() { unblocked.Do(func() { close(release) }) }
			var executions atomic.Int64
			writer := newFileCASWriter(func(request *fileCASRequest) error {
				executions.Add(1)
				if request.uid[15] == 1 {
					close(started)
					<-release
				}
				return nil
			}, func(context.Context, []*fileCASRequest) (bool, error) {
				t.Error("only one live queued follower should remain")
				return false, errors.New("unexpected batch")
			})
			defer func() { unblock(); writer.close() }()
			done := []chan error{make(chan error, 1), make(chan error, 1), make(chan error, 1)}
			go func() { done[0] <- writer.submit(ctx, gocql.UUID{15: 1}, nil) }()
			<-started
			canceled, cancel := context.WithCancel(ctx)
			defer cancel()
			go func() { done[1] <- writer.submit(canceled, gocql.UUID{15: 2}, nil) }()
			go func() { done[2] <- writer.submit(ctx, gocql.UUID{15: 3}, nil) }()
			synctest.Wait()
			require.Len(t, writer.requests, 2)
			count, elapsed, failures := observation()
			require.EqualValues(t, 1, count, "a dequeued request must be observed before its physical write finishes")
			require.Zero(t, elapsed)
			require.Zero(t, failures)
			time.Sleep(7 * time.Millisecond)
			cancel()
			synctest.Wait()
			for _, result := range done {
				select {
				case <-result:
					t.Fatal("accepted writes must drain before their reservation can be released")
				default:
				}
			}
			time.Sleep(4 * time.Millisecond)
			unblock()
			require.NoError(t, <-done[0])
			require.ErrorIs(t, <-done[1], context.Canceled)
			require.NoError(t, <-done[2])
			writer.close()
			count, elapsed, failures = observation()
			require.EqualValues(t, 3, count, "every accepted request must finish its queue phase exactly once")
			require.InDelta(t, .022, elapsed, 1e-9, "queued time must exclude physical work but retain canceled drain time")
			require.EqualValues(t, 1, failures)
			require.EqualValues(t, 2, executions.Load(), "a canceled queued request must not execute")
			return nil
		}))
	})
}

func TestFileCASWriterBatchesReadyRowsAndPreservesIndependentConflicts(t *testing.T) {
	ambiguous := errors.New("conditional batch outcome unknown")
	for _, tc := range []struct {
		name    string
		applied bool
		err     error
		singles int64
	}{
		{"applied", true, nil, 1},
		{"definite conflict", false, nil, 4},
		{"ambiguous failure", false, ambiguous, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var singles, batches atomic.Int64
			writer := newFileCASWriter(func(request *fileCASRequest) error {
				singles.Add(1)
				if request.uid[15] == 1 {
					close(started)
					<-release
				}
				if request.uid[15] == 3 {
					return datastore.ErrConflict
				}
				return nil
			}, func(ctx context.Context, group []*fileCASRequest) (bool, error) {
				batches.Add(1)
				assert.NoError(t, ctx.Err())
				assert.Len(t, group, 3)
				for i, request := range group {
					assert.Equal(t, byte(i+2), request.uid[15])
				}
				return tc.applied, tc.err
			})
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
				writer.close()
			})
			done := make([]chan error, 4)
			for i := range done {
				done[i] = make(chan error, 1)
			}
			go func() { done[0] <- writer.submit(t.Context(), gocql.UUID{15: 1}, nil) }()
			<-started
			for i := 1; i < len(done); i++ {
				go func() { done[i] <- writer.submit(t.Context(), gocql.UUID{15: byte(i + 1)}, nil) }()
				require.Eventually(t, func() bool { return len(writer.requests) == i }, time.Second, time.Millisecond)
			}
			close(release)
			for i, result := range done {
				err := <-result
				switch {
				case i == 0:
					require.NoError(t, err)
				case tc.err != nil:
					require.ErrorIs(t, err, tc.err)
				case !tc.applied && i == 2:
					require.ErrorIs(t, err, datastore.ErrConflict)
				default:
					require.NoError(t, err)
				}
			}
			writer.close()
			require.Equal(t, tc.singles, singles.Load())
			require.EqualValues(t, 1, batches.Load())
			require.ErrorContains(t, writer.submit(t.Context(), gocql.UUID{15: 5}, nil), "closed")
		})
	}
}

func TestFileCASWriterNeverBatchesRepeatedUIDs(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	writer := newFileCASWriter(func(request *fileCASRequest) error {
		if request.uid[15] == 1 {
			close(started)
			<-release
		}
		return nil
	}, func(_ context.Context, group []*fileCASRequest) (bool, error) {
		seen := make(map[gocql.UUID]bool)
		for _, request := range group {
			assert.False(t, seen[request.uid], "one row cannot have conflicting conditions inside a batch")
			seen[request.uid] = true
		}
		assert.LessOrEqual(t, len(group), 4)
		return true, nil
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		writer.close()
	})
	done := make(chan error, 5)
	go func() { done <- writer.submit(t.Context(), gocql.UUID{15: 1}, nil) }()
	<-started
	for i, id := range []byte{2, 2, 3, 4} {
		go func() { done <- writer.submit(t.Context(), gocql.UUID{15: id}, nil) }()
		require.Eventually(t, func() bool { return len(writer.requests) == i+1 }, time.Second, time.Millisecond)
	}
	close(release)
	for range 5 {
		require.NoError(t, <-done)
	}
	writer.close()
}

func TestFileCASWriterRetainsBoundAndDrainsDuringClose(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	writer := newFileCASWriter(func(request *fileCASRequest) error {
		if request.uid[15] == 1 {
			close(started)
			<-release
		}
		return nil
	}, func(_ context.Context, group []*fileCASRequest) (bool, error) {
		assert.Len(t, group, 16, "already accepted small rows should share one physical CAS")
		return true, nil
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		writer.close()
	})
	done := make(chan error, 17)
	go func() { done <- writer.submit(t.Context(), gocql.UUID{15: 1}, nil) }()
	<-started
	for i := range 16 {
		go func() { done <- writer.submit(t.Context(), gocql.UUID{15: byte(i + 2)}, nil) }()
	}
	require.Eventually(t, func() bool { return len(writer.requests) == 16 }, time.Second, time.Millisecond)
	require.ErrorContains(t, writer.submit(t.Context(), gocql.UUID{15: 20}, nil), "queue exhausted")
	closed := make(chan struct{})
	go func() { writer.close(); close(closed) }()
	require.Eventually(t, func() bool {
		writer.mu.Lock()
		defer writer.mu.Unlock()
		return writer.closed
	}, time.Second, time.Millisecond)
	select {
	case <-closed:
		t.Fatal("writer closed before its accepted physical writes drained")
	default:
	}
	close(release)
	for range 17 {
		require.NoError(t, <-done)
	}
	<-closed
}

func TestFileCASWriterKeepsLargeValuesInFourRowBatches(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var batches atomic.Int64
	writer := newFileCASWriter(func(request *fileCASRequest) error {
		if request.uid[15] == 1 {
			close(started)
			<-release
		}
		return nil
	}, func(_ context.Context, group []*fileCASRequest) (bool, error) {
		batches.Add(1)
		assert.Len(t, group, 4)
		return true, nil
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		writer.close()
	})
	done := make(chan error, 17)
	go func() { done <- writer.submit(t.Context(), gocql.UUID{15: 1}, nil) }()
	<-started
	values := []any{strings.Repeat("x", fileCASBatchPayloadLimit)}
	for i := range 16 {
		go func() { done <- writer.submit(t.Context(), gocql.UUID{15: byte(i + 2)}, values) }()
	}
	require.Eventually(t, func() bool { return len(writer.requests) == 16 }, time.Second, time.Millisecond)
	close(release)
	for range 17 {
		require.NoError(t, <-done)
	}
	writer.close()
	require.EqualValues(t, 4, batches.Load())
}

func TestFileCASWriterBatchCancellationKeepsLivePeers(t *testing.T) {
	for _, cancelAll := range []bool{false, true} {
		t.Run(fmt.Sprintf("all-%t", cancelAll), func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			firstCtx, cancelFirst := context.WithCancel(t.Context())
			peerCtx, cancelPeer := context.WithCancel(t.Context())
			defer cancelFirst()
			defer cancelPeer()
			writer := newFileCASWriter(func(*fileCASRequest) error {
				close(started)
				<-release
				return nil
			}, func(ctx context.Context, group []*fileCASRequest) (bool, error) {
				assert.Len(t, group, 2)
				cancelFirst()
				if cancelAll {
					cancelPeer()
					select {
					case <-ctx.Done():
					case <-time.After(time.Second):
						return false, errors.New("all-canceled conditional batch remained active")
					}
					return false, ctx.Err()
				}
				assert.NoError(t, ctx.Err(), "one caller cannot cancel a live peer's physical batch")
				return true, nil
			})
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
				writer.close()
			})
			done := make([]chan error, 3)
			for i := range done {
				done[i] = make(chan error, 1)
			}
			go func() { done[0] <- writer.submit(t.Context(), gocql.UUID{15: 1}, nil) }()
			<-started
			go func() { done[1] <- writer.submit(firstCtx, gocql.UUID{15: 2}, nil) }()
			require.Eventually(t, func() bool { return len(writer.requests) == 1 }, time.Second, time.Millisecond)
			go func() { done[2] <- writer.submit(peerCtx, gocql.UUID{15: 3}, nil) }()
			require.Eventually(t, func() bool { return len(writer.requests) == 2 }, time.Second, time.Millisecond)
			close(release)
			require.NoError(t, <-done[0])
			for _, result := range done[1:] {
				if cancelAll {
					require.ErrorIs(t, <-result, context.Canceled)
				} else {
					require.NoError(t, <-result)
				}
			}
			writer.close()
		})
	}
}

func TestFileCASWriterSkipsCanceledQueuedRows(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var writes atomic.Int64
	writer := newFileCASWriter(func(request *fileCASRequest) error {
		writes.Add(1)
		assert.NotEqual(t, byte(2), request.uid[15], "an already-canceled queued row must not reach physical execution")
		if request.uid[15] == 1 {
			close(started)
			<-release
		}
		return nil
	}, func(context.Context, []*fileCASRequest) (bool, error) {
		t.Error("one live queued peer must use its independent single CAS")
		return false, errors.New("unexpected physical batch")
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		writer.close()
	})
	done := make([]chan error, 3)
	for i := range done {
		done[i] = make(chan error, 1)
	}
	go func() { done[0] <- writer.submit(t.Context(), gocql.UUID{15: 1}, nil) }()
	<-started
	canceled, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { done[1] <- writer.submit(canceled, gocql.UUID{15: 2}, nil) }()
	require.Eventually(t, func() bool { return len(writer.requests) == 1 }, time.Second, time.Millisecond)
	go func() { done[2] <- writer.submit(t.Context(), gocql.UUID{15: 3}, nil) }()
	require.Eventually(t, func() bool { return len(writer.requests) == 2 }, time.Second, time.Millisecond)
	cancel()
	close(release)
	require.NoError(t, <-done[0])
	require.ErrorIs(t, <-done[1], context.Canceled)
	require.NoError(t, <-done[2])
	writer.close()
	require.EqualValues(t, 2, writes.Load())
}

func TestFileUpdateBindingsPreserveChangesAndNormalization(t *testing.T) {
	now := time.Now().UTC()
	deleted := now.Add(time.Hour)
	previous := toFileRow(&datastore.File{
		UID: uuid.NewString(), Namespace: "namespace", Name: "file",
		APIVersion: "v1beta1", Kind: "File", Generation: 1, ResourceVersion: "1",
		CreationTimestamp: now, Revision: "revision", CreationActor: "creator",
		UpdateTimestamp: now, UpdateActor: "updater", Labels: map[string]string{"key": "value"},
		Annotations: map[string]string{"key": "value"}, OwnerReferences: json.RawMessage(`[]`),
		Finalizers: []string{"finalizer"}, DeletionTimestamp: &deleted, RepositoryID: uuid.NewString(),
		SourcePath: "path", GitCommitSHA: "commit", GitRef: "ref", Spec: json.RawMessage(`{}`),
		Body: "body", Status: json.RawMessage(`{}`),
	})
	same := *previous
	same.Labels = map[string]string{"key": "value"}
	same.Annotations = map[string]string{"key": "value"}
	same.Finalizers = []string{"finalizer"}
	sameDeleted := deleted
	same.DeletionTimestamp = &sameDeleted
	sameRepository := *previous.RepositoryID
	same.RepositoryID = &sameRepository
	values := fileUpdateValues(&same, previous)
	require.Len(t, values, 21)
	for i, value := range values {
		switch i {
		case 4:
			require.Equal(t, same.ResourceVersion, value)
		default:
			require.Equal(t, gocql.UnsetValue, value, "unchanged column %d", i)
		}
	}
	cleared := &fileRow{Namespace: previous.Namespace, UID: previous.UID, CreationTimestamp: previous.CreationTimestamp}
	values = fileUpdateValues(cleared, previous)
	for i, value := range values {
		require.NotEqual(t, gocql.UnsetValue, value, "changed/cleared column %d", i)
	}
	require.Equal(t, "", values[0])
	require.EqualValues(t, 0, values[3])
	require.Equal(t, map[string]string(nil), values[9])
	require.Equal(t, []string(nil), values[12])
	require.Equal(t, (*time.Time)(nil), values[13])
	require.Equal(t, (*gocql.UUID)(nil), values[14])
	require.Equal(t, "", values[19])
	empty := fileUpdateValues(cleared, cleared)
	for _, i := range []int{4, 11, 18, 20} {
		require.Equal(t, "", empty[i], "logical absence must retain explicit normalization for column %d", i)
	}
	require.Equal(t, "value", previous.Labels["key"], "binding preparation must not mutate snapshots")
	require.Equal(t, []string{"finalizer"}, previous.Finalizers)
}

func TestMigrationHistoryRequiresEveryCompletedUnchangedFile(t *testing.T) {
	data := []byte("-- comment;\nCREATE TABLE test (id text PRIMARY KEY);\n")
	bundle := fstest.MapFS{"001.cql": &fstest.MapFile{Data: data}}
	valid := migrate.Info{Name: "001.cql", Checksum: fmt.Sprintf("%x", md5.Sum(data)), Done: 2, EndTime: time.Now()}
	require.NoError(t, validateMigrationHistory([]migrate.Info{valid}, bundle))
	for name, history := range map[string][]migrate.Info{
		"missing":    nil,
		"ahead":      {valid, valid},
		"renamed":    {{Name: "old.cql", Checksum: valid.Checksum, Done: 2, EndTime: valid.EndTime}},
		"modified":   {{Name: valid.Name, Checksum: "different", Done: 2, EndTime: valid.EndTime}},
		"partial":    {{Name: valid.Name, Checksum: valid.Checksum, Done: 1, EndTime: valid.EndTime}},
		"unfinished": {{Name: valid.Name, Checksum: valid.Checksum, Done: 2}},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateMigrationHistory(history, bundle))
		})
	}
	data = []byte("CREATE TABLE test (id text PRIMARY KEY)")
	bundle["001.cql"] = &fstest.MapFile{Data: data}
	valid.Checksum, valid.Done = fmt.Sprintf("%x", md5.Sum(data)), 1
	require.NoError(t, validateMigrationHistory([]migrate.Info{valid}, bundle), "match the library's optional final semicolon")
}

func TestCatalogueRowsPreserveCanonicalEnvelope(t *testing.T) {
	now := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	deletedAt := now.Add(time.Hour)
	uid := uuid.New().String()
	repositoryID := uuid.New().String()
	owners := json.RawMessage(`[{"uid":"owner"}]`)
	spec := json.RawMessage(`{"field":"value"}`)
	status := json.RawMessage(`{"observedGeneration":4}`)

	product := &datastore.Product{
		UID: uid, Namespace: "shop", Name: "product", APIVersion: "catalog/v1", Kind: "Product",
		Generation: 4, ResourceVersion: "7", Revision: "main@sha", CreationTimestamp: now,
		CreationActor: "creator", UpdateTimestamp: now.Add(time.Minute), UpdateActor: "updater",
		Labels: map[string]string{"a": "b"}, Annotations: map[string]string{"c": "d"},
		OwnerReferences: owners, Finalizers: []string{"finalizer"}, DeletionTimestamp: &deletedAt,
		RepositoryID: repositoryID, SourcePath: "products/product.md", GitCommitSHA: "sha", GitRef: "refs/heads/main",
		Spec: spec, Body: "body", Status: status,
	}
	productRow := toProductRow(product)
	assert.Equal(t, product.CreationActor, productRow.CreationActor)
	assert.Equal(t, string(owners), productRow.OwnerReferences)
	assert.Equal(t, product.Finalizers, productRow.Finalizers)
	assert.Equal(t, &deletedAt, productRow.DeletionTimestamp)
	require.NotNil(t, productRow.RepositoryID)
	assert.Equal(t, repositoryID, productRow.RepositoryID.String())
	assert.Equal(t, product, fromProductRow(productRow))

	category := &datastore.CategoryTaxonomy{
		UID: uid, Namespace: "shop", Name: "category", APIVersion: "catalog/v1", Kind: "CategoryTaxonomy",
		Generation: 4, ResourceVersion: "7", Revision: "main@sha", CreationTimestamp: now,
		CreationActor: "creator", UpdateTimestamp: now.Add(time.Minute), UpdateActor: "updater",
		Labels: product.Labels, Annotations: product.Annotations, OwnerReferences: owners,
		Finalizers: product.Finalizers, DeletionTimestamp: &deletedAt, ParentName: "parent", AncestorPath: "parent/category",
		RepositoryID: repositoryID, SourcePath: "categories/category.md", GitCommitSHA: "sha", GitRef: "refs/heads/main",
		Spec: spec, Body: "body", Status: status,
	}
	categoryRow := toCategoryTaxonomyRow(category)
	assert.Equal(t, string(owners), categoryRow.OwnerReferences)
	assert.Equal(t, category, fromCategoryTaxonomyRow(categoryRow))

	collection := &datastore.Collection{
		UID: uid, Namespace: "shop", Name: "collections_by_namespace", APIVersion: "catalog/v1", Kind: "Collection",
		Generation: 4, ResourceVersion: "7", Revision: "main@sha", CreationTimestamp: now,
		CreationActor: "creator", UpdateTimestamp: now.Add(time.Minute), UpdateActor: "updater",
		Labels: product.Labels, Annotations: product.Annotations, OwnerReferences: owners,
		Finalizers: product.Finalizers, DeletionTimestamp: &deletedAt, RepositoryID: repositoryID,
		SourcePath: "collections/collection.md", GitCommitSHA: "sha", GitRef: "refs/heads/main",
		Spec: spec, Body: "body", Status: status,
	}
	collectionRow := toCollectionRow(collection)
	assert.Equal(t, string(owners), collectionRow.OwnerReferences)
	assert.Equal(t, collection, fromCollectionRow(collectionRow))

	variant := &datastore.ProductVariant{
		UID: uid, Namespace: "shop", Name: "variant", APIVersion: "catalog/v1", Kind: "ProductVariant",
		Generation: 4, ResourceVersion: "7", Revision: "main@sha", CreationTimestamp: now,
		CreationActor: "creator", UpdateTimestamp: now.Add(time.Minute), UpdateActor: "updater",
		Labels: product.Labels, Annotations: product.Annotations, OwnerReferences: owners,
		Finalizers: product.Finalizers, DeletionTimestamp: &deletedAt, SKU: "sku", ProductRefName: "product",
		RepositoryID: repositoryID, SourcePath: "variants/variant.md", GitCommitSHA: "sha", GitRef: "refs/heads/main",
		Spec: spec, Body: "body", Status: status,
	}
	variantRow := toProductVariantRow(variant)
	assert.Equal(t, string(owners), variantRow.OwnerReferences)
	assert.Equal(t, variant, fromProductVariantRow(variantRow))
}

func TestCatalogueCreateCompensatesEveryFailureBoundary(t *testing.T) {
	tests := []struct {
		kind        string
		projections []string
	}{
		{kind: "Product", projections: []string{"name", "uid", "authoritative"}},
		{kind: "CategoryTaxonomy", projections: []string{"name", "uid", "authoritative"}},
		{kind: "Collection", projections: []string{"name", "uid", "authoritative"}},
		{kind: "ProductVariant", projections: []string{"name", "uid", "sku", "authoritative", "product-ref"}},
	}

	for _, test := range tests {
		for _, failedProjection := range test.projections {
			for _, point := range []failurePoint{failureBefore, failureAfter} {
				t.Run(test.kind+"/"+failedProjection+"/"+string(point), func(t *testing.T) {
					state := make(map[string]bool)
					injector := newTestFailureInjector()
					injector.fail("create-"+failedProjection, point)
					executor := newMutationExecutor(injector)

					err := executor.execute(context.Background(), createActions(test.kind, state, test.projections)...)

					require.Error(t, err)
					for _, projection := range test.projections {
						assert.False(t, state[projection], "projection %s was not compensated", projection)
					}
				})
			}
		}
	}
}

func TestCatalogueUpdateRollsForwardAfterProjectionFailure(t *testing.T) {
	tests := []struct {
		kind        string
		projections []string
	}{
		{kind: "Product", projections: []string{"name", "uid"}},
		{kind: "CategoryTaxonomy", projections: []string{"name", "uid"}},
		{kind: "Collection", projections: []string{"name", "uid"}},
		{kind: "ProductVariant", projections: []string{"name", "uid", "sku", "product-ref"}},
	}
	for _, test := range tests {
		for _, failedProjection := range test.projections {
			for _, point := range []failurePoint{failureBefore, failureAfter} {
				t.Run(test.kind+"/"+failedProjection+"/"+string(point), func(t *testing.T) {
					state := map[string]string{"authoritative": "old"}
					for _, projection := range test.projections {
						state[projection] = "old"
					}
					injector := newTestFailureInjector()
					injector.fail("converge-"+failedProjection, point)
					executor := newMutationExecutor(injector)

					err := executor.executeUpdate(
						context.Background(),
						"2",
						updateAuthoritativeAction(test.kind, state),
						updateProjectionActions(test.kind, state, test.projections)...,
					)

					require.NoError(t, err)
					for key, value := range state {
						assert.Equal(t, "new", value, "%s did not roll forward", key)
					}
				})
			}
		}
	}
}

func TestCatalogueUpdateReturnsRepairRequiredWithoutRollingBackAuthoritative(t *testing.T) {
	state := map[string]string{"authoritative": "old", "old-sku": "present", "new-sku": "missing"}
	executor := newMutationExecutor(nil)
	newSKUConverged := false

	err := executor.executeUpdate(
		context.Background(),
		"2",
		mutationAction{
			Step: datastore.MutationStep{ResourceKind: "ProductVariant", Projection: "authoritative", Action: "update-authoritative"},
			Apply: func(context.Context) error {
				state["authoritative"] = "new"
				return nil
			},
		},
		mutationAction{
			Step: datastore.MutationStep{ResourceKind: "ProductVariant", Projection: "new-sku", Action: "converge-sku"},
			Apply: func(context.Context) error {
				return errors.New("persistent projection failure")
			},
		},
		mutationAction{
			Step: datastore.MutationStep{ResourceKind: "ProductVariant", Projection: "old-sku", Action: "delete-old-sku"},
			Apply: func(context.Context) error {
				if !newSKUConverged {
					return errors.New("new sku has not converged")
				}
				state["old-sku"] = "deleted"
				return nil
			},
		},
	)

	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	assert.Equal(t, "new", state["authoritative"])
	assert.Equal(t, "present", state["old-sku"])
	assert.Equal(t, "missing", state["new-sku"])
}

func TestCatalogueUpdateConvergesAfterAuthoritativePostCommitFailure(t *testing.T) {
	state := map[string]string{"authoritative": "old", "name": "old"}
	injector := newTestFailureInjector()
	injector.fail("update-authoritative", failureAfter)
	executor := newMutationExecutor(injector)

	err := executor.executeUpdate(
		context.Background(),
		"2",
		mutationAction{
			Step: datastore.MutationStep{ResourceKind: "Product", Projection: "authoritative", Action: "update-authoritative"},
			Apply: func(context.Context) error {
				state["authoritative"] = "new"
				return nil
			},
		},
		mutationAction{
			Step: datastore.MutationStep{ResourceKind: "Product", Projection: "name", Action: "converge-name"},
			Apply: func(context.Context) error {
				state["name"] = "new"
				return nil
			},
		},
	)

	require.Error(t, err)
	assert.Equal(t, "new", state["authoritative"])
	assert.Equal(t, "new", state["name"])
}

func TestCatalogueUpdateAuthoritativeFailurePerformsNoProjectionWrites(t *testing.T) {
	for _, applyFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "apply"}[applyFailure], func(t *testing.T) {
			state := map[string]string{"authoritative": "old", "name": "old"}
			injector := newTestFailureInjector()
			if !applyFailure {
				injector.fail("update-authoritative", failureBefore)
			}
			executor := newMutationExecutor(injector)
			authoritative := updateAuthoritativeAction("Product", state)
			if applyFailure {
				authoritative.Apply = func(context.Context) error {
					return errors.New("compare-and-set failed")
				}
			}

			err := executor.executeUpdate(
				context.Background(),
				"2",
				authoritative,
				updateProjectionActions("Product", state, []string{"name"})...,
			)

			require.Error(t, err)
			assert.Equal(t, "old", state["authoritative"])
			assert.Equal(t, "old", state["name"])
		})
	}
}

func TestCatalogueStaleVersionPerformsNoWrites(t *testing.T) {
	writes := 0
	err := datastore.ValidateCatalogUpdateVersion("3", "1", "2")
	if err == nil {
		writes++
	}

	require.ErrorIs(t, err, datastore.ErrConflict)
	assert.Zero(t, writes)
}

func TestCatalogueDeleteRestoresExactPriorStateAtEveryPrecommitBoundary(t *testing.T) {
	tests := []struct {
		kind        string
		projections []string
	}{
		{kind: "Product", projections: []string{"name", "uid"}},
		{kind: "CategoryTaxonomy", projections: []string{"name", "uid"}},
		{kind: "Collection", projections: []string{"name", "uid"}},
		{kind: "ProductVariant", projections: []string{"name", "uid", "sku", "product-ref"}},
	}
	for _, test := range tests {
		for _, failedStep := range append(append([]string(nil), test.projections...), "authoritative") {
			for _, point := range []failurePoint{failureBefore, failureAfter} {
				if failedStep == "authoritative" && point == failureAfter {
					continue
				}
				t.Run(test.kind+"/"+failedStep+"/"+string(point), func(t *testing.T) {
					state := map[string]bool{"authoritative": true}
					for _, projection := range test.projections {
						state[projection] = true
					}
					injector := newTestFailureInjector()
					injector.fail("delete-"+failedStep, point)
					executor := newMutationExecutor(injector)

					err := executor.executeDelete(
						context.Background(),
						deleteAuthoritativeAction(state, nil),
						deleteProjectionActions(state, test.projections)...,
					)

					require.Error(t, err)
					for key, present := range state {
						assert.True(t, present, "%s was not restored", key)
					}
				})
			}
		}
	}
}

func TestCatalogueDeleteAfterAuthoritativeFailureKeepsDesiredFinalState(t *testing.T) {
	state := map[string]bool{"authoritative": true, "name": true, "uid": true}
	injector := newTestFailureInjector()
	injector.fail("delete-authoritative", failureAfter)
	executor := newMutationExecutor(injector)

	err := executor.executeDelete(
		context.Background(),
		deleteAuthoritativeAction(state, nil),
		deleteProjectionActions(state, []string{"name", "uid"})...,
	)

	require.Error(t, err)
	assert.False(t, state["authoritative"])
	assert.False(t, state["name"])
	assert.False(t, state["uid"])
}

func TestCatalogueDeleteCompensationFailureRequiresRepair(t *testing.T) {
	state := map[string]bool{"authoritative": true, "name": true}
	executor := newMutationExecutor(nil)
	projection := mutationAction{
		Step: datastore.MutationStep{ResourceKind: "Collection", Projection: "name", Action: "delete-name"},
		Apply: func(context.Context) error {
			state["name"] = false
			return nil
		},
		Compensate: func(context.Context) error {
			return errors.New("restore failed")
		},
	}

	err := executor.executeDelete(
		context.Background(),
		deleteAuthoritativeAction(state, errors.New("delete failed")),
		projection,
	)

	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	assert.True(t, state["authoritative"])
	assert.False(t, state["name"])
}

func TestConditionalDeleteConflictDoesNotMutateProjections(t *testing.T) {
	projectionDeletes := 0
	executor := newMutationExecutor(nil)
	authoritative := mutationAction{
		Step:  datastore.MutationStep{Operation: "delete", ResourceKind: "ProductVariant", Projection: "authoritative", Action: "delete-authoritative"},
		Apply: func(context.Context) error { return datastore.ErrConflict },
	}
	projection := mutationAction{
		Step: datastore.MutationStep{Operation: "delete", ResourceKind: "ProductVariant", Projection: "sku", Action: "delete-sku"},
		Apply: func(context.Context) error {
			projectionDeletes++
			return nil
		},
	}

	err := executor.executeConditionalDelete(context.Background(), "1", authoritative, projection)

	require.ErrorIs(t, err, datastore.ErrConflict)
	assert.Zero(t, projectionDeletes)
}

func createActions(kind string, state map[string]bool, projections []string) []mutationAction {
	actions := make([]mutationAction, 0, len(projections))
	for _, projection := range projections {
		projection := projection
		actions = append(actions, mutationAction{
			Step: datastore.MutationStep{
				Operation: "create", ResourceKind: kind, Projection: projection, Action: "create-" + projection,
			},
			Apply: func(context.Context) error {
				state[projection] = true
				return nil
			},
			Compensate: func(context.Context) error {
				state[projection] = false
				return nil
			},
		})
	}
	return actions
}

func updateAuthoritativeAction(kind string, state map[string]string) mutationAction {
	return mutationAction{
		Step: datastore.MutationStep{
			Operation: "update", ResourceKind: kind, Projection: "authoritative", Action: "update-authoritative",
		},
		Apply: func(context.Context) error {
			state["authoritative"] = "new"
			return nil
		},
	}
}

func updateProjectionActions(kind string, state map[string]string, projections []string) []mutationAction {
	actions := make([]mutationAction, 0, len(projections))
	for _, projection := range projections {
		projection := projection
		actions = append(actions, mutationAction{
			Step: datastore.MutationStep{
				Operation: "update", ResourceKind: kind, Projection: projection, Action: "converge-" + projection,
			},
			Apply: func(context.Context) error {
				state[projection] = "new"
				return nil
			},
		})
	}
	return actions
}

func deleteProjectionActions(state map[string]bool, projections []string) []mutationAction {
	actions := make([]mutationAction, 0, len(projections))
	for _, projection := range projections {
		projection := projection
		actions = append(actions, mutationAction{
			Step: datastore.MutationStep{Operation: "delete", Projection: projection, Action: "delete-" + projection},
			Apply: func(context.Context) error {
				state[projection] = false
				return nil
			},
			Compensate: func(context.Context) error {
				state[projection] = true
				return nil
			},
		})
	}
	return actions
}

func deleteAuthoritativeAction(state map[string]bool, applyErr error) mutationAction {
	return mutationAction{
		Step: datastore.MutationStep{Operation: "delete", Projection: "authoritative", Action: "delete-authoritative"},
		Apply: func(context.Context) error {
			if applyErr != nil {
				return applyErr
			}
			state["authoritative"] = false
			return nil
		},
	}
}
