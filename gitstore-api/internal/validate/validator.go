// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package validate

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/adrg/frontmatter"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"
)

var validate = validator.New()

// Parse reads a Markdown document, extracts the YAML frontmatter into a
// ProductResource, validates it, and returns the parsed resource, the
// remaining Markdown body, and any error.
//
// Validation is two-pass:
//  1. Pre-parse: detect legacy format, forbidden top-level keys (status,
//     read-only metadata fields) from the raw YAML map before struct binding.
//  2. Post-parse: struct-tag validation via go-playground/validator, plus
//     spec-level rules (options.name required/unique, label length).
func Parse(r io.Reader) (*catalog.ProductResource, []byte, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, fmt.Errorf("validate: read: %w", err)
	}

	// Extract the raw YAML block between the first --- delimiters.
	fmRaw, err := extractFrontmatterBlock(raw)
	if err != nil {
		return nil, nil, err
	}

	// Pre-parse checks on the raw map.
	if err := preParseChecks(fmRaw); err != nil {
		return nil, nil, err
	}

	// Struct binding via frontmatter.Parse.
	var res catalog.ProductResource
	body, err := frontmatter.Parse(strings.NewReader(string(raw)), &res)
	if err != nil {
		return nil, nil, fmt.Errorf("validate: parse frontmatter: %w", err)
	}

	// Struct-tag validation — map to user-friendly messages.
	if err := validate.Struct(res); err != nil {
		return nil, nil, toFriendlyError(err)
	}

	// Spec-level validation.
	if err := validateSpec(res.Spec); err != nil {
		return nil, nil, err
	}

	// Label length validation.
	if err := validateLabels(res.Metadata.Labels); err != nil {
		return nil, nil, err
	}

	return &res, body, nil
}

// extractFrontmatterBlock returns the raw bytes between the opening and
// closing --- delimiters so they can be unmarshalled as a generic map.
func extractFrontmatterBlock(src []byte) ([]byte, error) {
	s := string(src)
	const delim = "---"
	start := strings.Index(s, delim)
	if start == -1 {
		return nil, fmt.Errorf("validate: document does not use Kubernetes-style frontmatter (missing apiVersion); migration is not supported in alpha")
	}
	rest := s[start+len(delim):]
	end := strings.Index(rest, delim)
	if end == -1 {
		return nil, fmt.Errorf("validate: unclosed frontmatter block")
	}
	return []byte(rest[:end]), nil
}

// preParseChecks unmarshals the frontmatter block into a raw map and enforces
// rules that must fire before struct binding.
func preParseChecks(fmRaw []byte) error {
	var raw map[string]any
	if err := yaml.Unmarshal(fmRaw, &raw); err != nil {
		return fmt.Errorf("validate: malformed YAML: %w", err)
	}

	// Legacy format guard: apiVersion must be present.
	if _, ok := raw["apiVersion"]; !ok {
		return fmt.Errorf("validate: document does not use Kubernetes-style frontmatter (missing apiVersion); migration is not supported in alpha")
	}

	// Forbidden top-level key: status.
	if _, ok := raw["status"]; ok {
		return fmt.Errorf("validate: status is system-managed and must not be set by authors")
	}

	// Forbidden read-only metadata keys.
	if meta, ok := raw["metadata"].(map[string]any); ok {
		readOnly := []string{"uid", "resourceVersion", "generation", "creationTimestamp", "revision", "ownerReferences"}
		for _, field := range readOnly {
			if _, present := meta[field]; present {
				return fmt.Errorf("validate: metadata.%s is read-only and must not be set by authors", field)
			}
		}
	}

	return nil
}

// validateSpec enforces spec-level rules that go beyond struct tags.
func validateSpec(spec catalog.ProductSpec) error {
	seen := make(map[string]struct{}, len(spec.Options))
	for i, opt := range spec.Options {
		if opt.Name == "" {
			return fmt.Errorf("validate: options[%d].name is required", i)
		}
		if _, dup := seen[opt.Name]; dup {
			return fmt.Errorf("validate: spec.options contains duplicate name %q", opt.Name)
		}
		seen[opt.Name] = struct{}{}
	}
	return nil
}

// toFriendlyError converts go-playground/validator field errors into
// lower-case, user-readable messages that match the spec error format.
func toFriendlyError(err error) error {
	var ve validator.ValidationErrors
	if !errors.As(err, &ve) {
		return fmt.Errorf("validate: %w", err)
	}
	msgs := make([]string, 0, len(ve))
	for _, fe := range ve {
		field := strings.ToLower(fe.Field())
		switch fe.Tag() {
		case "required":
			msgs = append(msgs, fmt.Sprintf("validate: %s is required", field))
		case "eq":
			msgs = append(msgs, fmt.Sprintf("validate: %s must be %q, got %q", field, fe.Param(), fe.Value()))
		default:
			msgs = append(msgs, fmt.Sprintf("validate: %s failed %s", field, fe.Tag()))
		}
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}

// validateLabels enforces Kubernetes label key/value length conventions.
func validateLabels(labels map[string]string) error {
	for k, v := range labels {
		// Key may have a prefix/name form: "prefix/name"
		prefix, name, hasSep := strings.Cut(k, "/")
		if hasSep {
			if len(prefix) > 253 {
				return fmt.Errorf("validate: label key prefix %q exceeds 253-character maximum", prefix)
			}
			if len(name) > 63 {
				return fmt.Errorf("validate: label key name segment %q exceeds 63-character maximum", name)
			}
		} else {
			if len(prefix) > 63 {
				return fmt.Errorf("validate: label key %q exceeds 63-character maximum", k)
			}
		}
		if len(v) > 63 {
			return fmt.Errorf("validate: label value for key %q exceeds 63-character maximum", k)
		}
	}
	return nil
}
