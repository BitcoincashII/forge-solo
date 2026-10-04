//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// The API is started with the Settings password and told it runs on Windows, so Settings asks for
// that password and says where to find it.
func TestAPIGetsTheSettingsPassword(t *testing.T) {
	savedInst, savedData, savedSec := installDir, dataDir, sec
	installDir, dataDir = t.TempDir(), t.TempDir()
	sec = secrets{Settings: strings.Repeat("ab", 32)}
	t.Cleanup(func() { installDir, dataDir, sec = savedInst, savedData, savedSec; stop("api") })
	out := dpath("api-env.txt")
	if err := os.WriteFile(ipath("api.exe"), []byte("#!/bin/sh\nenv > '"+out+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	startAPI()
	var env []byte
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && len(env) == 0; time.Sleep(50 * time.Millisecond) {
		env, _ = os.ReadFile(out)
	}
	for _, want := range []string{"SETTINGS_PASSWORD=" + sec.Settings, "FORGE_PLATFORM=windows"} {
		if !strings.Contains(string(env), want+"\n") {
			t.Errorf("API-ENV: the API was not started with %s", want)
		}
	}
}

// The API and the miner keep their data in one SQLite file, forgesolo.db in the data folder, as on
// Umbrel and Linux: both are given the same DB_PATH, and nothing about a PostgreSQL server, which
// no longer runs.
func TestServicesShareOneSQLiteFile(t *testing.T) {
	savedInst, savedData := installDir, dataDir
	installDir, dataDir = t.TempDir(), t.TempDir()
	t.Cleanup(func() { installDir, dataDir = savedInst, savedData; stop("api"); stop("stratum") })
	for _, exe := range []string{"api.exe", "stratum.exe"} {
		if err := os.WriteFile(ipath(exe), []byte("#!/bin/sh\nenv > '"+dpath(exe+".env")+"'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := startAPI(); err != nil {
		t.Fatal(err)
	}
	if err := startStratum(); err != nil {
		t.Fatal(err)
	}
	for _, exe := range []string{"api.exe", "stratum.exe"} {
		var env []byte
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && len(env) == 0; time.Sleep(50 * time.Millisecond) {
			env, _ = os.ReadFile(dpath(exe + ".env"))
		}
		if want := "DB_PATH=" + dpath("forgesolo.db"); !strings.Contains("\n"+string(env), "\n"+want+"\n") {
			t.Errorf("DB-SQLITE-PATH: %s was not started with %s", exe, want)
		}
		for _, l := range strings.Split(string(env), "\n") {
			if strings.HasPrefix(l, "DB_") && !strings.HasPrefix(l, "DB_PATH=") {
				t.Errorf("DB-NO-POSTGRES: %s was started with %s", exe, l)
			}
		}
	}
}
