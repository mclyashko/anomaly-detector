//go:build ignore

// Data generator for integration tests.
// Seeds TimescaleDB with deterministic cyclic time-series signals.
//
// Usage:
//   go run scripts/generate_signal.go [sin|parabola] [base_time] [days]
//
// base_time is RFC3339 (default: 2026-04-01T00:00:00Z)
// days is number of 24-hour cycles to generate (default: 1)
package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	DBDSN       = "postgres://postgres:secret@localhost:5432/anomaly?sslmode=disable"
	AgentID     = "agent-test"
	MetricName  = "test.signal"
	StepHours   = 0.1          // 6-minute intervals
	PeriodHours = 24.0         // 24-hour cycle
	StepsPerDay = 240          // 240 * 0.1h = 24h
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: generate_signal.go [sin|parabola] [base_time] [days]")
		os.Exit(1)
	}
	mode := os.Args[1]

	baseTime := time.Now().UTC()
	if len(os.Args) >= 3 {
		var err error
		baseTime, err = time.Parse(time.RFC3339, os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid base_time: %v\n", err)
			os.Exit(1)
		}
	}

	days := 1
	if len(os.Args) >= 4 {
		fmt.Sscanf(os.Args[3], "%d", &days)
	}
	totalSteps := StepsPerDay * days

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, DBDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to DB: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "failed to ping DB: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Generating %s signal (step=%.1fh, period=%.0fh, days=%d, steps=%d)...\n",
		mode, StepHours, PeriodHours, days, totalSteps)

	rows := make([][]interface{}, 0, totalSteps)
	for i := 0; i < totalSteps; i++ {
		// x is continuous across days: step 240 = x=24h, same as x=0
		x := math.Mod(float64(i)*StepHours, PeriodHours)
		var y float64
		switch mode {
		case "sin":
			y = 100 * math.Sin(math.Pi*x/PeriodHours)
		case "parabola":
			// Parabola peak at x=12, max=100 at x=12
			// y = -100/144 * (x - 12)^2 + 100
			y = -100.0/144.0*(x-12)*(x-12) + 100
		default:
			fmt.Fprintf(os.Stderr, "unknown mode: %s\n", mode)
			os.Exit(1)
		}

		t := baseTime.Add(time.Duration(i) * time.Duration(StepHours*float64(time.Hour)))
		rows = append(rows, []interface{}{t, AgentID, MetricName, y, `{}`, "gauge"})
	}

	// Truncate existing test data
	_, _ = pool.Exec(ctx, `DELETE FROM metrics WHERE agent_id=$1 AND name=$2`, AgentID, MetricName)
	fmt.Printf("Inserted %d rows for %s mode\n", len(rows), mode)

	batchSize := 100
	for i := 0; i < len(rows); i += batchSize {
		end := i + batchSize
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[i:end]

		values := make([]string, 0, len(batch))
		args := make([]interface{}, 0, len(batch)*6)
		colIdx := 1
		for _, row := range batch {
			values = append(values, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d)", colIdx, colIdx+1, colIdx+2, colIdx+3, colIdx+4, colIdx+5))
			colIdx += 6
			args = append(args, row...)
		}

		query := fmt.Sprintf(
			"INSERT INTO metrics (time, agent_id, name, value, labels, metric_type) VALUES %s",
			join(values, ","),
		)
		_, err = pool.Exec(ctx, query, args...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "insert error at batch %d: %v\n", i/batchSize, err)
			os.Exit(1)
		}
	}

	fmt.Printf("Done: %d rows inserted\n", len(rows))
}

func join(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for i := 1; i < len(parts); i++ {
		result += sep + parts[i]
	}
	return result
}
