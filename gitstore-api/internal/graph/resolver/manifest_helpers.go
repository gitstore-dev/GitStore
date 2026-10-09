// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"bytes"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"gopkg.in/yaml.v3"
)

// renderManifest renders a Markdown manifest: YAML frontmatter followed by the
// body. Map keys are sorted by the encoder, so equal input renders equal bytes.
func renderManifest(envelope map[string]any, body []byte) ([]byte, error) {
	frontmatter, err := yaml.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	content := append([]byte("---\n"), frontmatter...)
	content = append(content, "---\n"...)
	return append(content, body...), nil
}

// markdownBody returns the Markdown body following a manifest's frontmatter,
// or nil when content has no frontmatter.
func markdownBody(content []byte) []byte {
	lines := bytes.Split(content, []byte("\n"))
	if len(lines) < 3 || !bytes.Equal(bytes.TrimSpace(lines[0]), []byte("---")) {
		return nil
	}
	for index := 1; index < len(lines); index++ {
		if bytes.Equal(bytes.TrimSpace(lines[index]), []byte("---")) {
			return bytes.Join(lines[index+1:], []byte("\n"))
		}
	}
	return nil
}

// convergeCommittedResource accepts the record a committed admission produced
// only when it came from that commit, or when admission reported no change so
// the stored record legitimately carries an earlier identical commit. Any other
// record belongs to a concurrent writer and is reported as superseded.
func convergeCommittedResource[T any](result *admission.CommittedManifestResult, load func() (T, string, error)) (T, error) {
	var zero T
	record, commitSHA, err := load()
	if err != nil {
		return zero, err
	}
	if result == nil || (commitSHA != result.CommitSHA && !result.NoOp) {
		return zero, admission.ErrCommittedManifestSuperseded
	}
	return record, nil
}
