//go:build !linux && !darwin && !windows

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

import "context"

// Unsupported platforms retain portable Go runtime signals. Process readings
// are collected by platform implementations without inventing CPU/RSS values.
func readProcessMetrics(context.Context) (map[string]float64, error) { return nil, nil }
