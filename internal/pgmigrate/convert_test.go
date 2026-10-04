//go:build sqlite

package pgmigrate

import (
	"math"
	"testing"
	"time"
	_ "time/tzdata" // the zone tests need the zone database on every OS
)

// A time is stored as the SQLite build stores every time: UTC, rounded to the nearest second.
func TestConvertTime(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	chatham, err := time.LoadLocation("Pacific/Chatham")
	if err != nil {
		t.Fatal(err)
	}
	cdt, cst := time.FixedZone("CDT", -5*3600), time.FixedZone("CST", -6*3600)
	for _, c := range []struct {
		name string
		in   time.Time
		want string
	}{
		{"just under half a second rounds down", time.Date(2026, 3, 8, 7, 59, 59, 499999500, time.UTC), "2026-03-08 07:59:59"},
		{"half a second rounds up", time.Date(2026, 3, 8, 7, 59, 59, 500000000, time.UTC), "2026-03-08 08:00:00"},
		{"Chicago, the last second before the spring change", time.Date(2026, 3, 8, 1, 59, 59, 0, chicago), "2026-03-08 07:59:59"},
		{"Chicago, the first second after it", time.Date(2026, 3, 8, 3, 0, 0, 0, chicago), "2026-03-08 08:00:00"},
		{"Chicago, rounding across the change", time.Date(2026, 3, 8, 1, 59, 59, 600000000, chicago), "2026-03-08 08:00:00"},
		{"Chicago, the repeated autumn hour, first time", time.Date(2026, 11, 1, 1, 30, 0, 0, cdt), "2026-11-01 06:30:00"},
		{"Chicago, the repeated autumn hour, second time", time.Date(2026, 11, 1, 1, 30, 0, 0, cst), "2026-11-01 07:30:00"},
		{"Chatham in its summer, UTC+13:45", time.Date(2026, 1, 15, 12, 0, 0, 0, chatham), "2026-01-14 22:15:00"},
		{"Chatham in its winter, UTC+12:45", time.Date(2026, 7, 15, 12, 0, 0, 0, chatham), "2026-07-14 23:15:00"},
		{"Chatham, half a second over a day", time.Date(2026, 1, 15, 13, 44, 59, 500000000, chatham), "2026-01-15 00:00:00"},
		{"this PC's own zone", time.Date(2026, 10, 1, 20, 0, 0, 0, time.Local), "2026-10-02 01:00:00"},
	} {
		if got := ConvertTime(c.in); got != c.want {
			t.Errorf("MIG-CONV-TIME: %s: %s stored as %q, want %q", c.name, c.in.Format(time.RFC3339Nano), got, c.want)
		}
	}
}

func TestConvertNumeric(t *testing.T) {
	for _, c := range []struct {
		in   string
		want float64
	}{
		{"50.00000000", 50},
		{"0.00000001", 0.00000001},
		{"3.12512345", 3.12512345},
		{"12.34567891", 12.34567891},
		{"0", 0},
	} {
		got, err := ConvertNumeric(c.in)
		if err != nil || math.Float64bits(got) != math.Float64bits(c.want) {
			t.Errorf("MIG-CONV-NUM: %q became %v (%v), want %v", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"NaN", "Infinity", "-Infinity", "", "1,5"} {
		if _, err := ConvertNumeric(bad); err == nil {
			t.Errorf("MIG-CONV-NAN: %q was taken as an amount", bad)
		}
	}
	if _, err := convert(KindMoney, "NaN"); CodeOf(err) != CodeRefused {
		t.Errorf("MIG-CONV-NAN: a NaN amount gave %v (code %d), want a refusal (30)", err, CodeOf(err))
	}
}

// A boolean is stored as INTEGER 1 or 0, and NULL stays NULL: payouts.confirmed is NULL on rows
// older versions wrote, and nothing on the dashboard would show it turned into false.
func TestConvertBool(t *testing.T) {
	for _, c := range []struct {
		in   any
		want any
	}{{true, int64(1)}, {false, int64(0)}, {nil, nil}} {
		got, err := convert(KindBool, c.in)
		if err != nil || got != c.want {
			t.Errorf("MIG-CONV-BOOL: %#v stored as %#v (%v), want %#v", c.in, got, err, c.want)
		}
	}
}

func TestConvertKeepsNullAndEmptyText(t *testing.T) {
	for _, k := range []Kind{KindInt, KindText, KindBool, KindMoney, KindFloat, KindTime} {
		if got, err := convert(k, nil); got != nil || err != nil {
			t.Errorf("MIG-CONV-NULL: a NULL %s became %#v (%v)", k, got, err)
		}
	}
	if got, _ := convert(KindText, ""); got != "" {
		t.Errorf("MIG-CONV-NULL: '' became %#v: txid NULL and '' must stay distinct", got)
	}
}

// A column an older database lacks gets what 1.0.12's own ADD COLUMN gave it, and nothing else.
func TestDefaultsForMissingColumns(t *testing.T) {
	want := map[string]any{
		"payouts.status":          "pending",
		"pool_config.payout_mode": "solo",
		"blocks.is_solo":          int64(0),
		"blocks_1175.is_solo":     int64(0),
		"blocks_1175.distributed": int64(0),
	}
	for _, tb := range Tables {
		for _, c := range tb.Columns {
			if got := c.Default; got != want[tb.Name+"."+c.PG] {
				t.Errorf("MIG-CONV-DEFAULT: %s.%s missing in PostgreSQL gets %#v, want %#v", tb.Name, c.PG, got, want[tb.Name+"."+c.PG])
			}
		}
	}
	// A 1.0.0 payouts row: no status column.
	payouts, _ := table("payouts")
	have := []string{"id", "miner_address", "block_height", "amount", "confirmed", "txid", "created_at", "paid_at"}
	row, err := convertRow(payouts, have, []any{int64(7), "addr", int64(100), "3.125", true, "coinbase-direct",
		time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC), nil})
	if err != nil {
		t.Fatal(err)
	}
	wantRow := []any{int64(7), "addr", int64(100), 3.125, int64(1), "pending", "coinbase-direct", "2025-01-02 03:04:05", nil}
	for i := range wantRow {
		if row[i] != wantRow[i] {
			t.Errorf("MIG-CONV-DEFAULT: a 1.0.0 payouts row's %s became %#v, want %#v", payouts.Columns[i].PG, row[i], wantRow[i])
		}
	}
}

// A time without a time zone is refused, not guessed at.
func TestTimestampWithoutZoneRefused(t *testing.T) {
	blocks, _ := table("blocks")
	var created Column
	for _, c := range blocks.Columns {
		if c.PG == "created_at" {
			created = c
		}
	}
	if err := checkType("blocks", created, pgTimestampTZ); err != nil {
		t.Fatalf("MIG-CONV-TZ: a timestamptz column was refused: %v", err)
	}
	err := checkType("blocks", created, "timestamp without time zone")
	if CodeOf(err) != CodeRefused {
		t.Fatalf("MIG-CONV-TZ: a timestamp without time zone gave %v (code %d), want a refusal (30)", err, CodeOf(err))
	}
	if err := checkType("blocks", created, "text"); CodeOf(err) != CodeRefused {
		t.Fatalf("MIG-CONV-TYPE: a time column of type text gave %v, want a refusal (30)", err)
	}
}
