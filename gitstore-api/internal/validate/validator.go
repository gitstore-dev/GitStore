// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package validate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

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

	// Opt-in: files that don't begin with --- are not product resources; skip.
	if !bytes.HasPrefix(bytes.TrimLeftFunc(raw, unicode.IsSpace), []byte("---")) {
		return nil, raw, nil
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
	formats := []*frontmatter.Format{
		frontmatter.NewFormat("---", "---", yaml.Unmarshal),
	}
	body, err := frontmatter.Parse(bytes.NewReader(raw), &res, formats...)
	if err != nil {
		return nil, nil, fmt.Errorf("validate: parse frontmatter: %w", err)
	}

	// Post-parse: collect all violations before returning.
	var errs []error

	// Struct-tag validation — map to user-friendly messages.
	if err := validate.Struct(res); err != nil {
		errs = append(errs, toFriendlyError(err))
	}

	// Spec-level validation.
	if err := validateSpec(res.Spec); err != nil {
		errs = append(errs, err)
	}

	// Label length validation.
	if err := validateLabels(res.Metadata.Labels); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return nil, nil, errors.Join(errs...)
	}

	return &res, body, nil
}

// extractFrontmatterBlock returns the raw bytes between the opening and
// closing --- delimiters so they can be unmarshalled as a generic map.
func extractFrontmatterBlock(src []byte) ([]byte, error) {
	const delim = "---"
	_, rest, found := strings.Cut(string(src), delim)
	if !found {
		return nil, fmt.Errorf("validate: document does not use Kubernetes-style frontmatter (missing apiVersion); migration is not supported in alpha")
	}
	inner, _, ok := strings.Cut(rest, delim)
	if !ok {
		return nil, fmt.Errorf("validate: unclosed frontmatter block")
	}
	return []byte(inner), nil
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

	// spec block must be present.
	if _, ok := raw["spec"]; !ok {
		return fmt.Errorf("validate: spec is required")
	}

	// Forbidden top-level key: status.
	if _, ok := raw["status"]; ok {
		return fmt.Errorf("validate: status is system-managed and must not be set by authors")
	}

	// Forbidden read-only metadata keys — collect all before returning (FR-008, FR-009).
	if meta, ok := raw["metadata"].(map[string]any); ok {
		readOnly := []string{"uid", "resourceVersion", "generation", "creationTimestamp", "revision", "ownerReferences"}
		var forbidden []string
		for _, field := range readOnly {
			if _, present := meta[field]; present {
				forbidden = append(forbidden, field)
			}
		}
		if len(forbidden) > 0 {
			msgs := make([]string, len(forbidden))
			for i, f := range forbidden {
				msgs[i] = fmt.Sprintf("validate: metadata.%s is read-only and must not be set by authors", f)
			}
			return fmt.Errorf("%s", strings.Join(msgs, "\n"))
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

// fieldPath converts a go-playground/validator StructNamespace to a dotted
// lowercase path relative to the root struct.
// e.g. "ProductResource.Spec.Media[0].FileRef.Name" → "spec.media[0].fileref.name"
func fieldPath(fe validator.FieldError) string {
	ns := fe.StructNamespace()
	// Strip the root struct name prefix (everything up to and including the first dot).
	if idx := strings.IndexByte(ns, '.'); idx >= 0 {
		ns = ns[idx+1:]
	}
	return strings.ToLower(ns)
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
		path := fieldPath(fe)
		switch fe.Tag() {
		case "required":
			msgs = append(msgs, fmt.Sprintf("validate: %s is required", path))
		case "eq":
			msgs = append(msgs, fmt.Sprintf("validate: %s must be %q, got %q", path, fe.Param(), fe.Value()))
		case "max":
			msgs = append(msgs, fmt.Sprintf("validate: %s exceeds maximum length of %s characters", path, fe.Param()))
		default:
			msgs = append(msgs, fmt.Sprintf("validate: %s failed %s", path, fe.Tag()))
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
