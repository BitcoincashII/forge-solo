package main

import (
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"go.uber.org/zap"
)

// The dashboard's Workers tile and banner read mining-status' authorized and connections, and its
// rentals the public stats. With MiningRigRentals' health checks counted, one rented rig read as 2
// or 3 workers "authorized and submitting shares", and the health checks alone, before the rig
// came or while it reconnected, as a miner to check for hashing instead of no miner.
func TestMiningStatusLeavesHealthChecksOut(t *testing.T) {
	const payout = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"
	savedMain, savedRental, savedLogger := stratumServer, stratumRentalServer, logger
	t.Cleanup(func() { stratumServer, stratumRentalServer, logger = savedMain, savedRental, savedLogger })
	logger = zap.NewNop()
	srv := stratum.NewServer(&stratum.ServerConfig{Host: "127.0.0.1", Port: 0, MaxConnections: 8, ExtraNonce1Size: 4,
		ExtraNonce2Size: 8, MinDiff: 500000, MaxDiff: 1e12, IsRentalPort: true, SoloOnly: true, CreditPayoutAddress: true},
		zap.NewNop(), nil, nil)
	srv.SetSoloPayoutAddress(payout)
	stratumServer, stratumRentalServer = nil, srv
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)

	minerLogin(t, srv.ListenAddr(), "infinite-hash-proxy/probe", payout+".mrr")
	minerLogin(t, srv.ListenAddr(), "infinite-hash-proxy/probe", payout+".mrr")
	for end := time.Now().Add(5 * time.Second); srv.CountAuthorized() != 2; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the health checks did not log in")
		}
	}
	st := buildMiningStatus()
	if st.Connections != 0 || st.Authorized != 0 {
		t.Errorf("PROBE-STATUS-COUNTS: two health checks read as %d connections, %d authorized", st.Connections, st.Authorized)
	}
	if got := miningStatusFrom(true, true, st.Connections, st.Authorized, 1000, time.Now(), time.Time{}, "", time.Now()); got.Reason != "no_miners" {
		t.Errorf("PROBE-STATUS-NO-MINERS: with only health checks connected the dashboard says %q, want no_miners", got.Reason)
	}
	if r := sumRentalStats(stratumServer, stratumRentalServer); r.TotalRentals != 0 {
		t.Errorf("PROBE-STATUS-RENTALS: two health checks read as %d rentals", r.TotalRentals)
	}

	minerLogin(t, srv.ListenAddr(), "bosminer-plus-tuner 0.9.3-5a7fd334", payout+".mrr")
	for end := time.Now().Add(5 * time.Second); srv.CountAuthorized() != 3; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the rig did not log in")
		}
	}
	if st := buildMiningStatus(); st.Connections != 1 || st.Authorized != 1 {
		t.Errorf("PROBE-STATUS-RIG: a rig and two health checks read as %d connections, %d authorized; want 1 and 1",
			st.Connections, st.Authorized)
	}
}
