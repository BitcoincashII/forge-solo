package main

import (
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/mining"
)

// Merge mining could previously fail for the entire life of a process while every surface a
// user can see -- the settings card, the dashboard, the API -- went on showing the
// configured address as though it were earning. A fresh Umbrel install hits this every
// time, because the stratum starts while the 1175 node is still doing IBD.
//
// The distinction that matters most is "never_worked" versus "failing": one is almost
// always a node still syncing, the other is something that broke.
func TestAuxStatusFrom(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name      string
		health    mining.AuxHealth
		wantState string
		wantErr   string
	}{
		{
			name:      "not configured",
			health:    mining.AuxHealth{Enabled: false},
			wantState: "off",
		},
		{
			name:      "healthy",
			health:    mining.AuxHealth{Enabled: true, LastOKAt: now.Add(-5 * time.Second)},
			wantState: "ok",
		},
		{
			// The fresh-install case: enabled, erroring, and never once successful.
			name:      "never produced work",
			health:    mining.AuxHealth{Enabled: true, LastErr: "getauxblock rpc error -32603: Loading block index", LastErrAt: now},
			wantState: "never_worked",
			wantErr:   "getauxblock rpc error -32603: Loading block index",
		},
		{
			name: "worked, then stopped",
			health: mining.AuxHealth{
				Enabled: true, LastOKAt: now.Add(-10 * time.Minute),
				LastErr: "connection refused", LastErrAt: now,
			},
			wantState: "failing",
			wantErr:   "connection refused",
		},
		{
			// A single failed poll between successful ones is not an outage. Reporting it
			// as one would train the user to ignore the warning that matters.
			name: "one blip, work still recent",
			health: mining.AuxHealth{
				Enabled: true, LastOKAt: now.Add(-3 * time.Second),
				LastErr: "EOF", LastErrAt: now,
			},
			wantState: "ok",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state, errText, age := auxStatusFrom(tc.health, now)
			if state != tc.wantState {
				t.Errorf("state = %q, want %q", state, tc.wantState)
			}
			if tc.wantErr != "" && errText != tc.wantErr {
				t.Errorf("errText = %q, want %q", errText, tc.wantErr)
			}
			if tc.health.LastOKAt.IsZero() && age != -1 {
				t.Errorf("last-OK age = %d, want -1 when aux work has never succeeded", age)
			}
		})
	}
}

// The boundary must not flap a healthy install into a warning.
func TestAuxStatusStaleBoundary(t *testing.T) {
	now := time.Now()
	h := mining.AuxHealth{Enabled: true, LastOKAt: now.Add(-auxStaleAfter), LastErr: "x", LastErrAt: now}
	if state, _, _ := auxStatusFrom(h, now); state != "ok" {
		t.Errorf("state = %q at exactly the staleness boundary, want ok", state)
	}
	h.LastOKAt = now.Add(-auxStaleAfter - time.Second)
	if state, _, _ := auxStatusFrom(h, now); state != "failing" {
		t.Errorf("state = %q one second past the boundary, want failing", state)
	}
}
