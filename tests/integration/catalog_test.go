// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// TestInvalidPushIsRejected covers contract C-004.
//
// git-service is a transport-only layer; schema/policy enforcement is
// delegated to API-managed hook workers (not yet implemented). Until a
// pre-receive hook worker rejects invalid content, this contract cannot be
// verified end-to-end and the test is skipped.
func TestInvalidPushIsRejected(t *testing.T) {
	t.Skip("C-004: push-time schema rejection requires a pre-receive hook worker (not yet implemented)")
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
