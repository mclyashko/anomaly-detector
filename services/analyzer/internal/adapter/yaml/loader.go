package yaml

import (
	"fmt"
	"os"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/port"
	"gopkg.in/yaml.v3"
)

// Loader reads rule definitions from a YAML file.
type Loader struct {
	path string
}

// NewLoader creates a YAML loader that reads from the given file path.
func NewLoader(path string) *Loader {
	return &Loader{path: path}
}

func (l *Loader) Load() ([]core.RuleConfig, error) {
	data, err := os.ReadFile(l.path)
	if err != nil {
		return nil, fmt.Errorf("read rules file %q: %w", l.path, err)
	}

	var doc struct {
		Rules []core.RuleConfig `yaml:"rules"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse rules YAML: %w", err)
	}

	for i := range doc.Rules {
		if doc.Rules[i].Name == "" {
			return nil, fmt.Errorf("rule at index %d has no name", i)
		}
		if doc.Rules[i].Metric == "" {
			return nil, fmt.Errorf("rule %q has no metric", doc.Rules[i].Name)
		}
		if doc.Rules[i].Type == "" {
			return nil, fmt.Errorf("rule %q has no type", doc.Rules[i].Name)
		}
	}

	return doc.Rules, nil
}

// CompileRules loads configs from the YAML file and creates Rule instances
// using the core.RuleFactory. Returns the slice of compiled rules.
func CompileRules(path string, executor core.LuaExecutor) ([]core.Rule, error) {
	loader := NewLoader(path)
	configs, err := loader.Load()
	if err != nil {
		return nil, err
	}

	rules := make([]core.Rule, 0, len(configs))
	for _, cfg := range configs {
		rule, err := core.RuleFactory(cfg, executor)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", cfg.Name, err)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// Verify Loader satisfies port.RuleLoader.
var _ port.RuleLoader = (*Loader)(nil)
