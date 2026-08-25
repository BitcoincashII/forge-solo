package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/bch2/forge-pool/internal/stratum"
)

// Rented hashpower arrives on 3335, not 3333.
//
// The /internal/rental-stats handler asked only stratumServer (port 3333) — the one port
// an aggregated rental order never arrives on. NiceHash and MiningRigRentals are pointed
// at 3335, so their connections live in stratumRentalServer's client map and were never
// counted, and the public "rentals" block in /api/v1/stats reported zeros to external
// aggregators while an order was actively mining.
func TestRentalStatsCountsBothStratumServers(t *testing.T) {
	main3333 := stratum.NewServerForTest()
	rental3335 := stratum.NewServerForTest()

	main3333.AddAuthorizedRentalClientForTest(stratum.RentalOther)
	rental3335.AddAuthorizedRentalClientForTest(stratum.RentalNiceHash)
	rental3335.AddAuthorizedRentalClientForTest(stratum.RentalNiceHash)
	rental3335.AddAuthorizedRentalClientForTest(stratum.RentalMRR)

	got := sumRentalStats(main3333, rental3335)

	if got.NiceHashMiners != 2 {
		t.Errorf("NiceHash = %d, want 2 — the 3335 listener is not being counted", got.NiceHashMiners)
	}
	if got.MRRMiners != 1 {
		t.Errorf("MRR = %d, want 1 — the 3335 listener is not being counted", got.MRRMiners)
	}
	if got.OtherRentals != 1 {
		t.Errorf("Other = %d, want 1 — the 3333 listener is not being counted", got.OtherRentals)
	}
	if got.TotalRentals != 4 {
		t.Errorf("Total = %d, want 4", got.TotalRentals)
	}

	// Asking only the miner port is the bug this replaced: it must not equal the total.
	if only3333 := sumRentalStats(main3333); only3333.TotalRentals == got.TotalRentals {
		t.Error("counting only the 3333 server gives the same answer; the fixture does not " +
			"actually distinguish the two servers, so this test proves nothing")
	}
}

// The rental listener is optional and this runs inside an HTTP handler: a nil server must
// be skipped, not dereferenced into a panic that takes the process down.
func TestRentalStatsToleratesAMissingRentalServer(t *testing.T) {
	main3333 := stratum.NewServerForTest()
	main3333.AddAuthorizedRentalClientForTest(stratum.RentalMRR)

	got := sumRentalStats(main3333, nil)
	if got.MRRMiners != 1 || got.TotalRentals != 1 {
		t.Fatalf("got %+v, want the 3333 server's single MRR client", got)
	}
	if all := sumRentalStats(nil, nil); all.TotalRentals != 0 {
		t.Fatalf("all-nil should total 0, got %+v", all)
	}
}

// The handler must ask BOTH servers.
//
// A unit test on sumRentalStats alone does not catch the original defect: the helper was
// always correct, the wiring was not. This pins the call site, which is where the bug was.
func TestRentalStatsHandlerAsksBothServers(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	src := string(raw)

	// Inspect the CALL, not the surrounding prose -- an explanatory comment mentioning
	// stratumRentalServer would otherwise satisfy a naive substring check while the call
	// itself passed only the miner port. (It did, the first time this test was written.)
	call := regexp.MustCompile(`sumRentalStats\(([^)]*)\)`)
	var args []string
	for _, m := range call.FindAllStringSubmatch(src, -1) {
		if strings.Contains(m[1], "stratum") {
			args = append(args, m[1])
		}
	}
	if len(args) == 0 {
		t.Fatal("no sumRentalStats call over the stratum servers found in main.go")
	}
	joined := strings.Join(args, " | ")
	if !strings.Contains(joined, "stratumRentalServer") {
		t.Errorf("sumRentalStats is called with %s — rented hashpower connects to 3335, so "+
			"its clients live in stratumRentalServer's map and the endpoint reports zeros "+
			"while an order is mining", joined)
	}
	if !strings.Contains(joined, "stratumServer") {
		t.Errorf("sumRentalStats is called with %s — the 3333 server is no longer counted", joined)
	}
}
