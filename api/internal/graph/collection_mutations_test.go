package graph

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/commerce-projects/gitstore/api/internal/models"
	"github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateCollection(t *testing.T) {
	t.Run("should create collection with required fields", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		input := CreateCollectionInput{
			Name: "Summer Sale",
			Slug: "summer-sale",
		}

		payload, err := service.CreateCollection(ctx, input)
		require.NoError(t, err)
		require.NotNil(t, payload)
		require.NotNil(t, payload.Collection)

		collection := payload.Collection
		assert.Equal(t, "Summer Sale", collection.Name)
		assert.Equal(t, "summer-sale", collection.Slug)
		assert.Equal(t, 0, collection.DisplayOrder)
		assert.NotEmpty(t, collection.ID)
		assert.True(t, len(collection.ID) > 4) // Should have prefix + base62
		assert.NotZero(t, collection.CreatedAt)
		assert.NotZero(t, collection.UpdatedAt)
	})

	t.Run("should create collection with all fields", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		displayOrder := 5
		body := "Best products from our summer collection. Limited time offer!"
		clientMutationID := "test-col-123"

		input := CreateCollectionInput{
			ClientMutationID: &clientMutationID,
			Name:             "Summer Sale 2026",
			Slug:             "summer-sale-2026",
			DisplayOrder:     &displayOrder,
			Body:             &body,
		}

		payload, err := service.CreateCollection(ctx, input)
		require.NoError(t, err)
		require.NotNil(t, payload.ClientMutationID)
		assert.Equal(t, "test-col-123", *payload.ClientMutationID)

		collection := payload.Collection
		assert.Equal(t, "Summer Sale 2026", collection.Name)
		assert.Equal(t, "summer-sale-2026", collection.Slug)
		assert.Equal(t, 5, collection.DisplayOrder)
		assert.Contains(t, collection.Body, "Best products")
	})

	t.Run("should create markdown file in repository", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		input := CreateCollectionInput{
			Name: "New Arrivals",
			Slug: "new-arrivals",
		}

		_, err := service.CreateCollection(ctx, input)
		require.NoError(t, err)

		// Verify file was created
		filePath := filepath.Join(repoPath, "collections/new-arrivals.md")
		content, err := os.ReadFile(filePath)
		require.NoError(t, err)

		// Check file contains expected content
		contentStr := string(content)
		assert.Contains(t, contentStr, "---")
		assert.Contains(t, contentStr, "slug: new-arrivals")
		assert.Contains(t, contentStr, "name: New Arrivals")
		assert.Contains(t, contentStr, "display_order: 0")
		assert.Contains(t, contentStr, "# New Arrivals")
	})

	t.Run("should commit file to git", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		input := CreateCollectionInput{
			Name: "Best Sellers",
			Slug: "best-sellers",
		}

		_, err := service.CreateCollection(ctx, input)
		require.NoError(t, err)

		// Verify git commit was created
		repo, err := git.PlainOpen(repoPath)
		require.NoError(t, err)

		ref, err := repo.Head()
		require.NoError(t, err)

		commit, err := repo.CommitObject(ref.Hash())
		require.NoError(t, err)

		assert.Contains(t, commit.Message, "create")
		assert.Contains(t, commit.Message, "collection")
		assert.Contains(t, commit.Message, "best-sellers")
	})

	t.Run("should validate required fields", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		tests := []struct {
			name        string
			input       CreateCollectionInput
			expectedErr string
		}{
			{
				name: "missing name",
				input: CreateCollectionInput{
					Name: "",
					Slug: "test",
				},
				expectedErr: "name is required",
			},
			{
				name: "missing slug",
				input: CreateCollectionInput{
					Name: "Test",
					Slug: "",
				},
				expectedErr: "slug is required",
			},
			{
				name: "invalid slug with spaces",
				input: CreateCollectionInput{
					Name: "Test Collection",
					Slug: "test collection",
				},
				expectedErr: "lowercase alphanumeric",
			},
			{
				name: "invalid slug with uppercase",
				input: CreateCollectionInput{
					Name: "Test",
					Slug: "TestCollection",
				},
				expectedErr: "lowercase alphanumeric",
			},
			{
				name: "invalid slug with special chars",
				input: CreateCollectionInput{
					Name: "Test",
					Slug: "test_collection",
				},
				expectedErr: "lowercase alphanumeric",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := service.CreateCollection(ctx, tt.input)
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErr)
			})
		}
	})

	t.Run("should validate display order", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		negativeOrder := -1
		input := CreateCollectionInput{
			Name:         "Test",
			Slug:         "test",
			DisplayOrder: &negativeOrder,
		}

		_, err := service.CreateCollection(ctx, input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "display order cannot be negative")
	})

	t.Run("should accept valid slug formats", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		validSlugs := []string{
			"sale",
			"summer-sale",
			"spring-2026",
			"collection123",
			"col123",
		}

		for i, slug := range validSlugs {
			input := CreateCollectionInput{
				Name: "Collection " + slug,
				Slug: slug,
			}

			payload, err := service.CreateCollection(ctx, input)
			require.NoError(t, err, "Failed for slug: %s", slug)
			assert.Equal(t, slug, payload.Collection.Slug)

			// Verify file exists
			filePath := filepath.Join(repoPath, "collections", slug+".md")
			_, err = os.Stat(filePath)
			require.NoError(t, err, "File not created for slug %d: %s", i, slug)
		}
	})

	t.Run("should use default values", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		input := CreateCollectionInput{
			Name: "Default Collection",
			Slug: "default-collection",
			// DisplayOrder, Body not provided
		}

		payload, err := service.CreateCollection(ctx, input)
		require.NoError(t, err)

		collection := payload.Collection
		assert.Equal(t, 0, collection.DisplayOrder)
		assert.Empty(t, collection.Body)
	})
}

func TestUpdateCollection(t *testing.T) {
	t.Run("should update collection fields", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create initial collection
		createInput := CreateCollectionInput{
			Name: "Winter Sale",
			Slug: "winter-sale",
		}
		createPayload, err := service.CreateCollection(ctx, createInput)
		require.NoError(t, err)
		collectionID := createPayload.Collection.ID

		// Mock readCollectionFromGit to return the created collection
		service.readCollectionFromGit = func(id string) (*models.CollectionMutation, string, error) {
			if id == collectionID {
				content := service.generateCollectionContent(createPayload.Collection)
				return createPayload.Collection, content, nil
			}
			return nil, "", fmt.Errorf("collection not found")
		}

		// Update the collection
		newName := "Winter Sale 2026"
		newSlug := "winter-sale-2026"
		newDisplayOrder := 10
		newBody := "Best winter deals on all products."

		versionChecker := NewVersionChecker()
		originalContent := service.generateCollectionContent(createPayload.Collection)
		version := versionChecker.CalculateVersion(originalContent)

		updateInput := UpdateCollectionInput{
			ID:           collectionID,
			Name:         &newName,
			Slug:         &newSlug,
			DisplayOrder: &newDisplayOrder,
			Body:         &newBody,
			Version:      version,
		}

		payload, err := service.UpdateCollection(ctx, updateInput)
		require.NoError(t, err)
		require.NotNil(t, payload)
		require.Nil(t, payload.Conflict)

		collection := payload.Collection
		assert.Equal(t, "Winter Sale 2026", collection.Name)
		assert.Equal(t, "winter-sale-2026", collection.Slug)
		assert.Equal(t, 10, collection.DisplayOrder)
		assert.Contains(t, collection.Body, "Best winter deals")
	})

	t.Run("should detect optimistic lock conflict", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create initial collection
		createInput := CreateCollectionInput{
			Name: "Flash Sale",
			Slug: "flash-sale",
		}
		createPayload, err := service.CreateCollection(ctx, createInput)
		require.NoError(t, err)
		collectionID := createPayload.Collection.ID

		// Simulate concurrent modification
		modifiedCollection := *createPayload.Collection
		modifiedCollection.Name = "Lightning Sale"
		modifiedCollection.UpdatedAt = time.Now().UTC()

		service.readCollectionFromGit = func(id string) (*models.CollectionMutation, string, error) {
			if id == collectionID {
				content := service.generateCollectionContent(&modifiedCollection)
				return &modifiedCollection, content, nil
			}
			return nil, "", fmt.Errorf("collection not found")
		}

		// Try to update with old version
		versionChecker := NewVersionChecker()
		oldContent := service.generateCollectionContent(createPayload.Collection)
		oldVersion := versionChecker.CalculateVersion(oldContent)

		newName := "Quick Sale"
		updateInput := UpdateCollectionInput{
			ID:      collectionID,
			Name:    &newName,
			Version: oldVersion,
		}

		payload, err := service.UpdateCollection(ctx, updateInput)
		require.NoError(t, err)
		require.NotNil(t, payload)
		require.NotNil(t, payload.Conflict)
		require.Nil(t, payload.Collection)

		assert.True(t, payload.Conflict.Detected)
		assert.NotEmpty(t, payload.Conflict.Diff)
	})

	t.Run("should handle slug change with file move", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create initial collection
		createInput := CreateCollectionInput{
			Name: "Black Friday",
			Slug: "black-friday",
		}
		createPayload, err := service.CreateCollection(ctx, createInput)
		require.NoError(t, err)
		collectionID := createPayload.Collection.ID

		service.readCollectionFromGit = func(id string) (*models.CollectionMutation, string, error) {
			if id == collectionID {
				content := service.generateCollectionContent(createPayload.Collection)
				return createPayload.Collection, content, nil
			}
			return nil, "", fmt.Errorf("collection not found")
		}

		// Change the slug
		newSlug := "black-friday-2026"
		versionChecker := NewVersionChecker()
		originalContent := service.generateCollectionContent(createPayload.Collection)
		version := versionChecker.CalculateVersion(originalContent)

		updateInput := UpdateCollectionInput{
			ID:      collectionID,
			Slug:    &newSlug,
			Version: version,
		}

		payload, err := service.UpdateCollection(ctx, updateInput)
		require.NoError(t, err)
		require.NotNil(t, payload)

		// Verify old file doesn't exist
		oldFilePath := filepath.Join(repoPath, "collections/black-friday.md")
		_, err = os.Stat(oldFilePath)
		assert.True(t, os.IsNotExist(err))

		// Verify new file exists
		newFilePath := filepath.Join(repoPath, "collections/black-friday-2026.md")
		_, err = os.Stat(newFilePath)
		require.NoError(t, err)
	})

	t.Run("should validate updated fields", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create initial collection
		createInput := CreateCollectionInput{
			Name: "Clearance",
			Slug: "clearance",
		}
		createPayload, err := service.CreateCollection(ctx, createInput)
		require.NoError(t, err)
		collectionID := createPayload.Collection.ID

		service.readCollectionFromGit = func(id string) (*models.CollectionMutation, string, error) {
			if id == collectionID {
				content := service.generateCollectionContent(createPayload.Collection)
				return createPayload.Collection, content, nil
			}
			return nil, "", fmt.Errorf("collection not found")
		}

		versionChecker := NewVersionChecker()
		originalContent := service.generateCollectionContent(createPayload.Collection)
		version := versionChecker.CalculateVersion(originalContent)

		// Try invalid slug
		invalidSlug := "Clearance Sale!"
		updateInput := UpdateCollectionInput{
			ID:      collectionID,
			Slug:    &invalidSlug,
			Version: version,
		}

		_, err = service.UpdateCollection(ctx, updateInput)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "lowercase alphanumeric")
	})
}

func TestDeleteCollection(t *testing.T) {
	t.Run("should delete collection", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create collection
		createInput := CreateCollectionInput{
			Name: "Seasonal",
			Slug: "seasonal",
		}
		createPayload, err := service.CreateCollection(ctx, createInput)
		require.NoError(t, err)
		collectionID := createPayload.Collection.ID

		service.readCollectionFromGit = func(id string) (*models.CollectionMutation, string, error) {
			if id == collectionID {
				content := service.generateCollectionContent(createPayload.Collection)
				return createPayload.Collection, content, nil
			}
			return nil, "", fmt.Errorf("collection not found")
		}

		// Delete the collection
		deleteInput := DeleteCollectionInput{
			ID: collectionID,
		}

		payload, err := service.DeleteCollection(ctx, deleteInput)
		require.NoError(t, err)
		require.NotNil(t, payload)
		require.NotNil(t, payload.DeletedCollectionID)
		assert.Equal(t, collectionID, *payload.DeletedCollectionID)

		// Verify file was deleted
		filePath := filepath.Join(repoPath, "collections/seasonal.md")
		_, err = os.Stat(filePath)
		assert.True(t, os.IsNotExist(err))
	})

	t.Run("should commit deletion to git", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create collection
		createInput := CreateCollectionInput{
			Name: "Limited Edition",
			Slug: "limited-edition",
		}
		createPayload, err := service.CreateCollection(ctx, createInput)
		require.NoError(t, err)
		collectionID := createPayload.Collection.ID

		service.readCollectionFromGit = func(id string) (*models.CollectionMutation, string, error) {
			if id == collectionID {
				content := service.generateCollectionContent(createPayload.Collection)
				return createPayload.Collection, content, nil
			}
			return nil, "", fmt.Errorf("collection not found")
		}

		// Delete
		deleteInput := DeleteCollectionInput{
			ID: collectionID,
		}

		_, err = service.DeleteCollection(ctx, deleteInput)
		require.NoError(t, err)

		// Verify git commit
		repo, err := git.PlainOpen(repoPath)
		require.NoError(t, err)

		ref, err := repo.Head()
		require.NoError(t, err)

		commit, err := repo.CommitObject(ref.Hash())
		require.NoError(t, err)

		assert.Contains(t, commit.Message, "delete")
		assert.Contains(t, commit.Message, "collection")
		assert.Contains(t, commit.Message, "limited-edition")
	})

	t.Run("should return clientMutationId", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create collection
		createInput := CreateCollectionInput{
			Name: "Featured",
			Slug: "featured",
		}
		createPayload, err := service.CreateCollection(ctx, createInput)
		require.NoError(t, err)
		collectionID := createPayload.Collection.ID

		service.readCollectionFromGit = func(id string) (*models.CollectionMutation, string, error) {
			if id == collectionID {
				content := service.generateCollectionContent(createPayload.Collection)
				return createPayload.Collection, content, nil
			}
			return nil, "", fmt.Errorf("collection not found")
		}

		clientID := "delete-collection-123"
		deleteInput := DeleteCollectionInput{
			ClientMutationID: &clientID,
			ID:               collectionID,
		}

		payload, err := service.DeleteCollection(ctx, deleteInput)
		require.NoError(t, err)
		require.NotNil(t, payload.ClientMutationID)
		assert.Equal(t, "delete-collection-123", *payload.ClientMutationID)
	})
}

func TestReorderCollections(t *testing.T) {
	t.Run("should reorder multiple collections", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create three collections
		collections := []struct {
			name string
			slug string
		}{
			{"Summer Sale", "summer-sale"},
			{"Winter Sale", "winter-sale"},
			{"Spring Sale", "spring-sale"},
		}

		createdCollections := make([]*models.CollectionMutation, 0, len(collections))
		for _, col := range collections {
			createInput := CreateCollectionInput{
				Name: col.name,
				Slug: col.slug,
			}
			payload, err := service.CreateCollection(ctx, createInput)
			require.NoError(t, err)
			createdCollections = append(createdCollections, payload.Collection)
		}

		// Mock readCollectionFromGit
		service.readCollectionFromGit = func(id string) (*models.CollectionMutation, string, error) {
			for _, col := range createdCollections {
				if col.ID == id {
					content := service.generateCollectionContent(col)
					return col, content, nil
				}
			}
			return nil, "", fmt.Errorf("collection not found")
		}

		// Reorder: Winter=0, Spring=1, Summer=2
		reorderInput := ReorderCollectionsInput{
			Orders: []CollectionOrderInput{
				{ID: createdCollections[1].ID, DisplayOrder: 0}, // Winter
				{ID: createdCollections[2].ID, DisplayOrder: 1}, // Spring
				{ID: createdCollections[0].ID, DisplayOrder: 2}, // Summer
			},
		}

		payload, err := service.ReorderCollections(ctx, reorderInput)
		require.NoError(t, err)
		require.NotNil(t, payload)
		require.Len(t, payload.Collections, 3)

		// Verify display orders
		assert.Equal(t, 0, payload.Collections[0].DisplayOrder)
		assert.Equal(t, 1, payload.Collections[1].DisplayOrder)
		assert.Equal(t, 2, payload.Collections[2].DisplayOrder)
	})

	t.Run("should update markdown files", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create two collections
		col1Input := CreateCollectionInput{Name: "Bestsellers", Slug: "bestsellers"}
		col1Payload, err := service.CreateCollection(ctx, col1Input)
		require.NoError(t, err)

		col2Input := CreateCollectionInput{Name: "New Arrivals", Slug: "new-arrivals"}
		col2Payload, err := service.CreateCollection(ctx, col2Input)
		require.NoError(t, err)

		service.readCollectionFromGit = func(id string) (*models.CollectionMutation, string, error) {
			if id == col1Payload.Collection.ID {
				content := service.generateCollectionContent(col1Payload.Collection)
				return col1Payload.Collection, content, nil
			}
			if id == col2Payload.Collection.ID {
				content := service.generateCollectionContent(col2Payload.Collection)
				return col2Payload.Collection, content, nil
			}
			return nil, "", fmt.Errorf("collection not found")
		}

		// Reorder
		reorderInput := ReorderCollectionsInput{
			Orders: []CollectionOrderInput{
				{ID: col1Payload.Collection.ID, DisplayOrder: 5},
				{ID: col2Payload.Collection.ID, DisplayOrder: 10},
			},
		}

		_, err = service.ReorderCollections(ctx, reorderInput)
		require.NoError(t, err)

		// Verify files were updated
		col1Path := filepath.Join(repoPath, "collections/bestsellers.md")
		col1Content, err := os.ReadFile(col1Path)
		require.NoError(t, err)
		assert.Contains(t, string(col1Content), "display_order: 5")

		col2Path := filepath.Join(repoPath, "collections/new-arrivals.md")
		col2Content, err := os.ReadFile(col2Path)
		require.NoError(t, err)
		assert.Contains(t, string(col2Content), "display_order: 10")
	})

	t.Run("should commit single transaction", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create collections
		col1Input := CreateCollectionInput{Name: "Featured", Slug: "featured"}
		col1Payload, err := service.CreateCollection(ctx, col1Input)
		require.NoError(t, err)

		col2Input := CreateCollectionInput{Name: "Trending", Slug: "trending"}
		col2Payload, err := service.CreateCollection(ctx, col2Input)
		require.NoError(t, err)

		// Get initial commit count
		repo, err := git.PlainOpen(repoPath)
		require.NoError(t, err)

		ref, err := repo.Head()
		require.NoError(t, err)

		initialCommit, err := repo.CommitObject(ref.Hash())
		require.NoError(t, err)

		service.readCollectionFromGit = func(id string) (*models.CollectionMutation, string, error) {
			if id == col1Payload.Collection.ID {
				content := service.generateCollectionContent(col1Payload.Collection)
				return col1Payload.Collection, content, nil
			}
			if id == col2Payload.Collection.ID {
				content := service.generateCollectionContent(col2Payload.Collection)
				return col2Payload.Collection, content, nil
			}
			return nil, "", fmt.Errorf("collection not found")
		}

		// Reorder
		reorderInput := ReorderCollectionsInput{
			Orders: []CollectionOrderInput{
				{ID: col1Payload.Collection.ID, DisplayOrder: 1},
				{ID: col2Payload.Collection.ID, DisplayOrder: 2},
			},
		}

		_, err = service.ReorderCollections(ctx, reorderInput)
		require.NoError(t, err)

		// Verify only one new commit was created
		ref, err = repo.Head()
		require.NoError(t, err)

		newCommit, err := repo.CommitObject(ref.Hash())
		require.NoError(t, err)

		assert.NotEqual(t, initialCommit.Hash, newCommit.Hash)
		assert.Contains(t, newCommit.Message, "reorder")
		assert.Contains(t, newCommit.Message, "collections")
		assert.Contains(t, newCommit.Message, "2 collections")
	})

	t.Run("should validate display orders", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create collection
		createInput := CreateCollectionInput{Name: "Clearance", Slug: "clearance"}
		createPayload, err := service.CreateCollection(ctx, createInput)
		require.NoError(t, err)

		service.readCollectionFromGit = func(id string) (*models.CollectionMutation, string, error) {
			if id == createPayload.Collection.ID {
				content := service.generateCollectionContent(createPayload.Collection)
				return createPayload.Collection, content, nil
			}
			return nil, "", fmt.Errorf("collection not found")
		}

		// Try invalid display order
		reorderInput := ReorderCollectionsInput{
			Orders: []CollectionOrderInput{
				{ID: createPayload.Collection.ID, DisplayOrder: -1},
			},
		}

		_, err = service.ReorderCollections(ctx, reorderInput)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "display order cannot be negative")
	})

	t.Run("should require at least one collection", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Try to reorder with no collections
		reorderInput := ReorderCollectionsInput{
			Orders: []CollectionOrderInput{},
		}

		_, err := service.ReorderCollections(ctx, reorderInput)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least one collection")
	})

	t.Run("should return clientMutationId", func(t *testing.T) {
		repoPath, cleanup := setupTestMutationRepo(t)
		defer cleanup()

		service := NewProductMutationService(repoPath, "")
		ctx := context.Background()

		// Create collection
		createInput := CreateCollectionInput{Name: "Limited", Slug: "limited"}
		createPayload, err := service.CreateCollection(ctx, createInput)
		require.NoError(t, err)

		service.readCollectionFromGit = func(id string) (*models.CollectionMutation, string, error) {
			if id == createPayload.Collection.ID {
				content := service.generateCollectionContent(createPayload.Collection)
				return createPayload.Collection, content, nil
			}
			return nil, "", fmt.Errorf("collection not found")
		}

		clientID := "reorder-123"
		// Change display order to create actual changes
		reorderInput := ReorderCollectionsInput{
			ClientMutationID: &clientID,
			Orders: []CollectionOrderInput{
				{ID: createPayload.Collection.ID, DisplayOrder: 5},
			},
		}

		payload, err := service.ReorderCollections(ctx, reorderInput)
		require.NoError(t, err)
		require.NotNil(t, payload.ClientMutationID)
		assert.Equal(t, "reorder-123", *payload.ClientMutationID)
	})
}
