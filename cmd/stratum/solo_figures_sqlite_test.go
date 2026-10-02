//go:build sqlite

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stats"
	"go.uber.org/zap"
)

// The stratum's solo figures for the dashboard count every block, and a database that does not
// answer is said to be one (503), not shown as no blocks.
func TestSoloFiguresSayWhenTheDatabaseIsDown(t *testing.T) {
	if err := stats.InitDB(filepath.Join(t.TempDir(), "f.db")); err != nil {
		t.Fatal(err)
	}
	defer stats.CloseDB()
	saved := logger
	logger = zap.NewNop()
	t.Cleanup(func() { logger = saved })
	for h := int64(1); h <= 105; h++ { // more than the 100 the list holds
		if err := stats.SaveSoloBlockCoinbaseDirect(blockTestPayout, h, 2.5, fmt.Sprintf("%064x", h)); err != nil {
			t.Fatal(err)
		}
	}
	call := func(h http.HandlerFunc) (int, map[string]any) {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", "/internal/x?miner="+url.QueryEscape(blockTestPayout), nil))
		var m map[string]any
		json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	if code, m := call(minerSoloBlocks); code != 200 || m["total"] != 105.0 || m["totalReward"] != 262.5 {
		t.Fatalf("DATA6-STRATUM-BLOCKS: %d %v", code, m)
	}
	if code, m := call(minerSoloPayouts); code != 200 || m["total"] != 105.0 || m["totalPaid"] != 262.5 {
		t.Fatalf("DATA6-STRATUM-PAYOUTS: %d %v", code, m)
	}
	stats.CloseDB()
	if code, _ := call(minerSoloBlocks); code != http.StatusServiceUnavailable {
		t.Fatalf("DATA5-STRATUM-BLOCKS: no database, and the blocks answered %d", code)
	}
	if code, _ := call(minerSoloPayouts); code != http.StatusServiceUnavailable {
		t.Fatalf("DATA5-STRATUM-PAYOUTS: no database, and the payouts answered %d", code)
	}
}
