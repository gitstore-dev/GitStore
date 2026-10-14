// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitclient

import (
	"context"
	"errors"
	"sync"
	"time"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// signedConn binds each call to its exact method and request. Streaming calls
// are opened on their first SendMsg, so ReceivePack signs the first chunk's
// actor, repository, policy and complete ref commands without buffering a pack.
type signedConn struct {
	grpc.ClientConnInterface
	secret string
}

func signedContext(ctx context.Context, secret, method string, request any) (context.Context, error) {
	message, ok := request.(proto.Message)
	if !ok {
		return nil, errors.New("protobuf RPC request required")
	}
	envelope, err := auth.SignRPC(secret, "gitstore-git", method, message)
	if err != nil {
		return nil, err
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	md.Delete("authorization")
	md.Set(auth.RPCSignatureMetadata, envelope)
	return metadata.NewOutgoingContext(ctx, md), nil
}
func (c signedConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	attempts := 1
	switch method {
	case gitv1.GitService_GetFile_FullMethodName, gitv1.GitService_GetFiles_FullMethodName, gitv1.GitService_ListFiles_FullMethodName,
		gitv1.GitService_InfoRefs_FullMethodName, gitv1.GitService_ListTags_FullMethodName,
		gitv1.GitService_GetLatestTag_FullMethodName:
		attempts = 4
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return status.FromContextError(err).Err()
		}
		if attempt > 0 {
			message, ok := reply.(proto.Message)
			if !ok {
				return errors.New("protobuf RPC response required")
			}
			proto.Reset(message)
		}
		signed, err := signedContext(ctx, c.secret, method, args)
		if err != nil {
			return err
		}
		err = c.ClientConnInterface.Invoke(signed, method, args, reply, opts...)
		if status.Code(err) != codes.Unavailable || attempt+1 == attempts {
			return err
		}
		// A new logical read attempt must carry a new single-use nonce.
		timer := time.NewTimer((25 * time.Millisecond) << attempt)
		select {
		case <-ctx.Done():
			timer.Stop()
			return status.FromContextError(ctx.Err()).Err()
		case <-timer.C:
		}
	}
}
func (c signedConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return &signedStream{ctx: ctx, conn: c, desc: desc, method: method, opts: opts}, nil
}

type signedStream struct {
	mu     sync.Mutex
	stream grpc.ClientStream
	ctx    context.Context
	conn   signedConn
	desc   *grpc.StreamDesc
	method string
	opts   []grpc.CallOption
}

func (s *signedStream) SendMsg(message any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream == nil {
		ctx, err := signedContext(s.ctx, s.conn.secret, s.method, message)
		if err != nil {
			return err
		}
		stream, err := s.conn.ClientConnInterface.NewStream(ctx, s.desc, s.method, s.opts...)
		if err != nil {
			return err
		}
		s.stream = stream
	}
	return s.stream.SendMsg(message)
}
func (s *signedStream) current() (grpc.ClientStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream == nil {
		return nil, errors.New("signed RPC requires a first request message")
	}
	return s.stream, nil
}
func (s *signedStream) Header() (metadata.MD, error) {
	stream, err := s.current()
	if err != nil {
		return nil, err
	}
	return stream.Header()
}
func (s *signedStream) Trailer() metadata.MD {
	stream, err := s.current()
	if err != nil {
		return nil
	}
	return stream.Trailer()
}
func (s *signedStream) CloseSend() error {
	stream, err := s.current()
	if err != nil {
		return err
	}
	return stream.CloseSend()
}
func (s *signedStream) Context() context.Context { return s.ctx }
func (s *signedStream) RecvMsg(message any) error {
	stream, err := s.current()
	if err != nil {
		return err
	}
	return stream.RecvMsg(message)
}
