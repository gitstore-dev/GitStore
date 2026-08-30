// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gocql/gocql"
	scyllacdc "github.com/scylladb/scylla-cdc-go"
	"go.uber.org/zap"
)

const namespaceCDCGenerationProgress = "__namespace_cdc_generation__"

// RunNamespaceCDC consumes only the authoritative namespaces_by_uid CDC log.
// The caller owns lease renewal and cancels ctx immediately on fencing loss.
func (s *scyllaDatastore) RunNamespaceCDC(ctx context.Context, materializer *watchjournal.Materializer, lease datastore.NamespaceWatchLease, changeAgeLimit time.Duration, ready func()) error {
	progress := &namespaceCDCProgressManager{journal: s, lease: lease}
	factory := &namespaceCDCConsumerFactory{materializer: materializer, lease: lease}
	reader, err := scyllacdc.NewReader(ctx, &scyllacdc.ReaderConfig{
		Session:               s.session.Session,
		TableNames:            []string{s.keyspace + ".namespaces_by_uid"},
		Consistency:           gocql.Quorum,
		ChangeConsumerFactory: factory,
		ProgressManager:       progress,
		Logger:                zapCDCLogger{s.log},
		Advanced: scyllacdc.AdvancedReaderConfig{
			ChangeAgeLimit:          changeAgeLimit,
			PostEmptyQueryDelay:     100 * time.Millisecond,
			PostNonEmptyQueryDelay:  100 * time.Millisecond,
			PostFailedQueryDelay:    100 * time.Millisecond,
			MaxPostFailedQueryDelay: 2 * time.Second,
			TableMissingRetryLimit:  30,
		},
	})
	if err != nil {
		return fmt.Errorf("create Namespace CDC reader: %w", err)
	}
	if ready != nil {
		ready()
	}
	return reader.Run(ctx)
}

type zapCDCLogger struct{ log *zap.Logger }

func (l zapCDCLogger) Printf(format string, values ...interface{}) {
	if l.log != nil {
		l.log.Debug("Namespace CDC reader", zap.String("message", fmt.Sprintf(format, values...)))
	}
}

type namespaceCDCConsumerFactory struct {
	materializer *watchjournal.Materializer
	lease        datastore.NamespaceWatchLease
}

func (f *namespaceCDCConsumerFactory) CreateChangeConsumer(_ context.Context, input scyllacdc.CreateChangeConsumerInput) (scyllacdc.ChangeConsumer, error) {
	if f.materializer == nil || input.ProgressReporter == nil {
		return nil, fmt.Errorf("namespace CDC consumer is not configured")
	}
	return &namespaceCDCConsumer{
		materializer: f.materializer,
		lease:        f.lease,
		streamID:     encodeCDCStreamID(input.StreamID),
		reporter:     input.ProgressReporter,
	}, nil
}

type namespaceCDCConsumer struct {
	materializer *watchjournal.Materializer
	lease        datastore.NamespaceWatchLease
	streamID     string
	reporter     *scyllacdc.ProgressReporter
}

func (c *namespaceCDCConsumer) Consume(ctx context.Context, change scyllacdc.Change) error {
	before := namespaceCDCPostimage(change.PreImage)
	after := namespaceCDCPostimage(change.PostImage)
	name := ""
	if after != nil {
		name = after.Name
	} else if before != nil {
		name = before.Name
	}
	beforeJSON, err := marshalOptionalNamespace(before)
	if err != nil {
		return err
	}
	afterJSON, err := marshalOptionalNamespace(after)
	if err != nil {
		return err
	}
	_, err = c.materializer.Process(ctx, c.lease, watchjournal.Change{
		StreamID:         c.streamID,
		Position:         change.Time.Bytes(),
		DeduplicationKey: c.streamID + ":" + change.Time.String(),
		Name:             name,
		Before:           beforeJSON,
		After:            afterJSON,
		At:               change.Time.Time().UTC(),
	})
	if err != nil {
		return err
	}
	// The official reader advances only after the materializer returned from
	// append-then-progress. This duplicate fenced save is intentional: it keeps
	// the reader's generation-aware resume contract aligned with our journal.
	return c.reporter.MarkProgress(ctx, scyllacdc.Progress{LastProcessedRecordTime: change.Time})
}

func (c *namespaceCDCConsumer) End() error { return nil }

func namespaceCDCPostimage(rows []*scyllacdc.ChangeRow) *datastore.Namespace {
	if len(rows) == 0 || rows[0] == nil {
		return nil
	}
	row := rows[0]
	scyllaRow := &namespaceRow{}
	assignCDC(row, "api_version", &scyllaRow.APIVersion)
	assignCDC(row, "kind", &scyllaRow.Kind)
	assignCDC(row, "uid", &scyllaRow.UID)
	assignCDC(row, "name", &scyllaRow.Name)
	assignCDC(row, "title", &scyllaRow.Title)
	assignCDC(row, "tier", &scyllaRow.Tier)
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
	assignCDC(row, "source_path", &scyllaRow.SourcePath)
	assignCDC(row, "git_commit_sha", &scyllaRow.GitCommitSHA)
	assignCDC(row, "git_ref", &scyllaRow.GitRef)
	assignCDC(row, "spec", &scyllaRow.Spec)
	assignCDC(row, "body", &scyllaRow.Body)
	assignCDC(row, "status", &scyllaRow.Status)
	return fromNamespaceRow(scyllaRow)
}

func assignCDC(row *scyllacdc.ChangeRow, column string, destination any) {
	value, ok := row.GetValue(column)
	if !ok || value == nil {
		return
	}
	source := reflect.ValueOf(value)
	for source.Kind() == reflect.Pointer {
		if source.IsNil() {
			return
		}
		source = source.Elem()
	}
	target := reflect.ValueOf(destination)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return
	}
	target = target.Elem()
	if source.Type().AssignableTo(target.Type()) {
		target.Set(source)
	}
}

func marshalOptionalNamespace(namespace *datastore.Namespace) (json.RawMessage, error) {
	if namespace == nil {
		return nil, nil
	}
	raw, err := json.Marshal(namespace)
	if err != nil {
		return nil, fmt.Errorf("marshal Namespace CDC postimage: %w", err)
	}
	return raw, nil
}

func encodeCDCStreamID(streamID scyllacdc.StreamID) string {
	return base64.RawURLEncoding.EncodeToString(streamID)
}

type namespaceCDCProgressManager struct {
	journal datastore.NamespaceWatchJournal
	lease   datastore.NamespaceWatchLease
}

func (m *namespaceCDCProgressManager) GetCurrentGeneration(ctx context.Context) (time.Time, error) {
	progress, err := m.journal.LoadProgress(ctx, namespaceCDCGenerationProgress)
	if errors.Is(err, datastore.ErrNotFound) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	nanos, err := strconv.ParseInt(string(progress.Position), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("decode Namespace CDC generation: %w", err)
	}
	return time.Unix(0, nanos).UTC(), nil
}

func (m *namespaceCDCProgressManager) StartGeneration(ctx context.Context, generation time.Time) error {
	return m.journal.SaveProgress(ctx, m.lease, datastore.NamespaceCDCProgress{
		StreamID:  namespaceCDCGenerationProgress,
		Position:  []byte(strconv.FormatInt(generation.UnixNano(), 10)),
		UpdatedAt: time.Now().UTC(),
	})
}

func (m *namespaceCDCProgressManager) GetProgress(ctx context.Context, generation time.Time, table string, streamID scyllacdc.StreamID) (scyllacdc.Progress, error) {
	progress, err := m.journal.LoadProgress(ctx, cdcProgressKey(generation, table, streamID))
	if errors.Is(err, datastore.ErrNotFound) {
		return scyllacdc.Progress{}, nil
	}
	if err != nil {
		return scyllacdc.Progress{}, err
	}
	position, err := gocql.UUIDFromBytes(progress.Position)
	if err != nil {
		return scyllacdc.Progress{}, fmt.Errorf("decode Namespace CDC progress: %w", err)
	}
	return scyllacdc.Progress{LastProcessedRecordTime: position}, nil
}

func (m *namespaceCDCProgressManager) SaveProgress(ctx context.Context, generation time.Time, table string, streamID scyllacdc.StreamID, progress scyllacdc.Progress) error {
	return m.journal.SaveProgress(ctx, m.lease, datastore.NamespaceCDCProgress{
		StreamID:  cdcProgressKey(generation, table, streamID),
		Position:  progress.LastProcessedRecordTime.Bytes(),
		UpdatedAt: time.Now().UTC(),
	})
}

func cdcProgressKey(generation time.Time, table string, streamID scyllacdc.StreamID) string {
	return strconv.FormatInt(generation.UnixNano(), 36) + ":" + table + ":" + encodeCDCStreamID(streamID)
}
