//go:build sqlite

package main

import (
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

// auxNodeConfig is a stratum config whose 1175 node is srv.
func auxNodeConfig(t *testing.T, srv *httptest.Server, enabled bool) *viper.Viper {
	t.Helper()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(port)
	cfg := viper.New()
	cfg.Set("mergemining.enabled", enabled)
	cfg.Set("mergemining.aux_node.host", host)
	cfg.Set("mergemining.aux_node.port", p)
	cfg.Set("mergemining.aux_node.user", "u")
	cfg.Set("mergemining.aux_node.pass", "p")
	return cfg
}

// The 1175 payout processor runs whenever the database is up and the install has a 1175 node,
// with merge mining off: in TIDES mode, or with the 1175 address cleared. It ran only with merge
// mining on, so after a restart in TIDES mode the 1175 blocks found before stayed Pending, and an
// orphaned one kept counting in the 1175 totals, until the user went back to solo with an address.
func TestThe1175ProcessorRunsWithMergeMiningOff(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "aux.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	node := &auxConfNode{confs: map[string]float64{}}
	srv := httptest.NewServer(node)
	defer srv.Close()

	savedURL, savedUser, savedPass, savedOn, savedRun, savedLogger := aux1175NodeURL, aux1175User, aux1175Pass, merge1175Enabled, run1175Processor, logger
	t.Cleanup(func() {
		aux1175NodeURL, aux1175User, aux1175Pass, merge1175Enabled, run1175Processor, logger = savedURL, savedUser, savedPass, savedOn, savedRun, savedLogger
		payout1175Once = sync.Once{}
	})
	logger = zap.NewNop()
	started := make(chan struct{}, 4)
	run1175Processor = func() { started <- struct{}{} }

	// Forge Solo for Linux: no 1175 node, nothing starts.
	aux1175NodeURL, merge1175Enabled, payout1175Once = "", false, sync.Once{}
	start1175Ledger(auxNodeConfig(t, srv, false))
	select {
	case <-started:
		t.Fatal("DATA3-NO-NODE: the 1175 processor started on an install without a 1175 node")
	case <-time.After(100 * time.Millisecond):
	}

	// A 1175 node, merge mining off (booted in TIDES mode, or no 1175 address).
	aux1175NodeURL, merge1175Enabled, payout1175Once = "", false, sync.Once{}
	start1175Ledger(auxNodeConfig(t, srv, true))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("DATA3-1175-PROCESSOR: with merge mining off, the 1175 processor never started")
	}

	// And it reaches the node: a mature block is confirmed, an orphaned one marked.
	const finder = "bitcoincashii:qfinder1175"
	mature, orphan := strings.Repeat("1", 64), strings.Repeat("2", 64)
	node.set(mature, 150)
	node.set(orphan, -1)
	for h, hash := range map[int64]string{700: mature, 701: orphan} {
		if err := stats.Record1175Block(h, hash, 25, finder, true); err != nil {
			t.Fatal(err)
		}
		if err := stats.Distribute1175Block(h, 0); err != nil {
			t.Fatal(err)
		}
	}
	run1175PayoutCycle()
	if pending, _ := stats.UnconfirmedBlocks1175(); len(pending) != 0 {
		t.Fatalf("DATA3-1175-NODE: the processor did not reach the configured 1175 node; still pending: %v", pending)
	}
	if n, paid, _ := stats.Miner1175Totals(finder, true); n != 1 || paid != 25 {
		t.Fatalf("DATA3-1175-TOTALS: %d blocks, %v paid; want the mature one alone", n, paid)
	}
}

// A database that only comes up after boot starts the 1175 processor too.
func TestALateDatabaseStartsThe1175Processor(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	i := strings.Index(src, "func watchPoolConfig(")
	j := strings.Index(src[i:], "pool, p1175, tag, err := stats.GetPoolConfig()")
	if i < 0 || j < 0 {
		t.Fatal("watchPoolConfig's late database branch not found")
	}
	if !strings.Contains(src[i:i+j], "start1175Ledger(cfg)") {
		t.Fatal("DATA3-LATE-DB: a database that comes up after boot does not start the 1175 processor")
	}
}
