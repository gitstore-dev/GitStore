package models

import (
	"testing"
)

func TestCollectionContainsProduct(t *testing.T) {
	collection := &Collection{
		ID:         "coll_1",
		ProductIDs: []string{"prod_1", "prod_2", "prod_3"},
	}

	if !collection.ContainsProduct("prod_2") {
		t.Error("Expected collection to contain prod_2")
	}

	if collection.ContainsProduct("prod_99") {
		t.Error("Expected collection not to contain prod_99")
	}
}

func TestCollectionProductCount(t *testing.T) {
	collection := &Collection{
		ID:         "coll_1",
		ProductIDs: []string{"prod_1", "prod_2", "prod_3"},
	}

	if collection.ProductCount() != 3 {
		t.Errorf("Expected count 3, got %d", collection.ProductCount())
	}

	emptyCollection := &Collection{
		ID:         "coll_2",
		ProductIDs: []string{},
	}

	if emptyCollection.ProductCount() != 0 {
		t.Errorf("Expected count 0, got %d", emptyCollection.ProductCount())
	}
}
