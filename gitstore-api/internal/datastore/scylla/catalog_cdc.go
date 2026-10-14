// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gocql/gocql"
	scyllacdc "github.com/scylladb/scylla-cdc-go"
)

// catalogCDCSource is one namespaced catalog kind whose authoritative table
// feeds the shared resource watch journal. New kinds (including future
// custom resources) register here instead of adding a dedicated runner.
type catalogCDCSource struct {
	table string
	// payload converts a decoded CDC image into the kind's datastore JSON.
	payload func(rows []*scyllacdc.ChangeRow) (json.RawMessage, error)
}

var catalogCDCSources = map[string]catalogCDCSource{
	"Product": {table: "products_by_namespace", payload: func(rows []*scyllacdc.ChangeRow) (json.RawMessage, error) {
		return marshalCDCImage(catalogCDCRow(rows), fromProductRow)
	}},
	"File": {table: "files_by_namespace", payload: func(rows []*scyllacdc.ChangeRow) (json.RawMessage, error) {
		// Both authoritative tables share the same column layout. Convert
		// through fileRow so File payloads keep their own representation.
		return marshalCDCImage((*fileRow)(catalogCDCRow(rows)), fromFileRow)
	}},
	"CategoryTaxonomy": {table: "category_taxonomies_by_namespace", payload: func(rows []*scyllacdc.ChangeRow) (json.RawMessage, error) {
		return marshalCDCImage(categoryTaxonomyCDCRow(rows), fromCategoryTaxonomyRow)
	}},
}

// CatalogCDCKinds lists the registered catalog CDC sources in stable order.
func (s *scyllaDatastore) CatalogCDCKinds() []string {
	return slices.Sorted(maps.Keys(catalogCDCSources))
}

// RunCatalogCDC consumes one registered catalog kind's authoritative table.
// Catalog rows are list-visible in that same table, so a postimage can enter
// the shared journal without a secondary projection fence.
func (s *scyllaDatastore) RunCatalogCDC(ctx context.Context, kind string, materializer *watchjournal.Materializer, lease datastore.ResourceWatchLease, changeAgeLimit, confidenceWindow time.Duration, ready func()) error {
	source, ok := catalogCDCSources[kind]
	if !ok {
		return fmt.Errorf("unsupported catalog CDC kind %q", kind)
	}
	return s.runCatalogResourceCDC(ctx, materializer, lease, changeAgeLimit, confidenceWindow, ready, kind, source)
}

func (s *scyllaDatastore) runCatalogResourceCDC(ctx context.Context, materializer *watchjournal.Materializer, lease datastore.ResourceWatchLease, changeAgeLimit, confidenceWindow time.Duration, ready func(), kind string, source catalogCDCSource) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sequencer := newNamespaceCDCSequencer(materializer, lease)
	progress := &repositoryCDCProgressManager{journal: s, lease: lease, source: kind}
	progress.beginGeneration = func(generationCtx context.Context, generation time.Time) error {
		streams, err := s.resourceCDCGenerationStreams(generationCtx, generation, source.table, kind)
		if err != nil {
			return err
		}
		return sequencer.BeginGeneration(generationCtx, generation, streams)
	}
	sequencerErr := make(chan error, 1)
	go func() { sequencerErr <- sequencer.Run(runCtx); cancel() }()
	factory := &catalogCDCConsumerFactory{sequencer: sequencer, onReady: ready, kind: kind, payload: source.payload}
	reader, err := scyllacdc.NewReader(runCtx, &scyllacdc.ReaderConfig{
		Session: s.session.Session, TableNames: []string{s.keyspace + "." + source.table}, Consistency: gocql.Quorum,
		ChangeConsumerFactory: factory, ProgressManager: progress, Logger: zapCDCLogger{s.log},
		Advanced: scyllacdc.AdvancedReaderConfig{ChangeAgeLimit: changeAgeLimit, ConfidenceWindowSize: confidenceWindow,
			PostEmptyQueryDelay: 100 * time.Millisecond, PostNonEmptyQueryDelay: 100 * time.Millisecond,
			PostFailedQueryDelay: 100 * time.Millisecond, MaxPostFailedQueryDelay: 2 * time.Second, TableMissingRetryLimit: 30},
	})
	if err != nil {
		return fmt.Errorf("create %s CDC reader: %w", kind, err)
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

type catalogCDCConsumerFactory struct {
	sequencer *namespaceCDCSequencer
	onReady   func()
	readyOnce sync.Once
	kind      string
	payload   func([]*scyllacdc.ChangeRow) (json.RawMessage, error)
}

func (f *catalogCDCConsumerFactory) CreateChangeConsumer(ctx context.Context, input scyllacdc.CreateChangeConsumerInput) (scyllacdc.ChangeConsumer, error) {
	if f.sequencer == nil || input.ProgressReporter == nil {
		return catalogCDCFailedConsumer{fmt.Errorf("%s CDC consumer is not configured", f.kind)}, nil
	}
	streamID := encodeCDCStreamID(input.StreamID)
	if err := f.sequencer.Register(ctx, streamID); err != nil {
		return catalogCDCFailedConsumer{err}, nil
	}
	return &catalogCDCConsumer{
		sequencer: f.sequencer,
		streamID:  streamID,
		reporter:  input.ProgressReporter,
		onReady:   f.markReady,
		kind:      f.kind,
		payload:   f.payload,
	}, nil
}

func (f *catalogCDCConsumerFactory) markReady() {
	if f.onReady != nil {
		f.readyOnce.Do(f.onReady)
	}
}

type catalogCDCFailedConsumer struct{ err error }

func (c catalogCDCFailedConsumer) Consume(context.Context, scyllacdc.Change) error { return c.err }
func (c catalogCDCFailedConsumer) Empty(context.Context, gocql.UUID) error         { return c.err }
func (catalogCDCFailedConsumer) End() error                                        { return nil }

type catalogCDCConsumer struct {
	sequencer *namespaceCDCSequencer
	streamID  string
	reporter  *scyllacdc.ProgressReporter
	onReady   func()
	kind      string
	payload   func([]*scyllacdc.ChangeRow) (json.RawMessage, error)
}

func (c *catalogCDCConsumer) Consume(ctx context.Context, change scyllacdc.Change) error {
	changes, err := c.changes(change)
	if err != nil {
		return err
	}
	// Checkpoint the source position only after every row in its frame publishes.
	err = c.sequencer.Submit(ctx, namespaceCDCSequenceRequest{cdcTime: change.Time, streamID: c.streamID,
		changes: changes, progressOnly: len(changes) == 0,
		markProgress: func(markCtx context.Context) error {
			return c.reporter.MarkProgress(markCtx, scyllacdc.Progress{LastProcessedRecordTime: change.Time})
		},
	})
	if err == nil && c.onReady != nil {
		c.onReady()
	}
	return err
}

const catalogCDCMaxRowsPerChange = 16

func (c *catalogCDCConsumer) changes(change scyllacdc.Change) ([]watchjournal.Change, error) {
	if len(change.PreImage) > catalogCDCMaxRowsPerChange || len(change.PostImage) > catalogCDCMaxRowsPerChange {
		return nil, fmt.Errorf("%w: %s CDC frame exceeds %d row images", datastore.ErrNamespaceWatchDiscontinuity, c.kind, catalogCDCMaxRowsPerChange)
	}
	type image struct {
		before, after *scyllacdc.ChangeRow
		envelope      *productRow
	}
	images := make(map[gocql.UUID]*image)
	for side, rows := range [][]*scyllacdc.ChangeRow{change.PreImage, change.PostImage} {
		for _, row := range rows {
			envelope := catalogCDCRow([]*scyllacdc.ChangeRow{row})
			if envelope == nil || envelope.UID == (gocql.UUID{}) || envelope.Namespace == "" {
				return nil, fmt.Errorf("%w: %s CDC image has no authoritative identity", datastore.ErrNamespaceWatchDiscontinuity, c.kind)
			}
			pair := images[envelope.UID]
			if pair == nil {
				if len(images) == catalogCDCMaxRowsPerChange {
					return nil, fmt.Errorf("%w: %s CDC frame exceeds %d resources", datastore.ErrNamespaceWatchDiscontinuity, c.kind, catalogCDCMaxRowsPerChange)
				}
				pair = &image{envelope: envelope}
				images[envelope.UID] = pair
			}
			if pair.envelope.Namespace != envelope.Namespace || !pair.envelope.CreationTimestamp.Equal(envelope.CreationTimestamp) {
				return nil, fmt.Errorf("%w: %s CDC images disagree on immutable key", datastore.ErrNamespaceWatchDiscontinuity, c.kind)
			}
			if side == 0 {
				if pair.before != nil {
					return nil, fmt.Errorf("%w: %s CDC frame repeats a preimage", datastore.ErrNamespaceWatchDiscontinuity, c.kind)
				}
				pair.before = row
			} else {
				if pair.after != nil {
					return nil, fmt.Errorf("%w: %s CDC frame repeats a postimage", datastore.ErrNamespaceWatchDiscontinuity, c.kind)
				}
				pair.after, pair.envelope = row, envelope
			}
		}
	}
	ids := slices.Collect(maps.Keys(images))
	slices.SortFunc(ids, func(a, b gocql.UUID) int { return bytes.Compare(a[:], b[:]) })
	changes := make([]watchjournal.Change, 0, len(ids))
	for _, uid := range ids {
		pair := images[uid]
		var before, after json.RawMessage
		var err error
		if pair.before != nil {
			before, err = c.payload([]*scyllacdc.ChangeRow{pair.before})
			if err != nil {
				return nil, err
			}
		}
		if pair.after != nil {
			after, err = c.payload([]*scyllacdc.ChangeRow{pair.after})
			if err != nil {
				return nil, err
			}
		}
		key := c.streamID + ":" + change.Time.String()
		if len(ids) > 1 {
			key += ":" + uid.String()
		}
		changes = append(changes, watchjournal.Change{
			Kind: c.kind, Namespace: pair.envelope.Namespace, StreamID: c.streamID, Position: change.Time.Bytes(),
			DeduplicationKey: key, Name: pair.envelope.Name, Before: before, After: after, At: change.Time.Time().UTC(),
		})
	}
	return changes, nil
}

func (c *catalogCDCConsumer) Empty(ctx context.Context, ackTime gocql.UUID) error {
	err := c.sequencer.Submit(ctx, namespaceCDCSequenceRequest{cdcTime: ackTime, streamID: c.streamID, progressOnly: true,
		markProgress: func(markCtx context.Context) error {
			return c.reporter.MarkProgress(markCtx, scyllacdc.Progress{LastProcessedRecordTime: ackTime})
		},
	})
	if err == nil && c.onReady != nil {
		c.onReady()
	}
	return err
}
func (c *catalogCDCConsumer) End() error { return c.sequencer.Unregister(c.streamID) }

func catalogCDCRow(rows []*scyllacdc.ChangeRow) *productRow {
	if len(rows) == 0 || rows[0] == nil {
		return nil
	}
	row := rows[0]
	scyllaRow := &productRow{}
	assignCDC(row, "namespace", &scyllaRow.Namespace)
	assignCDC(row, "creation_timestamp", &scyllaRow.CreationTimestamp)
	assignCDC(row, "uid", &scyllaRow.UID)
	assignCDC(row, "name", &scyllaRow.Name)
	assignCDC(row, "api_version", &scyllaRow.APIVersion)
	assignCDC(row, "kind", &scyllaRow.Kind)
	assignCDC(row, "generation", &scyllaRow.Generation)
	assignCDC(row, "resource_version", &scyllaRow.ResourceVersion)
	assignCDC(row, "revision", &scyllaRow.Revision)
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
	return scyllaRow
}

// categoryTaxonomyCDCRow decodes the CategoryTaxonomy image, which carries
// the hierarchy columns on top of the shared catalog envelope.
func categoryTaxonomyCDCRow(rows []*scyllacdc.ChangeRow) *categoryTaxonomyRow {
	base := catalogCDCRow(rows)
	if base == nil {
		return nil
	}
	row := &categoryTaxonomyRow{
		Namespace: base.Namespace, CreationTimestamp: base.CreationTimestamp, UID: base.UID, Name: base.Name,
		APIVersion: base.APIVersion, Kind: base.Kind, Generation: base.Generation, ResourceVersion: base.ResourceVersion,
		Revision: base.Revision, CreationActor: base.CreationActor, UpdateTimestamp: base.UpdateTimestamp, UpdateActor: base.UpdateActor,
		Labels: base.Labels, Annotations: base.Annotations, OwnerReferences: base.OwnerReferences, Finalizers: base.Finalizers,
		DeletionTimestamp: base.DeletionTimestamp, RepositoryID: base.RepositoryID, SourcePath: base.SourcePath,
		GitCommitSHA: base.GitCommitSHA, GitRef: base.GitRef, Spec: base.Spec, Body: base.Body, Status: base.Status,
	}
	assignCDC(rows[0], "parent_name", &row.ParentName)
	assignCDC(rows[0], "ancestor_path", &row.AncestorPath)
	return row
}

func marshalCDCImage[R any, T any](row *R, convert func(*R) *T) (json.RawMessage, error) {
	if row == nil {
		return nil, nil
	}
	raw, err := json.Marshal(convert(row))
	if err != nil {
		return nil, fmt.Errorf("marshal CDC image: %w", err)
	}
	return raw, nil
}
