// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build !darwin && !linux

package cataloggrpc

func namespaceCapacityProcessCPUSeconds() float64 {
	panic("Namespace capacity process CPU measurement is supported only on Darwin and Linux")
}
