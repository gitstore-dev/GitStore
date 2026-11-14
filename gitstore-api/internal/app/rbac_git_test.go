// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/allowall"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type recoveryRPC struct {
	gitv1.GitServiceClient
	calls atomic.Int32
	file  []byte
}

func (r *recoveryRPC) check(ctx context.Context, authorization *gitv1.RequestAuthorization, action string) {
	r.calls.Add(1)
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 30*time.Second || authorization == nil ||
		authorization.Actor == nil || authorization.Actor.Subject != "system:api" ||
		authorization.Actor.AuthMethod != "internal" || authorization.Action != action {
		panic("recovery must carry bounded, purpose-bound internal RPC authorization")
	}
}

func (r *recoveryRPC) ListFiles(ctx context.Context, request *gitv1.ListFilesRequest, _ ...grpc.CallOption) (*gitv1.ListFilesResponse, error) {
	r.check(ctx, request.Authorization, "repository.contents.read")
	response := &gitv1.ListFilesResponse{RefCommitSha: strings.Repeat("a", 40)}
	if request.Recursive {
		response.Files = []*gitv1.FileEntry{{Path: "rbac/clusterrolebindings/initial-admin.yaml", SizeBytes: 32}}
	}
	return response, nil
}

func (r *recoveryRPC) GetFile(ctx context.Context, request *gitv1.GetFileRequest, _ ...grpc.CallOption) (*gitv1.GetFileResponse, error) {
	r.check(ctx, request.Authorization, "repository.contents.read")
	return &gitv1.GetFileResponse{Content: r.file}, nil
}

func (r *recoveryRPC) CommitFileAtRevision(ctx context.Context, request *gitv1.CommitFileAtRevisionRequest, _ ...grpc.CallOption) (*gitv1.CommitFileResponse, error) {
	r.check(ctx, request.File.Authorization, "repository.contents.write")
	if !request.RetainReceipt {
		panic("bootstrap publication must retain its durable receipt")
	}
	return &gitv1.CommitFileResponse{CommitSha: strings.Repeat("b", 40)}, nil
}

type unbornRecoveryRPC struct {
	recoveryRPC
	unborn   bool
	conflict bool
	writes   int
}

func (r *unbornRecoveryRPC) CreateRepository(ctx context.Context, request *gitv1.CreateRepositoryRequest, _ ...grpc.CallOption) (*gitv1.CreateRepositoryResponse, error) {
	r.check(ctx, request.Authorization, "repository.create")
	return nil, grpcstatus.Error(codes.AlreadyExists, "storage created before fixture replacement")
}

func (r *unbornRecoveryRPC) ListFiles(ctx context.Context, request *gitv1.ListFilesRequest, options ...grpc.CallOption) (*gitv1.ListFilesResponse, error) {
	if r.unborn {
		r.check(ctx, request.Authorization, "repository.contents.read")
		return nil, grpcstatus.Error(codes.NotFound, "unborn canonical branch")
	}
	return r.recoveryRPC.ListFiles(ctx, request, options...)
}

func (r *unbornRecoveryRPC) CommitFileAtRevision(ctx context.Context, request *gitv1.CommitFileAtRevisionRequest, _ ...grpc.CallOption) (*gitv1.CommitFileResponse, error) {
	r.check(ctx, request.File.Authorization, "repository.contents.write")
	if request.File.Path != ".gitkeep" || len(request.File.Content) != 0 ||
		request.File.AuthorName != bootstrapActor || request.File.AuthorEmail != "" ||
		request.File.CommitMessage != "Initialize system repository" ||
		request.RefName != "refs/heads/main" || request.ExpectedCommitSha != strings.Repeat("0", 40) ||
		request.RetainReceipt || request.ReceiptOnly {
		panic("unborn initialization must publish only the fixed marker with a missing-ref precondition")
	}
	r.writes++
	r.unborn = false
	if r.conflict {
		return nil, grpcstatus.Error(codes.Aborted, "another initializer won")
	}
	return &gitv1.CommitFileResponse{CommitSha: strings.Repeat("a", 40)}, nil
}

func TestRBACRecoveryInitializesOnlyUnpublishedCanonicalBranches(t *testing.T) {
	for _, scenario := range []string{"unborn", "concurrent-winner", "initialized"} {
		t.Run(scenario, func(t *testing.T) {
			store, _, validator, _, initialize := newRBACBootstrapFixture(t)
			if scenario == "initialized" {
				require.NoError(t, initialize("first-admin"))
			}
			mapping, err := store.LookupRepository(t.Context(), "gitstore-system", bootstrapRepositoryName)
			require.NoError(t, err)
			rpc := &unbornRecoveryRPC{unborn: true, conflict: scenario == "concurrent-winner"}
			recovery := &rbacRecoveryGit{store: store, client: &gitclient.Client{Git: rpc, Registry: validator.Capture.Registry}}
			_, err = recovery.CreateRepository(t.Context(), mapping.RepositoryID, "default")
			if scenario == "initialized" {
				require.ErrorIs(t, err, rbac.ErrUnavailable)
				require.Zero(t, rpc.writes, "a missing initialized ref must never be replaced with empty policy")
				return
			}
			require.NoError(t, err)
			require.Equal(t, 1, rpc.writes)
			_, err = recovery.CreateRepository(t.Context(), mapping.RepositoryID, "default")
			require.NoError(t, err)
			require.Equal(t, 1, rpc.writes, "replacement must preserve the admitted canonical tip")
		})
	}
}

func TestRBACRecoveryTransportIsCanonicalBoundedAndIndependentOfServingReadiness(t *testing.T) {
	store, _, validator, _, _ := newRBACBootstrapFixture(t)
	namespace, err := store.GetNamespaceByName(t.Context(), "gitstore-system")
	require.NoError(t, err)
	mapping, err := store.LookupRepository(t.Context(), namespace.Name, bootstrapRepositoryName)
	require.NoError(t, err)
	rpc := &recoveryRPC{file: []byte("fixture")}
	client := &gitclient.Client{Git: rpc, Registry: validator.Capture.Registry}
	recovery := &rbacRecoveryGit{store: store, client: client}
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "ordinary-user", AuthMethod: "static-users"})
	tip, err := recovery.ResolveRef(ctx, mapping.RepositoryID, "refs/heads/main")
	require.NoError(t, err)
	paths, err := recovery.ListFiles(ctx, mapping.RepositoryID, "rbac", tip)
	require.NoError(t, err)
	require.Len(t, paths, 1)
	content, err := recovery.ReadFile(ctx, mapping.RepositoryID, paths[0], tip)
	require.NoError(t, err)
	require.Equal(t, rpc.file, content)
	ordinary, err := client.Registry.AuthZ().Authorize(ctx, auth.PrincipalFromContext(ctx), "repository.contents.read", auth.ResourceContext{Kind: "repository"})
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeDeny, ordinary.Outcome, "recovery must not change serving authorization")
	before := rpc.calls.Load()
	_, err = recovery.ReadFile(ctx, mapping.RepositoryID, "products/private.md", tip)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	_, err = recovery.ReadFile(ctx, mapping.RepositoryID, "rbac/roles/wrong-scope.yaml", tip)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	_, err = recovery.ListFiles(ctx, mapping.RepositoryID, "", tip)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	_, err = recovery.ResolveRef(ctx, mapping.RepositoryID, "refs/heads/review")
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	_, err = recovery.ReadFile(ctx, mapping.RepositoryID, paths[0], "refs/heads/main")
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	require.Equal(t, before, rpc.calls.Load(), "out-of-purpose work must not reach Git")
	rpc.file = make([]byte, rbac.MaxManifestBytes+1)
	_, err = recovery.ReadFile(ctx, mapping.RepositoryID, paths[0], tip)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	client.Registry = auth.NewProviderRegistry(nil, allowall.New(zap.NewNop()), nil)
	before = rpc.calls.Load()
	_, err = recovery.ResolveRef(ctx, mapping.RepositoryID, "refs/heads/main")
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	require.Equal(t, before, rpc.calls.Load())
}

func TestRBACRecoveryTransportCanPublishOnlyThePersistedPendingSeed(t *testing.T) {
	store, _, validator, _, _ := newRBACBootstrapFixture(t)
	namespace, err := store.GetNamespaceByName(t.Context(), "gitstore-system")
	require.NoError(t, err)
	mapping, err := store.LookupRepository(t.Context(), namespace.Name, bootstrapRepositoryName)
	require.NoError(t, err)
	rpc := &recoveryRPC{}
	recovery := &rbacRecoveryGit{store: store, client: &gitclient.Client{Git: rpc, Registry: validator.Capture.Registry}}
	content, err := initialAdministratorManifest("first-admin")
	require.NoError(t, err)
	params := gitclient.CommitFileAtRevisionParams{CommitFileParams: gitclient.CommitFileParams{
		Path: "rbac/clusterrolebindings/initial-admin.yaml", Content: content,
		AuthorName: bootstrapActor, CommitMessage: "Initialize RBAC for " + namespace.UID,
	}, RefName: "refs/heads/main", ExpectedCommitSHA: strings.Repeat("a", 40), RetainReceipt: true}
	_, err = recovery.CommitFileAtRevisionForRepo(t.Context(), mapping.RepositoryID, params)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	require.Zero(t, rpc.calls.Load())
	receipt := datastore.NamespaceRBACBootstrap{NamespaceUID: namespace.UID, RepositoryID: mapping.RepositoryID,
		GitRef: params.RefName, ExpectedCommit: params.ExpectedCommitSHA, Subject: "first-admin"}
	require.NoError(t, persistRBACBootstrap(t.Context(), store, namespace, receipt))
	commit, err := recovery.CommitFileAtRevisionForRepo(t.Context(), mapping.RepositoryID, params)
	require.NoError(t, err)
	require.NotEmpty(t, commit)
	altered := params
	altered.Content, err = initialAdministratorManifest("different-admin")
	require.NoError(t, err)
	_, err = recovery.CommitFileAtRevisionForRepo(t.Context(), mapping.RepositoryID, altered)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	altered = params
	altered.Path = "rbac/clusterrolebindings/arbitrary.yaml"
	_, err = recovery.CommitFileAtRevisionForRepo(t.Context(), mapping.RepositoryID, altered)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	require.EqualValues(t, 1, rpc.calls.Load())
	receipt.Commit = commit
	require.NoError(t, persistRBACBootstrap(t.Context(), store, namespace, receipt))
	_, err = recovery.CommitFileAtRevisionForRepo(t.Context(), mapping.RepositoryID, params)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	require.EqualValues(t, 1, rpc.calls.Load(), "completed initialization must not become a general write capability")
}
