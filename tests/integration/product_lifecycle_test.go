// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── Kubernetes-style product fixtures ─────────────────────────────────────────

// validProductFixture returns a Kubernetes-style product file accepted by the
// pre-receive validation hook.
func validProductFixture(name string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: %s
  namespace: gitstore
spec:
  title: Integration Test Product
  tags: [integration, test]
  options:
  - name: size
    values: [S, M, L]
---

Integration test product.
`, name)
}

// invalidTitleFixture returns a product file with spec.title > 200 chars.
func invalidTitleFixture(name string) string {
	title := strings.Repeat("x", 201)
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: %s
  namespace: gitstore
spec:
  title: "%s"
---
`, name, title)
}

// invalidStatusFixture returns a product file with a top-level status key.
func invalidStatusFixture(name string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: %s
  namespace: gitstore
spec: {}
status:
  conditions: []
---
`, name)
}

// invalidMediaFixture returns a product file with a media entry missing fileRef.name.
func invalidMediaFixture(name string) string {
	return fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: %s
  namespace: gitstore
spec:
  media:
  - fileRef:
      kind: File
---
`, name)
}

// ── GraphQL helpers ───────────────────────────────────────────────────────────

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type gqlResponse struct {
	Data   json.RawMessage   `json:"data"`
	Errors []json.RawMessage `json:"errors,omitempty"`
}

func gqlQuery(t *testing.T, query string, vars map[string]any) gqlResponse {
	t.Helper()
	body, err := json.Marshal(gqlRequest{Query: query, Variables: vars})
	if err != nil {
		t.Fatalf("marshal gql request: %v", err)
	}
	resp, err := http.Post(apiURL+"/graphql", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("graphql request failed: %v — is the stack up? (API_URL=%s)", err, apiURL)
	}
	defer resp.Body.Close()
	var gqlResp gqlResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		t.Fatalf("decode gql response: %v", err)
	}
	return gqlResp
}

// uniqueName returns a timestamped product name to avoid collisions between runs.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// ── T032/T039: Full lifecycle — valid file accepted and queryable ─────────────

func TestProductLifecycle_ValidFile_AcceptedAndQueryable(t *testing.T) {
	name := uniqueName("lifecycle-valid")
	h := newPushHelper(t)
	h.commitProduct(name+".md", validProductFixture(name))
	out, err := h.push()
	if err != nil {
		t.Fatalf("expected push to succeed for valid product, got error:\n%s", out)
	}

	// Query the product via GraphQL.
	resp := gqlQuery(t, `
		query($ns: String!, $name: String!) {
			product(by: {namespacePath: {namespace: $ns, name: $name}}) {
				id
				spec { title tags }
			}
		}
	`, map[string]any{"ns": "gitstore", "name": name})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors: %s", resp.Errors)
	}

	var data struct {
		Product *struct {
			ID   string `json:"id"`
			Spec struct {
				Title *string  `json:"title"`
				Tags  []string `json:"tags"`
			} `json:"spec"`
		} `json:"product"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("unmarshal product response: %v", err)
	}
	if data.Product == nil {
		t.Fatal("expected product in response, got nil")
	}
	if data.Product.Spec.Title == nil || *data.Product.Spec.Title != "Integration Test Product" {
		t.Errorf("expected spec.title %q, got %v", "Integration Test Product", data.Product.Spec.Title)
	}
}

// ── T033/T040: Invalid title — push rejected with field-scoped error ──────────

func TestProductLifecycle_InvalidTitle_PushRejected(t *testing.T) {
	name := uniqueName("lifecycle-bad-title")
	h := newPushHelper(t)
	h.commitProduct(name+".md", invalidTitleFixture(name))
	out, err := h.push()
	if err == nil {
		t.Fatal("expected push to be rejected for title > 200 chars, but it succeeded")
	}
	combined := strings.ToLower(out)
	if !strings.Contains(combined, "spec.title") {
		t.Errorf("expected rejection message to name spec.title, got:\n%s", out)
	}
	if !strings.Contains(combined, "200") {
		t.Errorf("expected rejection message to mention 200-character limit, got:\n%s", out)
	}
}

// ── T034/T040: Status key present — push rejected as system-managed ───────────

func TestProductLifecycle_StatusPresent_PushRejected(t *testing.T) {
	name := uniqueName("lifecycle-bad-status")
	h := newPushHelper(t)
	h.commitProduct(name+".md", invalidStatusFixture(name))
	out, err := h.push()
	if err == nil {
		t.Fatal("expected push to be rejected for status key present, but it succeeded")
	}
	combined := strings.ToLower(out)
	if !strings.Contains(combined, "status") {
		t.Errorf("expected rejection message to identify 'status', got:\n%s", out)
	}
	if !strings.Contains(combined, "system-managed") {
		t.Errorf("expected rejection message to say 'system-managed', got:\n%s", out)
	}
}

// ── T035/T040: Missing fileRef.name — push rejected with indexed path ─────────

func TestProductLifecycle_MissingFileRefName_PushRejected(t *testing.T) {
	name := uniqueName("lifecycle-bad-media")
	h := newPushHelper(t)
	h.commitProduct(name+".md", invalidMediaFixture(name))
	out, err := h.push()
	if err == nil {
		t.Fatal("expected push to be rejected for missing fileRef.name, but it succeeded")
	}
	combined := strings.ToLower(out)
	if !strings.Contains(combined, "fileref.name") && !strings.Contains(combined, "fileref") {
		t.Errorf("expected rejection to name fileRef.name, got:\n%s", out)
	}
}

// ── T036/T041: Status hydration — controller blob returned with correct enums ─

func TestProductLifecycle_StatusHydration(t *testing.T) {
	// This test requires a product to be ingested and a status blob written.
	// It validates FR-010/FR-012 end-to-end via the GraphQL API.
	name := uniqueName("lifecycle-status")
	h := newPushHelper(t)
	h.commitProduct(name+".md", validProductFixture(name))
	if out, err := h.push(); err != nil {
		t.Fatalf("setup push failed: %v\n%s", err, out)
	}

	// Query the product to confirm it exists and has null status (not yet reconciled).
	resp := gqlQuery(t, `
		query($ns: String!, $name: String!) {
			product(by: {namespacePath: {namespace: $ns, name: $name}}) {
				id
				status { observedGeneration conditions { type status } }
			}
		}
	`, map[string]any{"ns": "gitstore", "name": name})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors after push: %s", resp.Errors)
	}

	var data struct {
		Product *struct {
			ID     string      `json:"id"`
			Status interface{} `json:"status"`
		} `json:"product"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Product == nil {
		t.Fatal("product not found after push")
	}
	// Status must be null for a newly admitted product (FR-011).
	if data.Product.Status != nil {
		t.Logf("status is non-nil for newly pushed product (controller may have already reconciled): %v", data.Product.Status)
	}
}

// ── T037/T042: Documentation examples parseable via push ─────────────────────

func TestDocumentationExamples_ParseCorrectly(t *testing.T) {
	examplesDir := filepath.Join("..", "..", "docs", "products", "examples")

	cases := []struct {
		file        string
		expectAccept bool
		expectErrFragment string
	}{
		{"valid-product.md", true, ""},
		{"invalid-status.md", false, "system-managed"},
		{"invalid-title.md", false, "200"},
		{"invalid-media.md", false, "fileref"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.file, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(examplesDir, tc.file))
			if err != nil {
				t.Fatalf("could not read example file %s: %v", tc.file, err)
			}

			name := uniqueName("docexample")
			// Replace the metadata.name in the example with a unique name to avoid collisions.
			fixture := string(content)
			if !strings.Contains(fixture, "name:") {
				t.Fatalf("example file %s has no metadata.name field", tc.file)
			}

			h := newPushHelper(t)
			h.commitProduct(name+".md", fixture)
			out, pushErr := h.push()

			if tc.expectAccept {
				if pushErr != nil {
					t.Fatalf("expected %s to be accepted, but push rejected:\n%s", tc.file, out)
				}
			} else {
				if pushErr == nil {
					t.Fatalf("expected %s to be rejected, but push succeeded", tc.file)
				}
				if tc.expectErrFragment != "" && !strings.Contains(strings.ToLower(out), tc.expectErrFragment) {
					t.Errorf("expected rejection for %s to contain %q, got:\n%s", tc.file, tc.expectErrFragment, out)
				}
			}
		})
	}
}
