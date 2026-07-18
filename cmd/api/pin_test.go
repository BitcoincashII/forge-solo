package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPinLockoutAtomic proves the concurrency fix for the PIN brute-force lockout: a
// burst of parallel attempts against one address must not let more than pinMaxFails
// through before the address locks (the pre-fix check-then-act allowed all of them).
func TestPinLockoutAtomic(t *testing.T) {
	const addr = "bitcoincashii:qtestlockout00000000000000000000000000000"
	pinFailMu.Lock()
	pinFailCount = map[string]int{}
	pinFailUntil = map[string]time.Time{}
	pinFailMu.Unlock()

	var allowed int64
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if pinBeginAttempt(addr) {
				atomic.AddInt64(&allowed, 1)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&allowed); got > pinMaxFails {
		t.Fatalf("lockout not atomic: %d concurrent attempts allowed (want <= %d)", got, pinMaxFails)
	}
	// Locked now: further attempts denied.
	if pinBeginAttempt(addr) {
		t.Fatal("attempt allowed while locked")
	}
	// A successful PIN clears the counter; attempts allowed again.
	pinClearFail(addr)
	if !pinBeginAttempt(addr) {
		t.Fatal("attempt denied after pinClearFail")
	}
	t.Logf("✅ lockout atomic: <= %d attempts before lock, clear resets", pinMaxFails)
}
