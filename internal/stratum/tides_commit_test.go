package stratum

import (
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// A TIDES job commits to the share difficulty its miners work at (MaxDifficulty); the pool credits
// each share on it at least that, and at its own difficulty, 1024, when the job commits to nothing.
// Counting only miners with a recent share, the job registered while MiningRigRentals' health
// checks were logged in and the rig had not connected yet committed to nothing, and so did the one
// registered while the rig reconnected: the rental's shares on them were credited 0.2% of their
// work. A miner without a recent share now counts at what it was given, up to its port's floor or
// the level its shares had set; a claim still counts at the floor only.
func TestMaxDifficultyCountsAMinerBeforeItsFirstShare(t *testing.T) {
	s := rentalPortServer()
	floor := s.config.AbsoluteMinDiff
	const host = "192.168.5.148"
	const remembered = 7172222.77
	s.clients.Store("probe", &Client{ID: "probe", IP: host + ":33152", Authorized: true, MinerID: testPayout,
		WorkerName: "mrr", UserAgent: "infinite-hash-proxy/probe", Difficulty: floor, LastDifficultySent: floor})
	if got := s.MaxDifficulty(); got != floor {
		t.Errorf("TIDES-COUNT-FLOOR: a miner logged in at the floor, no share yet, counts %g, want %g", got, floor)
	}

	// The rig ramped to 7.17M, then reconnected: it logs in at the level remembered for it.
	s.rememberDifficulty(testPayout, "mrr", host, remembered)
	s.clients.Store("rig", &Client{ID: "rig", IP: host + ":36478", Authorized: true, MinerID: testPayout,
		WorkerName: "mrr", Difficulty: remembered, LastDifficultySent: remembered})
	if got := s.MaxDifficulty(); got != remembered {
		t.Errorf("TIDES-COUNT-REMEMBERED: a miner back at its remembered %g, no share yet, counts %g", remembered, got)
	}
	// Given less than its level, it counts what it was given.
	s.clients.Store("rig", &Client{ID: "rig", IP: host + ":36478", Authorized: true, MinerID: testPayout,
		WorkerName: "mrr", Difficulty: 2e6, LastDifficultySent: 2e6})
	if got := s.MaxDifficulty(); got != 2e6 {
		t.Errorf("TIDES-COUNT-GIVEN: a miner given 2e6 below its remembered %g counts %g", remembered, got)
	}

	// A claim counts at the floor only: d=1000000000000 in a password, from an address with no level.
	for _, srv := range []*Server{newSoloServer(t, testPayout), rentalPortServer()} {
		srv.clients.Store("claim", &Client{ID: "claim", IP: "203.0.113.50:1", Authorized: true, MinerID: testPayout,
			WorkerName: "x", Difficulty: 1e12, LastDifficultySent: 1e12})
		srv.clients.Store("unauthorized", &Client{ID: "unauthorized", Difficulty: 1e9, LastDifficultySent: 1e9})
		if got, want := srv.MaxDifficulty(), srv.config.AbsoluteMinDiff; got != want {
			t.Errorf("TIDES-COUNT-CLAIM: a claim of 1e12 counts %g, want the floor %g", got, want)
		}
	}
}

// A rental that reconnects leaves a gap: the old connection is gone and the new one has no share
// yet. A miner that disconnects counts at the level its shares proved until provenFor after the
// last of them, as it would connected, so the job registered in that gap is committed to it.
func TestADisconnectedMinerCountsUntilItsLastShareIsOld(t *testing.T) {
	s := NewServer(&ServerConfig{Host: "127.0.0.1", Port: 0, MaxConnections: 10, ExtraNonce1Size: 4, ExtraNonce2Size: 8,
		MinDiff: 500000, MaxDiff: 1e12, SoloOnly: true, CreditPayoutAddress: true}, zap.NewNop(), nil, nil)
	s.SetSoloPayoutAddress(testPayout)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	conn := loggedIn(t, s)
	const level = 7172222.77
	proven := time.Now()
	s.clients.Range(func(_, v interface{}) bool {
		c := v.(*Client)
		c.mu.Lock()
		c.Difficulty, c.LastDifficultySent, c.ProvenDifficulty, c.ProvenAt = level, level, level, proven
		c.mu.Unlock()
		return true
	})
	conn.Close()
	for end := time.Now().Add(3 * time.Second); s.clientCount.Load() != 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the miner's connection did not end")
		}
	}
	if got := s.MaxDifficulty(); got != level {
		t.Errorf("TIDES-DEPARTED-COUNTS: a miner that proved %g and disconnected a moment ago counts %g", level, got)
	}
	if got := s.maxDifficultyAt(proven.Add(provenFor)); got != 0 {
		t.Errorf("TIDES-DEPARTED-EXPIRES: %v after its last share a disconnected miner still counts %g", provenFor, got)
	}
}

// Each login that is sent work runs the login handler: in TIDES mode it asks for a new job when the
// one in flight commits to less than the miner works at.
func TestALoginRunsTheLoginHandler(t *testing.T) {
	s, _ := reasonServer(t)
	var n atomic.Int32
	s.SetLoginHandler(func() { n.Add(1) })
	loggedIn(t, s)
	for end := time.Now().Add(2 * time.Second); n.Load() != 1; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("TIDES-LOGIN-HANDLER: a login ran the login handler %d times, want once", n.Load())
		}
	}
}
