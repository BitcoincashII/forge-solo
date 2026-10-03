package main

import (
	"strings"
	"testing"
)

// The API is started with DASHBOARD_PASSWORD as its settings password, and told it runs on Linux,
// so Settings asks for it and says where it is: other accounts on this machine can reach the API.
func TestAPIGetsTheSettingsPassword(t *testing.T) {
	env := strings.Join(apiEnv("/data", "/opt/forge-solo", ports{RPC: 1, API: 2, Stats: 3}, secrets{DashboardPassword: "dpw"}), "\n") + "\n"
	for _, want := range []string{"SETTINGS_PASSWORD=dpw", "FORGE_PLATFORM=linux", "API_LISTEN_HOST=127.0.0.1", "API_LISTEN_PORT=2"} {
		if !strings.Contains(env, want+"\n") {
			t.Errorf("API-ENV: the API is not started with %s", want)
		}
	}
}
