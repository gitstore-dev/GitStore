// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// gRPC-backed git write operations.

package gitclient

import (
	"context"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
)

// CommitFileParams holds parameters for a CommitFile RPC call.
type CommitFileParams struct {
	Path          string
	Content       []byte
	CommitMessage string
	AuthorName    string
	AuthorEmail   string
}

// DeleteFileParams holds parameters for a DeleteFile RPC call.
type DeleteFileParams struct {
	Path          string
	CommitMessage string
	AuthorName    string
	AuthorEmail   string
}

// CreateTagParams holds parameters for a CreateTag RPC call.
type CreateTagParams struct {
	Name            string
	Message         string
	TargetCommitSha string // empty = HEAD
}

// CommitFile writes a single file and commits it to the default branch.
// Returns the new commit SHA on success.
func (c *Client) CommitFile(ctx context.Context, p CommitFileParams) (string, error) {
	resp, err := c.Git.CommitFile(ctx, &gitv1.CommitFileRequest{
		Path:          p.Path,
		Content:       p.Content,
		CommitMessage: p.CommitMessage,
		AuthorName:    p.AuthorName,
		AuthorEmail:   p.AuthorEmail,
	})
	if err != nil {
		return "", err
	}
	return resp.CommitSha, nil
}

// DeleteFile removes a file and commits the deletion to the default branch.
// Returns the new commit SHA on success.
func (c *Client) DeleteFile(ctx context.Context, p DeleteFileParams) (string, error) {
	resp, err := c.Git.DeleteFile(ctx, &gitv1.DeleteFileRequest{
		Path:          p.Path,
		CommitMessage: p.CommitMessage,
		AuthorName:    p.AuthorName,
		AuthorEmail:   p.AuthorEmail,
	})
	if err != nil {
		return "", err
	}
	return resp.CommitSha, nil
}

// CreateTag creates an annotated tag on HEAD (or the specified commit SHA).
// Returns the tag object SHA on success.
func (c *Client) CreateTag(ctx context.Context, p CreateTagParams) (string, error) {
	resp, err := c.Git.CreateTag(ctx, &gitv1.CreateTagRequest{
		TagName:         p.Name,
		Message:         p.Message,
		TargetCommitSha: p.TargetCommitSha,
	})
	if err != nil {
		return "", err
	}
	return resp.TagSha, nil
}
