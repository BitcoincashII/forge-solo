package stats

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A worker's all-time best share lived only in the stratum's memory: every restart and every update
// reset it, the miner's best with it. It is kept in the database now, read at start and written
// every few seconds when it changed, never once per share, and the kept ones are bounded.

const bestMiner = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"

// fakeBestDB stands in for the database's best_shares, as SaveBestSharesDB keeps it: a best is never
// lowered, nor its time put back.
type fakeBestDB struct {
	mu        sync.Mutex
	rows      map[bestKey]BestShare
	readErr   error
	writeErr  error
	reads     int
	writes    int
	lastWrite []BestShare
	lastGone  []BestShare
}

func useFakeBestDB(t *testing.T) *fakeBestDB {
	t.Helper()
	f := &fakeBestDB{rows: map[bestKey]BestShare{}}
	oldRead, oldWrite := readBestShares, writeBestShares
	readBestShares = func() ([]BestShare, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reads++
		if f.readErr != nil {
			return nil, f.readErr
		}
		var out []BestShare
		for _, r := range f.rows {
			out = append(out, r)
		}
		return out, nil
	}
	writeBestShares = func(raised, gone []BestShare) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.writeErr != nil {
			return f.writeErr
		}
		f.writes++
		f.lastWrite, f.lastGone = raised, gone
		for _, g := range gone {
			delete(f.rows, bestKey{g.Miner, g.Worker})
		}
		for _, r := range raised {
			k := bestKey{r.Miner, r.Worker}
			if old, ok := f.rows[k]; ok {
				r.Difficulty = max(r.Difficulty, old.Difficulty)
				if old.Seen.After(r.Seen) {
					r.Seen = old.Seen
				}
			}
			f.rows[k] = r
		}
		return nil
	}
	t.Cleanup(func() { readBestShares, writeBestShares = oldRead, oldWrite })
	return f
}

func (f *fakeBestDB) row(miner, worker string) (BestShare, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[bestKey{miner, worker}]
	return r, ok
}

func (f *fakeBestDB) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

func (f *fakeBestDB) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

func newStatsManager() *StatsManager { return &StatsManager{workers: make(map[string]*WorkerStats)} }

// startRun is a stratum that has read the database's bests.
func startRun(t *testing.T) *StatsManager {
	t.Helper()
	m := newStatsManager()
	if _, err := m.LoadBestShares(); err != nil {
		t.Fatalf("ATH-SETUP: reading the kept bests: %v", err)
	}
	return m
}

// keptOf is a worker's kept best. The test stops when there is none, rather than the whole package
// with a nil pointer.
func keptOf(t *testing.T, m *StatsManager, miner, worker string) *keptBest {
	t.Helper()
	b := m.kept[bestKey{miner, worker}]
	if b == nil {
		t.Fatalf("ATH-KEPT-NONE: no best is kept for %s %s", miner, worker)
	}
	return b
}

func mustWrite(t *testing.T, m *StatsManager) {
	t.Helper()
	if err := m.WriteBestShares(); err != nil {
		t.Fatalf("ATH-SETUP: writing the bests: %v", err)
	}
}

func TestBestShareOutlivesARestart(t *testing.T) {
	useFakeBestDB(t)
	run1 := startRun(t)
	run1.UpdateWorker(bestMiner, "s19", true, 1000, 5e9)
	run1.UpdateWorker(bestMiner, "s19", true, 1000, 4e6)
	run1.UpdateWorker(bestMiner, "bitaxe", true, 1000, 2e6)
	mustWrite(t, run1)

	// An update, a reboot: a new stratum, the same database.
	run2 := startRun(t)
	run2.UpdateWorker(bestMiner, "s19", true, 1000, 3000)
	w := run2.workers[bestMiner+":s19"]
	if w.ATHDiff != 5e9 {
		t.Errorf("ATH-RESTART-WORKER: after a restart the worker's best is %v, want the 5e9 of before", w.ATHDiff)
	}
	if w.BestDiff != 3000 || w.RoundBestDiff != 3000 {
		t.Errorf("ATH-ROUND-BEST: the round's best after a restart is %v/%v, want this run's 3000 (it resets on a block, as before)",
			w.BestDiff, w.RoundBestDiff)
	}
	if got := run2.KeptBest(bestMiner, "bitaxe"); got != 2e6 {
		t.Errorf("ATH-RESTART-KEPT: a worker not back yet keeps %v, want 2e6", got)
	}
	if got := run2.MinerBests()[bestMiner]; got != 5e9 {
		t.Errorf("ATH-RESTART-MINER: the miner's best after a restart is %v, want 5e9", got)
	}
}

// The database may come up after the first shares: what this run counted and what was kept come
// together, the higher of each.
func TestBestSharesReadAfterTheFirstShares(t *testing.T) {
	f := useFakeBestDB(t)
	f.rows[bestKey{bestMiner, "s19"}] = BestShare{Miner: bestMiner, Worker: "s19", Difficulty: 5e9}
	f.rows[bestKey{bestMiner, "s21"}] = BestShare{Miner: bestMiner, Worker: "s21", Difficulty: 1e9}
	f.readErr = errors.New("database is locked")

	m := newStatsManager()
	if _, err := m.LoadBestShares(); err == nil {
		t.Fatal("ATH-SETUP: the read did not fail")
	}
	m.UpdateWorker(bestMiner, "s19", true, 1000, 7e6)
	m.UpdateWorker(bestMiner, "s21", true, 1000, 9e9)
	if err := m.WriteBestShares(); err != nil || f.writeCount() != 0 {
		t.Fatalf("ATH-WRITE-BEFORE-READ: before the kept bests were read %d writes were made (%v); one could drop a best", f.writeCount(), err)
	}

	f.readErr = nil
	if n, err := m.LoadBestShares(); err != nil || n != 2 {
		t.Fatalf("ATH-READ-RETRY: the read after a failed one gave %d, %v", n, err)
	}
	if w := m.workers[bestMiner+":s19"]; w.ATHDiff != 5e9 {
		t.Errorf("ATH-READ-LIVE-WORKER: a worker counted before the read has best %v, want the kept 5e9", w.ATHDiff)
	}
	mustWrite(t, m)
	if r, _ := f.row(bestMiner, "s21"); r.Difficulty != 9e9 {
		t.Errorf("ATH-READ-HIGHER-STAYS: the kept best is %v, want this run's higher 9e9", r.Difficulty)
	}
	if n, err := m.LoadBestShares(); n != 0 || err != nil || f.reads != 2 {
		t.Errorf("ATH-READ-ONCE: a read after the first made %d reads, gave %d, %v", f.reads, n, err)
	}
}

// A share writes nothing itself: only a new best does, at the next write.
func TestBestSharesWrittenOnlyWhenRaised(t *testing.T) {
	f := useFakeBestDB(t)
	m := startRun(t)
	m.UpdateWorker(bestMiner, "s19", true, 1000, 5e9)
	mustWrite(t, m)
	if f.writeCount() != 1 {
		t.Fatalf("ATH-WRITE-FIRST: %d writes for a first best, want 1", f.writeCount())
	}
	for i := 0; i < 1000; i++ {
		m.UpdateWorker(bestMiner, "s19", true, 1000, float64(1000+i))
	}
	mustWrite(t, m)
	if f.writeCount() != 1 {
		t.Errorf("ATH-WRITE-PER-SHARE: 1000 shares below the best made %d writes, want none", f.writeCount()-1)
	}
	if seen, last := keptOf(t, m, bestMiner, "s19").seen, m.workers[bestMiner+":s19"].LastShareAt; !seen.Equal(last) {
		t.Errorf("ATH-SEEN-SHARE: the worker is kept as last seen at %v, want its last share's %v", seen, last)
	}
	m.UpdateWorker(bestMiner, "s19", true, 1000, 6e9)
	m.UpdateWorker(bestMiner, "s19", true, 1000, 8e9)
	mustWrite(t, m)
	if f.writeCount() != 2 || len(f.lastWrite) != 1 {
		t.Fatalf("ATH-WRITE-RAISE: two new bests made %d writes of %d rows, want 1 of 1", f.writeCount()-1, len(f.lastWrite))
	}
	if r, _ := f.row(bestMiner, "s19"); r.Difficulty != 8e9 {
		t.Errorf("ATH-WRITE-RAISE: the kept best is %v, want 8e9", r.Difficulty)
	}
	m.UpdateWorker(bestMiner, "s19", false, 1000, 9e10) // refused
	mustWrite(t, m)
	if f.writeCount() != 2 || m.KeptBest(bestMiner, "s19") != 8e9 {
		t.Errorf("ATH-INVALID: a refused share was kept as the best (%v)", m.KeptBest(bestMiner, "s19"))
	}
}

// What the database did not take is written at the next try, the removals too.
func TestBestSharesWrittenAgainAfterAFailedWrite(t *testing.T) {
	f := useFakeBestDB(t)
	m := startRun(t)
	m.UpdateWorker(bestMiner, "s19", true, 1000, 5e9)
	f.writeErr = errors.New("database is locked")
	if err := m.WriteBestShares(); err == nil {
		t.Fatal("ATH-SETUP: the write did not fail")
	}
	f.writeErr = nil
	mustWrite(t, m)
	if r, ok := f.row(bestMiner, "s19"); !ok || r.Difficulty != 5e9 {
		t.Fatalf("ATH-WRITE-RETRY: after a failed write the database holds %+v, want 5e9", r)
	}

	// A removal that failed is made at the next write.
	for i := 0; i < MaxKeptWorkers; i++ {
		m.UpdateWorker(bestMiner, fmt.Sprintf("rig%03d", i), true, 1000, 1000)
	}
	unlist(m, bestMiner, "rig000")
	keptOf(t, m, bestMiner, "rig000").seen = time.Time{} // seen longest ago
	f.writeErr = errors.New("database is locked")
	if err := m.WriteBestShares(); err == nil {
		t.Fatal("ATH-SETUP: the write did not fail")
	}
	f.writeErr = nil
	f.rows[bestKey{bestMiner, "rig000"}] = BestShare{Miner: bestMiner, Worker: "rig000", Difficulty: 1000}
	mustWrite(t, m)
	if _, ok := f.row(bestMiner, "rig000"); ok {
		t.Error("ATH-GONE-RETRY: a best dropped while the write failed is still in the database")
	}
}

// The database is written without the lock every share takes: a write can wait up to the database's
// busy timeout while another program holds it, and no share may wait with it. A best raised during
// the write is written at the next one.
func TestBestShareWriteHoldsNoLock(t *testing.T) {
	f := useFakeBestDB(t)
	m := startRun(t)
	m.UpdateWorker(bestMiner, "s19", true, 1000, 5e9)
	entered, release := make(chan struct{}), make(chan struct{})
	fake := writeBestShares
	var calls atomic.Int32
	writeBestShares = func(raised, gone []BestShare) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return fake(raised, gone)
	}
	t.Cleanup(func() { writeBestShares = fake })
	wrote := make(chan error, 1)
	go func() { wrote <- m.WriteBestShares() }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("ATH-SETUP: the write did not start")
	}
	shared := make(chan struct{})
	go func() {
		defer close(shared)
		m.UpdateWorker(bestMiner, "s19", true, 1000, 6e9)
	}()
	select {
	case <-shared:
	case <-time.After(time.Second):
		t.Error("ATH-WRITE-UNLOCKED: a share waited for the database write of the best shares")
	}
	close(release)
	select {
	case <-shared:
	case <-time.After(5 * time.Second):
		t.Fatal("ATH-SETUP: the share never ended")
	}
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ATH-SETUP: the write never ended")
	}
	mustWrite(t, m)
	if r, _ := f.row(bestMiner, "s19"); r.Difficulty != 6e9 {
		t.Errorf("ATH-WRITE-DURING: a best raised during a write is kept as %v after the next one, want 6e9", r.Difficulty)
	}
}

// The bests are written every few seconds: a crash loses no more than that, and the database is never
// written at the pace of the shares.
func TestBestSharesWrittenEveryFewSeconds(t *testing.T) {
	if bestShareWriteEvery < 2*time.Second || bestShareWriteEvery > time.Minute {
		t.Errorf("ATH-WRITE-EVERY: the bests are written every %v; want between 2 s and a minute", bestShareWriteEvery)
	}
}

// unlist removes a worker from the stratum's list, as a restart does for every worker until its
// next share.
func unlist(m *StatsManager, miner, worker string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.workers, miner+":"+worker)
}

// pruned is a worker as the stratum has it over a day after its last share: PruneStaleWorkers has
// removed it from the list, and its kept best was last seen over a day ago. The order in which the
// workers were seen stays.
func pruned(m *StatsManager, miner, worker string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.workers, miner+":"+worker)
	if b := m.kept[bestKey{miner, worker}]; b != nil {
		b.seen = b.seen.Add(-2 * WorkerRetention)
	}
}

// longAgo is a time more than a day before now: a worker last seen then no longer mines.
func longAgo() time.Time { return time.Now().Add(-3 * WorkerRetention) }

// keptWorkers is the names of a miner's kept bests, in order.
func keptWorkers(m *StatsManager, miner string) []string {
	var out []string
	for k := range m.kept {
		if k.miner == miner {
			out = append(out, k.worker)
		}
	}
	sort.Strings(out)
	return out
}

// A rented rig that comes back under a new name every time adds a name each time. Of the workers
// that have not mined for a day, a miner keeps its best and the ones seen last; the miners seen last
// are kept.
func TestKeptBestsAreBounded(t *testing.T) {
	f := useFakeBestDB(t)
	base := longAgo()
	f.rows[bestKey{bestMiner, "record"}] = BestShare{Miner: bestMiner, Worker: "record", Difficulty: 9e12, Seen: base} // seen first of all
	const extra = 30
	for i := 0; i < MaxKeptWorkers+extra; i++ {
		name := fmt.Sprintf("rental%03d", i)
		f.rows[bestKey{bestMiner, name}] = BestShare{Miner: bestMiner, Worker: name, Difficulty: 1e6, Seen: base.Add(time.Duration(i+1) * time.Minute)}
	}
	m := startRun(t)
	mustWrite(t, m)
	if m.KeptBest(bestMiner, "record") != 9e12 {
		t.Error("ATH-BOUND-BEST: the miner's best was dropped, the worker seen longest ago")
	}
	kept := keptWorkers(m, bestMiner)
	if len(kept) != MaxKeptWorkers {
		t.Fatalf("ATH-BOUND-WORKERS: %d workers kept for one miner, want %d", len(kept), MaxKeptWorkers)
	}
	// The record, and the 99 rentals seen last: rental031 to rental129.
	if kept[0] != "record" || kept[1] != fmt.Sprintf("rental%03d", extra+1) {
		t.Errorf("ATH-BOUND-LAST-SEEN: kept %s ... ; want the record and the workers seen last, from rental%03d", kept[:2], extra+1)
	}
	if f.count() != MaxKeptWorkers {
		t.Errorf("ATH-BOUND-DB: the database holds %d rows, want %d", f.count(), MaxKeptWorkers)
	}
	if got := m.MinerBests()[bestMiner]; got != 9e12 {
		t.Errorf("ATH-BOUND-MINER-BEST: the miner's best is %v, want 9e12", got)
	}

	// Miners: the ones seen last, after a restart.
	for i := 0; i < MaxKeptMiners+2; i++ {
		miner := fmt.Sprintf("miner%02d", i)
		f.rows[bestKey{miner, "rig"}] = BestShare{Miner: miner, Worker: "rig", Difficulty: 5e5, Seen: base.Add(time.Duration(200+i) * time.Minute)}
	}
	m = startRun(t)
	mustWrite(t, m)
	keptMiners := map[string]bool{}
	for k := range m.kept {
		keptMiners[k.miner] = true
	}
	if len(keptMiners) != MaxKeptMiners {
		t.Fatalf("ATH-BOUND-MINERS: %d miners kept, want %d", len(keptMiners), MaxKeptMiners)
	}
	if keptMiners[bestMiner] || keptMiners["miner00"] || keptMiners["miner01"] || !keptMiners["miner02"] {
		t.Errorf("ATH-BOUND-MINERS-LAST-SEEN: kept %v; want the miners seen last, from miner02", keptMiners)
	}
	if _, ok := f.row(bestMiner, "record"); ok || f.count() != MaxKeptMiners {
		t.Errorf("ATH-BOUND-MINERS-DB: the database holds %d rows (the dropped miner's record: %v), want %d", f.count(), ok, MaxKeptMiners)
	}
}

// A worker that mines is kept, and so is its miner, however many there are: the stratum's list has
// bounds of its own, and one dropped while it mines would be written again at its next share,
// every few seconds. A day after their last share, the bounds apply.
func TestListedWorkersAreAlwaysKept(t *testing.T) {
	f := useFakeBestDB(t)
	m := startRun(t)
	const farm = MaxKeptWorkers + 50
	for i := 0; i < MaxKeptMiners+5; i++ {
		m.UpdateWorker(fmt.Sprintf("miner%02d", i), "rig", true, 1000, 5e5)
	}
	for i := 0; i < farm; i++ {
		m.UpdateWorker(bestMiner, fmt.Sprintf("rig%03d", i), true, 1000, 1e6+float64(i))
	}
	mustWrite(t, m)
	if n := len(keptWorkers(m, bestMiner)); n != farm {
		t.Errorf("ATH-BOUND-LISTED: of %d workers mining for one miner %d are kept; want all", farm, n)
	}
	minersKept := map[string]bool{}
	for k := range m.kept {
		minersKept[k.miner] = true
	}
	if len(minersKept) != MaxKeptMiners+6 || f.count() != farm+MaxKeptMiners+5 {
		t.Errorf("ATH-BOUND-LISTED-MINERS: of %d miners mining %d are kept, %d rows written; want all", MaxKeptMiners+6, len(minersKept), f.count())
	}
	if t.Failed() {
		t.FailNow()
	}
	writes := f.writeCount()
	for i := 0; i < farm; i++ {
		m.UpdateWorker(bestMiner, fmt.Sprintf("rig%03d", i), true, 1000, 1000)
	}
	mustWrite(t, m)
	if f.writeCount() != writes {
		t.Errorf("ATH-BOUND-LISTED-CHURN: shares below each worker's best made %d writes; one listed worker was dropped and written again",
			f.writeCount()-writes)
	}

	// A day later none of them mines any more: the bounds apply.
	for i := 0; i < farm; i++ {
		pruned(m, bestMiner, fmt.Sprintf("rig%03d", i))
	}
	for i := 0; i < MaxKeptMiners+5; i++ {
		pruned(m, fmt.Sprintf("miner%02d", i), "rig")
	}
	mustWrite(t, m)
	keptMiners := map[string]bool{}
	for k := range m.kept {
		keptMiners[k.miner] = true
	}
	if n := len(keptWorkers(m, bestMiner)); n > MaxKeptWorkers || len(keptMiners) != MaxKeptMiners || f.count() > MaxKeptWorkers+MaxKeptMiners {
		t.Errorf("ATH-BOUND-UNLISTED: a day after their last share, %d workers of the miner and %d miners are kept (%d rows); want at most %d and %d",
			n, len(keptMiners), f.count(), MaxKeptWorkers, MaxKeptMiners)
	}
	if m.KeptBest(bestMiner, fmt.Sprintf("rig%03d", farm-1)) != 1e6+farm-1 {
		t.Error("ATH-BOUND-UNLISTED-BEST: the miner's best was dropped")
	}
}

// The day is the stratum's own (WorkerRetention): of a miner's workers, one last seen 23 hours ago
// is kept however many others are, and one last seen 25 hours ago is not.
func TestKeptBestsOfTheLastDay(t *testing.T) {
	f := useFakeBestDB(t)
	now := time.Now()
	f.rows[bestKey{bestMiner, "record"}] = BestShare{Miner: bestMiner, Worker: "record", Difficulty: 9e12, Seen: longAgo()}
	for i := 0; i < MaxKeptWorkers; i++ {
		name := fmt.Sprintf("day%03d", i)
		f.rows[bestKey{bestMiner, name}] = BestShare{Miner: bestMiner, Worker: name, Difficulty: 1e6,
			Seen: now.Add(-23*time.Hour + time.Duration(i)*time.Minute)}
	}
	f.rows[bestKey{bestMiner, "older"}] = BestShare{Miner: bestMiner, Worker: "older", Difficulty: 1e6, Seen: now.Add(-25 * time.Hour)}
	m := startRun(t)
	mustWrite(t, m)
	kept := 0
	for i := 0; i < MaxKeptWorkers; i++ {
		if m.KeptBest(bestMiner, fmt.Sprintf("day%03d", i)) == 1e6 {
			kept++
		}
	}
	if kept != MaxKeptWorkers {
		t.Errorf("ATH-BOUND-DAY-KEPT: of %d workers last seen in the last 23 hours, %d are kept; want all", MaxKeptWorkers, kept)
	}
	if m.KeptBest(bestMiner, "older") != 0 {
		t.Errorf("ATH-BOUND-DAY-DROPPED: a worker last seen 25 hours ago is kept beyond the %d", MaxKeptWorkers)
	}
}

// A worker back after more than a day, with only refused shares so far, is listed again: its kept
// best stays however many workers its miner has, and its miner's however many miners there are.
func TestListedWorkerKeptWhateverItsLastShare(t *testing.T) {
	f := useFakeBestDB(t)
	base := longAgo()
	for i := 0; i < MaxKeptWorkers+20; i++ {
		name := fmt.Sprintf("rig%03d", i)
		f.rows[bestKey{bestMiner, name}] = BestShare{Miner: bestMiner, Worker: name, Difficulty: 1e6 + float64(i),
			Seen: base.Add(time.Duration(i) * time.Minute)}
	}
	for i := 0; i < MaxKeptMiners+2; i++ {
		miner := fmt.Sprintf("miner%02d", i)
		f.rows[bestKey{miner, "rig"}] = BestShare{Miner: miner, Worker: "rig", Difficulty: 5e5, Seen: base.Add(time.Duration(200+i) * time.Minute)}
	}
	m := startRun(t)
	m.RecordInvalidShare(bestMiner, "rig000") // its miner's worker seen longest ago
	m.RecordInvalidShare("miner00", "rig")    // the miner seen longest ago
	mustWrite(t, m)
	if got := m.KeptBest(bestMiner, "rig000"); got != 1e6 {
		t.Errorf("ATH-BOUND-LISTED-REFUSED: a worker listed again has kept best %v, want its 1e6", got)
	}
	if got := m.KeptBest("miner00", "rig"); got != 5e5 {
		t.Errorf("ATH-BOUND-LISTED-REFUSED-MINER: a miner listed again has kept best %v, want its 5e5", got)
	}
}

// The miners mining now count toward the miners kept: with 3 of them, the 17 others seen last keep
// their bests.
func TestListedMinersCountTowardTheBound(t *testing.T) {
	f := useFakeBestDB(t)
	base := longAgo()
	for i := 0; i < MaxKeptMiners; i++ {
		miner := fmt.Sprintf("miner%02d", i)
		f.rows[bestKey{miner, "rig"}] = BestShare{Miner: miner, Worker: "rig", Difficulty: 5e5, Seen: base.Add(time.Duration(i) * time.Minute)}
	}
	m := startRun(t)
	for i := 0; i < 3; i++ {
		m.UpdateWorker(fmt.Sprintf("live%d", i), "rig", true, 1000, 5e5)
	}
	mustWrite(t, m)
	keptMiners := map[string]bool{}
	for k := range m.kept {
		keptMiners[k.miner] = true
	}
	if len(keptMiners) != MaxKeptMiners || !keptMiners["live0"] || keptMiners["miner02"] || !keptMiners["miner03"] || f.count() != MaxKeptMiners {
		t.Errorf("ATH-BOUND-MINERS-ROOM: with 3 miners mining, %d miners are kept (%d rows): %v; want the 3 and the 17 others seen last",
			len(keptMiners), f.count(), keptMiners)
	}
}

// A run that cannot read the database keeps its bests in memory bounded too, and removes none of
// the database's: what it dropped before the read was never written.
func TestKeptBestsBoundedBeforeTheRead(t *testing.T) {
	f := useFakeBestDB(t)
	f.rows[bestKey{bestMiner, "rental000"}] = BestShare{Miner: bestMiner, Worker: "rental000", Difficulty: 9e12}
	f.readErr = errors.New("database not initialized")
	m := newStatsManager()
	if _, err := m.LoadBestShares(); err == nil {
		t.Fatal("ATH-SETUP: the read did not fail")
	}
	base := longAgo()
	for i := 0; i < MaxKeptWorkers+10; i++ {
		name := fmt.Sprintf("rental%03d", i)
		m.UpdateWorker(bestMiner, name, true, 1000, 1e6+float64(i))
		unlist(m, bestMiner, name)
		keptOf(t, m, bestMiner, name).seen = base.Add(time.Duration(i) * time.Minute)
	}
	if err := m.WriteBestShares(); err != nil {
		t.Fatal(err)
	}
	if len(m.kept) != MaxKeptWorkers {
		t.Errorf("ATH-BOUND-UNREAD: a run that could not read the database holds %d bests, want %d", len(m.kept), MaxKeptWorkers)
	}
	f.readErr = nil
	if _, err := m.LoadBestShares(); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, m)
	if r, ok := f.row(bestMiner, "rental000"); !ok || r.Difficulty != 9e12 {
		t.Errorf("ATH-UNREAD-KEEPS-DB: the database's best of a worker dropped before the read is %+v (%v), want 9e12 kept", r, ok)
	}
	if got := m.MinerBests()[bestMiner]; got != 9e12 {
		t.Errorf("ATH-UNREAD-MINER: the miner's best is %v, want the database's 9e12", got)
	}
}

// The time a worker was last seen orders the workers kept. One that mines on without a new best has
// it written once an hour, not at every write.
func TestBestSeenWrittenHourly(t *testing.T) {
	f := useFakeBestDB(t)
	m := startRun(t)
	m.UpdateWorker(bestMiner, "s19", true, 1000, 5e9)
	mustWrite(t, m)
	b := keptOf(t, m, bestMiner, "s19")
	b.seen = b.written.Add(59 * time.Minute)
	mustWrite(t, m)
	if f.writeCount() != 1 {
		t.Errorf("ATH-SEEN-EARLY: a worker seen 59 minutes after its row was written made %d writes, want none", f.writeCount()-1)
	}
	b.seen = b.written.Add(time.Hour)
	mustWrite(t, m)
	if r, _ := f.row(bestMiner, "s19"); f.writeCount() != 2 || !r.Seen.Equal(b.seen) || r.Difficulty != 5e9 {
		t.Errorf("ATH-SEEN-HOURLY: a worker seen an hour after its row was written made %d writes, row %+v; want 1, seen %v",
			f.writeCount()-1, r, b.seen)
	}
}

// Only a real share's difficulty is kept: a value that is not would stay for good, and the
// stratum's answer to the api, which is JSON, could not carry it.
func TestBestSharesKeepOnlyRealDifficulties(t *testing.T) {
	f := useFakeBestDB(t)
	for _, d := range []float64{math.Inf(1), math.NaN(), -5, 0} {
		f.rows[bestKey{bestMiner, fmt.Sprint(d)}] = BestShare{Miner: bestMiner, Worker: fmt.Sprint(d), Difficulty: d}
	}
	m := startRun(t)
	m.UpdateWorker(bestMiner, "odd", true, 1000, math.Inf(1))
	if got := m.KeptBest(bestMiner, "odd"); got != 0 {
		t.Errorf("ATH-NONFINITE-KEPT: an infinite difficulty was kept (%v)", got)
	}
	bests := m.MinerBests()
	if _, err := json.Marshal(bests); err != nil || bests[bestMiner] != 0 {
		t.Errorf("ATH-NONFINITE-ANSWER: the miners' bests are %v (%v)", bests, err)
	}
	mustWrite(t, m)
	if f.count() != 0 {
		t.Errorf("ATH-NONFINITE-DB: %d damaged rows are still in the database after a write, want none", f.count())
	}
}

// A worker dropped from the list after a day without a share keeps its best: the miner's, and its
// own when it comes back, a refused share first included.
func TestDroppedWorkerKeepsItsBest(t *testing.T) {
	useFakeBestDB(t)
	m := startRun(t)
	m.UpdateWorker(bestMiner, "s19", true, 1000, 5e9)
	m.UpdateWorker(bestMiner, "bitaxe", true, 1000, 3e6)
	w := m.workers[bestMiner+":s19"]
	w.Online, w.LastShareAt = false, time.Now().Add(-2*WorkerRetention)
	m.PruneStaleWorkers(WorkerRetention)
	if m.workers[bestMiner+":s19"] != nil {
		t.Fatal("ATH-SETUP: the worker was not dropped")
	}
	if got := m.MinerBests()[bestMiner]; got != 5e9 {
		t.Errorf("ATH-MINER-DROPPED: the miner's best after its best worker was dropped is %v, want 5e9", got)
	}
	m.RecordInvalidShare(bestMiner, "s19")
	if got := m.workers[bestMiner+":s19"].ATHDiff; got != 5e9 {
		t.Errorf("ATH-BACK-REFUSED: back with a refused share, the worker's best is %v, want 5e9", got)
	}
	delete(m.workers, bestMiner+":s19")
	m.UpdateWorker(bestMiner, "s19", true, 1000, 2000)
	if got := m.workers[bestMiner+":s19"].ATHDiff; got != 5e9 {
		t.Errorf("ATH-BACK: back with a share, the worker's best is %v, want 5e9", got)
	}

	// A block found: the round's best resets, the all-time best does not.
	m.ResetWorkerRoundStats(bestMiner)
	m.ResetAllWorkerRoundStats()
	if w := m.workers[bestMiner+":s19"]; w.BestDiff != 0 || w.ATHDiff != 5e9 || m.KeptBest(bestMiner, "s19") != 5e9 {
		t.Errorf("ATH-ROUND-RESET: after a block the worker's round best is %v and its all-time best %v (kept %v), want 0 and 5e9",
			w.BestDiff, w.ATHDiff, m.KeptBest(bestMiner, "s19"))
	}
}

// KeepBestShares reads the kept bests, again until it can, and writes what changed until it is
// stopped.
func TestKeepBestSharesReadsThenWrites(t *testing.T) {
	f := useFakeBestDB(t)
	old := bestShareWriteEvery
	bestShareWriteEvery = 10 * time.Millisecond
	t.Cleanup(func() { bestShareWriteEvery = old })
	f.rows[bestKey{bestMiner, "s19"}] = BestShare{Miner: bestMiner, Worker: "s19", Difficulty: 5e9}
	f.readErr = errors.New("database not initialized")

	m := newStatsManager()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		m.KeepBestShares(stop)
	}()
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	f.readErr = nil
	f.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for m.KeptBest(bestMiner, "s19") != 5e9 {
		if time.Now().After(deadline) {
			t.Fatal("ATH-LOOP-READ-RETRY: the kept bests were not read once the database answered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	m.UpdateWorker(bestMiner, "s21", true, 1000, 7e9)
	for {
		if r, ok := f.row(bestMiner, "s21"); ok && r.Difficulty == 7e9 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ATH-LOOP-WRITE: a new best was not written")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ATH-LOOP-STOP: KeepBestShares did not end when stopped")
	}
}

// bestLog holds what is logged while a test runs.
type bestLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *bestLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *bestLog) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The read is logged once, with how many bests it gave, none included: the first start after the
// update that adds the table reads none, and must say so.
func TestKeepBestSharesLogsTheRead(t *testing.T) {
	useFakeBestDB(t)
	old := bestShareWriteEvery
	bestShareWriteEvery = 5 * time.Millisecond
	t.Cleanup(func() { bestShareWriteEvery = old })
	logged := &bestLog{}
	prev := log.Writer()
	log.SetOutput(logged)
	t.Cleanup(func() { log.SetOutput(prev) })

	m := newStatsManager()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		m.KeepBestShares(stop)
	}()
	time.Sleep(100 * time.Millisecond) // many rounds
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ATH-SETUP: KeepBestShares did not end when stopped")
	}
	switch n := strings.Count(logged.String(), "✅ Read the workers' kept best shares: 0\n"); {
	case n == 0:
		t.Errorf("ATH-LOG-READ: reading no kept best was not logged; the log holds %q", logged.String())
	case n > 1:
		t.Errorf("ATH-LOG-READ-ONCE: the read was logged %d times, want once", n)
	}
}
