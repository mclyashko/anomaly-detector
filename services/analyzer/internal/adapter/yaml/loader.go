package yaml

import (
	"fmt"
	"os"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/adapter/ml"
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
		cfg := doc.Rules[i]
		if cfg.Name == "" {
			return nil, fmt.Errorf("rule at index %d has no name", i)
		}
		if cfg.Metric == "" {
			return nil, fmt.Errorf("rule %q has no metric", cfg.Name)
		}
		if cfg.Type == "" {
			return nil, fmt.Errorf("rule %q has no type", cfg.Name)
		}
		if cfg.Type == core.RuleTypeML {
			if cfg.TrainIntervalMin == 0 {
				return nil, fmt.Errorf("rule %q (ml) requires train_interval_min", cfg.Name)
			}
			if cfg.TrainDataWindow == 0 {
				return nil, fmt.Errorf("rule %q (ml) requires train_data_window", cfg.Name)
			}
			if cfg.SeasonalityPeriod == 0 {
				return nil, fmt.Errorf("rule %q (ml) requires seasonality_period", cfg.Name)
			}
			if len(cfg.Order) != 3 {
				return nil, fmt.Errorf("rule %q (ml) requires order: [p,d,q] with 3 elements", cfg.Name)
			}
			if len(cfg.SeasonalOrder) != 4 {
				return nil, fmt.Errorf("rule %q (ml) requires seasonal_order: [P,D,Q,S] with 4 elements", cfg.Name)
			}
		}
	}

	// Filter enabled rules only
	var enabledRules []core.RuleConfig
	for _, cfg := range doc.Rules {
		if !cfg.Enabled {
			continue
		}
		enabledRules = append(enabledRules, cfg)
	}

	return enabledRules, nil
}

// CompileRules loads configs from the YAML file and creates Rule instances
// using the core.RuleFactory. The mlClient is required for ML rules.
func CompileRules(path string, executor core.LuaExecutor, mlClient *ml.Client) ([]core.Rule, error) {
	loader := NewLoader(path)
	configs, err := loader.Load()
	if err != nil {
		return nil, err
	}

	rules := make([]core.Rule, 0, len(configs))
	for _, cfg := range configs {
		rule, err := core.RuleFactory(cfg, executor, mlClient)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", cfg.Name, err)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// Verify Loader satisfies port.RuleLoader.
var _ port.RuleLoader = (*Loader)(nil)
