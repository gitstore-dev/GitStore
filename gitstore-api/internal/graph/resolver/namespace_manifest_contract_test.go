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

	for _, section := range []string{"Create manifest", "Update manifest"} {
		block, ok := firstFencedCodeBlockInSection(text, section)
		require.Truef(t, ok, "documentation must contain a manifest example in the %q section", section)

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
		require.NoError(t, yaml.Unmarshal([]byte(block), &manifest), "%s manifest example must be valid YAML", section)
		assert.Equal(t, "gitstore.dev/v1beta1", manifest.APIVersion)
		assert.Equal(t, "Namespace", manifest.Kind)
		assert.NotEmpty(t, manifest.Metadata.Name)
		assert.NotEmpty(t, manifest.Spec.Tier)
		assert.Empty(t, manifest.Metadata.UID, "author examples must omit system metadata")
		assert.Nil(t, manifest.Status, "author examples must omit status")
	}
}

func TestNamespaceDeletionOutcomeSchemaContract(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	schemaPath := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "..", "shared", "schemas", "namespace.graphqls")
	content, err := os.ReadFile(schemaPath)
	require.NoError(t, err)

	text := string(content)
	assert.Contains(t, text, "enum NamespaceDeletionOutcome {")
	assert.Contains(t, text, "TERMINATION_STARTED")
	assert.Contains(t, text, "ALREADY_TERMINATING")
	assert.Regexp(t, `(?s)type DeleteNamespacePayload\s*\{.*outcome:\s*NamespaceDeletionOutcome!`, text)
}

func firstFencedCodeBlockInSection(markdown, heading string) (string, bool) {
	sectionStart := strings.Index(markdown, "## "+heading+"\n")
	if sectionStart < 0 {
		return "", false
	}

	section := markdown[sectionStart+len("## "+heading):]
	if nextSection := strings.Index(section, "\n## "); nextSection >= 0 {
		section = section[:nextSection]
	}

	blockStart := strings.Index(section, "```")
	if blockStart < 0 {
		return "", false
	}
	block := section[blockStart+len("```"):]
	if newline := strings.IndexByte(block, '\n'); newline >= 0 {
		block = block[newline+1:]
	}
	blockEnd := strings.Index(block, "```")
	if blockEnd < 0 {
		return "", false
	}

	return strings.TrimSpace(strings.TrimPrefix(block[:blockEnd], "---")), true
}
