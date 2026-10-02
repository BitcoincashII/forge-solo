package tidesgw

import (
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/datum/wire"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// The pool credits this install's shares while its window holds none of its work: said on the
// dashboard (Status) and once in the log, rather than seen only as blocks that pay others.
func TestAWindowWithoutThisInstallIsReported(t *testing.T) {
	p := newFakePool(t)
	p.snap.Work = map[string]float64{other: 5}
	core, logs := observer.New(zapcore.WarnLevel)
	g := newGateway(t, p)
	g.logger = zap.New(core)
	credited := func(ago time.Duration) {
		g.mu.Lock()
		g.acceptedBy, g.firstAccepted, g.snap = me, time.Now().Add(-ago), nil
		g.mu.Unlock()
	}
	register := func() {
		t.Helper()
		if _, err := g.Register(template(), me, nil); err != nil {
			t.Fatal(err)
		}
	}

	credited(20 * time.Minute)
	register()
	if !g.Status().NotInWindow {
		t.Fatal("TIDES4-WARN: the window holds none of this install's work, and nothing says so")
	}
	register()
	if n := logs.FilterMessageSnippet("lists none of this install's work").Len(); n != 1 {
		t.Fatalf("TIDES4-LOG-ONCE: said %d times, want once", n)
	}

	credited(2 * time.Minute)
	register()
	if g.Status().NotInWindow {
		t.Fatal("TIDES4-GRACE: warned before the pool's snapshot could hold the first shares")
	}

	p.mu.Lock()
	p.snap.Work = map[string]float64{other: 5, strings.ToUpper(me): 1}
	p.mu.Unlock()
	credited(20 * time.Minute)
	register()
	if g.Status().NotInWindow {
		t.Fatal("TIDES4-IN-WINDOW: warned while the window holds this install's work")
	}

	p.mu.Lock()
	p.snap.Work = map[string]float64{}
	p.mu.Unlock()
	credited(20 * time.Minute)
	register()
	if g.Status().NotInWindow {
		t.Fatal("TIDES4-EMPTY-WINDOW: an empty window pays the finder; nothing is missing")
	}

	g.mu.Lock()
	g.acceptedBy, g.snap = "", nil
	g.mu.Unlock()
	p.mu.Lock()
	p.snap.Work = map[string]float64{other: 5}
	p.mu.Unlock()
	register()
	if g.Status().NotInWindow {
		t.Fatal("TIDES4-NO-SHARES: warned with no share of this install's credited yet")
	}
}

// The first time the pool credits a share to an address is kept until shares go to another.
func TestTheFirstCreditedShareIsRemembered(t *testing.T) {
	g := newGateway(t, newFakePool(t))
	accept := func(miner string) {
		g.mu.Lock()
		g.tally([]queued{{share: wire.Share{Miner: miner}}}, &wire.ShareBatchResponse{Results: []wire.ShareResult{{Accepted: true}}})
		g.mu.Unlock()
	}
	accept(me)
	g.mu.Lock()
	by, first := g.acceptedBy, g.firstAccepted
	g.mu.Unlock()
	if by != me || time.Since(first) > time.Minute {
		t.Fatalf("TIDES4-TALLY: credited to %q at %v", by, first)
	}
	time.Sleep(10 * time.Millisecond)
	accept(me)
	g.mu.Lock()
	again := g.firstAccepted
	g.mu.Unlock()
	if !again.Equal(first) {
		t.Fatal("TIDES4-FIRST-STAYS: a later share moved the time of the first")
	}
	accept(other)
	g.mu.Lock()
	by, moved := g.acceptedBy, g.firstAccepted
	g.mu.Unlock()
	if by != other || !moved.After(first) {
		t.Fatal("TIDES4-NEW-ADDRESS: shares credited to a new address kept the old one's time")
	}
}
