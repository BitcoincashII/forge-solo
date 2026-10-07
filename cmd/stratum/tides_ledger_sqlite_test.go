package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/datum/wire"
	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"github.com/BitcoincashII/forge-solo/internal/tides"
	"go.uber.org/zap"
)

// A block found on a TIDES job is Forge Pool's: its coinbase paid the TIDES split. It must not
// enter this install's solo ledger, which would list it beside solo wins at a part of the reward
// and count that part in the balance -- found in a live regtest run, where the first TIDES block
// landed in the blocks table because the recorder also wrote the database. A solo block, on the
// same path, still must.
func TestTidesBlockStaysOutOfTheSoloLedger(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "ledger.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()

	// A node that takes every block.
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Write([]byte(`{"result":null,"error":null,"id":"submit"}`))
	}))
	defer node.Close()
	savedURL, savedLogger := rpcURL, logger
	rpcURL, logger = node.URL, zap.NewNop()
	defer func() { rpcURL, logger = savedURL, savedLogger }()

	const payout = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"
	cb1, cb2, err := wire.BuildCoinbase(200, []byte("test"), []tides.Output{{Address: payout, Sats: 50_0000_0000}})
	if err != nil {
		t.Fatal(err)
	}
	mine := func(id string, height int64, tides bool) {
		job := &mining.Job{ID: id, Height: height, CoinBase1: cb1, CoinBase2: cb2, Version: "20000000", NBits: "207fffff",
			NTime: "66f8a1b2", PrevBlockHash: "00000000000000000000000000000000000000000000000000000000000000aa",
			CoinbaseValue: 50_0000_0000, Tides: tides, TidesFinderSats: 12_0000_0000}
		jobHistoryMu.Lock()
		jobHistory[id] = job
		jobHistoryMu.Unlock()
		share := &stratum.Share{JobID: id, MinerID: payout, WorkerName: "rig1", ExtraNonce1: "00000001",
			ExtraNonce2: "0000000000000001", NTime: "66f8a1b2", Nonce: "00000000", IsSolo: true}
		(&BlockFindingShareProcessor{logger: zap.NewNop()}).submitBlock(share)
	}
	rows := func(height int64) (blocks, payouts int) {
		for _, b := range stats.GetMinerBlocksDB(payout) {
			if b.Height == height {
				blocks++
			}
		}
		_, n, _ := stats.GetMinerSoloPayoutsDB(payout)
		return blocks, n
	}

	mine("tides-job", 201, true)
	if b, p := rows(201); b != 0 || p != 0 {
		t.Fatalf("TIDES-LEDGER: a TIDES block entered the solo ledger (%d block rows, %d payouts)", b, p)
	}
	mine("solo-job", 202, false)
	if b, p := rows(202); b != 1 || p != 1 {
		t.Fatalf("TIDES-LEDGER-SOLO: a solo block was not recorded (%d block rows, %d payouts)", b, p)
	}
}
