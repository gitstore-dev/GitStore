// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package translation

import (
	"strings"
	"testing"
)

func TestCanonicalLocale(t *testing.T) {
	for _, value := range []string{
		"de-DE", "fr-FR", "zh-Hant-TW", "sl-rozaj-biske-1994", "de-CH-1901",
		"en-US-u-ca-gregory", "en-a-foo-b-bar-x-private", "x-private", "i-klingon",
		"en-GB-oed", "sgn-BE-FR", "art-lojban", "zzzz-Qaaa-QM", "qzz-Latn-ZZ",
	} {
		t.Run(value, func(t *testing.T) {
			got, err := CanonicalLocale(value)
			if err != nil || got == "" {
				t.Fatalf("well-formed locale rejected: %q, %v", got, err)
			}
			again, err := CanonicalLocale(strings.ToUpper(value))
			if err != nil || got != again {
				t.Fatalf("case changed comparison key: %q != %q (%v)", got, again, err)
			}
		})
	}
	unknown, err := CanonicalLocale("qzz-Qaaa-QM-x-Original")
	if err != nil || unknown != "qzz-qaaa-qm-x-original" {
		t.Fatalf("unknown subtags were lost: %q, %v", unknown, err)
	}
	for _, value := range []string{
		"", " ", " de-DE", "de-DE ", "en_US", "a", "123", "en--US", "en-",
		"en-abcdefghi", "en-1901-1901", "en-a-foo-a-bar", "en-u", "en-x",
		"en-a-x-private", "x", "en-1", "en-\u00e9", "en-US-Latn",
		"en-\u212a\u212a\u212a\u212a", "x-\u212a", strings.Repeat("a", 256),
	} {
		t.Run("invalid/"+value, func(t *testing.T) {
			if _, err := CanonicalLocale(value); err == nil {
				t.Fatal("malformed locale accepted")
			}
		})
	}
	for _, pair := range [][2]string{{"iw-IL", "he-IL"}, {"i-klingon", "tlh"}, {"de-de", "de-DE"}} {
		left, err := CanonicalLocale(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		right, err := CanonicalLocale(pair[1])
		if err != nil || left != right {
			t.Fatalf("canonical aliases differ: %q / %q (%v)", left, right, err)
		}
	}
}
