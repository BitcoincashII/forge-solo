package main

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// supervisedProgram is how to start the program under key again when it exits on its own, and what
// to call it; nil for one the launcher does not start again.
func supervisedProgram(key string) (func() error, string) {
	extraMu.Lock()
	p, ok := extraSupervised[key]
	extraMu.Unlock()
	if ok {
		return p.start, p.what
	}
	switch key {
	case "stratum":
		return startStratum, "the miner"
	case "api":
		return startAPI, "the dashboard's API"
	case "bch2":
		return startBCH2, "the BCH2 node"
	case "aux1175":
		return startAux, "the 1175 node"
	}
	return nil, ""
}

// supervising is set while programs that exit on their own are started again: always, but in the
// tests, which turn it on only where they test it.
var supervising atomic.Bool

func init() { supervising.Store(true) }

// extraSupervised are the tests' own programs to start again.
var (
	extraMu         sync.Mutex
	extraSupervised = map[string]struct {
		start func() error
		what  string
	}{}
)

// The wait before a program that exited on its own is started again: restartFirstWait, doubled
// each time it exits within restartQuick of its start, up to restartMaxWait; back to the first once
// it has run longer (shorter in the tests).
var restartFirstWait, restartMaxWait, restartQuick = 2 * time.Second, time.Minute, time.Minute

var (
	restartMu        sync.Mutex // guards the waits above and below
	restartWaits     = map[string]time.Duration{}
	restartsUnderWay sync.WaitGroup // the restarts under way (the tests wait for them)
)

// exitedOnItsOwn follows the exit of c, started under key at since. One the launcher did not ask
// for -- a crash, or a program quitting on an error -- is logged, shown in the tray, and the program
// is started again after a wait. There is no service manager on Windows to do it, and a miner that
// crashed left the tray saying "running" with nothing mining until someone noticed.
func exitedOnItsOwn(key string, c *exec.Cmd, st *os.ProcessState, since time.Time) {
	if !supervising.Load() {
		return
	}
	mu.Lock()
	onItsOwn := !stopping && procs[key] == c
	if onItsOwn {
		delete(procs, key)
		delete(exited, key)
		if w := stdins[key]; w != nil {
			_ = w.Close()
			delete(stdins, key)
		}
	}
	mu.Unlock()
	start, what := supervisedProgram(key)
	if !onItsOwn || start == nil {
		return
	}
	restartsUnderWay.Add(1)
	defer restartsUnderWay.Done()
	ran := time.Since(since)
	restartMu.Lock()
	wait, quick := restartWaits[key], restartQuick
	if wait == 0 || ran >= restartQuick {
		wait = restartFirstWait
	} else {
		wait = min(2*wait, restartMaxWait)
	}
	restartWaits[key] = wait
	restartMu.Unlock()
	code := -1
	if st != nil {
		code = st.ExitCode()
	}
	logf("%s (%s) exited on its own after %v, exit code %d: starting it again in %v", what, key, ran.Round(time.Second), code, wait)
	status(tipRestarting(what))
	if damagedChain(key) {
		start = repairOnce(key, what, start)
	}
	time.Sleep(wait)
	if isStopping() {
		return
	}
	if err := start(); err != nil {
		if !errors.Is(err, errStopping) && !errors.Is(err, errStarted) {
			next := nextWait(wait)
			couldNotStart(key, what, err, next)
			keepStarting(key, what, start, next)
		}
		return
	}
	logf("%s started again", what)
	time.AfterFunc(quick, showRunning)
}

// trouble is what the tray says about a program that is not running as it should (one that could
// not start), by its key. The tray does not say "running" while there is any.
var (
	troubleMu sync.Mutex
	trouble   = map[string]string{}
	retrying  = map[string]bool{} // the programs tried again in the background
)

// runningKeys are the programs that run once Forge Solo has started, in the order the tray names
// one in trouble; a move of the old data that failed ("database") comes first.
var runningKeys = []string{"bch2", "aux1175", "api", "stratum"}

// runningNotes are said after "running" in the tray, by what each is about: what the dashboard also
// says about an earlier version's data ("database") and about the rental port ("rentals"), while
// Forge Solo runs as it should. The tray has room for one, the first in noteOrder.
var (
	runningNotes = map[string]string{}
	noteOrder    = []string{"database", "rentals"}
)

// setRunningNote sets the note about key; "" takes it away.
func setRunningNote(key, s string) {
	troubleMu.Lock()
	runningNotes[key] = s
	troubleMu.Unlock()
}

// runningNote is the note the tray shows after "running", or "".
func runningNote() string {
	troubleMu.Lock()
	defer troubleMu.Unlock()
	for _, k := range noteOrder {
		if n := runningNotes[k]; n != "" {
			return n
		}
	}
	return ""
}

// showTrouble shows the first trouble in the tray, if there is one, and reports whether there was.
func showTrouble() bool {
	troubleMu.Lock()
	tip := ""
	for _, k := range append([]string{"database"}, runningKeys...) {
		if tip = trouble[k]; tip != "" {
			break
		}
	}
	troubleMu.Unlock()
	if tip != "" {
		status(tip)
	}
	return tip != ""
}

// showRunning says "running" in the tray once the dashboard is open and both nodes, the API and the
// miner run; while one could not start, the tray says that instead. While no payout address is set,
// the miner mines nothing, and the tray asks for one. A program still being started again leaves
// the tray as it is.
func showRunning() {
	if showTrouble() || !dashboardOpen.Load() {
		return
	}
	if noPayoutAddress.Load() {
		status(tipSetAddress)
		return
	}
	for _, k := range runningKeys {
		if !started(k) {
			return
		}
	}
	status(tipRunningWith(runningNote()))
}

// startOrKeepTrying starts the program under key and reports whether it runs. One that cannot start
// (an antivirus holding or removing its program, say) is logged and named in the tray, and tried
// again in the background until it starts or the stop begins: nothing else on Windows would.
func startOrKeepTrying(key string) bool {
	start, what := supervisedProgram(key)
	err := start()
	if err == nil || errors.Is(err, errStarted) {
		return true
	}
	if errors.Is(err, errStopping) {
		return false
	}
	wait := nextWait(0)
	couldNotStart(key, what, err, wait)
	if supervising.Load() {
		restartsUnderWay.Add(1)
		go func() {
			defer restartsUnderWay.Done()
			keepStarting(key, what, start, wait)
		}()
	}
	return false
}

// couldNotStart logs that the program under key could not start, and when it is tried again, and
// names it in the tray with the reason.
func couldNotStart(key, what string, err error, wait time.Duration) {
	logf("%s could not start: %v; trying again in %v", what, err, wait)
	setTrouble(key, tipProgramCannotStart(what, startError(err)))
}

// setTrouble notes what is wrong with the program under key, and shows it in the tray.
func setTrouble(key, tip string) {
	troubleMu.Lock()
	trouble[key] = tip
	troubleMu.Unlock()
	status(tip)
}

// clearTrouble forgets what was wrong with the program under key.
func clearTrouble(key string) {
	troubleMu.Lock()
	delete(trouble, key)
	troubleMu.Unlock()
}

// keepStarting starts the program under key after wait, again after twice as long each time it
// cannot, up to restartMaxWait, until it starts or the stop begins. One loop per program: another
// start meanwhile (Restart Mining) ends it.
func keepStarting(key, what string, start func() error, wait time.Duration) {
	troubleMu.Lock()
	if retrying[key] {
		troubleMu.Unlock()
		return
	}
	retrying[key] = true
	troubleMu.Unlock()
	defer func() {
		troubleMu.Lock()
		delete(retrying, key)
		troubleMu.Unlock()
	}()
	for {
		if pause(wait) {
			return
		}
		err := start()
		switch {
		case err == nil || errors.Is(err, errStarted):
			if err == nil {
				logf("%s started", what)
			}
			showRunning()
			return
		case errors.Is(err, errStopping):
			return
		}
		wait = nextWait(wait)
		couldNotStart(key, what, err, wait)
	}
}

// pause waits d, or less once the stop has begun, and reports whether it has.
func pause(d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end); {
		if isStopping() {
			return true
		}
		time.Sleep(min(50*time.Millisecond, time.Until(end)))
	}
	return isStopping()
}

// nextWait is the wait after wait: restartFirstWait at first, then twice the one before, up to
// restartMaxWait.
func nextWait(wait time.Duration) time.Duration {
	restartMu.Lock()
	defer restartMu.Unlock()
	if wait == 0 {
		return restartFirstWait
	}
	return min(2*wait, restartMaxWait)
}

// startError is why a program could not start, without its path, which the tray has no room for.
func startError(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	var ee *exec.Error
	if errors.As(err, &ee) {
		return ee.Err.Error()
	}
	return err.Error()
}

// The node's own words for chain data it cannot use: after a power cut, a disk that filled up, or a
// crash mid-write. It then stops at every start, until it is started once with -reindex.
var damagedChainSays = []string{"Corrupted block database detected", "restart with -reindex"}

var (
	logEndMu sync.Mutex
	logEnds  = map[string]struct {
		path string
		end  int64
	}{} // where each node's debug.log ended when it was last started
	repaired = map[string]bool{} // the nodes already started with -reindex in this run
)

// noteLogEnd notes where the debug.log of the node under key ends, before it starts.
func noteLogEnd(key, path string) {
	var end int64
	if st, err := os.Stat(path); err == nil {
		end = st.Size()
	}
	logEndMu.Lock()
	logEnds[key] = struct {
		path string
		end  int64
	}{path, end}
	logEndMu.Unlock()
}

// damagedChain reports whether the node under key wrote, since its last start, that its chain data
// is damaged.
func damagedChain(key string) bool {
	logEndMu.Lock()
	l, ok := logEnds[key]
	logEndMu.Unlock()
	if !ok {
		return false
	}
	f, err := os.Open(l.path)
	if err != nil {
		return false
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.Size() < l.end {
		l.end = 0 // the node trimmed its log at the start: all of it is this run's
	}
	b, err := io.ReadAll(io.NewSectionReader(f, l.end, 1<<20))
	if err != nil {
		return false
	}
	for _, s := range damagedChainSays {
		if strings.Contains(string(b), s) {
			return true
		}
	}
	return false
}

// repairOnce is the start for a node whose chain data is damaged: once in a run, with -reindex,
// which rebuilds its chain state from the blocks on disk (minutes for this chain); after that, as
// usual, with the tray saying what is left to do.
func repairOnce(key, what string, start func() error) func() error {
	logEndMu.Lock()
	again := repaired[key]
	repaired[key] = true
	logEndMu.Unlock()
	folder := "bch2"
	if key == "aux1175" {
		folder = "elevenseventyfive"
	}
	if again {
		logf("%s still finds its chain data damaged after rebuilding it: delete the blocks and chainstate folders in %s in the data folder, then start Forge Solo again", what, folder)
		status(tipChainDamaged(what))
		return start
	}
	logf("%s says its chain data is damaged: starting it once with -reindex, which rebuilds it from the blocks on disk (this takes a few minutes)", what)
	status(tipRebuilding(what))
	return func() error { return startNode(key, "-reindex") }
}
