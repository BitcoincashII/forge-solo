package main

import (
	"os"
	"testing"
	"time"
)

// The database stores every time in UTC. These tests run five hours behind UTC, as a PC in the
// Americas does, so a time stored in the local zone reads five hours off.
func TestMain(m *testing.M) {
	time.Local = time.FixedZone("UTC-5", -5*60*60)
	os.Exit(m.Run())
}
