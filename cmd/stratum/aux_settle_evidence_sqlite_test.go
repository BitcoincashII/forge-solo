//go:build sqlite

package main

import (
	"database/sql"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// rows1175 is each 1175 block as "height status distributed" and each credit as "height status",
// read from the database file at path.
func rows1175(t *testing.T, path string) (blocks, credits string) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	read := func(q string) string {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var a, b, c string
			if err := rows.Scan(&a, &b, &c); err != nil {
				t.Fatal(err)
			}
			out = append(out, strings.TrimSpace(fmt.Sprintf("%s %s %s", a, b, c)))
		}
		return strings.Join(out, ", ")
	}
	return read(`SELECT height, status, CASE WHEN distributed THEN 'credited' ELSE 'not-credited' END FROM blocks_1175 ORDER BY height`),
		read(`SELECT block_height, status, '' FROM payouts_1175 ORDER BY block_height`)
}

// A 1175 credit is settled as paid by the block's own coinbase only once its block is confirmed,
// and a block is confirmed only by a 1175 node that has caught up with its chain and has it 100
// deep. While the node is down or still syncing, the payout processor credits a block found and
// not credited yet (the credit pending, as when a block is found) and settles a credit whose block
// the node confirmed before, and judges no block. That is what it did on a Linux install with no
// 1175 node and a database brought from elsewhere: block 5002 credited, pending; block 5010's
// credit, confirmed before, settled.
func TestThe1175ProcessorSettlesOnlyWhatTheNodeConfirmed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aux.db")
	if err := stats.InitDB(path); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	node, _ := useAuxConfNode(t)
	seedMoved1175(t)
	check := func(code, wantBlocks, wantCredits string) {
		t.Helper()
		if blocks, credits := rows1175(t, path); blocks != wantBlocks || credits != wantCredits {
			t.Fatalf("%s: blocks %q, credits %q; want %q, %q", code, blocks, credits, wantBlocks, wantCredits)
		}
	}
	check("AUX-EVIDENCE-SETUP", "5002 pending not-credited, 5010 confirmed credited", "5010 pending")

	// The node down.
	down := httptest.NewServer(node)
	aux1175NodeURL = down.URL
	down.Close()
	run1175PayoutCycle()
	check("AUX-DOWN", "5002 pending credited, 5010 confirmed credited", "5002 pending, 5010 paid")

	// Syncing: it has block 5002 150 deep, but has not caught up with its chain.
	srv := httptest.NewServer(node)
	defer srv.Close()
	aux1175NodeURL = srv.URL
	node.set(strings.Repeat("a2", 32), 150)
	node.setChain(4000, 6000, true)
	run1175PayoutCycle()
	check("AUX-SYNCING", "5002 pending credited, 5010 confirmed credited", "5002 pending, 5010 paid")

	// Caught up: block 5002 confirmed, and its credit settled.
	node.setChain(6000, 6000, false)
	run1175PayoutCycle()
	check("AUX-CAUGHT-UP", "5002 confirmed credited, 5010 confirmed credited", "5002 paid, 5010 paid")
}
