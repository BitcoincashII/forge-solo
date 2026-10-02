package main

import (
	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
)

// jobDue says whether the job loop builds a new job now: on a new block, on a switch of payout
// mode, when the current solo job pays an address other than payTo (the dashboard changed it), and
// every 15 seconds otherwise. In TIDES mode an address change waits for the periodic job, which
// asks the gateway no more often than it already does.
func jobDue(cur *mining.Job, newBlock, periodic, modeSwitch, tides bool, payTo string) bool {
	return newBlock || periodic || modeSwitch || (!tides && payoutChanged(cur, payTo))
}

// payoutChanged reports whether cur is a solo job paying an address other than payTo.
func payoutChanged(cur *mining.Job, payTo string) bool {
	return cur != nil && !cur.Tides && cur.PayTo != payTo
}

// mustDropWork says whether miners must drop the work they have for job (clean_jobs): on a new
// block, and when job pays differently from cur -- the payout mode switched, or the address
// changed and work on cur still pays the old one. Without it a miner finished its old work first,
// and a block found on it paid the address the user had just replaced.
func mustDropWork(cur, job *mining.Job, newBlock bool) bool {
	return newBlock || cur == nil || job.Tides != cur.Tides || job.PayTo != cur.PayTo // a TIDES job pays no address of its own: ""
}

// soloBlockOwner is the address a solo block is recorded under: the one its job's coinbase pays,
// in the form the dashboard looks miners up by. A block found on a job from before an address
// change paid the old address, whatever its miner is credited to now.
func soloBlockOwner(minerID string, job *mining.Job) string {
	if job == nil || job.PayTo == "" {
		return minerID
	}
	if owner := stratum.NormalizeMinerAddress(job.PayTo); owner != "" {
		return owner
	}
	return minerID
}
