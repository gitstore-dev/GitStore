// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamespaceCursorRoundTrip(t *testing.T) {
	epoch := "018f47d2-cd4b-7a11-9c35-4b4c423d56cb"
	for _, sequence := range []uint64{0, 1, 35, 36, 4096, 100000} {
		raw := EncodeCursor(epoch, sequence)
		cursor, err := ParseCursor(raw)
		require.NoError(t, err)
		assert.Equal(t, epoch, cursor.Epoch)
		assert.Equal(t, sequence, cursor.Sequence)
		assert.Equal(t, raw, cursor.String())
	}
}

func TestNamespaceCursorRejectsOtherKindsAndMalformedValues(t *testing.T) {
	for _, raw := range []string{
		"prv1:018f47d2-cd4b-7a11-9c35-4b4c423d56cb:1",
		"nwv2:018f47d2-cd4b-7a11-9c35-4b4c423d56cb:1",
		"nwv1:not-a-uuid:1",
		"nwv1:018f47d2-cd4b-7a11-9c35-4b4c423d56cb:-1",
		"nwv1:018f47d2-cd4b-7a11-9c35-4b4c423d56cb:",
	} {
		_, err := ParseCursor(raw)
		require.Error(t, err, raw)
	}
}

func TestNamespaceCursorOrdersBySequenceInsideEpoch(t *testing.T) {
	epoch := "018f47d2-cd4b-7a11-9c35-4b4c423d56cb"
	assert.True(t, Cursor{Epoch: epoch, Sequence: 37}.After(Cursor{Epoch: epoch, Sequence: 36}))
	assert.False(t, Cursor{Epoch: epoch, Sequence: 36}.After(Cursor{Epoch: epoch, Sequence: 36}))
	assert.False(t, Cursor{Epoch: "018f47d2-cd4b-7a11-9c35-4b4c423d56cc", Sequence: 37}.After(Cursor{Epoch: epoch, Sequence: 36}))
}

func TestBootstrapSentinelIsNotAnExternalCursor(t *testing.T) {
	assert.Equal(t, "__namespace_watch_bootstrap__", BootstrapCursor)
	_, err := ParseCursor(BootstrapCursor)
	require.Error(t, err)
}
