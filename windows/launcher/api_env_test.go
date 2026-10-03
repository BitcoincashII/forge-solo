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
