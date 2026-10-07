package pgmigrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The old databases the tests move: built as the lib/pq Source reads them, with the column types
// 1.0.12's schema (testdata/pg-1.0.12-schema.sql) gives them.

const (
	addrA    = "bitcoincashii:qqminera00000000000000000000000000000000000000"
	addrB    = "bitcoincashii:qqminerb00000000000000000000000000000000000000"
	addrC    = "bitcoincashii:qqminerc00000000000000000000000000000000000000"
	addr1175 = "esf1qexample0000000000000000000000000000000"
	pinHash  = "$2a$10$abcdefghijklmnopqrstuuJ0123456789abcdefghijklmnopqrstu"
)

var chicago = time.FixedZone("CDT", -5*3600)

// at is a time as lib/pq reads a timestamptz: in the server's zone, to the microsecond.
func at(day, hour, min, sec, micro int) time.Time {
	return time.Date(2026, 9, day, hour, min, sec, micro*1000, chicago)
}

func hashOf(h int64, salt string) string { return fmt.Sprintf("%s%063x", salt, h)[:64] }

var types1012 = map[string]map[string]string{
	"pool_config": {"id": "integer", "pool_address": "text", "payout_address_1175": "text", "coinbase_tag": "text",
		"payout_mode": "text", "updated_at": "timestamp with time zone"},
	"datum_identity": {"id": "integer", "key_seed": "text", "created_at": "timestamp with time zone"},
	"miners": {"id": "bigint", "address": "character varying", "solo_mining": "boolean", "manual_diff": "numeric",
		"address_1175": "text", "settings_pin_hash": "text", "created_at": "timestamp with time zone", "updated_at": "timestamp with time zone"},
	"blocks": {"id": "bigint", "height": "bigint", "hash": "character varying", "miner_address": "character varying",
		"reward": "numeric", "difficulty": "numeric", "status": "character varying", "confirmations": "integer",
		"is_solo": "boolean", "created_at": "timestamp with time zone", "confirmed_at": "timestamp with time zone"},
	"payouts": {"id": "bigint", "miner_address": "character varying", "block_height": "bigint", "amount": "numeric",
		"confirmed": "boolean", "txid": "character varying", "created_at": "timestamp with time zone",
		"paid_at": "timestamp with time zone", "status": "character varying"},
	"shares": {"id": "bigint", "time": "timestamp with time zone", "miner_address": "character varying",
		"worker_name": "character varying", "job_id": "character varying", "difficulty": "numeric", "is_valid": "boolean",
		"is_block": "boolean", "is_solo": "boolean", "block_hash": "character varying"},
	"pool_stats": {"time": "timestamp with time zone", "hashrate": "numeric", "workers": "integer", "miners_online": "integer",
		"valid_shares": "bigint", "invalid_shares": "bigint", "network_difficulty": "numeric", "block_height": "bigint"},
	"blocks_1175": {"height": "bigint", "hash": "text", "gross_reward": "double precision", "is_solo": "boolean",
		"finder": "text", "distributed": "boolean", "status": "text", "created_at": "timestamp with time zone"},
	"payouts_1175": {"id": "bigint", "miner_address": "text", "block_height": "bigint", "amount": "double precision",
		"txid": "text", "status": "text", "batch": "text", "paid_at": "timestamp with time zone", "created_at": "timestamp with time zone"},
}

// source1012 is a 1.0.12 database as a home miner's install holds it: TIDES mode with its gateway
// key, a 1175 address and a tag with a non-ASCII letter, a miner with a PIN, solo blocks confirmed,
// pending, orphaned and superseded, pool-era rows of older versions, the 1175 ledger in every
// state, and odd rows older versions left: a NULL status and a NULL boolean, ” and NULL txids, and
// a second pool_config row Forge Solo never reads.
func source1012() *MemSource {
	m := NewMemSource(Info{ServerVersion: "16.6", SystemIdentifier: "7692687789219831848"})
	for name, types := range types1012 {
		m.AddTable(name, types)
	}
	m.Insert("pool_config", map[string]any{"id": int64(1), "pool_address": addrA, "payout_address_1175": addr1175,
		"coinbase_tag": "/forge ü 1.0.12/", "payout_mode": "tides", "updated_at": at(20, 10, 0, 0, 123456)})
	m.Insert("pool_config", map[string]any{"id": int64(2), "pool_address": addrC, "payout_address_1175": "",
		"coinbase_tag": "", "payout_mode": "solo", "updated_at": at(1, 0, 0, 0, 0)})
	m.Insert("datum_identity", map[string]any{"id": int64(1), "key_seed": strings.Repeat("c3", 32), "created_at": at(20, 10, 0, 1, 500000)})
	m.Insert("miners", map[string]any{"id": int64(3), "address": addrA, "solo_mining": true, "manual_diff": "0.00000000",
		"address_1175": addr1175, "settings_pin_hash": pinHash, "created_at": at(2, 1, 0, 0, 0), "updated_at": at(21, 9, 30, 0, 499999)})
	m.Insert("miners", map[string]any{"id": int64(7), "address": addrC, "solo_mining": false, "manual_diff": "1234.50000000",
		"address_1175": nil, "settings_pin_hash": nil, "created_at": at(2, 2, 0, 0, 0), "updated_at": at(2, 2, 0, 0, 0)})

	id := int64(10)
	block := func(h int64, miner, hash, reward, status string, solo bool, created time.Time, confirmed any) {
		m.Insert("blocks", map[string]any{"id": id, "height": h, "hash": hash, "miner_address": miner, "reward": reward,
			"difficulty": "0.00000000", "status": status, "confirmations": int64(0), "is_solo": solo,
			"created_at": created, "confirmed_at": confirmed})
		id++
	}
	pid := int64(100)
	payout := func(miner string, h int64, amount string, confirmed any, txid any, status any, created, paid any) {
		m.Insert("payouts", map[string]any{"id": pid, "miner_address": miner, "block_height": h, "amount": amount,
			"confirmed": confirmed, "txid": txid, "status": status, "created_at": created, "paid_at": paid})
		pid++
	}
	for i := int64(0); i < 12; i++ {
		h := 100000 + i
		reward := "3.12500000"
		if i%3 == 1 {
			reward = "3.12512345"
		}
		created := at(3+int(i), 4, 5, 6, 500000) // .5 s: rounds up
		status := "confirmed"
		var confirmed any = at(4+int(i), 4, 5, 6, 0)
		switch {
		case i >= 9:
			status, confirmed = "pending", nil
		case i == 5:
			status = "orphaned"
		}
		block(h, addrA, hashOf(h, "a"), reward, status, true, created, confirmed)
		payStatus, txid := "paid", "coinbase-direct"
		if i == 5 {
			payStatus, txid = "orphaned", "orphaned"
		}
		payout(addrA, h, reward, i != 5, txid, payStatus, created, created)
	}
	// Two blocks of B, the address the payout went to before a change.
	block(100020, addrB, hashOf(100020, "b"), "3.12500000", "confirmed", true, at(18, 0, 0, 0, 0), at(18, 2, 0, 0, 0))
	payout(addrB, 100020, "3.12500000", true, "coinbase-direct", "paid", at(18, 0, 0, 0, 0), at(18, 0, 0, 0, 0))
	// Pool-era rows of older versions: a block and its split, an unpaid row with a NULL txid, one
	// with '', a reserved one and a NULL status with a NULL boolean.
	block(99000, addrC, hashOf(99000, "d"), "50.00000000", "confirmed", false, at(1, 1, 1, 1, 0), nil)
	payout(addrC, 99000, "33.33333333", true, strings.Repeat("ab", 32), "paid", at(1, 1, 1, 1, 0), at(1, 2, 0, 0, 0))
	payout(addrA, 99000, "16.66666667", false, nil, "pending", at(1, 1, 1, 1, 0), nil)
	payout(addrB, 99000, "0.00000001", false, "", "pending", at(1, 1, 1, 1, 0), nil)
	payout(addrC, 99001, "12.34567891", false, "pending_1700000000000000000_bitcoinc", "processing", at(1, 3, 0, 0, 0), at(1, 3, 0, 0, 1))
	payout(addrC, 99100, "0.00000001", nil, nil, nil, at(1, 4, 0, 0, 0), nil)

	// The 1175 ledger: settled, orphaned, undistributed, and a pool-era block.
	b1175 := func(h int64, gross float64, solo bool, finder any, distributed bool, status string, created time.Time) {
		m.Insert("blocks_1175", map[string]any{"height": h, "hash": hashOf(h, "9"), "gross_reward": gross, "is_solo": solo,
			"finder": finder, "distributed": distributed, "status": status, "created_at": created})
	}
	p1175 := int64(1)
	c1175 := func(miner string, h int64, amount float64, txid any, status string, paid any, created time.Time) {
		m.Insert("payouts_1175", map[string]any{"id": p1175, "miner_address": miner, "block_height": h, "amount": amount,
			"txid": txid, "status": status, "batch": nil, "paid_at": paid, "created_at": created})
		p1175++
	}
	b1175(5000, 0.78125, true, addrA, true, "confirmed", at(10, 1, 0, 0, 0))
	c1175(addrA, 5000, 0.78125, "coinbase-direct", "paid", at(12, 1, 0, 0, 0), at(10, 1, 0, 0, 0))
	b1175(5001, 0.78125, true, addrA, true, "orphaned", at(11, 1, 0, 0, 0))
	c1175(addrA, 5001, 0.78125, "orphaned", "orphaned", nil, at(11, 1, 0, 0, 0))
	b1175(5002, 0.78125, true, addrA, false, "pending", at(22, 1, 0, 0, 0))
	b1175(5010, 0.7, false, addrC, true, "confirmed", at(5, 1, 0, 0, 0))
	c1175(addrC, 5010, 0.525, nil, "pending", nil, at(5, 1, 0, 0, 0))
	c1175(addrA, 5010, 0.175, nil, "pending", nil, at(5, 1, 0, 0, 0))

	for i := int64(1); i <= 3; i++ {
		m.Insert("shares", map[string]any{"id": i, "time": at(22, 0, 0, int(i), 0), "miner_address": addrA, "worker_name": "rig1",
			"job_id": nil, "difficulty": "1024.00000000", "is_valid": true, "is_block": false, "is_solo": true, "block_hash": nil})
	}
	return m
}

// source100 is a database 1.0.0 made: no pool_config, no datum_identity, no 1175 ledger, no
// payouts.status, and the columns later versions dropped.
func source100() *MemSource {
	m := NewMemSource(Info{ServerVersion: "16.4", SystemIdentifier: "1"})
	m.AddTable("miners", map[string]string{"id": "bigint", "address": "character varying", "solo_mining": "boolean",
		"min_payout": "numeric", "manual_diff": "numeric", "created_at": "timestamp with time zone", "updated_at": "timestamp with time zone"})
	m.AddTable("blocks", map[string]string{"id": "bigint", "height": "bigint", "hash": "character varying", "miner_address": "character varying",
		"reward": "numeric", "difficulty": "numeric", "status": "character varying", "confirmations": "integer",
		"is_solo": "boolean", "created_at": "timestamp with time zone", "confirmed_at": "timestamp with time zone"})
	m.AddTable("payouts", map[string]string{"id": "bigint", "miner_address": "character varying", "block_height": "bigint",
		"amount": "numeric", "confirmed": "boolean", "txid": "character varying", "created_at": "timestamp with time zone",
		"paid_at": "timestamp with time zone"})
	m.AddTable("shares", types1012["shares"])
	m.Insert("miners", map[string]any{"id": int64(1), "address": addrA, "solo_mining": true, "min_payout": "0.10000000",
		"manual_diff": "0.00000000", "created_at": at(1, 0, 0, 0, 0), "updated_at": at(1, 0, 0, 0, 0)})
	for i := int64(0); i < 5; i++ {
		h := 90000 + i
		m.Insert("blocks", map[string]any{"id": i + 1, "height": h, "hash": hashOf(h, "e"), "miner_address": addrA,
			"reward": "3.12500000", "difficulty": "0", "status": "confirmed", "confirmations": int64(100), "is_solo": true,
			"created_at": at(2, int(i), 0, 0, 0), "confirmed_at": nil})
		m.Insert("payouts", map[string]any{"id": i + 1, "miner_address": addrA, "block_height": h, "amount": "3.12500000",
			"confirmed": true, "txid": "coinbase-direct", "created_at": at(2, int(i), 0, 0, 0), "paid_at": at(2, int(i), 0, 0, 0)})
	}
	return m
}

// dump is every row of the app's tables in the database at path, each value as its SQL literal:
// what two databases must share to hold the same data.
func dump(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var b strings.Builder
	names := []string{"shares"}
	for _, tb := range Tables {
		names = append(names, tb.Name)
	}
	for _, n := range names {
		cols, err := tableColumns(ctx, db, n)
		if err != nil {
			t.Fatal(err)
		}
		q := make([]string, len(cols))
		for i, c := range cols {
			q[i] = "quote(" + c.name + ")"
		}
		rows, err := db.Query(`SELECT ` + strings.Join(q, " || '|' || ") + ` FROM ` + n + ` ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%s: %s\n", n, line)
		}
		rows.Close()
	}
	return b.String()
}

// rowsOf is one table of the database at path as dump writes it.
func rowsOf(t *testing.T, path, table string) []string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(dump(t, path), "\n") {
		if strings.HasPrefix(l, table+": ") {
			out = append(out, strings.TrimPrefix(l, table+": "))
		}
	}
	return out
}
