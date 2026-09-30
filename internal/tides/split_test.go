package tides

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func sumOutputs(outs []Output) int64 {
	var s int64
	for _, o := range outs {
		s += o.Sats
	}
	return s
}

func outputFor(outs []Output, addr string) (Output, bool) {
	for _, o := range outs {
		if o.Address == addr && !o.Fee {
			return o, true
		}
	}
	return Output{}, false
}

// A 50 BCH2 coinbase with the 1% pool fee must pay out every satoshi: the fee output first, then
// the miners, each at or above dust.
func TestTidesSplitPaysExactlyTheCoinbaseValue(t *testing.T) {
	const value = int64(50_00000000)
	work := map[string]float64{"qa": 123456.7, "qb": 9.87e6, "qc": 42.5, "qd": 3.3e5, "qe": 7777777.1}
	outs, carry, err := Split(value, work, nil, 1.0, "qfee", DustSats)
	if err != nil {
		t.Fatal(err)
	}
	if got := sumOutputs(outs); got != value {
		t.Fatalf("EXACT: outputs sum to %d, want %d", got, value)
	}
	if !outs[0].Fee || outs[0].Address != "qfee" || outs[0].Sats != 50000000 {
		t.Fatalf("EXACT: first output %+v, want the fee: qfee 50000000", outs[0])
	}
	for _, o := range outs[1:] {
		if o.Sats < DustSats {
			t.Fatalf("EXACT: miner output %+v is below dust", o)
		}
	}
	if len(carry) != 0 {
		t.Fatalf("EXACT: nobody is below dust, but carry = %v", carry)
	}
}

// Each miner is paid in proportion to its work.
func TestTidesSplitIsProportional(t *testing.T) {
	outs, _, err := Split(4000, map[string]float64{"a": 1, "b": 3}, nil, 0, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []Output{{Address: "a", Sats: 1000}, {Address: "b", Sats: 3000}}
	if !reflect.DeepEqual(outs, want) {
		t.Fatalf("PROPORTIONAL: outputs %+v, want %+v", outs, want)
	}
}

// Satoshis lost to flooring go to the largest paid miner; on a tie, the first by address.
func TestTidesSplitLeftoverGoesToLargestThenFirstAddress(t *testing.T) {
	outs, _, err := Split(10, map[string]float64{"c": 1, "a": 1, "b": 1}, nil, 0, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []Output{{Address: "a", Sats: 4}, {Address: "b", Sats: 3}, {Address: "c", Sats: 3}}
	if !reflect.DeepEqual(outs, want) {
		t.Fatalf("LEFTOVER: outputs %+v, want %+v", outs, want)
	}
	outs, _, _ = Split(11, map[string]float64{"a": 1, "b": 2}, nil, 0, "", 0)
	if o, _ := outputFor(outs, "b"); o.Sats != 8 { // 3.67 -> 3, 7.33 -> 7, leftover 1 to the larger (b)
		t.Fatalf("LEFTOVER: b got %d, want 8 (outputs %+v)", o.Sats, outs)
	}
}

// The same inputs always give the same coinbase, whatever order the maps were built in.
func TestTidesSplitIsDeterministic(t *testing.T) {
	addrs := []string{"qz", "qa", "qm", "qb", "qy", "qc"}
	var first []Output
	for round := 0; round < 40; round++ {
		work := map[string]float64{}
		for i := range addrs {
			j := (i + round) % len(addrs)
			work[addrs[j]] = float64(1000 + 37*j)
		}
		outs, _, err := Split(50_00000000, work, map[string]int64{"qm": 100}, 1, "qfee", DustSats)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = outs
		} else if !reflect.DeepEqual(outs, first) {
			t.Fatalf("DETERMINISM: round %d gave %+v, first gave %+v", round, outs, first)
		}
	}
	for i := 2; i < len(first); i++ {
		if first[i-1].Address >= first[i].Address {
			t.Fatalf("DETERMINISM: miner outputs not sorted by address: %+v", first)
		}
	}
}

// A miner whose share is below dust gets no output: its due is carried, and the block still pays
// out in full to the others.
func TestTidesSplitCarriesDustInsteadOfPayingIt(t *testing.T) {
	outs, carry, err := Split(1_000_000, map[string]float64{"big": 9999, "tiny": 1}, nil, 0, "", DustSats)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := outputFor(outs, "tiny"); ok {
		t.Fatalf("DUST: tiny (100 sats) was paid: %+v", outs)
	}
	if carry["tiny"] != 100 {
		t.Fatalf("DUST: carry %v, want tiny=100", carry)
	}
	if o, _ := outputFor(outs, "big"); o.Sats != 1_000_000 || sumOutputs(outs) != 1_000_000 {
		t.Fatalf("DUST: outputs %+v, want big paid the whole 1000000", outs)
	}
}

// Once carried balance plus this block's share reaches dust, the miner is paid all of it and the
// carry is cleared; the coinbase still pays out exactly.
func TestTidesSplitPaysCarryOnceItReachesDust(t *testing.T) {
	outs, carry, err := Split(1_000_000, map[string]float64{"big": 9999, "tiny": 1}, map[string]int64{"tiny": 500}, 0, "", DustSats)
	if err != nil {
		t.Fatal(err)
	}
	o, ok := outputFor(outs, "tiny")
	if !ok || o.Sats < 599 || o.Sats > 600 {
		t.Fatalf("CARRY-PAID: tiny output %+v (present=%v), want about 600 (500 carried + ~100 now)", o, ok)
	}
	if sumOutputs(outs) != 1_000_000 {
		t.Fatalf("CARRY-PAID: outputs sum to %d, want 1000000", sumOutputs(outs))
	}
	if len(carry) != 0 {
		t.Fatalf("CARRY-PAID: carry left %v, want none", carry)
	}
}

// A carried balance of a miner who is no longer in the window is kept, not paid.
func TestTidesSplitKeepsCarryOfMinersOutsideTheWindow(t *testing.T) {
	outs, carry, err := Split(5000, map[string]float64{"a": 1}, map[string]int64{"gone": 300}, 0, "", DustSats)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := outputFor(outs, "gone"); ok || carry["gone"] != 300 {
		t.Fatalf("OUTSIDE: outputs %+v carry %v, want gone kept at 300 and unpaid", outs, carry)
	}
}

func TestTidesSplitRejectsBadInput(t *testing.T) {
	ok := map[string]float64{"a": 1}
	cases := []struct {
		name  string
		value int64
		work  map[string]float64
		carry map[string]int64
		fee   float64
		addr  string
		want  error
	}{
		{"zero value", 0, ok, nil, 0, "", ErrBadInput},
		{"negative fee", 100, ok, nil, -1, "f", ErrBadInput},
		{"100% fee", 100, ok, nil, 100, "f", ErrBadInput},
		{"NaN fee", 100, ok, nil, math.NaN(), "f", ErrBadInput},
		{"negative work", 100, map[string]float64{"a": -1}, nil, 0, "", ErrBadInput},
		{"NaN work", 100, map[string]float64{"a": math.NaN()}, nil, 0, "", ErrBadInput},
		{"empty address", 100, map[string]float64{"": 1}, nil, 0, "", ErrBadInput},
		{"negative carry", 100, ok, map[string]int64{"a": -5}, 0, "", ErrBadInput},
		{"fee without an address", 100, ok, nil, 1, "", ErrBadInput},
		{"no work", 100, map[string]float64{"a": 0}, nil, 0, "", ErrNoWork},
	}
	for _, c := range cases {
		if _, _, err := Split(c.value, c.work, c.carry, c.fee, c.addr, DustSats); !errors.Is(err, c.want) {
			t.Errorf("REJECT %s: err = %v, want %v", c.name, err, c.want)
		}
	}
}
