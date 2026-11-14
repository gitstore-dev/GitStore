// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/resolver"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/opa"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gitstore-dev/gitstore/observability"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestRBACNativeProcess(t *testing.T) {
	path := os.Getenv("GITSTORE_RBAC_NATIVE_TEST_CONFIG")
	if path == "" {
		t.Skip("owned native RBAC process fixture not selected")
	}
	require.Equal(t, "1", os.Getenv("GITSTORE_RBAC_NATIVE_TEST_OWNED"), "explicit ownership acknowledgement is required")
	cfg, err := config.LoadFrom(path)
	require.NoError(t, err)
	require.Equal(t, "scylla", cfg.Api.Datastore.Backend)
	require.Equal(t, "opa", cfg.Api.Auth.AuthZ.Provider)
	if os.Getenv("GITSTORE_RBAC_NATIVE_TEST_SCYLLA_NAT") == "1" {
		require.Len(t, cfg.Api.Datastore.Scylla.Hosts, 1)
		host, portText, err := net.SplitHostPort(cfg.Api.Datastore.Scylla.Hosts[0])
		require.NoError(t, err)
		ip := net.ParseIP(host)
		require.NotNil(t, ip)
		port, err := strconv.Atoi(portText)
		require.NoError(t, err)
		cfg.Api.Datastore.Scylla.AddressTranslator = gocql.AddressTranslatorFunc(func(net.IP, int) (net.IP, int) {
			return ip, port
		})
	}
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log, err := zap.NewProduction()
	require.NoError(t, err)
	defer func() { _ = log.Sync() }()
	stopProfiler, err := observability.StartCapacityProfiler(cfg.Api.Profiling, log)
	require.NoError(t, err)
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, stopProfiler(shutdown))
	}()
	server, err := newServer(cfg, log, true)
	require.NoError(t, err)
	defer server.Close()
	server.Start()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server.Shutdown(shutdown)
}

type bootstrapRPCServer struct {
	gitv1.UnimplementedGitServiceServer
	mu           sync.Mutex
	repositories map[string]*bootstrapRBACGit
	unavailable  atomic.Bool
}

func (s *bootstrapRPCServer) repository(id string) (*bootstrapRBACGit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable.Load() {
		return nil, grpcstatus.Error(codes.Unavailable, "owned fixture Git unavailable")
	}
	git := s.repositories[id]
	if git == nil {
		return nil, grpcstatus.Error(codes.NotFound, "repository storage absent")
	}
	return git, nil
}

func (s *bootstrapRPCServer) CreateRepository(_ context.Context, request *gitv1.CreateRepositoryRequest) (*gitv1.CreateRepositoryResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable.Load() {
		return nil, grpcstatus.Error(codes.Unavailable, "owned fixture Git unavailable")
	}
	if s.repositories[request.RepositoryId] != nil {
		return nil, grpcstatus.Error(codes.AlreadyExists, "repository exists")
	}
	tip := strings.Repeat("a", 40)
	s.repositories[request.RepositoryId] = &bootstrapRBACGit{
		tip: tip, history: map[string]map[string][]byte{tip: {}}, receipts: map[string]string{},
	}
	return &gitv1.CreateRepositoryResponse{RepositoryId: request.RepositoryId, StoragePath: "/owned-fixture/" + request.RepositoryId}, nil
}

func (s *bootstrapRPCServer) ListFiles(ctx context.Context, request *gitv1.ListFilesRequest) (*gitv1.ListFilesResponse, error) {
	git, err := s.repository(request.RepositoryId)
	if err != nil {
		return nil, err
	}
	commit := request.Ref
	if strings.HasPrefix(commit, "refs/heads/") {
		commit, err = git.ResolveRef(ctx, request.RepositoryId, commit)
		if err != nil {
			return nil, err
		}
	}
	response := &gitv1.ListFilesResponse{RefCommitSha: commit}
	if request.Recursive {
		paths, err := git.ListFiles(ctx, request.RepositoryId, request.PathPrefix, commit)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			content, err := git.ReadFile(ctx, request.RepositoryId, path, commit)
			if err != nil {
				return nil, err
			}
			response.Files = append(response.Files, &gitv1.FileEntry{Path: path, SizeBytes: uint64(len(content))})
		}
	}
	return response, nil
}

func (s *bootstrapRPCServer) GetFile(ctx context.Context, request *gitv1.GetFileRequest) (*gitv1.GetFileResponse, error) {
	git, err := s.repository(request.RepositoryId)
	if err != nil {
		return nil, err
	}
	content, err := git.ReadFile(ctx, request.RepositoryId, request.Path, request.Ref)
	if errors.Is(err, datastore.ErrNotFound) {
		return nil, grpcstatus.Error(codes.NotFound, "manifest absent")
	}
	return &gitv1.GetFileResponse{Content: content}, err
}

func (s *bootstrapRPCServer) CommitFileAtRevision(ctx context.Context, request *gitv1.CommitFileAtRevisionRequest) (*gitv1.CommitFileResponse, error) {
	git, err := s.repository(request.File.RepositoryId)
	if err != nil {
		return nil, err
	}
	commit, err := git.CommitFileAtRevisionForRepo(ctx, request.File.RepositoryId, gitclient.CommitFileAtRevisionParams{
		CommitFileParams: gitclient.CommitFileParams{Path: request.File.Path, Content: request.File.Content,
			CommitMessage: request.File.CommitMessage, AuthorName: request.File.AuthorName, AuthorEmail: request.File.AuthorEmail},
		RefName: request.RefName, ExpectedCommitSHA: request.ExpectedCommitSha,
		RetainReceipt: request.RetainReceipt, ReceiptOnly: request.ReceiptOnly,
	})
	return &gitv1.CommitFileResponse{CommitSha: commit}, err
}

func TestRBACServerCompositionRecoversBeforeOpeningTrafficAndReusesProviderOnReload(t *testing.T) {
	const secret = "owned-rbac-server-composition-test-secret"
	native := &bootstrapRPCServer{repositories: map[string]*bootstrapRBACGit{}}
	native.unavailable.Store(true)
	var verified atomic.Int32
	rpc := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, request any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		envelopes := md.Get(auth.RPCSignatureMetadata)
		if len(envelopes) != 1 {
			return nil, grpcstatus.Error(codes.Unauthenticated, "signed RPC required")
		}
		if _, _, err := auth.VerifyRPC(secret, "gitstore-git", info.FullMethod, envelopes[0], request.(proto.Message), time.Now()); err != nil {
			return nil, grpcstatus.Error(codes.Unauthenticated, "invalid signed RPC")
		}
		verified.Add(1)
		return next(ctx, request)
	}))
	gitv1.RegisterGitServiceServer(rpc, native)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = rpc.Serve(listener) }()
	t.Cleanup(rpc.Stop)
	t.Cleanup(func() { _ = listener.Close() })
	ports := make([]int, 4)
	var reservations []net.Listener
	for index := range ports {
		reserved, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		reservations = append(reservations, reserved)
		ports[index] = reserved.Addr().(*net.TCPAddr).Port
	}
	for _, reservation := range reservations {
		require.NoError(t, reservation.Close())
	}
	cfg := &config.Config{}
	cfg.Api.AdminPort, cfg.Api.StorefrontPort, cfg.Api.GitPort, cfg.Api.GrpcPort = ports[0], ports[1], ports[2], ports[3]
	cfg.Api.Admin, cfg.Api.Storefront = config.DefaultEndpoint("admin"), config.DefaultEndpoint("storefront")
	cfg.Api.Datastore.Backend = "memdb"
	cfg.Api.GitService.Uri = listener.Addr().String()
	cfg.GrpcAuth.HmacSecret = secret
	cfg.Api.Auth.AuthN.Chain = []string{"anonymous"}
	cfg.Api.Auth.AuthZ.Provider, cfg.Api.Auth.UserDir.Provider = "opa", "none"
	cfg.Api.Auth.OPA = config.OPAConfig{WritesEnabled: true, InitialAdminSubject: "first-admin", MaxStaleness: 5 * time.Second,
		ValidationTimeout: 30 * time.Second, RebuildWorkers: 1, DirtyQueueCapacity: 8, SnapshotRetries: 1,
		SnapshotMemorySize: "16MiB", RebuildMemorySize: "1MiB"}
	cfg.Api.Watch.Journal = config.WatchJournalConfig{Retention: time.Hour, BookmarkInterval: time.Second,
		Materializer: config.MaterializerWatchConfig{LeaseTTL: time.Minute, LeaseRenewInterval: 10 * time.Second, MaxLag: 5 * time.Second},
		Read:         config.ReadWatchConfig{BatchSize: 128, MaxReplayEvents: 1000},
		Poll:         config.PollWatchConfig{Min: 5 * time.Millisecond, Max: 20 * time.Millisecond},
		Subscriber:   config.SubscriberWatchConfig{Buffer: 16, Backpressure: time.Second}}
	_, err = NewServer(cfg, zap.NewNop())
	require.ErrorContains(t, err, "production acceptance is pending")
	server, err := newServer(cfg, zap.NewNop(), true)
	require.NoError(t, err)
	t.Cleanup(server.Close)
	runtime := server.providerShutdown[0].(*rbacRuntime)
	registry := server.gitClient.Registry
	principal := &auth.Principal{Subject: "fixture-master", AuthMethod: "static-users", Groups: []string{"system:masters"}}
	_, err = registry.AuthZ().Authorize(t.Context(), principal, "namespace.read", auth.ResourceContext{Kind: "namespace"})
	require.ErrorIs(t, err, opa.ErrUnavailable)
	status := func(endpoint, path string) int {
		t.Helper()
		for _, listener := range server.httpServers {
			if listener.name == endpoint {
				response := httptest.NewRecorder()
				listener.server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				return response.Code
			}
		}
		t.Fatalf("endpoint %q absent", endpoint)
		return 0
	}
	require.Equal(t, http.StatusOK, status("admin", "/health"))
	require.Equal(t, http.StatusServiceUnavailable, status("admin", "/ready"))
	require.Equal(t, http.StatusServiceUnavailable, status("admin", "/admin/graphql"))
	require.Equal(t, http.StatusServiceUnavailable, status("git-http", "/default/example.git/info/refs"))
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	validator := &admission.RBACValidator{Capture: runtime.validationCapture(registry), Builtins: builtins, Ready: runtime.Ready}
	_, err = validator.Validate(t.Context(), principal, admission.RBACCandidate{})
	require.ErrorIs(t, err, rbac.ErrUnavailable, "authoritative pinning must not become an external bootstrap bypass")
	journal := server.namespaceWatch.journal
	bounds, err := journal.Bounds(t.Context())
	require.NoError(t, err)
	lease, acquired, err := journal.AcquireLease(t.Context(), "owned-server-composition", time.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { _ = journal.ReleaseLease(context.Background(), lease) })
	refresh := func() {
		t.Helper()
		for _, source := range []string{"Namespace", "Repository", "RBAC", "ServiceAccount"} {
			require.NoError(t, journal.SaveProgress(t.Context(), lease, datastore.ResourceCDCProgress{
				Source: source, StreamID: watchjournal.SourceReadFrontier, Position: []byte(bounds.Epoch), UpdatedAt: time.Now(),
			}))
		}
	}
	native.unavailable.Store(false)
	refreshed := time.Now().Add(-time.Second)
	require.Eventually(t, func() bool {
		if time.Since(refreshed) >= time.Second {
			refresh()
			refreshed = time.Now()
		}
		return runtime.Ready(t.Context()) == nil
	}, 8*time.Second, 10*time.Millisecond)
	require.Positive(t, verified.Load())
	require.Equal(t, http.StatusOK, status("admin", "/ready"))
	decision, err := registry.AuthZ().Authorize(t.Context(), &auth.Principal{Subject: "first-admin", AuthMethod: "static-users"},
		"namespace.create", auth.ResourceContext{Kind: "namespace"})
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome)
	mapping, err := server.store.LookupRepository(t.Context(), "gitstore-system", bootstrapRepositoryName)
	require.NoError(t, err)
	namespace, err := server.store.GetNamespaceByName(t.Context(), "gitstore-system")
	require.NoError(t, err)
	receipt, err := datastore.ReadNamespaceRBACBootstrap(namespace)
	require.NoError(t, err)
	content, err := server.gitClient.ReadFileForRepo(t.Context(), mapping.RepositoryID, "rbac/clusterrolebindings/initial-admin.yaml", receipt.Commit)
	require.NoError(t, err, "ordinary root-repository reads must become usable after bootstrap")
	require.Contains(t, string(content), "first-admin")
	fresh := &datastore.Namespace{UID: uuid.NewString(), Name: "fresh", Tier: datastore.NamespaceTierOrganization}
	require.NoError(t, server.store.CreateNamespace(t.Context(), fresh))
	controllerUID := uuid.NewString()
	require.NoError(t, server.store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{UID: controllerUID, Name: "gitstore-controller-manager"}))
	controller := &auth.Principal{Subject: "system:serviceaccount:gitstore-controller-manager", AuthMethod: "serviceaccount-jwt", ServiceAccountUID: controllerUID}
	target := auth.ResourceContext{Kind: "product", Name: "example", Attrs: map[string]any{"namespace": fresh.Name}}
	_, err = registry.AuthZ().Authorize(t.Context(), controller, "product.read", target)
	require.ErrorIs(t, err, opa.ErrUnavailable, "a new namespace has no serving tier before its canonical repository exists")
	recovery := &rbacRecoveryGit{store: server.store, client: server.gitClient}
	service, err := resolver.NewService(resolver.ServiceDeps{Store: server.store, Logger: zap.NewNop(), GitWriter: server.gitClient,
		ProvisionNamespaceRepository: recovery.provisionNamespace})
	require.NoError(t, err)
	ordinary := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "ordinary", AuthMethod: "static-users"})
	require.Error(t, service.ProvisionSystemRepository(ordinary, fresh.Name, "ordinary"))
	_, err = server.store.LookupRepository(t.Context(), fresh.Name, bootstrapRepositoryName)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	controllerContext := auth.ContextWithPrincipal(t.Context(), controller)
	require.NoError(t, service.ProvisionSystemRepository(controllerContext, fresh.Name, controller.Subject),
		"authorized infrastructure provisioning must not depend on the absent namespace tier it establishes")
	require.NoError(t, service.ProvisionSystemRepository(controllerContext, fresh.Name, controller.Subject))
	require.Eventually(t, func() bool {
		decision, err := registry.AuthZ().Authorize(t.Context(), controller, "product.read", target)
		return err == nil && decision.Outcome == auth.OutcomeAllow
	}, 3*time.Second, 10*time.Millisecond)
	before := registry.Diagnostics().Policy
	require.NotNil(t, before)
	require.NoError(t, server.authReloader.Reload())
	after := registry.Diagnostics().Policy
	require.NotNil(t, after)
	require.Equal(t, before, after, "same-provider reload must preserve the prepared engine and live publication")
	require.NoError(t, runtime.Ready(t.Context()))
	runtime.Shutdown()
	require.Equal(t, http.StatusServiceUnavailable, status("admin", "/ready"))
}
