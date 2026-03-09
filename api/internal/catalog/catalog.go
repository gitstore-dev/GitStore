// Catalog - in-memory representation of the product catalog

package catalog

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Product represents a sellable item
type Product struct {
	ID                string                 `yaml:"id"`
	SKU               string                 `yaml:"sku"`
	Title             string                 `yaml:"title"`
	Price             float64                `yaml:"price"`
	Currency          string                 `yaml:"currency"`
	InventoryStatus   string                 `yaml:"inventory_status"`
	InventoryQuantity *int                   `yaml:"inventory_quantity"`
	CategoryID        string                 `yaml:"category_id"`
	CollectionIDs     []string               `yaml:"collection_ids"`
	Images            []string               `yaml:"images"`
	Metadata          map[string]interface{} `yaml:"metadata"`
	CreatedAt         time.Time              `yaml:"created_at"`
	UpdatedAt         time.Time              `yaml:"updated_at"`
	Body              string                 `yaml:"-"` // Markdown body
}

// Category represents a hierarchical classification
type Category struct {
	ID           string    `yaml:"id"`
	Name         string    `yaml:"name"`
	Slug         string    `yaml:"slug"`
	ParentID     *string   `yaml:"parent_id"`
	DisplayOrder int       `yaml:"display_order"`
	CreatedAt    time.Time `yaml:"created_at"`
	UpdatedAt    time.Time `yaml:"updated_at"`
	Body         string    `yaml:"-"`
}

// Collection represents a flat grouping of products
type Collection struct {
	ID           string    `yaml:"id"`
	Name         string    `yaml:"name"`
	Slug         string    `yaml:"slug"`
	DisplayOrder int       `yaml:"display_order"`
	ProductIDs   []string  `yaml:"product_ids"`
	CreatedAt    time.Time `yaml:"created_at"`
	UpdatedAt    time.Time `yaml:"updated_at"`
	Body         string    `yaml:"-"`
}

// Catalog holds all catalog entities loaded from git
type Catalog struct {
	mu                 sync.RWMutex
	commit             string
	products           map[string]*Product    // ID -> Product
	categories         map[string]*Category   // ID -> Category
	collections        map[string]*Collection // ID -> Collection
	productsBySKU      map[string]*Product    // SKU -> Product
	categoriesBySlug   map[string]*Category   // Slug -> Category
	collectionsBySlug  map[string]*Collection // Slug -> Collection
	loadedAt           time.Time
}

// NewCatalog creates a new empty catalog
func NewCatalog(commit string) *Catalog {
	return &Catalog{
		commit:            commit,
		products:          make(map[string]*Product),
		categories:        make(map[string]*Category),
		collections:       make(map[string]*Collection),
		productsBySKU:     make(map[string]*Product),
		categoriesBySlug:  make(map[string]*Category),
		collectionsBySlug: make(map[string]*Collection),
		loadedAt:          time.Now(),
	}
}

// AddProduct adds a product to the catalog
func (c *Catalog) AddProduct(p *Product) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.products[p.ID] = p
	c.productsBySKU[p.SKU] = p
}

// AddCategory adds a category to the catalog
func (c *Catalog) AddCategory(cat *Category) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.categories[cat.ID] = cat
	c.categoriesBySlug[cat.Slug] = cat
}

// AddCollection adds a collection to the catalog
func (c *Catalog) AddCollection(coll *Collection) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.collections[coll.ID] = coll
	c.collectionsBySlug[coll.Slug] = coll
}

// GetProduct retrieves a product by ID
func (c *Catalog) GetProduct(id string) (*Product, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.products[id]
	return p, ok
}

// GetProductBySKU retrieves a product by SKU
func (c *Catalog) GetProductBySKU(sku string) (*Product, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.productsBySKU[sku]
	return p, ok
}

// GetCategory retrieves a category by ID
func (c *Catalog) GetCategory(id string) (*Category, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cat, ok := c.categories[id]
	return cat, ok
}

// GetCollection retrieves a collection by ID
func (c *Catalog) GetCollection(id string) (*Collection, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	coll, ok := c.collections[id]
	return coll, ok
}

// GetCategoryBySlug retrieves a category by slug
func (c *Catalog) GetCategoryBySlug(slug string) (*Category, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cat, ok := c.categoriesBySlug[slug]
	return cat, ok
}

// GetCollectionBySlug retrieves a collection by slug
func (c *Catalog) GetCollectionBySlug(slug string) (*Collection, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	coll, ok := c.collectionsBySlug[slug]
	return coll, ok
}

// AllProducts returns all products
func (c *Catalog) AllProducts() []*Product {
	c.mu.RLock()
	defer c.mu.RUnlock()
	products := make([]*Product, 0, len(c.products))
	for _, p := range c.products {
		products = append(products, p)
	}
	return products
}

// AllCategories returns all categories
func (c *Catalog) AllCategories() []*Category {
	c.mu.RLock()
	defer c.mu.RUnlock()
	categories := make([]*Category, 0, len(c.categories))
	for _, cat := range c.categories {
		categories = append(categories, cat)
	}
	return categories
}

// AllCollections returns all collections
func (c *Catalog) AllCollections() []*Collection {
	c.mu.RLock()
	defer c.mu.RUnlock()
	collections := make([]*Collection, 0, len(c.collections))
	for _, coll := range c.collections {
		collections = append(collections, coll)
	}
	return collections
}

// ProductCount returns the number of products
func (c *Catalog) ProductCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.products)
}

// CategoryCount returns the number of categories
func (c *Catalog) CategoryCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.categories)
}

// CollectionCount returns the number of collections
func (c *Catalog) CollectionCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.collections)
}

// Commit returns the git commit SHA
func (c *Catalog) Commit() string {
	return c.commit
}

// LoadedAt returns when the catalog was loaded
func (c *Catalog) LoadedAt() time.Time {
	return c.loadedAt
}

// AddProductFromMarkdown parses markdown and adds product
func (c *Catalog) AddProductFromMarkdown(filename, content string) error {
	frontmatter, body, err := parseMarkdown(content)
	if err != nil {
		return fmt.Errorf("failed to parse markdown: %w", err)
	}

	var product Product
	if err := yaml.Unmarshal([]byte(frontmatter), &product); err != nil {
		return fmt.Errorf("failed to parse YAML: %w", err)
	}

	product.Body = body
	c.AddProduct(&product)
	return nil
}

// AddCategoryFromMarkdown parses markdown and adds category
func (c *Catalog) AddCategoryFromMarkdown(filename, content string) error {
	frontmatter, body, err := parseMarkdown(content)
	if err != nil {
		return fmt.Errorf("failed to parse markdown: %w", err)
	}

	var category Category
	if err := yaml.Unmarshal([]byte(frontmatter), &category); err != nil {
		return fmt.Errorf("failed to parse YAML: %w", err)
	}

	category.Body = body
	c.AddCategory(&category)
	return nil
}

// AddCollectionFromMarkdown parses markdown and adds collection
func (c *Catalog) AddCollectionFromMarkdown(filename, content string) error {
	frontmatter, body, err := parseMarkdown(content)
	if err != nil {
		return fmt.Errorf("failed to parse markdown: %w", err)
	}

	var collection Collection
	if err := yaml.Unmarshal([]byte(frontmatter), &collection); err != nil {
		return fmt.Errorf("failed to parse YAML: %w", err)
	}

	collection.Body = body
	c.AddCollection(&collection)
	return nil
}

// parseMarkdown splits markdown into frontmatter and body
func parseMarkdown(content string) (frontmatter, body string, err error) {
	if !strings.HasPrefix(content, "---\n") {
		return "", "", fmt.Errorf("missing frontmatter delimiter")
	}

	rest := content[4:] // Skip opening "---\n"
	endIdx := strings.Index(rest, "\n---\n")
	if endIdx == -1 {
		return "", "", fmt.Errorf("missing closing frontmatter delimiter")
	}

	frontmatter = rest[:endIdx]
	body = strings.TrimSpace(rest[endIdx+5:]) // Skip "\n---\n"

	return frontmatter, body, nil
}
