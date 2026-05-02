package graph

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishCatalog(t *testing.T) {
	t.Run("should require remote URL", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "") // No remote URL
		ctx := context.Background()

		input := PublishCatalogInput{}

		_, err := service.PublishCatalog(ctx, input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "remote URL not configured")
	})

	t.Run("should commit pending changes", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		// Set up bare remote repository
		remoteRepoPath := filepath.Join(os.TempDir(), "gitstore-test-remote-"+t.Name())
		defer os.RemoveAll(remoteRepoPath)

		_, err := git.PlainInit(remoteRepoPath, true) // true = bare repository
		require.NoError(t, err)

		service := NewProductMutationService(repoPath, "file://"+remoteRepoPath)
		ctx := context.Background()

		// Create a product without committing
		productPath := filepath.Join(repoPath, "products/electronics/test-product.md")
		err = os.MkdirAll(filepath.Dir(productPath), 0755)
		require.NoError(t, err)
		err = os.WriteFile(productPath, []byte("# Test Product\n"), 0644)
		require.NoError(t, err)

		// Publish catalog
		input := PublishCatalogInput{}

		payload, err := service.PublishCatalog(ctx, input)
		require.NoError(t, err)
		require.NotNil(t, payload)
		assert.True(t, payload.Success)
		assert.NotEmpty(t, payload.CommitHash)
		assert.NotEmpty(t, payload.TagName)

		// Verify commit was created
		repo, err := git.PlainOpen(repoPath)
		require.NoError(t, err)

		ref, err := repo.Head()
		require.NoError(t, err)

		commit, err := repo.CommitObject(ref.Hash())
		require.NoError(t, err)
		assert.Contains(t, commit.Message, "publish catalog")
	})

	t.Run("should create tag when no pending changes", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		// Set up bare remote repository
		remoteRepoPath := filepath.Join(os.TempDir(), "gitstore-test-remote-"+t.Name())
		defer os.RemoveAll(remoteRepoPath)

		_, err := git.PlainInit(remoteRepoPath, true) // true = bare repository
		require.NoError(t, err)

		service := NewProductMutationService(repoPath, "file://"+remoteRepoPath)
		ctx := context.Background()

		// Create and commit a product first
		createInput := CreateProductInput{
			SKU:        "TEST-001",
			Title:      "Test Product",
			Price:      99.99,
			CategoryID: "electronics",
		}
		_, err = service.CreateProduct(ctx, createInput)
		require.NoError(t, err)

		// Publish catalog (no pending changes)
		input := PublishCatalogInput{}

		payload, err := service.PublishCatalog(ctx, input)
		require.NoError(t, err)
		require.NotNil(t, payload)
		assert.True(t, payload.Success)
		assert.NotEmpty(t, payload.CommitHash)
		assert.NotEmpty(t, payload.TagName)

		// Verify tag was created
		repo, err := git.PlainOpen(repoPath)
		require.NoError(t, err)

		tags, err := repo.Tags()
		require.NoError(t, err)

		foundTag := false
		err = tags.ForEach(func(ref *plumbing.Reference) error {
			if ref.Name().Short() == payload.TagName {
				foundTag = true
			}
			return nil
		})
		require.NoError(t, err)
		assert.True(t, foundTag, "Tag should exist: "+payload.TagName)
	})

	t.Run("should use custom tag name", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		// Set up bare remote repository
		remoteRepoPath := filepath.Join(os.TempDir(), "gitstore-test-remote-"+t.Name())
		defer os.RemoveAll(remoteRepoPath)

		_, err := git.PlainInit(remoteRepoPath, true)
		require.NoError(t, err)

		service := NewProductMutationService(repoPath, "file://"+remoteRepoPath)
		ctx := context.Background()

		// Create a product first
		createInput := CreateProductInput{
			SKU:        "TEST-002",
			Title:      "Test Product 2",
			Price:      199.99,
			CategoryID: "electronics",
		}
		_, err = service.CreateProduct(ctx, createInput)
		require.NoError(t, err)

		// Publish with custom tag
		customTag := "v1.0.0-beta"
		input := PublishCatalogInput{
			TagName: &customTag,
		}

		payload, err := service.PublishCatalog(ctx, input)
		require.NoError(t, err)
		require.NotNil(t, payload)
		assert.Equal(t, "v1.0.0-beta", payload.TagName)
	})

	t.Run("should use custom message", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		// Set up bare remote repository
		remoteRepoPath := filepath.Join(os.TempDir(), "gitstore-test-remote-"+t.Name())
		defer os.RemoveAll(remoteRepoPath)

		_, err := git.PlainInit(remoteRepoPath, true)
		require.NoError(t, err)

		service := NewProductMutationService(repoPath, "file://"+remoteRepoPath)
		ctx := context.Background()

		// Create uncommitted changes
		productPath := filepath.Join(repoPath, "products/electronics/test-product-3.md")
		err = os.MkdirAll(filepath.Dir(productPath), 0755)
		require.NoError(t, err)
		err = os.WriteFile(productPath, []byte("# Test Product 3\n"), 0644)
		require.NoError(t, err)

		// Publish with custom message
		customMessage := "Release: Holiday catalog 2026"
		input := PublishCatalogInput{
			Message: &customMessage,
		}

		payload, err := service.PublishCatalog(ctx, input)
		require.NoError(t, err)
		require.NotNil(t, payload)

		// Verify commit message
		repo, err := git.PlainOpen(repoPath)
		require.NoError(t, err)

		ref, err := repo.Head()
		require.NoError(t, err)

		commit, err := repo.CommitObject(ref.Hash())
		require.NoError(t, err)
		assert.Contains(t, commit.Message, "Holiday catalog")
	})

	t.Run("should auto-generate semver tag", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		// Set up bare remote repository
		remoteRepoPath := filepath.Join(os.TempDir(), "gitstore-test-remote-"+t.Name())
		defer os.RemoveAll(remoteRepoPath)

		_, err := git.PlainInit(remoteRepoPath, true)
		require.NoError(t, err)

		service := NewProductMutationService(repoPath, "file://"+remoteRepoPath)
		ctx := context.Background()

		// Create a product first
		createInput := CreateProductInput{
			SKU:        "TEST-003",
			Title:      "Test Product 3",
			Price:      299.99,
			CategoryID: "electronics",
		}
		_, err = service.CreateProduct(ctx, createInput)
		require.NoError(t, err)

		// Publish - should auto-generate v0.1.0
		input := PublishCatalogInput{}

		payload, err := service.PublishCatalog(ctx, input)
		require.NoError(t, err)
		require.NotNil(t, payload)
		// GenerateSemverTagName starts with v1.0.0 when no tags exist
		assert.Equal(t, "v1.0.0", payload.TagName)
	})

	t.Run("should return clientMutationId", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		// Set up bare remote repository
		remoteRepoPath := filepath.Join(os.TempDir(), "gitstore-test-remote-"+t.Name())
		defer os.RemoveAll(remoteRepoPath)

		_, err := git.PlainInit(remoteRepoPath, true)
		require.NoError(t, err)

		service := NewProductMutationService(repoPath, "file://"+remoteRepoPath)
		ctx := context.Background()

		// Create a product first
		createInput := CreateProductInput{
			SKU:        "TEST-004",
			Title:      "Test Product 4",
			Price:      399.99,
			CategoryID: "electronics",
		}
		_, err = service.CreateProduct(ctx, createInput)
		require.NoError(t, err)

		clientID := "publish-123"
		input := PublishCatalogInput{
			ClientMutationID: &clientID,
		}

		payload, err := service.PublishCatalog(ctx, input)
		require.NoError(t, err)
		require.NotNil(t, payload.ClientMutationID)
		assert.Equal(t, "publish-123", *payload.ClientMutationID)
	})
}
