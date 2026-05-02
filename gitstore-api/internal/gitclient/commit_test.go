package gitclient

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestRepo(t *testing.T) (*CommitBuilder, string) {
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "gitstore-test-*")
	require.NoError(t, err)

	// Initialize git repository
	_, err = git.PlainInit(tmpDir, false)
	require.NoError(t, err)

	// Create commit builder
	cb, err := NewCommitBuilder(tmpDir)
	require.NoError(t, err)

	return cb, tmpDir
}

func cleanupTestRepo(t *testing.T, path string) {
	err := os.RemoveAll(path)
	require.NoError(t, err)
}

func TestNewCommitBuilder(t *testing.T) {
	t.Run("should open existing repository", func(t *testing.T) {
		cb, tmpDir := setupTestRepo(t)
		defer cleanupTestRepo(t, tmpDir)

		assert.NotNil(t, cb)
		assert.NotNil(t, cb.repo)
		assert.Equal(t, tmpDir, cb.path)
	})

	t.Run("should fail for non-existent repository", func(t *testing.T) {
		cb, err := NewCommitBuilder("/non/existent/path")
		assert.Error(t, err)
		assert.Nil(t, cb)
	})
}

func TestWriteFile(t *testing.T) {
	t.Run("should write file to repository", func(t *testing.T) {
		cb, tmpDir := setupTestRepo(t)
		defer cleanupTestRepo(t, tmpDir)

		content := "# Test Product\n\nTest content"
		err := cb.WriteFile("products/test.md", content)
		require.NoError(t, err)

		// Verify file exists
		fullPath := filepath.Join(tmpDir, "products/test.md")
		data, err := os.ReadFile(fullPath)
		require.NoError(t, err)
		assert.Equal(t, content, string(data))
	})

	t.Run("should create directories if they don't exist", func(t *testing.T) {
		cb, tmpDir := setupTestRepo(t)
		defer cleanupTestRepo(t, tmpDir)

		err := cb.WriteFile("products/electronics/laptop.md", "content")
		require.NoError(t, err)

		// Verify directory was created
		dirPath := filepath.Join(tmpDir, "products/electronics")
		info, err := os.Stat(dirPath)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	})
}

func TestDeleteFile(t *testing.T) {
	t.Run("should delete existing file", func(t *testing.T) {
		cb, tmpDir := setupTestRepo(t)
		defer cleanupTestRepo(t, tmpDir)

		// Create file first
		err := cb.WriteFile("test.md", "content")
		require.NoError(t, err)

		// Delete it
		err = cb.DeleteFile("test.md")
		require.NoError(t, err)

		// Verify it's gone
		fullPath := filepath.Join(tmpDir, "test.md")
		_, err = os.Stat(fullPath)
		assert.True(t, os.IsNotExist(err))
	})

	t.Run("should not error for non-existent file", func(t *testing.T) {
		cb, tmpDir := setupTestRepo(t)
		defer cleanupTestRepo(t, tmpDir)

		err := cb.DeleteFile("non-existent.md")
		assert.NoError(t, err) // Should not error
	})
}

func TestStageFile(t *testing.T) {
	t.Run("should stage file changes", func(t *testing.T) {
		cb, tmpDir := setupTestRepo(t)
		defer cleanupTestRepo(t, tmpDir)

		// Write and stage file
		err := cb.WriteFile("test.md", "content")
		require.NoError(t, err)

		err = cb.StageFile("test.md")
		require.NoError(t, err)

		// Check status
		status, err := cb.GetStatus()
		require.NoError(t, err)

		// File should be staged
		fileStatus := status.File("test.md")
		assert.NotNil(t, fileStatus)
		assert.Equal(t, git.Added, fileStatus.Staging)
	})
}

func TestCommit(t *testing.T) {
	t.Run("should create commit with message", func(t *testing.T) {
		cb, tmpDir := setupTestRepo(t)
		defer cleanupTestRepo(t, tmpDir)

		// Write and stage file
		err := cb.WriteFile("test.md", "content")
		require.NoError(t, err)
		err = cb.StageFile("test.md")
		require.NoError(t, err)

		// Commit
		commitHash, err := cb.Commit(CommitOptions{
			Message: "test: add test file",
		})
		require.NoError(t, err)
		assert.NotEmpty(t, commitHash)

		// Verify commit exists
		ref, err := cb.repo.Head()
		require.NoError(t, err)
		assert.Equal(t, commitHash, ref.Hash().String())
	})

	t.Run("should use default author if not provided", func(t *testing.T) {
		cb, tmpDir := setupTestRepo(t)
		defer cleanupTestRepo(t, tmpDir)

		// Write and stage file
		err := cb.WriteFile("test.md", "content")
		require.NoError(t, err)
		err = cb.StageFile("test.md")
		require.NoError(t, err)

		// Commit without specifying author
		_, err = cb.Commit(CommitOptions{
			Message: "test commit",
		})
		require.NoError(t, err)

		// Get commit object
		ref, err := cb.repo.Head()
		require.NoError(t, err)
		commit, err := cb.repo.CommitObject(ref.Hash())
		require.NoError(t, err)

		assert.Equal(t, "GitStore Admin", commit.Author.Name)
		assert.Equal(t, "admin@gitstore.local", commit.Author.Email)
	})
}

func TestHasChanges(t *testing.T) {
	t.Run("should detect uncommitted changes", func(t *testing.T) {
		cb, tmpDir := setupTestRepo(t)
		defer cleanupTestRepo(t, tmpDir)

		// Initially clean
		hasChanges, err := cb.HasChanges()
		require.NoError(t, err)
		assert.False(t, hasChanges)

		// Write file (unstaged)
		err = cb.WriteFile("test.md", "content")
		require.NoError(t, err)

		// Should detect changes
		hasChanges, err = cb.HasChanges()
		require.NoError(t, err)
		assert.True(t, hasChanges)
	})
}

func TestCommitChange(t *testing.T) {
	t.Run("should write, stage, and commit in one operation", func(t *testing.T) {
		cb, tmpDir := setupTestRepo(t)
		defer cleanupTestRepo(t, tmpDir)

		commitHash, err := cb.CommitChange(
			"products/test.md",
			"# Test Product\n\nTest content",
			"feat: add test product",
		)
		require.NoError(t, err)
		assert.NotEmpty(t, commitHash)

		// Verify file exists
		fullPath := filepath.Join(tmpDir, "products/test.md")
		_, err = os.Stat(fullPath)
		require.NoError(t, err)

		// Verify commit exists
		ref, err := cb.repo.Head()
		require.NoError(t, err)
		assert.Equal(t, commitHash, ref.Hash().String())
	})
}

func TestCommitMultiple(t *testing.T) {
	t.Run("should commit multiple files in one commit", func(t *testing.T) {
		cb, tmpDir := setupTestRepo(t)
		defer cleanupTestRepo(t, tmpDir)

		changes := map[string]string{
			"products/product1.md":   "# Product 1",
			"products/product2.md":   "# Product 2",
			"categories/category.md": "# Category",
		}

		commitHash, err := cb.CommitMultiple(changes, "feat: add multiple files")
		require.NoError(t, err)
		assert.NotEmpty(t, commitHash)

		// Verify all files exist
		for filePath := range changes {
			fullPath := filepath.Join(tmpDir, filePath)
			_, err = os.Stat(fullPath)
			require.NoError(t, err)
		}
	})
}

func TestGenerateCommitMessage(t *testing.T) {
	tests := []struct {
		name       string
		action     string
		entityType string
		entityID   string
		summary    string
		expected   string
	}{
		{
			name:       "create with summary",
			action:     "create",
			entityType: "product",
			entityID:   "LAPTOP-001",
			summary:    "Premium Laptop",
			expected:   "create: product LAPTOP-001 - Premium Laptop",
		},
		{
			name:       "update without summary",
			action:     "update",
			entityType: "category",
			entityID:   "electronics",
			summary:    "",
			expected:   "update: category electronics",
		},
		{
			name:       "delete with summary",
			action:     "delete",
			entityType: "collection",
			entityID:   "winter-2026",
			summary:    "Seasonal collection ended",
			expected:   "delete: collection winter-2026 - Seasonal collection ended",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateCommitMessage(tt.action, tt.entityType, tt.entityID, tt.summary)
			assert.Equal(t, tt.expected, result)
		})
	}
}
