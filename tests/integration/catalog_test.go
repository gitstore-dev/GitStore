package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestTagPushPublishesToGraphQL covers contract C-003:
// After pushing a valid commit + release tag, the product must appear in
// the gitstore-api GraphQL catalog within 10 seconds.
func TestTagPushPublishesToGraphQL(t *testing.T) {
	h := newPushHelper(t)
	h.commitProduct("inttest-catalog.md", validProductFrontmatter)

	out, err := h.push()
	if err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}

	tag := fmt.Sprintf("v0.0.1-inttest-%d", time.Now().UnixMilli())
	out, err = h.pushTag(tag)
	if err != nil {
		t.Fatalf("tag push failed: %v\n%s", err, out)
	}

	// Poll GraphQL up to 10 seconds for the product to appear.
	const targetSKU = "INTTEST-001"
	deadline := time.Now().Add(10 * time.Second)
	found := false

	for time.Now().Before(deadline) {
		skus, err := queryProductSKUs(t)
		if err == nil {
			for _, sku := range skus {
				if sku == targetSKU {
					found = true
					break
				}
			}
		}
		if found {
			break
		}
		time.Sleep(time.Second)
	}

	if !found {
		t.Errorf("product with SKU %q not found in GraphQL catalog within 10 seconds after tag push", targetSKU)
	}
}

// TestInvalidPushIsRejected covers contract C-004:
// A commit with invalid front-matter (non-numeric price) must be rejected.
// The invalid product must NOT appear in the GraphQL catalog.
func TestInvalidPushIsRejected(t *testing.T) {
	h := newPushHelper(t)
	h.commitProduct("inttest-invalid.md", invalidProductFrontmatter)

	out, err := h.push()
	if err == nil {
		t.Errorf("expected push to be rejected, but it succeeded\noutput: %s", out)
		return
	}

	combined := strings.ToLower(out)
	if !strings.Contains(combined, "price") && !strings.Contains(combined, "validation") {
		t.Errorf("rejection message should mention 'price' or 'validation', got:\n%s", out)
	}

	// Confirm the invalid SKU is absent from GraphQL.
	skus, err := queryProductSKUs(t)
	if err != nil {
		t.Logf("could not query GraphQL to verify absence (skipping absence check): %v", err)
		return
	}
	for _, sku := range skus {
		if sku == "INTTEST-BAD-001" {
			t.Errorf("invalid product SKU %q appeared in GraphQL catalog after rejected push", sku)
		}
	}
}

// queryProductSKUs returns all product SKUs currently in the GraphQL catalog.
func queryProductSKUs(t *testing.T) ([]string, error) {
	t.Helper()

	query := `{"query":"{ products(first: 100) { edges { node { sku } } } }"}`
	resp, err := http.Post(
		fmt.Sprintf("%s/graphql", apiURL),
		"application/json",
		bytes.NewBufferString(query),
	)
	if err != nil {
		return nil, fmt.Errorf("GraphQL request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading GraphQL response: %w", err)
	}

	var result struct {
		Data struct {
			Products struct {
				Edges []struct {
					Node struct {
						SKU string `json:"sku"`
					} `json:"node"`
				} `json:"edges"`
			} `json:"products"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing GraphQL response: %w (body: %s)", err, body)
	}

	var skus []string
	for _, edge := range result.Data.Products.Edges {
		skus = append(skus, edge.Node.SKU)
	}
	return skus, nil
}
