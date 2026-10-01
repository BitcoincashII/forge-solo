package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/mining"
)

// A config that does not name a tag gives Forge Solo's default.
func TestConfigWithoutATagGivesTheDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("pool:\n  name: \"Forge Solo\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.GetString("pool.coinbase_tag"); got != mining.DefaultCoinbaseTag {
		t.Errorf("pool.coinbase_tag = %q, want %q", got, mining.DefaultCoinbaseTag)
	}
}
