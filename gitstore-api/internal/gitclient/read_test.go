// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Unit tests for gRPC read client methods.
// Uses bufconn to run a real gRPC server in-process — no Docker required.

package gitclient_test

import (
	"context"
	"testing"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// readStub implements only the read RPCs.
type readStub struct {
	gitv1.UnimplementedGitServiceServer

	getFileFunc      func(*gitv1.GetFileRequest) (*gitv1.GetFileResponse, error)
	listFilesFunc    func(*gitv1.ListFilesRequest) (*gitv1.ListFilesResponse, error)
	getLatestTagFunc func(*gitv1.GetLatestTagRequest) (*gitv1.GetLatestTagResponse, error)
	listTagsFunc     func(*gitv1.ListTagsRequest) (*gitv1.ListTagsResponse, error)
}

func (s *readStub) GetFile(_ context.Context, req *gitv1.GetFileRequest) (*gitv1.GetFileResponse, error) {
	if s.getFileFunc != nil {
		return s.getFileFunc(req)
	}
	return nil, status.Error(codes.Unimplemented, "not set up")
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
