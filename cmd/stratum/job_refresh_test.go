package main

import (
	"strings"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/cashaddr"
	"github.com/BitcoincashII/forge-solo/internal/mining"
)

func testAddr(b byte) string {
	var h [20]byte
	for i := range h {
		h[i] = b
	}
	return cashaddr.Encode(cashaddr.MainnetPrefix, cashaddr.P2PKH, h)
}

// A changed payout address sends new work at once, and miners drop the work that pays the old one.
func TestAnAddressChangeMovesMinersAtOnce(t *testing.T) {
	a, b := testAddr(1), testAddr(2)
	old := &mining.Job{PayTo: a}
	tidesJob := &mining.Job{Tides: true}

	if !jobDue(old, false, false, false, false, b) {
		t.Error("PAY3-DUE: a changed address waited for the next periodic job")
	}
	if jobDue(old, false, false, false, false, a) {
		t.Error("PAY3-NOT-DUE: a new job every second while nothing changed")
	}
	if jobDue(old, false, false, false, true, b) || jobDue(tidesJob, false, false, false, false, b) {
		t.Error("PAY3-TIDES-WAITS: in TIDES mode an address change asked the gateway for a job at once")
	}
	if !jobDue(old, true, false, false, false, a) || !jobDue(old, false, true, false, false, a) || !jobDue(old, false, false, true, false, a) {
		t.Error("PAY3-REASONS: a new block, the periodic job or a mode switch no longer builds a job")
	}

	if !mustDropWork(old, &mining.Job{PayTo: b}, false) {
		t.Error("PAY3-CLEAN: miners kept work paying the old address")
	}
	if mustDropWork(old, &mining.Job{PayTo: a}, false) || mustDropWork(tidesJob, &mining.Job{Tides: true}, false) {
		t.Error("PAY3-PERIODIC: a periodic job made miners drop their work")
	}
	if !mustDropWork(old, &mining.Job{PayTo: a}, true) || !mustDropWork(nil, &mining.Job{PayTo: a}, false) {
		t.Error("PAY3-NEW-BLOCK: a new block's job let miners keep their work")
	}
	if !mustDropWork(tidesJob, &mining.Job{PayTo: a}, false) || !mustDropWork(old, &mining.Job{Tides: true}, false) ||
		!mustDropWork(tidesJob, &mining.Job{}, false) {
		t.Error("PAY3-MODE: a switch of payout mode let miners keep their work")
	}
}

// A solo block is recorded under the address its job's coinbase pays, in the dashboard's form.
func TestASoloBlockIsOwnedByTheAddressItPays(t *testing.T) {
	a, b := testAddr(1), testAddr(2)
	if got := soloBlockOwner(b, &mining.Job{PayTo: strings.ToUpper(a)}); got != a {
		t.Errorf("PAY3-OWNER: a block paying %s is recorded under %q", a, got)
	}
	if got := soloBlockOwner(b, &mining.Job{}); got != b {
		t.Errorf("PAY3-OWNER-UNSET: a job that does not say is recorded under %q, want the miner's %s", got, b)
	}
	if got := soloBlockOwner(b, nil); got != b {
		t.Errorf("PAY3-OWNER-NIL: %q", got)
	}
}
