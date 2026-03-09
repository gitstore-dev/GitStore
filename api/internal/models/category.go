package models

import "time"

// Category represents a hierarchical product category
type Category struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Slug         string     `json:"slug"`
	ParentID     *string    `json:"parent_id,omitempty"`
	DisplayOrder int        `json:"display_order"`
	Body         string     `json:"body"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`

	// Computed fields (not in YAML)
	Parent       *Category   `json:"-"`
	Children     []*Category `json:"-"`
	Path         []*Category `json:"-"`  // Root to current
	Depth        int         `json:"-"`
}

// IsRoot returns true if this category has no parent
func (c *Category) IsRoot() bool {
	return c.ParentID == nil
}

// HasChildren returns true if this category has child categories
func (c *Category) HasChildren() bool {
	return len(c.Children) > 0
}

// GetAncestorIDs returns all ancestor category IDs from root to parent
func (c *Category) GetAncestorIDs() []string {
	if len(c.Path) == 0 {
		return []string{}
	}

	ids := make([]string, 0, len(c.Path))
	for _, ancestor := range c.Path {
		ids = append(ids, ancestor.ID)
	}
	return ids
}

// GetDescendantIDs returns all descendant category IDs recursively
func (c *Category) GetDescendantIDs() []string {
	ids := []string{}

	for _, child := range c.Children {
		ids = append(ids, child.ID)
		ids = append(ids, child.GetDescendantIDs()...)
	}

	return ids
}
