// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

import (
	"context"
	"io"
	"os"
	"time"

	"go.opentelemetry.io/otel/metric"
)

func registerProcessMetrics(meter metric.Meter) error {
	type signal struct {
		name, unit string
		counter    metric.Float64ObservableCounter
		gauge      metric.Float64ObservableGauge
	}
	signals := []signal{
		{name: "process_cpu_seconds_total", unit: "s"}, {name: "process_resident_memory_bytes", unit: "By"}, {name: "process_virtual_memory_bytes", unit: "By"},
		{name: "process_start_time_seconds", unit: "s"}, {name: "process_open_fds", unit: "{file}"}, {name: "process_max_fds", unit: "{file}"},
		{name: "process_virtual_memory_max_bytes", unit: "By"}, {name: "process_network_receive_bytes_total", unit: "By"}, {name: "process_network_transmit_bytes_total", unit: "By"},
	}
	var instruments []metric.Observable
	for i := range signals {
		s := &signals[i]
		if s.name == "process_cpu_seconds_total" || s.name == "process_network_receive_bytes_total" || s.name == "process_network_transmit_bytes_total" {
			s.counter = MustInstrument(meter.Float64ObservableCounter(s.name, metric.WithUnit(s.unit)))
			instruments = append(instruments, s.counter)
		} else {
			s.gauge = MustInstrument(meter.Float64ObservableGauge(s.name, metric.WithUnit(s.unit)))
			instruments = append(instruments, s.gauge)
		}
	}
	_, err := meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		values, err := readProcessMetrics(ctx)
		if err != nil {
			return err
		}
		for _, s := range signals {
			if value, ok := values[s.name]; ok {
				if s.counter != nil {
					observer.ObserveFloat64(s.counter, value)
				} else {
					observer.ObserveFloat64(s.gauge, value)
				}
			}
		}
		return nil
	}, instruments...)
	return err
}

// Count descriptors in fixed-size batches instead of retaining all names. A
// collection deadline bounds work even when the process has many descriptors.
func openFDCount(ctx context.Context, path string) (float64, error) {
	directory, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer directory.Close()
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		names, err := directory.Readdirnames(256)
		count += len(names)
		if err == io.EOF {
			return float64(count - 1), nil
		}
		if err != nil {
			return 0, err
		}
	}
}
