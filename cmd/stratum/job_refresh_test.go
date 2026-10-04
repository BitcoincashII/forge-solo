package main

import (
	"strings"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/cashaddr"
	"github.com/BitcoincashII/forge-solo/internal/mergemining"
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

	if !jobDue(old, false, false, false, false, b, "") {
		t.Error("PAY3-DUE: a changed address waited for the next periodic job")
	}
	if jobDue(old, false, false, false, false, a, "") {
		t.Error("PAY3-NOT-DUE: a new job every second while nothing changed")
	}
	if jobDue(old, false, false, false, true, b, "") || jobDue(tidesJob, false, false, false, false, b, "") {
		t.Error("PAY3-TIDES-WAITS: in TIDES mode an address change asked the gateway for a job at once")
	}
	if !jobDue(old, true, false, false, false, a, "") || !jobDue(old, false, true, false, false, a, "") || !jobDue(old, false, false, true, false, a, "") {
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

// A changed 1175 address sends new work at once, and miners drop the work whose 1175 block pays
// the old one. Only the BCH2 address did: work paying the old 1175 address stayed with the miners
// until the next BCH2 block.
func TestA1175AddressChangeMovesMinersAtOnce(t *testing.T) {
	a := testAddr(1)
	work, fresh := &mergemining.AuxWork{Hash: strings.Repeat("aa", 32)}, &mergemining.AuxWork{Hash: strings.Repeat("bb", 32)}
	old := &mining.Job{PayTo: a, AuxWork: work, AuxPayTo: "esf1old"}

	if !jobDue(old, false, false, false, false, a, "esf1new") {
		t.Error("AUX-PAY3-DUE: a changed 1175 address waited for the next periodic job")
	}
	if !jobDue(old, false, false, false, false, a, "") {
		t.Error("AUX-PAY3-DUE-CLEARED: a cleared 1175 address waited for the next periodic job")
	}
	if jobDue(old, false, false, false, false, a, "esf1old") {
		t.Error("AUX-PAY3-NOT-DUE: a new job every second while nothing changed")
	}
	if jobDue(old, false, false, false, true, a, "esf1new") || jobDue(&mining.Job{Tides: true}, false, false, false, false, a, "esf1new") {
		t.Error("AUX-PAY3-TIDES-WAITS: in TIDES mode a 1175 address change asked the gateway for a job at once")
	}

	if !mustDropWork(old, &mining.Job{PayTo: a, AuxWork: fresh, AuxPayTo: "esf1new"}, false) {
		t.Error("AUX-PAY3-CLEAN: miners kept work whose 1175 block pays the old address")
	}
	if !mustDropWork(old, &mining.Job{PayTo: a, AuxPayTo: "esf1new"}, false) || !mustDropWork(old, &mining.Job{PayTo: a}, false) {
		t.Error("AUX-PAY3-CLEAN-NO-WORK: re-pointed or cleared with no 1175 work yet, and miners kept the old address's")
	}
	if mustDropWork(old, &mining.Job{PayTo: a, AuxWork: fresh, AuxPayTo: "esf1old"}, false) || mustDropWork(old, &mining.Job{PayTo: a, AuxPayTo: "esf1old"}, false) {
		t.Error("AUX-PAY3-PERIODIC: new 1175 work for the same address made miners drop their work")
	}
	if mustDropWork(&mining.Job{PayTo: a, AuxPayTo: "esf1old"}, &mining.Job{PayTo: a, AuxWork: work, AuxPayTo: "esf1new"}, false) {
		t.Error("AUX-PAY3-NOTHING-TO-DROP: miners dropped work that carried no 1175 work")
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
