// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package status

import (
	"strings"
)

const currentResourceVersionPrefix = "current resourceVersion is "

// conflictResourceVersion returns the version supplied by either the legacy
// top-level extension or the standard admission diagnostic envelope. The API
// uses the latter so status-client retry logs retain the useful current value.
func conflictResourceVersion(extensions map[string]any) string {
	if resourceVersion, ok := extensions["resourceVersion"].(string); ok && resourceVersion != "" {
		return resourceVersion
	}

	diagnostics, ok := extensions["diagnostics"].([]any)
	if !ok {
		return "<unknown>"
	}
	for _, diagnostic := range diagnostics {
		entry, ok := diagnostic.(map[string]any)
		if !ok || entry["reason"] != "RESOURCE_VERSION_CONFLICT" {
			continue
		}
		message, ok := entry["message"].(string)
		if !ok {
			continue
		}
		if resourceVersion, found := strings.CutPrefix(message, currentResourceVersionPrefix); found && resourceVersion != "" {
			return resourceVersion
		}
	}
	return "<unknown>"
}
