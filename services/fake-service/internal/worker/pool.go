package worker

import (
	"context"
	"log/slog"
	"time"
)

// OpsRecorder is satisfied by metrics.Registry.
type OpsRecorder interface {
	AddWorkerOps(n int64)
}

// Pool runs a fixed number of goroutines that do bounded CPU work.
type Pool struct {
	count    int
	recorder OpsRecorder
	logger   *slog.Logger
}

func NewPool(count int, recorder OpsRecorder, logger *slog.Logger) *Pool {
	return &Pool{count: count, recorder: recorder, logger: logger}
}

func (p *Pool) Start(ctx context.Context) {
	for i := range p.count {
		go p.run(ctx, i)
	}
	p.logger.Info("worker pool started", "workers", p.count)
}

func (p *Pool) run(ctx context.Context, id int) {
	p.logger.Debug("worker started", "id", id)
	for {
		select {
		case <-ctx.Done():
			p.logger.Debug("worker stopped", "id", id)
			return
		default:
			ops := countPrimes(2, 5_000) // ~600 primes; bounded, no allocation
			p.recorder.AddWorkerOps(int64(ops))
			time.Sleep(10 * time.Millisecond) // yield; keeps CPU usage reasonable
		}
	}
}

// countPrimes returns the number of primes in [from, to] via trial division.
// Input is always bounded so there is no risk of runaway CPU or memory use.
func countPrimes(from, to int) int {
	n := 0
	for candidate := from; candidate <= to; candidate++ {
		if isPrime(candidate) {
			n++
		}
	}
	return n
}

func isPrime(n int) bool {
	if n < 2 {
		return false
	}
	for i := 2; i*i <= n; i++ {
		if n%i == 0 {
			return false
		}
	}
	return true
}
