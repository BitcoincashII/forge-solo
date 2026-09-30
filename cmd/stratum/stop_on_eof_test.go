package main

import (
	"io"
	"os"
	"syscall"
	"testing"
	"time"
)

// Forge Solo for Windows cannot signal the stratum, so it closes the stratum's stdin to stop it:
// the stratum must then shut down as it does on SIGTERM (sending its queued TIDES shares), not be
// killed with them.
func TestStopOnEOFAsksForShutdown(t *testing.T) {
	r, w := io.Pipe()
	stop := make(chan os.Signal, 1)
	go stopOnEOF(r, stop)
	select {
	case s := <-stop:
		t.Fatalf("asked to stop (%v) before stdin closed", s)
	case <-time.After(100 * time.Millisecond):
	}
	w.Close()
	select {
	case s := <-stop:
		if s != syscall.SIGTERM {
			t.Fatalf("got %v, want SIGTERM", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing stdin did not ask for shutdown")
	}
}

// A shutdown already asked for (a real signal first) must not be blocked on.
func TestStopOnEOFDoesNotBlockOnAPendingStop(t *testing.T) {
	r, w := io.Pipe()
	stop := make(chan os.Signal, 1)
	stop <- syscall.SIGINT
	done := make(chan struct{})
	go func() { stopOnEOF(r, stop); close(done) }()
	w.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stopOnEOF blocked on a full stop channel")
	}
}
