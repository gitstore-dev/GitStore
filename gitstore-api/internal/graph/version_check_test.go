package graph

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalculateVersion(t *testing.T) {
	t.Run("should generate consistent hash for same content", func(t *testing.T) {
		vc := NewVersionChecker()
		content := "test content"

		v1 := vc.CalculateVersion(content)
		v2 := vc.CalculateVersion(content)

		assert.Equal(t, v1, v2)
		assert.NotEmpty(t, v1)
	})

	t.Run("should generate different hash for different content", func(t *testing.T) {
		vc := NewVersionChecker()

		v1 := vc.CalculateVersion("content 1")
		v2 := vc.CalculateVersion("content 2")

		assert.NotEqual(t, v1, v2)
	})

	t.Run("should generate 64-character hex string", func(t *testing.T) {
		vc := NewVersionChecker()
		version := vc.CalculateVersion("test")

		assert.Len(t, version, 64) // SHA-256 produces 64 hex characters
		assert.Regexp(t, "^[0-9a-f]+$", version)
	})
}

func TestCalculateVersionShort(t *testing.T) {
	t.Run("should return first 12 characters", func(t *testing.T) {
		vc := NewVersionChecker()
		version := vc.CalculateVersionShort("test content")

		assert.Len(t, version, 12)
		assert.Regexp(t, "^[0-9a-f]+$", version)
	})

	t.Run("should be consistent prefix of full version", func(t *testing.T) {
		vc := NewVersionChecker()
		content := "test content"

		fullVersion := vc.CalculateVersion(content)
		shortVersion := vc.CalculateVersionShort(content)

		assert.True(t, strings.HasPrefix(fullVersion, shortVersion))
	})
}

func TestCheckVersion(t *testing.T) {
	vc := NewVersionChecker()

	t.Run("should pass when versions match", func(t *testing.T) {
		content := "test content"
		expectedVersion := vc.CalculateVersion(content)

		err := vc.CheckVersion(expectedVersion, content, "product", "PROD-001")
		assert.NoError(t, err)
	})

	t.Run("should fail when versions don't match", func(t *testing.T) {
		oldContent := "old content"
		newContent := "new content"
		expectedVersion := vc.CalculateVersion(oldContent)

		err := vc.CheckVersion(expectedVersion, newContent, "product", "PROD-001")
		require.Error(t, err)

		vme, ok := err.(*VersionMismatchError)
		require.True(t, ok)
		assert.Equal(t, "product", vme.EntityType)
		assert.Equal(t, "PROD-001", vme.EntityID)
		assert.Len(t, vme.ExpectedVersion, 12) // Should show shortened version
		assert.Len(t, vme.ActualVersion, 12)
	})

	t.Run("should pass when expected version is empty", func(t *testing.T) {
		content := "new content"
		err := vc.CheckVersion("", content, "product", "PROD-001")
		assert.NoError(t, err)
	})

	t.Run("should include entity details in error message", func(t *testing.T) {
		oldContent := "old content"
		newContent := "new content"
		expectedVersion := vc.CalculateVersion(oldContent)

		err := vc.CheckVersion(expectedVersion, newContent, "category", "CAT-001")
		require.Error(t, err)

		errMsg := err.Error()
		assert.Contains(t, errMsg, "category")
		assert.Contains(t, errMsg, "CAT-001")
		assert.Contains(t, errMsg, "version mismatch")
	})
}

func TestCheckVersionShort(t *testing.T) {
	vc := NewVersionChecker()

	t.Run("should work with shortened versions", func(t *testing.T) {
		content := "test content"
		expectedVersion := vc.CalculateVersionShort(content)

		err := vc.CheckVersionShort(expectedVersion, content, "product", "PROD-001")
		assert.NoError(t, err)
	})

	t.Run("should detect mismatch with short versions", func(t *testing.T) {
		oldContent := "old content"
		newContent := "new content"
		expectedVersion := vc.CalculateVersionShort(oldContent)

		err := vc.CheckVersionShort(expectedVersion, newContent, "product", "PROD-001")
		require.Error(t, err)

		vme, ok := err.(*VersionMismatchError)
		require.True(t, ok)
		assert.Len(t, vme.ExpectedVersion, 12)
		assert.Len(t, vme.ActualVersion, 12)
	})
}

func TestCompareVersions(t *testing.T) {
	vc := NewVersionChecker()

	t.Run("should return 0 for equal versions", func(t *testing.T) {
		v1 := vc.CalculateVersion("content")
		v2 := vc.CalculateVersion("content")

		result := vc.CompareVersions(v1, v2)
		assert.Equal(t, 0, result)
	})

	t.Run("should return -1 or 1 for different versions", func(t *testing.T) {
		v1 := vc.CalculateVersion("aaa")
		v2 := vc.CalculateVersion("zzz")

		result := vc.CompareVersions(v1, v2)
		assert.NotEqual(t, 0, result)
		assert.True(t, result == -1 || result == 1)
	})
}

func TestIsVersionMismatchError(t *testing.T) {
	t.Run("should return true for version mismatch error", func(t *testing.T) {
		err := &VersionMismatchError{
			EntityType: "product",
			EntityID:   "PROD-001",
		}

		assert.True(t, IsVersionMismatchError(err))
	})

	t.Run("should return false for other errors", func(t *testing.T) {
		err := assert.AnError

		assert.False(t, IsVersionMismatchError(err))
	})
}

func TestExtractVersionMismatchError(t *testing.T) {
	t.Run("should extract version mismatch error", func(t *testing.T) {
		original := &VersionMismatchError{
			EntityType:      "product",
			EntityID:        "PROD-001",
			ExpectedVersion: "abc123",
			ActualVersion:   "def456",
		}

		extracted, ok := ExtractVersionMismatchError(original)
		require.True(t, ok)
		assert.Equal(t, original.EntityType, extracted.EntityType)
		assert.Equal(t, original.EntityID, extracted.EntityID)
		assert.Equal(t, original.ExpectedVersion, extracted.ExpectedVersion)
		assert.Equal(t, original.ActualVersion, extracted.ActualVersion)
	})

	t.Run("should return false for other errors", func(t *testing.T) {
		err := assert.AnError

		_, ok := ExtractVersionMismatchError(err)
		assert.False(t, ok)
	})
}

func TestGetConflictDetails(t *testing.T) {
	t.Run("should extract conflict details from version mismatch", func(t *testing.T) {
		err := &VersionMismatchError{
			EntityType:      "product",
			EntityID:        "PROD-001",
			ExpectedVersion: "abc123",
			ActualVersion:   "def456",
		}

		details := GetConflictDetails(err)
		require.NotNil(t, details)
		assert.True(t, details.HasConflict)
		assert.Equal(t, "product", details.EntityType)
		assert.Equal(t, "PROD-001", details.EntityID)
		assert.Equal(t, "abc123", details.ExpectedVersion)
		assert.Equal(t, "def456", details.ActualVersion)
		assert.NotEmpty(t, details.Message)
	})

	t.Run("should return no conflict for other errors", func(t *testing.T) {
		err := assert.AnError

		details := GetConflictDetails(err)
		require.NotNil(t, details)
		assert.False(t, details.HasConflict)
	})
}

func TestVersionedEntity(t *testing.T) {
	t.Run("should create versioned entity with calculated version", func(t *testing.T) {
		entity := NewVersionedEntity("PROD-001", "test content")

		assert.Equal(t, "PROD-001", entity.ID)
		assert.Equal(t, "test content", entity.Content)
		assert.NotEmpty(t, entity.Version)
		assert.Len(t, entity.Version, 64)
	})

	t.Run("should get shortened version", func(t *testing.T) {
		entity := NewVersionedEntity("PROD-001", "test content")

		shortVersion := entity.GetVersionShort()
		assert.Len(t, shortVersion, 12)
		assert.True(t, strings.HasPrefix(entity.Version, shortVersion))
	})

	t.Run("should update content and recalculate version", func(t *testing.T) {
		entity := NewVersionedEntity("PROD-001", "original content")
		originalVersion := entity.Version

		entity.UpdateContent("updated content")

		assert.Equal(t, "updated content", entity.Content)
		assert.NotEqual(t, originalVersion, entity.Version)
		assert.Len(t, entity.Version, 64)
	})

	t.Run("should validate update with matching version", func(t *testing.T) {
		entity := NewVersionedEntity("PROD-001", "test content")
		expectedVersion := entity.Version

		err := entity.ValidateUpdate(expectedVersion, "product")
		assert.NoError(t, err)
	})

	t.Run("should reject update with mismatched version", func(t *testing.T) {
		entity := NewVersionedEntity("PROD-001", "test content")
		wrongVersion := "incorrect-version-hash"

		err := entity.ValidateUpdate(wrongVersion, "product")
		require.Error(t, err)
		assert.True(t, IsVersionMismatchError(err))
	})

	t.Run("should allow update with empty version", func(t *testing.T) {
		entity := NewVersionedEntity("PROD-001", "test content")

		err := entity.ValidateUpdate("", "product")
		assert.NoError(t, err)
	})
}

func TestVersionMismatchErrorMessage(t *testing.T) {
	t.Run("should format default error message", func(t *testing.T) {
		err := &VersionMismatchError{
			EntityType:      "product",
			EntityID:        "PROD-001",
			ExpectedVersion: "abc123",
			ActualVersion:   "def456",
		}

		msg := err.Error()
		assert.Contains(t, msg, "product")
		assert.Contains(t, msg, "PROD-001")
		assert.Contains(t, msg, "abc123")
		assert.Contains(t, msg, "def456")
		assert.Contains(t, msg, "version mismatch")
	})

	t.Run("should use custom message if provided", func(t *testing.T) {
		err := &VersionMismatchError{
			EntityType:      "product",
			EntityID:        "PROD-001",
			ExpectedVersion: "abc123",
			ActualVersion:   "def456",
			Message:         "Custom conflict message",
		}

		msg := err.Error()
		assert.Equal(t, "Custom conflict message", msg)
	})
}
