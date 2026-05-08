// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Unit tests for gRPC write client methods (CommitFile, DeleteFile, CreateTag).
// Uses bufconn — no Docker required.

package gitclient_test

import (
	"context"
	"testing"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// writeStub implements only the write RPCs.
type writeStub struct {
	gitv1.UnimplementedGitServiceServer

	commitFileFunc func(*gitv1.CommitFileRequest) (*gitv1.CommitFileResponse, error)
	deleteFileFunc func(*gitv1.DeleteFileRequest) (*gitv1.DeleteFileResponse, error)
	createTagFunc  func(*gitv1.CreateTagRequest) (*gitv1.CreateTagResponse, error)
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
