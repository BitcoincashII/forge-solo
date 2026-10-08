package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The settings password never reaches launcher.log: not when the tray makes it at a first start,
// starts the gateway with it, restarts the gateway, hears why the gateway stopped, or stops.
func TestNoSecretReachesLauncherLog(t *testing.T) {
	w := gatewayWorld(t, "args")
	statusPageWait = 200 * time.Millisecond
	sec = secrets{}
	_ = os.Remove(dpath("secrets.env"))
	if err := prepare(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	made := sec.Settings
	if len(made) != 64 {
		t.Fatalf("setup: the tray made a settings password of %d characters", len(made))
	}
	boot()
	if !waitFor(10*time.Second, func() bool { return w.has("args") }) {
		t.Fatal("setup: the gateway did not start")
	}
	if b, _ := os.ReadFile(filepath.Join(w.dir, "args")); !strings.Contains(string(b), "SETTINGS_PASSWORD="+made) {
		t.Fatal("setup: the gateway was not given the settings password the tray made")
	}
	restartGateway()
	// The gateway's last words when it stops on its own are logged: here, a config mistake.
	t.Setenv("GWL_HELPER", "exit3")
	startSupervising(t)
	stopNow(gatewayKey)
	if err := startGateway(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if !waitFor(10*time.Second, logHas("forge-gateway.exe said:")) {
		t.Fatalf("setup: the gateway's last words were not logged:\n%s", launcherLog())
	}
	stopEverything()
	log := launcherLog()
	if len(strings.Split(log, "\n")) < 5 {
		t.Fatalf("setup: launcher.log has too little to look at:\n%s", log)
	}
	for _, s := range []string{made, testPassword} {
		if strings.Contains(log, s) {
			t.Errorf("GWL-SECRET-LOG: launcher.log holds the settings password:\n%s", log)
		}
	}
}
