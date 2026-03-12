package models

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// CategoryMutation represents a category for mutation operations
type CategoryMutation struct {
	ID           string
	Name         string
	Slug         string
	ParentID     *string
	DisplayOrder int
	Body         string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// GenerateCategoryID generates a unique category ID in format: cat_[base62]
func GenerateCategoryID() (string, error) {
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

	return "cat_" + encoded, nil
}

// NewCategory creates a new category with generated ID and timestamps
func NewCategory(name, slug string, parentID *string, displayOrder int) (*CategoryMutation, error) {
	id, err := GenerateCategoryID()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	return &CategoryMutation{
		ID:           id,
		Name:         name,
		Slug:         slug,
		ParentID:     parentID,
		DisplayOrder: displayOrder,
		Body:         "",
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// ValidateCategoryName checks if category name is valid
func ValidateCategoryName(name string) error {
	if name == "" {
		return fmt.Errorf("category name is required")
	}
	if len(name) < 1 {
		return fmt.Errorf("category name cannot be empty")
	}
	if len(name) > 100 {
		return fmt.Errorf("category name must be at most 100 characters")
	}
	return nil
}

// ValidateSlug checks if slug is valid
func ValidateSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("slug is required")
	}
	if len(slug) < 1 {
		return fmt.Errorf("slug cannot be empty")
	}
	if len(slug) > 100 {
		return fmt.Errorf("slug must be at most 100 characters")
	}

	// Slug should be lowercase alphanumeric with hyphens
	slugPattern := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	if !slugPattern.MatchString(slug) {
		return fmt.Errorf("slug must be lowercase alphanumeric with hyphens (e.g., 'electronics' or 'home-garden')")
	}

	return nil
}

// ValidateDisplayOrder checks if display order is valid
func ValidateDisplayOrder(order int) error {
	if order < 0 {
		return fmt.Errorf("display order cannot be negative")
	}
	if order > 10000 {
		return fmt.Errorf("display order is too large (max 10000)")
	}
	return nil
}

// Validate performs comprehensive validation on the category
func (c *CategoryMutation) Validate() error {
	if err := ValidateCategoryName(c.Name); err != nil {
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

// GenerateSlug creates a URL-friendly slug from a category name
func GenerateSlug(name string) string {
	// Convert to lowercase
	slug := strings.ToLower(name)

	// Replace spaces and underscores with hyphens
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = strings.ReplaceAll(slug, "_", "-")

	// Remove special characters (keep only alphanumeric and hyphens)
	reg := regexp.MustCompile(`[^a-z0-9-]+`)
	slug = reg.ReplaceAllString(slug, "")

	// Remove multiple consecutive hyphens
	reg = regexp.MustCompile(`-+`)
	slug = reg.ReplaceAllString(slug, "-")

	// Trim hyphens from start and end
	slug = strings.Trim(slug, "-")

	return slug
}
