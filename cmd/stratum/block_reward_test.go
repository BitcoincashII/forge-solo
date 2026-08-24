package main

import "testing"

// A solved block's reward must come from the coinbase the miner actually hashed,
// not from whatever template happened to be current when the share arrived. The
// two differ by the block's fee total whenever a template refresh lands between
// the job going out and the share coming back -- invisible on a fee-free regtest
// chain, wrong on every busy mainnet block.
func TestBlockCoinbaseBTC_PrefersTheSolvedJobsOwnCoinbase(t *testing.T) {
	const (
		jobSats   = int64(50_00012345) // the job the miner solved: 50 BCH2 + 0.00012345 fees
		staleGlob = 50.99999999        // a LATER template, with a fatter mempool
	)

	got := blockCoinbaseBTC(jobSats, staleGlob)
	if want := 50.00012345; got != want {
		t.Fatalf("recorded reward from the wrong source: got %.8f, want %.8f "+
			"(the job's own coinbase, not the newest template)", got, want)
	}
	if got == staleGlob {
		t.Fatal("recorded the newest template's value; a template refresh between " +
			"job and share would mis-state this block's fees")
	}
}

// Jobs that carry no value of their own must still record something rather than 0.
func TestBlockCoinbaseBTC_FallsBackWhenJobCarriesNoValue(t *testing.T) {
	for _, jobSats := range []int64{0, -1} {
		if got := blockCoinbaseBTC(jobSats, 50.5); got != 50.5 {
			t.Fatalf("jobCoinbaseValue=%d: got %.8f, want the 50.50000000 fallback",
				jobSats, got)
		}
	}
}

// Fees must survive the satoshi->BCH2 conversion intact; a float32 round-trip or
// an integer division would quietly drop them.
func TestBlockCoinbaseBTC_KeepsSatoshiPrecision(t *testing.T) {
	if got := blockCoinbaseBTC(1, 0); got != 0.00000001 {
		t.Fatalf("one satoshi became %.10f", got)
	}
}
