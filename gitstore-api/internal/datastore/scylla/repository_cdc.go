// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gocql/gocql"
	scyllacdc "github.com/scylladb/scylla-cdc-go"
)

const (
	repositoryCDCSource             = "Repository"
	repositoryCDCGenerationProgress = "__repository_cdc_generation__"
)

// RunRepositoryCDC consumes repositories_by_uid, the sole authoritative
// repository projection. Progress keys are source-qualified, allowing this
// source to share one durable journal and lease with Namespace CDC safely.
func (s *scyllaDatastore) RunRepositoryCDC(ctx context.Context, materializer *watchjournal.Materializer, lease datastore.ResourceWatchLease, changeAgeLimit, confidenceWindow time.Duration, ready func()) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sequencer := newNamespaceCDCSequencer(materializer, lease)
	progress := &repositoryCDCProgressManager{journal: s, lease: lease}
	progress.beginGeneration = func(generationCtx context.Context, generation time.Time) error {
		streams, err := s.resourceCDCGenerationStreams(generationCtx, generation, "repositories_by_uid", "Repository")
		if err != nil {
			return err
		}
		return sequencer.BeginGeneration(generationCtx, generation, streams)
	}
	sequencerErr := make(chan error, 1)
	go func() {
		sequencerErr <- sequencer.Run(runCtx)
		cancel()
	}()
	reader, err := scyllacdc.NewReader(runCtx, &scyllacdc.ReaderConfig{
		Session: s.session.Session, TableNames: []string{s.keyspace + ".repositories_by_uid"}, Consistency: gocql.Quorum,
		ChangeConsumerFactory: &repositoryCDCConsumerFactory{sequencer: sequencer, additionReady: s.repositoryCDCAdditionReady},
		ProgressManager:       progress,
		Logger:                zapCDCLogger{s.log},
		Advanced: scyllacdc.AdvancedReaderConfig{ChangeAgeLimit: changeAgeLimit, ConfidenceWindowSize: confidenceWindow,
			PostEmptyQueryDelay: 100 * time.Millisecond, PostNonEmptyQueryDelay: 100 * time.Millisecond,
			PostFailedQueryDelay: 100 * time.Millisecond, MaxPostFailedQueryDelay: 2 * time.Second, TableMissingRetryLimit: 30},
	})
	if err != nil {
		return fmt.Errorf("create Repository CDC reader: %w", err)
	}
	if ready != nil {
		ready()
	}
	readerErr := reader.Run(runCtx)
	cancel()
	sequenceErr := <-sequencerErr
	if readerErr != nil && !errors.Is(readerErr, context.Canceled) {
		return readerErr
	}
	if sequenceErr != nil && !errors.Is(sequenceErr, context.Canceled) {
		return sequenceErr
	}
	return readerErr
}

type repositoryCDCConsumerFactory struct {
	sequencer     *namespaceCDCSequencer
	additionReady func(context.Context, *datastore.Repository) (bool, error)
}

func (f *repositoryCDCConsumerFactory) CreateChangeConsumer(ctx context.Context, input scyllacdc.CreateChangeConsumerInput) (scyllacdc.ChangeConsumer, error) {
	if f.sequencer == nil || input.ProgressReporter == nil {
		return repositoryCDCFailedConsumer{fmt.Errorf("repository CDC consumer is not configured")}, nil
	}
	streamID := encodeCDCStreamID(input.StreamID)
	if err := f.sequencer.Register(ctx, streamID); err != nil {
		return repositoryCDCFailedConsumer{err}, nil
	}
	return &repositoryCDCConsumer{sequencer: f.sequencer, streamID: streamID, reporter: input.ProgressReporter, additionReady: f.additionReady}, nil
}

type repositoryCDCFailedConsumer struct{ err error }

func (c repositoryCDCFailedConsumer) Consume(context.Context, scyllacdc.Change) error { return c.err }
func (c repositoryCDCFailedConsumer) Empty(context.Context, gocql.UUID) error         { return c.err }
func (repositoryCDCFailedConsumer) End() error                                        { return nil }

type repositoryCDCConsumer struct {
	sequencer     *namespaceCDCSequencer
	streamID      string
	reporter      *scyllacdc.ProgressReporter
	additionReady func(context.Context, *datastore.Repository) (bool, error)
}

func (c *repositoryCDCConsumer) Consume(ctx context.Context, change scyllacdc.Change) error {
	before := repositoryCDCPostimage(change.PreImage)
	after := repositoryCDCPostimage(change.PostImage)
	beforeJSON, err := marshalOptionalRepository(before)
	if err != nil {
		return err
	}
	afterJSON, err := marshalOptionalRepository(after)
	if err != nil {
		return err
	}
	name, namespace := "", ""
	if after != nil {
		name, namespace = after.Name, after.Namespace
	} else if before != nil {
		name, namespace = before.Name, before.Namespace
	}
	request := namespaceCDCSequenceRequest{cdcTime: change.Time, streamID: c.streamID, markProgress: func(markCtx context.Context) error {
		return c.reporter.MarkProgress(markCtx, scyllacdc.Progress{LastProcessedRecordTime: change.Time})
	}, change: watchjournal.Change{Kind: repositoryCDCSource, Namespace: namespace, StreamID: c.streamID, Position: change.Time.Bytes(), DeduplicationKey: c.streamID + ":" + change.Time.String(), Name: name, Before: beforeJSON, After: afterJSON, At: change.Time.Time().UTC()}}
	if before == nil && after != nil && c.additionReady != nil {
		request.shouldPublish = func(publishCtx context.Context) (bool, error) {
			return c.additionReady(publishCtx, after)
		}
	}
	return c.sequencer.Submit(ctx, request)
}
func (c *repositoryCDCConsumer) Empty(ctx context.Context, ackTime gocql.UUID) error {
	return c.sequencer.Submit(ctx, namespaceCDCSequenceRequest{cdcTime: ackTime, streamID: c.streamID, progressOnly: true, markProgress: func(markCtx context.Context) error {
		return c.reporter.MarkProgress(markCtx, scyllacdc.Progress{LastProcessedRecordTime: ackTime})
	}})
}
func (c *repositoryCDCConsumer) End() error { return c.sequencer.Unregister(c.streamID) }

func repositoryCDCPostimage(rows []*scyllacdc.ChangeRow) *datastore.Repository {
	if len(rows) == 0 || rows[0] == nil {
		return nil
	}
	row := rows[0]
	scyllaRow := &repositoryRow{}
	assignCDC(row, "api_version", &scyllaRow.APIVersion)
	assignCDC(row, "kind", &scyllaRow.Kind)
	assignCDC(row, "namespace", &scyllaRow.Namespace)
	assignCDC(row, "uid", &scyllaRow.UID)
	assignCDC(row, "name", &scyllaRow.Name)
	assignCDC(row, "generation", &scyllaRow.Generation)
	assignCDC(row, "resource_version", &scyllaRow.ResourceVersion)
	assignCDC(row, "revision", &scyllaRow.Revision)
	assignCDC(row, "creation_timestamp", &scyllaRow.CreationTimestamp)
	assignCDC(row, "creation_actor", &scyllaRow.CreationActor)
	assignCDC(row, "update_timestamp", &scyllaRow.UpdateTimestamp)
	assignCDC(row, "update_actor", &scyllaRow.UpdateActor)
	assignCDC(row, "labels", &scyllaRow.Labels)
	assignCDC(row, "annotations", &scyllaRow.Annotations)
	assignCDC(row, "owner_references", &scyllaRow.OwnerReferences)
	assignCDC(row, "finalizers", &scyllaRow.Finalizers)
	assignCDC(row, "deletion_timestamp", &scyllaRow.DeletionTimestamp)
	assignCDC(row, "repository_id", &scyllaRow.RepositoryID)
	assignCDC(row, "source_path", &scyllaRow.SourcePath)
	assignCDC(row, "git_commit_sha", &scyllaRow.GitCommitSHA)
	assignCDC(row, "git_ref", &scyllaRow.GitRef)
	assignCDC(row, "spec", &scyllaRow.Spec)
	assignCDC(row, "body", &scyllaRow.Body)
	assignCDC(row, "status", &scyllaRow.Status)
	assignCDC(row, "default_branch", &scyllaRow.DefaultBranch)
	assignCDC(row, "storage_class", &scyllaRow.StorageClass)
	assignCDC(row, "max_pack_size_bytes", &scyllaRow.MaxPackSizeBytes)
	assignCDC(row, "max_file_size_bytes", &scyllaRow.MaxFileSizeBytes)
	return fromRepositoryRow(scyllaRow)
}
func marshalOptionalRepository(repository *datastore.Repository) (json.RawMessage, error) {
	if repository == nil {
		return nil, nil
	}
	raw, err := json.Marshal(repository)
	if err != nil {
		return nil, fmt.Errorf("marshal Repository CDC postimage: %w", err)
	}
	return raw, nil
}

// repositoryCDCAdditionReady establishes the list/watch linearization point
// for Repository creation. The authoritative insert produces CDC before the
// namespace listing projection is written, so an ADDED event must not become
// public until the exact version carried by that event is list-visible.
func (s *scyllaDatastore) repositoryCDCAdditionReady(ctx context.Context, repository *datastore.Repository) (bool, error) {
	if repository == nil {
		return false, nil
	}
	uid, err := gocql.ParseUUID(repository.UID)
	if err != nil {
		return false, fmt.Errorf("parse Repository CDC addition uid: %w", err)
	}
	return repositoryCDCAdditionVisible(
		ctx,
		repository,
		func(readCtx context.Context) (*repositoryCDCVersion, error) {
			var current repositoryCDCVersion
			err := s.session.Query(
				"SELECT namespace, creation_timestamp, resource_version FROM repositories_by_uid WHERE uid=?",
				nil,
			).WithContext(readCtx).Bind(uid).GetRelease(&current)
			if errors.Is(err, gocql.ErrNotFound) {
				return nil, nil
			}
			if err != nil {
				return nil, fmt.Errorf("read Repository CDC authoritative version: %w", err)
			}
			return &current, nil
		},
		func(readCtx context.Context) (bool, error) {
			var projection struct {
				UID gocql.UUID `db:"uid"`
			}
			err := s.session.Query(
				"SELECT uid FROM repositories_by_namespace WHERE namespace=? AND bucket=? AND creation_timestamp=? AND uid=?",
				nil,
			).WithContext(readCtx).Bind(repository.Namespace, namespaceBucket(repository.CreationTimestamp), repository.CreationTimestamp, uid).GetRelease(&projection)
			if errors.Is(err, gocql.ErrNotFound) {
				return false, nil
			}
			if err != nil {
				return false, fmt.Errorf("read Repository CDC namespace projection: %w", err)
			}
			return true, nil
		},
	)
}

type repositoryCDCVersion struct {
	Namespace         string    `db:"namespace"`
	CreationTimestamp time.Time `db:"creation_timestamp"`
	ResourceVersion   string    `db:"resource_version"`
}

func repositoryCDCAdditionVisible(
	ctx context.Context,
	repository *datastore.Repository,
	readCurrent func(context.Context) (*repositoryCDCVersion, error),
	projectionVisible func(context.Context) (bool, error),
) (bool, error) {
	current, err := readCurrent(ctx)
	if err != nil {
		return false, err
	}
	if current == nil || current.ResourceVersion != repository.ResourceVersion ||
		current.Namespace != repository.Namespace || !current.CreationTimestamp.Equal(repository.CreationTimestamp) {
		// The create was rolled back, deleted, or superseded. Its later CDC
		// transition (if any) represents the current list state, so advancing
		// past this stale addition is safe.
		return false, nil
	}
	visible, err := projectionVisible(ctx)
	if err != nil {
		return false, err
	}
	if !visible {
		// Returning an error prevents both public journal publication and the
		// source checkpoint from advancing. The CDC supervisor restarts from
		// durable progress and retries after the projection write completes.
		return false, fmt.Errorf("repository CDC namespace projection is not committed")
	}
	return true, nil
}

type repositoryCDCProgressManager struct {
	journal         datastore.ResourceWatchJournal
	lease           datastore.ResourceWatchLease
	source          string
	beginGeneration func(context.Context, time.Time) error
}

func (m *repositoryCDCProgressManager) sourceName() string {
	if m.source == "" {
		return repositoryCDCSource
	}
	return m.source
}

func (m *repositoryCDCProgressManager) GetCurrentGeneration(ctx context.Context) (time.Time, error) {
	progress, err := m.journal.LoadProgress(ctx, m.sourceName()+":"+repositoryCDCGenerationProgress)
	if err == datastore.ErrNotFound {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	nanos, err := strconv.ParseInt(string(progress.Position), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("decode Repository CDC generation: %w", err)
	}
	return time.Unix(0, nanos).UTC(), nil
}
func (m *repositoryCDCProgressManager) StartGeneration(ctx context.Context, generation time.Time) error {
	if m.beginGeneration != nil {
		if err := m.beginGeneration(ctx, generation); err != nil {
			return err
		}
	}
	return m.journal.SaveProgress(ctx, m.lease, datastore.ResourceCDCProgress{Source: m.sourceName(), StreamID: repositoryCDCGenerationProgress, Position: []byte(strconv.FormatInt(generation.UnixNano(), 10)), UpdatedAt: time.Now().UTC()})
}
func (m *repositoryCDCProgressManager) GetProgress(ctx context.Context, generation time.Time, table string, streamID scyllacdc.StreamID) (scyllacdc.Progress, error) {
	progress, err := m.journal.LoadProgress(ctx, m.sourceName()+":"+repositoryCDCProgressKey(generation, table, streamID))
	if err == datastore.ErrNotFound {
		return scyllacdc.Progress{}, nil
	}
	if err != nil {
		return scyllacdc.Progress{}, err
	}
	position, err := gocql.UUIDFromBytes(progress.Position)
	if err != nil {
		return scyllacdc.Progress{}, fmt.Errorf("decode Repository CDC progress: %w", err)
	}
	return scyllacdc.Progress{LastProcessedRecordTime: position}, nil
}
func (m *repositoryCDCProgressManager) SaveProgress(ctx context.Context, generation time.Time, table string, streamID scyllacdc.StreamID, progress scyllacdc.Progress) error {
	return m.journal.SaveProgress(ctx, m.lease, datastore.ResourceCDCProgress{Source: m.sourceName(), StreamID: repositoryCDCProgressKey(generation, table, streamID), Position: progress.LastProcessedRecordTime.Bytes(), UpdatedAt: progress.LastProcessedRecordTime.Time().UTC()})
}
func repositoryCDCProgressKey(generation time.Time, table string, streamID scyllacdc.StreamID) string {
	return cdcProgressKey(generation, table, streamID)
}
