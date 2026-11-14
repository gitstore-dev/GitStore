// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/opa"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type bootstrapRBACGit struct {
	mu          sync.Mutex
	tip         string
	history     map[string]map[string][]byte
	receipts    map[string]string
	writes      int
	unavailable atomic.Bool
	loseAck     atomic.Bool
}

func (g *bootstrapRBACGit) ResolveRef(context.Context, string, string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.unavailable.Load() {
		return "", rbac.ErrUnavailable
	}
	return g.tip, nil
}

func (g *bootstrapRBACGit) ListFiles(_ context.Context, _, _, commit string) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var paths []string
	for path := range g.history[commit] {
		paths = append(paths, path)
	}
	return paths, nil
}

func (g *bootstrapRBACGit) ReadFile(_ context.Context, _, path, commit string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.unavailable.Load() {
		return nil, rbac.ErrUnavailable
	}
	if content, ok := g.history[commit][path]; ok {
		return append([]byte(nil), content...), nil
	}
	return nil, datastore.ErrNotFound
}

func (g *bootstrapRBACGit) CommitFileAtRevisionForRepo(_ context.Context, repository string, p gitclient.CommitFileAtRevisionParams) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.unavailable.Load() {
		return "", grpcstatus.Error(codes.Unavailable, "fixture unavailable")
	}
	if !p.RetainReceipt || p.AuthorName != bootstrapActor || p.RefName != "refs/heads/main" {
		return "", grpcstatus.Error(codes.InvalidArgument, "bootstrap must use a guarded retained system write")
	}
	key := fmt.Sprintf("%s/%s/%s/%s/%s/%s", repository, p.RefName, p.ExpectedCommitSHA, p.Path, p.CommitMessage, p.Content)
	if commit, ok := g.receipts[key]; ok {
		return commit, nil
	}
	if p.ReceiptOnly {
		return "", grpcstatus.Error(codes.NotFound, "no receipt")
	}
	if p.ExpectedCommitSHA != g.tip {
		return "", grpcstatus.Error(codes.Aborted, "tip changed")
	}
	next := maps.Clone(g.history[g.tip])
	next[p.Path] = append([]byte(nil), p.Content...)
	g.tip = fmt.Sprintf("%040x", len(g.history)+1)
	g.history[g.tip] = next
	g.receipts[key] = g.tip
	g.writes++
	if g.loseAck.Swap(false) {
		return "", grpcstatus.Error(codes.Unavailable, "fixture lost acknowledgment after commit")
	}
	return g.tip, nil
}

func (g *bootstrapRBACGit) removeSeed() {
	g.mu.Lock()
	defer g.mu.Unlock()
	next := maps.Clone(g.history[g.tip])
	delete(next, "rbac/clusterrolebindings/initial-admin.yaml")
	g.tip = fmt.Sprintf("%040x", len(g.history)+1)
	g.history[g.tip] = next
}

type bootstrapModelProvider struct{ *opa.Engine }

type concurrentOriginGit struct {
	*bootstrapRBACGit
	entered chan struct{}
	release chan struct{}
}

func (g *concurrentOriginGit) ReadFile(ctx context.Context, repository, path, commit string) ([]byte, error) {
	select {
	case g.entered <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-g.release:
		return g.bootstrapRBACGit.ReadFile(ctx, repository, path, commit)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestRBACOriginReadsImmutableFilesConcurrentlyAndRechecksNamespace(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			store, git, _, _, _ := newRBACBootstrapFixture(t)
			for _, name := range []string{"parallel-one", "parallel-two"} {
				git.history[git.tip]["rbac/clusterroles/"+name+".yaml"] = []byte(fmt.Sprintf(
					"apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: ClusterRole\nmetadata:\n  name: %s\nspec:\n  rules:\n  - resources: [product]\n    verbs: [read]\n", name))
			}
			reader := &concurrentOriginGit{bootstrapRBACGit: git, entered: make(chan struct{}, 2), release: make(chan struct{})}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			defer close(reader.release)
			done := make(chan error, 1)
			go func() {
				_, resources, err := (rbac.OriginReader{Store: store, Git: reader}).ReadTree(ctx, rbac.ClusterTierID)
				if err == nil && len(resources) != 2 {
					err = fmt.Errorf("expected two immutable resources, got %d", len(resources))
				}
				done <- err
			}()
			for range 2 {
				select {
				case <-reader.entered:
				case <-ctx.Done():
					t.Fatal("immutable file reads must overlap within the bounded scan")
				}
			}
			if changed {
				namespace, err := store.GetNamespaceByName(ctx, "gitstore-system")
				require.NoError(t, err)
				version := namespace.ResourceVersion
				namespace.Labels = map[string]string{"fixture": "changed"}
				namespace.ResourceVersion = fmt.Sprint(time.Now().UnixNano())
				require.NoError(t, store.UpdateNamespace(ctx, namespace, version))
			}
			reader.release <- struct{}{}
			reader.release <- struct{}{}
			select {
			case err := <-done:
				if changed {
					require.ErrorIs(t, err, rbac.ErrUnavailable)
				} else {
					require.NoError(t, err)
				}
			case <-ctx.Done():
				t.Fatal("bounded scan did not finish")
			}
		})
	}
}

func (*bootstrapModelProvider) Name() string { return "opa" }
func (*bootstrapModelProvider) Authorize(context.Context, *auth.Principal, string, auth.ResourceContext) (auth.Decision, error) {
	return auth.Deny("ordinary-cache", "bootstrap must not open serving authorization"), nil
}

func newRBACBootstrapFixture(t *testing.T) (datastore.Datastore, *bootstrapRBACGit, *admission.RBACValidator, *atomic.Bool, func(string) error) {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, ensureBootstrapResources(t.Context(), store, &bootstrapGitProvisioner{},
		apiruntime.SystemClock{}, apiruntime.UUIDGenerator{}, zap.NewNop()))
	tip := strings.Repeat("a", 40)
	git := &bootstrapRBACGit{tip: tip, history: map[string]map[string][]byte{tip: {}}, receipts: map[string]string{}}
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	engine, err := opa.NewEngine(t.Context())
	require.NoError(t, err)
	registry := auth.NewProviderRegistry(nil, &bootstrapModelProvider{engine}, nil)
	source := rbac.OriginReader{Store: store, Git: git}
	validator := &admission.RBACValidator{Builtins: builtins, Capture: rbac.ValidationCapture{
		Registry: registry, Reader: rbac.SnapshotReader{
			Store: store.(datastore.RBACCapable).RBACStore(), ReadOrigin: source.ReadOrigin,
			CatalogDigest: auth.AuthorizationCatalogDigest(), BuiltinDigest: builtins.Digest(),
		},
	}}
	projector, err := cataloggrpc.NewServer(cataloggrpc.ServerDeps{
		Store: store, GitReader: git, Registry: registry, Logger: zap.NewNop(), RBACValidator: validator,
	})
	require.NoError(t, err)
	block := &atomic.Bool{}
	run := func(subject string) error {
		return initializeRBACAdministrator(t.Context(), store, git, validator, func(ctx context.Context, tier string) error {
			if block.Load() {
				return rbac.ErrUnavailable
			}
			return projector.ReconcileRBACTier(ctx, tier)
		}, subject)
	}
	return store, git, validator, block, run
}

func TestRBACBootstrapInitialAdministratorIsEffectiveRemovableAndNeverReseeded(t *testing.T) {
	store, git, validator, _, run := newRBACBootstrapFixture(t)
	require.NoError(t, run("first-admin"))
	namespace, err := store.GetNamespaceByName(t.Context(), "gitstore-system")
	require.NoError(t, err)
	receipt, err := datastore.ReadNamespaceRBACBootstrap(namespace)
	require.NoError(t, err)
	require.NotEmpty(t, receipt.Commit)
	principal := &auth.Principal{Subject: "first-admin", AuthMethod: "static-users", Groups: []string{"system:authenticated"}}
	authorize := func() auth.Decision {
		t.Helper()
		capture, err := validator.Capture.Begin(t.Context(), principal, "")
		require.NoError(t, err)
		defer capture.Close()
		decision, err := capture.Authorize(t.Context(), "namespace.create", auth.ResourceContext{Kind: "namespace"})
		require.NoError(t, err)
		return decision
	}
	require.Equal(t, auth.OutcomeAllow, authorize().Outcome)
	ordinary, err := validator.Capture.Registry.AuthZ().Authorize(t.Context(), principal, "namespace.create", auth.ResourceContext{Kind: "namespace"})
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeDeny, ordinary.Outcome, "initialization must not alter ordinary serving policy")
	git.removeSeed()
	require.NoError(t, run("replacement-config-must-not-regrant"))
	require.Equal(t, auth.OutcomeDeny, authorize().Outcome)
	require.Equal(t, 1, git.writes)
	restarted, err := store.GetNamespaceByName(t.Context(), "gitstore-system")
	require.NoError(t, err)
	actual, err := datastore.ReadNamespaceRBACBootstrap(restarted)
	require.NoError(t, err)
	require.Equal(t, receipt, actual)
	_, err = validator.ValidateInitialAdministrator(t.Context(), store)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
}

func TestRBACBootstrapRecoversLostAcknowledgmentEvenAfterSeedRemoval(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(fmt.Sprint(removed), func(t *testing.T) {
			store, git, _, _, run := newRBACBootstrapFixture(t)
			git.loseAck.Store(true)
			require.ErrorIs(t, run("first-admin"), rbac.ErrUnavailable)
			namespace, err := store.GetNamespaceByName(t.Context(), "gitstore-system")
			require.NoError(t, err)
			pending, err := datastore.ReadNamespaceRBACBootstrap(namespace)
			require.NoError(t, err)
			require.Empty(t, pending.Commit)
			if removed {
				git.removeSeed()
			}
			require.NoError(t, run("configuration-changed-during-retry"))
			require.Equal(t, 1, git.writes)
			namespace, err = store.GetNamespaceByName(t.Context(), "gitstore-system")
			require.NoError(t, err)
			completed, err := datastore.ReadNamespaceRBACBootstrap(namespace)
			require.NoError(t, err)
			require.NotEmpty(t, completed.Commit)
			require.Equal(t, "first-admin", completed.Subject)
		})
	}
}

func TestRBACBootstrapConcurrentAPIsShareOneSeedAndReceipt(t *testing.T) {
	_, git, _, _, run := newRBACBootstrapFixture(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			var err error
			for range 4 {
				if err = run("first-admin"); err == nil {
					break
				}
			}
			results <- err
		}()
	}
	close(start)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	require.Equal(t, 1, git.writes)
}

func TestRBACBootstrapUnavailableDependenciesNeverOpenOrSeedAuthority(t *testing.T) {
	for _, failure := range []string{"git", "projection", "subject"} {
		t.Run(failure, func(t *testing.T) {
			_, git, _, blocked, run := newRBACBootstrapFixture(t)
			subject := "first-admin"
			switch failure {
			case "git":
				git.unavailable.Store(true)
			case "projection":
				blocked.Store(true)
			case "subject":
				subject = "system:api"
			}
			require.ErrorIs(t, run(subject), rbac.ErrUnavailable)
			require.Zero(t, git.writes)
			git.unavailable.Store(false)
			blocked.Store(false)
			require.NoError(t, run("first-admin"))
			require.Equal(t, 1, git.writes)
		})
	}
}
