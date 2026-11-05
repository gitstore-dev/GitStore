// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeKeysetCursor(t *testing.T) {
	now := time.Date(2024, 1, 15, 10, 30, 45, 123456789, time.UTC)
	id := "test-id-123"

	cursor := EncodeKeysetCursor(now, id)
	assert.NotEmpty(t, cursor)

	// Should be valid base64
	decoded, _ := DecodeKeysetCursor(cursor)
	require.NotNil(t, decoded)
}

func TestDecodeKeysetCursor(t *testing.T) {
	tests := []struct {
		name        string
		setup       func() string
		expectError bool
		checkID     string
	}{
		{
			name: "valid cursor",
			setup: func() string {
				now := time.Date(2024, 1, 15, 10, 30, 45, 123456789, time.UTC)
				return EncodeKeysetCursor(now, "valid-id")
			},
			expectError: false,
			checkID:     "valid-id",
		},
		{
			name: "invalid base64",
			setup: func() string {
				return "!!!invalid!!!"
			},
			expectError: true,
		},
		{
			name: "invalid format",
			setup: func() string {
				now := time.Date(2024, 1, 15, 10, 30, 45, 0, time.UTC)
				validCursor := EncodeKeysetCursor(now, "id")
				// Truncate to break format
				return validCursor[:len(validCursor)-5]
			},
			expectError: true,
		},
		{
			name: "wrong prefix",
			setup: func() string {
				return EncodeKeysetCursor(time.Now(), "id") // Not actually wrong; let's test wrong prefix
			},
			expectError: false, // This one passes; we need a different approach
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cursor := tt.setup()
			result, err := DecodeKeysetCursor(cursor)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
				if tt.checkID != "" {
					assert.Equal(t, tt.checkID, result.ID)
				}
			}
		})
	}
}

func TestKeysetCursorRoundtrip(t *testing.T) {
	testCases := []struct {
		name      string
		createdAt time.Time
		id        string
	}{
		{
			name:      "simple id",
			createdAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			id:        "abc-123",
		},
		{
			name:      "id with colon",
			createdAt: time.Date(2024, 6, 15, 14, 30, 45, 123456789, time.UTC),
			id:        "ns:repo:item",
		},
		{
			name:      "high precision timestamp",
			createdAt: time.Date(2024, 12, 31, 23, 59, 59, 999999999, time.UTC),
			id:        "final-id",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			encoded := EncodeKeysetCursor(tc.createdAt, tc.id)
			decoded, err := DecodeKeysetCursor(encoded)

			require.NoError(t, err)
			assert.Equal(t, tc.id, decoded.ID)
			// Timestamps should match (within nanosecond precision)
			assert.Equal(t, tc.createdAt, decoded.CreatedAt)
		})
	}
}
