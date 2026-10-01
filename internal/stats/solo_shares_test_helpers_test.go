package stats

import "testing"

// checkSoloShares is the behaviour both databases must have: a solo share is not stored, a PPLNS
// share is, and ClearSoloShares removes the solo shares earlier versions stored.
func checkSoloShares(t *testing.T, insertOld func(solo bool)) {
	t.Helper()
	count := func(where string) int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM shares WHERE ` + where).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n, err := ClearSoloShares(); err != nil || n != 0 {
		t.Fatalf("empty table: ClearSoloShares = %d, %v; want 0, nil", n, err)
	}
	if err := SaveShare("bitcoincashii:qtestminer", "rig1", 1024, true); err != nil {
		t.Fatal(err)
	}
	if n := count("TRUE"); n != 0 {
		t.Fatalf("a solo share was stored (%d rows): nothing reads solo shares", n)
	}
	if err := SaveShare("bitcoincashii:qtestminer", "rig1", 1024, false); err != nil {
		t.Fatal(err)
	}
	if n := count("NOT is_solo"); n != 1 {
		t.Fatalf("a PPLNS share was not stored (%d rows)", n)
	}

	// Mixed, as an install that once ran PPLNS: only the solo rows go.
	for i := 0; i < 3; i++ {
		insertOld(true)
	}
	if n, err := ClearSoloShares(); err != nil || n != 3 {
		t.Fatalf("mixed table: ClearSoloShares = %d, %v; want 3, nil", n, err)
	}
	if solo, pplns := count("is_solo"), count("NOT is_solo"); solo != 0 || pplns != 1 {
		t.Fatalf("after clearing a mixed table: %d solo, %d PPLNS rows; want 0 and 1", solo, pplns)
	}

	// Only solo rows, as every Forge Solo install up to 1.0.12: the whole table is emptied.
	if _, err := db.Exec(`DELETE FROM shares`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		insertOld(true)
	}
	if n, err := ClearSoloShares(); err != nil || n != -1 {
		t.Fatalf("solo-only table: ClearSoloShares = %d, %v; want -1 (emptied), nil", n, err)
	}
	if n := count("TRUE"); n != 0 {
		t.Fatalf("%d rows left after clearing a solo-only table", n)
	}
}
