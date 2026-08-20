// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import "testing"

func TestNextResourceVersion(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		current string
		want    string
	}{
		"empty":         {current: "", want: "1"},
		"invalid":       {current: "invalid", want: "1"},
		"zero":          {current: "0", want: "1"},
		"negative":      {current: "-1", want: "1"},
		"initial":       {current: "1", want: "2"},
		"large integer": {current: "9223372036854775808", want: "9223372036854775809"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := nextResourceVersion(test.current); got != test.want {
				t.Fatalf("nextResourceVersion(%q) = %q, want %q", test.current, got, test.want)
			}
		})
	}
}
