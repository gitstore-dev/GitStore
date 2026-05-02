package gitclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// HTTPGitClient handles git operations via HTTP protocol to the git-server
type HTTPGitClient struct {
	serverURL  string
	repoName   string
	localPath  string
	httpClient *http.Client
	logger     *zap.Logger
}

// NewHTTPGitClient creates a new HTTP git client with a pooled transport.
func NewHTTPGitClient(serverURL, repoName, localPath string, logger *zap.Logger) *HTTPGitClient {
	return &HTTPGitClient{
		serverURL:  strings.TrimSuffix(serverURL, "/"),
		repoName:   repoName,
		localPath:  localPath,
		httpClient: newPooledHTTPClient(30 * time.Second),
		logger:     logger,
	}
}

// PushChange creates a commit with the given file changes and pushes to git-server
func (c *HTTPGitClient) PushChange(ctx context.Context, filePath string, content string, commitMessage string) error {
	// Create a temporary working directory
	tmpDir, err := os.MkdirTemp("", "gitstore-push-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Clone the repository to temp directory
	cloneURL := fmt.Sprintf("%s/%s", c.serverURL, c.repoName)
	if err := c.gitClone(ctx, cloneURL, tmpDir); err != nil {
		return fmt.Errorf("failed to clone repository: %w", err)
	}

	// Write the file
	fullPath := filepath.Join(tmpDir, filePath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	// Git add, commit, and push
	if err := c.gitAdd(ctx, tmpDir, filePath); err != nil {
		return fmt.Errorf("failed to add file: %w", err)
	}
	if err := c.gitCommit(ctx, tmpDir, commitMessage); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}
	if err := c.gitPush(ctx, tmpDir, cloneURL); err != nil {
		return fmt.Errorf("failed to push: %w", err)
	}

	return nil
}

// PushDelete deletes a file and pushes the change to git-server
func (c *HTTPGitClient) PushDelete(ctx context.Context, filePath string, commitMessage string) error {
	// Create a temporary working directory
	tmpDir, err := os.MkdirTemp("", "gitstore-push-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Clone the repository to temp directory
	cloneURL := fmt.Sprintf("%s/%s", c.serverURL, c.repoName)
	if err := c.gitClone(ctx, cloneURL, tmpDir); err != nil {
		return fmt.Errorf("failed to clone repository: %w", err)
	}

	// Delete the file
	fullPath := filepath.Join(tmpDir, filePath)
	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete file: %w", err)
	}

	// Git add, commit, and push
	if err := c.gitAdd(ctx, tmpDir, filePath); err != nil {
		return fmt.Errorf("failed to add deletion: %w", err)
	}
	if err := c.gitCommit(ctx, tmpDir, commitMessage); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}
	if err := c.gitPush(ctx, tmpDir, cloneURL); err != nil {
		return fmt.Errorf("failed to push: %w", err)
	}

	return nil
}

// PushMultiple commits multiple file changes in a single commit and pushes
func (c *HTTPGitClient) PushMultiple(ctx context.Context, changes map[string]string, commitMessage string) error {
	// Create a temporary working directory
	tmpDir, err := os.MkdirTemp("", "gitstore-push-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Clone the repository to temp directory
	cloneURL := fmt.Sprintf("%s/%s", c.serverURL, c.repoName)
	if err := c.gitClone(ctx, cloneURL, tmpDir); err != nil {
		return fmt.Errorf("failed to clone repository: %w", err)
	}

	// Write all files
	for filePath, content := range changes {
		fullPath := filepath.Join(tmpDir, filePath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", filePath, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			return fmt.Errorf("failed to write file %s: %w", filePath, err)
		}
	}

	// Git add all, commit, and push
	if err := c.gitAddAll(ctx, tmpDir); err != nil {
		return fmt.Errorf("failed to add files: %w", err)
	}
	if err := c.gitCommit(ctx, tmpDir, commitMessage); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}
	if err := c.gitPush(ctx, tmpDir, cloneURL); err != nil {
		return fmt.Errorf("failed to push: %w", err)
	}

	return nil
}

// gitClone clones the repository to the specified directory
func (c *HTTPGitClient) gitClone(ctx context.Context, url, dir string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", url, dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		c.logger.Error("Git clone failed", zap.String("url", url), zap.String("output", string(output)), zap.Error(err))
		return fmt.Errorf("git clone failed: %s", string(output))
	}
	return nil
}

// gitAdd stages a file for commit
func (c *HTTPGitClient) gitAdd(ctx context.Context, repoDir, filePath string) error {
	cmd := exec.CommandContext(ctx, "git", "add", filePath)
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		c.logger.Error("Git add failed", zap.String("file", filePath), zap.String("output", string(output)), zap.Error(err))
		return fmt.Errorf("git add failed: %s", string(output))
	}
	return nil
}

// gitAddAll stages all changes
func (c *HTTPGitClient) gitAddAll(ctx context.Context, repoDir string) error {
	cmd := exec.CommandContext(ctx, "git", "add", "-A")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		c.logger.Error("Git add -A failed", zap.String("output", string(output)), zap.Error(err))
		return fmt.Errorf("git add -A failed: %s", string(output))
	}
	return nil
}

// gitCommit creates a commit with the staged changes
func (c *HTTPGitClient) gitCommit(ctx context.Context, repoDir, message string) error {
	cmd := exec.CommandContext(ctx, "git", "commit", "-m", message)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=GitStore Admin",
		"GIT_AUTHOR_EMAIL=admin@gitstore.local",
		"GIT_COMMITTER_NAME=GitStore Admin",
		"GIT_COMMITTER_EMAIL=admin@gitstore.local",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if there are no changes to commit
		if strings.Contains(string(output), "nothing to commit") {
			c.logger.Info("No changes to commit")
			return nil
		}
		c.logger.Error("Git commit failed", zap.String("output", string(output)), zap.Error(err))
		return fmt.Errorf("git commit failed: %s", string(output))
	}
	return nil
}

// gitPush pushes commits to the remote repository
func (c *HTTPGitClient) gitPush(ctx context.Context, repoDir, url string) error {
	cmd := exec.CommandContext(ctx, "git", "push", url, "main")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		c.logger.Error("Git push failed", zap.String("url", url), zap.String("output", string(output)), zap.Error(err))
		return fmt.Errorf("git push failed: %s", string(output))
	}
	c.logger.Info("Git push successful", zap.String("output", string(output)))
	return nil
}

// HealthCheck verifies connectivity to the git-server
func (c *HTTPGitClient) HealthCheck(ctx context.Context) error {
	url := fmt.Sprintf("%s/%s/info/refs?service=git-upload-pack", c.serverURL, c.repoName)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to git-server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("git-server returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
