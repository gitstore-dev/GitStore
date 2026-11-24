// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/allowall"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type recoveryRPC struct {
	gitv1.GitServiceClient
	calls        atomic.Int32
	batches      atomic.Int32
	file         []byte
	files        map[string][]byte
	read         func()
	rewriteFiles func(*gitv1.GetFilesResponse) *gitv1.GetFilesResponse
}

type batchOriginFacts struct {
	datastore.Datastore
	origins         map[string]rbac.SnapshotOrigin
	reads           atomic.Int32
	invalid         string
	beforeNamespace func(context.Context)
}

type bulkOriginFacts struct {
	*batchOriginFacts
	read datastore.RBACOriginFactsReader
}

func (f *bulkOriginFacts) RBACOriginFactsReader() datastore.RBACOriginFactsReader {
	return f.read
}

func TestRBACOriginBulkFactsRemainLiveAndFailClosed(t *testing.T) {
	_, _, validator, _, _ := newRBACBootstrapFixture(t)
	facts := &bulkOriginFacts{batchOriginFacts: &batchOriginFacts{origins: map[string]rbac.SnapshotOrigin{}}}
	checks := make([]rbac.OriginCheck, rbac.MaxOriginChecks)
	for i := range checks {
		id := uuid.NewString()
		origin := rbac.SnapshotOrigin{NamespaceUID: id, Namespace: fmt.Sprintf("tenant-%d", i), NamespaceVersion: "1",
			Active: true, RepositoryID: uuid.NewString(), GitRef: "refs/heads/main",
			GitRevision: "main@sha1:" + strings.Repeat("a", 40), TreeDigest: strings.Repeat("b", 64)}
		facts.origins[id], checks[i] = origin, rbac.OriginCheck{Tier: id, Expected: origin}
	}
	rpc := &refProofRPC{requests: make(chan *gitv1.ResolveRefsRequest, 4), release: make(chan struct{})}
	close(rpc.release)
	recovery := &rbacRecoveryGit{store: facts, client: &gitclient.Client{Git: rpc, Registry: validator.Capture.Registry}}
	reader := rbac.OriginReader{Store: facts, Git: recovery}
	var calls int
	var rewrite func([]datastore.RBACOriginFacts) ([]datastore.RBACOriginFacts, error)
	facts.read = func(ctx context.Context, lookups []datastore.RBACOriginLookup) ([]datastore.RBACOriginFacts, error) {
		calls++
		require.Len(t, lookups, len(checks))
		result := make([]datastore.RBACOriginFacts, len(lookups))
		for i, lookup := range lookups {
			origin := facts.origins[lookup.NamespaceUID]
			require.Equal(t, origin.Namespace, lookup.Namespace)
			require.Equal(t, origin.RepositoryID, lookup.RepositoryID)
			result[i] = datastore.RBACOriginFacts{
				Namespace: &datastore.Namespace{UID: origin.NamespaceUID, Name: origin.Namespace, ResourceVersion: origin.NamespaceVersion},
				Mapping: &datastore.NamespaceMapping{Namespace: origin.Namespace, Name: bootstrapRepositoryName,
					RepositoryID: origin.RepositoryID},
				Repository: &datastore.Repository{UID: origin.RepositoryID, Namespace: origin.Namespace,
					Name: bootstrapRepositoryName, DefaultBranch: "main"},
			}
		}
		if rewrite != nil {
			return rewrite(result)
		}
		return result, ctx.Err()
	}
	for range 2 {
		for _, err := range reader.CheckOrigins(t.Context(), checks) {
			require.NoError(t, err)
		}
		request := <-rpc.requests
		require.Len(t, request.Refs, len(checks))
		for i, ref := range request.Refs {
			require.Equal(t, checks[i].Expected.RepositoryID, ref.RepositoryId)
		}
	}
	require.Equal(t, 2, calls, "live facts must be reread, not cached")
	require.Zero(t, facts.reads.Load(), "bulk-capable stores must not fan out individual reads")
	for name, mutate := range map[string]func(*datastore.RBACOriginFacts){
		"missing": func(f *datastore.RBACOriginFacts) { *f = datastore.RBACOriginFacts{} },
		"namespace retired": func(f *datastore.RBACOriginFacts) {
			now := time.Now()
			f.Namespace.DeletionTimestamp = &now
		},
		"namespace deletion intent": func(f *datastore.RBACOriginFacts) {
			f.Namespace.Status = []byte(`{"deletionIntent":{"requestedAt":"2026-01-01T00:00:00Z"}}`)
		},
		"namespace replaced": func(f *datastore.RBACOriginFacts) { f.Namespace.UID = uuid.NewString() },
		"namespace changed":  func(f *datastore.RBACOriginFacts) { f.Namespace.ResourceVersion = "2" },
		"mapping replaced":   func(f *datastore.RBACOriginFacts) { f.Mapping.RepositoryID = uuid.NewString() },
		"wrong namespace":    func(f *datastore.RBACOriginFacts) { f.Mapping.Namespace = "other" },
		"repository retired": func(f *datastore.RBACOriginFacts) {
			now := time.Now()
			f.Repository.DeletionTimestamp = &now
		},
		"repository replaced": func(f *datastore.RBACOriginFacts) { f.Repository.UID = uuid.NewString() },
		"external source":     func(f *datastore.RBACOriginFacts) { f.Repository.SourcePath = "external" },
	} {
		t.Run(name, func(t *testing.T) {
			rewrite = func(result []datastore.RBACOriginFacts) ([]datastore.RBACOriginFacts, error) {
				mutate(&result[0])
				return result, nil
			}
			results := reader.CheckOrigins(t.Context(), checks)
			require.ErrorIs(t, results[0], rbac.ErrUnavailable)
			for _, err := range results[1:] {
				require.NoError(t, err, "one bad identity must not erase unrelated proofs")
			}
			require.NotEmpty(t, rpc.requests)
			<-rpc.requests
		})
	}
	for name, corrupt := range map[string]func([]datastore.RBACOriginFacts) ([]datastore.RBACOriginFacts, error){
		"read failure": func(f []datastore.RBACOriginFacts) ([]datastore.RBACOriginFacts, error) {
			return f, fmt.Errorf("unavailable live read")
		},
		"short response": func(f []datastore.RBACOriginFacts) ([]datastore.RBACOriginFacts, error) { return f[:len(f)-1], nil },
		"wrong association": func(f []datastore.RBACOriginFacts) ([]datastore.RBACOriginFacts, error) {
			return append(f[1:], f[0]), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			rewrite = corrupt
			for _, err := range reader.CheckOrigins(t.Context(), checks) {
				require.ErrorIs(t, err, rbac.ErrUnavailable)
			}
			require.Empty(t, rpc.requests, "unverified live facts cannot authorize an RPC")
		})
	}
}

func (f *batchOriginFacts) GetNamespace(ctx context.Context, id string) (*datastore.Namespace, error) {
	if f.beforeNamespace != nil {
		f.beforeNamespace(ctx)
	}
	f.reads.Add(1)
	o := f.origins[id]
	return &datastore.Namespace{UID: o.NamespaceUID, Name: o.Namespace, ResourceVersion: o.NamespaceVersion}, nil
}

func (f *batchOriginFacts) LookupRepository(_ context.Context, namespace, _ string) (*datastore.NamespaceMapping, error) {
	f.reads.Add(1)
	for _, o := range f.origins {
		if o.Namespace == namespace {
			return &datastore.NamespaceMapping{Namespace: namespace, Name: bootstrapRepositoryName, RepositoryID: o.RepositoryID}, nil
		}
	}
	return nil, rbac.ErrUnavailable
}

func (f *batchOriginFacts) GetRepository(_ context.Context, id string) (*datastore.Repository, error) {
	f.reads.Add(1)
	for tier, o := range f.origins {
		if o.RepositoryID == id {
			repository := &datastore.Repository{UID: id, Namespace: o.Namespace, Name: bootstrapRepositoryName, DefaultBranch: "main"}
			if tier == f.invalid {
				repository.SourcePath = "external"
			}
			return repository, nil
		}
	}
	return nil, rbac.ErrUnavailable
}

func TestRBACOriginBatchAmortizesSignedProofsButRereadsEveryIdentity(t *testing.T) {
	_, _, validator, _, _ := newRBACBootstrapFixture(t)
	facts := &batchOriginFacts{origins: map[string]rbac.SnapshotOrigin{}}
	checks := make([]rbac.OriginCheck, rbac.MaxOriginChecks)
	for i := range checks {
		id := uuid.NewString()
		origin := rbac.SnapshotOrigin{NamespaceUID: id, Namespace: fmt.Sprintf("tenant-%d", i), NamespaceVersion: "1",
			Active: true, RepositoryID: uuid.NewString(), GitRef: "refs/heads/main",
			GitRevision: "main@sha1:" + strings.Repeat("a", 40), TreeDigest: strings.Repeat("b", 64)}
		facts.origins[id], checks[i] = origin, rbac.OriginCheck{Tier: id, Expected: origin}
	}
	rpc := &refProofRPC{requests: make(chan *gitv1.ResolveRefsRequest, 4), release: make(chan struct{})}
	close(rpc.release)
	recovery := &rbacRecoveryGit{store: facts, client: &gitclient.Client{Git: rpc, Registry: validator.Capture.Registry}}
	reader := rbac.OriginReader{Store: facts, Git: recovery}
	for _, err := range reader.CheckOrigins(t.Context(), checks) {
		require.NoError(t, err)
	}
	require.Len(t, (<-rpc.requests).Refs, len(checks), "a worker must submit a complete signed batch, not one RPC per tier")
	require.EqualValues(t, 3*len(checks), facts.reads.Load())
	facts.invalid = checks[0].Tier
	results := reader.CheckOrigins(t.Context(), checks)
	require.ErrorIs(t, results[0], rbac.ErrUnavailable)
	for _, err := range results[1:] {
		require.NoError(t, err, "one invalid identity must not erase unrelated successful proofs")
	}
	require.Len(t, (<-rpc.requests).Refs, len(checks)-1, "invalid live mapping facts cannot authorize an RPC")
	require.EqualValues(t, 6*len(checks), facts.reads.Load(), "the previous batch cannot cache freshness")
	for _, err := range reader.CheckOrigins(t.Context(), append(checks, checks[0])) {
		require.ErrorIs(t, err, rbac.ErrUnavailable)
	}
	require.Empty(t, rpc.requests, "an oversized batch must be refused before native work")

	t.Run("bounded concurrent live identities", func(t *testing.T) {
		facts.invalid = ""
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		entered := make(chan struct{}, len(checks))
		release := make(chan struct{})
		var active, maximum atomic.Int32
		facts.beforeNamespace = func(ctx context.Context) {
			current := active.Add(1)
			defer active.Add(-1)
			for previous := maximum.Load(); current > previous; previous = maximum.Load() {
				if maximum.CompareAndSwap(previous, current) {
					break
				}
			}
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		done := make(chan []error, 1)
		go func() { done <- reader.CheckOrigins(ctx, checks) }()
		started := 0
		for started < 4 && ctx.Err() == nil {
			select {
			case <-entered:
				started++
			case <-ctx.Done():
			}
		}
		close(release)
		results := <-done
		facts.beforeNamespace = nil
		require.Equal(t, 4, started, "one slow identity must not serialize the entire proof batch")
		require.LessOrEqual(t, maximum.Load(), int32(4), "live identity work must remain bounded")
		for _, err := range results {
			require.NoError(t, err)
		}
		request := <-rpc.requests
		require.Len(t, request.Refs, len(checks))
		for i, ref := range request.Refs {
			require.Equal(t, checks[i].Expected.RepositoryID, ref.RepositoryId, "parallel reads must preserve proof association")
		}
	})
	t.Run("contention still checks live identity", func(t *testing.T) {
		facts.invalid = ""
		rpc.rewrite = func(response *gitv1.ResolveRefsResponse) {
			response.Proofs[0].CommitSha, response.Proofs[0].Busy = "", true
		}
		for _, changed := range []bool{false, true} {
			if changed {
				current := facts.origins[checks[0].Tier]
				current.NamespaceVersion = "another"
				facts.origins[checks[0].Tier] = current
			}
			results := reader.CheckOrigins(t.Context(), checks)
			<-rpc.requests
			require.ErrorIs(t, results[0], rbac.ErrUnavailable)
			if changed {
				require.NotErrorIs(t, results[0], rbac.ErrOriginRefBusy)
			} else {
				require.ErrorIs(t, results[0], rbac.ErrOriginRefBusy)
			}
			for _, err := range results[1:] {
				require.NoError(t, err, "one contended ref must not erase unrelated proofs")
			}
		}
	})
}

type refProofRPC struct {
	gitv1.GitServiceClient
	requests chan *gitv1.ResolveRefsRequest
	release  chan struct{}
	rewrite  func(*gitv1.ResolveRefsResponse)
}

func (r *refProofRPC) ResolveRefs(ctx context.Context, request *gitv1.ResolveRefsRequest, _ ...grpc.CallOption) (*gitv1.ResolveRefsResponse, error) {
	r.requests <- request
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.release:
	}
	response := &gitv1.ResolveRefsResponse{}
	for _, query := range request.Refs {
		if query.Authorization == nil || query.Authorization.RepositoryId != query.RepositoryId ||
			query.Authorization.Actor.Subject != "system:api" || query.Authorization.Action != "repository.contents.read" {
			return nil, rbac.ErrUnavailable
		}
		response.Proofs = append(response.Proofs, &gitv1.RepositoryRefProof{
			RepositoryId: query.RepositoryId, Ref: query.Ref, CommitSha: strings.Repeat("a", 40),
		})
	}
	if r.rewrite != nil {
		r.rewrite(response)
	}
	return response, nil
}

func TestRecoveryRefProofRejectsMismatchedOrMalformedResults(t *testing.T) {
	for _, scenario := range []string{"repository", "branch", "digest", "count", "nil", "missing", "busy", "busy-with-proof"} {
		t.Run(scenario, func(t *testing.T) {
			rpc := &refProofRPC{requests: make(chan *gitv1.ResolveRefsRequest, 1), release: make(chan struct{})}
			close(rpc.release)
			rpc.rewrite = func(response *gitv1.ResolveRefsResponse) {
				switch scenario {
				case "repository":
					response.Proofs[0].RepositoryId = "another"
				case "branch":
					response.Proofs[0].Ref = "refs/heads/another"
				case "digest":
					response.Proofs[0].CommitSha = strings.Repeat("A", 40)
				case "count":
					response.Proofs = nil
				case "nil":
					response.Proofs[0] = nil
				case "missing":
					response.Proofs[0].CommitSha = ""
				case "busy":
					response.Proofs[0].CommitSha = ""
					response.Proofs[0].Busy = true
				case "busy-with-proof":
					response.Proofs[0].Busy = true
				}
			}
			recovery := &rbacRecoveryGit{client: &gitclient.Client{Git: rpc}}
			commit, err := recovery.resolveRef(t.Context(), "repository", "refs/heads/main", false)
			require.Empty(t, commit)
			if scenario == "missing" {
				require.Equal(t, codes.NotFound, grpcstatus.Code(err))
				require.NotErrorIs(t, err, rbac.ErrOriginRefBusy)
			} else if scenario == "busy" {
				require.ErrorIs(t, err, rbac.ErrOriginRefBusy)
			} else {
				require.ErrorIs(t, err, rbac.ErrUnavailable)
			}
		})
	}
}

func TestRBACContendedOriginStillRequiresTheSameLiveIdentity(t *testing.T) {
	store, _, validator, _, _ := newRBACBootstrapFixture(t)
	facts := &recoveryTreeFacts{Datastore: store}
	manifest, err := initialAdministratorManifest("first-admin")
	require.NoError(t, err)
	recovery := &rbacRecoveryGit{store: facts, client: &gitclient.Client{
		Git: &recoveryRPC{file: manifest}, Registry: validator.Capture.Registry,
	}}
	reader := rbac.OriginReader{Store: facts, Git: recovery}
	origin, _, err := reader.ReadTree(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	for _, scenario := range []string{"same", "namespace-version", "branch"} {
		t.Run(scenario, func(t *testing.T) {
			rpc := &refProofRPC{requests: make(chan *gitv1.ResolveRefsRequest, 4), release: make(chan struct{})}
			close(rpc.release)
			rpc.rewrite = func(response *gitv1.ResolveRefsResponse) {
				response.Proofs[0].CommitSha, response.Proofs[0].Busy = "", true
			}
			recovery.client.Git = rpc
			expected := origin
			switch scenario {
			case "namespace-version":
				expected.NamespaceVersion = "another"
			case "branch":
				expected.GitRef = "refs/heads/another"
			}
			err := reader.CheckOrigin(t.Context(), rbac.ClusterTierID, expected)
			require.ErrorIs(t, err, rbac.ErrUnavailable)
			if scenario == "same" {
				require.ErrorIs(t, err, rbac.ErrOriginRefBusy)
			} else {
				require.NotErrorIs(t, err, rbac.ErrOriginRefBusy)
			}
		})
	}
}

func TestRecoveryRefProofCoalescingIsBoundedAndCancellationSafe(t *testing.T) {
	rpc := &refProofRPC{requests: make(chan *gitv1.ResolveRefsRequest, 32), release: make(chan struct{})}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(rpc.release) }) })
	recovery := &rbacRecoveryGit{client: &gitclient.Client{Git: rpc}}
	results := make(chan error, 32)
	prove := func(ctx context.Context, id string) {
		commit, err := recovery.resolveRef(ctx, id, "refs/heads/main", false)
		if err == nil && commit != strings.Repeat("a", 40) {
			err = fmt.Errorf("incorrect canonical ref proof")
		}
		results <- err
	}
	go prove(t.Context(), "first")
	first := <-rpc.requests
	require.Len(t, first.Refs, 1)
	go prove(t.Context(), "second")
	select {
	case second := <-rpc.requests:
		require.Len(t, second.Refs, 1)
		require.Equal(t, "second", second.Refs[0].RepositoryId)
	case <-time.After(time.Second):
		t.Fatal("an independent Namespace proof queued behind a blocked batch")
	}
	cancelCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go prove(cancelCtx, "canceled")
	for index := range 29 {
		go prove(t.Context(), fmt.Sprintf("repository-%d", index))
	}
	require.Eventually(t, func() bool {
		recovery.refsMu.Lock()
		defer recovery.refsMu.Unlock()
		return recovery.refsInFlight == 32
	}, time.Second, time.Millisecond)
	_, err := recovery.resolveRef(t.Context(), "overflow", "refs/heads/main", false)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	require.Empty(t, rpc.requests, "Namespace proofs must use at most two concurrent batches")
	rootResult := make(chan error, 1)
	go func() {
		_, err := recovery.resolveRef(t.Context(), "root", "refs/heads/main", true)
		rootResult <- err
	}()
	select {
	case rootRequest := <-rpc.requests:
		require.Len(t, rootRequest.Refs, 1)
		require.Equal(t, "root", rootRequest.Refs[0].RepositoryId,
			"shared root proofs must not queue behind a blocked or full Namespace batch")
	case <-time.After(time.Second):
		t.Fatal("Namespace batching consumed the reserved root proof path")
	}
	cancel()
	require.ErrorIs(t, <-results, context.Canceled)
	release.Do(func() { close(rpc.release) })
	require.NoError(t, <-rootResult)
	for range 31 {
		require.NoError(t, <-results)
	}
	queued := <-rpc.requests
	require.Len(t, queued.Refs, 29, "the canceled queued proof must not consume native work")
	require.Empty(t, rpc.requests, "independent canonical refs share one signed RPC per bounded batch")
	require.Eventually(t, func() bool {
		recovery.refsMu.Lock()
		defer recovery.refsMu.Unlock()
		return !recovery.refsRunning && recovery.refsInFlight == 0
	}, time.Second, time.Millisecond)
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
		if r.files != nil {
			response.Files = nil
			for path, content := range r.files {
				response.Files = append(response.Files, &gitv1.FileEntry{Path: path, SizeBytes: uint64(len(content))})
			}
		}
	}
	return response, nil
}

func (r *recoveryRPC) ResolveRefs(ctx context.Context, request *gitv1.ResolveRefsRequest, _ ...grpc.CallOption) (*gitv1.ResolveRefsResponse, error) {
	response := &gitv1.ResolveRefsResponse{}
	for _, query := range request.Refs {
		r.check(ctx, query.Authorization, "repository.contents.read")
		response.Proofs = append(response.Proofs, &gitv1.RepositoryRefProof{
			RepositoryId: query.RepositoryId, Ref: query.Ref, CommitSha: strings.Repeat("a", 40),
		})
	}
	return response, nil
}

func (r *recoveryRPC) GetFile(ctx context.Context, request *gitv1.GetFileRequest, _ ...grpc.CallOption) (*gitv1.GetFileResponse, error) {
	r.check(ctx, request.Authorization, "repository.contents.read")
	if r.read != nil {
		r.read()
	}
	content := r.file
	if r.files != nil {
		content = r.files[request.Path]
	}
	return &gitv1.GetFileResponse{Content: content}, nil
}

func (r *recoveryRPC) GetFiles(ctx context.Context, request *gitv1.GetFilesRequest, _ ...grpc.CallOption) (*gitv1.GetFilesResponse, error) {
	r.check(ctx, request.Authorization, "repository.contents.read")
	r.batches.Add(1)
	if len(request.Paths) == 0 || len(request.Paths) > 32 || !recoveryCommit(request.CommitSha) {
		panic("immutable reads must have bounded paths and a pinned commit")
	}
	response := &gitv1.GetFilesResponse{CommitSha: request.CommitSha}
	for _, path := range request.Paths {
		if r.read != nil {
			r.read()
		}
		content := r.file
		if r.files != nil {
			content = r.files[path]
		}
		response.Files = append(response.Files, &gitv1.GetFileResponse{
			Path: path, Content: content, SizeBytes: uint64(len(content)), BlobSha: strings.Repeat("b", 40),
		})
	}
	if r.rewriteFiles != nil {
		response = r.rewriteFiles(response)
	}
	return response, nil
}

func TestRecoveryManifestBatchesRejectMalformedResultsAndOutOfScopeInputs(t *testing.T) {
	for _, scenario := range []string{"valid", "commit", "path", "count", "nil", "nil-file", "oversize", "size", "blob"} {
		t.Run(scenario, func(t *testing.T) {
			store, _, validator, _, _ := newRBACBootstrapFixture(t)
			mapping, err := store.LookupRepository(t.Context(), "gitstore-system", bootstrapRepositoryName)
			require.NoError(t, err)
			rpc := &recoveryRPC{file: []byte("fixture")}
			rpc.rewriteFiles = func(response *gitv1.GetFilesResponse) *gitv1.GetFilesResponse {
				switch scenario {
				case "commit":
					response.CommitSha = strings.Repeat("c", 40)
				case "path":
					response.Files[1].Path = response.Files[0].Path
				case "count":
					response.Files = response.Files[:1]
				case "nil":
					return nil
				case "nil-file":
					response.Files[1] = nil
				case "oversize":
					response.Files[1].Content = make([]byte, rbac.MaxManifestBytes+1)
					response.Files[1].SizeBytes = uint64(len(response.Files[1].Content))
				case "size":
					response.Files[1].SizeBytes++
				case "blob":
					response.Files[1].BlobSha = strings.Repeat("B", 40)
				}
				return response
			}
			recovery := &rbacRecoveryGit{store: store, client: &gitclient.Client{Git: rpc, Registry: validator.Capture.Registry}}
			read, err := recovery.OpenFileBatches(t.Context(), mapping.RepositoryID, strings.Repeat("a", 40))
			require.NoError(t, err)
			paths := []string{"rbac/clusterrolebindings/initial-admin.yaml", "rbac/clusterrolebindings/another.yaml"}
			contents, err := read(t.Context(), paths)
			if scenario != "valid" {
				require.ErrorIs(t, err, rbac.ErrUnavailable)
				require.Empty(t, contents, "a malformed later item must not publish the valid prefix")
				return
			}
			require.NoError(t, err)
			require.Equal(t, [][]byte{rpc.file, rpc.file}, contents)
			before := rpc.calls.Load()
			for _, invalid := range [][]string{nil, {paths[0], paths[0]}, {"products/private.md"},
				{"rbac/roles/wrong-scope.yaml"}, {"rbac/clusterrolebindings/../another.yaml"}, make([]string, 33)} {
				contents, err := read(t.Context(), invalid)
				require.ErrorIs(t, err, rbac.ErrUnavailable)
				require.Empty(t, contents)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			contents, err = read(ctx, paths)
			require.ErrorIs(t, err, rbac.ErrUnavailable)
			require.Empty(t, contents)
			require.Equal(t, before, rpc.calls.Load(), "invalid or canceled reads must not reach Git")
			read, err = recovery.OpenFileBatches(t.Context(), mapping.RepositoryID, "refs/heads/main")
			require.ErrorIs(t, err, rbac.ErrUnavailable)
			require.Nil(t, read)
		})
	}
}

type recoveryTreeFacts struct {
	datastore.Datastore
	reads   atomic.Uint32
	changed atomic.Bool
}

func (f *recoveryTreeFacts) GetNamespaceByName(ctx context.Context, name string) (*datastore.Namespace, error) {
	f.reads.Add(1)
	namespace, err := f.Datastore.GetNamespaceByName(ctx, name)
	if err == nil && namespace != nil && f.changed.Load() {
		copy := *namespace
		copy.ResourceVersion = "changed-during-immutable-read"
		namespace = &copy
	}
	return namespace, err
}

func (f *recoveryTreeFacts) GetRepository(ctx context.Context, id string) (*datastore.Repository, error) {
	f.reads.Add(1)
	return f.Datastore.GetRepository(ctx, id)
}

func (f *recoveryTreeFacts) LookupRepository(ctx context.Context, namespace, name string) (*datastore.NamespaceMapping, error) {
	f.reads.Add(1)
	return f.Datastore.LookupRepository(ctx, namespace, name)
}

func TestRBACRecoveryTreeDoesNotRepeatLiveMappingReadsPerImmutableFile(t *testing.T) {
	var baseline uint32
	for _, count := range []int{1, 20, 65} {
		t.Run(fmt.Sprintf("%d-files", count), func(t *testing.T) {
			store, _, validator, _, _ := newRBACBootstrapFixture(t)
			facts := &recoveryTreeFacts{Datastore: store}
			rpc := &recoveryRPC{files: make(map[string][]byte)}
			manifest, err := initialAdministratorManifest("first-admin")
			require.NoError(t, err)
			for index := range count {
				name := fmt.Sprintf("binding-%d", index)
				rpc.files["rbac/clusterrolebindings/"+name+".yaml"] =
					[]byte(strings.ReplaceAll(string(manifest), "initial-admin", name))
			}
			recovery := &rbacRecoveryGit{store: facts, client: &gitclient.Client{Git: rpc, Registry: validator.Capture.Registry}}
			reader := rbac.OriginReader{Store: facts, Git: recovery}
			_, resources, err := reader.ReadTree(t.Context(), rbac.ClusterTierID)
			require.NoError(t, err)
			require.Len(t, resources, count)
			require.EqualValues(t, (count+31)/32, rpc.batches.Load(),
				"canonical tree reads must amortize signed RPCs in bounded immutable batches")
			if count == 1 {
				baseline = facts.reads.Load()
			} else {
				require.Equal(t, baseline, facts.reads.Load(), "live identity work must not grow with immutable file count")
			}
			rpc.read = func() { facts.changed.Store(true) }
			_, resources, err = reader.ReadTree(t.Context(), rbac.ClusterTierID)
			require.ErrorIs(t, err, rbac.ErrUnavailable, "a scoped read must still reject a changed live identity")
			require.Empty(t, resources, "no candidate from a changed identity may escape the final check")
		})
	}
}

func TestRBACOriginProbeReusesOnlyItsFreshAuthoritativeMapping(t *testing.T) {
	store, _, validator, _, _ := newRBACBootstrapFixture(t)
	facts := &recoveryTreeFacts{Datastore: store}
	manifest, err := initialAdministratorManifest("first-admin")
	require.NoError(t, err)
	rpc := &recoveryRPC{file: manifest}
	recovery := &rbacRecoveryGit{store: facts, client: &gitclient.Client{Git: rpc, Registry: validator.Capture.Registry}}
	reader := rbac.OriginReader{Store: facts, Git: recovery}
	origin, _, err := reader.ReadTree(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	facts.reads.Store(0)
	calls := rpc.calls.Load()
	require.NoError(t, reader.CheckOrigin(t.Context(), rbac.ClusterTierID, origin))
	require.EqualValues(t, 3, facts.reads.Load(), "one probe must freshly read Namespace, mapping and repository exactly once")
	require.Equal(t, calls+1, rpc.calls.Load(), "the canonical ref must still be checked on every probe")
	facts.changed.Store(true)
	require.ErrorIs(t, reader.CheckOrigin(t.Context(), rbac.ClusterTierID, origin), rbac.ErrUnavailable,
		"the previous probe's mapping cannot become a cached freshness proof")

	namespace, err := store.GetNamespaceByName(t.Context(), "gitstore-system")
	require.NoError(t, err)
	mapping, err := store.LookupRepository(t.Context(), namespace.Name, bootstrapRepositoryName)
	require.NoError(t, err)
	repository, err := store.GetRepository(t.Context(), mapping.RepositoryID)
	require.NoError(t, err)
	for _, scenario := range []string{"replacement", "external-tree", "terminating"} {
		t.Run(scenario, func(t *testing.T) {
			current := *repository
			switch scenario {
			case "replacement":
				current.UID = "replacement"
			case "external-tree":
				current.SourcePath = "external"
			case "terminating":
				now := time.Now()
				current.DeletionTimestamp = &now
			}
			before := rpc.calls.Load()
			_, err := recovery.ResolveOriginRef(t.Context(), namespace, mapping, &current)
			require.ErrorIs(t, err, rbac.ErrUnavailable)
			require.Equal(t, before, rpc.calls.Load(), "changed canonical identity must be refused before an internal RPC")
		})
	}
}

type parallelOriginFacts struct {
	*recoveryTreeFacts
	entered            chan string
	release            chan struct{}
	mappingReplacement bool
}

func (f *parallelOriginFacts) wait(ctx context.Context, name string) error {
	f.entered <- name
	select {
	case <-f.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *parallelOriginFacts) GetNamespaceByName(ctx context.Context, name string) (*datastore.Namespace, error) {
	if err := f.wait(ctx, "namespace"); err != nil {
		return nil, err
	}
	return f.recoveryTreeFacts.GetNamespaceByName(ctx, name)
}

func (f *parallelOriginFacts) LookupRepository(ctx context.Context, namespace, name string) (*datastore.NamespaceMapping, error) {
	if err := f.wait(ctx, "mapping"); err != nil {
		return nil, err
	}
	mapping, err := f.recoveryTreeFacts.LookupRepository(ctx, namespace, name)
	if err == nil && mapping != nil && f.mappingReplacement {
		copy := *mapping
		copy.RepositoryID = "replacement"
		mapping = &copy
	}
	return mapping, err
}

func (f *parallelOriginFacts) GetRepository(ctx context.Context, id string) (*datastore.Repository, error) {
	if err := f.wait(ctx, "repository"); err != nil {
		return nil, err
	}
	return f.recoveryTreeFacts.GetRepository(ctx, id)
}

func TestRBACOriginProbeOverlapsFreshReadsWithoutTrustingPriorFacts(t *testing.T) {
	for _, scenario := range []string{"unchanged", "namespace-version", "mapping-replacement", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			store, _, validator, _, _ := newRBACBootstrapFixture(t)
			manifest, err := initialAdministratorManifest("first-admin")
			require.NoError(t, err)
			rpc := &recoveryRPC{file: manifest}
			recovery := &rbacRecoveryGit{store: store, client: &gitclient.Client{Git: rpc, Registry: validator.Capture.Registry}}
			reader := rbac.OriginReader{Store: store, Git: recovery}
			origin, _, err := reader.ReadTree(t.Context(), rbac.ClusterTierID)
			require.NoError(t, err)
			facts := &parallelOriginFacts{
				recoveryTreeFacts:  &recoveryTreeFacts{Datastore: store},
				entered:            make(chan string, 3),
				release:            make(chan struct{}),
				mappingReplacement: scenario == "mapping-replacement",
			}
			facts.changed.Store(scenario == "namespace-version")
			reader.Store, recovery.store = facts, facts
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			var once sync.Once
			release := func() { once.Do(func() { close(facts.release) }) }
			defer release()
			done := make(chan error, 1)
			calls := rpc.calls.Load()
			go func() { done <- reader.CheckOrigin(ctx, rbac.ClusterTierID, origin) }()
			seen := make(map[string]bool)
			for range 3 {
				select {
				case name := <-facts.entered:
					require.False(t, seen[name])
					seen[name] = true
				case <-ctx.Done():
					t.Fatal("all three fresh reads must begin without waiting for another read")
				}
			}
			require.Zero(t, facts.reads.Load(), "no previous probe fact can substitute for blocked live reads")
			if scenario == "canceled" {
				cancel()
			} else {
				release()
			}
			err = <-done
			if scenario != "unchanged" {
				require.ErrorIs(t, err, rbac.ErrUnavailable)
			} else {
				require.NoError(t, err)
			}
			if scenario == "canceled" {
				require.Zero(t, facts.reads.Load())
			} else {
				require.EqualValues(t, 3, facts.reads.Load())
			}
			if scenario == "mapping-replacement" || scenario == "canceled" {
				require.Equal(t, calls, rpc.calls.Load(), "unverified or canceled facts must never authorize a ref RPC")
			}
		})
	}
}

func (r *recoveryRPC) CommitFileAtRevision(ctx context.Context, request *gitv1.CommitFileAtRevisionRequest, _ ...grpc.CallOption) (*gitv1.CommitFileAtRevisionResponse, error) {
	r.check(ctx, request.File.Authorization, "repository.contents.write")
	if !request.RetainReceipt {
		panic("bootstrap publication must retain its durable receipt")
	}
	return &gitv1.CommitFileAtRevisionResponse{CommitSha: strings.Repeat("b", 40)}, nil
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

func (r *unbornRecoveryRPC) ResolveRefs(ctx context.Context, request *gitv1.ResolveRefsRequest, options ...grpc.CallOption) (*gitv1.ResolveRefsResponse, error) {
	if r.unborn {
		response := &gitv1.ResolveRefsResponse{}
		for _, query := range request.Refs {
			r.check(ctx, query.Authorization, "repository.contents.read")
			response.Proofs = append(response.Proofs, &gitv1.RepositoryRefProof{RepositoryId: query.RepositoryId, Ref: query.Ref})
		}
		return response, nil
	}
	return r.recoveryRPC.ResolveRefs(ctx, request, options...)
}

func (r *unbornRecoveryRPC) CommitFileAtRevision(ctx context.Context, request *gitv1.CommitFileAtRevisionRequest, _ ...grpc.CallOption) (*gitv1.CommitFileAtRevisionResponse, error) {
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
	return &gitv1.CommitFileAtRevisionResponse{CommitSha: strings.Repeat("a", 40)}, nil
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
