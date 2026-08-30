// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import "testing"

func TestParseConfigFile(t *testing.T) {
	path, err := parseConfigFile([]string{"--config-file", "/config/shared.toml"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/config/shared.toml" {
		t.Fatalf("path = %q", path)
	}
}
