package collector_test

import (
	"context"
	"testing"

	"github.com/mclyashko/anomaly-detector/services/agent/internal/collector"
	"github.com/mclyashko/anomaly-detector/services/agent/internal/domain"
)

var wantSystemMetrics = []string{
	"system_memory_heap_alloc_bytes",
	"system_memory_heap_sys_bytes",
	"system_memory_heap_inuse_bytes",
	"system_memory_stack_inuse_bytes",
	"system_gc_last_pause_ns",
	"system_gc_total_runs",
	"system_goroutines",
}

func TestSystemCollector_ReturnsAllMetrics(t *testing.T) {
	c := collector.NewSystemCollector()
	metrics, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(metrics) != len(wantSystemMetrics) {
		t.Fatalf("got %d metrics, want %d", len(metrics), len(wantSystemMetrics))
	}

	got := make(map[string]domain.Metric, len(metrics))
	for _, m := range metrics {
		got[m.Name] = m
	}
	for _, name := range wantSystemMetrics {
		if _, ok := got[name]; !ok {
			t.Errorf("metric %q not present", name)
		}
	}
}

func TestSystemCollector_ValuesAndTimestamps(t *testing.T) {
	c := collector.NewSystemCollector()
	metrics, _ := c.Collect(context.Background())

	for _, m := range metrics {
		if m.Value < 0 {
			t.Errorf("metric %q: negative value %v", m.Name, m.Value)
		}
		if m.Timestamp.IsZero() {
			t.Errorf("metric %q: zero timestamp", m.Name)
		}
	}
}

func TestSystemCollector_MetricTypes(t *testing.T) {
	c := collector.NewSystemCollector()
	metrics, _ := c.Collect(context.Background())

	for _, m := range metrics {
		switch m.Name {
		case "system_gc_total_runs":
			if m.Type != domain.MetricTypeCounter {
				t.Errorf("%q: want counter, got %v", m.Name, m.Type)
			}
		default:
			if m.Type != domain.MetricTypeGauge {
				t.Errorf("%q: want gauge, got %v", m.Name, m.Type)
			}
		}
	}
}

func TestSystemCollector_Name(t *testing.T) {
	c := collector.NewSystemCollector()
	if c.Name() != "system" {
		t.Errorf("Name() = %q, want system", c.Name())
	}
}
