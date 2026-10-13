package main

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

const (
	// A 1175 address with a 19-byte witness program, which 1.0.12 to 1.0.15 saved: the 1175 node
	// refuses every getauxblock for it ("-5 Invalid payout address").
	bad1175 = "esf1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqnkz876"
	// What the stratum logs, once, for such an address.
	bad1175Said = "Your 1175 payout address is not a valid 1175 address, so 1175 merge-mining is OFF. Enter a valid one in Settings on the dashboard. BCH2 mining is unaffected and continues normally."
	// A 1175 node: merge-mining enabled, with a node's address (nothing listens there).
	with1175Node = "mergemining:\n  enabled: true\n  aux_node:\n    host: 127.0.0.1\n    port: 9\n"
)

// waitFor waits until the stratum r has logged part, for up to 60 s.
func (r *mmRun) waitFor(t *testing.T, part string) {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); !strings.Contains(r.out.String(), part); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("in 60 s the stratum did not log %q:\n%s", part, r.out.String())
		}
	}
}

// A 1175 address that is not one, saved by 1.0.12 to 1.0.15: merge-mining stayed on for it, the
// 1175 node refused every getauxblock, and the log said once a minute that merge-mining had never
// produced work, maybe as the 1175 node was still syncing. The start leaves merge-mining off and
// says why once, in plain words; the settings watcher does not say it again, and the mining status
// says merge-mining is off.
func TestABad1175AddressLeavesMergeMiningOffAndSaysSo(t *testing.T) {
	r := startWith1175(t, with1175Node, bad1175)
	r.watched(t)
	mm := r.mergeMining(t)
	r.stop()
	out := r.out.String()
	if n := strings.Count(out, bad1175Said); n != 1 {
		t.Errorf("MM-BAD-SAID: the stratum said %d times that the 1175 address is not valid, want once:\n%s", n, out)
	}
	if !strings.Contains(out, `"why":"not a segwit address: a version 0 witness program of 19 bytes, not 20 or 32"`) {
		t.Errorf("MM-BAD-WHY: the line does not say what is wrong with the address:\n%s", out)
	}
	if strings.Contains(out, "Merge mining enabled") || strings.Contains(out, "merge-mining enabled from dashboard") {
		t.Errorf("MM-BAD-ON: merge-mining came on for an address the 1175 node refuses:\n%s", out)
	}
	if mm != "off" {
		t.Errorf("MM-BAD-STATUS: the mining status says merge-mining is %q, want off", mm)
	}
}

// PAYOUT_ADDRESS_1175 reaches the config in quotes, spaces and all. The api trims it, so the
// dashboard gave no warning for a valid address with a stray space; the stratum did not, so it left
// merge-mining off and logged that the address is not a valid 1175 address. It trims it too.
func TestA1175AddressFromTheConfigIsTrimmed(t *testing.T) {
	r := startWith1175(t, with1175Node+"  payout_address: \"  "+movedEsf+" \"\n", "")
	r.waitFor(t, "Merge mining enabled")
	r.watched(t)
	mm := r.mergeMining(t)
	r.stop()
	out := r.out.String()
	if strings.Contains(out, bad1175Said) {
		t.Errorf("MM-TRIM-SAID: the stratum called the address with a space around it not valid:\n%s", out)
	}
	if !strings.Contains(out, `"payout_address_1175":"`+movedEsf+`"`) {
		t.Errorf("MM-TRIM-ADDRESS: merge-mining does not pay the address trimmed:\n%s", out)
	}
	if mm == "off" {
		t.Errorf("MM-TRIM-STATUS: the mining status says merge-mining is %q", mm)
	}
}

// The settings change while the stratum runs: a valid 1175 address turns merge-mining on, one that
// is not a 1175 address turns it off and is said once, and a valid one turns it on again. BCH2
// mining goes on throughout (merge-mining never stops the stratum).
func TestA1175AddressThatIsNotOneTurnsMergeMiningOff(t *testing.T) {
	r := startWith1175(t, with1175Node, movedEsf)
	r.waitFor(t, "Merge mining enabled")
	r.watched(t)
	set := func(esf string) {
		t.Helper()
		db, err := sql.Open("sqlite", r.db+"?_busy_timeout=10000")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`UPDATE pool_config SET payout_address_1175 = ? WHERE id = 1`, esf); err != nil {
			t.Fatal(err)
		}
	}

	set(bad1175)
	r.waitFor(t, "1175 merge-mining DISABLED: the 1175 address in the settings is not a valid 1175 address")
	r.waitFor(t, bad1175Said)
	if mm := r.mergeMining(t); mm != "off" {
		t.Errorf("MM-BAD-RUNTIME-STATUS: after the change the mining status says merge-mining is %q, want off", mm)
	}
	time.Sleep(10 * time.Second) // another look at the settings
	if n := strings.Count(r.out.String(), bad1175Said); n != 1 {
		t.Errorf("MM-BAD-RUNTIME-ONCE: said %d times, want once", n)
	}

	set(movedEsf)
	r.waitFor(t, "1175 merge-mining enabled from dashboard")
	if mm := r.mergeMining(t); mm == "off" {
		t.Errorf("MM-BAD-RUNTIME-BACK: with a valid 1175 address again merge-mining is %q", mm)
	}
	r.stop()
	if out := r.out.String(); strings.Contains(out, `"level":"fatal"`) {
		t.Errorf("MM-BAD-RUNTIME-FATAL: the stratum stopped:\n%s", out)
	}
}
