//go:build sqlite

package stats

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The api reloads the miner settings every 10 s; the count is logged when it changes, not every time.
func TestMinerSettingsCountIsLoggedOnlyWhenItChanges(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "x.db")); err != nil {
		t.Fatal(err)
	}
	defer CloseDB()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	minerSettingsLogged.Store(0)
	for i := 0; i < 5; i++ {
		LoadAllMinerSettings()
	}
	if n := strings.Count(buf.String(), "miner settings from database"); n != 1 {
		t.Fatalf("five reloads of an unchanged table logged %d lines, want 1:\n%s", n, buf.String())
	}
}
