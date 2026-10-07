package stats

import (
	"path/filepath"
	"testing"
)

// An install that saved its settings under 1.0.11 has the old default tag stored; it must read
// back as no tag chosen, while a tag the user typed reads back as typed.
func TestStoredOldDefaultTagReadsAsNoneChosen(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "x.db")); err != nil {
		t.Fatal(err)
	}
	defer CloseDB()
	const addr = "bitcoincashii:qtestminer000000000000000000000000000000"
	for stored, want := range map[string]string{"Forge": "", "MyRig": "MyRig", "": ""} {
		if err := SavePoolConfig(addr, "", stored); err != nil {
			t.Fatal(err)
		}
		pool, _, tag, err := GetPoolConfig()
		if err != nil {
			t.Fatal(err)
		}
		if tag != want || pool != addr {
			t.Errorf("stored tag %q: read pool %q tag %q, want %q %q", stored, pool, tag, addr, want)
		}
	}
}
