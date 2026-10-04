package main

import (
	"sync"
	"sync/atomic"
	"time"
)

// processExit is closed once process pid has exited (a stand-in outside Windows, for the tests).
var processExit = processExitOS

// dbWatchPoll is how often the database's watch looks whether the stop has begun (shorter in the
// tests).
var dbWatchPoll = time.Second

var (
	dbWatched  atomic.Bool    // set once the database is watched: one watch per run
	dbWatching sync.WaitGroup // the watch, while it runs (the tests wait for it)
)

// watchDatabase starts the database again when its server stops on its own (ended from Task
// Manager or by a cleanup tool, or a recovery that failed after a crash), as the other programs
// are started again. Nothing else on Windows would: Umbrel's database container restarts, but here
// Settings and every block record failed, with the tray saying "running", until someone restarted
// Forge Solo, and that restart lost the miner's retries of a block record it still held. A server
// that goes once the stop has begun is the stop's doing, and is left alone.
func watchDatabase() {
	if !supervising.Load() || !dbWatched.CompareAndSwap(false, true) {
		return
	}
	dbWatching.Add(1)
	go func() {
		defer dbWatching.Done()
		var wait time.Duration
		for {
			// The handle on the process is taken before it is checked to be this install's
			// database, so that its number cannot meanwhile go to another program.
			pid := postmasterPID()
			exited := processExit(pid)
			if pid == 0 || !runs(installedPrograms(), pid, "postgres.exe") {
				logf("database: postmaster.pid names no server of this install's (%d), so it is not watched", pid)
				return
			}
			since := time.Now()
			for gone := false; !gone; {
				select {
				case <-exited:
					gone = true
				case <-time.After(dbWatchPoll):
					if isStopping() {
						return
					}
				}
			}
			if isStopping() {
				return
			}
			restartMu.Lock()
			quick := restartQuick
			restartMu.Unlock()
			if time.Since(since) >= quick {
				wait = 0
			}
			wait = nextWait(wait)
			logf("the database stopped on its own: starting it again in %v", wait)
			setTrouble("database", "Forge Solo: the database stopped on its own and is started again (see launcher.log)")
			for {
				if pause(wait) {
					return
				}
				if startPostgres() {
					break
				}
				if isStopping() {
					return
				}
				wait = nextWait(wait)
				logf("the database did not start again (see pglog.txt): trying again in %v", wait)
				setTrouble("database", "Forge Solo: the database could not start again (see pglog.txt in the data folder)")
			}
			clearTrouble("database")
			logf("the database started again")
			showRunning()
		}
	}()
}
