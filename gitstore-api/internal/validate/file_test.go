package validate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseFileResource(t *testing.T) {
	doc := `---
apiVersion: storage.gitstore.dev/v1beta1
kind: File
metadata:
  name: product-hero
spec:
  contentType: image/jpeg
  source:
    type: s3
    uri: s3://bucket/key
---
Product hero image`
	parsed, body, err := NewParser().ParseResource(strings.NewReader(doc))
	require.NoError(t, err)
	require.NotNil(t, parsed.File)
	require.Equal(t, "Product hero image", string(body))
	require.Equal(t, "image/jpeg", parsed.File.Spec.ContentType)
}

func TestParseFileRejectsAuthorStatusAndInvalidSource(t *testing.T) {
	doc := `---
apiVersion: storage.gitstore.dev/v1beta1
kind: File
metadata:
  name: bad
spec:
  contentType: image/jpeg
  source:
    type: ftp
    uri: ""
status: {}
---`
	_, _, err := NewParser().ParseResource(strings.NewReader(doc))
	require.Error(t, err)
	require.Contains(t, err.Error(), "status is system-managed")
}
