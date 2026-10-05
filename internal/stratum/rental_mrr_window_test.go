package stratum

import (
	"io"
	"math"
	"net"
	"testing"
	"time"
)

// MiningRigRentals shows each SHA-256 rig an "optimal difficulty" range: one share every 10 to 60
// seconds at the rig's ADVERTISED hashrate (a 5.00 PH/s rig: 11,642k - 69,849k; 270 TH/s: 629k -
// 3,772k; 10 PH/s: 23,283k - 140m), and warns the owner of a "Low Worker Difficulty" below it. The
// rental port's vardiff, as shipped, keeps a steady rental inside that range whether the rig
// delivers 80% or 110% of what it advertises, from 0.1 to 10 PH/s. At target_time 5 a 4.5 PH/s rig
// sat at 5-6M, below its range, for the whole rental.
func mrrWindow(advertised float64) (lo, hi float64) {
	return advertised * 10 / (1 << 32), advertised * 60 / (1 << 32)
}

// timeIn is the part of [t0,t1) the difficulty spent within [lo,hi].
func timeIn(ch []diffChange, t0, t1, lo, hi float64) float64 {
	in := 0.0
	for i, c := range ch {
		end := t1
		if i+1 < len(ch) {
			end = math.Min(ch[i+1].sec, t1)
		}
		if st := math.Max(c.sec, t0); end > st && c.diff >= lo && c.diff <= hi {
			in += end - st
		}
	}
	return in / (t1 - t0)
}

func TestRentalPortStaysInMiningRigRentalsOptimalWindow(t *testing.T) {
	s := rentalPortServer()
	floor := s.config.AbsoluteMinDiff
	for _, h := range []float64{0.1e15, 1e15, 4.5e15, 10e15} {
		start := math.Max(level(s, h), floor)
		for _, delivered := range []float64{0.8, 1.0, 1.1} {
			lo, hi := mrrWindow(h / delivered)
			var in, changes float64
			const seeds, warm, hours = 12, 600.0, 3.0
			for seed := int64(1); seed <= seeds; seed++ {
				ch := simulateVardiff(t, s, seed, start, func(float64) float64 { return h }, warm+hours*3600)
				in += timeIn(ch, warm, warm+hours*3600, lo, hi) / seeds
				for _, c := range ch {
					if c.sec >= warm {
						changes++
					}
				}
			}
			if in < 0.99 {
				t.Errorf("RENTAL-MRR-WINDOW: %.1f PH/s delivering %.0f%% of its advertised hashrate: inside MRR's optimal range (%.4g - %.4g) %.1f%% of the time",
					h/1e15, 100*delivered, lo, hi, 100*in)
			}
			if perHour := changes / (seeds * hours); perHour > 6 {
				t.Errorf("RENTAL-MRR-CHANGES: %.1f PH/s: %.1f difficulty changes an hour", h/1e15, perHour)
			}
		}
		// A fresh order opens at the floor and is inside its range within a minute.
		if h >= 1e15 {
			lo, hi := mrrWindow(h / 0.9)
			m := newRampMiner(t, s, 7)
			m.mine(h, 3600)
			entered := math.Inf(1)
			for _, c := range m.changes {
				if c.diff >= lo && c.diff <= hi {
					entered = c.sec
					break
				}
			}
			if entered > 60 {
				t.Errorf("RENTAL-MRR-RAMP: a fresh %.1f PH/s order reached MRR's optimal range after %.0f s", h/1e15, entered)
			}
		}
	}
}

// A rental whose level holds still is remembered for as long as its shares confirm it. Remembered
// only when vardiff changed it, the level aged out after diffMemoryTTL while the rig mined on at it:
// at the rental port's target_time a steady rig changes level two or three times an hour, so a
// quarter of the time MiningRigRentals' health checks, which log in under the order's own name from
// the same address every few seconds, and the rig itself on a reconnect, opened at the 500000 floor.
func TestALevelTheSharesConfirmIsRemembered(t *testing.T) {
	s := rentalPortServer()
	s.config.SoloOnly, s.config.CreditPayoutAddress = true, true
	s.SetSoloPayoutAddress(testPayout)
	const host = "203.0.113.9"
	lvl := level(s, 4.5e15)
	every := time.Duration(s.config.TargetShareTime) * time.Second
	now := time.Now()
	start := now.Add(-40 * time.Minute)

	poolSide, minerSide := net.Pipe()
	t.Cleanup(func() { poolSide.Close(); minerSide.Close() })
	go io.Copy(io.Discard, minerSide)
	rig := &Client{ID: "rig", Conn: poolSide, IP: host + ":4000", MinerID: testPayout, WorkerName: "mrr",
		Authorized: true, Difficulty: lvl, FirstRampDone: true, DifficultyChangedAt: start}
	// Its last change, 40 minutes ago, is when its level was last remembered.
	s.diffMemory.Store(diffMemoryKey(testPayout, "mrr", host), diffMem{diff: lvl, at: start})
	// Since then a share every target_time, exactly its level: no change.
	for at := start.Add(every); !at.After(now); at = at.Add(every) {
		rig.mu.Lock()
		n := rig.addShareSample(at, lvl)
		rig.mu.Unlock()
		if n >= VardiffMinShares {
			s.adjustVardiffAt(rig, at)
		}
	}
	rig.mu.RLock()
	d := rig.Difficulty
	rig.mu.RUnlock()
	if d != lvl {
		t.Fatalf("DIFFMEM-CONFIRMED-SETUP: a rig mining at exactly its level was moved from %.4g to %.4g", lvl, d)
	}

	if got, ok := s.recallDifficulty(testPayout, "mrr", host); !ok || got != lvl {
		t.Errorf("DIFFMEM-CONFIRMED-RECALL: 40 minutes of shares at its level, and the level remembered is %.4g (found %v), want %.4g",
			got, ok, lvl)
	}
	// A new connection from that address under that name: the health check, or the rig reconnecting.
	probeSide, far := net.Pipe()
	t.Cleanup(func() { probeSide.Close(); far.Close() })
	go io.Copy(io.Discard, far)
	probe := &Client{ID: "probe", Conn: probeSide, IP: host + ":5000", UserAgent: "infinite-hash-proxy/probe"}
	if resp := s.handleAuthorize(probe, &Request{ID: 2, Method: MethodAuthorize,
		Params: []byte(`["` + testPayout + `.mrr","x"]`)}); resp.Result != true {
		t.Fatalf("DIFFMEM-CONFIRMED-LOGIN: %+v", resp.Error)
	}
	probe.mu.RLock()
	opened := probe.Difficulty
	probe.mu.RUnlock()
	if opened != lvl {
		t.Errorf("DIFFMEM-CONFIRMED-RESUME: a connection under the rig's name from its address opened at %.4g, the rig's level is %.4g",
			opened, lvl)
	}
}
