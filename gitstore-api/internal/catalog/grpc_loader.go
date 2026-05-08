// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// GRPCLoader loads the product catalogue via the gRPC git-service.
// It replaces the go-git-backed Loader once US1 is fully implemented.

package catalog

import (
	"context"
	"fmt"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"go.uber.org/zap"
)

// GitReader is the read subset of gitclient.Client used by the catalogue loader.
// Defined here so the catalog package has no import cycle with gitclient.
type GitReader interface {
	ReadFile(ctx context.Context, path, ref string) ([]byte, error)
	ListFiles(ctx context.Context, prefix, ref string) ([]*gitv1.FileEntry, error)
	GetLatestTag(ctx context.Context) (*gitv1.TagEntry, error)
	ListTags(ctx context.Context, prefix string) ([]*gitv1.TagEntry, error)
}

// GRPCLoader loads catalog data from git-service via gRPC.
type GRPCLoader struct {
	git    GitReader
	logger *zap.Logger
}

// NewGRPCLoader creates a GRPCLoader backed by the given GitReader.
func NewGRPCLoader(git GitReader, logger *zap.Logger) *GRPCLoader {
	return &GRPCLoader{git: git, logger: logger}
}

// LoadFromTag loads the catalogue at the given release tag.
func (l *GRPCLoader) LoadFromTag(ctx context.Context, tag string) (*Catalog, error) {
	l.logger.Info("loading catalogue from tag via gRPC", zap.String("tag", tag))

	cat := NewCatalog("", tag)

	prefixes := []string{"products/", "categories/", "collections/"}
	for _, prefix := range prefixes {
		entries, err := l.git.ListFiles(ctx, prefix, tag)
		if err != nil {
			return nil, fmt.Errorf("ListFiles(%s, %s): %w", prefix, tag, err)
		}
		for _, entry := range entries {
			content, err := l.git.ReadFile(ctx, entry.Path, tag)
			if err != nil {
				l.logger.Warn("failed to read file", zap.String("path", entry.Path), zap.Error(err))
				continue
			}
			if err := l.addEntry(cat, entry.Path, string(content)); err != nil {
				l.logger.Warn("failed to parse file", zap.String("path", entry.Path), zap.Error(err))
			}
		}
	}

	cat.BuildCategoryHierarchy()

	l.logger.Info("catalogue loaded",
		zap.String("tag", tag),
		zap.Int("products", cat.ProductCount()),
		zap.Int("categories", cat.CategoryCount()),
		zap.Int("collections", cat.CollectionCount()),
	)
	return cat, nil
}

// LoadFromLatestTag fetches the latest semver tag and loads the catalogue at that ref.
func (l *GRPCLoader) LoadFromLatestTag(ctx context.Context) (*Catalog, error) {
	tag, err := l.git.GetLatestTag(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetLatestTag: %w", err)
	}
	l.logger.Info("latest tag resolved", zap.String("tag", tag.Name))
	return l.LoadFromTag(ctx, tag.Name)
}

// addEntry dispatches file content to the right Catalog.Add* method based on path prefix.
func (l *GRPCLoader) addEntry(cat *Catalog, path, content string) error {
	switch {
	case len(path) > len("products/") && path[:len("products/")] == "products/":
		return cat.AddProductFromMarkdown(path, content)
	case len(path) > len("categories/") && path[:len("categories/")] == "categories/":
		return cat.AddCategoryFromMarkdown(path, content)
	case len(path) > len("collections/") && path[:len("collections/")] == "collections/":
		return cat.AddCollectionFromMarkdown(path, content)
	}
	return nil
}
