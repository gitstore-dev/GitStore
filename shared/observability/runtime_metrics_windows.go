//go:build windows

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

import (
	"context"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PROCESS_MEMORY_COUNTERS_EX is the Windows process-memory API layout.
type windowsMemory struct {
	Size, PageFaults                                                uint32
	PeakWorkingSet, WorkingSet, PeakPagedPool, PagedPool            uintptr
	PeakNonPagedPool, NonPagedPool, Pagefile, PeakPagefile, Private uintptr
}

var processMemoryInfo = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")
var processHandleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

func readProcessMetrics(ctx context.Context) (map[string]float64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	handle := windows.CurrentProcess()
	var start, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &start, &exit, &kernel, &user); err != nil {
		return nil, err
	}
	seconds := func(value windows.Filetime) float64 {
		return float64(uint64(value.HighDateTime)<<32|uint64(value.LowDateTime)) / 1e7
	}
	values := map[string]float64{
		"process_cpu_seconds_total":  seconds(kernel) + seconds(user),
		"process_start_time_seconds": float64(start.Nanoseconds() / 1e9),
		"process_max_fds":            16 * 1024 * 1024,
	}
	memory := windowsMemory{}
	memory.Size = uint32(unsafe.Sizeof(memory))
	if ok, _, err := processMemoryInfo.Call(uintptr(handle), uintptr(unsafe.Pointer(&memory)), uintptr(memory.Size)); ok == 0 {
		return nil, err
	}
	values["process_resident_memory_bytes"] = float64(memory.WorkingSet)
	values["process_virtual_memory_bytes"] = float64(memory.Private)
	var count uint32
	if ok, _, err := processHandleCount.Call(uintptr(handle), uintptr(unsafe.Pointer(&count))); ok == 0 {
		return nil, err
	}
	values["process_open_fds"] = float64(count)
	return values, nil
}
