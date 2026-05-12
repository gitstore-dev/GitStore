// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import "time"

// millisToTime converts a Unix-millisecond epoch value stored in ScyllaDB
// timestamp columns back to a time.Time. Zero yields the zero time.
func millisToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
