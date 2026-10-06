package stats

import (
	"log"
	"math"
	"sort"
	"time"
)

// Each worker's all-time best share is kept in the database, so that a restart or an update no
// longer resets it. The stratum reads the kept ones once the database is there, and writes what
// changed every bestShareWriteEvery: a share itself writes nothing.
//
// The kept ones are bounded. A worker that mines now or did in the last day is always kept, and so
// is its miner: the stratum lists such a worker (m.workers drops one a day after its last share),
// or did before it restarted. Of the others, a miner keeps its best one and the ones seen last,
// MaxKeptWorkers in all, and the miners seen last keep theirs, MaxKeptMiners in all: a rented rig
// that comes back under a new name every time adds a name each time, and would otherwise add a row
// for good.

const (
	// MaxKeptWorkers is how many workers' bests a miner keeps: its best one and the ones seen last.
	MaxKeptWorkers = 100
	// MaxKeptMiners is how many miners' bests are kept: the ones seen last.
	MaxKeptMiners = 20
	// bestSeenEvery is how often a worker that mines on without a new best has the time it was
	// last seen written: that time decides which workers are kept.
	bestSeenEvery = time.Hour
)

// bestShareWriteEvery is how often what changed is written. A variable so that a test need not wait.
var bestShareWriteEvery = 5 * time.Second

// The database's side, replaced in tests.
var (
	readBestShares  = LoadBestSharesDB
	writeBestShares = SaveBestSharesDB
)

// BestShare is a worker's all-time best share, as the database keeps it.
type BestShare struct {
	Miner      string
	Worker     string
	Difficulty float64
	Seen       time.Time // the worker's last share, when the row was written
}

// bestKey names a worker's kept best.
type bestKey struct{ miner, worker string }

// keptBest is a worker's all-time best share.
type keptBest struct {
	diff    float64
	seen    time.Time // the worker's last share
	written time.Time // seen, as last written or read; zero when it never was
	dirty   bool      // diff rose since it was last written
}

// keepable reports whether d can be kept as a best: a share's difficulty is positive and finite. A
// value that is not would be kept for good, and the stratum's answer to the api cannot carry it.
func keepable(d float64) bool { return d > 0 && !math.IsInf(d, 0) }

// keepBest raises a worker's kept best to diff, and notes that the worker was seen at at. The
// caller holds m.mu.
func (m *StatsManager) keepBest(minerID, workerName string, diff float64, at time.Time) {
	if !keepable(diff) {
		return
	}
	if m.kept == nil {
		m.kept = make(map[bestKey]*keptBest)
	}
	k := bestKey{minerID, workerName}
	b := m.kept[k]
	if b == nil {
		b = &keptBest{}
		m.kept[k] = b
	}
	if diff > b.diff {
		b.diff, b.dirty = diff, true
	}
	if at.After(b.seen) {
		b.seen = at
	}
}

// KeptBest is a worker's all-time best share as kept, or 0.
func (m *StatsManager) KeptBest(minerID, workerName string) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if b := m.kept[bestKey{minerID, workerName}]; b != nil {
		return b.diff
	}
	return 0
}

// MinerBests is each miner's all-time best share: the best of its workers', those no longer listed
// included.
func (m *StatsManager) MinerBests() map[string]float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]float64)
	for k, b := range m.kept {
		if b.diff > out[k.miner] {
			out[k.miner] = b.diff
		}
	}
	for _, w := range m.workers {
		if keepable(w.ATHDiff) && w.ATHDiff > out[w.MinerID] {
			out[w.MinerID] = w.ATHDiff
		}
	}
	return out
}

// LoadBestShares reads the bests the database keeps, once. Until it has, nothing is written: a run
// that has not read them could otherwise drop a worker's best for one it counted itself. A best
// counted in this run before the read stays when it is higher. It returns how many were read.
func (m *StatsManager) LoadBestShares() (int, error) {
	m.mu.RLock()
	loaded := m.keptLoaded
	m.mu.RUnlock()
	if loaded {
		return 0, nil
	}
	rows, err := readBestShares()
	if err != nil {
		return 0, err
	}
	m.loadBests(rows)
	return len(rows), nil
}

func (m *StatsManager) loadBests(rows []BestShare) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.kept == nil {
		m.kept = make(map[bestKey]*keptBest)
	}
	for _, r := range rows {
		k := bestKey{r.Miner, r.Worker}
		if !keepable(r.Difficulty) {
			m.drop(k) // a damaged row: removed at the next write
			continue
		}
		b := m.kept[k]
		if b == nil {
			b = &keptBest{}
			m.kept[k] = b
		}
		b.diff = max(b.diff, r.Difficulty)
		if r.Seen.After(b.seen) {
			b.seen = r.Seen
		}
		if r.Seen.After(b.written) {
			b.written = r.Seen
		}
		if w := m.workers[r.Miner+":"+r.Worker]; w != nil && b.diff > w.ATHDiff {
			w.ATHDiff = b.diff
		}
	}
	m.keptLoaded = true
}

// drop forgets a kept best; the next write removes it from the database. The caller holds m.mu.
func (m *StatsManager) drop(k bestKey) {
	delete(m.kept, k)
	if m.gone == nil {
		m.gone = make(map[bestKey]bool)
	}
	m.gone[k] = true
}

// listed reports whether the stratum lists the worker now. The caller holds m.mu.
func (m *StatsManager) listed(k bestKey) bool { return m.workers[k.miner+":"+k.worker] != nil }

// mining reports whether a kept best's worker mines now or did after since: the stratum lists it,
// or its last share, in this run or before a restart, came after since. The caller holds m.mu.
func (m *StatsManager) mining(k bestKey, since time.Time) bool {
	b := m.kept[k]
	return m.listed(k) || (b != nil && b.seen.After(since))
}

// trimKept bounds the kept bests (see the top of this file). The caller holds m.mu.
func (m *StatsManager) trimKept() {
	since := time.Now().Add(-WorkerRetention)
	byMiner := make(map[string][]bestKey)
	for k := range m.kept {
		byMiner[k.miner] = append(byMiner[k.miner], k)
	}
	listedMiners := make(map[string]bool)
	for _, w := range m.workers {
		listedMiners[w.MinerID] = true
	}
	type minerSeen struct {
		miner string
		seen  time.Time
	}
	var unlisted []minerSeen
	for miner, keys := range byMiner {
		if len(keys) > MaxKeptWorkers {
			m.trimWorkers(keys, since)
		}
		ms := minerSeen{miner: miner}
		for _, k := range keys {
			if b := m.kept[k]; b != nil && b.seen.After(ms.seen) {
				ms.seen = b.seen
			}
		}
		// A miner with a worker that mines now or did in the last day is kept.
		if listedMiners[miner] || ms.seen.After(since) {
			continue
		}
		unlisted = append(unlisted, ms)
	}
	room := max(0, MaxKeptMiners-(len(byMiner)-len(unlisted)))
	if len(unlisted) <= room {
		return
	}
	sort.Slice(unlisted, func(i, j int) bool {
		if !unlisted[i].seen.Equal(unlisted[j].seen) {
			return unlisted[i].seen.After(unlisted[j].seen)
		}
		return unlisted[i].miner < unlisted[j].miner
	})
	for _, ms := range unlisted[room:] {
		for _, k := range byMiner[ms.miner] {
			if m.kept[k] != nil {
				m.drop(k)
			}
		}
	}
}

// trimWorkers keeps, of one miner's kept bests (keys), its best one, the workers that mine now or
// did since, and the ones seen last, MaxKeptWorkers in all unless more mine. The caller holds m.mu.
func (m *StatsManager) trimWorkers(keys []bestKey, since time.Time) {
	sort.Slice(keys, func(i, j int) bool { return m.seenLater(keys[i], keys[j]) })
	best := keys[0]
	for _, k := range keys[1:] {
		if m.kept[k].diff > m.kept[best].diff {
			best = k
		}
	}
	kept := 1 // the best
	for _, k := range keys {
		switch {
		case k == best:
		case kept < MaxKeptWorkers || m.mining(k, since):
			kept++
		default:
			m.drop(k)
		}
	}
}

// seenLater orders kept bests by the time their worker was last seen, the latest first, then by name.
func (m *StatsManager) seenLater(a, b bestKey) bool {
	sa, sb := m.kept[a].seen, m.kept[b].seen
	if !sa.Equal(sb) {
		return sa.After(sb)
	}
	return a.worker < b.worker
}

// WriteBestShares writes what changed since the last write: the bests that rose, the workers seen
// again an hour or more after their row was written, and the removal of those trimKept dropped.
// What the database does not take is written at the next try. Nothing is written before
// LoadBestShares has read the database's; the bests in memory are bounded all the same.
func (m *StatsManager) WriteBestShares() error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()

	m.mu.Lock()
	m.trimKept()
	if !m.keptLoaded {
		// Nothing of this run is in the database yet, and what is there stays.
		m.gone = nil
		m.mu.Unlock()
		return nil
	}
	var raised, gone []BestShare
	for k, b := range m.kept {
		if b.dirty || b.seen.Sub(b.written) >= bestSeenEvery {
			raised = append(raised, BestShare{Miner: k.miner, Worker: k.worker, Difficulty: b.diff, Seen: b.seen})
			b.dirty = false
		}
	}
	for k := range m.gone {
		gone = append(gone, BestShare{Miner: k.miner, Worker: k.worker})
	}
	m.gone = nil
	m.mu.Unlock()
	if len(raised) == 0 && len(gone) == 0 {
		return nil
	}

	// Without m.mu: a share never waits for the database.
	err := writeBestShares(raised, gone)

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range raised {
		b := m.kept[bestKey{r.Miner, r.Worker}]
		switch {
		case b == nil:
		case err != nil:
			b.dirty = true
		case r.Seen.After(b.written):
			b.written = r.Seen
		}
	}
	if err != nil {
		if m.gone == nil {
			m.gone = make(map[bestKey]bool)
		}
		for _, g := range gone {
			m.gone[bestKey{g.Miner, g.Worker}] = true
		}
	}
	return err
}

// KeepBestShares reads the kept bests, and writes what changed every bestShareWriteEvery, until stop
// is closed. Until the read works it is tried again at every write: the database can come up after
// the stratum. The read is logged with how many it gave, none included. A failure is logged once,
// until it works again.
func (m *StatsManager) KeepBestShares(stop <-chan struct{}) {
	t := time.NewTicker(bestShareWriteEvery)
	defer t.Stop()
	failing, read := false, false
	for {
		var err error
		if !read {
			var n int
			if n, err = m.LoadBestShares(); err == nil {
				read = true
				log.Printf("✅ Read the workers' kept best shares: %d", n)
			}
		}
		if werr := m.WriteBestShares(); err == nil {
			err = werr
		}
		switch {
		case err != nil && !failing:
			log.Printf("Warning: the workers' best shares could not be kept, trying again: %v", err)
		case err == nil && failing:
			log.Printf("The workers' best shares are kept again")
		}
		failing = err != nil
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}
