// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gocql/gocql"
	scyllacdc "github.com/scylladb/scylla-cdc-go"
)

const productCDCSource = "Product"

// RunProductCDC consumes the Product authoritative/list projection. Product
// rows are list-visible in this same table, so a postimage can enter the
// shared journal without a secondary projection fence.
func (s *scyllaDatastore) RunProductCDC(ctx context.Context, materializer *watchjournal.Materializer, lease datastore.ResourceWatchLease, changeAgeLimit, confidenceWindow time.Duration, ready func()) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sequencer := newNamespaceCDCSequencer(materializer, lease)
	progress := &repositoryCDCProgressManager{journal: s, lease: lease, source: productCDCSource}
	progress.beginGeneration = func(generationCtx context.Context, generation time.Time) error {
		streams, err := s.resourceCDCGenerationStreams(generationCtx, generation, "products_by_namespace", productCDCSource)
		if err != nil {
			return err
		}
		return sequencer.BeginGeneration(generationCtx, generation, streams)
	}
	sequencerErr := make(chan error, 1)
	go func() { sequencerErr <- sequencer.Run(runCtx); cancel() }()
	factory := &productCDCConsumerFactory{sequencer: sequencer, onReady: ready}
	reader, err := scyllacdc.NewReader(runCtx, &scyllacdc.ReaderConfig{
		Session: s.session.Session, TableNames: []string{s.keyspace + ".products_by_namespace"}, Consistency: gocql.Quorum,
		ChangeConsumerFactory: factory, ProgressManager: progress, Logger: zapCDCLogger{s.log},
		Advanced: scyllacdc.AdvancedReaderConfig{ChangeAgeLimit: changeAgeLimit, ConfidenceWindowSize: confidenceWindow,
			PostEmptyQueryDelay: 100 * time.Millisecond, PostNonEmptyQueryDelay: 100 * time.Millisecond,
			PostFailedQueryDelay: 100 * time.Millisecond, MaxPostFailedQueryDelay: 2 * time.Second, TableMissingRetryLimit: 30},
	})
	if err != nil {
		return fmt.Errorf("create Product CDC reader: %w", err)
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

type productCDCConsumerFactory struct {
	sequencer *namespaceCDCSequencer
	onReady   func()
	readyOnce sync.Once
}

func (f *productCDCConsumerFactory) CreateChangeConsumer(ctx context.Context, input scyllacdc.CreateChangeConsumerInput) (scyllacdc.ChangeConsumer, error) {
	if f.sequencer == nil || input.ProgressReporter == nil {
		return productCDCFailedConsumer{fmt.Errorf("product CDC consumer is not configured")}, nil
	}
	streamID := encodeCDCStreamID(input.StreamID)
	if err := f.sequencer.Register(ctx, streamID); err != nil {
		return productCDCFailedConsumer{err}, nil
	}
	return &productCDCConsumer{
		sequencer: f.sequencer,
		streamID:  streamID,
		reporter:  input.ProgressReporter,
		onReady:   f.markReady,
	}, nil
}

func (f *productCDCConsumerFactory) markReady() {
	if f.onReady != nil {
		f.readyOnce.Do(f.onReady)
	}
}

type productCDCFailedConsumer struct{ err error }

func (c productCDCFailedConsumer) Consume(context.Context, scyllacdc.Change) error { return c.err }
func (c productCDCFailedConsumer) Empty(context.Context, gocql.UUID) error         { return c.err }
func (productCDCFailedConsumer) End() error                                        { return nil }

type productCDCConsumer struct {
	sequencer *namespaceCDCSequencer
	streamID  string
	reporter  *scyllacdc.ProgressReporter
	onReady   func()
}

func (c *productCDCConsumer) Consume(ctx context.Context, change scyllacdc.Change) error {
	before := productCDCPostimage(change.PreImage)
	after := productCDCPostimage(change.PostImage)
	beforeJSON, err := marshalOptionalProduct(before)
	if err != nil {
		return err
	}
	afterJSON, err := marshalOptionalProduct(after)
	if err != nil {
		return err
	}
	name, namespace := "", ""
	if after != nil {
		name, namespace = after.Name, after.Namespace
	} else if before != nil {
		name, namespace = before.Name, before.Namespace
	}
	err = c.sequencer.Submit(ctx, namespaceCDCSequenceRequest{cdcTime: change.Time, streamID: c.streamID,
		markProgress: func(markCtx context.Context) error {
			return c.reporter.MarkProgress(markCtx, scyllacdc.Progress{LastProcessedRecordTime: change.Time})
		},
		change: watchjournal.Change{Kind: productCDCSource, Namespace: namespace, StreamID: c.streamID, Position: change.Time.Bytes(), DeduplicationKey: c.streamID + ":" + change.Time.String(), Name: name, Before: beforeJSON, After: afterJSON, At: change.Time.Time().UTC()},
	})
	if err == nil && c.onReady != nil {
		c.onReady()
	}
	return err
}
func (c *productCDCConsumer) Empty(ctx context.Context, ackTime gocql.UUID) error {
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
func (c *productCDCConsumer) End() error { return c.sequencer.Unregister(c.streamID) }

func productCDCPostimage(rows []*scyllacdc.ChangeRow) *datastore.Product {
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
	return fromProductRow(scyllaRow)
}

func marshalOptionalProduct(product *datastore.Product) (json.RawMessage, error) {
	if product == nil {
		return nil, nil
	}
	raw, err := json.Marshal(product)
	if err != nil {
		return nil, fmt.Errorf("marshal Product CDC postimage: %w", err)
	}
	return raw, nil
}
