package models

import "time"

// Collection represents a flat grouping of products
type Collection struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Slug         string    `json:"slug"`
	DisplayOrder int       `json:"display_order"`
	ProductIDs   []string  `json:"product_ids"`
	Body         string    `json:"body"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ContainsProduct returns true if the collection contains the given product ID
func (c *Collection) ContainsProduct(productID string) bool {
	for _, id := range c.ProductIDs {
		if id == productID {
			return true
		}
	}
	return false
}

// ProductCount returns the number of products in this collection
func (c *Collection) ProductCount() int {
	return len(c.ProductIDs)
}
