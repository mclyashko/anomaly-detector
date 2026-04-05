package yaml_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/adapter/yaml"
	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
)

func TestLoader_ValidFile(t *testing.T) {
	content := `
rules:
  - name: high-cpu
    metric: cpu_usage
    type: threshold
    condition: "value > 0.8"
    severity: warning
`
	tmp := tempFile(t, content)
	defer os.Remove(tmp)

	loader := yaml.NewLoader(tmp)
	configs, err := loader.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("got %d rules, want 1", len(configs))
	}
	c := configs[0]
	if c.Name != "high-cpu" {
		t.Errorf("name = %q, want high-cpu", c.Name)
	}
	if c.Metric != "cpu_usage" {
		t.Errorf("metric = %q, want cpu_usage", c.Metric)
	}
	if c.Type != core.RuleTypeThreshold {
		t.Errorf("type = %v, want threshold", c.Type)
	}
	if c.Severity != core.SeverityWarning {
		t.Errorf("severity = %v, want warning", c.Severity)
	}
}

func TestLoader_MultipleRules(t *testing.T) {
	content := `
rules:
  - name: rule1
    metric: m1
    type: threshold
    condition: "value > 0.5"
    severity: info
  - name: rule2
    metric: m2
    type: threshold
    condition: "value > 0.9"
    severity: critical
`
	tmp := tempFile(t, content)
	defer os.Remove(tmp)

	configs, err := yaml.NewLoader(tmp).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 2 {
		t.Errorf("got %d rules, want 2", len(configs))
	}
}

func TestLoader_FileNotFound(t *testing.T) {
	_, err := yaml.NewLoader("/nonexistent/path.yaml").Load()
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoader_InvalidYAML(t *testing.T) {
	tmp := tempFile(t, "not: valid: yaml: :")
	defer os.Remove(tmp)

	_, err := yaml.NewLoader(tmp).Load()
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLoader_MissingName(t *testing.T) {
	content := `
rules:
  - metric: cpu
    type: threshold
    condition: "value > 0.8"
    severity: warning
`
	tmp := tempFile(t, content)
	defer os.Remove(tmp)

	_, err := yaml.NewLoader(tmp).Load()
	if err == nil {
		t.Error("expected error for missing rule name")
	}
}

func TestLoader_MissingMetric(t *testing.T) {
	content := `
rules:
  - name: r1
    type: threshold
    condition: "value > 0.8"
    severity: warning
`
	tmp := tempFile(t, content)
	defer os.Remove(tmp)

	_, err := yaml.NewLoader(tmp).Load()
	if err == nil {
		t.Error("expected error for missing metric")
	}
}

func TestCompileRules(t *testing.T) {
	content := `
rules:
  - name: high-cpu
    metric: cpu_usage
    type: threshold
    condition: "value > 0.8"
    severity: warning
`
	tmp := tempFile(t, content)
	defer os.Remove(tmp)

	rules, err := yaml.CompileRules(tmp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(rules))
	}
	if rules[0].Name() != "high-cpu" {
		t.Errorf("name = %q, want high-cpu", rules[0].Name())
	}
}

func tempFile(t *testing.T, content string) string {
	t.Helper()
	tmp, err := os.CreateTemp("", "rules-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}
	return tmp.Name()
}

// Verify Loader satisfies port.RuleLoader.
var _ interface {
	Load() ([]core.RuleConfig, error)
} = (*yaml.Loader)(nil)

// Suppress unused import warning.
var _ = filepath.Join
