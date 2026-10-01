//go:build sqlite

package main

import (
	"path/filepath"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// Settings shows the tag blocks carry: Forge Solo's default when none was chosen, including the
// old default "Forge" that a 1.0.11 install saved without anyone choosing it.
func TestSettingsShowTheDefaultTagWhenNoneIsChosen(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "api.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	t.Setenv("POOL_ADDRESS", "")
	t.Setenv("COINBASE_TAG", "")
	for stored, want := range map[string]string{"": mining.DefaultCoinbaseTag, "Forge": mining.DefaultCoinbaseTag, "MyRig": "MyRig"} {
		if err := stats.SavePoolConfig("", "", stored); err != nil {
			t.Fatal(err)
		}
		if got := getJSON(t, getPoolConfig, "/pool/config")["coinbase_tag"]; got != want {
			t.Errorf("stored tag %q: Settings shows %v, want %q", stored, got, want)
		}
	}
}
