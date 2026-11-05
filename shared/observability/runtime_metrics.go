// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

import (
	"context"
	"runtime"
	"runtime/debug"
	runtimemetrics "runtime/metrics"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Runtime observations use SDK instruments directly. The former GC summary is
// represented by its same-named cumulative count/sum and five quantile gauges;
// the Metrics API has no summary instrument. No synthetic histogram samples are
// added to reproduce precomputed runtime aggregates.
func registerRuntimeMetrics(provider metric.MeterProvider) error {
	meter := provider.Meter("gitstore/runtime")
	type memoryObservation struct {
		name string
		read func(*runtime.MemStats) float64
	}
	fields := []memoryObservation{
		{"alloc_bytes", func(s *runtime.MemStats) float64 { return float64(s.Alloc) }},
		{"alloc_bytes_total", func(s *runtime.MemStats) float64 { return float64(s.TotalAlloc) }},
		{"sys_bytes", func(s *runtime.MemStats) float64 { return float64(s.Sys) }},
		{"mallocs_total", func(s *runtime.MemStats) float64 { return float64(s.Mallocs) }},
		{"frees_total", func(s *runtime.MemStats) float64 { return float64(s.Frees) }},
		{"heap_alloc_bytes", func(s *runtime.MemStats) float64 { return float64(s.HeapAlloc) }},
		{"heap_sys_bytes", func(s *runtime.MemStats) float64 { return float64(s.HeapSys) }},
		{"heap_idle_bytes", func(s *runtime.MemStats) float64 { return float64(s.HeapIdle) }},
		{"heap_inuse_bytes", func(s *runtime.MemStats) float64 { return float64(s.HeapInuse) }},
		{"heap_released_bytes", func(s *runtime.MemStats) float64 { return float64(s.HeapReleased) }},
		{"heap_objects", func(s *runtime.MemStats) float64 { return float64(s.HeapObjects) }},
		{"stack_inuse_bytes", func(s *runtime.MemStats) float64 { return float64(s.StackInuse) }},
		{"stack_sys_bytes", func(s *runtime.MemStats) float64 { return float64(s.StackSys) }},
		{"mspan_inuse_bytes", func(s *runtime.MemStats) float64 { return float64(s.MSpanInuse) }},
		{"mspan_sys_bytes", func(s *runtime.MemStats) float64 { return float64(s.MSpanSys) }},
		{"mcache_inuse_bytes", func(s *runtime.MemStats) float64 { return float64(s.MCacheInuse) }},
		{"mcache_sys_bytes", func(s *runtime.MemStats) float64 { return float64(s.MCacheSys) }},
		{"buck_hash_sys_bytes", func(s *runtime.MemStats) float64 { return float64(s.BuckHashSys) }},
		{"gc_sys_bytes", func(s *runtime.MemStats) float64 { return float64(s.GCSys) }},
		{"other_sys_bytes", func(s *runtime.MemStats) float64 { return float64(s.OtherSys) }},
		{"next_gc_bytes", func(s *runtime.MemStats) float64 { return float64(s.NextGC) }},
	}
	var instruments []metric.Observable
	type observation struct {
		read    func(*runtime.MemStats) float64
		counter metric.Float64ObservableCounter
		gauge   metric.Float64ObservableGauge
	}
	observations := make([]observation, 0, len(fields))
	for _, field := range fields {
		item := observation{read: field.read}
		if len(field.name) >= 6 && field.name[len(field.name)-6:] == "_total" {
			item.counter = MustInstrument(meter.Float64ObservableCounter("go_memstats_" + field.name))
			instruments = append(instruments, item.counter)
		} else {
			item.gauge = MustInstrument(meter.Float64ObservableGauge("go_memstats_" + field.name))
			instruments = append(instruments, item.gauge)
		}
		observations = append(observations, item)
	}
	goroutines := MustInstrument(meter.Int64ObservableGauge("go_goroutines"))
	procs := MustInstrument(meter.Int64ObservableGauge("go_sched_gomaxprocs_threads"))
	threads := MustInstrument(meter.Int64ObservableGauge("go_threads"))
	gogc := MustInstrument(meter.Int64ObservableGauge("go_gc_gogc_percent"))
	memoryLimit := MustInstrument(meter.Int64ObservableGauge("go_gc_gomemlimit_bytes"))
	gcCount := MustInstrument(meter.Int64ObservableCounter("go_gc_duration_seconds_count"))
	gcSum := MustInstrument(meter.Float64ObservableCounter("go_gc_duration_seconds_sum", metric.WithUnit("s")))
	gcQuantile := MustInstrument(meter.Float64ObservableGauge("go_gc_duration_seconds", metric.WithUnit("s")))
	lastGC := MustInstrument(meter.Float64ObservableGauge("go_memstats_last_gc_time_seconds", metric.WithUnit("s")))
	info := MustInstrument(meter.Int64ObservableGauge("go_info"))
	instruments = append(instruments, goroutines, procs, threads, gogc, memoryLimit, gcCount, gcSum, gcQuantile, lastGC, info)
	var lock sync.Mutex
	var lastRead time.Time
	var memory runtime.MemStats
	gc := debug.GCStats{PauseQuantiles: make([]time.Duration, 5)}
	samples := []runtimemetrics.Sample{{Name: "/gc/gogc:percent"}, {Name: "/gc/gomemlimit:bytes"}}
	_, err := meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		lock.Lock()
		defer lock.Unlock()
		if time.Since(lastRead) >= time.Second {
			runtime.ReadMemStats(&memory)
			debug.ReadGCStats(&gc)
			lastRead = time.Now()
		}
		for _, item := range observations {
			value := item.read(&memory)
			if item.counter != nil {
				observer.ObserveFloat64(item.counter, value)
			} else {
				observer.ObserveFloat64(item.gauge, value)
			}
		}
		observer.ObserveInt64(goroutines, int64(runtime.NumGoroutine()))
		observer.ObserveInt64(procs, int64(runtime.GOMAXPROCS(0)))
		n, _ := runtime.ThreadCreateProfile(nil)
		observer.ObserveInt64(threads, int64(n))
		runtimemetrics.Read(samples)
		observer.ObserveInt64(gogc, int64(samples[0].Value.Uint64()))
		observer.ObserveInt64(memoryLimit, int64(samples[1].Value.Uint64()))
		observer.ObserveInt64(gcCount, gc.NumGC)
		observer.ObserveFloat64(gcSum, gc.PauseTotal.Seconds())
		for i, pause := range gc.PauseQuantiles {
			observer.ObserveFloat64(gcQuantile, pause.Seconds(), metric.WithAttributes(attribute.String("quantile", strconv.FormatFloat(float64(i)/4, 'g', -1, 64))))
		}
		observer.ObserveFloat64(lastGC, float64(gc.LastGC.UnixNano())/1e9)
		observer.ObserveInt64(info, 1, metric.WithAttributes(attribute.String("version", runtime.Version())))
		return nil
	}, instruments...)
	if err != nil {
		return err
	}
	return registerProcessMetrics(meter)
}
