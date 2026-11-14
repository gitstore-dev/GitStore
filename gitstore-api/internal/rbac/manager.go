// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
)

const (
	rebuildWorkers = 4
	dirtyQueueSize = 1024
	watchPoll      = 100 * time.Millisecond
	maxSourceLag   = 5 * time.Second
)

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
	generation    uint64
	revision      uint64
	running       bool
	pending       bool
	blocked       bool
	installed     bool
	unavailableAt time.Time
	namespace     string
	retryAt       time.Time
	origin        SnapshotOrigin
	projection    ProjectionIdentity
	checkAt       time.Time
}

// Manager consumes the shared journal continuously while bounded workers scan.
// Only a complete snapshot caught up to its post-scan barrier is installable.
type Manager struct {
	mu          sync.Mutex
	deps        ManagerDeps
	work        map[string]*tierWork
	pending     int
	cursor      datastore.ResourceWatchCursor
	continuous  bool
	running     bool
	wake        chan struct{}
	changed     chan struct{}
	scratch     *MemoryBudget
	lookups     chan string
	lookupNames map[string]bool
	probeNext   bool
}

func NewManager(deps ManagerDeps) (*Manager, error) {
	if deps.Journal == nil || deps.ReadSnapshot == nil || deps.Prepare == nil ||
		deps.Invalidate == nil || deps.CheckProgress == nil || (deps.CheckOrigin == nil) != (deps.RecordOrigin == nil) {
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
	return &Manager{deps: deps, work: map[string]*tierWork{ClusterTierID: {pending: true}}, pending: 1, wake: make(chan struct{}, deps.Workers),
		changed: make(chan struct{}), scratch: deps.Scratch,
		lookups: make(chan string, deps.QueueSize), lookupNames: map[string]bool{}}, nil
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
	if work.installed {
		work.unavailableAt = m.deps.Clock.Now()
	}
	work.installed = false
	work.revision = max(work.revision, revision)
	m.deps.Invalidate(tier)
	if work.blocked {
		return ErrUnavailable
	}
	if !work.pending && m.pending < m.deps.QueueSize {
		work.pending = true
		m.pending++
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
	if err == nil && m.deps.ReadProgress != nil {
		err = m.deps.ReadProgress(readCtx, bounds)
	}
	if m.deps.ProgressAt != nil {
		if at := m.deps.ProgressAt(); !at.IsZero() {
			auth.ObserveRBACSourceAge(ctx, m.deps.Clock.Now().Sub(at))
		}
	}
	m.mu.Lock()
	if err != nil || bounds.Epoch == "" || m.deps.CheckProgress(m.deps.Clock.Now()) != nil {
		m.unavailableLocked()
		m.mu.Unlock()
		return
	}
	if !m.continuous || m.cursor.Epoch != bounds.Epoch ||
		m.cursor.Sequence > bounds.HighWater || bounds.Oldest > m.cursor.Sequence+1 {
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
		m.unavailableLocked()
		return
	}
	for _, event := range events {
		if event.Epoch != m.cursor.Epoch || event.Sequence != m.cursor.Sequence+1 ||
			!event.At.IsZero() && m.deps.Clock.Now().Sub(event.At) > m.deps.MaxStaleness {
			m.unavailableLocked()
			return
		}
		if err := m.eventLocked(event); err != nil {
			m.unavailableLocked()
			return
		}
		m.cursor.Sequence = event.Sequence
	}
	for tier, work := range m.work {
		if !work.blocked && !work.pending && !work.running && !work.retryAt.IsZero() && !m.deps.Clock.Now().Before(work.retryAt) && m.pending < m.deps.QueueSize {
			_ = m.requestLocked(tier, work.revision)
		}
	}
	m.signalLocked()
}

func (m *Manager) eventLocked(event datastore.ResourceWatchEvent) error {
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
				_ = m.requestLocked(tier, 0)
			}
		}
	case "RBACTier":
		var progress datastore.RBACTierProgress
		if json.Unmarshal(event.Payload, &progress) != nil || !ValidTier(progress.TierID) || progress.Revision == 0 {
			return ErrUnavailable
		}
		work := m.work[progress.TierID]
		if work != nil && progress.Revision <= work.revision {
			return nil
		}
		_ = m.requestLocked(progress.TierID, progress.Revision)
	case "Namespace":
		var namespace datastore.Namespace
		if json.Unmarshal(event.Payload, &namespace) != nil || namespace.UID == "" || namespace.Name == "" {
			return ErrUnavailable
		}
		target := namespace.UID
		if namespace.Name == "gitstore-system" {
			target = ClusterTierID
		}
		inactive := namespace.DeletionTimestamp != nil || event.Type == datastore.ResourceWatchDeleted
		for tier, work := range m.work {
			if tier != target && work.namespace == "" && event.Type == datastore.ResourceWatchAdded {
				_ = m.requestLocked(tier, 0)
			}
			if tier == target || event.Type == datastore.ResourceWatchAdded && work.namespace == namespace.Name {
				work.generation++
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
		if !inactive {
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
	for tier, work := range m.work {
		if work.running || work.blocked {
			continue
		}
		if rebuild == nil && work.pending && !now.Before(work.retryAt) {
			rebuildTier, rebuild = tier, work
		}
		if m.deps.CheckOrigin != nil && work.installed && !work.pending && !now.Before(work.checkAt) &&
			(probe == nil || work.checkAt.Before(probe.checkAt)) {
			probeTier, probe = tier, work
		}
	}
	// Alternate backlog work with the oldest due proof, so neither can starve.
	if probe != nil && (rebuild == nil || m.probeNext) {
		probe.running = true
		m.probeNext = false
		return probeTier, probe, probe.generation, true
	}
	if rebuild != nil {
		rebuild.pending, rebuild.running = false, true
		m.pending--
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
			started := m.deps.Clock.Now()
			var err error
			if probe {
				err = m.probe(ctx, tier, work, generation)
			} else {
				err = m.rebuild(ctx, tier, work, generation)
				auth.ObserveRBACRebuild(ctx, tier == ClusterTierID, err == nil, m.deps.Clock.Now().Sub(started))
			}
			m.mu.Lock()
			work.running = false
			if m.work[tier] == work {
				if err != nil && !work.blocked {
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
	if !m.continuous || m.work[tier] != work || work.generation != generation {
		return ErrUnavailable
	}
	if err == nil && ctx.Err() == nil && m.deps.CheckProgress(m.deps.Clock.Now()) == nil {
		err = m.deps.RecordOrigin(tier, projection, checkedAt)
		if err == nil {
			work.checkAt = m.deps.Clock.Now().Add(min(time.Second, m.deps.MaxStaleness/2))
			return nil
		}
	}
	_ = m.requestLocked(tier, 0)
	return ErrUnavailable
}

func (m *Manager) rebuild(ctx context.Context, tier string, work *tierWork, generation uint64) error {
	ctx, cancel := context.WithTimeout(ctx, m.deps.Timeout)
	defer cancel()
	reservation, err := m.scratch.Reserve(m.deps.ScratchBytes)
	if err != nil {
		return err
	}
	defer reservation.Close()
	snapshot, err := m.deps.ReadSnapshot(ctx, tier)
	if err != nil {
		return err
	}
	if snapshot.State.TierID != tier || snapshot.ModelVersion != ModelVersion || !snapshot.Origin.valid(tier) {
		return ErrUnavailable
	}
	bounds, err := m.deps.Journal.Bounds(ctx)
	if err != nil {
		return err
	}
	namespace := snapshot.Origin.Namespace
	if tier == ClusterTierID {
		namespace = ""
	}
	prepared, err := m.deps.Prepare(ctx, auth.AuthorizationTier{
		Namespace: namespace, NamespaceVersion: snapshot.Origin.NamespaceVersion, Active: snapshot.Origin.Active,
		State: snapshot.State, CatalogDigest: snapshot.CatalogDigest, BuiltinDigest: snapshot.BuiltinDigest,
		ContentDigest: snapshot.ContentDigest, Resources: snapshot.Resources,
	})
	if err != nil {
		return err
	}
	defer prepared.Close()
	snapshot.Resources = nil
	if m.deps.CheckOrigin != nil {
		checkedAt := m.deps.Clock.Now()
		checkCtx, cancel := context.WithTimeout(ctx, m.deps.MaxStaleness)
		err := m.deps.CheckOrigin(checkCtx, tier, snapshot.Origin)
		checkErr := checkCtx.Err()
		cancel()
		if err != nil || checkErr != nil {
			return ErrUnavailable
		}
		proof, ok := prepared.(interface{ SetOriginProgress(time.Time) error })
		if !ok || proof.SetOriginProgress(checkedAt) != nil {
			return ErrUnavailable
		}
	}
	for {
		m.mu.Lock()
		if ctx.Err() != nil || !m.continuous || m.cursor.Epoch != bounds.Epoch ||
			m.work[tier] != work || work.generation != generation || work.revision > snapshot.State.Revision ||
			m.deps.CheckProgress(m.deps.Clock.Now()) != nil {
			m.mu.Unlock()
			return ErrUnavailable
		}
		if m.cursor.Sequence >= bounds.HighWater {
			err := prepared.Install()
			if err == nil {
				work.namespace, work.revision = snapshot.Origin.Namespace, snapshot.State.Revision
				work.installed = true
				work.origin, work.projection = snapshot.Origin, snapshot.State.Projection
				work.checkAt = m.deps.Clock.Now().Add(min(time.Second, m.deps.MaxStaleness/2))
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
