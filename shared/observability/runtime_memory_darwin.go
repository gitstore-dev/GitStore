//go:build darwin && cgo

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

/*
#include <mach/mach.h>
static int gitstore_process_memory(unsigned long long *resident, unsigned long long *virtual_size) {
    mach_task_basic_info_data_t info;
    mach_msg_type_number_t count = MACH_TASK_BASIC_INFO_COUNT;
    kern_return_t result = task_info(mach_task_self(), MACH_TASK_BASIC_INFO, (task_info_t)&info, &count);
    if (result != KERN_SUCCESS) return 0;
    *resident = info.resident_size;
    *virtual_size = info.virtual_size;
    return 1;
}
*/
import "C"

func darwinMemory() (float64, float64, bool) {
	var rss, virtual C.ulonglong
	ok := C.gitstore_process_memory(&rss, &virtual)
	return float64(rss), float64(virtual), ok != 0
}
