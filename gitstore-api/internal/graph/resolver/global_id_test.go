// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeDecodeNodeID(t *testing.T) {
	tests := []struct {
		kind  string
		rawID string
	}{
		{kind: nodeKindProduct, rawID: "prod-123"},
		{kind: nodeKindCategory, rawID: "cat-123"},
		{kind: nodeKindCollection, rawID: "col-123"},
		{kind: nodeKindNamespace, rawID: "018f64aa-7f55-7d48-8ea5-517b513c01f8"},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			encoded, err := EncodeNodeID(tt.kind, tt.rawID)
			require.NoError(t, err)

			kind, rawID, err := DecodeNodeID(encoded)
			require.NoError(t, err)
			assert.Equal(t, tt.kind, kind)
			assert.Equal(t, tt.rawID, rawID)
		})
	}
}

func TestDecodeNodeIDRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "invalid base64", value: "not-base64!"},
		{name: "wrong scheme", value: encodeRawGlobalID("https://GitStore/Product/123")},
		{name: "wrong host", value: encodeRawGlobalID("gid://Other/Product/123")},
		{name: "missing kind", value: encodeRawGlobalID("gid://GitStore/")},
		{name: "missing raw ID", value: encodeRawGlobalID("gid://GitStore/Product/")},
		{name: "unsupported kind", value: encodeRawGlobalID("gid://GitStore/Order/123")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := DecodeNodeID(tt.value)
			assert.Error(t, err)
		})
	}
}

func TestEncodeNodeIDRejectsInvalidValues(t *testing.T) {
	_, err := EncodeNodeID("Order", "123")
	assert.Error(t, err)

	_, err = EncodeNodeID(nodeKindProduct, "")
	assert.Error(t, err)
}

func encodeRawGlobalID(raw string) string {
	return base64.StdEncoding.EncodeToString([]byte(raw))
}
