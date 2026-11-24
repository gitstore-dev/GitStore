// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package translation

import (
	"fmt"
	"strings"
)

type Segment struct {
	Property string
	Identity string
}

func identifier(s string, property bool) bool {
	if s == "" {
		return false
	}
	for i, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' {
			continue
		}
		if (!property || i > 0) && c >= '0' && c <= '9' || !property && c == '-' {
			continue
		}
		return false
	}
	return true
}

func ParsePath(path string) ([]Segment, error) {
	bad := func() ([]Segment, error) {
		return nil, invalid("fieldPath", "InvalidPath", "expected a bounded spec-rooted dot path with optional named brace selectors")
	}
	if len(path) > MaxPathBytes || !strings.HasPrefix(path, "spec.") {
		return bad()
	}
	parts := strings.Split(strings.TrimPrefix(path, "spec."), ".")
	if len(parts) > MaxPathSegments {
		return bad()
	}
	result := make([]Segment, 0, len(parts))
	selectors := 0
	for _, part := range parts {
		segment := Segment{Property: part}
		if at := strings.IndexByte(part, '{'); at >= 0 {
			if !strings.HasSuffix(part, "}") {
				return bad()
			}
			segment.Property, segment.Identity = part[:at], part[at+1:len(part)-1]
			if !identifier(segment.Identity, false) {
				return bad()
			}
			selectors++
		}
		if !identifier(segment.Property, true) || selectors > MaxSelectors {
			return bad()
		}
		result = append(result, segment)
	}
	return result, nil
}

func ValidatePaths(paths []string) error {
	_, err := validatePaths(paths)
	return err
}

func validatePaths(paths []string) (int, error) {
	if len(paths) > MaxPatches {
		return 0, invalid("spec.patches", "LimitExceeded", "at most 256 paths are permitted")
	}
	parsed := make([][]Segment, 0, len(paths))
	for i, path := range paths {
		segments, err := ParsePath(path)
		if err != nil {
			return i, patchError(i, err)
		}
		for _, previous := range parsed {
			if overlapping(previous, segments) {
				return i, invalid(fmt.Sprintf("spec.patches[%d].fieldPath", i), "OverlappingPaths", "duplicate or overlapping content is not permitted")
			}
		}
		parsed = append(parsed, segments)
	}
	return -1, nil
}

func overlapping(a, b []Segment) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i].Property != b[i].Property {
			return false
		}
		if a[i].Identity != b[i].Identity {
			return (a[i].Identity == "" && i == len(a)-1) || (b[i].Identity == "" && i == len(b)-1)
		}
	}
	return true
}
