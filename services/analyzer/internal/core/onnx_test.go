package core_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/adapter/storage"
	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
)

// mockStorage implements storage.ModelStoragePort for testing.
type mockStorage struct {
	prefixes   []string
	metadata   map[string]*storage.ModelMetadata
	downloadErr error
	listErr    error
}

func (m *mockStorage) ListModelPrefixes(ctx context.Context) ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.prefixes, nil
}

func (m *mockStorage) DownloadModel(ctx context.Context, prefix string) ([]byte, error) {
	return nil, m.downloadErr
}

func (m *mockStorage) GetMetadata(ctx context.Context, prefix string) (*storage.ModelMetadata, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	if meta, ok := m.metadata[prefix]; ok {
		return meta, nil
	}
	return nil, nil
}

func (m *mockStorage) GetLatestVersion(ctx context.Context, agentID, metricName, modelType string) (string, error) {
	return "", nil
}

// --- ONNXModel.Infer and DetectAnomaly ---

// DetectAnomaly uses Config.WindowSize to determine if history is sufficient.
// Default WindowSize is 0 (treated as 48). Tests must set Config.WindowSize
// to match the provided history length to avoid "insufficient history" path.

func TestInfer_AnomalyOutsideCI(t *testing.T) {
	model := &core.ONNXModel{
		Config: core.ModelConfig{WindowSize: 5},
		Params: &core.ModelParams{
			ARParams: 0.5, MAParams: 0.0, ResidualStd: 1.0, ConfidenceLevel: 0.95,
		},
	}
	// history [1,2,3,4,5]: last=5, prev=4 → ar_correction=0.5, forecast=5.5
	// z(0.975)≈1.96, halfWidth≈1.96 → CI≈[3.54, 7.46]
	// value=10 is outside CI
	triggered, msg := model.DetectAnomaly(10.0, []float64{1.0, 2.0, 3.0, 4.0, 5.0})
	if !triggered {
		t.Error("expected anomaly for value outside CI")
	}
	if msg == "" {
		t.Error("expected non-empty message")
	}
}

func TestInfer_NoAnomalyInsideCI(t *testing.T) {
	model := &core.ONNXModel{
		Config: core.ModelConfig{WindowSize: 5},
		Params: &core.ModelParams{
			ARParams: 0.5, MAParams: 0.0, ResidualStd: 1.0, ConfidenceLevel: 0.95,
		},
	}
	// value=5.0 is inside CI
	triggered, _ := model.DetectAnomaly(5.0, []float64{1.0, 2.0, 3.0, 4.0, 5.0})
	if triggered {
		t.Error("expected no anomaly for value inside CI")
	}
}

func TestInfer_InsufficientHistory(t *testing.T) {
	model := &core.ONNXModel{
		Config: core.ModelConfig{WindowSize: 48},
		Params: &core.ModelParams{
			ARParams: 0.5, ResidualStd: 1.0, ConfidenceLevel: 0.95,
		},
	}
	// Only 5 values but windowSize=48 → insufficient
	triggered, msg := model.DetectAnomaly(10.0, []float64{1, 2, 3, 4, 5})
	if triggered {
		t.Error("expected no anomaly with insufficient history")
	}
	if msg != "insufficient history" {
		t.Errorf("msg = %q, want 'insufficient history'", msg)
	}
}

func TestInfer_ARZeroReturnsLastValue(t *testing.T) {
	model := &core.ONNXModel{
		Config: core.ModelConfig{WindowSize: 2},
		Params: &core.ModelParams{
			ARParams: 0.0, ResidualStd: 1.0, ConfidenceLevel: 0.95,
		},
	}
	// With ar=0, forecast = last_val = 5.0
	// z(0.975)≈1.96, halfWidth≈1.96
	forecast, lower, upper := model.Infer([]float64{3.0, 5.0})
	if forecast != 5.0 {
		t.Errorf("forecast = %v, want 5.0", forecast)
	}
	if lower >= upper {
		t.Errorf("lower=%v >= upper=%v", lower, upper)
	}
	// CI should contain forecast
	if lower > forecast || upper < forecast {
		t.Errorf("forecast not in CI [%v, %v]", lower, upper)
	}
}

func TestInfer_OneValue(t *testing.T) {
	model := &core.ONNXModel{
		Config: core.ModelConfig{WindowSize: 1},
		Params: &core.ModelParams{
			ARParams: 0.5, ResidualStd: 1.0, ConfidenceLevel: 0.95,
		},
	}
	// With only 1 value, forecast = that value
	forecast, _, _ := model.Infer([]float64{5.0})
	if forecast != 5.0 {
		t.Errorf("forecast = %v, want 5.0", forecast)
	}
}

// --- Sliding window ---

func TestONNXModel_SlidingWindow_RespectsSize(t *testing.T) {
	model := &core.ONNXModel{
		Config: core.ModelConfig{WindowSize: 5},
		Params: &core.ModelParams{ARParams: 0.5, ResidualStd: 1.0, ConfidenceLevel: 0.95},
	}
	// Add 7 values
	for i := 0; i < 7; i++ {
		model.AddHistory("agent-1", "cpu", float64(i))
	}
	history := model.GetHistory("agent-1", "cpu")
	if len(history) != 5 {
		t.Errorf("history len = %d, want 5", len(history))
	}
	// Last value should be 6.0
	if len(history) > 0 && history[len(history)-1] != 6.0 {
		t.Errorf("last value = %v, want 6.0", history[len(history)-1])
	}
}

func TestONNXModel_GetHistory_UnknownKey(t *testing.T) {
	model := &core.ONNXModel{}
	if h := model.GetHistory("unknown", "metric"); h != nil {
		t.Errorf("expected nil for unknown key, got %v", h)
	}
}

func TestONNXModel_HistoryPerAgentMetric(t *testing.T) {
	model := &core.ONNXModel{
		Config: core.ModelConfig{WindowSize: 10},
		Params: &core.ModelParams{ARParams: 0.5, ResidualStd: 1.0, ConfidenceLevel: 0.95},
	}
	model.AddHistory("agent-1", "cpu", 1.0)
	model.AddHistory("agent-2", "cpu", 2.0)

	h1 := model.GetHistory("agent-1", "cpu")
	h2 := model.GetHistory("agent-2", "cpu")
	if h1[0] != 1.0 || h2[0] != 2.0 {
		t.Error("history not isolated per agent/metric")
	}
}

// --- ModelRegistry ---

func TestModelRegistry_Get_NotFound(t *testing.T) {
	ms := &mockStorage{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry := core.NewModelRegistry(ms, []core.ModelConfig{}, logger)
	_, ok := registry.Get("unknown", "unknown")
	if ok {
		t.Error("expected not found for unknown agent/metric")
	}
}

func TestModelRegistry_ModelCount_Empty(t *testing.T) {
	ms := &mockStorage{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry := core.NewModelRegistry(ms, []core.ModelConfig{}, logger)
	if registry.ModelCount() != 0 {
		t.Errorf("count = %d, want 0", registry.ModelCount())
	}
}

// --- ParseModelName ---

func TestParseModelName_Valid(t *testing.T) {
	agentID, metric, modelType, version, ok := core.ParseModelName("agent-1__http_latency__sarima__v3")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if agentID != "agent-1" || metric != "http_latency" || modelType != "sarima" || version != "v3" {
		t.Errorf("got (%s,%s,%s,%s), want (agent-1,http_latency,sarima,v3)",
			agentID, metric, modelType, version)
	}
}

func TestParseModelName_Invalid(t *testing.T) {
	for _, name := range []string{"no_double_underscore", "only_two__v1", ""} {
		_, _, _, _, ok := core.ParseModelName(name)
		if ok {
			t.Errorf("expected ok=false for %q", name)
		}
	}
}
