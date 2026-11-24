// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package validate

import (
	"strings"
	"unicode"
	"unicode/utf8"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/translation"
)

// TranslationValidationErrors preserves typed findings without splitting their
// messages or locations as if they were joined, unstructured validator errors.
func TranslationValidationErrors(file string, err error) ([]*catalogv1.ValidationError, bool) {
	const limit = 32
	var findings []*catalogv1.ValidationError
	visited, truncated := 0, false
	var visit func(error)
	visit = func(err error) {
		if err == nil {
			return
		}
		if len(findings) > limit || visited >= 256 {
			truncated = true
			return
		}
		visited++
		if issue, ok := err.(*translation.Error); ok {
			if issue != nil {
				findings = append(findings, &catalogv1.ValidationError{
					FilePath:   boundedTranslationDiagnostic(file, 256),
					Field:      boundedTranslationDiagnostic(issue.Field, 256),
					Constraint: "translation/" + issue.Reason,
					Message:    boundedTranslationDiagnostic(issue.Message, 256),
				})
			}
			return
		}
		switch wrapped := err.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range wrapped.Unwrap() {
				visit(child)
				if len(findings) > limit || visited >= 256 {
					truncated = true
					break
				}
			}
		case interface{ Unwrap() error }:
			visit(wrapped.Unwrap())
		}
	}
	visit(err)
	if len(findings) == 0 {
		return nil, false
	}
	if len(findings) > limit {
		findings, truncated = findings[:limit], true
	}
	if truncated {
		const suffix = "; additional issues omitted"
		last := findings[len(findings)-1]
		last.Message = boundedTranslationDiagnostic(last.Message, 256-len(suffix)) + suffix
	}
	return findings, true
}

func boundedTranslationDiagnostic(value string, limit int) string {
	suffix := ""
	if len(value) > limit {
		end := limit - 3
		for end > 0 && !utf8.RuneStart(value[end]) {
			end--
		}
		value, suffix = value[:end], "..."
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return '?'
		}
		return r
	}, value) + suffix
}
