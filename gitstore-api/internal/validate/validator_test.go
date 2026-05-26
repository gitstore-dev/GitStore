// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package validate

import (
	"embed"
	"testing"

	"github.com/adrg/frontmatter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/*
var testFS embed.FS

func TestExtractFrontmatter(t *testing.T) {
	var matter struct {
		Kind     string         `yaml:"kind" toml:"kind" json:"kind"`
		Metadata map[string]any `yaml:"metadata" toml:"metadata" json:"metadata"`
	}

	t.Run("valid frontmatter", func(t *testing.T) {
		reader, err := testFS.Open("testdata/macbook-pro-64gb-1tb-ssd-m4.md")
		require.NoError(t, err)
		rest, err := frontmatter.Parse(reader, &matter)
		require.NoError(t, err)
		assert.NotEmpty(t, rest)
		assert.Equal(t, "Product", matter.Kind)
		assert.Equal(t, map[string]any{
			"labels": map[any]any{"gitstore.dev/brand": "Apple", "gitstore.dev/vendor": "Apple"},
			"name":   "macbook-pro-64gb-1tb-ssd-m4", "namespace": "ensi-store",
		}, matter.Metadata)
	})

}
