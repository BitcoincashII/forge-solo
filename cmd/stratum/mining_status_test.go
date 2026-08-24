package main

import (
	"strings"
	"testing"
	"time"
)

// The reason ladder is the whole point of /internal/mining-status: a user whose node is
// synced and whose payout address is saved has no other way to find out why no work is
// reaching their miner. Each case here is a real support report.
func TestMiningStatusNamesTheFirstRealBlocker(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-5 * time.Second)
	stale := now.Add(-jobStaleAfter - time.Second)

	tests := []struct {
		name        string
		configured  bool
		dbConnected bool
		jobAt       time.Time
		tmplErr     string
		connections int64
		authorized  int64
		noShares    bool
		wantMining  bool
		wantReason  string
		wantInMsg   string
	}{
		{
			name:        "mining when a job was just built",
			configured:  true,
			dbConnected: true,
			jobAt:       fresh,
			wantMining:  true,
			wantReason:  "",
		},
		{
			// Jobs flowing, miners arriving, none authorizing: the shipped 1.0.8 behaviour
			// for a user who typed the worker name the docs told them to.
			name:        "miners connecting but none authorizing",
			configured:  true,
			dbConnected: true,
			jobAt:       fresh,
			connections: 3,
			authorized:  0,
			wantReason:  "miners_refused",
			wantInMsg:   "worker username",
		},
		{
			name:        "authorized but no accepted shares",
			configured:  true,
			dbConnected: true,
			jobAt:       fresh,
			connections: 1,
			authorized:  1,
			noShares:    true,
			wantReason:  "no_shares",
			wantInMsg:   "actually hashing",
		},
		{
			// The bug this endpoint exists for: address saved in the dashboard, stratum
			// never accepted it, banner used to say "ready to mine".
			name:        "no payout address in effect",
			configured:  false,
			dbConnected: true,
			jobAt:       fresh,
			wantReason:  "no_payout_address",
			wantInMsg:   "Settings",
		},
		{
			// Unconfigured AND no DB: the DB is the *cause* (settings unreadable), so
			// telling the user to open Settings would send them somewhere useless.
			name:       "database unavailable outranks the address message",
			configured: false,
			jobAt:      fresh,
			wantReason: "db_unavailable",
			wantInMsg:  "database unavailable",
		},
		{
			name:        "configured but the node has never served a template",
			configured:  true,
			dbConnected: true,
			jobAt:       time.Time{},
			wantReason:  "no_template_yet",
			wantInMsg:   "Waiting for the first block template",
		},
		{
			name:        "first-template failure names the node error",
			configured:  true,
			dbConnected: true,
			jobAt:       time.Time{},
			tmplErr:     "rpc error: Bitcoin is downloading blocks...",
			wantReason:  "no_template_yet",
			wantInMsg:   "downloading blocks",
		},
		{
			name:        "work stopped flowing after it once worked",
			configured:  true,
			dbConnected: true,
			jobAt:       stale,
			wantReason:  "stale_template",
			wantInMsg:   "No new work in",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conns, auth := tc.connections, tc.authorized
			if conns == 0 && auth == 0 {
				conns, auth = 1, 1 // default: one healthy, authorized miner
			}
			shareAt := fresh
			if tc.noShares {
				shareAt = time.Time{}
			}
			st := miningStatusFrom(tc.configured, tc.dbConnected, conns, auth, 93000, tc.jobAt, shareAt, tc.tmplErr, now)
			if st.Mining != tc.wantMining {
				t.Errorf("Mining = %v, want %v (reason %q)", st.Mining, tc.wantMining, st.Reason)
			}
			if st.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", st.Reason, tc.wantReason)
			}
			if tc.wantInMsg != "" && !strings.Contains(st.Message, tc.wantInMsg) {
				t.Errorf("Message = %q, want it to contain %q", st.Message, tc.wantInMsg)
			}
			if !tc.wantMining && st.Message == "" {
				t.Error("a non-mining status must carry a message the user can act on")
			}
		})
	}
}

// A job exactly at the staleness boundary is still mining: the boundary must not flap a
// healthy miner into a warning banner every 15s job cycle.
func TestMiningStatusBoundaryIsNotStale(t *testing.T) {
	now := time.Now()
	st := miningStatusFrom(true, true, 1, 1, 93000, now.Add(-jobStaleAfter), now, "", now)
	if !st.Mining {
		t.Errorf("a job exactly %s old reported not mining (reason %q)", jobStaleAfter, st.Reason)
	}
	if st.LastJobAgeSec != int64(jobStaleAfter.Seconds()) {
		t.Errorf("LastJobAgeSec = %d, want %d", st.LastJobAgeSec, int64(jobStaleAfter.Seconds()))
	}
}

// LastJobAgeSec is -1, never 0, when no job has ever been built: a dashboard that read 0
// as "just built" would show a brand-new never-working install as healthy.
func TestMiningStatusNoJobEverIsNegativeAge(t *testing.T) {
	st := miningStatusFrom(true, true, 0, 0, 0, time.Time{}, time.Time{}, "", time.Now())
	if st.LastJobAgeSec != -1 {
		t.Errorf("LastJobAgeSec = %d, want -1 when no job has ever been built", st.LastJobAgeSec)
	}
}

// An idle stratum must not report that it is mining.
//
// There was no rung for "nothing is connected", so connections=0 fell through to
// `default: Mining = true`. The dashboard then rendered its reassuring green
// banner -- which, worse, read "node synced and a miner is connected", a sentence
// only ever reachable when authorized==0 AND connections==0, i.e. false every
// single time it appeared. A rig that drops at 3am looked exactly like one that
// is working.
func TestMiningStatusIsNotMiningWithNothingConnected(t *testing.T) {
	now := time.Now()
	st := miningStatusFrom(
		true,                    // configured
		true,                    // dbConnected
		0,                       // connections
		0,                       // authorized
		1000,                    // jobHeight
		now.Add(-2*time.Second), // fresh job
		time.Time{},             // no share ever
		"", now,
	)
	if st.Mining {
		t.Error("reports mining:true with zero connections; the dashboard shows a green " +
			"'Mining' banner while nothing is attached")
	}
	if st.Reason != "no_miners" {
		t.Errorf("reason = %q, want %q", st.Reason, "no_miners")
	}
	if st.Message == "" {
		t.Error("no message to show the user")
	}
}

// The new rung must not swallow the states below it.
func TestMiningStatusLadderKeepsItsOtherRungs(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-2 * time.Second)

	cases := []struct {
		name                  string
		connections, authoriz int64
		shareAt               time.Time
		wantReason            string
		wantMining            bool
	}{
		{"connected but none authorized", 3, 0, time.Time{}, "miners_refused", false},
		{"authorized but silent", 1, 1, now.Add(-30 * time.Minute), "no_shares", false},
		{"authorized and hashing", 1, 1, now.Add(-5 * time.Second), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := miningStatusFrom(true, true, tc.connections, tc.authoriz, 1000, fresh, tc.shareAt, "", now)
			if st.Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", st.Reason, tc.wantReason)
			}
			if st.Mining != tc.wantMining {
				t.Errorf("mining = %v, want %v", st.Mining, tc.wantMining)
			}
		})
	}
}

// Faults that are not the miner's fault must still outrank "no miner connected" --
// otherwise a dead node with nothing attached blames the user's rig.
func TestMiningStatusNodeFaultsOutrankNoMiners(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name, want string
		jobAt      time.Time
		configured bool
	}{
		{"unconfigured", "no_payout_address", now.Add(-2 * time.Second), false},
		{"no template yet", "no_template_yet", time.Time{}, true},
		{"stale template", "stale_template", now.Add(-1 * time.Hour), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := miningStatusFrom(tc.configured, true, 0, 0, 1000, tc.jobAt, time.Time{}, "", now)
			if st.Reason != tc.want {
				t.Errorf("with nothing connected, reason = %q, want %q — the node's own "+
					"fault is being reported as a missing miner", st.Reason, tc.want)
			}
		})
	}
}
