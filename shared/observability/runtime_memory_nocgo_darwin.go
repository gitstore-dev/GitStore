//go:build darwin && !cgo

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

// Match the former collector: current RSS is unavailable without the Mach API;
// do not substitute peak RSS or Go heap bytes for physical resident memory.
func darwinMemory() (float64, float64, bool) { return 0, 0, false }
