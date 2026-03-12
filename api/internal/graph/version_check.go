package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// VersionMismatchError represents a concurrent modification conflict
type VersionMismatchError struct {
	EntityType      string
	EntityID        string
	ExpectedVersion string
	ActualVersion   string
	Message         string
}

func (e *VersionMismatchError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf(
		"version mismatch for %s %s: expected %s, but current version is %s",
		e.EntityType,
		e.EntityID,
		e.ExpectedVersion,
		e.ActualVersion,
	)
}

// VersionChecker handles optimistic locking version verification
type VersionChecker struct{}

// NewVersionChecker creates a new version checker
func NewVersionChecker() *VersionChecker {
	return &VersionChecker{}
}

// CalculateVersion computes a version hash from entity content
// This hash represents the state of the entity and changes when content changes
func (vc *VersionChecker) CalculateVersion(content string) string {
	hash := sha256.Sum256([]byte(content))
	return hex.EncodeToString(hash[:])
}

// CalculateVersionShort returns a shortened version hash (first 12 characters)
// Suitable for display in UIs and error messages
func (vc *VersionChecker) CalculateVersionShort(content string) string {
	fullVersion := vc.CalculateVersion(content)
	if len(fullVersion) >= 12 {
		return fullVersion[:12]
	}
	return fullVersion
}

// CheckVersion verifies that the expected version matches the current version
func (vc *VersionChecker) CheckVersion(expectedVersion, currentContent, entityType, entityID string) error {
	if expectedVersion == "" {
		// No version provided - this is a create operation, not an update
		return nil
	}

	currentVersion := vc.CalculateVersion(currentContent)

	if expectedVersion != currentVersion {
		return &VersionMismatchError{
			EntityType:      entityType,
			EntityID:        entityID,
			ExpectedVersion: expectedVersion[:12], // Show shortened version
			ActualVersion:   currentVersion[:12],
		}
	}

	return nil
}

// CheckVersionShort is a convenience method that works with shortened versions
func (vc *VersionChecker) CheckVersionShort(expectedVersion, currentContent, entityType, entityID string) error {
	if expectedVersion == "" {
		return nil
	}

	currentVersionShort := vc.CalculateVersionShort(currentContent)

	if expectedVersion != currentVersionShort {
		return &VersionMismatchError{
			EntityType:      entityType,
			EntityID:        entityID,
			ExpectedVersion: expectedVersion,
			ActualVersion:   currentVersionShort,
		}
	}

	return nil
}

// CompareVersions compares two version hashes
// Returns: -1 if v1 < v2, 0 if equal, 1 if v1 > v2
// Note: This is primarily for ordering, not semantic versioning
func (vc *VersionChecker) CompareVersions(v1, v2 string) int {
	if v1 == v2 {
		return 0
	}
	if v1 < v2 {
		return -1
	}
	return 1
}

// IsVersionMismatchError checks if an error is a version mismatch error
func IsVersionMismatchError(err error) bool {
	_, ok := err.(*VersionMismatchError)
	return ok
}

// ExtractVersionMismatchError extracts version mismatch details from an error
func ExtractVersionMismatchError(err error) (*VersionMismatchError, bool) {
	vme, ok := err.(*VersionMismatchError)
	return vme, ok
}

// ConflictDetails contains information about a version conflict
type ConflictDetails struct {
	EntityType      string
	EntityID        string
	ExpectedVersion string
	ActualVersion   string
	HasConflict     bool
	Message         string
}

// GetConflictDetails extracts conflict information from a version mismatch error
func GetConflictDetails(err error) *ConflictDetails {
	vme, ok := ExtractVersionMismatchError(err)
	if !ok {
		return &ConflictDetails{
			HasConflict: false,
		}
	}

	return &ConflictDetails{
		EntityType:      vme.EntityType,
		EntityID:        vme.EntityID,
		ExpectedVersion: vme.ExpectedVersion,
		ActualVersion:   vme.ActualVersion,
		HasConflict:     true,
		Message:         vme.Error(),
	}
}

// VersionedEntity represents an entity with version tracking
type VersionedEntity struct {
	ID      string
	Content string
	Version string
}

// NewVersionedEntity creates a versioned entity from content
func NewVersionedEntity(id string, content string) *VersionedEntity {
	vc := NewVersionChecker()
	return &VersionedEntity{
		ID:      id,
		Content: content,
		Version: vc.CalculateVersion(content),
	}
}

// GetVersionShort returns the shortened version hash
func (ve *VersionedEntity) GetVersionShort() string {
	if len(ve.Version) >= 12 {
		return ve.Version[:12]
	}
	return ve.Version
}

// UpdateContent updates the entity content and recalculates version
func (ve *VersionedEntity) UpdateContent(newContent string) {
	vc := NewVersionChecker()
	ve.Content = newContent
	ve.Version = vc.CalculateVersion(newContent)
}

// ValidateUpdate checks if an update with the given expected version is valid
func (ve *VersionedEntity) ValidateUpdate(expectedVersion string, entityType string) error {
	vc := NewVersionChecker()
	return vc.CheckVersion(expectedVersion, ve.Content, entityType, ve.ID)
}
