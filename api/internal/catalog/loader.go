// Catalog loader - loads catalog from git repository

package catalog

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// Loader loads catalog data from git repository
type Loader struct {
	repoPath string
	logger   *zap.Logger
}

// NewLoader creates a new catalog loader
func NewLoader(repoPath string, logger *zap.Logger) *Loader {
	return &Loader{
		repoPath: repoPath,
		logger:   logger,
	}
}

// LoadFromTag loads catalog from a specific release tag
func (l *Loader) LoadFromTag(ctx context.Context, tag string) (*Catalog, error) {
	l.logger.Info("Loading catalog from tag",
		zap.String("tag", tag),
		zap.String("repo", l.repoPath),
	)

	// Open repository
	repo, err := git.PlainOpen(l.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Resolve tag to commit
	tagRef, err := repo.Tag(tag)
	if err != nil {
		return nil, fmt.Errorf("tag not found: %s: %w", tag, err)
	}

	// Get commit from tag
	commit, err := repo.CommitObject(tagRef.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get commit: %w", err)
	}

	l.logger.Debug("Resolved tag to commit",
		zap.String("tag", tag),
		zap.String("commit", commit.Hash.String()),
	)

	// Load catalog from commit
	return l.loadFromCommit(ctx, repo, commit)
}

// LoadFromHEAD loads catalog from the current HEAD commit
func (l *Loader) LoadFromHEAD(ctx context.Context) (*Catalog, error) {
	l.logger.Info("Loading catalog from HEAD")

	// Open repository
	repo, err := git.PlainOpen(l.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Get HEAD reference
	ref, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}

	// Get commit from HEAD
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get commit: %w", err)
	}

	l.logger.Debug("Resolved HEAD to commit",
		zap.String("commit", commit.Hash.String()),
	)

	// Load catalog from commit
	return l.loadFromCommit(ctx, repo, commit)
}

// LoadFromLatestTag loads catalog from the latest release tag
func (l *Loader) LoadFromLatestTag(ctx context.Context) (*Catalog, error) {
	l.logger.Info("Loading catalog from latest tag")

	repo, err := git.PlainOpen(l.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Get all tags
	tags, err := repo.Tags()
	if err != nil {
		return nil, fmt.Errorf("failed to list tags: %w", err)
	}

	// Find latest version tag (starting with 'v')
	var latestTag *plumbing.Reference
	err = tags.ForEach(func(ref *plumbing.Reference) error {
		tagName := ref.Name().Short()
		if strings.HasPrefix(tagName, "v") {
			if latestTag == nil {
				latestTag = ref
			}
			// TODO: Proper semantic version comparison
			// For now, just use the last one found
			latestTag = ref
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to iterate tags: %w", err)
	}

	if latestTag == nil {
		return nil, fmt.Errorf("no release tags found")
	}

	tagName := latestTag.Name().Short()
	l.logger.Info("Found latest tag", zap.String("tag", tagName))

	return l.LoadFromTag(ctx, tagName)
}

// loadFromCommit loads catalog data from a specific commit
func (l *Loader) loadFromCommit(ctx context.Context, repo *git.Repository, commit *object.Commit) (*Catalog, error) {
	catalog := NewCatalog(commit.Hash.String())

	// Get tree from commit
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	// Walk tree and load markdown files
	err = tree.Files().ForEach(func(f *object.File) error {
		if filepath.Ext(f.Name) != ".md" {
			return nil
		}

		// Read file content
		content, err := f.Contents()
		if err != nil {
			l.logger.Warn("Failed to read file",
				zap.String("file", f.Name),
				zap.Error(err),
			)
			return nil // Continue with other files
		}

		// Parse and add to catalog based on path
		if strings.HasPrefix(f.Name, "products/") {
			if err := catalog.AddProductFromMarkdown(f.Name, content); err != nil {
				l.logger.Warn("Failed to parse product",
					zap.String("file", f.Name),
					zap.Error(err),
				)
			}
		} else if strings.HasPrefix(f.Name, "categories/") {
			if err := catalog.AddCategoryFromMarkdown(f.Name, content); err != nil {
				l.logger.Warn("Failed to parse category",
					zap.String("file", f.Name),
					zap.Error(err),
				)
			}
		} else if strings.HasPrefix(f.Name, "collections/") {
			if err := catalog.AddCollectionFromMarkdown(f.Name, content); err != nil {
				l.logger.Warn("Failed to parse collection",
					zap.String("file", f.Name),
					zap.Error(err),
				)
			}
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk tree: %w", err)
	}

	l.logger.Info("Catalog loaded successfully",
		zap.Int("products", catalog.ProductCount()),
		zap.Int("categories", catalog.CategoryCount()),
		zap.Int("collections", catalog.CollectionCount()),
	)

	return catalog, nil
}
