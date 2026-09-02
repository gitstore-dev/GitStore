// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package serviceaccountassertion

import (
	"crypto/sha256"
	"encoding/hex"
)

// assertionReplayDigest keeps a raw JTI out of datastore keys and logs while
// retaining one stable, fixed-width key for the assertion's replay window.
func assertionReplayDigest(jti string) string {
	sum := sha256.Sum256([]byte(jti))
	return hex.EncodeToString(sum[:])
}
