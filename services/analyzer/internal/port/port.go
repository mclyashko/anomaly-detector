package port

import (
	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
)

// RuleLoader reads rule definitions from some source and returns the configs.
type RuleLoader interface {
	Load() ([]core.RuleConfig, error)
}
