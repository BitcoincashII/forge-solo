//go:build sqlite

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// startLog runs the stratum's main() on a fresh database in payout mode mode, with the 1175 address
// esf saved in the dashboard settings and mergemining.enabled true, as Windows and Umbrel ship it,
// until it has logged its vardiff configuration, and returns the lines it logged: message -> level.
// Nothing it talks to is real: the nodes and Forge Pool are a port nothing listens on.
func startLog(t *testing.T, mode, esf string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	db := filepath.Join(data, "forgesolo.db")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := stats.InitDB(db); err != nil {
		t.Fatal(err)
	}
	if err := stats.SavePoolSettings("", esf, "", mode); err != nil {
		t.Fatal(err)
	}
	stats.CloseDB()

	config := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf(`stratum:
  host: 127.0.0.1
  port: %s
  extranonce1_size: 4
  extranonce2_size: 8
node:
  host: 127.0.0.1
  port: 9
  zmq_endpoint: ""
pool:
  payout_scheme: solo
mergemining:
  enabled: true
  payout_address: ""
  aux_node:
    host: 127.0.0.1
    port: 9
logging:
  level: info
  format: json
`, unusedPort(t))), 0o600); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "PAYOUT_ADDRESS") && !strings.HasPrefix(e, "DATUM_POOL_URL=") {
			env = append(env, e)
		}
	}
	cmd := exec.Command(os.Args[0], "-config", config)
	cmd.Env = append(env, stratumMainEnv+"=1", "DB_PATH="+db, "INTERNAL_STATS_HOST=127.0.0.1",
		"INTERNAL_STATS_PORT="+unusedPort(t), "RPC_USER=forge", "RPC_PASSWORD=x", "FORGE_STOP_ON_STDIN_EOF=",
		"API_HOST=127.0.0.1", "API_PORT=9", "DATUM_POOL_URL=http://127.0.0.1:9")
	out := &outputBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		cmd.Process.Kill()
		<-done
	}()
	for deadline := time.Now().Add(90 * time.Second); !strings.Contains(out.String(), "Vardiff configuration"); time.Sleep(50 * time.Millisecond) {
		select {
		case err := <-done:
			done <- err
			t.Fatalf("the stratum ended (%v) before it started:\n%s", err, out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("in 90 s the stratum did not start:\n%s", out.String())
		}
	}
	lines := map[string]string{}
	for _, l := range strings.Split(out.String(), "\n") {
		var e struct{ Level, Msg string }
		if json.Unmarshal([]byte(l), &e) == nil && e.Msg != "" {
			lines[e.Msg] = e.Level
		}
	}
	return lines
}

// logged is the level of the first line in lines that contains part, or "".
func logged(lines map[string]string, part string) string {
	for msg, level := range lines {
		if strings.Contains(msg, part) {
			return level
		}
	}
	return ""
}

// TIDES mode mines BCH2 only, whatever the 1175 address. With none set, every start in TIDES mode
// warned that 1175 merge-mining was off "until you set it in the dashboard", which would not turn it
// on. In TIDES mode the start says merge-mining stays off; the missing-address warning is solo's.
func TestTheStartSaysWhy1175MergeMiningIsOff(t *testing.T) {
	const esf = "esf1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"
	const warning = "1175 merge-mining is OFF until you set it in the dashboard"
	const staysOff = "1175 merge-mining stays OFF while TIDES mode is on"

	tides := startLog(t, stats.PayoutModeTides, "")
	if got := logged(tides, staysOff); got != "info" {
		t.Errorf("MM-TIDES-SAYS-OFF: in TIDES mode with no 1175 address the start logged %q at level %q, want info", staysOff, got)
	}
	if got := logged(tides, warning); got != "" {
		t.Errorf("MM-TIDES-NO-WARN: in TIDES mode the start still warns to set the 1175 address (level %q)", got)
	}

	tidesSet := startLog(t, stats.PayoutModeTides, esf)
	if got := logged(tidesSet, staysOff); got != "info" {
		t.Errorf("MM-TIDES-SET-SAYS-OFF: in TIDES mode with a 1175 address the start logged %q at level %q, want info", staysOff, got)
	}

	solo := startLog(t, stats.PayoutModeSolo, "")
	if got := logged(solo, warning); got != "warn" {
		t.Errorf("MM-SOLO-WARNS: in solo with no 1175 address the start logged the warning at level %q, want warn", got)
	}
	if got := logged(solo, staysOff); got != "" {
		t.Errorf("MM-SOLO-NOT-TIDES: in solo the start says %q", staysOff)
	}
}
