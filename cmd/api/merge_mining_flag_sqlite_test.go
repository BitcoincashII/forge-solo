package main

import (
	"path/filepath"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// /pool/config tells the dashboard whether this install runs a 1175 node (see
// TestMergeMiningAvailableFlag).
func TestPoolConfigReportsMergeMiningAvailable(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "api.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	t.Setenv("POOL_ADDRESS", "")
	for env, want := range map[string]bool{"": true, "0": false} {
		t.Setenv("MERGE_MINING_AVAILABLE", env)
		if cfg := getJSON(t, getPoolConfig, "/pool/config"); cfg["merge_mining_available"] != want {
			t.Fatalf("MERGE_MINING_AVAILABLE=%q: merge_mining_available = %v, want %v", env, cfg["merge_mining_available"], want)
		}
	}
}
