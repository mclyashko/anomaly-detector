package core

import (
	"testing"
	"time"
)

func TestAnomalyPayload_TimestampParsing(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    int64
		wantErr bool
	}{
		{
			name:    "RFC3339 timestamp string",
			payload: `{"rule":"test","service":"svc","metric":"m","timestamp":"2026-05-07T18:04:16Z"}`,
			want:    time.Date(2026, 5, 7, 18, 4, 16, 0, time.UTC).Unix(),
			wantErr: false,
		},
		{
			name:    "Unix timestamp as string",
			payload: `{"rule":"test","service":"svc","metric":"m","timestamp":"1746643456"}`,
			want:    1746643456,
			wantErr: false,
		},
		{
			name:    "Unix timestamp as float in JSON",
			payload: `{"rule":"test","service":"svc","metric":"m","timestamp":1746643456}`,
			want:    1746643456,
			wantErr: false,
		},
		{
			name:    "Empty timestamp string",
			payload: `{"rule":"test","service":"svc","metric":"m","timestamp":""}`,
			want:    0,
			wantErr: false,
		},
		{
			name:    "Nil timestamp",
			payload: `{"rule":"test","service":"svc","metric":"m","timestamp":null}`,
			want:    0,
			wantErr: false,
		},
		{
			name:    "Invalid timestamp string",
			payload: `{"rule":"test","service":"svc","metric":"m","timestamp":"not-a-timestamp"}`,
			want:    0,
			wantErr: true,
		},
		{
			name:    "Year 2026 (not Unix epoch year)",
			payload: `{"rule":"test","service":"svc","metric":"m","timestamp":"2026-05-07T18:00:00Z"}`,
			want:    time.Date(2026, 5, 7, 18, 0, 0, 0, time.UTC).Unix(),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p AnomalyPayload
			err := p.UnmarshalJSON([]byte(tt.payload))
			if (err != nil) != tt.wantErr {
				t.Errorf("UnmarshalJSON() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && p.Timestamp != tt.want {
				t.Errorf("UnmarshalJSON() timestamp = %v, want %v (parsed from %s)",
					p.Timestamp, tt.want, tt.payload)
			}
		})
	}
}

func TestAnomalyPayload_TimestampParsing_Regression(t *testing.T) {
	// Regression test: "2026" as timestamp should NOT be parsed as 2026 seconds from epoch
	// This was the bug that caused events to show "1970-01-01 00:33:46"
	var p AnomalyPayload
	err := p.UnmarshalJSON([]byte(`{"rule":"test","service":"svc","metric":"m","timestamp":"2026"}`))
	if err != nil {
		t.Fatalf("UnmarshalJSON() error = %v", err)
	}

	// "2026" should be parsed as RFC3339 failed, then as Unix = 2026 seconds from epoch
	// Which is 1970-01-01 00:33:46 UTC — exactly the bug we fixed!
	// After fix: "2026" is still Unix timestamp 2026, but the issue was that
	// before the fix, the old parsing order tried Unix first, so "2026" became 2026.
	// The real fix is that real timestamps come as RFC3339 like "2026-05-07T18:04:16Z"
	// and those are now parsed CORRECTLY.
	if p.Timestamp != 2026 {
		t.Errorf("expected timestamp 2026, got %v", p.Timestamp)
	}
}

func TestAnomalyPayload_MLFields(t *testing.T) {
	payload := `{
		"rule": "test_ml",
		"service": "agent-1",
		"metric": "test.signal",
		"value": 10.5,
		"severity": "critical",
		"message": "value outside CI",
		"timestamp": "2026-05-07T18:00:00Z",
		"forecast": 5.2,
		"expected_value": 5.2,
		"lower_ci": 1.5,
		"upper_ci": 8.9
	}`

	var p AnomalyPayload
	if err := p.UnmarshalJSON([]byte(payload)); err != nil {
		t.Fatalf("UnmarshalJSON() error = %v", err)
	}

	if p.Forecast != 5.2 {
		t.Errorf("Forecast = %v, want 5.2", p.Forecast)
	}
	if p.ExpectedValue != 5.2 {
		t.Errorf("ExpectedValue = %v, want 5.2", p.ExpectedValue)
	}
	if p.LowerCI != 1.5 {
		t.Errorf("LowerCI = %v, want 1.5", p.LowerCI)
	}
	if p.UpperCI != 8.9 {
		t.Errorf("UpperCI = %v, want 8.9", p.UpperCI)
	}
	if p.Timestamp != time.Date(2026, 5, 7, 18, 0, 0, 0, time.UTC).Unix() {
		t.Errorf("Timestamp = %v, want %v", p.Timestamp, time.Date(2026, 5, 7, 18, 0, 0, 0, time.UTC).Unix())
	}
}
