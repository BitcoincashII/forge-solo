//go:build sqlite

package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// outage is how long the database is down in these tests: long enough that a block listed when the
// database came back is listed seconds after it was found.
const outage = 2500 * time.Millisecond

func near(unix int64, at time.Time) bool {
	d := unix - at.Unix()
	return d >= -1 && d <= 1
}

// A block recorded once the database is back is listed, with its payout, at the time it was found.
// It was listed at the time the database came back.
func TestABlockRecordedAfterAnOutageKeepsItsFoundTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.db")
	if err := stats.InitDB(path); err != nil {
		t.Fatal(err)
	}
	shortRetries(t)
	blockRecordRetryFor = 10 * time.Second // outlasts the outage; shortRetries restores it
	stats.CloseDB()
	found := time.Now()
	findBlock(t, &fakeNode{}, 307)
	time.Sleep(outage)
	if err := stats.InitDB(path); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	deadline := time.Now().Add(3 * time.Second)
	for !recorded(307) {
		if time.Now().After(deadline) {
			t.Fatal("DATA5-SETUP: the block was never recorded")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, b := range stats.GetMinerSoloBlocksDB(blockTestPayout) {
		if b.Height == 307 && !near(b.Time, found) {
			t.Fatalf("DATA5-BLOCK-TIME: found at %d, listed at %d", found.Unix(), b.Time)
		}
	}
	payouts, _, _, err := stats.SoloPayoutsSummary(blockTestPayout)
	if err != nil || len(payouts) != 1 {
		t.Fatalf("DATA5-SETUP: payouts %+v, %v", payouts, err)
	}
	if !near(payouts[0].PaidAt.Unix(), found) {
		t.Fatalf("DATA5-PAYOUT-TIME: found at %d, its payout listed at %d", found.Unix(), payouts[0].PaidAt.Unix())
	}
}

// The same for a 1175 block.
func TestA1175BlockRecordedAfterAnOutageKeepsItsFoundTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aux.db")
	if err := stats.InitDB(path); err != nil {
		t.Fatal(err)
	}
	shortRetries(t)
	blockRecordRetryFor = 10 * time.Second
	_, logs := useAuxConfNode(t)
	const finder = "bitcoincashii:qfinder1175"
	stats.CloseDB()
	found := time.Now()
	aux1175BlockHandler(910, strings.Repeat("e", 64), 25_0000_0000, finder, true)
	time.Sleep(outage)
	if err := stats.InitDB(path); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if n, _, _ := stats.Miner1175Totals(finder, true); n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("DATA5-SETUP: the 1175 block was never recorded")
		}
		time.Sleep(20 * time.Millisecond)
	}
	blocks, err := stats.Get1175BlocksForMiner(finder, true, 10)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("DATA5-SETUP: %+v, %v", blocks, err)
	}
	if !near(blocks[0].Time, found) {
		t.Fatalf("DATA5-1175-TIME: found at %d, listed at %d", found.Unix(), blocks[0].Time)
	}
	waitForLog(t, logs, "DATA5-1175-DONE", "1175 block distributed") // the retry's last word, before the test's cleanup
}
