// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Keyset cursor encoding and decoding for stable, opaque Relay pagination

package graph

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// KeysetCursor represents a stable keyset position for pagination.
// It encodes createdAt + id so we can implement keyset/seek pagination in both memdb and ScyllaDB.
type KeysetCursor struct {
	CreatedAt time.Time
	ID        string
}

// EncodeKeysetCursor returns an opaque base64-encoded cursor from a keyset position.
func EncodeKeysetCursor(createdAt time.Time, id string) string {
	// Format: "keyset|RFC3339_TIMESTAMP|ID" (using | as separator to avoid timestamp colons)
	payload := fmt.Sprintf("keyset|%s|%s", createdAt.Format(time.RFC3339Nano), id)
	return base64.StdEncoding.EncodeToString([]byte(payload))
}

// DecodeKeysetCursor decodes an opaque cursor into createdAt and id.
// Returns an error if the cursor is invalid.
func DecodeKeysetCursor(cursor string) (*KeysetCursor, error) {
	decoded, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 encoding: %w", err)
	}

	parts := strings.SplitN(string(decoded), "|", 3)
	if len(parts) != 3 || parts[0] != "keyset" {
		return nil, fmt.Errorf("invalid cursor format: expected 'keyset|TIMESTAMP|ID'")
	}

	createdAt, err := time.Parse(time.RFC3339Nano, parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid cursor timestamp: %w", err)
	}

	return &KeysetCursor{
		CreatedAt: createdAt,
		ID:        parts[2],
	}, nil
}
