//go:build sqlite

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/spf13/viper"
)

// The 1175 address and 1175 records of a database brought from Umbrel or Windows.
const (
	movedEsf    = "esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x"
	movedFinder = "bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2"
)

// seedMoved1175 records what such a database holds: a 1175 block found and not credited yet, and
// one the 1175 node confirmed whose credit is not settled yet.
func seedMoved1175(t *testing.T) {
	t.Helper()
	for _, b := range []struct {
		height int64
		hash   string
	}{{5002, strings.Repeat("a2", 32)}, {5010, strings.Repeat("b8", 32)}} {
		if err := stats.Record1175Block(b.height, b.hash, 0.78125, movedFinder, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := stats.Distribute1175Block(5010, 0); err != nil {
		t.Fatal(err)
	}
	if err := stats.Confirm1175Block(5010); err != nil {
		t.Fatal(err)
	}
}

// dump1175 is every 1175 block and credit, as text.
func dump1175(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	blocks, err := stats.UnconfirmedBlocks1175()
	if err != nil {
		t.Fatal(err)
	}
	undistributed, err := stats.UndistributedBlocks1175()
	if err != nil {
		t.Fatal(err)
	}
	payable, err := stats.ConfirmedPendingMiners1175()
	if err != nil {
		t.Fatal(err)
	}
	n, paid, err := stats.Miner1175Totals(movedFinder, true)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&b, "pending %v undistributed %v payable %v totals %d %v", blocks, undistributed, payable, n, paid)
	return b.String()
}

// mmRun is the stratum run by its own main() on such a database, in solo, with mm as the config's
// mergemining section.
type mmRun struct {
	out           *outputBuffer
	cmd           *exec.Cmd
	done          chan error
	db, statsPort string
	before        string
	stopped       sync.Once
}

func startWithMoved1175(t *testing.T, mm string) *mmRun {
	t.Helper()
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	r := &mmRun{out: &outputBuffer{}, done: make(chan error, 1), db: filepath.Join(data, "forgesolo.db"), statsPort: unusedPort(t)}
	if err := stats.InitDB(r.db); err != nil {
		t.Fatal(err)
	}
	// The tag is the marker: the settings watcher logs it at its first look at the database, in
	// the same round as it would switch 1175 merge-mining on.
	if err := stats.SavePoolSettings("", movedEsf, "/moved/", stats.PayoutModeSolo); err != nil {
		t.Fatal(err)
	}
	seedMoved1175(t)
	r.before = dump1175(t)
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
%slogging:
  level: info
  format: json
`, unusedPort(t), mm)), 0o600); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "PAYOUT_ADDRESS") && !strings.HasPrefix(e, "DATUM_POOL_URL=") && !strings.HasPrefix(e, "INTERNAL_API_TOKEN=") {
			env = append(env, e)
		}
	}
	r.cmd = exec.Command(os.Args[0], "-config", config)
	r.cmd.Env = append(env, stratumMainEnv+"=1", "DB_PATH="+r.db, "INTERNAL_STATS_HOST=127.0.0.1",
		"INTERNAL_STATS_PORT="+r.statsPort, "INTERNAL_API_TOKEN=t", "RPC_USER=forge", "RPC_PASSWORD=x", "FORGE_STOP_ON_STDIN_EOF=",
		"DATUM_POOL_URL=http://127.0.0.1:9")
	r.cmd.Stdout, r.cmd.Stderr = r.out, r.out
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { r.done <- r.cmd.Wait() }()
	t.Cleanup(r.stop)
	return r
}

func (r *mmRun) stop() {
	r.stopped.Do(func() {
		r.cmd.Process.Kill()
		<-r.done
	})
}

// watched waits until the settings watcher has looked at the database once, and a moment more.
func (r *mmRun) watched(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(90 * time.Second); !strings.Contains(r.out.String(), "coinbase tag updated from dashboard"); time.Sleep(100 * time.Millisecond) {
		select {
		case err := <-r.done:
			r.done <- err
			t.Fatalf("the stratum ended (%v) before its settings watcher ran:\n%s", err, r.out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("in 90 s the settings watcher did not run:\n%s", r.out.String())
		}
	}
	time.Sleep(time.Second)
}

// mergeMining is what the stratum's mining status says of merge-mining.
func (r *mmRun) mergeMining(t *testing.T) string {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://127.0.0.1:"+r.statsPort+"/internal/mining-status", nil)
	req.Header.Set("X-Internal-Token", "t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st struct {
		MergeMining string `json:"merge_mining"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("mining status: %d %v", resp.StatusCode, err)
	}
	return st.MergeMining
}

// Forge Solo for Linux runs no 1175 node, and its database can still hold a 1175 address and 1175
// records: one brought from Umbrel or Windows, or an address saved through the API. Merge-mining
// came on all the same at the settings watcher's first look, against "http://:0", with a warning
// every minute that it had never worked and "never_worked" in the mining status, and the 1175
// payout processor started: two minutes in, it credited and settled that database's 1175 records
// with no 1175 node to ask. Without a 1175 node neither starts, and the records stay as they are.
func TestNo1175NodeNoMergeMining(t *testing.T) {
	cases := []struct {
		name, mm string
		node     bool
	}{
		{"linux", "mergemining:\n  enabled: false\n", false},
		{"no node address", "mergemining:\n  enabled: true\n  aux_node:\n    host: \"\"\n    port: 0\n", false},
		{"a 1175 node", "mergemining:\n  enabled: true\n  aux_node:\n    host: 127.0.0.1\n    port: 9\n", true},
	}
	runs := make([]*mmRun, len(cases))
	for i, c := range cases {
		runs[i] = startWithMoved1175(t, c.mm)
	}
	var wg sync.WaitGroup
	for _, r := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			deadline := time.Now().Add(90 * time.Second)
			for !strings.Contains(r.out.String(), "coinbase tag updated from dashboard") && time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
			}
		}()
	}
	wg.Wait()
	for i, c := range cases {
		r := runs[i]
		r.watched(t)
		mm := r.mergeMining(t)
		r.stop()
		out := r.out.String()
		on := strings.Contains(out, "Merge mining enabled") || strings.Contains(out, "merge-mining enabled from dashboard")
		processor := strings.Contains(out, "1175 payout processor started")
		if !c.node {
			if on {
				t.Errorf("MM-NONODE-ON: %s: with no 1175 node, merge-mining came on:\n%s", c.name, out)
			}
			if processor {
				t.Errorf("MM-NONODE-PROCESSOR: %s: with no 1175 node, the 1175 payout processor started", c.name)
			}
			if mm != "off" {
				t.Errorf("MM-NONODE-STATUS: %s: with no 1175 node, the mining status says merge-mining is %q, want off", c.name, mm)
			}
			if !strings.Contains(out, "This install runs no 1175 node") {
				t.Errorf("MM-NONODE-SAYS: %s: the start does not say the 1175 address is not used", c.name)
			}
			if err := stats.InitDB(r.db); err != nil {
				t.Fatal(err)
			}
			after := dump1175(t)
			stats.CloseDB()
			if after != r.before {
				t.Errorf("MM-NONODE-RECORDS: %s: the 1175 records changed:\nbefore %s\nafter  %s", c.name, r.before, after)
			}
			continue
		}
		// The control: with a 1175 node the same database switches merge-mining on, so the run
		// above would have shown it.
		if !on || !processor || mm != "never_worked" {
			t.Errorf("MM-NODE-CONTROL: with a 1175 node: merge-mining on %v, processor %v, status %q; want on, started, never_worked:\n%s", on, processor, mm, out)
		}
		if strings.Contains(out, "This install runs no 1175 node") {
			t.Errorf("MM-NODE-SAYS-NONE: with a 1175 node the start says there is none")
		}
	}
}

// What counts as a 1175 node: merge-mining enabled, with a host and a port.
func TestHas1175Node(t *testing.T) {
	for _, c := range []struct {
		code    string
		enabled bool
		host    string
		port    int
		want    bool
	}{
		{"MM-NODE-FLAG", false, "127.0.0.1", 25359, false},
		{"MM-NODE-HOST", true, " ", 25359, false},
		{"MM-NODE-PORT", true, "127.0.0.1", 0, false},
		{"MM-NODE-OK", true, "bch2-apps-forge-solo_node1175_1", 25359, true},
	} {
		cfg := viper.New()
		cfg.Set("mergemining.enabled", c.enabled)
		cfg.Set("mergemining.aux_node.host", c.host)
		cfg.Set("mergemining.aux_node.port", c.port)
		if got := has1175Node(cfg); got != c.want {
			t.Errorf("%s: enabled %v, host %q, port %d: has1175Node %v, want %v", c.code, c.enabled, c.host, c.port, got, c.want)
		}
	}
}

// With merge-mining enabled in the config but no 1175 node's address, the 1175 payout processor
// does not start either.
func TestNo1175NodeAddressNoProcessor(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "aux.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	savedURL, savedRun := aux1175NodeURL, run1175Processor
	t.Cleanup(func() {
		aux1175NodeURL, run1175Processor = savedURL, savedRun
		payout1175Once = sync.Once{}
	})
	started := make(chan struct{}, 1)
	run1175Processor = func() { started <- struct{}{} }
	aux1175NodeURL, payout1175Once = "", sync.Once{}
	cfg := viper.New()
	cfg.Set("mergemining.enabled", true)
	start1175Ledger(cfg)
	select {
	case <-started:
		t.Fatal("DATA3-NO-HOST: the 1175 processor started with no 1175 node's address")
	case <-time.After(100 * time.Millisecond):
	}
	if aux1175NodeURL != "" {
		t.Fatalf("DATA3-NO-HOST-URL: the processor was pointed at %q", aux1175NodeURL)
	}
}
