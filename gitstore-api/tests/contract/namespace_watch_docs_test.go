// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNamespaceWatchDocumentationContract(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(current), "..", "..", "..", "docs", "namespace", "namespace-watch.md")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Namespace watch documentation: %v", err)
	}
	doc := string(contents)
	for _, required := range []string{
		"watchNamespaces", "__namespace_watch_bootstrap__", "ListNamespaces", "ResumeNamespaces",
		"WATCH_EXPIRED", "WATCH_UNAVAILABLE", "RETENTION_EXPIRED", "EPOCH_MISMATCH",
		"INCOMPATIBLE_CURSOR", "INVALID_CURSOR", "REPLAY_LIMIT", "SUBSCRIBER_OVERFLOW",
		"JOURNAL_DISCONTINUITY", "ADDED", "MODIFIED", "DELETED", "BOOKMARK",
		"Terminating", "finalizers", "at-least-once", "namespace.watch",
		"watchResources(kind: \"Namespace\")", "deletionTimestamp", "spec 047",
	} {
		if !strings.Contains(doc, required) {
			t.Errorf("documentation is missing %q", required)
		}
	}
}
