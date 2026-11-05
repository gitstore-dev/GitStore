//go:build darwin

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

import (
	"context"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func readProcessMetrics(ctx context.Context) (map[string]float64, error) {
	values := map[string]float64{}
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		return nil, err
	}
	values["process_cpu_seconds_total"] = float64(usage.Utime.Nano()+usage.Stime.Nano()) / 1e9
	if process, err := unix.SysctlKinfoProc("kern.proc.pid", os.Getpid()); err == nil {
		values["process_start_time_seconds"] = float64(process.Proc.P_starttime.Nano() / 1e9)
	}
	if fds, err := openFDCount(ctx, "/dev/fd"); err == nil {
		values["process_open_fds"] = fds
	}
	var limits syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limits); err == nil {
		values["process_max_fds"] = float64(limits.Cur)
	}
	if err := syscall.Getrlimit(syscall.RLIMIT_AS, &limits); err == nil {
		values["process_virtual_memory_max_bytes"] = float64(limits.Cur)
	}
	if rss, virtual, ok := darwinMemory(); ok {
		values["process_resident_memory_bytes"] = rss
		values["process_virtual_memory_bytes"] = virtual
	}
	return values, nil
}
