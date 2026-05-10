package collector

import (
	"context"
	"runtime"
	"time"

	"github.com/mclyashko/anomaly-detector/services/agent/internal/domain"
)

// SystemCollector reports Go runtime memory and goroutine metrics.
// Cross-platform; no cgo or /proc dependencies.
type SystemCollector struct{}

// NewSystemCollector creates a collector that gathers Go runtime metrics:
// memory (heap alloc/sys/inuse, stack), GC (last pause, total runs), goroutines.
// Works without cgo and without /proc — cross-platform.
func NewSystemCollector() *SystemCollector { return &SystemCollector{} }

// Name returns the collector name for logging purposes.
func (c *SystemCollector) Name() string { return "system" }

func (c *SystemCollector) Collect(_ context.Context) ([]domain.Metric, error) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	now := time.Now().UTC()

	// Index of the most recent GC pause in the circular buffer.
	lastPauseIdx := ms.NumGC % 256

	return []domain.Metric{
		gauge("system_memory_heap_alloc_bytes", float64(ms.HeapAlloc), now),
		gauge("system_memory_heap_sys_bytes", float64(ms.HeapSys), now),
		gauge("system_memory_heap_inuse_bytes", float64(ms.HeapInuse), now),
		gauge("system_memory_stack_inuse_bytes", float64(ms.StackInuse), now),
		gauge("system_gc_last_pause_ns", float64(ms.PauseNs[lastPauseIdx]), now),
		counter("system_gc_total_runs", float64(ms.NumGC), now),
		gauge("system_goroutines", float64(runtime.NumGoroutine()), now),
	}, nil
}

func gauge(name string, value float64, ts time.Time) domain.Metric {
	return domain.Metric{Name: name, Value: value, Type: domain.MetricTypeGauge, Timestamp: ts}
}

func counter(name string, value float64, ts time.Time) domain.Metric {
	return domain.Metric{Name: name, Value: value, Type: domain.MetricTypeCounter, Timestamp: ts}
}
