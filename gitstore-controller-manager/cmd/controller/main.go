// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/api"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/categorytaxonomy"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/config"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/health"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	namespacecontroller "github.com/gitstore-dev/gitstore/controller-manager/internal/namespace"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"go.uber.org/zap"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	log, err := manager.InitLogger(cfg.Log.Level, cfg.Log.Format)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to init logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync() //nolint:errcheck

	mgr := manager.New().WithLogger(log)

	// checkpointStore is shared across every kind's listwatch.Runner[T]
	// (spec 036): each Runner persists into its own file within this
	// directory (checkpoint.FilesystemStore, one file per kind).
	checkpointStore, err := checkpoint.NewFilesystemStore(cfg.Controller.CheckpointDir)
	if err != nil {
		log.Fatal("failed to init checkpoint store", zap.Error(err))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if _, err = registerNamespace(ctx, mgr, checkpointStore, cfg, log); err != nil {
		log.Fatal("failed to register Namespace reconciler", zap.Error(err))
	}

	var productRunnerMu sync.RWMutex
	var productRunner *listwatch.Runner[categorytaxonomy.Product]
	_, err = registerCategoryTaxonomy(ctx, mgr, checkpointStore, cfg, log, func(key types.WorkItemKey) {
		productRunnerMu.RLock()
		runner := productRunner
		productRunnerMu.RUnlock()
		if runner != nil {
			runner.MarkRelatedCompleted(key)
		}
	})
	if err != nil {
		log.Fatal("failed to register CategoryTaxonomy reconciler", zap.Error(err))
	}

	productRunnerMu.Lock()
	productRunner = registerProductWatch(ctx, mgr, checkpointStore, cfg, log)
	productRunnerMu.Unlock()

	addr := fmt.Sprintf(":%d", cfg.Controller.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: buildMux(mgr),
	}

	go func() {
		log.Info("HTTP server listening", zap.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("HTTP server error", zap.Error(err))
		}
	}()

	log.Info("controller-manager started", zap.String("apiURI", cfg.Controller.ApiURI))
	if err := mgr.Start(ctx); err != nil {
		log.Error("manager exited with error", zap.Error(err))
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("HTTP server shutdown error", zap.Error(err))
	}
	log.Info("controller-manager stopped")
}

// registerNamespace wires Namespace list/watch, repository provisioning,
// status writeback, and foreground-deletion reconciliation into mgr.
func registerNamespace(ctx context.Context, mgr *manager.Manager, checkpointStore *checkpoint.FilesystemStore, cfg *config.Config, log *zap.Logger) (*listwatch.Runner[namespacecontroller.Namespace], error) {
	client := graphqlclient.New(cfg.Controller.ApiURI, cfg.Controller.ApiToken)
	namespaceCache := cache.New[namespacecontroller.Namespace]()
	runner := &listwatch.Runner[namespacecontroller.Namespace]{
		Kind:        "Namespace",
		ListWatcher: listwatch.NewNamespaceListWatcher(client),
		Cache:       namespaceCache,
		Store:       checkpointStore,
		Enqueue:     mgr.Enqueue,
		KeyFunc: func(item namespacecontroller.Namespace) types.WorkItemKey {
			return types.WorkItemKey{Kind: "Namespace", Name: item.Name}
		},
		RevisionFunc: func(item namespacecontroller.Namespace) string {
			return item.ResourceVersion
		},
		FlushIntervalEvents: cfg.Controller.CheckpointFlushIntervalEvents,
		MaxBackoff:          cfg.Controller.MaxWatchBackoff,
		Log:                 log,
	}
	reconciler := namespacecontroller.NewReconciler(
		cache.AsReadOnly(namespaceCache),
		status.NewGraphQLResourceStatusClient(client),
		namespacecontroller.NewGraphQLRepositoryClient(client),
		namespacecontroller.NewGraphQLDeletionClient(client),
	)

	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:           "Namespace",
		Reconciler:     reconciler,
		Cache:          namespaceCache,
		OnSuccess:      runner.MarkCompleted,
		MaxAttempts:    cfg.Controller.DefaultMaxAttempts,
		StallThreshold: cfg.Controller.DefaultStallThreshold,
	}); err != nil {
		return nil, fmt.Errorf("register Namespace: %w", err)
	}

	go func() {
		if err := runner.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("Namespace runner exited with error", zap.Error(err))
		}
	}()
	return runner, nil
}

// buildMux returns the HTTP handler for the health/metrics and management surface.
func buildMux(mgr *manager.Manager) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /health", health.NewHandler(mgr))
	mux.Handle("GET /metrics", health.NewMetricsHandler(mgr))
	mux.HandleFunc("GET /controller/v1/poison/{kind}", api.ListPoisonHandler(mgr))
	mux.HandleFunc("POST /controller/v1/poison/{namespace}/{kind}/{name}/requeue", api.RequeuePoisonHandler(mgr))
	return mux
}

// registerCategoryTaxonomy wires the GraphQL client, the CategoryTaxonomy
// list-then-watch adapters (spec 040's client side, deferred to spec 039),
// and the CategoryTaxonomy reconciler into mgr, then starts its
// listwatch.Runner on a background goroutine. Per specs/039-category-taxonomy-reconciler/quickstart.md.
func registerCategoryTaxonomy(ctx context.Context, mgr *manager.Manager, checkpointStore *checkpoint.FilesystemStore, cfg *config.Config, log *zap.Logger, onRelatedSuccess func(types.WorkItemKey)) (*listwatch.Runner[categorytaxonomy.CategoryTaxonomy], error) {
	client := graphqlclient.New(cfg.Controller.ApiURI, cfg.Controller.ApiToken)
	listWatcher := listwatch.NewCategoryTaxonomyListWatcher(client)
	statusClient := status.NewGraphQLStatusClient(client)

	catCache := cache.New[categorytaxonomy.CategoryTaxonomy]()
	reconciler := categorytaxonomy.NewReconciler(
		cache.AsReadOnly(catCache),
		statusClient,
		categorytaxonomy.NewProductCounter(client),
		mgr.Enqueue,
		categorytaxonomy.NewGraphQLDeletionClient(client),
	)

	runner := &listwatch.Runner[categorytaxonomy.CategoryTaxonomy]{
		Kind:        "CategoryTaxonomy",
		ListWatcher: listWatcher,
		Cache:       catCache,
		Store:       checkpointStore,
		Enqueue:     mgr.Enqueue,
		KeyFunc: func(c categorytaxonomy.CategoryTaxonomy) types.WorkItemKey {
			return types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: c.Namespace, Name: c.Name}
		},
		RevisionFunc:        func(c categorytaxonomy.CategoryTaxonomy) string { return c.ResourceVersion },
		AcceptUpdate:        categorytaxonomy.AcceptWatchUpdate,
		ShouldEnqueueUpdate: categorytaxonomy.ShouldEnqueueWatchUpdate,
		FlushIntervalEvents: cfg.Controller.CheckpointFlushIntervalEvents,
		MaxBackoff:          cfg.Controller.MaxWatchBackoff,
		Log:                 log,
	}

	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:       "CategoryTaxonomy",
		Reconciler: reconciler,
		Cache:      catCache,
		OnSuccess: func(key types.WorkItemKey) {
			runner.MarkCompleted(key)
			if onRelatedSuccess != nil {
				onRelatedSuccess(key)
			}
		},
		MaxAttempts:    cfg.Controller.DefaultMaxAttempts,
		StallThreshold: cfg.Controller.DefaultStallThreshold,
	}); err != nil {
		return nil, fmt.Errorf("register CategoryTaxonomy: %w", err)
	}
	// A child's membership contributes to its parent's ChildCount. Requeue both
	// sides of a reparent operation, and the parent on add/delete, so counts do
	// not depend on an unrelated future category update.
	enqueueParent := func(namespace, name string) {
		if name != "" {
			_ = mgr.Enqueue(types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: namespace, Name: name})
		}
	}
	catCache.AddEventHandler(cache.EventHandler[categorytaxonomy.CategoryTaxonomy]{
		OnAdd: func(_ types.WorkItemKey, c categorytaxonomy.CategoryTaxonomy) {
			enqueueParent(c.Namespace, c.ParentRefName)
		},
		OnUpdate: func(_ types.WorkItemKey, old, current categorytaxonomy.CategoryTaxonomy) {
			if old.ParentRefName == current.ParentRefName {
				return
			}
			enqueueParent(old.Namespace, old.ParentRefName)
			enqueueParent(current.Namespace, current.ParentRefName)
		},
		OnDelete: func(_ types.WorkItemKey, c categorytaxonomy.CategoryTaxonomy) {
			enqueueParent(c.Namespace, c.ParentRefName)
		},
	})

	go func() {
		if err := runner.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("CategoryTaxonomy runner exited with error", zap.Error(err))
		}
	}()

	return runner, nil
}

// registerProductWatch wires a Product list-then-watch loop into a
// dedicated Runner[Product], without registering "Product" as a reconciled
// kind — Product is observed only to drive CategoryTaxonomy enqueues
// (research.md R1, spec 042). Its cache event handlers enqueue the
// already-registered "CategoryTaxonomy" kind via mgr.Enqueue whenever a
// Product's categoryRef appears, disappears, or changes.
func registerProductWatch(ctx context.Context, mgr *manager.Manager, checkpointStore *checkpoint.FilesystemStore, cfg *config.Config, log *zap.Logger) *listwatch.Runner[categorytaxonomy.Product] {
	client := graphqlclient.New(cfg.Controller.ApiURI, cfg.Controller.ApiToken)
	listWatcher := listwatch.NewProductListWatcher(client)

	productCache := cache.New[categorytaxonomy.Product]()
	var runner *listwatch.Runner[categorytaxonomy.Product]
	enqueueCategory := func(namespace, categoryName string) {
		if categoryName == "" {
			return
		}
		key := types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: namespace, Name: categoryName}
		runner.RememberRelatedReplay(key)
		_ = mgr.Enqueue(key)
	}

	runner = &listwatch.Runner[categorytaxonomy.Product]{
		Kind:        "Product",
		ListWatcher: listWatcher,
		Cache:       productCache,
		Store:       checkpointStore,
		Enqueue: func(types.WorkItemKey) error {
			// Product has no registered Reconciler/work queue of its own
			// (research.md R1) — the Runner's own replay-dedup Enqueue hook
			// is therefore a no-op; the real side effect is the cache event
			// handler above, driven by Cache.Set/Delete, not by this hook.
			return nil
		},
		ReplayEnqueue: func(key types.WorkItemKey) error {
			return mgr.Enqueue(key)
		},
		DisableReplay: true,
		KeyFunc: func(p categorytaxonomy.Product) types.WorkItemKey {
			return types.WorkItemKey{Kind: "Product", Namespace: p.Namespace, Name: p.Name}
		},
		RevisionFunc: func(p categorytaxonomy.Product) string { return p.ResourceVersion },
		// Persist each Product event before advancing the watch cursor so a
		// crash cannot lose an affected CategoryTaxonomy key.
		FlushIntervalEvents: 1,
		MaxBackoff:          cfg.Controller.MaxWatchBackoff,
		Log:                 log,
	}
	productCache.AddEventHandler(categorytaxonomy.NewProductCategoryEnqueueHandler(enqueueCategory))

	go func() {
		if err := runner.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("Product runner exited with error", zap.Error(err))
		}
	}()
	return runner
}
