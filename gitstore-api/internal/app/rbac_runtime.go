// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/opa"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
)

type rbacRuntime struct {
	provider      *opa.Provider
	manager       *rbac.Manager
	cancel        context.CancelFunc
	done          chan struct{}
	once          sync.Once
	reader        rbac.SnapshotReader
	scratch       *rbac.MemoryBudget
	timeout       time.Duration
	ctx           context.Context
	initializing  atomic.Bool
	bootstrapDone chan struct{}
}

type rbacRuntimeOptions struct {
	provider     *opa.Provider
	prepare      func(context.Context) error
	initializing bool
}

func newCanonicalRBACRuntime(ctx context.Context, store datastore.Datastore, journal datastore.ResourceWatchJournal, git rbac.OriginGitReader,
	limits config.OPAConfig, repair func(context.Context, string) error, options ...rbacRuntimeOptions) (*rbacRuntime, error) {
	if err := limits.Validate("opa"); err != nil {
		return nil, err
	}
	capable, ok := store.(datastore.RBACCapable)
	if !ok || capable.RBACStore() == nil || git == nil || repair == nil {
		return nil, rbac.ErrUnavailable
	}
	builtins, err := rbac.ReleaseBuiltins()
	if err != nil {
		return nil, err
	}
	source := rbac.OriginReader{Store: store, Git: git, MaxBytes: limits.RebuildMemoryBytes, Digests: new(rbac.OriginDigestCache)}
	reader := rbac.SnapshotReader{Store: capable.RBACStore(), ReadOrigin: source.ReadOrigin, CheckOrigin: source.CheckOrigin,
		CatalogDigest: auth.AuthorizationCatalogDigest(), BuiltinDigest: builtins.Digest()}
	return newRBACRuntime(ctx, store, journal, reader, limits, source.CheckOrigin, repair, options...)
}

// Construction alone does not activate this provider in the serving registry.
func newRBACRuntime(ctx context.Context, store datastore.Datastore, journal datastore.ResourceWatchJournal, reader rbac.SnapshotReader,
	limits config.OPAConfig,
	checkOrigin func(context.Context, string, rbac.SnapshotOrigin) error, repair func(context.Context, string) error, options ...rbacRuntimeOptions) (*rbacRuntime, error) {
	if store == nil || journal == nil || reader.Store == nil || reader.ReadOrigin == nil {
		return nil, rbac.ErrUnavailable
	}
	if err := limits.Validate("opa"); err != nil {
		return nil, err
	}
	reader.Retries, reader.Timeout, reader.MaxBytes = limits.SnapshotRetries, limits.ValidationTimeout, limits.RebuildMemoryBytes
	scratch := rbac.NewMemoryBudget(int64(max(2, limits.RebuildWorkers)) * limits.RebuildMemoryBytes)
	var option rbacRuntimeOptions
	if len(options) > 0 {
		option = options[0]
	}
	provider := option.provider
	if provider == nil {
		var err error
		provider, err = opa.NewProviderWithMemoryLimit(ctx, limits.SnapshotMemoryBytes)
		if err != nil {
			return nil, err
		}
	}
	progress := watchjournal.NewReadiness(limits.MaxStaleness)
	runtime := &rbacRuntime{provider: provider, reader: reader, scratch: scratch, timeout: limits.ValidationTimeout}
	runtime.initializing.Store(option.initializing)
	provider.SetProgressCheck(func(now time.Time) error {
		select {
		case <-runtime.done:
			return rbac.ErrUnavailable
		default:
		}
		if runtime.initializing.Load() {
			return rbac.ErrUnavailable
		}
		return progress.Check(now)
	})
	if checkOrigin != nil {
		if err := provider.RequireOriginProgress(limits.MaxStaleness); err != nil {
			provider.Close()
			return nil, err
		}
	}
	var recordOrigin func(string, rbac.ProjectionIdentity, time.Time) error
	if checkOrigin != nil {
		recordOrigin = provider.RecordOriginProgress
	}
	manager, err := rbac.NewManager(rbac.ManagerDeps{
		Journal: journal, Invalidate: provider.InvalidateTier,
		ReadSnapshot: func(ctx context.Context, tier string) (rbac.Snapshot, error) {
			snapshot, err := reader.Read(ctx, tier)
			needsRepair := errors.Is(err, rbac.ErrSnapshotProjectionMismatch) ||
				err == nil && snapshot.State.ProjectionPending
			if !needsRepair || repair == nil {
				return snapshot, err
			}
			if ctx.Err() != nil {
				return rbac.Snapshot{}, ctx.Err()
			}
			if err := repair(ctx, tier); err != nil {
				return rbac.Snapshot{}, err
			}
			return reader.Read(ctx, tier)
		},
		CheckOrigin: checkOrigin, RecordOrigin: recordOrigin,
		Workers: limits.RebuildWorkers, QueueSize: limits.DirtyQueueCapacity,
		Timeout: limits.ValidationTimeout, MaxStaleness: limits.MaxStaleness,
		Scratch: scratch, ScratchBytes: limits.RebuildMemoryBytes,
		Prepare: func(ctx context.Context, tier auth.AuthorizationTier) (rbac.PreparedPublication, error) {
			return provider.PrepareTier(ctx, tier)
		},
		ReadProgress: func(ctx context.Context, bounds datastore.ResourceWatchBounds) error {
			return watchjournal.RefreshSourceReadiness(ctx, journal, bounds, progress,
				[]string{"Namespace", "Repository", "RBAC", "ServiceAccount"})
		},
		CheckProgress: progress.Check, ProgressAt: progress.LastProgressAt,
		ResolveNamespace: func(ctx context.Context, name string) (string, error) {
			namespace, err := store.GetNamespaceByName(ctx, name)
			if err != nil || namespace == nil || namespace.DeletionTimestamp != nil {
				return "", rbac.ErrUnavailable
			}
			if name == "gitstore-system" {
				return rbac.ClusterTierID, nil
			}
			return namespace.UID, nil
		},
	})
	if err != nil {
		provider.Close()
		return nil, err
	}
	provider.SetNamespaceLoader(func(name string) { _ = manager.RequestNamespace(name) })
	runCtx, cancel := context.WithCancel(ctx)
	runtime.manager, runtime.cancel, runtime.done, runtime.ctx = manager, cancel, make(chan struct{}), runCtx
	go func() {
		defer close(runtime.done)
		_ = manager.Run(runCtx)
	}()
	return runtime, nil
}

func (r *rbacRuntime) validationCapture(registry *auth.ProviderRegistry) rbac.ValidationCapture {
	return rbac.ValidationCapture{Reader: r.reader, Registry: registry, Scratch: r.scratch, Timeout: r.timeout}
}

func (r *rbacRuntime) Ready(context.Context) error {
	readiness, supported := r.provider.AuthorizationReadiness(rbac.ClusterTierID)
	if !supported || !readiness.Ready {
		return rbac.ErrUnavailable
	}
	return nil
}

func (r *rbacRuntime) Shutdown() {
	r.once.Do(func() {
		r.cancel()
		<-r.done
		if r.bootstrapDone != nil {
			<-r.bootstrapDone
		}
		r.provider.Close()
	})
}

func waitRBACReady(ctx context.Context, runtime *rbacRuntime) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if runtime.Ready(ctx) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.Join(rbac.ErrUnavailable, ctx.Err())
		case <-runtime.done:
			return rbac.ErrUnavailable
		case <-ticker.C:
		}
	}
}

func rbacServingHandler(next http.Handler, ready func(context.Context) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health", "/ready", "/metrics":
		default:
			if ready(request.Context()) != nil {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "authorization is unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		next.ServeHTTP(w, request)
	})
}
