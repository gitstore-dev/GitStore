package gitclient

import (
	"fmt"
	"regexp"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// TagClient handles git tag operations
type TagClient struct {
	repo *git.Repository
}

// NewTagClient creates a new tag client for a git repository
func NewTagClient(repoPath string) (*TagClient, error) {
	// Open existing repository
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	return &TagClient{
		repo: repo,
	}, nil
}

// TagOptions contains options for creating a tag
type TagOptions struct {
	// Tag name (e.g., "v1.0.0" or "2026-03-10")
	Name string

	// Tag message
	Message string

	// Tagger signature (optional, defaults to "GitStore Admin")
	Tagger *object.Signature

	// Target commit hash (optional, defaults to HEAD)
	Target string
}

// CreateTag creates an annotated tag
func (tc *TagClient) CreateTag(opts TagOptions) (string, error) {
	// Validate tag name
	if err := validateTagName(opts.Name); err != nil {
		return "", err
	}

	// Get target commit
	var targetHash plumbing.Hash
	if opts.Target != "" {
		targetHash = plumbing.NewHash(opts.Target)
	} else {
		// Use HEAD
		ref, err := tc.repo.Head()
		if err != nil {
			return "", fmt.Errorf("failed to get HEAD: %w", err)
		}
		targetHash = ref.Hash()
	}

	// Set default tagger if not provided
	if opts.Tagger == nil {
		opts.Tagger = &object.Signature{
			Name:  "GitStore Admin",
			Email: "admin@gitstore.local",
			When:  time.Now(),
		}
	}

	// Create annotated tag
	tagRef, err := tc.repo.CreateTag(opts.Name, targetHash, &git.CreateTagOptions{
		Tagger:  opts.Tagger,
		Message: opts.Message,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create tag %s: %w", opts.Name, err)
	}

	return tagRef.Hash().String(), nil
}

// PushTag pushes a tag to the remote repository
func (tc *TagClient) PushTag(tagName string, remoteName string, remoteURL string) error {
	// Create push client
	pushClient := &PushClient{
		repo:       tc.repo,
		remoteName: remoteName,
		remoteURL:  remoteURL,
	}

	// Ensure remote exists
	if err := pushClient.EnsureRemote(); err != nil {
		return err
	}

	// Push tag with refspec
	refSpec := fmt.Sprintf("refs/tags/%s:refs/tags/%s", tagName, tagName)
	return pushClient.PushWithRefSpec(refSpec)
}

// CreateAndPushTag creates a tag and pushes it to remote in one operation
func (tc *TagClient) CreateAndPushTag(opts TagOptions, remoteName string, remoteURL string) (string, error) {
	// Create tag
	tagHash, err := tc.CreateTag(opts)
	if err != nil {
		return "", err
	}

	// Push tag
	if err := tc.PushTag(opts.Name, remoteName, remoteURL); err != nil {
		return tagHash, fmt.Errorf("tag created locally but push failed: %w", err)
	}

	return tagHash, nil
}

// ListTags returns all tags in the repository
func (tc *TagClient) ListTags() ([]string, error) {
	tagRefs, err := tc.repo.Tags()
	if err != nil {
		return nil, fmt.Errorf("failed to list tags: %w", err)
	}

	var tags []string
	err = tagRefs.ForEach(func(ref *plumbing.Reference) error {
		// Extract tag name from reference (refs/tags/v1.0.0 -> v1.0.0)
		tagName := ref.Name().Short()
		tags = append(tags, tagName)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to iterate tags: %w", err)
	}

	return tags, nil
}

// GetTag retrieves tag information
func (tc *TagClient) GetTag(tagName string) (*object.Tag, error) {
	// Get tag reference
	ref, err := tc.repo.Tag(tagName)
	if err != nil {
		return nil, fmt.Errorf("failed to get tag %s: %w", tagName, err)
	}

	// Get tag object
	tagObj, err := tc.repo.TagObject(ref.Hash())
	if err != nil {
		// Might be a lightweight tag (not annotated)
		return nil, fmt.Errorf("tag %s is not an annotated tag: %w", tagName, err)
	}

	return tagObj, nil
}

// DeleteTag deletes a tag from the repository
func (tc *TagClient) DeleteTag(tagName string) error {
	if err := tc.repo.DeleteTag(tagName); err != nil {
		return fmt.Errorf("failed to delete tag %s: %w", tagName, err)
	}
	return nil
}

// TagExists checks if a tag exists
func (tc *TagClient) TagExists(tagName string) (bool, error) {
	_, err := tc.repo.Tag(tagName)
	if err == git.ErrTagNotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to check tag existence: %w", err)
	}
	return true, nil
}

// validateTagName validates tag name format
// Supports semantic versioning (v1.0.0) and date-based tags (2026-03-10)
func validateTagName(name string) error {
	if name == "" {
		return fmt.Errorf("tag name cannot be empty")
	}

	// Check for invalid characters
	if regexp.MustCompile(`[~^:\s\[\]\\]`).MatchString(name) {
		return fmt.Errorf("tag name contains invalid characters: %s", name)
	}

	// Check for semantic version format (v1.0.0, v1.0.0-beta, etc.)
	semverPattern := regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[a-zA-Z0-9.-]+)?$`)
	if semverPattern.MatchString(name) {
		return nil
	}

	// Check for date-based format (YYYY-MM-DD, YYYY-MM-DD-HH-MM-SS)
	datePattern := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(-\d{2}-\d{2}-\d{2})?$`)
	if datePattern.MatchString(name) {
		return nil
	}

	// Check for custom alphanumeric tags (release-1, prod-deploy, etc.)
	customPattern := regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	if customPattern.MatchString(name) {
		return nil
	}

	return fmt.Errorf("invalid tag name format: %s (use semver, date, or alphanumeric)", name)
}

// GenerateReleaseTagName generates a timestamped release tag name
func GenerateReleaseTagName() string {
	return time.Now().UTC().Format("2006-01-02-15-04-05")
}

// GenerateSemverTagName generates next semantic version tag
// Increments patch version (e.g., v1.0.0 -> v1.0.1)
func (tc *TagClient) GenerateSemverTagName() (string, error) {
	tags, err := tc.ListTags()
	if err != nil {
		return "", err
	}

	// Find latest semver tag
	var latest string
	var major, minor, patch int
	semverPattern := regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

	for _, tag := range tags {
		matches := semverPattern.FindStringSubmatch(tag)
		if matches != nil {
			var tagMajor, tagMinor, tagPatch int
			fmt.Sscanf(matches[1], "%d", &tagMajor)
			fmt.Sscanf(matches[2], "%d", &tagMinor)
			fmt.Sscanf(matches[3], "%d", &tagPatch)

			// Compare versions
			if tagMajor > major ||
				(tagMajor == major && tagMinor > minor) ||
				(tagMajor == major && tagMinor == minor && tagPatch > patch) {
				major, minor, patch = tagMajor, tagMinor, tagPatch
				latest = tag
			}
		}
	}

	// If no semver tags exist, start at v1.0.0
	if latest == "" {
		return "v1.0.0", nil
	}

	// Increment patch version
	patch++
	return fmt.Sprintf("v%d.%d.%d", major, minor, patch), nil
}

// PushAllTags pushes all tags to remote
func (tc *TagClient) PushAllTags(remoteName string, remoteURL string) error {
	pushClient := &PushClient{
		repo:       tc.repo,
		remoteName: remoteName,
		remoteURL:  remoteURL,
	}

	if err := pushClient.EnsureRemote(); err != nil {
		return err
	}

	return pushClient.Push(PushOptions{
		RefSpecs: []string{string(config.DefaultFetchRefSpec)},
	})
}
