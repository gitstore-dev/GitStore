// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package validate

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/translation"
	"gopkg.in/yaml.v3"
)

func readResourceSource(r io.Reader) ([]byte, error) {
	prefix, err := io.ReadAll(io.LimitReader(r, translation.MaxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("validate: read: %w", err)
	}
	if len(prefix) <= translation.MaxDocumentBytes {
		return prefix, nil
	}
	if IsTranslationDocument(prefix) {
		return nil, translationError("document", "LimitExceeded", "Translation document exceeds 1 MiB")
	}
	remainder, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("validate: read: %w", err)
	}
	return append(prefix, remainder...), nil
}

// IsTranslationDocument probes only the first YAML document without resolving
// aliases or interpreting Markdown. It also recognizes unsupported i18n versions.
func IsTranslationDocument(raw []byte) bool {
	raw = bytes.TrimLeftFunc(raw, unicode.IsSpace)
	if !bytes.HasPrefix(raw, []byte("---")) {
		return false
	}
	if frontmatter, _, err := translationEnvelope(raw); err == nil {
		raw = frontmatter
	}
	var document yaml.Node
	if yaml.NewDecoder(bytes.NewReader(raw)).Decode(&document) != nil || len(document.Content) != 1 {
		return translationHeaderProbe(raw)
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if value.Kind == yaml.AliasNode && value.Alias != nil {
			value = value.Alias
		}
		if key.Value == "kind" && value.Value == "Translation" ||
			key.Value == "apiVersion" && strings.HasPrefix(value.Value, "i18n.gitstore.dev/") {
			return true
		}
	}
	return false
}

func translationHeaderProbe(raw []byte) bool {
	for line := range bytes.SplitSeq(raw, []byte{'\n'}) {
		if len(line) == 0 || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		key, value, ok := strings.Cut(string(line), ":")
		if !ok {
			continue
		}
		key = strings.Trim(strings.TrimSpace(key), `"'`)
		value = strings.TrimSpace(value)
		if before, _, found := strings.Cut(value, " #"); found {
			value = strings.TrimSpace(before)
		}
		value = strings.Trim(value, `"'`)
		if key == "kind" && value == "Translation" ||
			key == "apiVersion" && strings.HasPrefix(value, "i18n.gitstore.dev/") {
			return true
		}
	}
	return false
}

func translationError(field, reason, message string) error {
	if len(field) > 256 {
		field = string([]rune(field)[:min(64, utf8.RuneCountInString(field))]) + "..."
	}
	return &translation.Error{Field: field, Reason: reason, Message: message}
}

func translationEnvelope(raw []byte) ([]byte, []byte, error) {
	source := bytes.TrimLeftFunc(raw, unicode.IsSpace)
	opening := bytes.IndexByte(source, '\n')
	if opening < 0 || !bytes.Equal(bytes.TrimSpace(source[:opening]), []byte("---")) {
		return nil, nil, translationError("document", "MalformedSource", "expected an opening delimiter line")
	}
	start := opening + 1
	for at := start; at <= len(source); {
		end := bytes.IndexByte(source[at:], '\n')
		next := len(source)
		if end >= 0 {
			next = at + end + 1
			end += at
		} else {
			end = len(source)
		}
		if bytes.Equal(bytes.TrimSpace(source[at:end]), []byte("---")) {
			return source[start:at], source[next:], nil
		}
		if next == len(source) {
			break
		}
		at = next
	}
	return nil, nil, translationError("document", "MalformedSource", "frontmatter has no closing delimiter line")
}

func validateTranslationTree(node *yaml.Node, depth int, count *int) error {
	return validateTranslationNode(node, depth, count, "")
}

func validateTranslationNode(node *yaml.Node, depth int, count *int, field string) error {
	*count += 1
	if *count > 65_536 || depth > 64 {
		return translationError("document", "LimitExceeded", "YAML node or depth limit exceeded")
	}
	if node.Kind == yaml.AliasNode || node.Tag == "!!merge" {
		return translationError(translationNodeField(field), "MalformedSource", "YAML aliases and merges are not permitted")
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || seen[key.Value] {
				return translationError(translationNodeField(strings.TrimPrefix(field+"."+key.Value, ".")), "MalformedSource", "mapping keys must be unique strings")
			}
			seen[key.Value] = true
		}
	}
	for i, child := range node.Content {
		childField := field
		switch node.Kind {
		case yaml.MappingNode:
			childField = strings.TrimPrefix(field+"."+node.Content[i-i%2].Value, ".")
		case yaml.SequenceNode:
			childField = fmt.Sprintf("%s[%d]", field, i)
		}
		if err := validateTranslationNode(child, depth+1, count, childField); err != nil {
			return err
		}
	}
	return nil
}

func translationNodeField(field string) string {
	if field == "" {
		return "document"
	}
	return field
}

func translationMapping(node *yaml.Node, path string, allowed, required []string) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, translationError(path, "MalformedSource", "expected a mapping")
	}
	fields := map[string]*yaml.Node{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		found := false
		for _, name := range allowed {
			found = found || name == key
		}
		if !found {
			return nil, translationError(strings.TrimPrefix(path+"."+key, "."), "UnknownField", "member is not author-writable")
		}
		fields[key] = node.Content[i+1]
	}
	for _, key := range required {
		if fields[key] == nil {
			return nil, translationError(strings.TrimPrefix(path+"."+key, "."), "Required", "member is required")
		}
	}
	return fields, nil
}

func translationString(node *yaml.Node, path string, empty bool) error {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return translationError(path, "InvalidText", "expected a string, not null or a coerced value")
	}
	if !empty && strings.TrimSpace(node.Value) == "" {
		return translationError(path, "Required", "a nonempty string is required")
	}
	return nil
}

func (p *Parser) parseTranslation(raw []byte) (*ParsedResource, []byte, error) {
	if len(raw) > translation.MaxDocumentBytes {
		return nil, nil, translationError("document", "LimitExceeded", "Translation document exceeds 1 MiB")
	}
	if !utf8.Valid(raw) {
		return nil, nil, translationError("document", "InvalidEncoding", "document must be valid UTF-8")
	}
	frontmatter, body, err := translationEnvelope(raw)
	if err != nil {
		return nil, nil, err
	}
	var document yaml.Node
	if yaml.Unmarshal(frontmatter, &document) != nil || len(document.Content) != 1 {
		return nil, nil, translationError("document", "MalformedSource", "invalid YAML frontmatter")
	}
	count := 0
	if err := validateTranslationTree(&document, 0, &count); err != nil {
		return nil, nil, err
	}
	envelope, err := translationMapping(document.Content[0], "", []string{"apiVersion", "kind", "metadata", "spec"}, []string{"apiVersion", "kind", "metadata", "spec"})
	if err != nil {
		return nil, nil, err
	}
	for _, key := range []string{"apiVersion", "kind"} {
		if err := translationString(envelope[key], key, false); err != nil {
			return nil, nil, err
		}
	}
	if envelope["apiVersion"].Value != translation.APIVersion || envelope["kind"].Value != "Translation" {
		return nil, nil, translationError("apiVersion", "UnsupportedResource", "unsupported Translation version or kind")
	}
	meta, err := translationMapping(envelope["metadata"], "metadata", []string{"name", "namespace", "labels", "annotations"}, []string{"name"})
	if err != nil {
		return nil, nil, err
	}
	for _, key := range []string{"name", "namespace"} {
		if meta[key] != nil {
			if err := translationString(meta[key], "metadata."+key, false); err != nil {
				return nil, nil, err
			}
		}
	}
	for _, key := range []string{"labels", "annotations"} {
		if node := meta[key]; node != nil {
			if node.Kind != yaml.MappingNode {
				return nil, nil, translationError("metadata."+key, "MalformedSource", "expected a string mapping")
			}
			for i := 1; i < len(node.Content); i += 2 {
				if err := translationString(node.Content[i], "metadata."+key, true); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	spec, err := translationMapping(envelope["spec"], "spec", []string{"locale", "targetRef", "patches"}, []string{"locale", "targetRef"})
	if err != nil {
		return nil, nil, err
	}
	if err := translationString(spec["locale"], "spec.locale", false); err != nil {
		return nil, nil, err
	}
	ref, err := translationMapping(spec["targetRef"], "spec.targetRef", []string{"apiVersion", "kind", "name", "namespace", "uid", "resourceVersion"}, []string{"apiVersion", "kind", "name"})
	if err != nil {
		return nil, nil, err
	}
	for _, key := range []string{"apiVersion", "kind", "name", "namespace", "uid", "resourceVersion"} {
		if ref[key] != nil {
			if err := translationString(ref[key], "spec.targetRef."+key, false); err != nil {
				return nil, nil, err
			}
		}
	}
	if node := spec["patches"]; node != nil {
		if node.Kind != yaml.SequenceNode {
			return nil, nil, translationError("spec.patches", "MalformedSource", "expected a patch list")
		}
		if len(node.Content) > translation.MaxPatches {
			return nil, nil, translationError("spec.patches", "LimitExceeded", "at most 256 patches are permitted")
		}
		for i, entry := range node.Content {
			path := fmt.Sprintf("spec.patches[%d]", i)
			patch, err := translationMapping(entry, path, []string{"fieldPath", "translation"}, []string{"fieldPath", "translation"})
			if err != nil {
				return nil, nil, err
			}
			if err := translationString(patch["fieldPath"], path+".fieldPath", false); err != nil {
				return nil, nil, err
			}
			if err := translationString(patch["translation"], path+".translation", true); err != nil {
				return nil, nil, err
			}
		}
	}
	var resource catalog.TranslationResource
	if err := document.Decode(&resource); err != nil {
		return nil, nil, translationError("document", "MalformedSource", "invalid Translation structure")
	}
	effectiveNamespace, err := p.namespaceForManifest(frontmatter)
	if err != nil {
		return nil, nil, err
	}
	if effectiveNamespace != "" {
		resource.Metadata.Namespace = effectiveNamespace
	}
	if err := validateLabels(resource.Metadata.Labels); err != nil {
		return nil, nil, translationError("metadata.labels", "InvalidMetadata", "labels do not satisfy the metadata constraints")
	}
	if err := translation.ValidateSpec(resource.Spec, resource.Metadata.Namespace, body); err != nil {
		return nil, nil, err
	}
	return &ParsedResource{Kind: "Translation", Translation: &resource}, body, nil
}
