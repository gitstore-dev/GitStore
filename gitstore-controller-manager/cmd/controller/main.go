// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
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
	productcontroller "github.com/gitstore-dev/gitstore/controller-manager/internal/product"
	repositorycontroller "github.com/gitstore-dev/gitstore/controller-manager/internal/repository"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/secret"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/version"
	"go.uber.org/zap"
)

func main() {
	configFiles, err := parseConfigFiles(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse arguments: %v\n", err)
		os.Exit(2)
	}
	var cfg *config.Config
	if len(configFiles) == 0 {
		cfg, err = config.Load()
	} else {
		cfg, err = config.LoadFromFiles(configFiles)
	}
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	credentials, err := buildCredentialSource(ctx, cfg, log)
	if err != nil {
		log.Fatal("failed to build credential source", zap.Error(err))
	}
	if closer, ok := credentials.(io.Closer); ok {
		defer func() {
			if err := closer.Close(); err != nil {
				log.Warn("failed to close bootstrap provider", zap.Error(err))
			}
		}()
	}
	client, err := graphqlclient.NewWithRateLimit(cfg.Controller.ApiURI, credentials,
		cfg.Controller.APIClient.RequestsPerSecond, cfg.Controller.APIClient.Burst)
	if err != nil {
		log.Fatal("create API client", zap.Error(err))
	}

	// runners tracks every kind's list-then-watch goroutine so shutdown can
	// wait for each Runner's final checkpoint flush before the process exits.
	var runners sync.WaitGroup

	closeStores, err := registerDiskControllers(ctx, &runners, mgr, cfg, log, client)
	if err != nil {
		log.Fatal("failed to register durable controllers", zap.Error(err))
	}
	defer func() {
		if err := closeStores(); err != nil {
			log.Error("close controller stores", zap.Error(err))
		}
	}()

	addr := fmt.Sprintf(":%d", cfg.Controller.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: buildMux(mgr, credentialReadiness(credentials)),
	}

	go func() {
		log.Info("HTTP server listening", zap.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP server error", zap.Error(err))
		}
	}()

	log.Info("controller-manager started", zap.String("apiURI", cfg.Controller.ApiURI))
	if err := mgr.Start(ctx); err != nil {
		log.Error("manager exited with error", zap.Error(err))
	}

	// Stop the runners even when the manager exited on its own error, then
	// wait (bounded) for their final checkpoint flushes.
	stop()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	runnersDone := make(chan struct{})
	go func() {
		runners.Wait()
		close(runnersDone)
	}()
	select {
	case <-runnersDone:
	case <-shutdownCtx.Done():
		log.Warn("list-watch runners did not stop before shutdown timeout")
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("HTTP server shutdown error", zap.Error(err))
	}
	log.Info("controller-manager stopped")
}

func relationIndex(kind, namespace, name string) string {
	return kind + "/" + namespace + "\x00" + name
}

func newDiskRunner[T any](kind string, watcher listwatch.ListWatcher[T], store *checkpoint.DiskStore, cfg *config.Config, log *zap.Logger, key func(T) types.WorkItemKey, revision func(T) string) *listwatch.Runner[T] {
	return &listwatch.Runner[T]{
		Kind: kind, ListWatcher: watcher, Disk: store, Cache: cache.New[T](),
		KeyFunc: key, RevisionFunc: revision, Log: log,
		WaitForWatchBookmark: true,
		MaxBackoff:           cfg.Controller.Watch.MaxBackoff,
	}
}

func registerDiskRunner[T any](ctx context.Context, runners *sync.WaitGroup, mgr *manager.Manager, runner *listwatch.Runner[T], reconciler types.Reconciler, cfg *config.Config, related func(context.Context, types.WorkItemKey) error) error {
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind: runner.Kind, Reconciler: reconciler, Cache: runner.Cache, Disk: runner.Disk,
		RelatedEnqueue: related, ResyncInterval: cfg.Controller.Watch.ResyncInterval,
		MaxAttempts: cfg.Controller.Reconcile.MaxAttempts, StallThreshold: cfg.Controller.Reconcile.StallThreshold,
	}); err != nil {
		return err
	}
	runners.Go(func() {
		if err := runner.Run(ctx); err != nil && ctx.Err() == nil {
			runner.Log.Error("durable runner exited", zap.String("kind", runner.Kind), zap.Error(err))
			if err := runner.Cache.BeginRecovery(ctx); err != nil && ctx.Err() == nil {
				runner.Log.Error("close failed runner admission", zap.Error(err))
			}
		}
	})
	return nil
}

func registerDiskControllers(ctx context.Context, runners *sync.WaitGroup, mgr *manager.Manager, cfg *config.Config, log *zap.Logger, client *graphqlclient.Client) (func() error, error) {
	ctx, cancel := context.WithCancel(ctx)
	stores := make(map[string]*checkpoint.DiskStore, 4)
	closeStores := func() error {
		cancel()
		runners.Wait()
		var result error
		for _, store := range stores {
			result = errors.Join(result, store.Close())
		}
		return result
	}
	for _, kind := range []string{"Namespace", "Repository", "CategoryTaxonomy", "Product"} {
		store, err := checkpoint.OpenDiskStore(cfg.Controller.Checkpoint.Dir, kind)
		if err != nil {
			return nil, errors.Join(err, closeStores())
		}
		stores[kind] = store
	}
	ns := newDiskRunner("Namespace", listwatch.NewNamespaceListWatcher(client), stores["Namespace"], cfg, log,
		func(n namespacecontroller.Namespace) types.WorkItemKey {
			return types.WorkItemKey{Kind: "Namespace", Name: n.Name}
		},
		func(n namespacecontroller.Namespace) string { return n.ResourceVersion })
	repo := newDiskRunner("Repository", listwatch.NewRepositoryListWatcher(client), stores["Repository"], cfg, log,
		func(r repositorycontroller.Repository) types.WorkItemKey {
			return types.WorkItemKey{Kind: "Repository", Namespace: r.Namespace, Name: r.Name}
		},
		func(r repositorycontroller.Repository) string { return r.ResourceVersion })
	cat := newDiskRunner("CategoryTaxonomy", listwatch.NewCategoryTaxonomyListWatcher(client), stores["CategoryTaxonomy"], cfg, log,
		func(c categorytaxonomy.CategoryTaxonomy) types.WorkItemKey {
			return types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: c.Namespace, Name: c.Name}
		},
		func(c categorytaxonomy.CategoryTaxonomy) string { return c.ResourceVersion })
	product := newDiskRunner("Product", listwatch.NewProductListWatcher(client), stores["Product"], cfg, log,
		func(p categorytaxonomy.Product) types.WorkItemKey {
			return types.WorkItemKey{Kind: "Product", Namespace: p.Namespace, Name: p.Name}
		},
		func(p categorytaxonomy.Product) string { return p.ResourceVersion })
	cat.AcceptUpdate, cat.ShouldEnqueueUpdate = categorytaxonomy.AcceptWatchUpdate, categorytaxonomy.ShouldEnqueueWatchUpdate
	cat.DiskIndexes = func(c categorytaxonomy.CategoryTaxonomy) []string {
		if c.ParentRefName == "" {
			return nil
		}
		return []string{relationIndex("parent", c.Namespace, c.ParentRefName)}
	}
	cat.DiskRelated = func(c categorytaxonomy.CategoryTaxonomy) []types.WorkItemKey {
		keys := []types.WorkItemKey{{Kind: "ProductCategory", Namespace: c.Namespace, Name: c.Name}}
		if c.ParentRefName != "" {
			keys = append(keys, types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: c.Namespace, Name: c.ParentRefName})
		}
		return keys
	}
	cat.DiskRelatedVersion = func(c categorytaxonomy.CategoryTaxonomy) string {
		return fmt.Sprintf("%s/%t", c.UID, c.DeletionTimestamp != nil || slices.Contains(c.Finalizers, "gitstore.dev/foreground-deletion"))
	}
	product.DiskIndexes = func(p categorytaxonomy.Product) []string {
		if p.CategoryRefName == "" {
			return nil
		}
		return []string{relationIndex("category", p.Namespace, p.CategoryRefName)}
	}
	product.DiskRelated = func(p categorytaxonomy.Product) []types.WorkItemKey {
		if p.CategoryRefName == "" {
			return nil
		}
		return []types.WorkItemKey{{Kind: "CategoryTaxonomy", Namespace: p.Namespace, Name: p.CategoryRefName}}
	}
	related := func(ctx context.Context, key types.WorkItemKey) error {
		if key.Kind == "ProductCategory" {
			return product.Disk.RequestFanout(ctx, relationIndex("category", key.Namespace, key.Name))
		}
		store := stores[key.Kind]
		if store == nil {
			return fmt.Errorf("related kind %q: %w", key.Kind, types.ErrKindNotRegistered)
		}
		return store.Enqueue(ctx, key)
	}
	counter := func(store *checkpoint.DiskStore, synced func() bool, recovering func() cache.RecoveryState, index string) categorytaxonomy.ProductCounter {
		return func(ctx context.Context, namespace, name string) (int64, error) {
			if !synced() || recovering().Recovering {
				return 0, checkpoint.ErrSnapshotInProgress
			}
			count, err := store.IndexCount(ctx, relationIndex(index, namespace, name))
			if err != nil {
				return 0, err
			}
			if count > 1<<63-1 {
				return 0, errors.New("relation count exceeds status integer range")
			}
			return int64(count), nil
		}
	}
	categoryReconciler := categorytaxonomy.NewReconcilerWithLookup(cat.Lookup, status.NewGraphQLStatusClient(client),
		counter(product.Disk, product.Cache.HasSynced, product.Cache.RecoveryState, "category"),
		counter(cat.Disk, cat.Cache.HasSynced, cat.Cache.RecoveryState, "parent"),
		func(ctx context.Context, key types.WorkItemKey) error {
			return cat.Disk.RequestFanout(ctx, relationIndex("parent", key.Namespace, key.Name))
		}, categorytaxonomy.NewGraphQLDeletionClient(client))
	registrations := []func() error{
		func() error {
			return registerDiskRunner(ctx, runners, mgr, ns, namespacecontroller.NewReconcilerWithLookup(ns.Lookup, status.NewGraphQLNamespaceStatusClient(client), namespacecontroller.NewGraphQLRepositoryClient(client), namespacecontroller.NewGraphQLDeletionClient(client)), cfg, related)
		},
		func() error {
			return registerDiskRunner(ctx, runners, mgr, repo, repositorycontroller.NewReconcilerWithLookup(repo.Lookup, status.NewGraphQLRepositoryStatusClient(client), repositorycontroller.NewGraphQLStorageClient(client), repositorycontroller.NewGraphQLCompletionClient(client)), cfg, related)
		},
		func() error { return registerDiskRunner(ctx, runners, mgr, cat, categoryReconciler, cfg, related) },
		func() error {
			return registerDiskRunner(ctx, runners, mgr, product, productcontroller.NewReconcilerWithLookup(product.Lookup, cat.Lookup, status.NewGraphQLProductStatusClient(client), productcontroller.NewGraphQLCompletionClient(client)), cfg, related)
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return nil, errors.Join(err, closeStores())
		}
	}
	return closeStores, nil
}

// configFileFlags collects repeated --config-file occurrences in the order
// given; each one after the first is merged additively on top of the
// previous ones (see config.LoadFromFiles).
type configFileFlags []string

func (f *configFileFlags) String() string { return strings.Join(*f, ",") }

func (f *configFileFlags) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func parseConfigFiles(args []string) ([]string, error) {
	flags := flag.NewFlagSet("gitstore-controller-manager", flag.ContinueOnError)
	var paths configFileFlags
	flags.Var(&paths, "config-file", "path to a TOML configuration file; repeat to layer additive overlays on top of the first")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	return paths, nil
}

func buildCredentialSource(ctx context.Context, cfg *config.Config, log *zap.Logger) (graphqlclient.CredentialSource, error) {
	controller := cfg.Controller
	if controller.ServiceAccount.KeyRef.Name == "" {
		return nil, fmt.Errorf("configure controller.serviceaccount.key_ref")
	}

	owner := "serviceaccount:" + controller.ServiceAccount.Namespace + ":" + controller.ServiceAccount.Name + ":" + controller.ServiceAccount.UID
	resolver, err := secret.NewBootstrapResolver(controller.SecretProviders.Bootstrap, owner,
		controller.ServiceAccount.KeyRef, controller.ServiceAccount.KeyID, secret.NewObserver(log))
	if err != nil {
		return nil, fmt.Errorf("create bootstrap secret resolver: %w", err)
	}
	signer, err := graphqlclient.NewResolvingTokenSigner(ctx, resolver, controller.ServiceAccount.UID)
	if err != nil {
		return nil, fmt.Errorf("create service account signer: %w", errors.Join(err, resolver.Close()))
	}
	return graphqlclient.NewServiceAccountSource(
		controller.ApiURI,
		controller.ServiceAccount.Namespace,
		controller.ServiceAccount.Name,
		signer,
		controller.ServiceAccount.AssertionAudience,
		controller.ServiceAccount.AccessTokenAudience,
		10*time.Minute,
		time.Hour,
	), nil
}

func credentialReadiness(source graphqlclient.CredentialSource) health.CredentialReadiness {
	readiness, _ := source.(health.CredentialReadiness)
	return readiness
}

// registerRepository constructs the Repository runner and reconciler once the
// process has been supplied with the concrete typed Repository ListWatcher and
// idempotent storage provisioner. They are explicit dependencies because the
// controller has no direct Git-service credentials: the provisioner must use a
// server-side API contract rather than teach the controller a second transport.
func registerRepository(
	ctx context.Context,
	runners *sync.WaitGroup,
	mgr *manager.Manager,
	checkpointStore *checkpoint.FilesystemStore,
	cfg *config.Config,
	log *zap.Logger,
	client *graphqlclient.Client,
	watcher listwatch.ListWatcher[repositorycontroller.Repository],
	storageClient repositorycontroller.StorageClient,
) (*listwatch.Runner[repositorycontroller.Repository], error) {
	repositoryCache := cache.New[repositorycontroller.Repository]()
	runner := &listwatch.Runner[repositorycontroller.Repository]{
		Kind:                 "Repository",
		WaitForWatchBookmark: true,
		ListWatcher:          watcher,
		Cache:                repositoryCache,
		Store:                checkpointStore,
		Enqueue:              mgr.Enqueue,
		KeyFunc: func(item repositorycontroller.Repository) types.WorkItemKey {
			return types.WorkItemKey{Kind: "Repository", Namespace: item.Namespace, Name: item.Name}
		},
		RevisionFunc: func(item repositorycontroller.Repository) string {
			return item.ResourceVersion
		},
		FlushIntervalEvents: cfg.Controller.Checkpoint.FlushIntervalEvents,
		MaxBackoff:          cfg.Controller.Watch.MaxBackoff,
		ResyncInterval:      cfg.Controller.Watch.ResyncInterval,
		Log:                 log,
	}
	reconciler := repositorycontroller.NewReconciler(
		cache.AsReadOnly(repositoryCache),
		status.NewGraphQLRepositoryStatusClient(client),
		storageClient,
		repositorycontroller.NewGraphQLCompletionClient(client),
	)
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:           "Repository",
		Reconciler:     reconciler,
		Cache:          repositoryCache,
		OnSuccess:      runner.MarkCompleted,
		MaxAttempts:    cfg.Controller.Reconcile.MaxAttempts,
		StallThreshold: cfg.Controller.Reconcile.StallThreshold,
	}); err != nil {
		return nil, fmt.Errorf("register Repository: %w", err)
	}
	runners.Go(func() {
		if err := runner.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("Repository runner exited with error", zap.Error(err))
		}
	})
	return runner, nil
}

// buildMux returns the HTTP handler for the health/metrics and management surface.
func buildMux(mgr *manager.Manager, credentialReadiness ...health.CredentialReadiness) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /health", health.NewHandler(mgr, version.Version, credentialReadiness...))
	mux.Handle("GET /metrics", health.NewMetricsHandler(mgr))
	mux.HandleFunc("GET /controller/v1/poison/{kind}", api.ListPoisonHandler(mgr))
	mux.HandleFunc("POST /controller/v1/poison/{namespace}/{kind}/{name}/requeue", api.RequeuePoisonHandler(mgr))
	mux.HandleFunc("POST /controller/v1/poison/{kind}/{name}/requeue", api.RequeuePoisonHandler(mgr))
	return mux
}
