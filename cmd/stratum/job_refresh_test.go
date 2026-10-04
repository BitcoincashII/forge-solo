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

	if !jobDue(old, false, false, false, false, b, nil, "") {
		t.Error("PAY3-DUE: a changed address waited for the next periodic job")
	}
	if jobDue(old, false, false, false, false, a, nil, "") {
		t.Error("PAY3-NOT-DUE: a new job every second while nothing changed")
	}
	if jobDue(old, false, false, false, true, b, nil, "") || jobDue(tidesJob, false, false, false, false, b, nil, "") {
		t.Error("PAY3-TIDES-WAITS: in TIDES mode an address change asked the gateway for a job at once")
	}
	if !jobDue(old, true, false, false, false, a, nil, "") || !jobDue(old, false, true, false, false, a, nil, "") || !jobDue(old, false, false, true, false, a, nil, "") {
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

	if !jobDue(old, false, false, false, false, a, work, "esf1new") {
		t.Error("AUX-PAY3-DUE: a changed 1175 address waited for the next periodic job")
	}
	if !jobDue(old, false, false, false, false, a, nil, "") {
		t.Error("AUX-PAY3-DUE-CLEARED: a cleared 1175 address waited for the next periodic job")
	}
	if jobDue(old, false, false, false, false, a, work, "esf1old") {
		t.Error("AUX-PAY3-NOT-DUE: a new job every second while nothing changed")
	}
	if jobDue(old, false, false, false, true, a, fresh, "esf1new") || jobDue(&mining.Job{Tides: true}, false, false, false, false, a, fresh, "esf1new") {
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

// 1175 work on a new 1175 tip goes out at once, as does 1175 work coming or going, and miners keep
// their work. Work refreshed on the same tip waits for the periodic job.
func TestNew1175WorkOnANewTipGoesOutAtOnce(t *testing.T) {
	a := testAddr(1)
	tip1, tip2 := strings.Repeat("a1", 32), strings.Repeat("b2", 32)
	cur := &mining.Job{PayTo: a, AuxWork: &mergemining.AuxWork{Hash: strings.Repeat("aa", 32), PreviousBlockHash: tip1}, AuxPayTo: "esf1"}
	next := &mergemining.AuxWork{Hash: strings.Repeat("bb", 32), PreviousBlockHash: tip2}
	same := &mergemining.AuxWork{Hash: strings.Repeat("cc", 32), PreviousBlockHash: strings.ToUpper(tip1)}

	if !jobDue(cur, false, false, false, false, a, next, "esf1") {
		t.Error("AUX-TIP-DUE: 1175 work on a new tip waited for the next periodic job")
	}
	if jobDue(cur, false, false, false, false, a, same, "esf1") {
		t.Error("AUX-TIP-SAME: 1175 work refreshed on the same tip made a job at once")
	}
	if !jobDue(cur, false, false, false, false, a, nil, "esf1") {
		t.Error("AUX-WORK-WENT: 1175 work gone stale stayed in the miners' job until the periodic one")
	}
	if !jobDue(&mining.Job{PayTo: a, AuxPayTo: "esf1"}, false, false, false, false, a, next, "esf1") {
		t.Error("AUX-WORK-CAME: the first 1175 work waited for the next periodic job")
	}
	if mustDropWork(cur, &mining.Job{PayTo: a, AuxWork: next, AuxPayTo: "esf1"}, false) {
		t.Error("AUX-TIP-KEEPS-WORK: a new 1175 tip made miners drop work still good for BCH2")
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
