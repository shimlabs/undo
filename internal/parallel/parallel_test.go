package parallel

import (
	"errors"
	"sync/atomic"
	"testing"
)

func TestForCallsEveryIndex(t *testing.T) {
	var sum atomic.Int64
	if err := For(1000, func(i int) error {
		sum.Add(int64(i))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if sum.Load() != 999*1000/2 {
		t.Fatalf("sum = %d", sum.Load())
	}
}

func TestForReturnsErrorAndStops(t *testing.T) {
	failure := errors.New("failed")
	var calls atomic.Int64
	err := For(10_000, func(int) error {
		calls.Add(1)
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("For = %v, want the failure", err)
	}
	if calls.Load() > concurrency {
		t.Fatalf("%d calls after the first error, want at most %d", calls.Load(), concurrency)
	}
}
