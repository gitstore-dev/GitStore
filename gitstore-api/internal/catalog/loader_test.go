// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Unit tests for the gRPC-backed catalog Loader.
// Uses a mock GitReader so no network or Docker is required.

package catalog_test

import (
	"context"
	"fmt"
	"testing"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// mockGitReader implements catalog.GitReader for testing.
type mockGitReader struct {
	listFilesFunc    func(ctx context.Context, prefix, ref string) ([]*gitv1.FileEntry, error)
	readFileFunc     func(ctx context.Context, path, ref string) ([]byte, error)
	getLatestTagFunc func(ctx context.Context) (*gitv1.TagEntry, error)
	listTagsFunc     func(ctx context.Context, prefix string) ([]*gitv1.TagEntry, error)
}

func (m *mockGitReader) ListFiles(ctx context.Context, prefix, ref string) ([]*gitv1.FileEntry, error) {
	if m.listFilesFunc != nil {
		return m.listFilesFunc(ctx, prefix, ref)
	}
	return nil, nil
}

func (m *mockGitReader) ReadFile(ctx context.Context, path, ref string) ([]byte, error) {
	if m.readFileFunc != nil {
		return m.readFileFunc(ctx, path, ref)
	}
	return nil, fmt.Errorf("readFile: %s not found", path)
}

func (m *mockGitReader) GetLatestTag(ctx context.Context) (*gitv1.TagEntry, error) {
	if m.getLatestTagFunc != nil {
		return m.getLatestTagFunc(ctx)
	}
	return nil, fmt.Errorf("no tags")
}

func (m *mockGitReader) ListTags(ctx context.Context, prefix string) ([]*gitv1.TagEntry, error) {
	if m.listTagsFunc != nil {
		return m.listTagsFunc(ctx, prefix)
	}
	return nil, nil
}

const productMD = `---
id: prod-001
sku: SKU-001
title: Test Product
price: 9.99
currency: USD
inventory_status: in_stock
---
Product body.
`

const categoryMD = `---
id: cat-001
name: Widgets
slug: widgets
---
Category body.
`

func TestLoaderLoadFromTag(t *testing.T) {
	mock := &mockGitReader{
		listFilesFunc: func(_ context.Context, prefix, ref string) ([]*gitv1.FileEntry, error) {
			assert.Equal(t, "v1.0.0", ref)
			switch prefix {
			case "products/":
				return []*gitv1.FileEntry{{Path: "products/prod-001.md"}}, nil
			case "categories/":
				return []*gitv1.FileEntry{{Path: "categories/cat-001.md"}}, nil
			case "collections/":
				return nil, nil
			}
			return nil, nil
		},
		readFileFunc: func(_ context.Context, path, ref string) ([]byte, error) {
			assert.Equal(t, "v1.0.0", ref)
			switch path {
			case "products/prod-001.md":
				return []byte(productMD), nil
			case "categories/cat-001.md":
				return []byte(categoryMD), nil
			}
			return nil, fmt.Errorf("unexpected path: %s", path)
		},
	}

	loader := catalog.NewGRPCLoader(mock, zap.NewNop())
	cat, err := loader.LoadFromTag(context.Background(), "v1.0.0")
	require.NoError(t, err)

	assert.Equal(t, "v1.0.0", cat.Tag())
	assert.Equal(t, 1, cat.ProductCount())
	assert.Equal(t, 1, cat.CategoryCount())
	assert.Equal(t, 0, cat.CollectionCount())

	p, ok := cat.GetProduct("prod-001")
	require.True(t, ok)
	assert.Equal(t, "SKU-001", p.SKU)
	assert.Equal(t, 9.99, p.Price)
}

func TestLoaderLoadFromLatestTag(t *testing.T) {
	mock := &mockGitReader{
		getLatestTagFunc: func(_ context.Context) (*gitv1.TagEntry, error) {
			return &gitv1.TagEntry{Name: "v2.0.0", CommitSha: "abc"}, nil
		},
		listFilesFunc: func(_ context.Context, prefix, ref string) ([]*gitv1.FileEntry, error) {
			assert.Equal(t, "v2.0.0", ref)
			return nil, nil
		},
	}

	loader := catalog.NewGRPCLoader(mock, zap.NewNop())
	cat, err := loader.LoadFromLatestTag(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "v2.0.0", cat.Tag())
	assert.Equal(t, 0, cat.ProductCount())
}

func TestLoaderLoadFromLatestTag_NoTags(t *testing.T) {
	mock := &mockGitReader{
		getLatestTagFunc: func(_ context.Context) (*gitv1.TagEntry, error) {
			return nil, fmt.Errorf("no release tags found")
		},
	}

	loader := catalog.NewGRPCLoader(mock, zap.NewNop())
	_, err := loader.LoadFromLatestTag(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no release tags found")
}

func TestLoaderLoadFromTag_ListFilesError(t *testing.T) {
	mock := &mockGitReader{
		listFilesFunc: func(_ context.Context, prefix, ref string) ([]*gitv1.FileEntry, error) {
			if prefix == "products/" {
				return nil, fmt.Errorf("git-service unavailable")
			}
			return nil, nil
		},
	}

	loader := catalog.NewGRPCLoader(mock, zap.NewNop())
	_, err := loader.LoadFromTag(context.Background(), "v1.0.0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "git-service unavailable")
}

func TestLoaderParsesMalformedMarkdownGracefully(t *testing.T) {
	mock := &mockGitReader{
		listFilesFunc: func(_ context.Context, prefix, ref string) ([]*gitv1.FileEntry, error) {
			if prefix == "products/" {
				return []*gitv1.FileEntry{{Path: "products/bad.md"}}, nil
			}
			return nil, nil
		},
		readFileFunc: func(_ context.Context, path, _ string) ([]byte, error) {
			return []byte("no frontmatter here"), nil
		},
	}

	loader := catalog.NewGRPCLoader(mock, zap.NewNop())
	// malformed markdown: loader must skip, not fail
	cat, err := loader.LoadFromTag(context.Background(), "v1.0.0")
	require.NoError(t, err)
	assert.Equal(t, 0, cat.ProductCount())
}
