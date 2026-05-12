// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"testing"
)

// TestTagPushPublishesToGraphQL covered contract C-003 (git push → catalog sync).
// As of feature 006 the gitstore-api reads from its own Datastore instead of
// the git repository, so a tag push no longer automatically populates the
// GraphQL catalog. The git→datastore ingestion pipeline is tracked separately.
// This test is skipped until that pipeline is implemented.
func TestTagPushPublishesToGraphQL(t *testing.T) {
	t.Skip("git→datastore ingestion not yet implemented (post-006 work)")
}
