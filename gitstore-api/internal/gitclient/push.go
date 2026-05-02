package gitclient

import (
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// PushClient handles git push operations
type PushClient struct {
	repo       *git.Repository
	remoteName string
	remoteURL  string
}

// NewPushClient creates a new push client for a git repository
func NewPushClient(repoPath string, remoteName string, remoteURL string) (*PushClient, error) {
	// Open existing repository
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	return &PushClient{
		repo:       repo,
		remoteName: remoteName,
		remoteURL:  remoteURL,
	}, nil
}

// EnsureRemote ensures the remote exists with the correct URL
func (pc *PushClient) EnsureRemote() error {
	// Get or create remote
	remote, err := pc.repo.Remote(pc.remoteName)
	if err == git.ErrRemoteNotFound {
		// Create new remote
		_, err = pc.repo.CreateRemote(&config.RemoteConfig{
			Name: pc.remoteName,
			URLs: []string{pc.remoteURL},
		})
		if err != nil {
			return fmt.Errorf("failed to create remote %s: %w", pc.remoteName, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get remote %s: %w", pc.remoteName, err)
	}

	// Verify remote URL matches
	remoteConfig := remote.Config()
	if len(remoteConfig.URLs) == 0 || remoteConfig.URLs[0] != pc.remoteURL {
		// Update remote URL
		err = pc.repo.DeleteRemote(pc.remoteName)
		if err != nil {
			return fmt.Errorf("failed to delete remote for update: %w", err)
		}
		_, err = pc.repo.CreateRemote(&config.RemoteConfig{
			Name: pc.remoteName,
			URLs: []string{pc.remoteURL},
		})
		if err != nil {
			return fmt.Errorf("failed to recreate remote: %w", err)
		}
	}

	return nil
}

// PushOptions contains options for pushing to remote
type PushOptions struct {
	// RefSpecs to push (e.g., "refs/heads/main:refs/heads/main")
	// If empty, pushes current branch
	RefSpecs []string

	// Force push (not recommended for shared branches)
	Force bool

	// Auth credentials (if required)
	Auth transport.AuthMethod
}

// ValidationError represents a validation error from the git server
type ValidationError struct {
	Message string
	File    string
	Line    int
	Details []string
}

func (e *ValidationError) Error() string {
	if e.File != "" {
		return fmt.Sprintf("validation failed for %s: %s", e.File, e.Message)
	}
	return fmt.Sprintf("validation failed: %s", e.Message)
}

// Push pushes commits to the remote repository
func (pc *PushClient) Push(opts PushOptions) error {
	// Ensure remote exists
	if err := pc.EnsureRemote(); err != nil {
		return err
	}

	// Build push options
	pushOpts := &git.PushOptions{
		RemoteName: pc.remoteName,
		Auth:       opts.Auth,
		Force:      opts.Force,
	}

	// Add refspecs if provided
	if len(opts.RefSpecs) > 0 {
		pushOpts.RefSpecs = make([]config.RefSpec, len(opts.RefSpecs))
		for i, spec := range opts.RefSpecs {
			pushOpts.RefSpecs[i] = config.RefSpec(spec)
		}
	}

	// Execute push
	err := pc.repo.Push(pushOpts)
	if err != nil {
		// Check if it's already up-to-date
		if err == git.NoErrAlreadyUpToDate {
			return nil
		}

		// Parse validation errors from git server
		if strings.Contains(err.Error(), "validation") {
			return parseValidationError(err)
		}

		return fmt.Errorf("failed to push to remote: %w", err)
	}

	return nil
}

// PushBranch pushes the current branch to remote
func (pc *PushClient) PushBranch() error {
	return pc.Push(PushOptions{})
}

// PushWithRefSpec pushes specific refs to remote
func (pc *PushClient) PushWithRefSpec(refSpecs ...string) error {
	return pc.Push(PushOptions{
		RefSpecs: refSpecs,
	})
}

// parseValidationError attempts to extract validation error details from git error
func parseValidationError(err error) error {
	errMsg := err.Error()

	// Look for validation error patterns in git output
	if strings.Contains(errMsg, "pre-receive hook declined") ||
		strings.Contains(errMsg, "validation failed") {
		return &ValidationError{
			Message: extractValidationMessage(errMsg),
			Details: extractValidationDetails(errMsg),
		}
	}

	return err
}

// extractValidationMessage extracts the main validation error message
func extractValidationMessage(errMsg string) string {
	// Try to find message between "validation failed:" and next newline
	if idx := strings.Index(errMsg, "validation failed:"); idx != -1 {
		msg := errMsg[idx+len("validation failed:"):]
		if endIdx := strings.Index(msg, "\n"); endIdx != -1 {
			return strings.TrimSpace(msg[:endIdx])
		}
		return strings.TrimSpace(msg)
	}

	// Fallback to pre-receive hook message
	if idx := strings.Index(errMsg, "pre-receive hook declined"); idx != -1 {
		return "Pre-receive hook declined the push"
	}

	return "Unknown validation error"
}

// extractValidationDetails extracts detailed validation error lines
func extractValidationDetails(errMsg string) []string {
	var details []string

	lines := strings.Split(errMsg, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Look for error indicators
		if strings.Contains(line, "error:") ||
			strings.Contains(line, "ERROR:") ||
			strings.Contains(line, "invalid") {
			details = append(details, line)
		}
	}

	return details
}

// GetRemoteURL returns the configured remote URL
func (pc *PushClient) GetRemoteURL() string {
	return pc.remoteURL
}

// GetRemoteName returns the configured remote name
func (pc *PushClient) GetRemoteName() string {
	return pc.remoteName
}
