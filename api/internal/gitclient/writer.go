package gitclient

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ProductFrontMatter represents product metadata for YAML front-matter
type ProductFrontMatter struct {
	ID                string                 `yaml:"id"`
	SKU               string                 `yaml:"sku"`
	Title             string                 `yaml:"title"`
	Price             float64                `yaml:"price"`
	Currency          string                 `yaml:"currency"`
	InventoryStatus   string                 `yaml:"inventory_status"`
	InventoryQuantity *int                   `yaml:"inventory_quantity,omitempty"`
	CategoryID        string                 `yaml:"category_id"`
	CollectionIDs     []string               `yaml:"collection_ids,omitempty"`
	Images            []string               `yaml:"images,omitempty"`
	Metadata          map[string]interface{} `yaml:"metadata,omitempty"`
	CreatedAt         string                 `yaml:"created_at"`
	UpdatedAt         string                 `yaml:"updated_at"`
}

// CategoryFrontMatter represents category metadata for YAML front-matter
type CategoryFrontMatter struct {
	ID           string  `yaml:"id"`
	Name         string  `yaml:"name"`
	Slug         string  `yaml:"slug"`
	Description  *string `yaml:"description,omitempty"`
	ParentID     *string `yaml:"parent_id,omitempty"`
	DisplayOrder int     `yaml:"display_order"`
	CreatedAt    string  `yaml:"created_at"`
	UpdatedAt    string  `yaml:"updated_at"`
}

// CollectionFrontMatter represents collection metadata for YAML front-matter
type CollectionFrontMatter struct {
	ID           string   `yaml:"id"`
	Name         string   `yaml:"name"`
	Slug         string   `yaml:"slug"`
	Description  *string  `yaml:"description,omitempty"`
	ProductIDs   []string `yaml:"product_ids"`
	DisplayOrder int      `yaml:"display_order"`
	CreatedAt    string   `yaml:"created_at"`
	UpdatedAt    string   `yaml:"updated_at"`
}

// GenerateProductMarkdown generates a markdown file with YAML front-matter for a product
func GenerateProductMarkdown(frontMatter ProductFrontMatter, body string) (string, error) {
	// Ensure timestamps are set
	if frontMatter.CreatedAt == "" {
		frontMatter.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if frontMatter.UpdatedAt == "" {
		frontMatter.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	// Generate YAML front-matter
	yamlData, err := yaml.Marshal(frontMatter)
	if err != nil {
		return "", fmt.Errorf("failed to marshal product front-matter: %w", err)
	}

	// Build markdown file
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(yamlData)
	buf.WriteString("---\n\n")

	// Add title header if body doesn't already start with it
	if body == "" || !strings.HasPrefix(strings.TrimSpace(body), "#") {
		buf.WriteString(fmt.Sprintf("# %s\n\n", frontMatter.Title))
	}

	// Add body content
	buf.WriteString(body)

	// Ensure file ends with newline
	if !strings.HasSuffix(buf.String(), "\n") {
		buf.WriteString("\n")
	}

	return buf.String(), nil
}

// GenerateCategoryMarkdown generates a markdown file with YAML front-matter for a category
func GenerateCategoryMarkdown(frontMatter CategoryFrontMatter, body string) (string, error) {
	// Ensure timestamps are set
	if frontMatter.CreatedAt == "" {
		frontMatter.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if frontMatter.UpdatedAt == "" {
		frontMatter.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	// Generate YAML front-matter
	yamlData, err := yaml.Marshal(frontMatter)
	if err != nil {
		return "", fmt.Errorf("failed to marshal category front-matter: %w", err)
	}

	// Build markdown file
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(yamlData)
	buf.WriteString("---\n\n")

	// Add title header if body doesn't already start with it
	if body == "" || !strings.HasPrefix(strings.TrimSpace(body), "#") {
		buf.WriteString(fmt.Sprintf("# %s\n\n", frontMatter.Name))
	}

	// Add body content
	if body != "" {
		buf.WriteString(body)
	} else if frontMatter.Description != nil {
		buf.WriteString(*frontMatter.Description)
	}

	// Ensure file ends with newline
	if !strings.HasSuffix(buf.String(), "\n") {
		buf.WriteString("\n")
	}

	return buf.String(), nil
}

// GenerateCollectionMarkdown generates a markdown file with YAML front-matter for a collection
func GenerateCollectionMarkdown(frontMatter CollectionFrontMatter, body string) (string, error) {
	// Ensure timestamps are set
	if frontMatter.CreatedAt == "" {
		frontMatter.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if frontMatter.UpdatedAt == "" {
		frontMatter.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	// Generate YAML front-matter
	yamlData, err := yaml.Marshal(frontMatter)
	if err != nil {
		return "", fmt.Errorf("failed to marshal collection front-matter: %w", err)
	}

	// Build markdown file
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(yamlData)
	buf.WriteString("---\n\n")

	// Add title header if body doesn't already start with it
	if body == "" || !strings.HasPrefix(strings.TrimSpace(body), "#") {
		buf.WriteString(fmt.Sprintf("# %s\n\n", frontMatter.Name))
	}

	// Add body content
	if body != "" {
		buf.WriteString(body)
	} else if frontMatter.Description != nil {
		buf.WriteString(*frontMatter.Description)
	}

	// Ensure file ends with newline
	if !strings.HasSuffix(buf.String(), "\n") {
		buf.WriteString("\n")
	}

	return buf.String(), nil
}

// GetProductFilePath returns the file path for a product markdown file
// Format: products/{category-slug}/{SKU}.md
func GetProductFilePath(sku, categorySlug string) string {
	if categorySlug == "" {
		categorySlug = "uncategorized"
	}
	return fmt.Sprintf("products/%s/%s.md", categorySlug, sku)
}

// GetCategoryFilePath returns the file path for a category markdown file
// Format: categories/{slug}.md
func GetCategoryFilePath(slug string) string {
	return fmt.Sprintf("categories/%s.md", slug)
}

// GetCollectionFilePath returns the file path for a collection markdown file
// Format: collections/{slug}.md
func GetCollectionFilePath(slug string) string {
	return fmt.Sprintf("collections/%s.md", slug)
}
