package models

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// Product represents a sellable item in the catalog
type Product struct {
	ID                string
	SKU               string
	Title             string
	Body              string
	Price             float64
	Currency          string
	InventoryStatus   string
	InventoryQuantity *int
	CategoryID        string
	CollectionIDs     []string
	Images            []string
	Metadata          map[string]interface{}
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// GenerateProductID generates a unique product ID in format: prod_[base62]
func GenerateProductID() (string, error) {
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

	return "prod_" + encoded, nil
}

// NewProduct creates a new product with generated ID and timestamps
func NewProduct(sku, title, body string, price float64, currency, categoryID string) (*Product, error) {
	id, err := GenerateProductID()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	// Default currency if not specified
	if currency == "" {
		currency = "USD"
	}

	return &Product{
		ID:              id,
		SKU:             sku,
		Title:           title,
		Body:            body,
		Price:           price,
		Currency:        currency,
		InventoryStatus: "IN_STOCK",
		CategoryID:      categoryID,
		CollectionIDs:   []string{},
		Images:          []string{},
		Metadata:        make(map[string]interface{}),
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

// ValidateSKU checks if SKU is valid
func ValidateSKU(sku string) error {
	if sku == "" {
		return fmt.Errorf("SKU is required")
	}
	if len(sku) < 3 {
		return fmt.Errorf("SKU must be at least 3 characters")
	}
	if len(sku) > 100 {
		return fmt.Errorf("SKU must be at most 100 characters")
	}
	// SKU should be alphanumeric with hyphens/underscores
	for _, r := range sku {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_') {
			return fmt.Errorf("SKU must contain only alphanumeric characters, hyphens, and underscores")
		}
	}
	return nil
}

// ValidateTitle checks if title is valid
func ValidateTitle(title string) error {
	if title == "" {
		return fmt.Errorf("title is required")
	}
	if len(title) < 1 {
		return fmt.Errorf("title cannot be empty")
	}
	if len(title) > 200 {
		return fmt.Errorf("title must be at most 200 characters")
	}
	return nil
}

// ValidatePrice checks if price is valid
func ValidatePrice(price float64) error {
	if price < 0 {
		return fmt.Errorf("price cannot be negative")
	}
	if price > 999999.99 {
		return fmt.Errorf("price is too large")
	}
	return nil
}

// ValidateInventoryStatus checks if inventory status is valid
func ValidateInventoryStatus(status string) error {
	validStatuses := map[string]bool{
		"IN_STOCK":     true,
		"OUT_OF_STOCK": true,
		"PREORDER":     true,
		"DISCONTINUED": true,
	}

	if !validStatuses[strings.ToUpper(status)] {
		return fmt.Errorf("invalid inventory status: %s (must be IN_STOCK, OUT_OF_STOCK, PREORDER, or DISCONTINUED)", status)
	}

	return nil
}

// Validate performs comprehensive validation on the product
func (p *Product) Validate() error {
	if err := ValidateSKU(p.SKU); err != nil {
		return err
	}
	if err := ValidateTitle(p.Title); err != nil {
		return err
	}
	if err := ValidatePrice(p.Price); err != nil {
		return err
	}
	if p.Currency == "" {
		return fmt.Errorf("currency is required")
	}
	if err := ValidateInventoryStatus(p.InventoryStatus); err != nil {
		return err
	}
	if p.CategoryID == "" {
		return fmt.Errorf("categoryID is required")
	}
	return nil
}

// GetCategorySlug extracts the category slug from category ID
// Assumes format: cat_[slug]
func GetCategorySlug(categoryID string) string {
	if strings.HasPrefix(categoryID, "cat_") {
		return strings.TrimPrefix(categoryID, "cat_")
	}
	return categoryID
}
