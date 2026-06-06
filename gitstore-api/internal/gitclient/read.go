// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// gRPC-backed git read operations.

package gitclient

import (
	"context"
	"fmt"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NewClientFromConn wraps an existing gRPC connection (useful for tests).
func NewClientFromConn(conn *grpc.ClientConn) *Client {
	return &Client{
		conn: conn,
		Git:  gitv1.NewGitServiceClient(conn),
	}
}

// ReadFile fetches the raw bytes of a single file at the given ref.
// Uses the shared Client.RepositoryID field — prefer ReadFileForRepo for concurrent use.
func (c *Client) ReadFile(ctx context.Context, path, ref string) ([]byte, error) {
	return c.ReadFileForRepo(ctx, c.RepositoryID, path, ref)
}

// ReadFileForRepo fetches the raw bytes of a single file at the given ref for
// the specified repository. Safe for concurrent calls with different repository IDs.
func (c *Client) ReadFileForRepo(ctx context.Context, repositoryID, path, ref string) ([]byte, error) {
	resp, err := c.Git.GetFile(ctx, &gitv1.GetFileRequest{
		RepositoryId: repositoryID,
		Path:         path,
		Ref:          ref,
	})
	if err != nil {
		return nil, err
	}
	return resp.Content, nil
}

// ListFiles enumerates file paths under prefix at the given ref.
// Uses the shared Client.RepositoryID field — prefer ListFilesForRepo for concurrent use.
func (c *Client) ListFiles(ctx context.Context, prefix, ref string) ([]*gitv1.FileEntry, error) {
	return c.ListFilesForRepo(ctx, c.RepositoryID, prefix, ref)
}

// ListFilesForRepo enumerates file paths under prefix at the given ref for
// the specified repository. Safe for concurrent calls with different repository IDs.
func (c *Client) ListFilesForRepo(ctx context.Context, repositoryID, prefix, ref string) ([]*gitv1.FileEntry, error) {
	resp, err := c.Git.ListFiles(ctx, &gitv1.ListFilesRequest{
		RepositoryId: repositoryID,
		Ref:          ref,
		PathPrefix:   prefix,
		Recursive:    true,
	})
	if err != nil {
		return nil, err
	}
	return resp.Files, nil
}

// GetLatestTag returns the latest semver release tag.
// Returns an error (wrapping codes.NotFound) if no tags exist.
func (c *Client) GetLatestTag(ctx context.Context) (*gitv1.TagEntry, error) {
	resp, err := c.Git.GetLatestTag(ctx, &gitv1.GetLatestTagRequest{
		RepositoryId: c.RepositoryID,
		Prefix:       "v",
	})
	if err != nil {
		return nil, err
	}
	if !resp.Found {
		return nil, status.Error(codes.NotFound, "no release tags found")
	}
	if resp.Tag == nil {
		return nil, fmt.Errorf("git-service returned found=true but tag is nil")
	}
	return resp.Tag, nil
}

// ListTags enumerates tags with an optional prefix filter.
func (c *Client) ListTags(ctx context.Context, prefix string) ([]*gitv1.TagEntry, error) {
	resp, err := c.Git.ListTags(ctx, &gitv1.ListTagsRequest{
		RepositoryId: c.RepositoryID,
		Prefix:       prefix,
	})
	if err != nil {
		return nil, err
	}
	return resp.Tags, nil
}
