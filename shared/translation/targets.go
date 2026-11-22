// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package translation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	MaxTargetNodes = 65_536
	MaxTargetDepth = 64
	MaxTargetBytes = 100 << 20
)

type Descriptor struct {
	APIVersion string
	Kind       string
	Body       bool
	Digest     string
	policy     descriptorPolicy
}

type textRule struct {
	Path     string
	MinRunes int
	MaxRunes int
}

type selectorRule struct {
	Path         string
	IdentityPath string
}

type descriptorPolicy struct {
	ResolverVersion int
	APIVersion      string
	Kind            string
	Body            bool
	Fields          []textRule
	Lists           []selectorRule
	Boundaries      []string
	MaxNodes        int
	MaxDepth        int
	MaxBytes        int
}

func CapabilityDigest() string {
	policy := struct {
		APIVersion, LocalePolicy                                    string
		MaxDocumentBytes, MaxPatches, MaxPathBytes, MaxPathSegments int
		MaxSelectors                                                int
		Descriptors                                                 []string
	}{APIVersion: APIVersion, LocalePolicy: "rfc5646+x-text-v0.43.0/v1",
		MaxDocumentBytes: MaxDocumentBytes, MaxPatches: MaxPatches, MaxPathBytes: MaxPathBytes,
		MaxPathSegments: MaxPathSegments, MaxSelectors: MaxSelectors}
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		version := "catalog.gitstore.dev/v1beta1"
		if kind == "File" {
			version = "storage.gitstore.dev/v1beta1"
		}
		descriptor, _ := DescriptorFor(version, kind)
		policy.Descriptors = append(policy.Descriptors, descriptor.Digest)
	}
	return policyDigest(policy)
}

func policyDigest(policy any) string {
	raw, _ := json.Marshal(policy)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// TargetContentDigest fingerprints exact stored spec/body bytes, not status or
// resourceVersion. It does not validate target content or canonicalize its JSON.
func TargetContentDigest(ctx context.Context, apiVersion, kind string, spec json.RawMessage, body string) (string, error) {
	return targetContentDigest(ctx, apiVersion, kind, bytes.NewReader(spec), len(spec), body)
}

// TargetContentDigestStringSpec avoids copying a stored string spec into bytes.
func TargetContentDigestStringSpec(ctx context.Context, apiVersion, kind, spec, body string) (string, error) {
	return targetContentDigest(ctx, apiVersion, kind, strings.NewReader(spec), len(spec), body)
}

func targetContentDigest(ctx context.Context, apiVersion, kind string, spec io.Reader, specBytes int, body string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := DescriptorFor(apiVersion, kind); err != nil {
		return "", err
	}
	if specBytes > MaxTargetBytes || len(body) > MaxTargetBytes-specBytes {
		return "", invalid("spec.targetRef", "TargetUnavailable", "target source exceeds the material byte budget")
	}
	digest := sha256.New()
	var frame [8]byte
	var buffer [32 << 10]byte
	for _, part := range []struct {
		size   int
		reader io.Reader
	}{
		{len("gitstore-target-material/v1"), strings.NewReader("gitstore-target-material/v1")},
		{len(apiVersion), strings.NewReader(apiVersion)},
		{len(kind), strings.NewReader(kind)},
		{specBytes, spec},
		{len(body), strings.NewReader(body)},
	} {
		binary.BigEndian.PutUint64(frame[:], uint64(part.size))
		_, _ = digest.Write(frame[:])
		for {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			n, err := part.reader.Read(buffer[:])
			if n > 0 {
				_, _ = digest.Write(buffer[:n])
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", err
			}
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func DescriptorFor(apiVersion, kind string) (Descriptor, error) {
	version := "catalog.gitstore.dev/v1beta1"
	switch kind {
	case "Product", "ProductVariant", "CategoryTaxonomy", "Collection":
	case "File":
		version = "storage.gitstore.dev/v1beta1"
	default:
		return Descriptor{}, invalid("spec.targetRef.kind", "UnsupportedTarget", "target kind is not supported")
	}
	if version != apiVersion {
		return Descriptor{}, invalid("spec.targetRef.apiVersion", "UnsupportedTarget", "target version is not supported")
	}
	policy := descriptorPolicy{
		ResolverVersion: 1, APIVersion: version, Kind: kind, Body: true,
		MaxNodes: MaxTargetNodes, MaxDepth: MaxTargetDepth, MaxBytes: MaxTargetBytes,
	}
	if kind != "File" {
		policy.Fields = []textRule{{Path: "spec.title", MinRunes: 1}}
		policy.Lists = []selectorRule{{Path: "spec.media", IdentityPath: "fileRef.name"}}
		policy.Boundaries = []string{"spec.media{}.fileRef"}
	}
	switch kind {
	case "Product":
		policy.Fields[0].MaxRunes = 200
		policy.Fields = append(policy.Fields, textRule{Path: "spec.options{}.title"})
		policy.Lists = append(policy.Lists, selectorRule{Path: "spec.options", IdentityPath: "name"})
		policy.Boundaries = append(policy.Boundaries, "spec.categoryRef", "spec.options{}.definitionRef")
	case "ProductVariant":
		policy.Boundaries = append(policy.Boundaries, "spec.productRef", "spec.inventory.stockLocationRefs")
	case "CategoryTaxonomy":
		policy.Boundaries = append(policy.Boundaries, "spec.parentRef")
	case "Collection":
		policy.Boundaries = append(policy.Boundaries, "spec.targetRef")
	}
	return Descriptor{APIVersion: version, Kind: kind, Body: policy.Body, Digest: policyDigest(policy), policy: policy}, nil
}

func pathPattern(segments []Segment) string {
	var path strings.Builder
	path.WriteString("spec")
	for _, segment := range segments {
		path.WriteByte('.')
		path.WriteString(segment.Property)
		if segment.Identity != "" {
			path.WriteString("{}")
		}
	}
	return path.String()
}

func (d Descriptor) ruleFor(segments []Segment) (textRule, error) {
	pattern := pathPattern(segments)
	for _, rule := range d.policy.Fields {
		if rule.Path == pattern {
			return rule, nil
		}
	}
	for _, boundary := range d.policy.Boundaries {
		if pattern == boundary || strings.HasPrefix(pattern, boundary+".") {
			return textRule{}, invalid("fieldPath", "ReferenceBoundary", "paths cannot traverse a resource reference")
		}
	}
	return textRule{}, invalid("fieldPath", "UnsupportedField", "path is not an eligible owned text field")
}

func (d Descriptor) validatePath(path string) error {
	segments, err := ParsePath(path)
	if err != nil {
		return err
	}
	_, err = d.ruleFor(segments)
	return err
}

func (d Descriptor) validateText(path, text string) error {
	segments, err := ParsePath(path)
	if err != nil {
		return err
	}
	rule, err := d.ruleFor(segments)
	if err != nil {
		return err
	}
	length := utf8.RuneCountInString(text)
	if !utf8.ValidString(text) || length < rule.MinRunes || (rule.MaxRunes > 0 && length > rule.MaxRunes) {
		return invalid("translation", "InvalidText", "translation does not satisfy the target text constraint")
	}
	return nil
}

// ValidateFields observes a single bounded target snapshot without modifying it.
func ValidateFields(apiVersion, kind string, spec map[string]any, patches []Patch, body []byte) ([]string, error) {
	return ValidateFieldsContext(context.Background(), apiVersion, kind, spec, patches, body)
}

// ValidateFieldsContext also observes the caller's cancellation/deadline budget.
func ValidateFieldsContext(ctx context.Context, apiVersion, kind string, spec map[string]any, patches []Patch, body []byte) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	descriptor, err := DescriptorFor(apiVersion, kind)
	if err != nil {
		return nil, err
	}
	if err := validateContentBudget(patches, body); err != nil {
		return nil, err
	}
	if len(patches) == 0 && len(bytes.TrimSpace(body)) == 0 {
		return nil, invalid("spec.patches", "EmptyTranslation", "a patch or meaningful body is required")
	}
	if !utf8.Valid(body) {
		return nil, invalid("body", "InvalidEncoding", "body must be valid UTF-8")
	}
	paths := make([]string, len(patches))
	for i, patch := range patches {
		paths[i] = patch.FieldPath
	}
	pathIndex, pathErr := validatePaths(paths)
	for i, patch := range patches {
		if i == pathIndex {
			return nil, pathErr
		}
		if err := descriptor.validatePath(patch.FieldPath); err != nil {
			return nil, patchError(i, err)
		}
		if err := descriptor.validateText(patch.FieldPath, patch.Translation); err != nil {
			return nil, invalid(fmt.Sprintf("spec.patches[%d].translation", i), "InvalidText", "translation does not satisfy the target text constraint")
		}
	}
	var indexes selectorIndexes
	if len(patches) > 0 {
		budget := targetBudget{nodes: descriptor.policy.MaxNodes, bytes: descriptor.policy.MaxBytes, depth: descriptor.policy.MaxDepth}
		if err := budget.visit(ctx, spec, 0); err != nil {
			return nil, err
		}
		indexes, err = buildSelectorIndexes(ctx, spec, descriptor.policy.Lists)
		if err != nil {
			return nil, err
		}
	}
	validated := make([]string, 0, len(patches)+1)
	leaves := make(map[string]bool, len(patches))
	for i, patch := range patches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		segments, _ := ParsePath(patch.FieldPath)
		leaf, err := resolveTextLeaf(spec, segments, indexes)
		if err != nil {
			return nil, patchError(i, err)
		}
		if leaves[leaf] {
			return nil, invalid(fmt.Sprintf("spec.patches[%d].fieldPath", i), "OverlappingPaths", "multiple addresses resolve to the same text leaf")
		}
		leaves[leaf] = true
		validated = append(validated, patch.FieldPath)
	}
	if len(body) != 0 {
		validated = append(validated, "{body}")
	}
	return validated, nil
}

type selectedMember struct {
	value     map[string]any
	position  int
	ambiguous bool
}

type selectorIndexes map[string]map[string]selectedMember

func buildSelectorIndexes(ctx context.Context, spec map[string]any, rules []selectorRule) (selectorIndexes, error) {
	indexes := make(selectorIndexes, len(rules))
	for _, rule := range rules {
		list, _ := spec[strings.TrimPrefix(rule.Path, "spec.")].([]any)
		index := make(map[string]selectedMember, len(list))
		identityPath := strings.Split(rule.IdentityPath, ".")
		for position, value := range list {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			member, ok := value.(map[string]any)
			if !ok {
				continue
			}
			var identity any = member
			for _, property := range identityPath {
				object, _ := identity.(map[string]any)
				identity = object[property]
			}
			name, ok := identity.(string)
			if !ok || name == "" {
				continue
			}
			_, duplicate := index[name]
			index[name] = selectedMember{value: member, position: position, ambiguous: duplicate}
		}
		indexes[rule.Path] = index
	}
	return indexes, nil
}

func resolveTextLeaf(spec map[string]any, segments []Segment, indexes selectorIndexes) (string, error) {
	var value any = spec
	pattern, leaf := "spec", "spec"
	for _, segment := range segments {
		object, ok := value.(map[string]any)
		if !ok {
			return "", invalid("fieldPath", "FieldMissing", "expected an existing text leaf")
		}
		pattern += "." + segment.Property
		leaf += "." + segment.Property
		value = object[segment.Property]
		if segment.Identity != "" {
			member, exists := indexes[pattern][segment.Identity]
			if !exists {
				return "", invalid("fieldPath", "FieldMissing", "selected member does not exist")
			}
			if member.ambiguous {
				return "", invalid("fieldPath", "AmbiguousSelector", "selector matches multiple members")
			}
			value = member.value
			pattern += "{}"
			leaf += fmt.Sprintf("[%d]", member.position)
		}
	}
	if _, ok := value.(string); !ok {
		return "", invalid("fieldPath", "FieldMissing", "expected an existing text leaf")
	}
	return leaf, nil
}

type targetBudget struct{ nodes, bytes, depth int }

func (b *targetBudget) visit(ctx context.Context, value any, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.nodes--
	if b.nodes < 0 || depth > b.depth {
		return invalid("target", "TargetWorkUnavailable", "target exceeds the bounded validation work budget")
	}
	switch value := value.(type) {
	case map[string]any:
		if len(value) > b.nodes {
			return invalid("target", "TargetWorkUnavailable", "target exceeds the bounded validation work budget")
		}
		for key, child := range value {
			b.bytes -= len(key)
			if b.bytes < 0 {
				return invalid("target", "TargetWorkUnavailable", "target exceeds the bounded validation work budget")
			}
			if err := b.visit(ctx, child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if len(value) > b.nodes {
			return invalid("target", "TargetWorkUnavailable", "target exceeds the bounded validation work budget")
		}
		for _, child := range value {
			if err := b.visit(ctx, child, depth+1); err != nil {
				return err
			}
		}
	case string:
		b.bytes -= len(value)
	case json.Number:
		b.bytes -= len(value)
	case nil, bool, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
	default:
		return invalid("target", "TargetWorkUnavailable", "target snapshot is not a supported JSON structure")
	}
	if b.bytes < 0 {
		return invalid("target", "TargetWorkUnavailable", "target exceeds the bounded validation work budget")
	}
	return nil
}
