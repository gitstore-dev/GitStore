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
	"sort"
	"strconv"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gocql/gocql"
	scyllacdc "github.com/scylladb/scylla-cdc-go"
	"go.uber.org/zap"
)

const namespaceCDCGenerationProgress = "__namespace_cdc_generation__"

const namespaceCDCPublishedFrontierProgress = "__namespace_cdc_published_frontier__"

const namespaceCDCPendingLimit = 100000

const namespaceCDCPublishedManifestLimit = 1 << 20

// Coalesce records briefly enough to amortize Scylla's conditional journal
// publication without consuming a material portion of the one-second
// visibility SLO.
const namespaceCDCFlushInterval = 25 * time.Millisecond

// RunNamespaceCDC consumes only the authoritative namespaces_by_uid CDC log.
// The caller owns lease renewal and cancels ctx immediately on fencing loss.
func (s *scyllaDatastore) RunNamespaceCDC(ctx context.Context, materializer *watchjournal.Materializer, lease datastore.NamespaceWatchLease, changeAgeLimit, confidenceWindow time.Duration, ready func()) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	progress := &namespaceCDCProgressManager{journal: s, lease: lease, observeProgress: materializer.ObserveCDCProgress}
	publishedThrough, hasPublished, err := progress.PublishedFrontier(runCtx)
	if err != nil {
		return err
	}
	sequencer := newNamespaceCDCSequencer(materializer, lease)
	sequencer.publishedThrough = publishedThrough
	sequencer.hasPublished = hasPublished
	sequencer.persistFrontier = progress.SavePublishedFrontier
	progress.beginGeneration = func(generationCtx context.Context, generation time.Time) error {
		streams, err := s.namespaceCDCGenerationStreams(generationCtx, generation)
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
	factory := &namespaceCDCConsumerFactory{
		sequencer:     sequencer,
		additionReady: s.namespaceCDCAdditionReady,
		deletionReady: s.namespaceCDCDeletionReady,
	}
	reader, err := scyllacdc.NewReader(runCtx, &scyllacdc.ReaderConfig{
		Session:               s.session.Session,
		TableNames:            []string{s.keyspace + ".namespaces_by_uid"},
		Consistency:           gocql.Quorum,
		ChangeConsumerFactory: factory,
		ProgressManager:       progress,
		Logger:                zapCDCLogger{s.log},
		Advanced: scyllacdc.AdvancedReaderConfig{
			ChangeAgeLimit:          changeAgeLimit,
			ConfidenceWindowSize:    confidenceWindow,
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

type zapCDCLogger struct{ log *zap.Logger }

func (l zapCDCLogger) Printf(format string, values ...interface{}) {
	if l.log != nil {
		l.log.Debug("Namespace CDC reader", zap.String("message", fmt.Sprintf(format, values...)))
	}
}

type namespaceCDCConsumerFactory struct {
	sequencer     *namespaceCDCSequencer
	additionReady func(context.Context, *datastore.Namespace, bool) (bool, error)
	deletionReady func(context.Context, *datastore.Namespace, bool) (bool, error)
}

func (f *namespaceCDCConsumerFactory) CreateChangeConsumer(ctx context.Context, input scyllacdc.CreateChangeConsumerInput) (scyllacdc.ChangeConsumer, error) {
	if f.sequencer == nil || input.ProgressReporter == nil {
		return namespaceCDCFailedConsumer{err: fmt.Errorf("namespace CDC consumer is not configured")}, nil
	}
	streamID := encodeCDCStreamID(input.StreamID)
	if err := f.sequencer.Register(ctx, streamID); err != nil {
		// scylla-cdc-go v1.2.1 records a nil consumer and later dereferences it
		// when a factory returns an error. Return a non-nil consumer that reports
		// the initialization failure through the normal reader error path.
		return namespaceCDCFailedConsumer{err: err}, nil
	}
	return &namespaceCDCConsumer{
		sequencer:     f.sequencer,
		streamID:      streamID,
		reporter:      input.ProgressReporter,
		additionReady: f.additionReady,
		deletionReady: f.deletionReady,
	}, nil
}

type namespaceCDCFailedConsumer struct{ err error }

func (c namespaceCDCFailedConsumer) Consume(context.Context, scyllacdc.Change) error { return c.err }
func (c namespaceCDCFailedConsumer) Empty(context.Context, gocql.UUID) error         { return c.err }
func (namespaceCDCFailedConsumer) End() error                                        { return nil }

type namespaceCDCConsumer struct {
	sequencer     *namespaceCDCSequencer
	streamID      string
	reporter      *scyllacdc.ProgressReporter
	additionReady func(context.Context, *datastore.Namespace, bool) (bool, error)
	deletionReady func(context.Context, *datastore.Namespace, bool) (bool, error)
}

func (c *namespaceCDCConsumer) Consume(ctx context.Context, change scyllacdc.Change) error {
	before, beforeCommitted, _ := namespaceCDCPostimage(change.PreImage)
	after, afterCommitted, afterLegacy := namespaceCDCPostimage(change.PostImage)
	disposition := namespaceCDCDispositionFor(before, beforeCommitted, after, afterCommitted)
	if disposition == namespaceCDCPromotedAddition {
		// The false -> true commit-marker update is the durable proof that the
		// listing projection was committed. Present it to the materializer as
		// the ADDED transition; the preceding staged insert is intentionally
		// progress-only so it cannot race the projection write.
		before = nil
	}
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
	request := namespaceCDCSequenceRequest{
		cdcTime:  change.Time,
		streamID: c.streamID,
		markProgress: func(markCtx context.Context) error {
			return c.reporter.MarkProgress(markCtx, scyllacdc.Progress{LastProcessedRecordTime: change.Time})
		},
		change: watchjournal.Change{
			StreamID:         c.streamID,
			Position:         change.Time.Bytes(),
			DeduplicationKey: c.streamID + ":" + change.Time.String(),
			Name:             name,
			Before:           beforeJSON,
			After:            afterJSON,
			At:               change.Time.Time().UTC(),
		},
	}
	if disposition == namespaceCDCSuppress {
		request.shouldPublish = func(context.Context) (bool, error) { return false, nil }
	} else if before == nil && after != nil && disposition != namespaceCDCPromotedAddition && c.additionReady != nil {
		request.shouldPublish = func(publishCtx context.Context) (bool, error) {
			return c.additionReady(publishCtx, after, afterLegacy)
		}
	} else if before != nil && after == nil && c.deletionReady != nil {
		request.shouldPublish = func(publishCtx context.Context) (bool, error) {
			return c.deletionReady(publishCtx, before, beforeCommitted)
		}
	}
	return c.sequencer.Submit(ctx, request)
}

type namespaceCDCDisposition uint8

const (
	namespaceCDCRegular namespaceCDCDisposition = iota
	namespaceCDCSuppress
	namespaceCDCPromotedAddition
)

// namespaceCDCDispositionFor turns the private creation commit marker into a
// two-record protocol. The initial false postimage is acknowledged without a
// public event. Its later false -> true update is emitted as ADDED because the
// marker is written only after the listing projection is durable. This avoids
// rereading mutable authoritative state between those two CDC records.
func namespaceCDCDispositionFor(before *datastore.Namespace, beforeCommitted bool, after *datastore.Namespace, afterCommitted bool) namespaceCDCDisposition {
	if after != nil && !afterCommitted {
		return namespaceCDCSuppress
	}
	if before != nil && !beforeCommitted && after != nil && afterCommitted {
		return namespaceCDCPromotedAddition
	}
	return namespaceCDCRegular
}

func (c *namespaceCDCConsumer) End() error {
	return c.sequencer.Unregister(c.streamID)
}

// Empty records actual CDC query progress. This keeps shared readiness fresh
// while the source is idle without allowing journal bookmarks to mask a stuck
// reader.
func (c *namespaceCDCConsumer) Empty(ctx context.Context, ackTime gocql.UUID) error {
	return c.sequencer.Submit(ctx, namespaceCDCSequenceRequest{
		cdcTime:      ackTime,
		streamID:     c.streamID,
		progressOnly: true,
		markProgress: func(markCtx context.Context) error {
			return c.reporter.MarkProgress(markCtx, scyllacdc.Progress{LastProcessedRecordTime: ackTime})
		},
	})
}

type namespaceCDCSequenceRequest struct {
	change        watchjournal.Change
	cdcTime       gocql.UUID
	streamID      string
	progressOnly  bool
	markProgress  func(context.Context) error
	shouldPublish func(context.Context) (bool, error)
	arrivalNumber uint64
}

type namespaceCDCSequenceMessage struct {
	kind       namespaceCDCSequenceMessageKind
	generation time.Time
	streamID   string
	streams    []string
	request    namespaceCDCSequenceRequest
	result     chan error
}

type namespaceCDCSequenceMessageKind uint8

const (
	namespaceCDCBeginGeneration namespaceCDCSequenceMessageKind = iota
	namespaceCDCRegister
	namespaceCDCUnregister
	namespaceCDCEnqueue
)

// namespaceCDCSequencer is the single journal-entry path shared by every CDC
// stream consumer. Consumers enqueue without waiting for materialization so
// each independent stream reader can advance its watermark. An event becomes
// publishable only when every active stream has reached or passed its CDC
// position; the sequencer then orders publishable work by cdc$time, stream ID,
// and arrival number before the journal linearization point.
type namespaceCDCSequencer struct {
	materializer     *watchjournal.Materializer
	lease            datastore.NamespaceWatchLease
	messages         chan namespaceCDCSequenceMessage
	done             chan struct{}
	publishedThrough gocql.UUID
	hasPublished     bool
	persistFrontier  func(context.Context, namespaceCDCPublishedBatch) error
}

type namespaceCDCPublishedBatch struct {
	Frontier   gocql.UUID
	Generation time.Time
	Progress   map[string]gocql.UUID
}

func newNamespaceCDCSequencer(materializer *watchjournal.Materializer, lease datastore.NamespaceWatchLease) *namespaceCDCSequencer {
	return &namespaceCDCSequencer{
		materializer: materializer,
		lease:        lease,
		messages:     make(chan namespaceCDCSequenceMessage, 256),
		done:         make(chan struct{}),
	}
}

func (s *namespaceCDCSequencer) BeginGeneration(ctx context.Context, generation time.Time, streams []string) error {
	if s == nil || generation.IsZero() || len(streams) == 0 {
		return fmt.Errorf("namespace CDC generation has no streams")
	}
	return s.send(ctx, namespaceCDCSequenceMessage{kind: namespaceCDCBeginGeneration, generation: generation, streams: streams})
}

func (s *namespaceCDCSequencer) Submit(ctx context.Context, request namespaceCDCSequenceRequest) error {
	if s == nil || s.materializer == nil || request.markProgress == nil {
		return fmt.Errorf("namespace CDC sequencer is not configured")
	}
	return s.send(ctx, namespaceCDCSequenceMessage{kind: namespaceCDCEnqueue, streamID: request.streamID, request: request})
}

func (s *namespaceCDCSequencer) Register(ctx context.Context, streamID string) error {
	if s == nil || streamID == "" {
		return fmt.Errorf("namespace CDC sequencer stream is not configured")
	}
	return s.send(ctx, namespaceCDCSequenceMessage{kind: namespaceCDCRegister, streamID: streamID})
}

func (s *namespaceCDCSequencer) Unregister(streamID string) error {
	if s == nil || streamID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return s.send(ctx, namespaceCDCSequenceMessage{kind: namespaceCDCUnregister, streamID: streamID})
}

func (s *namespaceCDCSequencer) send(ctx context.Context, message namespaceCDCSequenceMessage) error {
	message.result = make(chan error, 1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return context.Canceled
	case s.messages <- message:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		select {
		case err := <-message.result:
			return err
		default:
			return context.Canceled
		}
	case err := <-message.result:
		return err
	}
}

func (s *namespaceCDCSequencer) Run(ctx context.Context) error {
	defer close(s.done)
	active := make(map[string]int)
	watermarks := make(map[string]gocql.UUID)
	pending := make([]namespaceCDCSequenceRequest, 0, 256)
	var arrivalNumber uint64
	publishedThrough := s.publishedThrough
	hasPublished := s.hasPublished
	expected := make(map[string]struct{})
	registered := make(map[string]struct{})
	generationPrepared := false
	registrationComplete := false
	var currentGeneration time.Time
	ticker := time.NewTicker(namespaceCDCFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case message := <-s.messages:
			switch message.kind {
			case namespaceCDCBeginGeneration:
				if len(active) != 0 || len(pending) != 0 {
					err := fmt.Errorf("%w: CDC generation changed with %d active streams and %d pending records", datastore.ErrNamespaceWatchDiscontinuity, len(active), len(pending))
					message.result <- err
					return err
				}
				expected = make(map[string]struct{}, len(message.streams))
				registered = make(map[string]struct{}, len(message.streams))
				for _, streamID := range message.streams {
					if streamID == "" {
						err := fmt.Errorf("namespace CDC generation contains an empty stream ID")
						message.result <- err
						return err
					}
					expected[streamID] = struct{}{}
				}
				generationPrepared = true
				registrationComplete = false
				currentGeneration = message.generation
			case namespaceCDCRegister:
				if !generationPrepared {
					err := fmt.Errorf("namespace CDC stream %s registered before generation discovery completed", message.streamID)
					message.result <- err
					return err
				}
				if _, ok := expected[message.streamID]; !ok {
					err := fmt.Errorf("%w: unexpected CDC stream %s", datastore.ErrNamespaceWatchDiscontinuity, message.streamID)
					message.result <- err
					return err
				}
				if _, ok := registered[message.streamID]; ok {
					err := fmt.Errorf("%w: CDC stream %s registered more than once", datastore.ErrNamespaceWatchDiscontinuity, message.streamID)
					message.result <- err
					return err
				}
				active[message.streamID]++
				registered[message.streamID] = struct{}{}
				registrationComplete = len(registered) == len(expected)
			case namespaceCDCUnregister:
				if active[message.streamID] <= 1 {
					delete(active, message.streamID)
					delete(watermarks, message.streamID)
				} else {
					active[message.streamID]--
				}
			case namespaceCDCEnqueue:
				arrivalNumber++
				message.request.arrivalNumber = arrivalNumber
				if previous, ok := watermarks[message.streamID]; ok && scyllacdc.CompareTimeUUID(message.request.cdcTime, previous) < 0 {
					err := fmt.Errorf("%w: stream %s moved backward from %s to %s", datastore.ErrNamespaceWatchDiscontinuity, message.streamID, previous, message.request.cdcTime)
					message.result <- err
					return err
				}
				watermarks[message.streamID] = message.request.cdcTime
				if hasPublished && scyllacdc.CompareTimeUUID(message.request.cdcTime, publishedThrough) < 0 {
					if message.request.progressOnly {
						// A newly discovered stream can first report an empty query
						// window behind the global frontier. That proves the stream
						// contains no transition in this interval, so advance only
						// its durable progress without regressing global ordering.
						err := message.request.markProgress(ctx)
						message.result <- err
						if err != nil {
							return err
						}
						continue
					}
					err := fmt.Errorf("%w: stream %s registered behind published frontier %s with %s", datastore.ErrNamespaceWatchDiscontinuity, message.streamID, publishedThrough, message.request.cdcTime)
					message.result <- err
					return err
				}
				pending = append(pending, message.request)
				if len(pending) > namespaceCDCPendingLimit {
					err := fmt.Errorf("namespace CDC sequencer exceeded %d pending records", namespaceCDCPendingLimit)
					message.result <- err
					return err
				}
			}
			message.result <- nil
			if message.kind != namespaceCDCUnregister {
				// CDC consumers only wait for ordered admission into pending work.
				// Flush on the bounded ticker so burst arrivals share the store's
				// conditional event batch instead of degenerating to one LWT pair
				// per message. Stream shutdown is the exception: drain its pending
				// tail before the reader begins the next tablet generation.
				continue
			}
		case <-ticker.C:
		}

		if len(pending) == 0 || !registrationComplete {
			continue
		}
		frontier, ready := namespaceCDCFrontier(active, watermarks)
		if !ready {
			continue
		}
		sort.SliceStable(pending, func(i, j int) bool { return namespaceCDCSequenceLess(pending[i], pending[j]) })
		published := 0
		latestProgress := make(map[string]namespaceCDCSequenceRequest, len(active))
		changes := make([]watchjournal.Change, 0, len(pending))
		var batchFrontier gocql.UUID
		for published < len(pending) {
			request := pending[published]
			if len(active) > 0 && scyllacdc.CompareTimeUUID(request.cdcTime, frontier) > 0 {
				break
			}
			publish := true
			var err error
			if request.shouldPublish != nil {
				publish, err = request.shouldPublish(ctx)
			}
			if err != nil {
				return err
			}
			if publish && !request.progressOnly {
				changes = append(changes, request.change)
			}
			latestProgress[request.streamID] = request
			batchFrontier = request.cdcTime
			published++
		}
		if published > 0 {
			if _, err := s.materializer.MaterializeBatch(ctx, s.lease, changes); err != nil {
				return err
			}
			// Journal appends are idempotent by CDC position. Persist the global
			// published frontier before advancing any source checkpoint so a crash
			// cannot restore a frontier older than records whose source positions
			// have already moved past them. The same durable record retains the
			// latest position for each stream in this batch, allowing a new leader
			// to reconcile checkpoints after a crash in the following write loop.
			if s.persistFrontier != nil {
				progress := make(map[string]gocql.UUID, len(latestProgress))
				for streamID, request := range latestProgress {
					progress[cdcProgressKeyEncoded(currentGeneration, "namespaces_by_uid", streamID)] = request.cdcTime
				}
				if err := s.persistFrontier(ctx, namespaceCDCPublishedBatch{
					Frontier: batchFrontier, Generation: currentGeneration, Progress: progress,
				}); err != nil {
					return err
				}
			}
			for index := 0; index < published; index++ {
				request := pending[index]
				latest := latestProgress[request.streamID]
				if request.arrivalNumber != latest.arrivalNumber {
					continue
				}
				if err := request.markProgress(ctx); err != nil {
					return err
				}
			}
			publishedThrough = batchFrontier
			hasPublished = true
			pending = retainUnpublishedCDCRequests(pending, published)
		}
	}
}

func retainUnpublishedCDCRequests(pending []namespaceCDCSequenceRequest, published int) []namespaceCDCSequenceRequest {
	remaining := len(pending) - published
	copy(pending[:remaining], pending[published:])
	// The GC scans the backing array, not only the live slice prefix. Clear the
	// discarded requests so their Namespace payloads and callback closures do
	// not remain retained at the leader's peak pending capacity.
	clear(pending[remaining:])
	return pending[:remaining]
}

// namespaceCDCAdditionReady protects direct additions from an older binary
// that does not participate in the false -> true commit-marker protocol. New
// binaries publish the marker promotion itself as ADDED, since that write is
// already ordered after the durable list projection. For a legacy direct
// addition, a missing authoritative row denotes a rolled-back create and a
// still-present row without its projection is retried from durable progress.
func (s *scyllaDatastore) namespaceCDCAdditionReady(ctx context.Context, namespace *datastore.Namespace, legacy bool) (bool, error) {
	if namespace == nil {
		return false, nil
	}
	uid, err := gocql.ParseUUID(namespace.UID)
	if err != nil {
		return false, fmt.Errorf("parse Namespace CDC addition uid: %w", err)
	}
	exists, committed, err := s.namespaceWatchCommitState(ctx, uid)
	if err != nil {
		return false, err
	}
	if !exists {
		return namespaceCDCMissingAdditionReady(legacy)
	}
	if !committed {
		return false, fmt.Errorf("namespace CDC creation is not committed")
	}
	var indexRow struct {
		UID gocql.UUID `db:"uid"`
	}
	err = s.session.Query(
		"SELECT uid FROM namespaces_by_bucket WHERE bucket=? AND creation_timestamp=? AND uid=?",
		nil,
	).WithContext(ctx).Bind(namespaceBucket(namespace.CreationTimestamp), namespace.CreationTimestamp, uid).GetRelease(&indexRow)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, gocql.ErrNotFound) {
		return false, fmt.Errorf("read Namespace CDC listing projection: %w", err)
	}
	return false, fmt.Errorf("namespace CDC listing projection is not committed")
}

func namespaceCDCMissingAdditionReady(legacy bool) (bool, error) {
	if legacy {
		// Before watch_committed existed, a missing row could mean either a
		// successfully created Namespace that was quickly deleted or a failed
		// create that rolled back. Publishing or suppressing ADDED would each
		// invent one of those histories. Mixed-version rollout keeps the
		// materializer gated; fail closed if that rule is violated.
		return false, fmt.Errorf("legacy Namespace addition commit state is ambiguous after authoritative deletion")
	}
	return false, nil
}

func (s *scyllaDatastore) namespaceWatchCommitState(ctx context.Context, uid gocql.UUID) (bool, bool, error) {
	var row struct {
		Committed *bool `db:"watch_committed"`
	}
	err := s.session.Query("SELECT watch_committed FROM namespaces_by_uid WHERE uid=?", nil).
		WithContext(ctx).Bind(uid).GetRelease(&row)
	if errors.Is(err, gocql.ErrNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("read Namespace CDC commit marker: %w", err)
	}
	// A null marker denotes a Namespace created by a pre-050 binary before the
	// additive migration; those rows were already fully projected.
	return true, row.Committed == nil || *row.Committed, nil
}

// namespaceCDCDeletionReady prevents a DELETED event from crossing the public
// journal linearization point while the deleted Namespace is still visible in
// the list projection. Namespace deletion commits the authoritative row first,
// so a failed conditional delete never transiently removes the projection. A
// successful delete remains repairable by the later DELETED event until this
// check observes that projection cleanup has completed.
func (s *scyllaDatastore) namespaceCDCDeletionReady(ctx context.Context, namespace *datastore.Namespace, committed bool) (bool, error) {
	if namespace == nil {
		return false, nil
	}
	if !committed {
		return false, nil
	}
	uid, err := gocql.ParseUUID(namespace.UID)
	if err != nil {
		return false, fmt.Errorf("parse Namespace CDC deletion uid: %w", err)
	}
	var indexRow struct {
		UID gocql.UUID `db:"uid"`
	}
	err = s.session.Query(
		"SELECT uid FROM namespaces_by_bucket WHERE bucket=? AND creation_timestamp=? AND uid=?",
		nil,
	).WithContext(ctx).Bind(namespaceBucket(namespace.CreationTimestamp), namespace.CreationTimestamp, uid).GetRelease(&indexRow)
	if errors.Is(err, gocql.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read Namespace CDC listing projection: %w", err)
	}
	return false, fmt.Errorf("namespace CDC listing projection deletion is not committed")
}

func namespaceCDCSequenceLess(left, right namespaceCDCSequenceRequest) bool {
	if compared := scyllacdc.CompareTimeUUID(left.cdcTime, right.cdcTime); compared != 0 {
		return compared < 0
	}
	if left.streamID != right.streamID {
		return left.streamID < right.streamID
	}
	return left.arrivalNumber < right.arrivalNumber
}

func namespaceCDCFrontier(active map[string]int, watermarks map[string]gocql.UUID) (gocql.UUID, bool) {
	if len(active) == 0 {
		return gocql.UUID{}, true
	}
	var frontier gocql.UUID
	first := true
	for streamID := range active {
		watermark, ok := watermarks[streamID]
		if !ok {
			return gocql.UUID{}, false
		}
		if first || scyllacdc.CompareTimeUUID(watermark, frontier) < 0 {
			frontier = watermark
			first = false
		}
	}
	return frontier, true
}

func namespaceCDCPostimage(rows []*scyllacdc.ChangeRow) (*datastore.Namespace, bool, bool) {
	if len(rows) == 0 || rows[0] == nil {
		return nil, false, false
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
	scyllaRow.WatchCommitted = optionalCDCBool(row, "watch_committed")
	committed := scyllaRow.WatchCommitted == nil || *scyllaRow.WatchCommitted
	return fromNamespaceRow(scyllaRow), committed, scyllaRow.WatchCommitted == nil
}

func optionalCDCBool(row *scyllacdc.ChangeRow, column string) *bool {
	value, ok := row.GetValue(column)
	if !ok || value == nil {
		return nil
	}
	source := reflect.ValueOf(value)
	for source.Kind() == reflect.Pointer {
		if source.IsNil() {
			return nil
		}
		source = source.Elem()
	}
	if source.Kind() != reflect.Bool {
		return nil
	}
	result := source.Bool()
	return &result
}

func assignCDC(row *scyllacdc.ChangeRow, column string, destination any) {
	value, ok := row.GetValue(column)
	if !ok || value == nil {
		return
	}
	assignCDCValue(value, destination)
}

func assignCDCValue(value, destination any) {
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
	if target.Kind() == reflect.Pointer && source.Type().AssignableTo(target.Type().Elem()) {
		allocated := reflect.New(target.Type().Elem())
		allocated.Elem().Set(source)
		target.Set(allocated)
		return
	}
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

// namespaceCDCGenerationStreams reads the same generation metadata that
// scylla-cdc-go uses before it starts a generation. Supplying the complete set
// to the sequencer prevents any batch from publishing while another batch is
// still being initialized, regardless of cluster size or scheduler delay.
func (s *scyllaDatastore) namespaceCDCGenerationStreams(ctx context.Context, generation time.Time) ([]string, error) {
	consistency, err := s.namespaceCDCGenerationConsistency(ctx)
	if err != nil {
		return nil, err
	}
	keyspaceMetadata := make(map[string]interface{})
	err = s.session.Session.Query(
		"SELECT * FROM system_schema.scylla_keyspaces WHERE keyspace_name = ?",
		s.keyspace,
	).WithContext(ctx).Consistency(consistency).MapScan(keyspaceMetadata)
	if err != nil && !errors.Is(err, gocql.ErrNotFound) {
		return nil, fmt.Errorf("detect Namespace CDC tablet topology: %w", err)
	}

	streams := make([]scyllacdc.StreamID, 0)
	if keyspaceMetadata["initial_tablets"] != nil {
		iter := s.session.Session.Query(
			"SELECT stream_id FROM system.cdc_streams WHERE keyspace_name = ? AND table_name = ? AND timestamp = ? AND stream_state = ?",
			s.keyspace, "namespaces_by_uid", generation, 0,
		).WithContext(ctx).Consistency(consistency).Iter()
		var stream scyllacdc.StreamID
		for iter.Scan(&stream) {
			streams = append(streams, append(scyllacdc.StreamID(nil), stream...))
		}
		if err := iter.Close(); err != nil {
			return nil, fmt.Errorf("read Namespace CDC tablet generation %s: %w", generation, err)
		}
	} else {
		iter := s.session.Session.Query(
			"SELECT streams FROM system_distributed.cdc_streams_descriptions_v2 WHERE time = ?",
			generation,
		).WithContext(ctx).Consistency(consistency).Iter()
		var vnodeStreams []scyllacdc.StreamID
		for iter.Scan(&vnodeStreams) {
			for _, stream := range vnodeStreams {
				streams = append(streams, append(scyllacdc.StreamID(nil), stream...))
			}
		}
		if err := iter.Close(); err != nil {
			return nil, fmt.Errorf("read Namespace CDC generation %s: %w", generation, err)
		}
	}
	if len(streams) == 0 {
		return nil, fmt.Errorf("namespace CDC generation %s contains no streams", generation)
	}

	encoded := make([]string, 0, len(streams))
	seen := make(map[string]struct{}, len(streams))
	for _, stream := range streams {
		streamID := encodeCDCStreamID(stream)
		if _, ok := seen[streamID]; ok {
			continue
		}
		seen[streamID] = struct{}{}
		encoded = append(encoded, streamID)
	}
	return encoded, nil
}

func (s *scyllaDatastore) namespaceCDCGenerationConsistency(ctx context.Context) (gocql.Consistency, error) {
	var peers int
	if err := s.session.Session.Query("SELECT COUNT(*) FROM system.peers").
		WithContext(ctx).Consistency(gocql.One).Scan(&peers); err != nil {
		return 0, fmt.Errorf("count Namespace CDC topology peers: %w", err)
	}
	return namespaceCDCConsistencyForPeerCount(peers), nil
}

func namespaceCDCConsistencyForPeerCount(peers int) gocql.Consistency {
	if peers > 0 {
		return gocql.Quorum
	}
	return gocql.One
}

type namespaceCDCProgressManager struct {
	journal         datastore.NamespaceWatchJournal
	lease           datastore.NamespaceWatchLease
	observeProgress func(time.Time)
	beginGeneration func(context.Context, time.Time) error
	recovery        namespaceCDCPublishedBatch
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
	if m.beginGeneration != nil {
		if err := m.beginGeneration(ctx, generation); err != nil {
			return fmt.Errorf("prepare Namespace CDC generation: %w", err)
		}
	}
	return m.journal.SaveProgress(ctx, m.lease, datastore.NamespaceCDCProgress{
		StreamID:  namespaceCDCGenerationProgress,
		Position:  []byte(strconv.FormatInt(generation.UnixNano(), 10)),
		UpdatedAt: time.Now().UTC(),
	})
}

func (m *namespaceCDCProgressManager) GetProgress(ctx context.Context, generation time.Time, table string, streamID scyllacdc.StreamID) (scyllacdc.Progress, error) {
	key := cdcProgressKey(generation, table, streamID)
	progress, err := m.journal.LoadProgress(ctx, key)
	if errors.Is(err, datastore.ErrNotFound) {
		progress = datastore.NamespaceCDCProgress{}
	} else if err != nil {
		return scyllacdc.Progress{}, err
	}
	var position gocql.UUID
	if len(progress.Position) > 0 {
		position, err = gocql.UUIDFromBytes(progress.Position)
		if err != nil {
			return scyllacdc.Progress{}, fmt.Errorf("decode Namespace CDC progress: %w", err)
		}
	}
	recovered, ok := m.recovery.Progress[key]
	if ok && (position == (gocql.UUID{}) || scyllacdc.CompareTimeUUID(recovered, position) > 0) {
		progress = datastore.NamespaceCDCProgress{StreamID: key, Position: recovered.Bytes(), UpdatedAt: recovered.Time().UTC()}
		if err := m.journal.SaveProgress(ctx, m.lease, progress); err != nil {
			return scyllacdc.Progress{}, fmt.Errorf("reconcile Namespace CDC progress from published frontier: %w", err)
		}
		position = recovered
	}
	return scyllacdc.Progress{LastProcessedRecordTime: position}, nil
}

func (m *namespaceCDCProgressManager) SaveProgress(ctx context.Context, generation time.Time, table string, streamID scyllacdc.StreamID, progress scyllacdc.Progress) error {
	updatedAt := progress.LastProcessedRecordTime.Time().UTC()
	return m.journal.SaveProgress(ctx, m.lease, datastore.NamespaceCDCProgress{
		StreamID:  cdcProgressKey(generation, table, streamID),
		Position:  progress.LastProcessedRecordTime.Bytes(),
		UpdatedAt: updatedAt,
	})
}

func (m *namespaceCDCProgressManager) PublishedFrontier(ctx context.Context) (gocql.UUID, bool, error) {
	progress, err := m.journal.LoadProgress(ctx, namespaceCDCPublishedFrontierProgress)
	if errors.Is(err, datastore.ErrNotFound) {
		return gocql.UUID{}, false, nil
	}
	if err != nil {
		return gocql.UUID{}, false, fmt.Errorf("load Namespace CDC published frontier: %w", err)
	}
	frontier, err := gocql.UUIDFromBytes(progress.Position)
	if err == nil {
		return frontier, true, nil
	}
	var record struct {
		Version    int               `json:"version"`
		Frontier   string            `json:"frontier"`
		Generation int64             `json:"generation"`
		Progress   map[string]string `json:"progress"`
	}
	if jsonErr := json.Unmarshal(progress.Position, &record); jsonErr != nil {
		return gocql.UUID{}, false, fmt.Errorf("decode Namespace CDC published frontier manifest: %w", jsonErr)
	}
	if record.Version != 1 || record.Generation == 0 || len(record.Progress) == 0 {
		return gocql.UUID{}, false, fmt.Errorf("decode Namespace CDC published frontier manifest: invalid version, generation, or progress")
	}
	frontier, err = gocql.ParseUUID(record.Frontier)
	if err != nil {
		return gocql.UUID{}, false, fmt.Errorf("decode Namespace CDC published frontier UUID: %w", err)
	}
	m.recovery = namespaceCDCPublishedBatch{Frontier: frontier, Generation: time.Unix(0, record.Generation).UTC(), Progress: make(map[string]gocql.UUID, len(record.Progress))}
	for streamID, value := range record.Progress {
		position, parseErr := gocql.ParseUUID(value)
		if parseErr != nil {
			return gocql.UUID{}, false, fmt.Errorf("decode Namespace CDC published progress for %s: %w", streamID, parseErr)
		}
		m.recovery.Progress[streamID] = position
	}
	return frontier, true, nil
}

func (m *namespaceCDCProgressManager) SavePublishedFrontier(ctx context.Context, batch namespaceCDCPublishedBatch) error {
	if batch.Frontier == (gocql.UUID{}) || batch.Generation.IsZero() || len(batch.Progress) == 0 {
		return fmt.Errorf("encode Namespace CDC published frontier: frontier, generation, and progress are required")
	}
	record := struct {
		Version    int               `json:"version"`
		Frontier   string            `json:"frontier"`
		Generation int64             `json:"generation"`
		Progress   map[string]string `json:"progress"`
	}{Version: 1, Frontier: batch.Frontier.String(), Generation: batch.Generation.UnixNano(), Progress: make(map[string]string, len(batch.Progress))}
	for streamID, position := range batch.Progress {
		record.Progress[streamID] = position.String()
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode Namespace CDC published frontier: %w", err)
	}
	if len(encoded) > namespaceCDCPublishedManifestLimit {
		return fmt.Errorf("encode Namespace CDC published frontier: recovery manifest exceeds %d bytes", namespaceCDCPublishedManifestLimit)
	}
	frontierAt := batch.Frontier.Time().UTC()
	err = m.journal.SaveProgress(ctx, m.lease, datastore.NamespaceCDCProgress{
		StreamID:  namespaceCDCPublishedFrontierProgress,
		Position:  encoded,
		UpdatedAt: frontierAt,
	})
	if err == nil && m.observeProgress != nil {
		m.observeProgress(frontierAt)
	}
	return err
}

func cdcProgressKey(generation time.Time, table string, streamID scyllacdc.StreamID) string {
	return cdcProgressKeyEncoded(generation, table, encodeCDCStreamID(streamID))
}

func cdcProgressKeyEncoded(generation time.Time, table, streamID string) string {
	return strconv.FormatInt(generation.UnixNano(), 36) + ":" + table + ":" + streamID
}
