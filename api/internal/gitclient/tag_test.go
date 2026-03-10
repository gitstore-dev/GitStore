package gitclient

import (
	"os"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTagTestRepo(t *testing.T) (*TagClient, string) {
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "gitstore-tag-test-*")
	require.NoError(t, err)

	// Initialize git repository
	repo, err := git.PlainInit(tmpDir, false)
	require.NoError(t, err)

	// Create initial commit
	worktree, err := repo.Worktree()
	require.NoError(t, err)

	testFile := tmpDir + "/README.md"
	err = os.WriteFile(testFile, []byte("# Test"), 0644)
	require.NoError(t, err)

	_, err = worktree.Add("README.md")
	require.NoError(t, err)

	_, err = worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// Create tag client
	tc, err := NewTagClient(tmpDir)
	require.NoError(t, err)

	return tc, tmpDir
}

func cleanupTagTestRepo(t *testing.T, path string) {
	err := os.RemoveAll(path)
	require.NoError(t, err)
}

func TestNewTagClient(t *testing.T) {
	t.Run("should create tag client for existing repository", func(t *testing.T) {
		tc, tmpDir := setupTagTestRepo(t)
		defer cleanupTagTestRepo(t, tmpDir)

		assert.NotNil(t, tc)
		assert.NotNil(t, tc.repo)
	})

	t.Run("should fail for non-existent repository", func(t *testing.T) {
		tc, err := NewTagClient("/non/existent/path")
		assert.Error(t, err)
		assert.Nil(t, tc)
	})
}

func TestCreateTag(t *testing.T) {
	t.Run("should create annotated tag with semver name", func(t *testing.T) {
		tc, tmpDir := setupTagTestRepo(t)
		defer cleanupTagTestRepo(t, tmpDir)

		tagHash, err := tc.CreateTag(TagOptions{
			Name:    "v1.0.0",
			Message: "Release version 1.0.0",
		})
		require.NoError(t, err)
		assert.NotEmpty(t, tagHash)

		// Verify tag exists
		exists, err := tc.TagExists("v1.0.0")
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("should create tag with date-based name", func(t *testing.T) {
		tc, tmpDir := setupTagTestRepo(t)
		defer cleanupTagTestRepo(t, tmpDir)

		tagHash, err := tc.CreateTag(TagOptions{
			Name:    "2026-03-10",
			Message: "Release 2026-03-10",
		})
		require.NoError(t, err)
		assert.NotEmpty(t, tagHash)

		// Verify tag exists
		exists, err := tc.TagExists("2026-03-10")
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("should use default tagger if not provided", func(t *testing.T) {
		tc, tmpDir := setupTagTestRepo(t)
		defer cleanupTagTestRepo(t, tmpDir)

		_, err := tc.CreateTag(TagOptions{
			Name:    "v1.0.0",
			Message: "Release",
		})
		require.NoError(t, err)

		// Get tag and verify tagger
		tag, err := tc.GetTag("v1.0.0")
		require.NoError(t, err)
		assert.Equal(t, "GitStore Admin", tag.Tagger.Name)
		assert.Equal(t, "admin@gitstore.local", tag.Tagger.Email)
	})

	t.Run("should use custom tagger if provided", func(t *testing.T) {
		tc, tmpDir := setupTagTestRepo(t)
		defer cleanupTagTestRepo(t, tmpDir)

		customTagger := &object.Signature{
			Name:  "Custom User",
			Email: "custom@example.com",
			When:  time.Now(),
		}

		_, err := tc.CreateTag(TagOptions{
			Name:    "v1.0.0",
			Message: "Release",
			Tagger:  customTagger,
		})
		require.NoError(t, err)

		// Get tag and verify tagger
		tag, err := tc.GetTag("v1.0.0")
		require.NoError(t, err)
		assert.Equal(t, "Custom User", tag.Tagger.Name)
		assert.Equal(t, "custom@example.com", tag.Tagger.Email)
	})

	t.Run("should fail for invalid tag name", func(t *testing.T) {
		tc, tmpDir := setupTagTestRepo(t)
		defer cleanupTagTestRepo(t, tmpDir)

		_, err := tc.CreateTag(TagOptions{
			Name:    "invalid tag name",
			Message: "Release",
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid")
	})
}

func TestListTags(t *testing.T) {
	t.Run("should list all tags", func(t *testing.T) {
		tc, tmpDir := setupTagTestRepo(t)
		defer cleanupTagTestRepo(t, tmpDir)

		// Create multiple tags
		_, err := tc.CreateTag(TagOptions{Name: "v1.0.0", Message: "Release 1"})
		require.NoError(t, err)

		_, err = tc.CreateTag(TagOptions{Name: "v1.1.0", Message: "Release 2"})
		require.NoError(t, err)

		_, err = tc.CreateTag(TagOptions{Name: "v2.0.0", Message: "Release 3"})
		require.NoError(t, err)

		// List tags
		tags, err := tc.ListTags()
		require.NoError(t, err)
		assert.Len(t, tags, 3)
		assert.Contains(t, tags, "v1.0.0")
		assert.Contains(t, tags, "v1.1.0")
		assert.Contains(t, tags, "v2.0.0")
	})

	t.Run("should return empty list if no tags", func(t *testing.T) {
		tc, tmpDir := setupTagTestRepo(t)
		defer cleanupTagTestRepo(t, tmpDir)

		tags, err := tc.ListTags()
		require.NoError(t, err)
		assert.Empty(t, tags)
	})
}

func TestGetTag(t *testing.T) {
	t.Run("should get tag information", func(t *testing.T) {
		tc, tmpDir := setupTagTestRepo(t)
		defer cleanupTagTestRepo(t, tmpDir)

		_, err := tc.CreateTag(TagOptions{
			Name:    "v1.0.0",
			Message: "Test release",
		})
		require.NoError(t, err)

		tag, err := tc.GetTag("v1.0.0")
		require.NoError(t, err)
		assert.Equal(t, "v1.0.0", tag.Name)
		assert.Contains(t, tag.Message, "Test release")
	})

	t.Run("should fail for non-existent tag", func(t *testing.T) {
		tc, tmpDir := setupTagTestRepo(t)
		defer cleanupTagTestRepo(t, tmpDir)

		_, err := tc.GetTag("non-existent")
		assert.Error(t, err)
	})
}

func TestDeleteTag(t *testing.T) {
	t.Run("should delete existing tag", func(t *testing.T) {
		tc, tmpDir := setupTagTestRepo(t)
		defer cleanupTagTestRepo(t, tmpDir)

		// Create tag
		_, err := tc.CreateTag(TagOptions{Name: "v1.0.0", Message: "Release"})
		require.NoError(t, err)

		// Verify exists
		exists, err := tc.TagExists("v1.0.0")
		require.NoError(t, err)
		assert.True(t, exists)

		// Delete tag
		err = tc.DeleteTag("v1.0.0")
		require.NoError(t, err)

		// Verify deleted
		exists, err = tc.TagExists("v1.0.0")
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("should fail for non-existent tag", func(t *testing.T) {
		tc, tmpDir := setupTagTestRepo(t)
		defer cleanupTagTestRepo(t, tmpDir)

		err := tc.DeleteTag("non-existent")
		assert.Error(t, err)
	})
}

func TestTagExists(t *testing.T) {
	t.Run("should return true for existing tag", func(t *testing.T) {
		tc, tmpDir := setupTagTestRepo(t)
		defer cleanupTagTestRepo(t, tmpDir)

		_, err := tc.CreateTag(TagOptions{Name: "v1.0.0", Message: "Release"})
		require.NoError(t, err)

		exists, err := tc.TagExists("v1.0.0")
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("should return false for non-existent tag", func(t *testing.T) {
		tc, tmpDir := setupTagTestRepo(t)
		defer cleanupTagTestRepo(t, tmpDir)

		exists, err := tc.TagExists("non-existent")
		require.NoError(t, err)
		assert.False(t, exists)
	})
}

func TestValidateTagName(t *testing.T) {
	tests := []struct {
		name      string
		tagName   string
		shouldErr bool
	}{
		{"semver with v prefix", "v1.0.0", false},
		{"semver without v prefix", "1.0.0", false},
		{"semver with prerelease", "v1.0.0-beta", false},
		{"semver with build metadata", "v1.0.0-beta.1", false},
		{"date format YYYY-MM-DD", "2026-03-10", false},
		{"date format with time", "2026-03-10-14-30-00", false},
		{"custom alphanumeric", "release-1", false},
		{"custom with dots", "prod.v1.0", false},
		{"empty name", "", true},
		{"with spaces", "tag name", true},
		{"with colon", "tag:name", true},
		{"with brackets", "tag[1]", true},
		{"with tilde", "tag~1", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTagName(tt.tagName)
			if tt.shouldErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGenerateReleaseTagName(t *testing.T) {
	t.Run("should generate timestamp-based tag name", func(t *testing.T) {
		tagName := GenerateReleaseTagName()
		assert.NotEmpty(t, tagName)

		// Verify format (YYYY-MM-DD-HH-MM-SS)
		err := validateTagName(tagName)
		assert.NoError(t, err)
	})
}

func TestGenerateSemverTagName(t *testing.T) {
	t.Run("should generate v1.0.0 if no tags exist", func(t *testing.T) {
		tc, tmpDir := setupTagTestRepo(t)
		defer cleanupTagTestRepo(t, tmpDir)

		tagName, err := tc.GenerateSemverTagName()
		require.NoError(t, err)
		assert.Equal(t, "v1.0.0", tagName)
	})

	t.Run("should increment patch version", func(t *testing.T) {
		tc, tmpDir := setupTagTestRepo(t)
		defer cleanupTagTestRepo(t, tmpDir)

		// Create existing tags
		_, err := tc.CreateTag(TagOptions{Name: "v1.0.0", Message: "Release"})
		require.NoError(t, err)

		_, err = tc.CreateTag(TagOptions{Name: "v1.0.1", Message: "Release"})
		require.NoError(t, err)

		// Generate next version
		tagName, err := tc.GenerateSemverTagName()
		require.NoError(t, err)
		assert.Equal(t, "v1.0.2", tagName)
	})

	t.Run("should find latest version across different versions", func(t *testing.T) {
		tc, tmpDir := setupTagTestRepo(t)
		defer cleanupTagTestRepo(t, tmpDir)

		// Create tags out of order
		_, err := tc.CreateTag(TagOptions{Name: "v1.0.0", Message: "Release"})
		require.NoError(t, err)

		_, err = tc.CreateTag(TagOptions{Name: "v2.1.5", Message: "Release"})
		require.NoError(t, err)

		_, err = tc.CreateTag(TagOptions{Name: "v1.5.3", Message: "Release"})
		require.NoError(t, err)

		// Generate next version (should increment v2.1.5)
		tagName, err := tc.GenerateSemverTagName()
		require.NoError(t, err)
		assert.Equal(t, "v2.1.6", tagName)
	})
}

func TestPushTag(t *testing.T) {
	t.Run("should push tag to remote", func(t *testing.T) {
		// Setup local and remote repos
		localPath, remotePath, cleanup := setupPushTestRepos(t)
		defer cleanup()

		// Create tag client
		tc, err := NewTagClient(localPath)
		require.NoError(t, err)

		// Create tag
		_, err = tc.CreateTag(TagOptions{
			Name:    "v1.0.0",
			Message: "Release 1.0.0",
		})
		require.NoError(t, err)

		// Push tag
		err = tc.PushTag("v1.0.0", "origin", remotePath)
		require.NoError(t, err)

		// Verify tag exists in remote
		remoteRepo, err := git.PlainOpen(remotePath)
		require.NoError(t, err)

		_, err = remoteRepo.Tag("v1.0.0")
		require.NoError(t, err)
	})
}

func TestCreateAndPushTag(t *testing.T) {
	t.Run("should create and push tag in one operation", func(t *testing.T) {
		// Setup local and remote repos
		localPath, remotePath, cleanup := setupPushTestRepos(t)
		defer cleanup()

		// Create tag client
		tc, err := NewTagClient(localPath)
		require.NoError(t, err)

		// Create and push tag
		tagHash, err := tc.CreateAndPushTag(
			TagOptions{
				Name:    "v1.0.0",
				Message: "Release 1.0.0",
			},
			"origin",
			remotePath,
		)
		require.NoError(t, err)
		assert.NotEmpty(t, tagHash)

		// Verify tag exists locally
		exists, err := tc.TagExists("v1.0.0")
		require.NoError(t, err)
		assert.True(t, exists)

		// Verify tag exists in remote
		remoteRepo, err := git.PlainOpen(remotePath)
		require.NoError(t, err)

		_, err = remoteRepo.Tag("v1.0.0")
		require.NoError(t, err)
	})
}
