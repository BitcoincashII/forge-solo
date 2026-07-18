package main

import (
	"math"
	"testing"

	"github.com/bch2/forge-pool/internal/stats"
)

// sumRows returns the total amount across chunked rows and the multiset of row IDs,
// so a test can assert nothing was dropped, split, or duplicated.
func sumRows(chunks [][]stats.PayoutRow) (total float64, ids map[int64]int) {
	ids = make(map[int64]int)
	for _, c := range chunks {
		for _, r := range c {
			total += r.Amount
			ids[r.ID]++
		}
	}
	return total, ids
}

func TestChunkPayoutRows_Conservation(t *testing.T) {
	rows := []stats.PayoutRow{
		{ID: 1, Amount: 300}, {ID: 2, Amount: 300}, {ID: 3, Amount: 300},
		{ID: 4, Amount: 300}, {ID: 5, Amount: 300}, {ID: 6, Amount: 49.75},
		{ID: 7, Amount: 0.00001},
	}
	var want float64
	for _, r := range rows {
		want += r.Amount
	}

	chunks := chunkPayoutRows(rows, maxPayoutPerTx)

	got, ids := sumRows(chunks)
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("chunk total %.8f != input total %.8f (rows lost or duplicated)", got, want)
	}
	if len(ids) != len(rows) {
		t.Fatalf("expected %d distinct rows, got %d", len(rows), len(ids))
	}
	for id, n := range ids {
		if n != 1 {
			t.Fatalf("row %d appears %d times (must be exactly once)", id, n)
		}
	}
	// Every chunk except a lone oversized row must be within the cap.
	for i, c := range chunks {
		var amt float64
		for _, r := range c {
			amt += r.Amount
		}
		if amt > maxPayoutPerTx && len(c) > 1 {
			t.Fatalf("chunk %d sums to %.2f > cap %.2f with %d rows (should have split)", i, amt, maxPayoutPerTx, len(c))
		}
	}
}

func TestChunkPayoutRows_Empty(t *testing.T) {
	if got := chunkPayoutRows(nil, maxPayoutPerTx); len(got) != 0 {
		t.Fatalf("expected no chunks for empty input, got %d", len(got))
	}
}

func TestChunkPayoutRows_UnderCapSingleChunk(t *testing.T) {
	rows := []stats.PayoutRow{{ID: 1, Amount: 4}, {ID: 2, Amount: 1}}
	chunks := chunkPayoutRows(rows, maxPayoutPerTx)
	if len(chunks) != 1 || len(chunks[0]) != 2 {
		t.Fatalf("expected a single chunk of 2 rows, got %d chunks", len(chunks))
	}
}

func TestChunkPayoutRows_OversizedRowIsolated(t *testing.T) {
	// A single row larger than the cap must become its own chunk, never dropped.
	rows := []stats.PayoutRow{{ID: 1, Amount: 10}, {ID: 2, Amount: 2500}, {ID: 3, Amount: 10}}
	chunks := chunkPayoutRows(rows, maxPayoutPerTx)
	total, ids := sumRows(chunks)
	if math.Abs(total-2520) > 1e-9 || len(ids) != 3 {
		t.Fatalf("oversized row handling lost value: total=%.2f ids=%d", total, len(ids))
	}
}

func TestIsDefinitelyNotBroadcast(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, true},
		{errString("RPC error: Insufficient funds"), true},
		{errString("RPC error: Invalid amount"), true},
		{errString("Post \"http://...\": context deadline exceeded"), false},
		{errString("read tcp: connection reset by peer"), false},
		{errString("failed to decode RPC response: unexpected EOF"), false},
	}
	for _, c := range cases {
		if got := isDefinitelyNotBroadcast(c.err); got != c.want {
			t.Errorf("isDefinitelyNotBroadcast(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
