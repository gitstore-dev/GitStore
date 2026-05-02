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
	// Use a timestamp suffix so repeated runs never collide on filename or SKU.
	ts := time.Now().UnixMilli()
	filename := fmt.Sprintf("inttest-catalog-%d.md", ts)
	sku := fmt.Sprintf("INTTEST-%d", ts)
	content := uniqueValidProduct(ts)

	h := newPushHelper(t)
	h.commitProduct(filename, content)

	out, err := h.push()
	if err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}

	tag := fmt.Sprintf("v0.0.1-inttest-%d", ts)
	out, err = h.pushTag(tag)
	if err != nil {
		t.Fatalf("tag push failed: %v\n%s", err, out)
	}

	// Poll GraphQL up to 10 seconds for the product to appear.
	targetSKU := sku
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
	ts := time.Now().UnixMilli()
	filename := fmt.Sprintf("inttest-invalid-%d.md", ts)
	content := uniqueInvalidProduct(ts)

	h := newPushHelper(t)
	h.commitProduct(filename, content)

	out, err := h.push()
	if err == nil {
		t.Errorf("expected push to be rejected, but it succeeded\noutput: %s", out)
		return
	}

	// The git transport layer surfaces the 422 as "HTTP 422" or "send-pack" error.
	// The human-readable validation message is in the git-service logs.
	// We accept any non-zero exit code with 422 in the output as a valid rejection.
	combined := strings.ToLower(out)
	if !strings.Contains(combined, "422") &&
		!strings.Contains(combined, "price") &&
		!strings.Contains(combined, "validation") {
		t.Errorf("rejection output should contain '422', 'price', or 'validation', got:\n%s", out)
	}

	// Confirm the invalid SKU is absent from GraphQL.
	invalidSKU := fmt.Sprintf("INTTEST-BAD-%d", ts)
	skus, queryErr := queryProductSKUs(t)
	if queryErr != nil {
		t.Logf("could not query GraphQL to verify absence (skipping absence check): %v", queryErr)
		return
	}
	for _, sku := range skus {
		if sku == invalidSKU {
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
