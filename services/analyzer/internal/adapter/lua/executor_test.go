package lua

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
)

func TestExecute_SimpleThreshold(t *testing.T) {
	logger := slog.Default()
	exec := New(logger)
	defer exec.Close()

	// Create a temp script that fires when value > 0.8
	script := `
function evaluate(metric)
  if metric.value > 0.8 then
    return true, "CPU too high: " .. tostring(metric.value)
  end
  return false, ""
end
`
	tmp := tempScript(t, script)
	defer os.Remove(tmp)

	tests := []struct {
		name     string
		value    float64
		wantFire bool
	}{
		{"high value", 0.95, true},
		{"low value", 0.5, false},
		{"boundary", 0.8, false},
		{"just above boundary", 0.81, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := core.Metric{
				Name:      "cpu_usage",
				Value:     tt.value,
				Timestamp: time.Now(),
			}
			triggered, msg, err := exec.Execute(tmp, m)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if triggered != tt.wantFire {
				t.Errorf("triggered = %v, want %v", triggered, tt.wantFire)
			}
			if tt.wantFire && msg == "" {
				t.Error("expected non-empty message when triggered")
			}
		})
	}
}

func TestExecute_InvalidScript(t *testing.T) {
	logger := slog.Default()
	exec := New(logger)
	defer exec.Close()

	script := `function evaluate(metric) return true` // missing 'end' keyword
	tmp := tempScript(t, script)
	defer os.Remove(tmp)

	m := core.Metric{Name: "cpu_usage", Value: 0.95, Timestamp: time.Now()}
	_, _, err := exec.Execute(tmp, m)
	if err == nil {
		t.Error("expected error for invalid script")
	}
}

func TestExecute_NonExistentScript(t *testing.T) {
	logger := slog.Default()
	exec := New(logger)
	defer exec.Close()

	m := core.Metric{Name: "cpu_usage", Value: 0.95, Timestamp: time.Now()}
	_, _, err := exec.Execute("/nonexistent/path/script.lua", m)
	if err == nil {
		t.Error("expected error for non-existent script")
	}
}

func TestExecute_NoEvaluateFunction(t *testing.T) {
	logger := slog.Default()
	exec := New(logger)
	defer exec.Close()

	// Script without evaluate function
	script := `x = 1`
	tmp := tempScript(t, script)
	defer os.Remove(tmp)

	m := core.Metric{Name: "cpu_usage", Value: 0.95, Timestamp: time.Now()}
	_, _, err := exec.Execute(tmp, m)
	if err == nil {
		t.Error("expected error when evaluate function is missing")
	}
}

func TestExecute_Timeout(t *testing.T) {
	// This test is slow so we skip it in normal runs.
	// To test manually, create a script with an infinite loop.
	t.Skip("slow test")
	logger := slog.Default()
	exec := New(logger)
	defer exec.Close()

	script := `
function evaluate(metric)
  while true do end
  return false, ""
end
`
	tmp := tempScript(t, script)
	defer os.Remove(tmp)

	m := core.Metric{Name: "cpu_usage", Value: 0.95, Timestamp: time.Now()}
	_, _, err := exec.Execute(tmp, m)
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestExecute_StringComparison(t *testing.T) {
	logger := slog.Default()
	exec := New(logger)
	defer exec.Close()

	// Script that checks metric name
	script := `
function evaluate(metric)
  if metric.name == "error_rate" then
    return true, "error rate anomaly"
  end
  return false, ""
end
`
	tmp := tempScript(t, script)
	defer os.Remove(tmp)

	tests := []struct {
		name       string
		metricName string
		wantFire   bool
	}{
		{"error_rate metric", "error_rate", true},
		{"other metric", "cpu_usage", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := core.Metric{
				Name:      tt.metricName,
				Value:     0.5,
				Timestamp: time.Now(),
			}
			triggered, _, err := exec.Execute(tmp, m)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if triggered != tt.wantFire {
				t.Errorf("triggered = %v, want %v", triggered, tt.wantFire)
			}
		})
	}
}

func TestExecute_AllComparisonOperators(t *testing.T) {
	logger := slog.Default()
	exec := New(logger)
	defer exec.Close()

	tests := []struct {
		name     string
		script   string
		value    float64
		wantFire bool
	}{
		{
			"greater than",
			`function evaluate(m) if m.value > 1.0 then return true, "" end return false, "" end`,
			1.5, true,
		},
		{
			"greater than equal",
			`function evaluate(m) if m.value >= 1.0 then return true, "" end return false, "" end`,
			1.0, true,
		},
		{
			"less than",
			`function evaluate(m) if m.value < 1.0 then return true, "" end return false, "" end`,
			0.5, true,
		},
		{
			"less than equal",
			`function evaluate(m) if m.value <= 1.0 then return true, "" end return false, "" end`,
			1.0, true,
		},
		{
			"equality",
			`function evaluate(m) if m.value == 1.0 then return true, "" end return false, "" end`,
			1.0, true,
		},
		{
			"inequality",
			`function evaluate(m) if m.value ~= 1.0 then return true, "" end return false, "" end`,
			0.5, true,
		},
		{
			"and condition",
			`function evaluate(m) if m.value > 0.5 and m.value < 1.0 then return true, "" end return false, "" end`,
			0.75, true,
		},
		{
			"or condition",
			`function evaluate(m) if m.value < 0.1 or m.value > 0.9 then return true, "" end return false, "" end`,
			0.95, true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := tempScript(t, tt.script)
			defer os.Remove(tmp)

			m := core.Metric{Name: "test", Value: tt.value, Timestamp: time.Now()}
			triggered, _, err := exec.Execute(tmp, m)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if triggered != tt.wantFire {
				t.Errorf("triggered = %v, want %v", triggered, tt.wantFire)
			}
		})
	}
}

func TestExecute_TimestampAccess(t *testing.T) {
	logger := slog.Default()
	exec := New(logger)
	defer exec.Close()

	script := `
function evaluate(metric)
  if metric.timestamp ~= "" then
    return true, "timestamp: " .. metric.timestamp
  end
  return false, ""
end
`
	tmp := tempScript(t, script)
	defer os.Remove(tmp)

	m := core.Metric{
		Name:      "test",
		Value:     0.5,
		Timestamp: time.Date(2026, 3, 25, 10, 30, 0, 0, time.UTC),
	}
	triggered, msg, err := exec.Execute(tmp, m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !triggered {
		t.Error("expected triggered=true for non-empty timestamp")
	}
	if msg == "" {
		t.Error("expected non-empty message")
	}
}

func TestExecute_TostringUsage(t *testing.T) {
	logger := slog.Default()
	exec := New(logger)
	defer exec.Close()

	script := `
function evaluate(metric)
  local v = tostring(metric.value)
  if v == "0.95" then
    return true, "value is 0.95"
  end
  return false, ""
end
`
	tmp := tempScript(t, script)
	defer os.Remove(tmp)

	m := core.Metric{Name: "test", Value: 0.95, Timestamp: time.Now()}
	triggered, msg, err := exec.Execute(tmp, m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !triggered {
		t.Error("expected triggered=true for value 0.95")
	}
	if msg != "value is 0.95" {
		t.Errorf("msg = %q, want %q", msg, "value is 0.95")
	}
}

func TestExecute_TypeFunction(t *testing.T) {
	logger := slog.Default()
	exec := New(logger)
	defer exec.Close()

	script := `
function evaluate(metric)
  if type(metric.value) == "number" and type(metric.name) == "string" then
    return true, "types ok"
  end
  return false, ""
end
`
	tmp := tempScript(t, script)
	defer os.Remove(tmp)

	m := core.Metric{Name: "test", Value: 0.5, Timestamp: time.Now()}
	triggered, msg, err := exec.Execute(tmp, m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !triggered {
		t.Error("expected triggered=true for valid types")
	}
	if msg != "types ok" {
		t.Errorf("msg = %q, want %q", msg, "types ok")
	}
}

func TestExecute_MultipleRulesInScript(t *testing.T) {
	logger := slog.Default()
	exec := New(logger)
	defer exec.Close()

	script := `
function evaluate(metric)
  if metric.value > 0.9 then
    return true, "critical: " .. tostring(metric.value)
  end
  if metric.value > 0.7 then
    return true, "warning: " .. tostring(metric.value)
  end
  return false, ""
end
`
	tmp := tempScript(t, script)
	defer os.Remove(tmp)

	tests := []struct {
		name     string
		value    float64
		wantFire bool
		wantMsg  string
	}{
		{"critical", 0.95, true, "critical: 0.95"},
		{"warning", 0.75, true, "warning: 0.75"},
		{"normal", 0.5, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := core.Metric{Name: "test", Value: tt.value, Timestamp: time.Now()}
			triggered, msg, err := exec.Execute(tmp, m)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if triggered != tt.wantFire {
				t.Errorf("triggered = %v, want %v", triggered, tt.wantFire)
			}
			if tt.wantFire && msg != tt.wantMsg {
				t.Errorf("msg = %q, want %q", msg, tt.wantMsg)
			}
		})
	}
}

func TestExecute_EmptyMessage(t *testing.T) {
	logger := slog.Default()
	exec := New(logger)
	defer exec.Close()

	script := `function evaluate(metric) return true, "" end`
	tmp := tempScript(t, script)
	defer os.Remove(tmp)

	m := core.Metric{Name: "test", Value: 0.95, Timestamp: time.Now()}
	triggered, msg, err := exec.Execute(tmp, m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !triggered {
		t.Error("expected triggered=true")
	}
	if msg != "" {
		t.Errorf("msg = %q, want empty string", msg)
	}
}

func TestExecute_NegativeValue(t *testing.T) {
	logger := slog.Default()
	exec := New(logger)
	defer exec.Close()

	script := `function evaluate(metric) if metric.value < 0 then return true, "negative" end return false, "" end`
	tmp := tempScript(t, script)
	defer os.Remove(tmp)

	m := core.Metric{Name: "test", Value: -5.0, Timestamp: time.Now()}
	triggered, msg, err := exec.Execute(tmp, m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !triggered {
		t.Error("expected triggered=true for negative value")
	}
	if msg != "negative" {
		t.Errorf("msg = %q, want %q", msg, "negative")
	}
}

func TestExecute_Zerovalue(t *testing.T) {
	logger := slog.Default()
	exec := New(logger)
	defer exec.Close()

	script := `function evaluate(metric) if metric.value == 0 then return true, "zero" end return false, "" end`
	tmp := tempScript(t, script)
	defer os.Remove(tmp)

	m := core.Metric{Name: "test", Value: 0.0, Timestamp: time.Now()}
	triggered, msg, err := exec.Execute(tmp, m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !triggered {
		t.Error("expected triggered=true for zero value")
	}
	if msg != "zero" {
		t.Errorf("msg = %q, want %q", msg, "zero")
	}
}

func TestExecute_ScriptCaching(t *testing.T) {
	logger := slog.Default()
	exec := New(logger)
	defer exec.Close()

	script := `function evaluate(metric) if metric.value > 0.8 then return true, "high" end return false, "" end`
	tmp := tempScript(t, script)
	defer os.Remove(tmp)

	// Execute same script multiple times - should use cache
	for i := range 3 {
		m := core.Metric{Name: "test", Value: 0.95, Timestamp: time.Now()}
		triggered, _, err := exec.Execute(tmp, m)
		if err != nil {
			t.Fatalf("unexpected error on iteration %d: %v", i, err)
		}
		if !triggered {
			t.Errorf("iteration %d: expected triggered=true", i)
		}
	}
}

func TestExecute_TrueAndFalseReturnValues(t *testing.T) {
	logger := slog.Default()
	exec := New(logger)
	defer exec.Close()

	tests := []struct {
		name     string
		script   string
		wantFire bool
	}{
		{
			"returns false only",
			`function evaluate(metric) return false, "ok" end`,
			false,
		},
		{
			"returns true only",
			`function evaluate(metric) return true, "anomaly" end`,
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := tempScript(t, tt.script)
			defer os.Remove(tmp)

			m := core.Metric{Name: "test", Value: 0.5, Timestamp: time.Now()}
			triggered, _, err := exec.Execute(tmp, m)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if triggered != tt.wantFire {
				t.Errorf("triggered = %v, want %v", triggered, tt.wantFire)
			}
		})
	}
}

func tempScript(t *testing.T, content string) string {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "test_script.lua")
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write temp script: %v", err)
	}
	return tmp
}
