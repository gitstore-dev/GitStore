// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
)

const (
	rebuildWorkers         = 4
	dirtyQueueSize         = 1024
	watchPoll              = 100 * time.Millisecond
	maxSourceLag           = 5 * time.Second
	maxPublicationCaptures = 3
)

var errWorkSuperseded = errors.Join(ErrUnavailable, errors.New("RBAC work generation superseded"))

type PreparedPublication interface {
	Install() error
	Close()
}

type ManagerDeps struct {
	Journal          datastore.ResourceWatchJournal
	ReadSnapshot     func(context.Context, string) (Snapshot, error)
	Prepare          func(context.Context, auth.AuthorizationTier) (PreparedPublication, error)
	Invalidate       func(string)
	CheckProgress    func(time.Time) error
	ReadProgress     func(context.Context, datastore.ResourceWatchBounds) error
	ProgressAt       func() time.Time
	CheckOrigin      func(context.Context, string, SnapshotOrigin) error
	CheckOrigins     func(context.Context, []OriginCheck) []error
	RecordOrigin     func(string, ProjectionIdentity, time.Time) error
	ResolveNamespace func(context.Context, string) (string, error)
	Clock            apiruntime.Clock
	Workers          int
	QueueSize        int
	Timeout          time.Duration
	MaxStaleness     time.Duration
	Scratch          *MemoryBudget
	ScratchBytes     int64
}

type tierWork struct {
	generation         uint64
	revisionGeneration uint64
	fence              uint64
	revision           uint64
	running            bool
	pending            bool
	blocked            bool
	installed          bool
	unavailableAt      time.Time
	namespace          string
	retryAt            time.Time
	origin             SnapshotOrigin
	projection         ProjectionIdentity
	originAt           time.Time
	checkAt            time.Time
}

// Manager consumes the shared journal continuously while bounded workers scan.
// Only a complete snapshot caught up to its post-scan barrier is installable.
type Manager struct {
	mu           sync.Mutex
	deps         ManagerDeps
	work         map[string]*tierWork
	pending      int
	pendingPeak  int
	cursor       datastore.ResourceWatchCursor
	continuous   bool
	running      bool
	wake         chan struct{}
	changed      chan struct{}
	scratch      *MemoryBudget
	lookups      chan string
	lookupNames  map[string]bool
	probeNext    bool
	rebuilding   int
	localRunning int
}

func NewManager(deps ManagerDeps) (*Manager, error) {
	if deps.Journal == nil || deps.ReadSnapshot == nil || deps.Prepare == nil ||
		deps.Invalidate == nil || deps.CheckProgress == nil || (deps.CheckOrigin == nil) != (deps.RecordOrigin == nil) ||
		deps.CheckOrigins != nil && deps.CheckOrigin == nil {
		return nil, ErrInvalid
	}
	if deps.Clock == nil {
		deps.Clock = apiruntime.SystemClock{}
	}
	if deps.Workers == 0 {
		deps.Workers = rebuildWorkers
	}
	if deps.QueueSize == 0 {
		deps.QueueSize = dirtyQueueSize
	}
	if deps.Timeout == 0 {
		deps.Timeout = 30 * time.Second
	}
	if deps.MaxStaleness == 0 {
		deps.MaxStaleness = maxSourceLag
	}
	if deps.ScratchBytes == 0 {
		deps.ScratchBytes = RebuildScratchLimit
	}
	if deps.Workers < 1 || deps.Workers > rebuildWorkers || deps.QueueSize < 1 || deps.QueueSize > dirtyQueueSize ||
		deps.Timeout <= 0 || deps.Timeout > 30*time.Second || deps.MaxStaleness <= 0 || deps.MaxStaleness > maxSourceLag ||
		deps.ScratchBytes < 1 || deps.ScratchBytes > RebuildScratchLimit {
		return nil, ErrInvalid
	}
	if deps.Scratch == nil {
		deps.Scratch = NewMemoryBudget(int64(deps.Workers) * deps.ScratchBytes)
	}
	return &Manager{deps: deps, work: map[string]*tierWork{ClusterTierID: {pending: true}}, pending: 1, pendingPeak: 1, wake: make(chan struct{}, deps.Workers),
		changed: make(chan struct{}), scratch: deps.Scratch,
		lookups: make(chan string, deps.QueueSize), lookupNames: map[string]bool{}}, nil
}

type DirtyQueueUsage struct{ Pending, Peak, Limit int }

func (m *Manager) QueueUsage() DirtyQueueUsage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return DirtyQueueUsage{m.pending, m.pendingPeak, m.deps.QueueSize}
}

func (m *Manager) signalLocked() {
	close(m.changed)
	m.changed = make(chan struct{})
	for range m.deps.Workers {
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
}

func (m *Manager) requestLocked(tier string, revision uint64) error {
	return m.queueLocked(tier, revision, false, false)
}

func (m *Manager) replaceLocked(tier string, revision uint64) error {
	return m.queueLocked(tier, revision, true, false)
}

func (m *Manager) queueLocked(tier string, revision uint64, replacing, revisionOnly bool) error {
	work := m.work[tier]
	if work == nil {
		if m.pending >= m.deps.QueueSize || len(m.work) >= m.deps.QueueSize+4096 {
			m.deps.Invalidate(tier)
			auth.ObserveRBACUnavailable(context.Background(), "queue", tier == ClusterTierID)
			return ErrUnavailable
		}
		work = &tierWork{}
		m.work[tier] = work
	}
	work.generation++
	if !revisionOnly {
		work.fence++
	}
	now := m.deps.Clock.Now()
	// A replacement does not renew the installed publication's origin proof.
	preserve := replacing && work.installed && m.deps.CheckOrigin != nil && !work.blocked &&
		!work.originAt.IsZero() && !now.Before(work.originAt) && now.Sub(work.originAt) <= m.deps.MaxStaleness &&
		(work.pending || m.pending < m.deps.QueueSize)
	if !preserve {
		if work.installed {
			work.unavailableAt = m.deps.Clock.Now()
		}
		work.installed = false
		m.deps.Invalidate(tier)
	}
	if revision > work.revision {
		work.retryAt = time.Time{}
		work.revision = revision
		work.revisionGeneration = work.generation
	}
	if work.blocked {
		return ErrUnavailable
	}
	if !work.pending && m.pending < m.deps.QueueSize {
		work.pending = true
		m.pending++
		m.pendingPeak = max(m.pendingPeak, m.pending)
	}
	if !work.pending {
		work.retryAt = m.deps.Clock.Now().Add(time.Second)
	}
	m.signalLocked()
	if !work.pending {
		return ErrUnavailable
	}
	return nil
}

func (m *Manager) RequestTier(tier string) error {
	if !ValidTier(tier) {
		return ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if work := m.work[tier]; work != nil && (work.installed || work.running || work.pending || !work.retryAt.IsZero()) {
		return nil
	}
	return m.requestLocked(tier, 0)
}

// RequestNamespace only queues a bounded lookup; it performs no source I/O.
func (m *Manager) RequestNamespace(name string) error {
	if !resourceNamePattern.MatchString(name) || m.deps.ResolveNamespace == nil {
		return ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, work := range m.work {
		if work.namespace == name && (work.installed || work.running || work.pending || !work.retryAt.IsZero()) {
			return nil
		}
	}
	if m.lookupNames[name] {
		return nil
	}
	if m.pending >= m.deps.QueueSize {
		auth.ObserveRBACUnavailable(context.Background(), "queue", false)
		return ErrUnavailable
	}
	select {
	case m.lookups <- name:
		m.lookupNames[name] = true
		m.pending++
		m.pendingPeak = max(m.pendingPeak, m.pending)
		return nil
	default:
		return ErrUnavailable
	}
}

func (m *Manager) unavailableLocked() {
	if m.continuous {
		m.continuous = false
		for tier, work := range m.work {
			work.generation++
			work.fence++
			if work.installed {
				work.unavailableAt = m.deps.Clock.Now()
			}
			work.installed = false
			m.deps.Invalidate(tier)
			auth.ObserveRBACUnavailable(context.Background(), "source", tier == ClusterTierID)
		}
		m.signalLocked()
	}
}

func (m *Manager) Run(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return ErrInvalid
	}
	m.running = true
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	for range m.deps.Workers {
		workers.Go(func() { m.worker(ctx) })
	}
	defer func() {
		cancel()
		workers.Wait()
		m.mu.Lock()
		defer m.mu.Unlock()
		m.unavailableLocked()
		m.running = false
	}()
	ticker := time.NewTicker(watchPoll)
	defer ticker.Stop()
	for {
		m.poll(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Manager) poll(ctx context.Context) {
	readCtx, cancel := context.WithTimeout(ctx, m.deps.MaxStaleness)
	defer cancel()
	bounds, err := m.deps.Journal.Bounds(readCtx)
	stage := "journal-bounds"
	if err == nil && m.deps.ReadProgress != nil {
		stage = "frontier-read"
		err = m.deps.ReadProgress(readCtx, bounds)
	}
	if m.deps.ProgressAt != nil {
		if at := m.deps.ProgressAt(); !at.IsZero() {
			auth.ObserveRBACSourceAge(ctx, m.deps.Clock.Now().Sub(at))
		}
	}
	m.mu.Lock()
	if err == nil {
		if bounds.Epoch == "" {
			stage, err = "journal-epoch", ErrUnavailable
		} else {
			stage = "frontier-stale"
			err = m.deps.CheckProgress(m.deps.Clock.Now())
		}
	}
	if err != nil {
		auth.ObserveRBACSourceFailure(ctx, stage)
		m.unavailableLocked()
		m.mu.Unlock()
		return
	}
	if !m.continuous || m.cursor.Epoch != bounds.Epoch ||
		m.cursor.Sequence > bounds.HighWater || bounds.Oldest > m.cursor.Sequence+1 {
		if m.continuous {
			stage = "journal-retention"
			if m.cursor.Epoch != bounds.Epoch {
				stage = "journal-epoch"
			} else if m.cursor.Sequence > bounds.HighWater {
				stage = "journal-high-water"
			}
			auth.ObserveRBACSourceFailure(ctx, stage)
		}
		m.unavailableLocked()
		m.cursor = datastore.ResourceWatchCursor{Epoch: bounds.Epoch, Sequence: bounds.HighWater}
		m.continuous = true
		_ = m.requestLocked(ClusterTierID, 0)
		for tier := range m.work {
			if tier != ClusterTierID {
				_ = m.requestLocked(tier, 0)
			}
		}
	}
	cursor := m.cursor
	m.mu.Unlock()
	events, err := m.deps.Journal.ReadAfter(readCtx, cursor, 256)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		auth.ObserveRBACSourceFailure(ctx, "journal-read")
		m.unavailableLocked()
		return
	}
	for _, event := range events {
		if event.Epoch != m.cursor.Epoch || event.Sequence != m.cursor.Sequence+1 {
			auth.ObserveRBACSourceFailure(ctx, "event-sequence")
			m.unavailableLocked()
			return
		}
		if !event.At.IsZero() && m.deps.Clock.Now().Sub(event.At) > m.deps.MaxStaleness {
			auth.ObserveRBACSourceFailure(ctx, "event-stale")
			m.unavailableLocked()
			return
		}
		if err := m.eventLocked(event); err != nil {
			auth.ObserveRBACSourceFailure(ctx, "event-payload")
			m.unavailableLocked()
			return
		}
		m.cursor.Sequence = event.Sequence
	}
	for tier, work := range m.work {
		if !work.blocked && !work.pending && !work.running && !work.retryAt.IsZero() && !m.deps.Clock.Now().Before(work.retryAt) && m.pending < m.deps.QueueSize {
			_ = m.replaceLocked(tier, work.revision)
		}
	}
	m.signalLocked()
}

func (m *Manager) eventLocked(event datastore.ResourceWatchEvent) error {
	if event.Type == datastore.ResourceWatchDeleted && len(event.Payload) == 0 &&
		(event.Kind == "Namespace" || event.Kind == "Repository") {
		if event.Name == "" || event.Kind == "Repository" && event.Namespace == "" {
			return ErrUnavailable
		}
		namespace := event.Name
		if event.Kind == "Repository" {
			if event.Name != "gitstore-system" {
				return nil
			}
			namespace = event.Namespace
		}
		// A name-only tombstone cannot identify a replaced UID. Fence matching
		// tiers and reread authority rather than blocking a new incarnation.
		for tier, work := range m.work {
			if work.namespace == namespace || work.namespace == "" {
				_ = m.requestLocked(tier, 0)
			}
		}
		return nil
	}
	switch event.Kind {
	case "Repository":
		var repository datastore.Repository
		if json.Unmarshal(event.Payload, &repository) != nil {
			return ErrUnavailable
		}
		datastore.NormalizeRepositoryContract(&repository)
		if repository.UID == "" || repository.Namespace == "" {
			return ErrUnavailable
		}
		if repository.Name != "gitstore-system" {
			return nil
		}
		for tier, work := range m.work {
			// A scan whose namespace is not known yet may have captured the
			// changed repository. Its generation must not remain publishable.
			if work.namespace == repository.Namespace || work.namespace == "" {
				if event.Type != datastore.ResourceWatchDeleted && repository.DeletionTimestamp == nil &&
					work.origin.RepositoryID == repository.UID && work.origin.GitRef == "refs/heads/"+repository.DefaultBranch {
					_ = m.replaceLocked(tier, 0)
				} else {
					_ = m.requestLocked(tier, 0)
				}
			}
		}
	case "RBACTier":
		var progress datastore.RBACTierProgress
		if json.Unmarshal(event.Payload, &progress) != nil || !ValidTier(progress.TierID) || progress.Revision == 0 {
			return ErrUnavailable
		}
		work := m.work[progress.TierID]
		if work == nil && progress.TierID != ClusterTierID {
			return nil
		}
		if event.Type != datastore.ResourceWatchDeleted && work != nil && progress.Revision <= work.revision {
			return nil
		}
		if event.Type == datastore.ResourceWatchDeleted {
			_ = m.requestLocked(progress.TierID, progress.Revision)
		} else {
			_ = m.queueLocked(progress.TierID, progress.Revision, true, true)
		}
	case "Namespace":
		var namespace datastore.Namespace
		if json.Unmarshal(event.Payload, &namespace) != nil || namespace.UID == "" || namespace.Name == "" {
			return ErrUnavailable
		}
		target := namespace.UID
		if namespace.Name == "gitstore-system" {
			target = ClusterTierID
		}
		requested := m.work[target] != nil || m.lookupNames[namespace.Name]
		inactive := namespace.DeletionTimestamp != nil || event.Type == datastore.ResourceWatchDeleted
		for tier, work := range m.work {
			if tier != target && tier != ClusterTierID && work.namespace == "" && event.Type == datastore.ResourceWatchAdded {
				_ = m.requestLocked(tier, 0)
			}
			if tier == target || event.Type == datastore.ResourceWatchAdded && work.namespace == namespace.Name {
				requested = true
				work.generation++
				work.fence++
				work.installed = false
				m.deps.Invalidate(tier)
				work.blocked = inactive
				if inactive {
					if work.pending {
						m.pending--
						work.pending = false
					}
					work.retryAt = time.Time{}
				}
				if tier != target {
					if work.pending {
						m.pending--
					}
					delete(m.work, tier)
				}
			}
		}
		if !inactive && requested {
			if work := m.work[target]; work != nil {
				work.blocked = false
			}
			_ = m.requestLocked(target, 0)
		}
	}
	return nil
}

func (m *Manager) take() (string, *tierWork, uint64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.continuous {
		return "", nil, 0, false
	}
	now := m.deps.Clock.Now()
	var rebuildTier, probeTier string
	var rebuild, probe *tierWork
	installed := false
	for tier, work := range m.work {
		installed = installed || work.installed
		if work.running || work.blocked {
			continue
		}
		if work.pending && !now.Before(work.retryAt) && (rebuild == nil || tier == ClusterTierID) {
			rebuildTier, rebuild = tier, work
		}
		if m.deps.CheckOrigin != nil && work.installed && !work.pending && work.retryAt.IsZero() && !now.Before(work.checkAt) &&
			(probe == nil || tier == ClusterTierID || probeTier != ClusterTierID && work.checkAt.Before(probe.checkAt)) {
			probeTier, probe = tier, work
		}
	}
	limit := m.deps.Workers
	// Reserve separate workers for Namespace proofs and shared root recovery.
	if installed && m.deps.CheckOrigin != nil && rebuildTier != ClusterTierID {
		limit = max(1, limit-2)
	}
	if m.rebuilding >= limit {
		rebuild = nil
	}
	// Local origin checks can block just like scans; reserve root capacity for both.
	if installed && m.deps.CheckOrigin != nil && m.localRunning >= max(1, m.deps.Workers-1) {
		if rebuildTier != ClusterTierID {
			rebuild = nil
		}
		if probeTier != ClusterTierID {
			probe = nil
		}
	}
	// Shared root work precedes local work; otherwise alternate scans and proofs.
	if probe != nil && (rebuild == nil || rebuildTier != ClusterTierID && (probeTier == ClusterTierID || m.probeNext)) {
		probe.running = true
		if probeTier != ClusterTierID {
			m.localRunning++
		}
		m.probeNext = false
		return probeTier, probe, probe.generation, true
	}
	if rebuild != nil {
		rebuild.pending, rebuild.running = false, true
		m.pending--
		m.rebuilding++
		if rebuildTier != ClusterTierID {
			m.localRunning++
		}
		m.probeNext = true
		return rebuildTier, rebuild, rebuild.generation, false
	}
	return "", nil, 0, false
}

func (m *Manager) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case name := <-m.lookups:
			lookupCtx, cancel := context.WithTimeout(ctx, m.deps.Timeout)
			tier, err := m.deps.ResolveNamespace(lookupCtx, name)
			cancel()
			m.mu.Lock()
			delete(m.lookupNames, name)
			m.pending--
			if err == nil && ValidTier(tier) {
				work := m.work[tier]
				if work == nil || !work.installed && !work.running && !work.pending && work.retryAt.IsZero() {
					_ = m.requestLocked(tier, 0)
					work = m.work[tier]
				}
				if work != nil && work.namespace == "" {
					work.namespace = name
				}
			}
			m.mu.Unlock()
		}
		for ctx.Err() == nil {
			tier, work, generation, probe := m.take()
			if work == nil {
				break
			}
			if probe && tier != ClusterTierID && m.deps.CheckOrigins != nil {
				m.probeBatch(ctx, tier, work, generation)
				continue
			}
			started := m.deps.Clock.Now()
			var err error
			if probe {
				err = m.probe(ctx, tier, work, generation)
			} else {
				err = m.rebuild(ctx, tier, work, generation)
				auth.ObserveRBACRebuild(ctx, tier == ClusterTierID, err == nil, m.deps.Clock.Now().Sub(started))
			}
			m.mu.Lock()
			if !probe {
				m.rebuilding--
			}
			if tier != ClusterTierID {
				m.localRunning--
			}
			work.running = false
			if m.work[tier] == work {
				// A failed probe's own retry is not newer durable work.
				newerPending := work.pending && (!probe && work.generation != generation || work.revisionGeneration > generation)
				if err != nil && !errors.Is(err, errWorkSuperseded) && !work.blocked && !newerPending {
					work.retryAt = m.deps.Clock.Now().Add(time.Second)
				} else {
					work.retryAt = time.Time{}
				}
			}
			m.signalLocked()
			m.mu.Unlock()
		}
	}
}

func (m *Manager) originCheckInterval(tier string) time.Duration {
	interval := m.deps.MaxStaleness / 2
	if tier == ClusterTierID {
		// Shared-root changes need more replacement headroom than local tiers.
		interval = min(interval, 2*watchPoll)
	}
	return interval
}

func (m *Manager) probe(ctx context.Context, tier string, work *tierWork, generation uint64) error {
	m.mu.Lock()
	identity := work.origin
	projection := work.projection
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, m.deps.MaxStaleness)
	defer cancel()
	checkedAt := m.deps.Clock.Now()
	err := m.deps.CheckOrigin(ctx, tier, identity)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.finishProbeLocked(ctx, tier, work, generation, projection, checkedAt, err)
}

func (m *Manager) finishProbeLocked(ctx context.Context, tier string, work *tierWork, generation uint64,
	projection ProjectionIdentity, checkedAt time.Time, err error,
) error {
	if !m.continuous || m.work[tier] != work {
		return ErrUnavailable
	}
	if work.generation != generation {
		if ctx.Err() == nil && (err == nil || errors.Is(err, errOriginAdvanced)) {
			return errWorkSuperseded
		}
		return ErrUnavailable
	}
	if ctx.Err() == nil && m.deps.CheckProgress(m.deps.Clock.Now()) == nil {
		if errors.Is(err, errOriginAdvanced) {
			return m.replaceLocked(tier, 0)
		}
		now := m.deps.Clock.Now()
		if errors.Is(err, ErrOriginRefBusy) && work.installed && !work.pending && !work.blocked &&
			!work.originAt.IsZero() && !now.Before(work.originAt) && now.Sub(work.originAt) <= m.deps.MaxStaleness {
			// Contention supplies no new proof and cannot move its original expiry.
			work.checkAt = now.Add(100 * time.Millisecond)
			return nil
		}
		if err == nil {
			err = m.deps.RecordOrigin(tier, projection, checkedAt)
			if err == nil {
				if checkedAt.After(work.originAt) {
					work.originAt = checkedAt
				}
				work.checkAt = work.originAt.Add(m.originCheckInterval(tier))
				return nil
			}
		}
	}
	_ = m.requestLocked(tier, 0)
	return ErrUnavailable
}

type scheduledOriginCheck struct {
	tier       string
	work       *tierWork
	generation uint64
	origin     SnapshotOrigin
	projection ProjectionIdentity
}

func (m *Manager) probeBatch(ctx context.Context, tier string, work *tierWork, generation uint64) {
	m.mu.Lock()
	batch := []scheduledOriginCheck{{tier, work, generation, work.origin, work.projection}}
	now := m.deps.Clock.Now()
	var due []scheduledOriginCheck
	running := 0
	for candidate, next := range m.work {
		if candidate != ClusterTierID && next.running {
			running++
		}
		if candidate != ClusterTierID && !next.running && !next.blocked && next.installed &&
			!next.pending && next.retryAt.IsZero() && !now.Before(next.checkAt) {
			due = append(due, scheduledOriginCheck{candidate, next, next.generation, next.origin, next.projection})
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].work.checkAt.Before(due[j].work.checkAt) })
	batchSize := MaxOriginChecks / max(2, m.deps.Workers)
	batchLimit := batchSize * max(1, m.deps.Workers-1)
	// Busy Namespace workers cannot claim more batches until their current work finishes.
	if m.localRunning >= max(1, m.deps.Workers-1) {
		batchSize = batchLimit
	}
	// Reserve one worker's share of the 32-ref bound for recovery/validation.
	for _, next := range due {
		if running >= batchLimit || len(batch) >= batchSize {
			break
		}
		next.work.running = true
		running++
		batch = append(batch, next)
	}
	m.mu.Unlock()
	checks := make([]OriginCheck, len(batch))
	for i, next := range batch {
		checks[i] = OriginCheck{Tier: next.tier, Expected: next.origin}
	}
	ctx, cancel := context.WithTimeout(ctx, m.deps.MaxStaleness)
	defer cancel()
	checkedAt := m.deps.Clock.Now()
	results := m.deps.CheckOrigins(ctx, checks)
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, next := range batch {
		err := ErrUnavailable
		if len(results) == len(batch) {
			err = results[i]
		}
		err = m.finishProbeLocked(ctx, next.tier, next.work, next.generation, next.projection, checkedAt, err)
		next.work.running = false
		if m.work[next.tier] == next.work {
			newerPending := next.work.pending && next.work.revisionGeneration > next.generation
			if err != nil && !errors.Is(err, errWorkSuperseded) && !next.work.blocked && !newerPending {
				next.work.retryAt = m.deps.Clock.Now().Add(time.Second)
			} else {
				next.work.retryAt = time.Time{}
			}
		}
	}
	m.localRunning--
	m.signalLocked()
}

func (m *Manager) rebuild(ctx context.Context, tier string, work *tierWork, generation uint64) (err error) {
	ctx, cancel := context.WithTimeout(ctx, m.deps.Timeout)
	defer cancel()
	m.mu.Lock()
	if m.work[tier] != work || work.generation != generation {
		m.mu.Unlock()
		return errWorkSuperseded
	}
	fence := work.fence
	m.mu.Unlock()
	stage := "rebuild-scratch"
	defer func() {
		if err != nil && !errors.Is(err, errWorkSuperseded) {
			auth.ObserveRBACSourceFailure(ctx, stage)
		}
	}()
	reservation, err := m.scratch.Reserve(m.deps.ScratchBytes)
	if err != nil {
		return err
	}
	defer reservation.Close()
	stage = "rebuild-snapshot"
	snapshot, err := m.deps.ReadSnapshot(ctx, tier)
	if err != nil {
		if errors.Is(err, ErrOriginRefBusy) {
			stage = "rebuild-origin-busy"
		}
		return err
	}
	if snapshot.State.TierID != tier || snapshot.ModelVersion != ModelVersion || !snapshot.Origin.valid(tier) {
		return ErrUnavailable
	}
	stage = "rebuild-journal"
	bounds, err := m.deps.Journal.Bounds(ctx)
	if err != nil {
		return err
	}
	stage = "rebuild-prepare"
	prepared, err := m.deps.Prepare(ctx, publicationTier(snapshot))
	if err != nil {
		return err
	}
	defer func() { prepared.Close() }()
	snapshot.Resources = nil
	var originAt time.Time
	if m.deps.CheckOrigin != nil {
		stage = "rebuild-origin"
		originAt, err = m.checkPreparedOrigin(ctx, tier, snapshot.Origin, prepared)
		if err != nil {
			if errors.Is(err, ErrOriginRefBusy) {
				stage = "rebuild-origin-busy"
			}
			return ErrUnavailable
		}
	}
	stage = "rebuild-continuity"
	captures := 1
	for {
		m.mu.Lock()
		if ctx.Err() != nil || !m.continuous || m.cursor.Epoch != bounds.Epoch ||
			m.work[tier] != work ||
			m.deps.CheckProgress(m.deps.Clock.Now()) != nil {
			m.mu.Unlock()
			return ErrUnavailable
		}
		if work.fence != fence {
			m.mu.Unlock()
			return errWorkSuperseded
		}
		if work.revision > snapshot.State.Revision {
			superseded := work.generation != generation
			rebaser, canRebase := prepared.(interface {
				Rebase(context.Context, auth.AuthorizationTier) error
			})
			m.mu.Unlock()
			if captures < maxPublicationCaptures && canRebase && m.deps.CheckOrigin != nil {
				// Catch up a complete source, not a revision number. Changed policy
				// requires new preparation and proof; every fence is checked again.
				stage = "rebuild-catch-up"
				captures++
				latest, err := m.deps.ReadSnapshot(ctx, tier)
				if err != nil {
					return err
				}
				if latest.ModelVersion != ModelVersion || latest.State.TierID != tier ||
					latest.Origin != snapshot.Origin || latest.State.Projection != snapshot.State.Projection ||
					latest.State.ProjectionPending || latest.State.NamespaceRetired ||
					latest.State.Revision <= snapshot.State.Revision {
					return errWorkSuperseded
				}
				latestBounds, err := m.deps.Journal.Bounds(ctx)
				if err != nil {
					return err
				}
				if latestBounds.Epoch != bounds.Epoch {
					return ErrUnavailable
				}
				if err := rebaser.Rebase(ctx, publicationTier(latest)); err != nil {
					stage = "rebuild-prepare"
					prepared.Close()
					next, err := m.deps.Prepare(ctx, publicationTier(latest))
					if err != nil {
						return err
					}
					prepared = next
					stage = "rebuild-origin"
					originAt, err = m.checkPreparedOrigin(ctx, tier, latest.Origin, prepared)
					if err != nil {
						if errors.Is(err, ErrOriginRefBusy) {
							stage = "rebuild-origin-busy"
						}
						return ErrUnavailable
					}
				}
				snapshot, bounds = latest, latestBounds
				snapshot.Resources = nil
				stage = "rebuild-continuity"
				continue
			}
			if superseded {
				return errWorkSuperseded
			}
			return ErrUnavailable
		}
		if m.cursor.Sequence >= bounds.HighWater {
			now := m.deps.Clock.Now()
			if !originAt.IsZero() && (now.Before(originAt) || now.Sub(originAt) > m.deps.MaxStaleness) {
				m.mu.Unlock()
				return ErrUnavailable
			}
			stage = "rebuild-install"
			err := prepared.Install()
			if err == nil {
				// The complete scan may already cover progress received while it ran.
				if work.pending {
					work.pending = false
					m.pending--
				}
				work.namespace, work.revision = snapshot.Origin.Namespace, snapshot.State.Revision
				work.installed = true
				work.origin, work.projection = snapshot.Origin, snapshot.State.Projection
				work.originAt = originAt
				work.checkAt = originAt.Add(m.originCheckInterval(tier))
				if !work.unavailableAt.IsZero() {
					auth.ObserveRBACRecovery(ctx, tier == ClusterTierID, m.deps.Clock.Now().Sub(work.unavailableAt))
					work.unavailableAt = time.Time{}
				}
			}
			m.mu.Unlock()
			return err
		}
		changed := m.changed
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return errors.Join(ErrUnavailable, ctx.Err())
		case <-changed:
		}
	}
}

func publicationTier(snapshot Snapshot) auth.AuthorizationTier {
	namespace := snapshot.Origin.Namespace
	if snapshot.State.TierID == ClusterTierID {
		namespace = ""
	}

	return auth.AuthorizationTier{
		Namespace: namespace, NamespaceVersion: snapshot.Origin.NamespaceVersion, Active: snapshot.Origin.Active,
		State: snapshot.State, CatalogDigest: snapshot.CatalogDigest, BuiltinDigest: snapshot.BuiltinDigest,
		ContentDigest: snapshot.ContentDigest, Resources: snapshot.Resources,
	}
}

func (m *Manager) checkPreparedOrigin(ctx context.Context, tier string, origin SnapshotOrigin, prepared PreparedPublication) (time.Time, error) {
	at := m.deps.Clock.Now()
	checkCtx, cancel := context.WithTimeout(ctx, m.deps.MaxStaleness)
	err := m.deps.CheckOrigin(checkCtx, tier, origin)
	checkErr := checkCtx.Err()
	cancel()
	if err != nil {
		return time.Time{}, err
	}
	if checkErr != nil {
		return time.Time{}, checkErr
	}
	proof, ok := prepared.(interface{ SetOriginProgress(time.Time) error })
	if !ok {
		return time.Time{}, ErrUnavailable
	}
	if err := proof.SetOriginProgress(at); err != nil {
		return time.Time{}, err
	}
	return at, nil
}
