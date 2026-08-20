// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestNamespaceDocumentationManifestContract(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	docPath := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "..", "docs", "namespace", "namespace-spec.md")
	content, err := os.ReadFile(docPath)
	require.NoError(t, err)

	text := string(content)
	for _, conditionType := range []string{"Ready", "AdmissionAccepted", "SystemRepoReady", "Terminating"} {
		assert.Contains(t, text, "`"+conditionType+"`")
	}

	blocks := fencedYAMLBlocks(text)
	require.GreaterOrEqual(t, len(blocks), 2, "documentation must contain create and update YAML examples")
	for _, block := range blocks[:2] {
		var manifest struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Metadata   struct {
				Name        string            `yaml:"name"`
				Labels      map[string]string `yaml:"labels"`
				Annotations map[string]string `yaml:"annotations"`
				UID         string            `yaml:"uid"`
			} `yaml:"metadata"`
			Spec struct {
				Tier string `yaml:"tier"`
			} `yaml:"spec"`
			Status any `yaml:"status"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(block), &manifest))
		assert.Equal(t, "gitstore.dev/v1beta1", manifest.APIVersion)
		assert.Equal(t, "Namespace", manifest.Kind)
		assert.NotEmpty(t, manifest.Metadata.Name)
		assert.NotEmpty(t, manifest.Spec.Tier)
		assert.Empty(t, manifest.Metadata.UID, "author examples must omit system metadata")
		assert.Nil(t, manifest.Status, "author examples must omit status")
	}
}

func fencedYAMLBlocks(markdown string) []string {
	parts := strings.Split(markdown, "```yaml")
	blocks := make([]string, 0, len(parts)-1)
	for _, part := range parts[1:] {
		end := strings.Index(part, "```")
		if end < 0 {
			continue
		}
		blocks = append(blocks, strings.TrimSpace(strings.TrimPrefix(part[:end], "---")))
	}
	return blocks
}
