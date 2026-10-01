package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// The 1175 payout processor starts once however often merge-mining is switched back on: each
// TIDES-to-solo round trip started another copy.
func TestThe1175PayoutProcessorStartsOnce(t *testing.T) {
	var started atomic.Int32
	run1175Processor = func() { started.Add(1) }
	for i := 0; i < 3; i++ {
		start1175PayoutProcessorOnce()
	}
	time.Sleep(100 * time.Millisecond)
	if n := started.Load(); n != 1 {
		t.Fatalf("started %d times, want once", n)
	}
}
