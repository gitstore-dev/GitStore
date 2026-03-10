package gitclient

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// CommitBuilder handles git commit operations
type CommitBuilder struct {
	repo *git.Repository
	path string
}

// NewCommitBuilder creates a new commit builder for a git repository
func NewCommitBuilder(repoPath string) (*CommitBuilder, error) {
	// Open existing repository
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	return &CommitBuilder{
		repo: repo,
		path: repoPath,
	}, nil
}

// WriteFile writes content to a file in the repository
func (cb *CommitBuilder) WriteFile(filePath string, content string) error {
	fullPath := filepath.Join(cb.path, filePath)

	// Create directory if it doesn't exist
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Write file
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write file %s: %w", filePath, err)
	}

	return nil
}

// DeleteFile deletes a file from the repository
func (cb *CommitBuilder) DeleteFile(filePath string) error {
	fullPath := filepath.Join(cb.path, filePath)

	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete file %s: %w", filePath, err)
	}

	return nil
}

// StageFile stages a file for commit
func (cb *CommitBuilder) StageFile(filePath string) error {
	worktree, err := cb.repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}

	// Add file to staging area
	if _, err := worktree.Add(filePath); err != nil {
		return fmt.Errorf("failed to stage file %s: %w", filePath, err)
	}

	return nil
}

// StageAll stages all changes in the repository
func (cb *CommitBuilder) StageAll() error {
	worktree, err := cb.repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}

	// Add all changes (new, modified, deleted files)
	if _, err := worktree.Add("."); err != nil {
		return fmt.Errorf("failed to stage all changes: %w", err)
	}

	return nil
}

// CommitOptions contains options for creating a commit
type CommitOptions struct {
	Message   string
	Author    *object.Signature
	Committer *object.Signature
}

// Commit creates a commit with the staged changes
func (cb *CommitBuilder) Commit(opts CommitOptions) (string, error) {
	worktree, err := cb.repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("failed to get worktree: %w", err)
	}

	// Set default author/committer if not provided
	if opts.Author == nil {
		opts.Author = &object.Signature{
			Name:  "GitStore Admin",
			Email: "admin@gitstore.local",
			When:  time.Now(),
		}
	}
	if opts.Committer == nil {
		opts.Committer = opts.Author
	}

	// Create commit
	commitHash, err := worktree.Commit(opts.Message, &git.CommitOptions{
		Author:    opts.Author,
		Committer: opts.Committer,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create commit: %w", err)
	}

	return commitHash.String(), nil
}

// GetStatus returns the current status of the working tree
func (cb *CommitBuilder) GetStatus() (git.Status, error) {
	worktree, err := cb.repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("failed to get worktree: %w", err)
	}

	status, err := worktree.Status()
	if err != nil {
		return nil, fmt.Errorf("failed to get status: %w", err)
	}

	return status, nil
}

// HasChanges checks if there are any uncommitted changes
func (cb *CommitBuilder) HasChanges() (bool, error) {
	status, err := cb.GetStatus()
	if err != nil {
		return false, err
	}

	return !status.IsClean(), nil
}

// CommitChange is a convenience method to write a file, stage it, and commit in one operation
func (cb *CommitBuilder) CommitChange(filePath string, content string, message string) (string, error) {
	// Write file
	if err := cb.WriteFile(filePath, content); err != nil {
		return "", err
	}

	// Stage file
	if err := cb.StageFile(filePath); err != nil {
		return "", err
	}

	// Commit
	return cb.Commit(CommitOptions{
		Message: message,
	})
}

// CommitDelete is a convenience method to delete a file, stage it, and commit
func (cb *CommitBuilder) CommitDelete(filePath string, message string) (string, error) {
	// Delete file
	if err := cb.DeleteFile(filePath); err != nil {
		return "", err
	}

	// Stage deletion
	if err := cb.StageFile(filePath); err != nil {
		return "", err
	}

	// Commit
	return cb.Commit(CommitOptions{
		Message: message,
	})
}

// CommitMultiple commits multiple file changes in a single commit
func (cb *CommitBuilder) CommitMultiple(changes map[string]string, message string) (string, error) {
	// Write all files
	for filePath, content := range changes {
		if err := cb.WriteFile(filePath, content); err != nil {
			return "", err
		}
	}

	// Stage all changes
	if err := cb.StageAll(); err != nil {
		return "", err
	}

	// Commit
	return cb.Commit(CommitOptions{
		Message: message,
	})
}

// GenerateCommitMessage generates a descriptive commit message for catalog changes
func GenerateCommitMessage(action string, entityType string, entityID string, summary string) string {
	if summary != "" {
		return fmt.Sprintf("%s: %s %s - %s", action, entityType, entityID, summary)
	}
	return fmt.Sprintf("%s: %s %s", action, entityType, entityID)
}
