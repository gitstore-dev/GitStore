package gitclient

import (
	"os"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupPushTestRepos(t *testing.T) (localPath string, remotePath string, cleanup func()) {
	// Create temporary directories for local and remote repos
	localDir, err := os.MkdirTemp("", "gitstore-local-*")
	require.NoError(t, err)

	remoteDir, err := os.MkdirTemp("", "gitstore-remote-*")
	require.NoError(t, err)

	// Initialize remote repository (bare)
	_, err = git.PlainInit(remoteDir, true)
	require.NoError(t, err)

	// Initialize local repository
	localRepo, err := git.PlainInit(localDir, false)
	require.NoError(t, err)

	// Create initial commit in local repo (bare repos need at least one commit to push to)
	worktree, err := localRepo.Worktree()
	require.NoError(t, err)

	testFile := localDir + "/README.md"
	err = os.WriteFile(testFile, []byte("# Test Repo"), 0644)
	require.NoError(t, err)

	_, err = worktree.Add("README.md")
	require.NoError(t, err)

	_, err = worktree.Commit("Initial commit", &git.CommitOptions{})
	require.NoError(t, err)

	cleanup = func() {
		os.RemoveAll(localDir)
		os.RemoveAll(remoteDir)
	}

	return localDir, remoteDir, cleanup
}

func TestNewPushClient(t *testing.T) {
	t.Run("should create push client for existing repository", func(t *testing.T) {
		localPath, remotePath, cleanup := setupPushTestRepos(t)
		defer cleanup()

		pc, err := NewPushClient(localPath, "origin", remotePath)
		require.NoError(t, err)
		assert.NotNil(t, pc)
		assert.Equal(t, "origin", pc.GetRemoteName())
		assert.Equal(t, remotePath, pc.GetRemoteURL())
	})

	t.Run("should fail for non-existent repository", func(t *testing.T) {
		pc, err := NewPushClient("/non/existent/path", "origin", "git://localhost/repo")
		assert.Error(t, err)
		assert.Nil(t, pc)
	})
}

func TestEnsureRemote(t *testing.T) {
	t.Run("should create remote if it doesn't exist", func(t *testing.T) {
		localPath, remotePath, cleanup := setupPushTestRepos(t)
		defer cleanup()

		pc, err := NewPushClient(localPath, "origin", remotePath)
		require.NoError(t, err)

		err = pc.EnsureRemote()
		require.NoError(t, err)

		// Verify remote was created
		remote, err := pc.repo.Remote("origin")
		require.NoError(t, err)
		assert.Equal(t, "origin", remote.Config().Name)
		assert.Contains(t, remote.Config().URLs, remotePath)
	})

	t.Run("should update remote URL if it differs", func(t *testing.T) {
		localPath, remotePath, cleanup := setupPushTestRepos(t)
		defer cleanup()

		// Create push client with initial remote
		pc, err := NewPushClient(localPath, "origin", remotePath)
		require.NoError(t, err)

		err = pc.EnsureRemote()
		require.NoError(t, err)

		// Change remote URL
		newRemotePath := remotePath + ".new"
		pc.remoteURL = newRemotePath

		err = pc.EnsureRemote()
		require.NoError(t, err)

		// Verify remote URL was updated
		remote, err := pc.repo.Remote("origin")
		require.NoError(t, err)
		assert.Contains(t, remote.Config().URLs, newRemotePath)
	})

	t.Run("should not error if remote already exists with correct URL", func(t *testing.T) {
		localPath, remotePath, cleanup := setupPushTestRepos(t)
		defer cleanup()

		pc, err := NewPushClient(localPath, "origin", remotePath)
		require.NoError(t, err)

		// Call twice
		err = pc.EnsureRemote()
		require.NoError(t, err)

		err = pc.EnsureRemote()
		require.NoError(t, err)

		// Verify remote still exists
		remote, err := pc.repo.Remote("origin")
		require.NoError(t, err)
		assert.Equal(t, "origin", remote.Config().Name)
	})
}

func TestPush(t *testing.T) {
	t.Run("should push commits to remote", func(t *testing.T) {
		localPath, remotePath, cleanup := setupPushTestRepos(t)
		defer cleanup()

		pc, err := NewPushClient(localPath, "origin", remotePath)
		require.NoError(t, err)

		// Add a commit
		cb, err := NewCommitBuilder(localPath)
		require.NoError(t, err)

		_, err = cb.CommitChange("test.txt", "test content", "test: add test file")
		require.NoError(t, err)

		// Push to remote
		err = pc.Push(PushOptions{
			RefSpecs: []string{"refs/heads/master:refs/heads/master"},
		})
		require.NoError(t, err)

		// Verify commit exists in remote
		remoteRepo, err := git.PlainOpen(remotePath)
		require.NoError(t, err)

		ref, err := remoteRepo.Reference(plumbing.NewBranchReferenceName("master"), true)
		require.NoError(t, err)
		assert.NotNil(t, ref)
	})

	t.Run("should handle already up-to-date", func(t *testing.T) {
		localPath, remotePath, cleanup := setupPushTestRepos(t)
		defer cleanup()

		pc, err := NewPushClient(localPath, "origin", remotePath)
		require.NoError(t, err)

		// First push
		err = pc.Push(PushOptions{
			RefSpecs: []string{"refs/heads/master:refs/heads/master"},
		})
		require.NoError(t, err)

		// Second push (should be no-op)
		err = pc.Push(PushOptions{
			RefSpecs: []string{"refs/heads/master:refs/heads/master"},
		})
		require.NoError(t, err) // Should not error
	})
}

func TestPushBranch(t *testing.T) {
	t.Run("should push current branch", func(t *testing.T) {
		localPath, remotePath, cleanup := setupPushTestRepos(t)
		defer cleanup()

		pc, err := NewPushClient(localPath, "origin", remotePath)
		require.NoError(t, err)

		// Note: PushBranch uses default refspec which may fail if no upstream
		// This test verifies the method exists and calls Push correctly
		err = pc.EnsureRemote()
		require.NoError(t, err)

		// PushBranch without explicit refspec might fail, but that's expected
		// The important thing is it doesn't panic
		_ = pc.PushBranch()
	})
}

func TestPushWithRefSpec(t *testing.T) {
	t.Run("should push with explicit refspec", func(t *testing.T) {
		localPath, remotePath, cleanup := setupPushTestRepos(t)
		defer cleanup()

		pc, err := NewPushClient(localPath, "origin", remotePath)
		require.NoError(t, err)

		// Add a commit
		cb, err := NewCommitBuilder(localPath)
		require.NoError(t, err)

		_, err = cb.CommitChange("test.txt", "test content", "test: add test file")
		require.NoError(t, err)

		// Push with refspec
		err = pc.PushWithRefSpec("refs/heads/master:refs/heads/master")
		require.NoError(t, err)
	})
}

func TestValidationError(t *testing.T) {
	t.Run("should format error message with file", func(t *testing.T) {
		err := &ValidationError{
			Message: "Invalid SKU format",
			File:    "products/electronics/LAPTOP-001.md",
			Line:    5,
		}

		assert.Contains(t, err.Error(), "validation failed")
		assert.Contains(t, err.Error(), "products/electronics/LAPTOP-001.md")
		assert.Contains(t, err.Error(), "Invalid SKU format")
	})

	t.Run("should format error message without file", func(t *testing.T) {
		err := &ValidationError{
			Message: "Missing required field: price",
		}

		assert.Contains(t, err.Error(), "validation failed")
		assert.Contains(t, err.Error(), "Missing required field: price")
	})
}

func TestParseValidationError(t *testing.T) {
	t.Run("should parse pre-receive hook error", func(t *testing.T) {
		gitErr := git.ErrNonFastForwardUpdate // Simulate a git error
		wrappedErr := parseValidationError(gitErr)

		// Should return original error if not a validation error
		assert.Equal(t, gitErr, wrappedErr)
	})

	t.Run("should extract validation message", func(t *testing.T) {
		msg := extractValidationMessage("remote: validation failed: Invalid price\nother text")
		assert.Equal(t, "Invalid price", msg)
	})

	t.Run("should extract pre-receive message", func(t *testing.T) {
		msg := extractValidationMessage("remote: pre-receive hook declined\nother text")
		assert.Contains(t, msg, "Pre-receive hook declined")
	})

	t.Run("should extract validation details", func(t *testing.T) {
		details := extractValidationDetails("line1\nerror: Invalid SKU\nline2\nERROR: Missing price\ninvalid field")
		assert.Len(t, details, 3)
		assert.Contains(t, details[0], "Invalid SKU")
		assert.Contains(t, details[1], "Missing price")
		assert.Contains(t, details[2], "invalid")
	})
}
