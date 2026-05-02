package models

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// CollectionMutation represents a collection for mutation operations
type CollectionMutation struct {
	ID           string
	Name         string
	Slug         string
	DisplayOrder int
	Body         string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// GenerateCollectionID generates a unique collection ID in format: col_[base62]
func GenerateCollectionID() (string, error) {
	// Generate 12 random bytes (96 bits)
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random ID: %w", err)
	}

	// Encode to base64 URL-safe and remove padding
	encoded := base64.RawURLEncoding.EncodeToString(b)

	// Make it more readable by using only alphanumeric characters
	encoded = strings.Map(func(r rune) rune {
		if r == '-' {
			return 'a'
		}
		if r == '_' {
			return 'b'
		}
		return r
	}, encoded)

	return "col_" + encoded, nil
}

// NewCollection creates a new collection with generated ID and timestamps
func NewCollection(name, slug string, displayOrder int) (*CollectionMutation, error) {
	id, err := GenerateCollectionID()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	return &CollectionMutation{
		ID:           id,
		Name:         name,
		Slug:         slug,
		DisplayOrder: displayOrder,
		Body:         "",
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// ValidateCollectionName checks if collection name is valid
func ValidateCollectionName(name string) error {
	if name == "" {
		return fmt.Errorf("collection name is required")
	}
	if len(name) < 1 {
		return fmt.Errorf("collection name cannot be empty")
	}
	if len(name) > 100 {
		return fmt.Errorf("collection name must be at most 100 characters")
	}
	return nil
}

// Validate performs comprehensive validation on the collection
func (c *CollectionMutation) Validate() error {
	if err := ValidateCollectionName(c.Name); err != nil {
		return err
	}
	if err := ValidateSlug(c.Slug); err != nil {
		return err
	}
	if err := ValidateDisplayOrder(c.DisplayOrder); err != nil {
		return err
	}
	return nil
}
