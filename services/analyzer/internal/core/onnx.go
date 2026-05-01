package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/adapter/storage"
)

// ModelConfig describes which model to load for a given (agent_id, metric_name).
type ModelConfig struct {
	AgentID          string  `yaml:"agent_id"`
	Metric           string  `yaml:"metric"`
	ModelType        string  `yaml:"model_type"`
	VersionPolicy    string  `yaml:"version_policy"` // "latest" or explicit "v3"
	AnomalyThreshold float64 `yaml:"anomaly_threshold"` // confidence level for CI (0.95)
	WindowSize       int     `yaml:"window_size"`     // history window (default 48)
}

// DefaultWindowSize is the default number of recent values used for inference.
const DefaultWindowSize = 48

// ModelParams are the SARIMA/forecast parameters extracted from metadata.
type ModelParams struct {
	ARParams          float64 `json:"ar_params"`
	MAParams          float64 `json:"ma_params"`
	SeasonalARParams  float64 `json:"seasonal_ar_params"`
	SeasonalMAParams  float64 `json:"seasonal_ma_params"`
	ResidualStd       float64 `json:"residual_std"`
	ConfidenceLevel   float64 `json:"confidence_level"`
	SeasonalityPeriod int     `json:"seasonality_period"`
	D                 int     `json:"d"`
	SeasonalD         int     `json:"seasonal_d"`
	WindowSize        int     `json:"window_size,omitempty"`
}

// ONNXModel holds a loaded model with its parameters.
type ONNXModel struct {
	Config    ModelConfig
	Params    *ModelParams
	LoadedAt  time.Time
	windowBuf map[string][]float64 // per-agent sliding window: "agentID:metricName" → values
	mu        sync.RWMutex
}

// ModelRegistry manages loaded ONNX models.
// Thread-safe; models can be replaced atomically.
type ModelRegistry struct {
	storage  storage.ModelStoragePort
	configs  []ModelConfig
	models   map[string]*ONNXModel // key = "agentID:metricName"
	mu       sync.RWMutex
	logger   *slog.Logger
	stopChan chan struct{}
}

// NewModelRegistry creates a registry with the given storage backend and model configs.
func NewModelRegistry(storage storage.ModelStoragePort, configs []ModelConfig, logger *slog.Logger) *ModelRegistry {
	r := &ModelRegistry{
		storage:  storage,
		configs:  configs,
		models:   make(map[string]*ONNXModel),
		logger:   logger,
		stopChan: make(chan struct{}),
	}
	// Set default window size.
	for i := range r.configs {
		if r.configs[i].WindowSize == 0 {
			r.configs[i].WindowSize = DefaultWindowSize
		}
	}
	return r
}

// Load loads all models from storage into the registry.
// Models are available via Get() immediately after this returns.
func (r *ModelRegistry) Load(ctx context.Context) error {
	return r.LoadAll(ctx)
}

// LoadAll loads or refreshes all models from MinIO.
func (r *ModelRegistry) LoadAll(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	prefixes, err := r.storage.ListModelPrefixes(ctx)
	if err != nil {
		return fmt.Errorf("list model prefixes: %w", err)
	}

	loaded := make(map[string]*ONNXModel)
	for _, prefix := range prefixes {
		meta, err := r.storage.GetMetadata(ctx, prefix)
		if err != nil {
			r.logger.Warn("failed to load metadata, skipping", "prefix", prefix, "err", err)
			continue
		}
		// Find matching config.
		cfg := r.findConfig(meta.AgentID, meta.MetricName, meta.ModelType)
		if cfg == nil {
			r.logger.Debug("no config for model, skipping", "prefix", prefix)
			continue
		}
		params, err := r.parseParams(meta)
		if err != nil {
			r.logger.Warn("failed to parse params, skipping", "prefix", prefix, "err", err)
			continue
		}
		key := cfg.AgentID + ":" + cfg.Metric
		model := &ONNXModel{
			Config:   *cfg,
			Params:   params,
			LoadedAt: time.Now(),
		}
		loaded[key] = model
		r.logger.Info("loaded model", "key", key, "prefix", prefix, "ar", params.ARParams, "residual_std", params.ResidualStd)
	}

	// Preserve models that didn't get refreshed but are still valid.
	for k, v := range r.models {
		if _, ok := loaded[k]; !ok {
			loaded[k] = v
		}
	}
	r.models = loaded
	return nil
}

func (r *ModelRegistry) findConfig(agentID, metricName, modelType string) *ModelConfig {
	for i := range r.configs {
		c := &r.configs[i]
		if c.AgentID == agentID && c.Metric == metricName && c.ModelType == modelType {
			return c
		}
	}
	return nil
}

func (r *ModelRegistry) parseParams(meta *storage.ModelMetadata) (*ModelParams, error) {
	// Build params from metadata fields.
	cl := meta.ConfidenceLevel
	if cl == 0 {
		cl = 0.95
	}
	ws := meta.WindowSize
	if ws == 0 {
		ws = 48
	}
	return &ModelParams{
		ARParams:          meta.ARParams,
		MAParams:          meta.MAParams,
		SeasonalARParams:  meta.SeasonalARParams,
		SeasonalMAParams:  meta.SeasonalMAParams,
		ResidualStd:       meta.ResidualStd,
		ConfidenceLevel:   cl,
		SeasonalityPeriod: meta.SeasonalityPeriod,
		D:                 meta.D,
		SeasonalD:         meta.SeasonalD,
		WindowSize:        ws,
	}, nil
}

// Get returns the model for a given (agentID, metricName).
func (r *ModelRegistry) Get(agentID, metricName string) (*ONNXModel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.models[agentID+":"+metricName]
	return m, ok
}

// ModelCount returns the number of loaded models.
func (r *ModelRegistry) ModelCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.models)
}

// StartCron starts the periodic model refresh loop.
func (r *ModelRegistry) StartCron(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.logger.Info("model registry cron stopped")
			return
		case <-ticker.C:
			if err := r.LoadAll(ctx); err != nil {
				r.logger.Error("model refresh failed", "err", err)
			} else {
				r.logger.Info("model refresh done", "count", r.ModelCount())
			}
		}
	}
}

// Close stops the cron loop.
func (r *ModelRegistry) Close() {
	close(r.stopChan)
}

// Infer runs AR(1)-style inference using the loaded model parameters.
// history should be the most recent WindowSize values (oldest first, most recent last).
// Returns (forecast, lowerCI, upperCI).
func (m *ONNXModel) Infer(history []float64) (float64, float64, float64) {
	n := len(history)
	if n < 2 {
		// Not enough history for AR(1) — naive forecast = last value.
		forecast := 0.0
		if n == 1 {
			forecast = history[0]
		}
		z := m.zScore()
		halfWidth := z * m.Params.ResidualStd
		return forecast, forecast - halfWidth, forecast + halfWidth
	}
	lastVal := history[n-1]
	prevVal := history[n-2]
	ar := m.Params.ARParams
	// AR(1) correction: forecast = last_val + ar * (last_val - prev_val)
	arCorrection := ar * (lastVal - prevVal)
	forecast := lastVal + arCorrection
	z := m.zScore()
	// For 1-step ahead, uncertainty is just residual_std * z
	halfWidth := z * m.Params.ResidualStd
	return forecast, forecast - halfWidth, forecast + halfWidth
}

// DetectAnomaly returns true if value falls outside the model's prediction interval.
// history must have at least WindowSize values (most recent last).
func (m *ONNXModel) DetectAnomaly(value float64, history []float64) (bool, string) {
	windowSize := m.Config.WindowSize
	if windowSize == 0 {
		windowSize = DefaultWindowSize
	}
	if len(history) < windowSize {
		return false, "insufficient history"
	}
	// Use the last WindowSize values.
	slice := history[len(history)-windowSize:]
	forecast, lower, upper := m.Infer(slice)
	anomaly := value < lower || value > upper
	msg := fmt.Sprintf("value=%.2f forecast=%.2f ci=[%.2f, %.2f]", value, forecast, lower, upper)
	return anomaly, msg
}

// zScore returns the z-score for the model's confidence level (2-tailed).
func (m *ONNXModel) zScore() float64 {
	cl := m.Params.ConfidenceLevel
	if cl <= 0 {
		cl = 0.95
	}
	return normQuantile((1+cl)/2) * 1.0
}

// normQuantile approximates the standard normal quantile function (inverse CDF).
// Uses Erfcinv for a good initial guess + Newton's method refinement.
// Pure Go (no CGO), accurate to ~1e-10.
func normQuantile(p float64) float64 {
	if p <= 0 {
		return math.Inf(-1)
	}
	if p >= 1 {
		return math.Inf(1)
	}
	if p == 0.5 {
		return 0
	}
	if p < 0.5 {
		return -normQuantile(1 - p)
	}
	// Erfcinv gives a very accurate initial guess: z0 = Erfcinv(2*(1-p)) * sqrt(2)
	x := math.Erfcinv(2*(1-p)) * math.Sqrt2
	// Newton refinement: x_new = x - (Phi(x) - p) / phi(x)
	for i := 0; i < 10; i++ {
		cdf := 0.5 * (1 + math.Erf(x/math.Sqrt2))
		pdf := math.Exp(-x*x/2) / math.Sqrt(2*math.Pi)
		delta := (cdf - p) / pdf
		x -= delta
		if math.Abs(delta) < 1e-12 {
			break
		}
	}
	return x
}

// AddHistory appends a value to the sliding window for (agentID, metricName).
// This is called by the analyzer after each batch evaluation to keep history fresh.
func (m *ONNXModel) AddHistory(agentID, metricName string, value float64) {
	key := agentID + ":" + metricName
	windowSize := m.Config.WindowSize
	if windowSize == 0 {
		windowSize = DefaultWindowSize
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.windowBuf == nil {
		m.windowBuf = make(map[string][]float64)
	}
	buf := m.windowBuf[key]
	buf = append(buf, value)
	// Keep only last windowSize values.
	if len(buf) > windowSize {
		buf = buf[len(buf)-windowSize:]
	}
	m.windowBuf[key] = buf
}

// GetHistory returns the sliding window values for (agentID, metricName).
func (m *ONNXModel) GetHistory(agentID, metricName string) []float64 {
	key := agentID + ":" + metricName
	m.mu.RLock()
	defer m.mu.RUnlock()
	if buf, ok := m.windowBuf[key]; ok {
		return buf
	}
	return nil
}

// ParseModelParamsFromJSON parses SARIMA params from the ONNX doc_string JSON.
// This is used when the ONNX file is loaded directly instead of metadata.json.
func ParseModelParamsFromJSON(docString string) (*ModelParams, error) {
	// doc_string is a JSON object with all SARIMA parameters.
	// Try to parse it directly.
	var params ModelParams
	if err := json.Unmarshal([]byte(docString), &params); err != nil {
		return nil, fmt.Errorf("parse doc_string JSON: %w", err)
	}
	// Fill defaults.
	if params.ResidualStd == 0 {
		params.ResidualStd = 1.0
	}
	if params.ConfidenceLevel == 0 {
		params.ConfidenceLevel = 0.95
	}
	return &params, nil
}

// ParseModelName parses a model name prefix like "agent-1__http_latency__sarima__v3"
// and returns its components.
func ParseModelName(name string) (agentID, metricName, modelType, version string, ok bool) {
	parts := strings.Split(name, "__")
	if len(parts) != 4 {
		return "", "", "", "", false
	}
	return parts[0], parts[1], parts[2], parts[3], true
}
