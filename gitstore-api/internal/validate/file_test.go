// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package validate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseFileResource(t *testing.T) {
	doc := `---
apiVersion: storage.gitstore.dev/v1beta1
kind: File
metadata:
  namespace: gitstore
  name: product-hero
spec:
  contentType: image/jpeg
  provider: s3
  source:
    uri: s3://bucket/key
---
Product hero image`
	parsed, body, err := NewParser().ParseResource(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, parsed.File)
	require.Equal(t, "Product hero image", string(body))
	require.Equal(t, "image/jpeg", parsed.File.Spec.ContentType)
	require.Equal(t, "s3", parsed.File.Spec.Provider)
}

func TestFileProviderContract(t *testing.T) {
	for _, tc := range []struct {
		name, provider, sourceField string
		valid                       bool
	}{
		{"git", "git", "", true},
		{"lfs", "lfs", "", true},
		{"s3", "s3", "", true},
		{"gcs", "gcs", "", true},
		{"missing", "", "", false},
		{"empty", `""`, "", false},
		{"null", "null", "", false},
		{"unknown", "ftp", "", false},
		{"old-selector-only", "", "    type: s3\n", false},
		{"old-selector-with-provider", "s3", "    type: s3\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := ""
			if tc.provider != "" {
				provider = "  provider: " + tc.provider + "\n"
			}
			doc := fmt.Sprintf(`---
apiVersion: storage.gitstore.dev/v1beta1
kind: File
metadata:
  namespace: gitstore
  name: hero
spec:
  contentType: image/jpeg
  type: gitstore.dev/media
%s  source:
%s    uri: media/hero.jpg
---
`, provider, tc.sourceField)
			parsed, _, err := NewParser().ParseResource(strings.NewReader(doc))
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.provider, parsed.File.Spec.Provider)
			require.Equal(t, "gitstore.dev/media", parsed.File.Spec.Type)
			require.Equal(t, "media/hero.jpg", parsed.File.Spec.Source.URI)
		})
	}
}

func TestParseFileRejectsAuthorStatusAndInvalidSource(t *testing.T) {
	doc := `---
apiVersion: storage.gitstore.dev/v1beta1
kind: File
metadata:
  namespace: gitstore
  name: bad
spec:
  contentType: image/jpeg
  provider: ftp
  source:
    uri: ""
status: {}
---`
	_, _, err := NewParser().ParseResource(strings.NewReader(doc))
	require.Error(t, err)
	require.Contains(t, err.Error(), "status is system-managed")
}
