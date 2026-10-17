// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Write client tests use bufconn by default; the deployed probe is explicitly opt-in.

package gitclient_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// writeStub implements only the write RPCs.
type writeStub struct {
	gitv1.UnimplementedGitServiceServer

	commitFileFunc  func(*gitv1.CommitFileRequest) (*gitv1.CommitFileResponse, error)
	deleteFileFunc  func(*gitv1.DeleteFileRequest) (*gitv1.DeleteFileResponse, error)
	createTagFunc   func(*gitv1.CreateTagRequest) (*gitv1.CreateTagResponse, error)
	receivePackFunc func(gitv1.GitService_ReceivePackServer) error
}

func (s *writeStub) ReceivePack(stream gitv1.GitService_ReceivePackServer) error {
	if s.receivePackFunc != nil {
		return s.receivePackFunc(stream)
	}
	return status.Error(codes.Unimplemented, "not set up")
}

func TestReceivePackSignsShallowBoundariesOnlyOnFirstChunk(t *testing.T) {
	for _, pack := range []string{"", "PACK" + strings.Repeat("x", 128*1024)} {
		t.Run(fmt.Sprintf("pack bytes %d", len(pack)), func(t *testing.T) {
			oid := strings.Repeat("a", 40)
			calls := 0
			c := startBufconn(t, &writeStub{receivePackFunc: func(stream gitv1.GitService_ReceivePackServer) error {
				first, err := stream.Recv()
				require.NoError(t, err)
				require.Equal(t, []string{oid}, first.ShallowOids)
				md, _ := metadata.FromIncomingContext(stream.Context())
				_, _, err = auth.VerifyRPC("fixture-key", "gitstore-git", gitv1.GitService_ReceivePack_FullMethodName,
					md.Get(auth.RPCSignatureMetadata)[0], first, time.Now())
				require.NoError(t, err)
				tampered := proto.Clone(first).(*gitv1.ReceivePackRequest)
				tampered.ShallowOids = []string{strings.Repeat("b", 40)}
				_, _, err = auth.VerifyRPC("fixture-key", "gitstore-git", gitv1.GitService_ReceivePack_FullMethodName,
					md.Get(auth.RPCSignatureMetadata)[0], tampered, time.Now())
				require.Error(t, err)
				actual := string(first.PackData)
				calls++
				for {
					chunk, err := stream.Recv()
					if err == io.EOF {
						break
					}
					require.NoError(t, err)
					require.Empty(t, chunk.ShallowOids)
					require.Empty(t, chunk.RepositoryId)
					require.Empty(t, chunk.RefCommands)
					require.Nil(t, chunk.PushContext)
					actual += string(chunk.PackData)
				}
				require.Equal(t, pack, actual)
				return stream.SendAndClose(&gitv1.ReceivePackResponse{ReportStatus: []byte("report")})
			}})
			ctx := gitclient.ContextForBackingWork(t.Context())
			authorization, err := gitclient.RequestAuthorization(ctx, auth.ActionRepositoryContentsWrite, c.RepositoryID)
			require.NoError(t, err)
			original := &gitv1.PushContext{RepositoryId: c.RepositoryID, Authorization: authorization}
			snapshot := proto.Clone(original)
			ctx = gitclient.ContextWithPushContext(ctx, original)
			packet := func(data string) string { return fmt.Sprintf("%04x%s", len(data)+4, data) }
			body := packet("shallow "+oid+"\n") + packet(oid+" "+strings.Repeat("0", 40)+" refs/heads/main\x00report-status\n") + "0000" + pack
			report, err := c.ReceivePack(ctx, c.RepositoryID, strings.NewReader(body))
			require.NoError(t, err)
			require.Equal(t, []byte("report"), report)
			require.Equal(t, 1, calls)
			require.True(t, proto.Equal(original, snapshot), "receive must not mutate shared authorization")
		})
	}
}

func (s *writeStub) CommitFile(_ context.Context, req *gitv1.CommitFileRequest) (*gitv1.CommitFileResponse, error) {
	if s.commitFileFunc != nil {
		return s.commitFileFunc(req)
	}
	return nil, status.Error(codes.Unimplemented, "not set up")
}

func (s *writeStub) DeleteFile(_ context.Context, req *gitv1.DeleteFileRequest) (*gitv1.DeleteFileResponse, error) {
	if s.deleteFileFunc != nil {
		return s.deleteFileFunc(req)
	}
	return nil, status.Error(codes.Unimplemented, "not set up")
}

func (s *writeStub) CreateTag(_ context.Context, req *gitv1.CreateTagRequest) (*gitv1.CreateTagResponse, error) {
	if s.createTagFunc != nil {
		return s.createTagFunc(req)
	}
	return nil, status.Error(codes.Unimplemented, "not set up")
}

func TestBackingWriteUsesLiveInternalPolicy(t *testing.T) {
	called := false
	c := startBufconn(t, &writeStub{commitFileFunc: func(req *gitv1.CommitFileRequest) (*gitv1.CommitFileResponse, error) {
		called = true
		require.Equal(t, "system:api", req.Authorization.Actor.Subject)
		require.Equal(t, "repository.contents.write", req.Authorization.Action)
		return &gitv1.CommitFileResponse{CommitSha: "commit"}, nil
	}})
	allow := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(nil, allow, nil)
	c.Registry = registry
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"})
	_, err := c.CommitFile(ctx, gitclient.CommitFileParams{Path: "products/p.md"})
	require.NoError(t, err)
	require.True(t, called)
	require.Equal(t, "alice", auth.PrincipalFromContext(ctx).Subject, "backing writer must not replace domain actor")
	called = false
	registry.Swap(nil, testutil.NewDenyAllAuthZ(t), nil)
	_, err = c.CommitFile(ctx, gitclient.CommitFileParams{Path: "products/p.md"})
	require.ErrorIs(t, err, gitclient.ErrAuthorizationDenied)
	require.False(t, called, "live reload denial must prevent the Git RPC")
}

func TestCommitFile_OK(t *testing.T) {
	var capturedReq *gitv1.CommitFileRequest
	c := startBufconn(t, &writeStub{
		commitFileFunc: func(req *gitv1.CommitFileRequest) (*gitv1.CommitFileResponse, error) {
			capturedReq = req
			return &gitv1.CommitFileResponse{CommitSha: "deadbeef", Branch: "main"}, nil
		},
	})

	sha, err := c.CommitFile(context.Background(), gitclient.CommitFileParams{
		Path:          "products/p1.md",
		Content:       []byte("---\nid: p1\n---"),
		CommitMessage: "add p1",
		AuthorName:    "Alice",
		AuthorEmail:   "alice@example.com",
	})
	require.NoError(t, err)
	assert.Equal(t, "deadbeef", sha)
	require.NotNil(t, capturedReq)
	assert.Equal(t, "products/p1.md", capturedReq.Path)
	assert.Equal(t, []byte("---\nid: p1\n---"), capturedReq.Content)
	assert.Equal(t, "add p1", capturedReq.CommitMessage)
	assert.Equal(t, "Alice", capturedReq.AuthorName)
}

func TestCommitFile_TransientUnavailable(t *testing.T) {
	c := startBufconn(t, &writeStub{
		commitFileFunc: func(_ *gitv1.CommitFileRequest) (*gitv1.CommitFileResponse, error) {
			return nil, status.Error(codes.Unavailable, "transient")
		},
	})

	_, err := c.CommitFile(context.Background(), gitclient.CommitFileParams{
		Path: "f.md", Content: []byte("x"), CommitMessage: "m",
	})
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
}

func TestDeleteFile_OK(t *testing.T) {
	c := startBufconn(t, &writeStub{
		deleteFileFunc: func(req *gitv1.DeleteFileRequest) (*gitv1.DeleteFileResponse, error) {
			assert.Equal(t, "products/p1.md", req.Path)
			assert.Equal(t, "delete p1", req.CommitMessage)
			return &gitv1.DeleteFileResponse{CommitSha: "cafe1234"}, nil
		},
	})

	sha, err := c.DeleteFile(context.Background(), gitclient.DeleteFileParams{
		Path:          "products/p1.md",
		CommitMessage: "delete p1",
		AuthorName:    "Bob",
		AuthorEmail:   "bob@example.com",
	})
	require.NoError(t, err)
	assert.Equal(t, "cafe1234", sha)
}

func TestDeleteFile_NotFound(t *testing.T) {
	c := startBufconn(t, &writeStub{
		deleteFileFunc: func(_ *gitv1.DeleteFileRequest) (*gitv1.DeleteFileResponse, error) {
			return nil, status.Error(codes.NotFound, "file not found")
		},
	})

	_, err := c.DeleteFile(context.Background(), gitclient.DeleteFileParams{Path: "missing.md"})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestDeleteFile_Preconditions(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "guarded"}[guarded], func(t *testing.T) {
			p := gitclient.DeleteFileParams{Path: "products/p1.md"}
			if guarded {
				p.RefName = "refs/heads/catalog"
				p.ExpectedCommitSHA = "0123456789012345678901234567890123456789"
			}
			c := startBufconn(t, &writeStub{
				deleteFileFunc: func(req *gitv1.DeleteFileRequest) (*gitv1.DeleteFileResponse, error) {
					assert.Equal(t, "selected-repository", req.RepositoryId)
					assert.Equal(t, p.RefName, req.GetRefName())
					assert.Equal(t, p.ExpectedCommitSHA, req.GetExpectedCommitSha())
					if !guarded {
						assert.Nil(t, req.RefName)
						assert.Nil(t, req.ExpectedCommitSha)
					}
					return nil, status.Error(codes.Aborted, "ref changed")
				},
			})
			_, err := c.DeleteFileForRepo(context.Background(), "selected-repository", p)
			assert.Equal(t, codes.Aborted, status.Code(err))
		})
	}
}

func TestLostSuccessfulRPCReplyFixture(t *testing.T) {
	intercept, receipts := testutil.LoseFirstSuccessfulGitReply(gitv1.GitService_DeleteFile_FullMethodName)
	calls := 0
	invoke := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		calls++
		if calls == 1 {
			return status.Error(codes.Aborted, "preexisting conflict")
		}
		return nil
	}
	for _, expected := range []codes.Code{codes.Aborted, codes.Unavailable, codes.OK} {
		err := intercept(t.Context(), gitv1.GitService_DeleteFile_FullMethodName, nil,
			&gitv1.DeleteFileResponse{CommitSha: "receipt"}, nil, invoke)
		require.Equal(t, expected, status.Code(err))
	}
	require.Equal(t, 3, calls, "the fixture must neither retry nor suppress an upstream failure")
	require.Len(t, receipts, 1)
	require.Equal(t, "receipt", <-receipts)
}

func TestGitRPCDeletionRecoveryDeployed(t *testing.T) {
	uri, secret := testutil.NativeGitTestConfiguration(t)
	for _, method := range []string{gitv1.GitService_DeleteFile_FullMethodName, gitv1.GitService_DeleteRepository_FullMethodName} {
		t.Run(strings.TrimPrefix(method, "/gitstore.git.v1.GitService/"), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			client := func(options ...grpc.DialOption) *gitclient.Client {
				c, err := gitclient.NewClientWithAddr(uri, secret, options...)
				require.NoError(t, err)
				c.Registry = auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil)
				t.Cleanup(func() { require.NoError(t, c.Close()) })
				return c
			}
			intercept, receipts := testutil.LoseFirstSuccessfulGitReply(method)
			writer := client(grpc.WithChainUnaryInterceptor(intercept))
			cleaner := client()
			provision := func() string {
				uid := uuid.NewString()
				t.Cleanup(func() {
					cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					require.NoError(t, cleaner.DeleteRepository(cleanup, uid))
				})
				_, err := cleaner.CreateRepository(ctx, uid, "")
				require.NoError(t, err)
				return uid
			}
			uid := provision()
			content := []byte("disposable native receipt fixture\n")
			write := func(c *gitclient.Client, repository, path string) string {
				sha, err := c.CommitFileForRepo(ctx, repository, gitclient.CommitFileParams{
					Path: path, Content: content, CommitMessage: "native deletion recovery fixture",
					AuthorName: "GitStore test", AuthorEmail: "native-test@gitstore.invalid",
				})
				require.NoError(t, err)
				require.Len(t, sha, 40)
				return sha
			}
			initial := write(cleaner, uid, "receipt.md")
			if method == gitv1.GitService_DeleteFile_FullMethodName {
				params := gitclient.DeleteFileParams{Path: "receipt.md", RefName: "refs/heads/main",
					ExpectedCommitSHA: initial, CommitMessage: "remove native receipt fixture",
					AuthorName: "GitStore test", AuthorEmail: "native-test@gitstore.invalid"}
				sha, err := writer.DeleteFileForRepo(ctx, uid, params)
				require.Equal(t, codes.Unavailable, status.Code(err))
				require.Empty(t, sha, "the caller must not receive the discarded success")
				require.Len(t, receipts, 1)
				removed := <-receipts
				require.Len(t, removed, 40)
				peer := client()
				head := write(peer, uid, "unrelated.md")
				require.NotEqual(t, removed, head)
				for range 2 {
					recovered, err := peer.DeleteFileForRepo(ctx, uid, params)
					require.NoError(t, err)
					require.Equal(t, removed, recovered, "retry must recover the original removal, not the later head")
					actual, err := peer.ResolveRefForRepo(ctx, uid, "refs/heads/main")
					require.NoError(t, err)
					require.Equal(t, head, actual, "receipt retry must not author another commit")
				}
				_, err = peer.ReadFileForRepo(ctx, uid, "receipt.md", head)
				require.Equal(t, codes.NotFound, status.Code(err))
				restored := write(peer, uid, "receipt.md")
				_, err = peer.DeleteFileForRepo(ctx, uid, params)
				require.Equal(t, codes.FailedPrecondition, status.Code(err))
				actual, err := peer.ReadFileForRepo(ctx, uid, "receipt.md", restored)
				require.NoError(t, err)
				require.Equal(t, content, actual, "stale receipt must not delete restored content")
			} else {
				require.Equal(t, codes.Unavailable, status.Code(writer.DeleteRepository(ctx, uid)))
				require.Len(t, receipts, 1)
				require.Equal(t, uid, <-receipts)
				peer := client()
				require.NoError(t, peer.DeleteRepository(ctx, uid))
				_, err := peer.CreateRepository(ctx, uid, "")
				require.Equal(t, codes.FailedPrecondition, status.Code(err), "a deleted UID must stay tombstoned")
				replacement := provision()
				head := write(peer, replacement, "receipt.md")
				require.NoError(t, peer.DeleteRepository(ctx, uid))
				actual, err := peer.ReadFileForRepo(ctx, replacement, "receipt.md", head)
				require.NoError(t, err)
				require.Equal(t, content, actual, "stale cleanup must not touch replacement-UID storage")
			}
		})
	}
}

func TestCreateTag_OK(t *testing.T) {
	c := startBufconn(t, &writeStub{
		createTagFunc: func(req *gitv1.CreateTagRequest) (*gitv1.CreateTagResponse, error) {
			assert.Equal(t, "v1.2.0", req.TagName)
			assert.Equal(t, "release v1.2.0", req.Message)
			assert.Empty(t, req.TargetCommitSha)
			return &gitv1.CreateTagResponse{TagName: "v1.2.0", TagSha: "tag123"}, nil
		},
	})

	tagSha, err := c.CreateTag(context.Background(), gitclient.CreateTagParams{
		Name:    "v1.2.0",
		Message: "release v1.2.0",
	})
	require.NoError(t, err)
	assert.Equal(t, "tag123", tagSha)
}

func TestCreateTag_AlreadyExists(t *testing.T) {
	c := startBufconn(t, &writeStub{
		createTagFunc: func(_ *gitv1.CreateTagRequest) (*gitv1.CreateTagResponse, error) {
			return nil, status.Error(codes.AlreadyExists, "tag exists")
		},
	})

	_, err := c.CreateTag(context.Background(), gitclient.CreateTagParams{Name: "v1.0.0"})
	require.Error(t, err)
	assert.Equal(t, codes.AlreadyExists, status.Code(err))
}
