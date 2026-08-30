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

const namespaceCDCRegistrationQuietPeriod = 250 * time.Millisecond

const namespaceCDCPendingLimit = 100000

// RunNamespaceCDC consumes only the authoritative namespaces_by_uid CDC log.
// The caller owns lease renewal and cancels ctx immediately on fencing loss.
func (s *scyllaDatastore) RunNamespaceCDC(ctx context.Context, materializer *watchjournal.Materializer, lease datastore.NamespaceWatchLease, changeAgeLimit, confidenceWindow time.Duration, ready func()) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	progress := &namespaceCDCProgressManager{journal: s, lease: lease}
	publishedThrough, hasPublished, err := progress.PublishedFrontier(runCtx)
	if err != nil {
		return err
	}
	sequencer := newNamespaceCDCSequencer(materializer, lease, namespaceCDCRegistrationQuietPeriod)
	sequencer.publishedThrough = publishedThrough
	sequencer.hasPublished = hasPublished
	sequencer.persistFrontier = progress.SavePublishedFrontier
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
	additionReady func(context.Context, *datastore.Namespace) (bool, error)
	deletionReady func(context.Context, *datastore.Namespace) (bool, error)
}

func (f *namespaceCDCConsumerFactory) CreateChangeConsumer(ctx context.Context, input scyllacdc.CreateChangeConsumerInput) (scyllacdc.ChangeConsumer, error) {
	if f.sequencer == nil || input.ProgressReporter == nil {
		return nil, fmt.Errorf("namespace CDC consumer is not configured")
	}
	streamID := encodeCDCStreamID(input.StreamID)
	if err := f.sequencer.Register(ctx, streamID); err != nil {
		return nil, err
	}
	return &namespaceCDCConsumer{
		sequencer:     f.sequencer,
		streamID:      streamID,
		reporter:      input.ProgressReporter,
		additionReady: f.additionReady,
		deletionReady: f.deletionReady,
	}, nil
}

type namespaceCDCConsumer struct {
	sequencer     *namespaceCDCSequencer
	streamID      string
	reporter      *scyllacdc.ProgressReporter
	additionReady func(context.Context, *datastore.Namespace) (bool, error)
	deletionReady func(context.Context, *datastore.Namespace) (bool, error)
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
	if before == nil && after != nil && c.additionReady != nil {
		request.shouldPublish = func(publishCtx context.Context) (bool, error) {
			return c.additionReady(publishCtx, after)
		}
	} else if before != nil && after == nil && c.deletionReady != nil {
		request.shouldPublish = func(publishCtx context.Context) (bool, error) {
			return c.deletionReady(publishCtx, before)
		}
	}
	return c.sequencer.Submit(ctx, request)
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
	kind     namespaceCDCSequenceMessageKind
	streamID string
	request  namespaceCDCSequenceRequest
	result   chan error
}

type namespaceCDCSequenceMessageKind uint8

const (
	namespaceCDCRegister namespaceCDCSequenceMessageKind = iota
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
	materializer            *watchjournal.Materializer
	lease                   datastore.NamespaceWatchLease
	registrationQuietPeriod time.Duration
	messages                chan namespaceCDCSequenceMessage
	done                    chan struct{}
	publishedThrough        gocql.UUID
	hasPublished            bool
	persistFrontier         func(context.Context, gocql.UUID) error
}

func newNamespaceCDCSequencer(materializer *watchjournal.Materializer, lease datastore.NamespaceWatchLease, quietPeriod time.Duration) *namespaceCDCSequencer {
	if quietPeriod <= 0 {
		quietPeriod = namespaceCDCRegistrationQuietPeriod
	}
	return &namespaceCDCSequencer{
		materializer:            materializer,
		lease:                   lease,
		registrationQuietPeriod: quietPeriod,
		messages:                make(chan namespaceCDCSequenceMessage, 256),
		done:                    make(chan struct{}),
	}
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
	registrationReadyAt := time.Time{}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case message := <-s.messages:
			switch message.kind {
			case namespaceCDCRegister:
				active[message.streamID]++
				registrationReadyAt = time.Now().Add(s.registrationQuietPeriod)
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
					err := fmt.Errorf("namespace CDC stream %s moved backward from %s to %s", message.streamID, previous, message.request.cdcTime)
					message.result <- err
					return err
				}
				watermarks[message.streamID] = message.request.cdcTime
				if hasPublished && scyllacdc.CompareTimeUUID(message.request.cdcTime, publishedThrough) < 0 {
					err := fmt.Errorf("namespace CDC stream %s registered behind published frontier %s with %s", message.streamID, publishedThrough, message.request.cdcTime)
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
		case <-ticker.C:
		}

		if len(pending) == 0 || (!registrationReadyAt.IsZero() && time.Now().Before(registrationReadyAt)) {
			continue
		}
		frontier, ready := namespaceCDCFrontier(active, watermarks)
		if !ready {
			continue
		}
		sort.SliceStable(pending, func(i, j int) bool { return namespaceCDCSequenceLess(pending[i], pending[j]) })
		published := 0
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
			if err == nil && publish && !request.progressOnly {
				_, err = s.materializer.Process(ctx, s.lease, request.change)
			}
			if err == nil && s.persistFrontier != nil {
				err = s.persistFrontier(ctx, request.cdcTime)
			}
			if err == nil {
				err = request.markProgress(ctx)
			}
			if err != nil {
				return err
			}
			publishedThrough = request.cdcTime
			hasPublished = true
			published++
		}
		if published > 0 {
			copy(pending, pending[published:])
			pending = pending[:len(pending)-published]
		}
	}
}

// namespaceCDCAdditionReady prevents an ADDED event from crossing the public
// journal linearization point before the list projection is visible. A missing
// authoritative row means the create rolled back and the staged CDC addition
// can be acknowledged without publication; a still-present authoritative row
// with no projection is retried by restarting from durable CDC progress.
func (s *scyllaDatastore) namespaceCDCAdditionReady(ctx context.Context, namespace *datastore.Namespace) (bool, error) {
	if namespace == nil {
		return false, nil
	}
	uid, err := gocql.ParseUUID(namespace.UID)
	if err != nil {
		return false, fmt.Errorf("parse Namespace CDC addition uid: %w", err)
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
	_, err = s.GetNamespace(ctx, namespace.UID)
	if errors.Is(err, datastore.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read Namespace CDC authoritative row: %w", err)
	}
	return false, fmt.Errorf("namespace CDC listing projection is not committed")
}

// namespaceCDCDeletionReady prevents a DELETED event from crossing the public
// journal linearization point while the deleted Namespace is still visible in
// the list projection. Namespace deletion commits the authoritative row first,
// so a failed conditional delete never transiently removes the projection. A
// successful delete remains repairable by the later DELETED event until this
// check observes that projection cleanup has completed.
func (s *scyllaDatastore) namespaceCDCDeletionReady(ctx context.Context, namespace *datastore.Namespace) (bool, error) {
	if namespace == nil {
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

func (m *namespaceCDCProgressManager) PublishedFrontier(ctx context.Context) (gocql.UUID, bool, error) {
	progress, err := m.journal.LoadProgress(ctx, namespaceCDCPublishedFrontierProgress)
	if errors.Is(err, datastore.ErrNotFound) {
		return gocql.UUID{}, false, nil
	}
	if err != nil {
		return gocql.UUID{}, false, fmt.Errorf("load Namespace CDC published frontier: %w", err)
	}
	frontier, err := gocql.UUIDFromBytes(progress.Position)
	if err != nil {
		return gocql.UUID{}, false, fmt.Errorf("decode Namespace CDC published frontier: %w", err)
	}
	return frontier, true, nil
}

func (m *namespaceCDCProgressManager) SavePublishedFrontier(ctx context.Context, frontier gocql.UUID) error {
	return m.journal.SaveProgress(ctx, m.lease, datastore.NamespaceCDCProgress{
		StreamID:  namespaceCDCPublishedFrontierProgress,
		Position:  frontier.Bytes(),
		UpdatedAt: time.Now().UTC(),
	})
}

func cdcProgressKey(generation time.Time, table string, streamID scyllacdc.StreamID) string {
	return strconv.FormatInt(generation.UnixNano(), 36) + ":" + table + ":" + encodeCDCStreamID(streamID)
}
