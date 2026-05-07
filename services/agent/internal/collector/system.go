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

// NewSystemCollector создаёт коллектор, который собирает runtime-метрики Go процесса:
// memory (heap alloc/sys/inuse, stack), GC (last pause, total runs), goroutines.
// Работает без cgo и без /proc — кроссплатформенный.
func NewSystemCollector() *SystemCollector { return &SystemCollector{} }

// Name возвращает имя коллектора для логирования.
func (c *SystemCollector) Name() string { return "system" }

func (c *SystemCollector) Collect(_ context.Context) ([]domain.Metric, error) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	now := time.Now().UTC()

	// Index of the most recent GC pause in the circular buffer.
	lastPauseIdx := (ms.NumGC + 255) % 256

	return []domain.Metric{
		gauge("system.memory.heap_alloc_bytes", float64(ms.HeapAlloc), now),
		gauge("system.memory.heap_sys_bytes", float64(ms.HeapSys), now),
		gauge("system.memory.heap_inuse_bytes", float64(ms.HeapInuse), now),
		gauge("system.memory.stack_inuse_bytes", float64(ms.StackInuse), now),
		gauge("system.gc.last_pause_ns", float64(ms.PauseNs[lastPauseIdx]), now),
		counter("system.gc.total_runs", float64(ms.NumGC), now),
		gauge("system.goroutines", float64(runtime.NumGoroutine()), now),
	}, nil
}

func gauge(name string, value float64, ts time.Time) domain.Metric {
	return domain.Metric{Name: name, Value: value, Type: domain.MetricTypeGauge, Timestamp: ts}
}

func counter(name string, value float64, ts time.Time) domain.Metric {
	return domain.Metric{Name: name, Value: value, Type: domain.MetricTypeCounter, Timestamp: ts}
}
