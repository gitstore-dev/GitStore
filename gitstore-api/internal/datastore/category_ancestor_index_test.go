// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClosureCursorRoundTripAndModeMismatch(t *testing.T) {
	cursor := EncodeClosureCursor(2, "laptops")
	depth, name, err := DecodeClosureCursor(cursor)
	require.NoError(t, err)
	assert.Equal(t, 2, depth)
	assert.Equal(t, "laptops", name)

	keyset := base64.StdEncoding.EncodeToString([]byte("keyset|2026-01-01T00:00:00Z|id"))
	_, _, err = DecodeClosureCursor(keyset)
	assert.ErrorIs(t, err, ErrInvalidArgument)
	assert.False(t, IsClosureCursor(keyset))
	assert.ErrorIs(t, RejectClosureCursors(PageParams{After: cursor}), ErrInvalidArgument)
	assert.NoError(t, RejectClosureCursors(PageParams{After: keyset}))
}

func TestCategoryAncestorRowsDerivation(t *testing.T) {
	rows, err := CategoryAncestorRows("laptops", []string{"electronics", "computers", "laptops"})
	require.NoError(t, err)
	assert.Equal(t, []CategoryAncestorRow{{"electronics", 2}, {"computers", 1}, {"laptops", 0}}, rows)

	none, err := CategoryAncestorRows("x", nil)
	require.NoError(t, err)
	assert.Empty(t, none)

	moved, err := CategoryAncestorRows("laptops", []string{"electronics", "laptops"})
	require.NoError(t, err)
	assert.Equal(t, []CategoryAncestorRow{{"electronics", 2}, {"computers", 1}}, ObsoleteCategoryAncestorRows(rows, moved))

	tooDeep := make([]string, MaxCategoryHierarchyDepth+2)
	for i := range tooDeep {
		tooDeep[i] = "c"
	}
	_, err = CategoryAncestorRows("c", tooDeep)
	assert.ErrorIs(t, err, ErrInvalidArgument)
}
