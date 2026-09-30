package stratum

import (
	"encoding/json"
	"io"
	"net"
	"testing"
)

// MRR's rig proxy connects rented rigs as "xminer-1.2.6" (and release candidates) with worker
// "mrr". Forge Pool classes it as a rental (forge-pool 97b9db4); here it must at least be
// recognised, so the dashboard's rental counts name the marketplace.
func TestMRRRigProxyIsRecognised(t *testing.T) {
	for _, ua := range []string{"xminer-1.2.6", "xminer-1.2.6-rc3", "xminer-1.2.6-rc5"} {
		if r := detectRentalService(ua); r != RentalMRR {
			t.Fatalf("MRR-XMINER: %q detected as %v, want MRR", ua, r)
		}
	}
	if r := detectRentalFromWorker("mrr"); r != RentalMRR {
		t.Fatalf("MRR-WORKER: worker \"mrr\" detected as %v, want MRR", r)
	}
}

// In solo, recognising MRR changes what is reported and nothing else: identity is recorded,
// the rental floor is not applied (see TestSoloRecordsMarketplaceIdentityWithoutApplyingItsFloor
// for why a home stratum proxy must never be pinned at 500000 on the main port).
func TestSoloMRRRigProxyIsCountedNotFloored(t *testing.T) {
	s := newSoloServer(t, testPayout)
	poolSide, minerSide := net.Pipe()
	t.Cleanup(func() { poolSide.Close(); minerSide.Close() })
	go io.Copy(io.Discard, minerSide)

	c := &Client{ID: "c", Conn: poolSide, IP: "203.0.113.5:4000", Difficulty: s.config.AbsoluteMinDiff}
	s.handleSubscribe(c, &Request{ID: 1, Method: MethodSubscribe, Params: json.RawMessage(`["xminer-1.2.6"]`)})
	params, _ := json.Marshal([]string{"mrr", "x"})
	if r := s.handleAuthorize(c, &Request{ID: 2, Method: MethodAuthorize, Params: params}); r.Result != true {
		t.Fatalf("authorize refused: %+v", r.Error)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.DetectedMarketplace != RentalMRR {
		t.Fatalf("MRR-SOLO-IDENTITY: the rig proxy was not recorded as MRR (got %v)", c.DetectedMarketplace)
	}
	if c.RentalService != RentalNone || c.Difficulty >= 500000 {
		t.Fatalf("MRR-SOLO-FLOOR: solo applied the rental floor on the main port (RentalService=%v, difficulty %g)",
			c.RentalService, c.Difficulty)
	}
}

// Ordinary mining software and worker names are not mistaken for a rental.
func TestOrdinaryMinersAreNotRentals(t *testing.T) {
	for _, ua := range []string{"cgminer/4.12.1", "bmminer/2.0.0", "bosminer/1.0.0", "Antminer S19j Pro", "bitaxe/2.4.2",
		"NerdMiner", "sgminer/5.0", "Whatsminer M30S", "AvalonMiner 1246", "xmrig/6.21"} {
		if r := detectRentalService(ua); r != RentalNone {
			t.Fatalf("NOT-RENTAL: %q detected as %v", ua, r)
		}
	}
	for _, w := range []string{"rig1", "mrrig", "default", "worker_mrr", "s19"} {
		if r := detectRentalFromWorker(w); r != RentalNone {
			t.Fatalf("NOT-RENTAL: worker %q detected as %v", w, r)
		}
	}
}
