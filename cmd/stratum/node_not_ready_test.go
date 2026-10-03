package main

import (
	"errors"
	"testing"
)

// A node that is loading its chain, syncing or not yet connected is waited for, not logged as a
// fault; anything else still is.
func TestNodeNotReady(t *testing.T) {
	for msg, want := range map[string]bool{
		"rpc error: Bitcoin Cash II is in initial sync and waiting for blocks...": true,
		"rpc error: Loading block index…":                                        true,
		"rpc error: Bitcoin Cash II is not connected!":                           true,
		"Post \"http://127.0.0.1:8332\": dial tcp 127.0.0.1:8332: connect: connection refused": false,
		"rpc error: Method not found":                                            false,
	} {
		if got := nodeNotReady(errors.New(msg)); got != want {
			t.Errorf("NODE-NOT-READY: %q gives %v, want %v", msg, got, want)
		}
	}
}
