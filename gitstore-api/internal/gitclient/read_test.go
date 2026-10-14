// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Unit tests for gRPC read client methods.
// Uses bufconn to run a real gRPC server in-process — no Docker required.

package gitclient_test

import (
	"bytes"
	"context"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestGitRPCDurationMetricsObserveSuccessAndFailure(t *testing.T) {
	handledCounts := func() map[string]float64 {
		t.Helper()
		families, err := prometheus.DefaultGatherer.Gather()
		require.NoError(t, err)
		counts := make(map[string]float64)
		for _, family := range families {
			if family.GetName() != "grpc_client_handled_total" {
				continue
			}
			for _, metric := range family.Metric {
				labels := make(map[string]string)
				for _, label := range metric.Label {
					labels[label.GetName()] = label.GetValue()
				}
				if labels["grpc_service"] == "gitstore.git.v1.GitService" && labels["grpc_method"] == "GetFile" && labels["grpc_type"] == "unary" {
					counts[labels["grpc_code"]] = metric.GetCounter().GetValue()
				}
			}
		}
		return counts
	}
	before := handledCounts()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	seen := make(map[string]bool)
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		envelopes := md.Get(auth.RPCSignatureMetadata)
		if len(envelopes) != 1 {
			return nil, status.Error(codes.Unauthenticated, "missing signed request")
		}
		nonce, _, err := auth.VerifyRPC("fixture-signing-key", "gitstore-git", info.FullMethod, envelopes[0], req.(proto.Message), time.Now())
		if err != nil || seen[nonce] {
			return nil, status.Error(codes.Unauthenticated, "invalid or repeated request")
		}
		seen[nonce] = true
		return handler(ctx, req)
	}))
	var transientCalls atomic.Int32
	gitv1.RegisterGitServiceServer(server, &readStub{getFileFunc: func(req *gitv1.GetFileRequest) (*gitv1.GetFileResponse, error) {
		if req.Ref == "denied" {
			return nil, status.Error(codes.PermissionDenied, "fixture denial")
		}
		if req.Ref == "transient" && transientCalls.Add(1) == 1 {
			return nil, status.Error(codes.Unavailable, "RPC clock moved backwards")
		}
		return &gitv1.GetFileResponse{}, nil
	}})
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		require.NoError(t, <-done)
	})
	client, err := gitclient.NewClientWithAddr(listener.Addr().String(), "fixture-signing-key")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for _, ref := range []string{"allowed", "denied", "transient"} {
		_, err = client.Git.GetFile(ctx, &gitv1.GetFileRequest{RepositoryId: "private-fixture-resource", Ref: ref})
		expected := codes.OK
		if ref == "denied" {
			expected = codes.PermissionDenied
		}
		require.Equal(t, expected, status.Code(err))
	}
	require.Equal(t, int32(2), transientCalls.Load())
	after := handledCounts()
	require.Equal(t, float64(2), after["OK"]-before["OK"])
	require.Equal(t, float64(1), after["Unavailable"]-before["Unavailable"], "recovered transient failures remain observable")
	require.Equal(t, float64(1), after["PermissionDenied"]-before["PermissionDenied"])
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	sampled := false
	for _, family := range families {
		if family.GetName() != "grpc_client_handling_seconds" {
			continue
		}
		for _, metric := range family.Metric {
			method := ""
			for _, label := range metric.Label {
				require.Contains(t, []string{"grpc_type", "grpc_service", "grpc_method"}, label.GetName())
				require.NotContains(t, label.GetValue(), "private-fixture-resource")
				if label.GetName() == "grpc_method" {
					method = label.GetValue()
				}
			}
			if method == "GetFile" {
				require.GreaterOrEqual(t, metric.Histogram.GetSampleCount(), uint64(4))
				require.Positive(t, metric.Histogram.GetSampleSum())
				sampled = true
			}
		}
	}
	require.True(t, sampled, "the production client constructor must enable RPC duration samples")
}

func TestGitFileReadPhaseMetadataIsBoundedAndDoesNotChangeRPCResults(t *testing.T) {
	keys := []string{
		"x-gitstore-file-read-authentication-ns", "x-gitstore-file-read-repository-lock-ns",
		"x-gitstore-file-read-blocking-queue-ns", "x-gitstore-file-read-payload-ns",
		"x-gitstore-file-read-history-commit-ns", "x-gitstore-file-read-history-tree-ns",
		"x-gitstore-file-read-repository-open-ns", "x-gitstore-file-read-revision-ns",
		"x-gitstore-file-read-target-tree-ns", "x-gitstore-file-read-blob-ns",
	}
	snapshot := func() (map[string]uint64, map[string]float64) {
		t.Helper()
		families, err := prometheus.DefaultGatherer.Gather()
		require.NoError(t, err)
		counts, reports := map[string]uint64{}, map[string]float64{}
		for _, family := range families {
			for _, metric := range family.Metric {
				switch family.GetName() {
				case "gitstore_git_file_read_server_phase_seconds":
					require.Len(t, metric.Label, 2)
					labels := map[string]string{}
					for _, label := range metric.Label {
						labels[label.GetName()] = label.GetValue()
					}
					require.Contains(t, []string{"authentication", "repository_lock", "blocking_queue", "payload", "history_commit", "history_tree", "repository_open", "revision", "target_tree", "blob"}, labels["phase"])
					require.Contains(t, []string{"content", "history_proof", "batch_content"}, labels["mode"])
					counts[labels["phase"]+"/"+labels["mode"]] = metric.Histogram.GetSampleCount()
				case "gitstore_git_file_read_phase_reports_total":
					require.Len(t, metric.Label, 1)
					require.Equal(t, "result", metric.Label[0].GetName())
					reports[metric.Label[0].GetValue()] = metric.Counter.GetValue()
				case "gitstore_git_file_read_history_commits":
					require.Empty(t, metric.Label)
					counts["history_commits"] = metric.Histogram.GetSampleCount()
					reports["history_sum"] = metric.Histogram.GetSampleSum()
				}
			}
		}
		return counts, reports
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		var ref string
		history := false
		switch request := req.(type) {
		case *gitv1.GetFileRequest:
			ref = request.Ref
			history = request.GetUnchangedSinceCommit() != ""
		case *gitv1.GetFilesRequest:
			ref = strings.TrimSuffix(request.CommitSha, "-batch")
		}
		headers := metadata.MD{}
		if ref != "missing" {
			for i, key := range keys {
				headers.Set(key, []string{"1000000", "2000000", "3000000", "8000000", "1000000", "1000000", "1000000", "1000000", "1000000", "1000000"}[i])
			}
			headers.Set("x-gitstore-file-read-history-commits", "0")
			if history {
				headers.Set("x-gitstore-file-read-history-commits", "2")
			} else {
				headers.Set(keys[4], "0")
				headers.Set(keys[5], "0")
			}
		}
		switch ref {
		case "partial":
			headers.Delete(keys[0])
		case "negative":
			headers.Set(keys[0], "-1")
		case "overflow":
			headers.Set(keys[0], "9223372036854775808")
		case "malformed":
			headers.Set(keys[0], "NaN")
		case "duplicate":
			headers.Append(keys[0], "1")
		case "missing-work":
			headers.Delete("x-gitstore-file-read-history-commits")
		case "duplicate-work":
			headers.Append("x-gitstore-file-read-history-commits", "0")
		case "malformed-work":
			headers.Set("x-gitstore-file-read-history-commits", "NaN")
		case "excess-work":
			headers.Set("x-gitstore-file-read-history-commits", "4098")
		case "negative-work":
			headers.Set("x-gitstore-file-read-history-commits", "-1")
		case "content-work":
			headers.Set("x-gitstore-file-read-history-commits", "1")
		case "zero-history":
			headers.Set("x-gitstore-file-read-history-commits", "0")
		case "valid-history-limit":
			headers.Set("x-gitstore-file-read-history-commits", "4097")
		case "valid-history-one":
			headers.Set("x-gitstore-file-read-history-commits", "1")
		case "overflow-work":
			headers.Set("x-gitstore-file-read-history-commits", "9223372036854775808")
		case "partial-history-phase":
			headers.Delete(keys[4])
		}
		require.NoError(t, grpc.SetHeader(ctx, headers))
		return handler(ctx, req)
	}))
	gitv1.RegisterGitServiceServer(server, &readStub{getFileFunc: func(req *gitv1.GetFileRequest) (*gitv1.GetFileResponse, error) {
		if req.Ref == "denied" {
			return nil, status.Error(codes.PermissionDenied, "fixture denial")
		}
		return &gitv1.GetFileResponse{Content: []byte("unchanged")}, nil
	}, getFilesFunc: func(context.Context, *gitv1.GetFilesRequest) (*gitv1.GetFilesResponse, error) {
		return &gitv1.GetFilesResponse{}, nil
	}})
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); require.NoError(t, <-done) })
	client, err := gitclient.NewClientWithAddr(listener.Addr().String(), "fixture-signing-key")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	for _, tc := range []struct{ ref, report string }{
		{"valid", "observed"}, {"valid-history", "observed"}, {"missing", "missing"}, {"partial", "invalid"},
		{"negative", "invalid"}, {"overflow", "invalid"}, {"malformed", "invalid"},
		{"duplicate", "invalid"}, {"denied", "rpc_error"},
		{"missing-work", "invalid"}, {"duplicate-work", "invalid"}, {"malformed-work", "invalid"},
		{"excess-work", "invalid"}, {"negative-work", "invalid"}, {"content-work", "invalid"},
		{"zero-history", "invalid"},
		{"valid-history-limit", "observed"}, {"valid-history-one", "observed"},
		{"overflow-work", "invalid"}, {"partial-history-phase", "invalid"},
		{"valid-batch", "observed"}, {"missing-batch", "missing"},
		{"partial-batch", "invalid"}, {"content-work-batch", "invalid"},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			before, reportsBefore := snapshot()
			var headers metadata.MD
			request := &gitv1.GetFileRequest{Ref: tc.ref}
			mode := "content"
			if strings.HasPrefix(tc.ref, "valid-history") || tc.ref == "zero-history" {
				ancestor := strings.Repeat("a", 40)
				request.UnchangedSinceCommit = &ancestor
				mode = "history_proof"
			}
			if strings.HasSuffix(tc.ref, "-batch") {
				mode = "batch_content"
				_, err := client.Git.GetFiles(t.Context(), &gitv1.GetFilesRequest{CommitSha: tc.ref}, grpc.Header(&headers))
				require.NoError(t, err)
			} else {
				resp, err := client.Git.GetFile(t.Context(), request, grpc.Header(&headers))
				if tc.ref == "denied" {
					require.Equal(t, codes.PermissionDenied, status.Code(err))
				} else {
					require.NoError(t, err)
					require.Equal(t, []byte("unchanged"), resp.Content)
				}
			}
			after, reportsAfter := snapshot()
			require.Equal(t, float64(1), reportsAfter[tc.report]-reportsBefore[tc.report])
			for _, phase := range []string{"authentication", "repository_lock", "blocking_queue", "payload", "history_commit", "history_tree", "repository_open", "revision", "target_tree", "blob"} {
				for _, candidate := range []string{"content", "history_proof", "batch_content"} {
					delta := uint64(0)
					if tc.report == "observed" && candidate == mode {
						delta = 1
					}
					proofs := uint64(0)
					commits := float64(0)
					if tc.report == "observed" && mode == "history_proof" {
						proofs = 1
						commits = 2
						switch tc.ref {
						case "valid-history-one":
							commits = 1
						case "valid-history-limit":
							commits = 4097
						}
					}
					require.Equal(t, proofs, after["history_commits"]-before["history_commits"])
					require.Equal(t, commits, reportsAfter["history_sum"]-reportsBefore["history_sum"])
					key := phase + "/" + candidate
					require.Equal(t, delta, after[key]-before[key], "invalid reports cannot partially record durations")
				}
			}
			if tc.ref == "valid" {
				require.Equal(t, []string{"1000000"}, headers.Get(keys[0]), "caller-requested response headers must be preserved")
			}
		})
	}
}

// readStub implements only the read RPCs.
type readStub struct {
	gitv1.UnimplementedGitServiceServer

	getFileFunc        func(*gitv1.GetFileRequest) (*gitv1.GetFileResponse, error)
	getFileContextFunc func(context.Context, *gitv1.GetFileRequest) (*gitv1.GetFileResponse, error)
	getFilesFunc       func(context.Context, *gitv1.GetFilesRequest) (*gitv1.GetFilesResponse, error)
	listFilesFunc      func(*gitv1.ListFilesRequest) (*gitv1.ListFilesResponse, error)
	getLatestTagFunc   func(*gitv1.GetLatestTagRequest) (*gitv1.GetLatestTagResponse, error)
	listTagsFunc       func(*gitv1.ListTagsRequest) (*gitv1.ListTagsResponse, error)
	uploadPackFunc     func(*gitv1.UploadPackRequest, gitv1.GitService_UploadPackServer) error
}

func (s *readStub) GetFile(ctx context.Context, req *gitv1.GetFileRequest) (*gitv1.GetFileResponse, error) {
	if s.getFileContextFunc != nil {
		return s.getFileContextFunc(ctx, req)
	}
	if s.getFileFunc != nil {
		return s.getFileFunc(req)
	}
	return nil, status.Error(codes.Unimplemented, "not set up")
}

func (s *readStub) GetFiles(ctx context.Context, req *gitv1.GetFilesRequest) (*gitv1.GetFilesResponse, error) {
	if s.getFilesFunc != nil {
		return s.getFilesFunc(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "not set up")
}

func TestReadFilesPreservesImmutableScopeOrderingAbsenceAndEmptyFiles(t *testing.T) {
	commit := strings.Repeat("a", 40)
	paths := []string{"files/full.md", "files/missing.md", "files/empty.md"}
	var calls atomic.Int32
	client := startBufconn(t, &readStub{getFilesFunc: func(ctx context.Context, request *gitv1.GetFilesRequest) (*gitv1.GetFilesResponse, error) {
		calls.Add(1)
		require.Equal(t, "explicit-repository", request.RepositoryId)
		require.Equal(t, paths, request.Paths)
		require.Equal(t, commit, request.CommitSha)
		require.Equal(t, "repository.contents.read", request.Authorization.Action)
		md, _ := metadata.FromIncomingContext(ctx)
		signatures := md.Get(auth.RPCSignatureMetadata)
		require.Len(t, signatures, 1)
		_, _, err := auth.VerifyRPC("fixture-key", "gitstore-git", gitv1.GitService_GetFiles_FullMethodName, signatures[0], request, time.Now())
		require.NoError(t, err)
		return &gitv1.GetFilesResponse{CommitSha: commit, Files: []*gitv1.FileSnapshot{
			{Path: paths[0], Found: true, Content: []byte("body"), SizeBytes: 4, BlobSha: strings.Repeat("b", 40)},
			{Path: paths[1]},
			{Path: paths[2], Found: true, BlobSha: strings.Repeat("c", 40)},
		}}, nil
	}})
	originalRepository := client.RepositoryID
	results, err := client.ReadFilesForRepo(t.Context(), "explicit-repository", paths, commit)
	require.NoError(t, err)
	require.Len(t, results, 3)
	require.Equal(t, []byte("body"), results[0])
	require.Nil(t, results[1], "absent paths must not become empty files")
	require.NotNil(t, results[2], "an existing empty file is not path absence")
	require.Empty(t, results[2])
	require.Equal(t, originalRepository, client.RepositoryID)
	for _, input := range []struct {
		paths  []string
		commit string
	}{
		{nil, commit}, {[]string{"x", "x"}, commit}, {[]string{""}, commit},
		{[]string{"a", "b", "c", "d", "e"}, commit}, {[]string{"x"}, "HEAD"},
	} {
		_, err := client.ReadFilesForRepo(t.Context(), "explicit-repository", input.paths, input.commit)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	}
	require.Equal(t, int32(1), calls.Load(), "invalid inputs cannot perform signed reads")
}

func TestReadFilesRejectsMalformedSnapshotsAndDoesNotFallback(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, mutation := range []struct {
		name   string
		change func(*gitv1.GetFilesResponse)
	}{
		{"commit", func(r *gitv1.GetFilesResponse) { r.CommitSha = strings.Repeat("b", 40) }},
		{"cardinality", func(r *gitv1.GetFilesResponse) { r.Files = nil }},
		{"nil result", func(r *gitv1.GetFilesResponse) { r.Files[0] = nil }},
		{"path", func(r *gitv1.GetFilesResponse) { r.Files[0].Path = "other" }},
		{"size", func(r *gitv1.GetFilesResponse) { r.Files[0].SizeBytes = 99 }},
		{"oversized blob", func(r *gitv1.GetFilesResponse) {
			r.Files[0].Content = make([]byte, (4<<20)+1)
			r.Files[0].SizeBytes = uint64(len(r.Files[0].Content))
		}},
		{"blob", func(r *gitv1.GetFilesResponse) { r.Files[0].BlobSha = "invalid" }},
		{"false absence", func(r *gitv1.GetFilesResponse) { r.Files[0].Found = false }},
		{"unrequested second commit", func(r *gitv1.GetFilesResponse) { r.SecondaryCommitSha = commit }},
		{"unrequested second files", func(r *gitv1.GetFilesResponse) { r.SecondaryFiles = r.Files }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			client := startBufconn(t, &readStub{
				getFilesFunc: func(context.Context, *gitv1.GetFilesRequest) (*gitv1.GetFilesResponse, error) {
					response := &gitv1.GetFilesResponse{CommitSha: commit, Files: []*gitv1.FileSnapshot{
						{Path: "file", Found: true, Content: []byte("x"), SizeBytes: 1, BlobSha: strings.Repeat("b", 40)},
					}}
					mutation.change(response)
					return response, nil
				},
				getFileFunc: func(*gitv1.GetFileRequest) (*gitv1.GetFileResponse, error) {
					t.Error("batch failures cannot fall back to individual reads")
					return nil, status.Error(codes.Internal, "unexpected fallback")
				},
			})
			results, err := client.ReadFilesForRepo(t.Context(), "repository", []string{"file"}, commit)
			require.Nil(t, results)
			require.Equal(t, codes.Internal, status.Code(err))
		})
	}
}

func TestReadFilePairSignsBothImmutableSnapshotsAndRequiresCompleteReceipts(t *testing.T) {
	first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
	paths := []string{"missing.md", "full.md", "empty.md"}
	for _, scenario := range []string{"complete", "missing second receipt", "wrong second commit", "wrong second cardinality", "wrong second path", "false second absence", "invalid second blob", "wrong second size", "unrequested current receipt", "unimplemented"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			client := startBufconn(t, &readStub{
				getFilesFunc: func(ctx context.Context, req *gitv1.GetFilesRequest) (*gitv1.GetFilesResponse, error) {
					calls++
					require.Equal(t, "selected-repository", req.RepositoryId)
					require.Equal(t, paths, req.Paths)
					require.Equal(t, first, req.CommitSha)
					require.NotNil(t, req.SecondaryCommitSha)
					require.Equal(t, second, req.GetSecondaryCommitSha())
					md, _ := metadata.FromIncomingContext(ctx)
					signature := md.Get(auth.RPCSignatureMetadata)[0]
					_, _, err := auth.VerifyRPC("fixture-key", "gitstore-git", gitv1.GitService_GetFiles_FullMethodName, signature, req, time.Now())
					require.NoError(t, err)
					tampered := proto.Clone(req).(*gitv1.GetFilesRequest)
					tampered.SecondaryCommitSha = &first
					_, _, err = auth.VerifyRPC("fixture-key", "gitstore-git", gitv1.GitService_GetFiles_FullMethodName, signature, tampered, time.Now())
					require.Error(t, err)
					if scenario == "unimplemented" {
						return nil, status.Error(codes.Unimplemented, "pair unavailable")
					}
					response := &gitv1.GetFilesResponse{
						CommitSha: first, SecondaryCommitSha: second,
						Files:          []*gitv1.FileSnapshot{{Path: paths[0]}, {Path: paths[1], Found: true, Content: []byte("old"), SizeBytes: 3, BlobSha: first}, {Path: paths[2], Found: true, BlobSha: first}},
						SecondaryFiles: []*gitv1.FileSnapshot{{Path: paths[0], Found: true, Content: []byte("new"), SizeBytes: 3, BlobSha: second}, {Path: paths[1]}, {Path: paths[2], Found: true, BlobSha: second}},
					}
					switch scenario {
					case "unrequested current receipt":
						response.CurrentRefCommitSha = second
					case "missing second receipt":
						response.SecondaryCommitSha = ""
					case "wrong second commit":
						response.SecondaryCommitSha = first
					case "wrong second cardinality":
						response.SecondaryFiles = response.SecondaryFiles[:2]
					case "wrong second path":
						response.SecondaryFiles[0].Path = "other"
					case "false second absence":
						response.SecondaryFiles[0].Found = false
					case "invalid second blob":
						response.SecondaryFiles[0].BlobSha = "invalid"
					case "wrong second size":
						response.SecondaryFiles[0].SizeBytes++
					}
					return response, nil
				},
				getFileFunc: func(*gitv1.GetFileRequest) (*gitv1.GetFileResponse, error) {
					t.Error("paired snapshots cannot fall back to other reads")
					return nil, status.Error(codes.Internal, "unexpected fallback")
				},
			})
			old, current, err := client.ReadFilePairForRepo(t.Context(), "selected-repository", paths, strings.ToUpper(first), strings.ToUpper(second))
			require.Equal(t, 1, calls)
			if scenario == "complete" {
				require.NoError(t, err)
				require.Nil(t, old[0])
				require.Equal(t, []byte("old"), old[1])
				require.NotNil(t, old[2])
				require.Equal(t, []byte("new"), current[0])
				require.Nil(t, current[1])
				require.NotNil(t, current[2])
			} else {
				expected := codes.Internal
				if scenario == "unimplemented" {
					expected = codes.Unimplemented
				}
				require.Equal(t, expected, status.Code(err))
				require.Nil(t, old)
				require.Nil(t, current)
			}
			for _, invalid := range []string{"", "HEAD", "bad", strings.Repeat("x", 40)} {
				_, _, err := client.ReadFilePairForRepo(t.Context(), "selected-repository", paths, first, invalid)
				require.Equal(t, codes.InvalidArgument, status.Code(err))
			}
			require.Equal(t, 1, calls, "invalid pair input must not perform signed reads")
		})
	}
}

func TestReadFilePairAtCurrentRefRequiresSignedRequestAndExactReceipt(t *testing.T) {
	first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, scenario := range []string{"complete", "missing receipt", "wrong receipt", "malformed receipt", "stale ref", "unavailable", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			client := startBufconn(t, &readStub{getFilesFunc: func(ctx context.Context, req *gitv1.GetFilesRequest) (*gitv1.GetFilesResponse, error) {
				calls++
				require.Equal(t, "refs/heads/main", req.GetCurrentRef())
				require.Equal(t, first, req.CommitSha)
				require.Equal(t, second, req.GetSecondaryCommitSha())
				md, _ := metadata.FromIncomingContext(ctx)
				signature := md.Get(auth.RPCSignatureMetadata)[0]
				_, _, err := auth.VerifyRPC("fixture-key", "gitstore-git", gitv1.GitService_GetFiles_FullMethodName, signature, req, time.Now())
				require.NoError(t, err)
				tampered := proto.Clone(req).(*gitv1.GetFilesRequest)
				other := "refs/heads/other"
				tampered.CurrentRef = &other
				_, _, err = auth.VerifyRPC("fixture-key", "gitstore-git", gitv1.GitService_GetFiles_FullMethodName, signature, tampered, time.Now())
				require.Error(t, err)
				for name, code := range map[string]codes.Code{"stale ref": codes.Aborted, "unavailable": codes.Unavailable, "cancelled": codes.Canceled} {
					if scenario == name {
						return nil, status.Error(code, scenario)
					}
				}
				response := &gitv1.GetFilesResponse{
					CommitSha: first, SecondaryCommitSha: second, CurrentRefCommitSha: second,
					Files:          []*gitv1.FileSnapshot{{Path: "a.md"}, {Path: "b.md"}},
					SecondaryFiles: []*gitv1.FileSnapshot{{Path: "a.md"}, {Path: "b.md"}},
				}
				switch scenario {
				case "missing receipt":
					response.CurrentRefCommitSha = ""
				case "wrong receipt":
					response.CurrentRefCommitSha = first
				case "malformed receipt":
					response.CurrentRefCommitSha = "HEAD"
				}
				return response, nil
			}})
			old, current, err := client.ReadFilePairAtCurrentRefForRepo(t.Context(), "repository", []string{"a.md", "b.md"}, first, second, "refs/heads/main")
			if scenario == "complete" {
				require.NoError(t, err)
				require.Len(t, old, 2)
				require.Len(t, current, 2)
			} else {
				code := codes.Internal
				switch scenario {
				case "stale ref":
					code = codes.Aborted
				case "unavailable":
					code = codes.Unavailable
				case "cancelled":
					code = codes.Canceled
				}
				require.Equal(t, code, status.Code(err))
				require.Nil(t, old)
				require.Nil(t, current)
			}
			for _, invalid := range []string{"", "HEAD", second} {
				_, _, err := client.ReadFilePairAtCurrentRefForRepo(t.Context(), "repository", []string{"a.md", "b.md"}, first, second, invalid)
				require.Equal(t, codes.InvalidArgument, status.Code(err))
			}
			expectedCalls := 1
			if scenario == "unavailable" {
				expectedCalls = 4
			}
			require.Equal(t, expectedCalls, calls)
		})
	}
}

func TestReadFilePairCancellationReturnsNoPartialSnapshot(t *testing.T) {
	started, finished := make(chan struct{}), make(chan struct{})
	client := startBufconn(t, &readStub{
		getFilesFunc: func(ctx context.Context, _ *gitv1.GetFilesRequest) (*gitv1.GetFilesResponse, error) {
			close(started)
			<-ctx.Done()
			close(finished)
			return nil, status.FromContextError(ctx.Err()).Err()
		},
		getFileFunc: func(*gitv1.GetFileRequest) (*gitv1.GetFileResponse, error) {
			t.Error("cancelled paired read cannot fall back")
			return nil, status.Error(codes.Internal, "unexpected fallback")
		},
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		first, second, err := client.ReadFilePairForRepo(ctx, "repository", []string{"a.md", "b.md"}, strings.Repeat("a", 40), strings.Repeat("b", 40))
		assert.Equal(t, codes.Canceled, status.Code(err))
		assert.Nil(t, first)
		assert.Nil(t, second)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("paired read did not start")
	}
	cancel()
	for _, completion := range []<-chan struct{}{done, finished} {
		select {
		case <-completion:
		case <-time.After(5 * time.Second):
			t.Fatal("cancelled paired read did not drain")
		}
	}
}

func (s *readStub) ListFiles(_ context.Context, req *gitv1.ListFilesRequest) (*gitv1.ListFilesResponse, error) {
	if s.listFilesFunc != nil {
		return s.listFilesFunc(req)
	}
	return nil, status.Error(codes.Unimplemented, "not set up")
}

func (s *readStub) GetLatestTag(_ context.Context, req *gitv1.GetLatestTagRequest) (*gitv1.GetLatestTagResponse, error) {
	if s.getLatestTagFunc != nil {
		return s.getLatestTagFunc(req)
	}
	return nil, status.Error(codes.Unimplemented, "not set up")
}

func (s *readStub) ListTags(_ context.Context, req *gitv1.ListTagsRequest) (*gitv1.ListTagsResponse, error) {
	if s.listTagsFunc != nil {
		return s.listTagsFunc(req)
	}
	return nil, status.Error(codes.Unimplemented, "not set up")
}

func (s *readStub) UploadPack(req *gitv1.UploadPackRequest, stream gitv1.GitService_UploadPackServer) error {
	if s.uploadPackFunc != nil {
		return s.uploadPackFunc(req, stream)
	}
	return status.Error(codes.Unimplemented, "not set up")
}

func TestReadFile_OK(t *testing.T) {
	want := []byte("---\nid: p1\n---\nhello")
	c := startBufconn(t, &readStub{
		getFileFunc: func(req *gitv1.GetFileRequest) (*gitv1.GetFileResponse, error) {
			assert.Equal(t, "products/p1.md", req.Path)
			assert.Equal(t, "v1.0.0", req.Ref)
			return &gitv1.GetFileResponse{Content: want}, nil
		},
	})

	got, err := c.ReadFile(context.Background(), "products/p1.md", "v1.0.0")
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestReadFile_NotFound(t *testing.T) {
	c := startBufconn(t, &readStub{
		getFileFunc: func(_ *gitv1.GetFileRequest) (*gitv1.GetFileResponse, error) {
			return nil, status.Error(codes.NotFound, "file not found")
		},
	})

	_, err := c.ReadFile(context.Background(), "missing.md", "HEAD")
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestReadUnchangedFileRequiresBoundProof(t *testing.T) {
	ancestor, tip := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, tc := range []struct {
		name            string
		proof, resolved string
		wireError       error
		wantError       bool
	}{
		{name: "verified", proof: ancestor, resolved: tip},
		{name: "old server ignored guard", wantError: true},
		{name: "wrong ancestor", proof: tip, resolved: tip, wantError: true},
		{name: "wrong tip", proof: ancestor, resolved: ancestor, wantError: true},
		{name: "changed path", wireError: status.Error(codes.Aborted, "changed"), wantError: true},
		{name: "read failure", wireError: status.Error(codes.Unavailable, "unavailable"), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := startBufconn(t, &readStub{
				getFileFunc: func(req *gitv1.GetFileRequest) (*gitv1.GetFileResponse, error) {
					assert.Equal(t, ancestor, req.GetUnchangedSinceCommit())
					assert.Equal(t, tip, req.Ref)
					assert.Equal(t, "repositories/catalog.md", req.Path)
					if tc.wireError != nil {
						return nil, tc.wireError
					}
					return &gitv1.GetFileResponse{Content: []byte("manifest"), UnchangedSinceCommit: tc.proof, RefCommitSha: tc.resolved}, nil
				},
			})
			got, err := c.ReadUnchangedFileForRepo(context.Background(), "repo-a", "repositories/catalog.md", tip, ancestor)
			if tc.wantError {
				require.Error(t, err)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, []byte("manifest"), got)
		})
	}
}

func TestReadCurrentFileProofUsesOneSignedScopedRPC(t *testing.T) {
	ancestor, current := strings.Repeat("a", 40), strings.Repeat("b", 40)
	var calls atomic.Int32
	client := startBufconn(t, &readStub{
		getFileContextFunc: func(ctx context.Context, req *gitv1.GetFileRequest) (*gitv1.GetFileResponse, error) {
			calls.Add(1)
			require.Equal(t, "explicit-repository", req.RepositoryId)
			require.Equal(t, "repositories/catalog.md", req.Path)
			require.Equal(t, "refs/heads/main", req.Ref)
			require.Equal(t, ancestor, req.GetUnchangedSinceCommit())
			require.True(t, req.AllowAncestorReceipt)
			require.Equal(t, auth.ActionRepositoryContentsRead, req.Authorization.Action)
			md, _ := metadata.FromIncomingContext(ctx)
			signatures := md.Get(auth.RPCSignatureMetadata)
			require.Len(t, signatures, 1)
			_, _, err := auth.VerifyRPC("fixture-key", "gitstore-git", gitv1.GitService_GetFile_FullMethodName, signatures[0], req, time.Now())
			require.NoError(t, err)
			tampered := proto.Clone(req).(*gitv1.GetFileRequest)
			tampered.AllowAncestorReceipt = false
			_, _, err = auth.VerifyRPC("fixture-key", "gitstore-git", gitv1.GitService_GetFile_FullMethodName, signatures[0], tampered, time.Now())
			require.Error(t, err, "receipt selection must be signature-bound")
			return &gitv1.GetFileResponse{Path: req.Path, Content: []byte("manifest"), SizeBytes: 8,
				BlobSha: strings.Repeat("c", 40), RefCommitSha: current, UnchangedSinceCommit: ancestor}, nil
		},
		listFilesFunc: func(*gitv1.ListFilesRequest) (*gitv1.ListFilesResponse, error) {
			t.Fatal("a current proof must not issue a separate ref RPC")
			return nil, nil
		},
	})
	atAncestor, body, err := client.ReadCurrentUnchangedFileForRepo(t.Context(), "explicit-repository", "repositories/catalog.md", "refs/heads/main", strings.ToUpper(ancestor))
	require.NoError(t, err)
	require.False(t, atAncestor)
	require.Equal(t, []byte("manifest"), body)
	for _, input := range [][2]string{{"refs/heads/main", "HEAD"}, {"HEAD", ancestor}, {"", ancestor}} {
		_, _, err := client.ReadCurrentUnchangedFileForRepo(t.Context(), "explicit-repository", "repositories/catalog.md", input[0], input[1])
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	}
	require.Equal(t, int32(1), calls.Load())
}

func TestReadCurrentFileProofRejectsMalformedResponses(t *testing.T) {
	ancestor := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name   string
		change func(*gitv1.GetFileResponse)
	}{
		{"missing current", func(r *gitv1.GetFileResponse) { r.RefCommitSha = "" }},
		{"invalid current", func(r *gitv1.GetFileResponse) { r.RefCommitSha = "HEAD" }},
		{"wrong ancestor", func(r *gitv1.GetFileResponse) { r.UnchangedSinceCommit = strings.Repeat("b", 40) }},
		{"wrong path", func(r *gitv1.GetFileResponse) { r.Path = "other" }},
		{"wrong size", func(r *gitv1.GetFileResponse) { r.SizeBytes = 2 }},
		{"invalid blob", func(r *gitv1.GetFileResponse) { r.BlobSha = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := startBufconn(t, &readStub{getFileFunc: func(*gitv1.GetFileRequest) (*gitv1.GetFileResponse, error) {
				response := &gitv1.GetFileResponse{Path: "file", Content: []byte("x"), SizeBytes: 1,
					BlobSha: strings.Repeat("b", 40), RefCommitSha: strings.Repeat("c", 40), UnchangedSinceCommit: ancestor}
				tc.change(response)
				return response, nil
			}})
			atAncestor, body, err := client.ReadCurrentUnchangedFileForRepo(t.Context(), "repo", "file", "refs/heads/main", ancestor)
			require.False(t, atAncestor)
			require.Nil(t, body)
			require.Equal(t, codes.Internal, status.Code(err))
		})
	}
}

func TestReadCurrentAncestorReceiptIsExplicitAndValidated(t *testing.T) {
	ancestor := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name   string
		change func(*gitv1.GetFileResponse)
		valid  bool
	}{
		{name: "exact receipt", valid: true},
		{name: "missing receipt", change: func(r *gitv1.GetFileResponse) { r.AtAncestor = false }},
		{name: "advanced receipt", change: func(r *gitv1.GetFileResponse) { r.RefCommitSha = strings.Repeat("b", 40) }},
		{name: "wrong ancestor", change: func(r *gitv1.GetFileResponse) { r.UnchangedSinceCommit = strings.Repeat("b", 40) }},
		{name: "wrong path", change: func(r *gitv1.GetFileResponse) { r.Path = "other" }},
		{name: "unexpected content", change: func(r *gitv1.GetFileResponse) { r.Content = []byte("x"); r.SizeBytes = 1 }},
		{name: "unexpected blob", change: func(r *gitv1.GetFileResponse) { r.BlobSha = strings.Repeat("b", 40) }},
		{name: "unexpected size", change: func(r *gitv1.GetFileResponse) { r.SizeBytes = 1 }},
		{name: "legacy full read", change: func(r *gitv1.GetFileResponse) {
			r.AtAncestor, r.Content, r.SizeBytes, r.BlobSha = false, []byte("x"), 1, strings.Repeat("b", 40)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := startBufconn(t, &readStub{
				getFileFunc: func(req *gitv1.GetFileRequest) (*gitv1.GetFileResponse, error) {
					require.True(t, req.AllowAncestorReceipt)
					response := &gitv1.GetFileResponse{Path: "file", RefCommitSha: ancestor,
						UnchangedSinceCommit: ancestor, AtAncestor: true}
					if tc.change != nil {
						tc.change(response)
					}
					return response, nil
				},
				listFilesFunc: func(*gitv1.ListFilesRequest) (*gitv1.ListFilesResponse, error) {
					t.Fatal("invalid or unsupported receipts cannot fall back to another RPC")
					return nil, nil
				},
			})
			atAncestor, content, err := client.ReadCurrentUnchangedFileForRepo(t.Context(), "repo", "file", "refs/heads/main", ancestor)
			require.Nil(t, content)
			require.Equal(t, tc.valid, atAncestor)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Equal(t, codes.Internal, status.Code(err))
			}
		})
	}
}

func TestOrdinaryFileReadRejectsUnrequestedReceipt(t *testing.T) {
	client := startBufconn(t, &readStub{getFileFunc: func(req *gitv1.GetFileRequest) (*gitv1.GetFileResponse, error) {
		require.False(t, req.AllowAncestorReceipt)
		return &gitv1.GetFileResponse{AtAncestor: true}, nil
	}})
	content, err := client.ReadFileForRepo(t.Context(), "repo", "file", "HEAD")
	require.Nil(t, content)
	require.Equal(t, codes.Internal, status.Code(err))
}

func TestReadCurrentFileProofPreservesErrorsWithoutRefFallback(t *testing.T) {
	for _, code := range []codes.Code{codes.Aborted, codes.NotFound, codes.FailedPrecondition, codes.PermissionDenied, codes.Internal, codes.ResourceExhausted, codes.Unavailable, codes.Canceled} {
		t.Run(code.String(), func(t *testing.T) {
			client := startBufconn(t, &readStub{
				getFileFunc: func(*gitv1.GetFileRequest) (*gitv1.GetFileResponse, error) {
					return nil, status.Error(code, "proof failed")
				},
				listFilesFunc: func(*gitv1.ListFilesRequest) (*gitv1.ListFilesResponse, error) {
					t.Fatal("failed proofs cannot fall back to a ref-only check")
					return nil, nil
				},
			})
			atAncestor, body, err := client.ReadCurrentUnchangedFileForRepo(t.Context(), "repo", "file", "refs/heads/main", strings.Repeat("a", 40))
			require.False(t, atAncestor)
			require.Nil(t, body)
			require.Equal(t, code, status.Code(err))
		})
	}
}

func TestListFiles_OK(t *testing.T) {
	c := startBufconn(t, &readStub{
		listFilesFunc: func(req *gitv1.ListFilesRequest) (*gitv1.ListFilesResponse, error) {
			assert.Equal(t, "products/", req.PathPrefix)
			assert.Equal(t, "v1.0.0", req.Ref)
			return &gitv1.ListFilesResponse{
				Files: []*gitv1.FileEntry{
					{Path: "products/a.md", SizeBytes: 100},
					{Path: "products/b.md", SizeBytes: 200},
				},
			}, nil
		},
	})

	entries, err := c.ListFiles(context.Background(), "products/", "v1.0.0")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "products/a.md", entries[0].Path)
}

func TestGetLatestTag_OK(t *testing.T) {
	c := startBufconn(t, &readStub{
		getLatestTagFunc: func(_ *gitv1.GetLatestTagRequest) (*gitv1.GetLatestTagResponse, error) {
			return &gitv1.GetLatestTagResponse{
				Tag:   &gitv1.TagEntry{Name: "v2.3.1", CommitSha: "abc123"},
				Found: true,
			}, nil
		},
	})

	tag, err := c.GetLatestTag(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "v2.3.1", tag.Name)
	assert.Equal(t, "abc123", tag.CommitSha)
}

func TestGetLatestTag_NotFound(t *testing.T) {
	c := startBufconn(t, &readStub{
		getLatestTagFunc: func(_ *gitv1.GetLatestTagRequest) (*gitv1.GetLatestTagResponse, error) {
			return &gitv1.GetLatestTagResponse{Found: false}, nil
		},
	})

	_, err := c.GetLatestTag(context.Background())
	require.Error(t, err)
}

func TestListTags_OK(t *testing.T) {
	c := startBufconn(t, &readStub{
		listTagsFunc: func(req *gitv1.ListTagsRequest) (*gitv1.ListTagsResponse, error) {
			assert.Equal(t, "v", req.Prefix)
			return &gitv1.ListTagsResponse{
				Tags: []*gitv1.TagEntry{
					{Name: "v1.0.0", CommitSha: "sha1"},
					{Name: "v1.1.0", CommitSha: "sha2"},
				},
			}, nil
		},
	})

	tags, err := c.ListTags(context.Background(), "v")
	require.NoError(t, err)
	require.Len(t, tags, 2)
	assert.Equal(t, "v1.0.0", tags[0].Name)
}

func TestUploadPackReadAllDrainsFinalChunkLargerThanReadBuffer(t *testing.T) {
	want := bytes.Repeat([]byte("0123456789abcdef"), 512)
	c := startBufconn(t, &readStub{
		uploadPackFunc: func(req *gitv1.UploadPackRequest, stream gitv1.GitService_UploadPackServer) error {
			assert.Equal(t, "repo-123", req.RepositoryId)
			assert.Equal(t, []byte("want abc123\n0000"), req.Body)
			return stream.Send(&gitv1.UploadPackResponse{
				Data:   want,
				IsLast: true,
			})
		},
	})

	reader, err := c.UploadPack(gitclient.ContextForBackingWork(context.Background()), "repo-123", []byte("want abc123\n0000"))
	require.NoError(t, err)

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}
