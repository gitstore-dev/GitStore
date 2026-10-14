// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitclient

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestReceivePackParsesShallowClientCommandsWithoutConsumingPack(t *testing.T) {
	oid := strings.Repeat("a", 40)
	command := oid + " " + strings.Repeat("0", 40) + " refs/heads/main\x00report-status delete-refs\n"
	packet := func(data string) string { return fmt.Sprintf("%04x%s", len(data)+4, data) }
	reader := bufio.NewReader(strings.NewReader(packet("shallow "+oid+"\n") + packet(command) + "0000PACK"))
	commands, shallow, err := parsePktLineRefCommands(reader)
	require.NoError(t, err)
	require.Equal(t, []string{oid}, shallow)
	require.Len(t, commands, 1)
	require.Equal(t, oid, commands[0].OldOid)
	require.Equal(t, "refs/heads/main", commands[0].RefName)
	pack, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, []byte("PACK"), pack)
}

func TestReceivePackRejectsMalformedOrUnboundedShallowHeaders(t *testing.T) {
	oid := strings.Repeat("a", 40)
	packet := func(data string) string { return fmt.Sprintf("%04x%s", len(data)+4, data) }
	command := packet(oid + " " + strings.Repeat("0", 40) + " refs/heads/main\n")
	for name, body := range map[string]string{
		"zero":          packet("shallow " + strings.Repeat("0", 40)),
		"uppercase":     packet("shallow " + strings.Repeat("A", 40)),
		"invalid hex":   packet("shallow " + strings.Repeat("x", 40)),
		"short":         packet("shallow a"),
		"extra field":   packet("shallow " + oid + " extra"),
		"capabilities":  packet("shallow " + oid + "\x00report-status"),
		"duplicate":     packet("shallow "+oid) + packet("shallow "+oid),
		"after command": command + packet("shallow "+oid),
		"too many": func() string {
			var b strings.Builder
			for i := 1; i <= 4097; i++ {
				b.WriteString(packet(fmt.Sprintf("shallow %040x", i)))
			}
			return b.String()
		}(),
		"oversized packet": "ffff",
		"delimiter":        "0001",
		"truncated":        "0034shallow a",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := parsePktLineRefCommands(bufio.NewReader(strings.NewReader(body + "0000")))
			require.Error(t, err)
		})
	}
	t.Run("header byte ceiling", func(t *testing.T) {
		large := packet(oid + " " + oid + " refs/heads/" + strings.Repeat("a", 64000))
		_, _, err := parsePktLineRefCommands(bufio.NewReader(strings.NewReader(strings.Repeat(large, 66) + "0000")))
		require.ErrorContains(t, err, "header exceeds")
	})
	t.Run("exact shallow ceiling", func(t *testing.T) {
		var b strings.Builder
		for i := 1; i <= 4096; i++ {
			b.WriteString(packet(fmt.Sprintf("shallow %040x", i)))
		}
		b.WriteString(command + "0000")
		commands, shallow, err := parsePktLineRefCommands(bufio.NewReader(strings.NewReader(b.String())))
		require.NoError(t, err)
		require.Len(t, commands, 1)
		require.Len(t, shallow, 4096)
	})
}

type signedRetryConnection struct {
	grpc.ClientConnInterface
	invoke    func(context.Context, string, any, any) error
	newStream func(context.Context, *grpc.StreamDesc, string) (grpc.ClientStream, error)
}

func (c signedRetryConnection) Invoke(ctx context.Context, method string, args, reply any, _ ...grpc.CallOption) error {
	return c.invoke(ctx, method, args, reply)
}

func (c signedRetryConnection) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
	return c.newStream(ctx, desc, method)
}

func TestSignedReadRetryUsesFreshNonceAndClearsPartialResponse(t *testing.T) {
	request := &gitv1.ListFilesRequest{
		RepositoryId: "repository", Ref: strings.Repeat("a", 40), PathPrefix: "files/",
		Authorization: &gitv1.RequestAuthorization{
			Actor:  &gitv1.AuthContext{Subject: "reader", Issuer: "fixture", Groups: []string{"readers"}},
			Action: "repository.contents.read", ResourceKind: "Repository", ResourceName: "catalog", RepositoryId: "repository",
		},
	}
	original := proto.Clone(request)
	seen := make(map[string]bool)
	calls := 0
	conn := signedConn{secret: "fixture-key", ClientConnInterface: signedRetryConnection{
		invoke: func(ctx context.Context, method string, args, reply any) error {
			calls++
			require.Same(t, request, args)
			require.True(t, proto.Equal(original, args.(proto.Message)), "retry must preserve the exact authorized request")
			md, _ := metadata.FromOutgoingContext(ctx)
			require.Empty(t, md.Get("authorization"))
			envelope := md.Get(auth.RPCSignatureMetadata)[0]
			_, _, err := auth.VerifyRPC("fixture-key", "gitstore-git", method, envelope, request, time.Now())
			require.NoError(t, err)
			nonce := strings.Split(envelope, ":")[2]
			require.False(t, seen[nonce], "retry must not reuse a spent envelope")
			seen[nonce] = true
			response := reply.(*gitv1.ListFilesResponse)
			require.Empty(t, response.Files)
			response.Files = append(response.Files, &gitv1.FileEntry{Path: "partial"})
			if calls == 1 {
				return status.Error(codes.Unavailable, "RPC clock moved backwards")
			}
			response.Files[0].Path = "accepted"
			response.RefCommitSha = request.Ref
			return nil
		},
	}}
	response := &gitv1.ListFilesResponse{}
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("authorization", "do-not-forward"))
	require.NoError(t, conn.Invoke(ctx, gitv1.GitService_ListFiles_FullMethodName, request, response))
	require.Equal(t, 2, calls)
	require.Len(t, response.Files, 1)
	require.Equal(t, "accepted", response.Files[0].Path)
	require.Equal(t, request.Ref, response.RefCommitSha)
	require.True(t, proto.Equal(original, request))
}

func TestSignedReadRetryIsBoundedAndDoesNotRetryWritesOrDenials(t *testing.T) {
	for _, method := range []string{
		gitv1.GitService_GetFile_FullMethodName, gitv1.GitService_ListFiles_FullMethodName,
		gitv1.GitService_InfoRefs_FullMethodName, gitv1.GitService_ListTags_FullMethodName,
		gitv1.GitService_GetLatestTag_FullMethodName, gitv1.GitService_CommitFile_FullMethodName,
		gitv1.GitService_DeleteFile_FullMethodName, gitv1.GitService_CreateRepository_FullMethodName,
		gitv1.GitService_DeleteRepository_FullMethodName, gitv1.GitService_CreateTag_FullMethodName,
		"/other.Service/GetFile",
	} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			conn := signedConn{secret: "fixture-key", ClientConnInterface: signedRetryConnection{
				invoke: func(context.Context, string, any, any) error {
					calls++
					return status.Error(codes.Unavailable, "unavailable")
				},
			}}
			err := conn.Invoke(t.Context(), method, &gitv1.GetFileRequest{}, &gitv1.GetFileResponse{})
			require.Equal(t, codes.Unavailable, status.Code(err))
			expected := 1
			switch method {
			case gitv1.GitService_GetFile_FullMethodName, gitv1.GitService_ListFiles_FullMethodName,
				gitv1.GitService_InfoRefs_FullMethodName, gitv1.GitService_ListTags_FullMethodName,
				gitv1.GitService_GetLatestTag_FullMethodName:
				expected = 4
			}
			require.Equal(t, expected, calls)
		})
	}
	for _, code := range []codes.Code{codes.PermissionDenied, codes.Unauthenticated, codes.NotFound,
		codes.FailedPrecondition, codes.ResourceExhausted, codes.InvalidArgument, codes.Internal,
		codes.Canceled, codes.DeadlineExceeded} {
		t.Run(code.String(), func(t *testing.T) {
			calls := 0
			conn := signedConn{secret: "fixture-key", ClientConnInterface: signedRetryConnection{
				invoke: func(context.Context, string, any, any) error {
					calls++
					return status.Error(code, "failure")
				},
			}}
			err := conn.Invoke(t.Context(), gitv1.GitService_GetFile_FullMethodName, &gitv1.GetFileRequest{}, &gitv1.GetFileResponse{})
			require.Equal(t, code, status.Code(err))
			require.Equal(t, 1, calls)
		})
	}
}

func TestSignedReadRetryStopsOnCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	conn := signedConn{secret: "fixture-key", ClientConnInterface: signedRetryConnection{
		invoke: func(context.Context, string, any, any) error {
			calls++
			cancel()
			return status.Error(codes.Unavailable, "temporary failure")
		},
	}}
	err := conn.Invoke(ctx, gitv1.GitService_GetFile_FullMethodName, &gitv1.GetFileRequest{}, &gitv1.GetFileResponse{})
	require.Equal(t, codes.Canceled, status.Code(err))
	require.Equal(t, 1, calls)
}

func TestSignedReadRetryStopsOnCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	calls := 0
	conn := signedConn{secret: "fixture-key", ClientConnInterface: signedRetryConnection{
		invoke: func(context.Context, string, any, any) error {
			calls++
			return status.Error(codes.Unavailable, "temporary failure")
		},
	}}
	err := conn.Invoke(ctx, gitv1.GitService_GetFile_FullMethodName, &gitv1.GetFileRequest{}, &gitv1.GetFileResponse{})
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))
	require.Equal(t, 1, calls)
}

func TestSignedStreamsDoNotRetryUnavailable(t *testing.T) {
	for _, method := range []string{
		gitv1.GitService_ReceivePack_FullMethodName, gitv1.GitService_UploadPack_FullMethodName,
		gitv1.GitService_GetFileStream_FullMethodName,
	} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			conn := signedConn{secret: "fixture-key", ClientConnInterface: signedRetryConnection{
				newStream: func(context.Context, *grpc.StreamDesc, string) (grpc.ClientStream, error) {
					calls++
					return nil, status.Error(codes.Unavailable, "temporary failure")
				},
			}}
			stream, err := conn.NewStream(t.Context(), &grpc.StreamDesc{}, method)
			require.NoError(t, err)
			err = stream.SendMsg(&gitv1.GetFileRequest{})
			require.Equal(t, codes.Unavailable, status.Code(err))
			require.Equal(t, 1, calls)
		})
	}
}

func TestSignedRequestNeverForwardsSigningKey(t *testing.T) {
	request := &gitv1.CreateRepositoryRequest{RepositoryId: "repository"}
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("authorization", "Bearer user-token"))
	ctx, err := signedContext(ctx, "mysecret", gitv1.GitService_CreateRepository_FullMethodName, request)
	require.NoError(t, err)
	md, _ := metadata.FromOutgoingContext(ctx)
	require.Empty(t, md.Get("authorization"))
	require.Len(t, md.Get(auth.RPCSignatureMetadata), 1)
	envelope := md.Get(auth.RPCSignatureMetadata)[0]
	require.NotContains(t, envelope, "mysecret")
	_, _, err = auth.VerifyRPC("mysecret", "gitstore-git", gitv1.GitService_CreateRepository_FullMethodName, envelope, request, time.Now())
	require.NoError(t, err)
	request.RepositoryId = "another"
	_, _, err = auth.VerifyRPC("mysecret", "gitstore-git", gitv1.GitService_CreateRepository_FullMethodName, envelope, request, time.Now())
	require.Error(t, err)
}
