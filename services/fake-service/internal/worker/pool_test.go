package worker

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRecorder implements OpsRecorder for testing.
type fakeRecorder struct{ total atomic.Int64 }

func (f *fakeRecorder) AddWorkerOps(n int64) { f.total.Add(n) }

// --- isPrime ---

func TestIsPrime(t *testing.T) {
	cases := []struct {
		n    int
		want bool
	}{
		{-1, false}, {0, false}, {1, false},
		{2, true}, {3, true}, {4, false},
		{17, true}, {18, false}, {97, true}, {100, false},
	}
	for _, c := range cases {
		if got := isPrime(c.n); got != c.want {
			t.Errorf("isPrime(%d) = %v, want %v", c.n, got, c.want)
		}
	}
}

// --- countPrimes ---

func TestCountPrimes(t *testing.T) {
	cases := []struct {
		from, to, want int
	}{
		{2, 10, 4},   // 2, 3, 5, 7
		{2, 2, 1},    // just 2
		{4, 6, 1},    // just 5
		{8, 10, 0},   // no primes
		{2, 100, 25}, // well-known result
	}
	for _, c := range cases {
		got := countPrimes(c.from, c.to)
		if got != c.want {
			t.Errorf("countPrimes(%d, %d) = %d, want %d", c.from, c.to, got, c.want)
		}
	}
}

// --- Pool ---

func TestPool_AccumulatesOps(t *testing.T) {
	rec := &fakeRecorder{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := NewPool(2, rec, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	pool.Start(ctx)
	<-ctx.Done()
	time.Sleep(20 * time.Millisecond) // allow goroutines to finish current iteration

	if rec.total.Load() == 0 {
		t.Fatal("expected worker_ops > 0 after running")
	}
}

func TestPool_StopsOnContextCancel(t *testing.T) {
	rec := &fakeRecorder{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := NewPool(1, rec, logger)

	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)
	time.Sleep(30 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond) // let the goroutine exit

	before := rec.total.Load()
	time.Sleep(50 * time.Millisecond) // worker must not add more ops after cancel
	after := rec.total.Load()

	if after > before {
		t.Errorf("worker kept running after context cancel: ops went from %d to %d", before, after)
	}
}
