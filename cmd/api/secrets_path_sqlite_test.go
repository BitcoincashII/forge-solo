//go:build sqlite

package main

import (
	"path/filepath"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// The settings read tells a Linux Settings page which secrets.env holds the password, and tells
// no other platform any path.
func TestPoolConfigNamesTheSecretsFileOnLinux(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "api.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	t.Setenv("POOL_ADDRESS", "")
	t.Setenv("DB_PATH", "/var/lib/forge-solo/forgesolo.db")

	t.Setenv("FORGE_PLATFORM", "linux")
	if got := getJSON(t, getPoolConfig, "/pool/config")["secrets_path"]; got != "/var/lib/forge-solo/secrets.env" {
		t.Errorf("SECRETS-PATH-READ: on Linux the settings read gives secrets_path %v", got)
	}
	for _, p := range []string{"windows", ""} {
		t.Setenv("FORGE_PLATFORM", p)
		if got, ok := getJSON(t, getPoolConfig, "/pool/config")["secrets_path"]; ok {
			t.Errorf("SECRETS-PATH-READ-ELSEWHERE: FORGE_PLATFORM=%q gives secrets_path %v", p, got)
		}
	}
}
