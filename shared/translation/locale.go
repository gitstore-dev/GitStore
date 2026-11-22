// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package translation

import (
	"strings"

	"golang.org/x/text/language"
)

var grandfathered = map[string]bool{
	"art-lojban": true, "cel-gaulish": true, "en-gb-oed": true,
	"i-ami": true, "i-bnn": true, "i-default": true, "i-enochian": true,
	"i-hak": true, "i-klingon": true, "i-lux": true, "i-mingo": true,
	"i-navajo": true, "i-pwn": true, "i-tao": true, "i-tay": true, "i-tsu": true,
	"no-bok": true, "no-nyn": true, "sgn-be-fr": true, "sgn-be-nl": true,
	"sgn-ch-de": true, "zh-guoyu": true, "zh-hakka": true, "zh-min": true,
	"zh-min-nan": true, "zh-xiang": true,
}

// CanonicalLocale returns a comparison key, never a replacement for raw source.
func CanonicalLocale(raw string) (string, error) {
	if len(raw) == 0 || len(raw) > 255 {
		return "", invalid("spec.locale", "InvalidLocale", "expected a well-formed BCP 47 tag of at most 255 ASCII bytes")
	}
	for i := range len(raw) {
		if raw[i] > 127 {
			return "", invalid("spec.locale", "InvalidLocale", "locale subtags must be ASCII")
		}
	}
	lower := strings.ToLower(raw)
	if !grandfathered[lower] && !wellFormedLocale(strings.Split(lower, "-")) {
		return "", invalid("spec.locale", "InvalidLocale", "expected a well-formed BCP 47 tag")
	}
	tag, err := language.Parse(raw)
	if err == nil {
		return strings.ToLower(tag.String()), nil
	}
	// Registry knowledge is not the grammar: retain unknown well-formed subtags.
	return lower, nil
}

func ascii(s string, min, max int, digits bool) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || digits && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

func numeric(s string) bool {
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

func wellFormedLocale(parts []string) bool {
	if parts[0] == "x" {
		return privateUse(parts[1:])
	}
	if !ascii(parts[0], 2, 8, false) {
		return false
	}
	i := 1
	if len(parts[0]) <= 3 {
		for n := 0; n < 3 && i < len(parts) && ascii(parts[i], 3, 3, false); n++ {
			i++
		}
	}
	if i < len(parts) && ascii(parts[i], 4, 4, false) {
		i++
	}
	if i < len(parts) && (ascii(parts[i], 2, 2, false) || len(parts[i]) == 3 && numeric(parts[i])) {
		i++
	}
	variants := map[string]bool{}
	for i < len(parts) && (ascii(parts[i], 5, 8, true) ||
		len(parts[i]) == 4 && parts[i][0] >= '0' && parts[i][0] <= '9' && ascii(parts[i], 4, 4, true)) {
		if variants[parts[i]] {
			return false
		}
		variants[parts[i]] = true
		i++
	}
	singletons := map[string]bool{}
	for i < len(parts) {
		if parts[i] == "x" {
			return privateUse(parts[i+1:])
		}
		if !ascii(parts[i], 1, 1, true) || singletons[parts[i]] {
			return false
		}
		singletons[parts[i]] = true
		i++
		start := i
		for i < len(parts) && ascii(parts[i], 2, 8, true) {
			i++
		}
		if start == i {
			return false
		}
	}
	return true
}

func privateUse(parts []string) bool {
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if !ascii(part, 1, 8, true) {
			return false
		}
	}
	return true
}
