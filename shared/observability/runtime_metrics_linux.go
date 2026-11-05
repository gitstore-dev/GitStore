//go:build linux

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

import (
	"context"
	"os"

	"github.com/prometheus/procfs"
)

func readProcessMetrics(ctx context.Context) (map[string]float64, error) {
	process, err := procfs.NewProc(os.Getpid())
	if err != nil {
		return nil, err
	}
	stat, err := process.Stat()
	if err != nil {
		return nil, err
	}
	values := map[string]float64{"process_cpu_seconds_total": stat.CPUTime(), "process_resident_memory_bytes": float64(stat.ResidentMemory()), "process_virtual_memory_bytes": float64(stat.VirtualMemory())}
	if start, err := stat.StartTime(); err == nil {
		values["process_start_time_seconds"] = start
	}
	if fds, err := openFDCount(ctx, "/proc/self/fd"); err == nil {
		values["process_open_fds"] = fds
	}
	if limits, err := process.Limits(); err == nil {
		values["process_max_fds"] = float64(limits.OpenFiles)
		values["process_virtual_memory_max_bytes"] = float64(limits.AddressSpace)
	}
	if netstat, err := process.Netstat(); err == nil {
		if netstat.InOctets != nil {
			values["process_network_receive_bytes_total"] = *netstat.InOctets
		}
		if netstat.OutOctets != nil {
			values["process_network_transmit_bytes_total"] = *netstat.OutOctets
		}
	}
	return values, nil
}
