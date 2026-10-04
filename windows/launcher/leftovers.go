package main

import (
	"strconv"
	"sync"
	"time"
)

// A runningProgram is a process running one of this install's programs; exe is the program's file
// name in lower case ("bitcoincashiid.exe").
type runningProgram struct {
	pid int
	exe string
}

// The operating system's part of finding and ending leftover programs (stand-ins in the tests).
var (
	installedPrograms = installedProgramsOS // the processes running programs from installDir
	loopbackPorts     = loopbackPortsOS     // the ports a process listens on at 127.0.0.1
	waitPID           = waitPIDOS           // waits up to d for a process to exit; reports whether it did
	killPID           = killPIDOS
)

// stopLeftovers stops what a launcher that did not stop (ended from Task Manager, or crashed) left
// running from this install. Windows does not end a program's children with it, and the leftovers
// hold forgesolo.db and the nodes' data folders, so nothing could start again until the PC was
// restarted. A migrator cut short is ended first, as nothing it did is put in place; then each node
// is asked to stop through its RPC, on the port it is seen listening on in its window (its password
// goes to no other port), and ended only if it does not stop in time; the old database a move
// started is signalled as on Quit; the miner and the API are ended, as nothing of theirs is lost.
func stopLeftovers() {
	progs := installedPrograms()
	if len(progs) == 0 {
		return
	}
	logf("a previous run left %d programs running: stopping them", len(progs))
	for _, p := range progs {
		if p.exe == migrateExe {
			endLeftover(p.pid)
			logf("ended a leftover %s", p.exe)
		}
	}
	var wg sync.WaitGroup
	for _, p := range progs {
		for _, n := range nodes() {
			if p.exe == n.exe {
				wg.Add(1)
				go func() {
					defer wg.Done()
					stopLeftoverNode(p.pid, n)
				}()
			}
		}
		if p.exe == "stratum.exe" || p.exe == "api.exe" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				endLeftover(p.pid)
				logf("ended a leftover %s", p.exe)
			}()
		}
	}
	if pid := postmasterPID(); pid != 0 && runs(progs, pid, "postgres.exe") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stopDatabase()
		}()
	}
	wg.Wait()
}

func stopLeftoverNode(pid int, n nodeInfo) {
	port := 0
	for _, p := range loopbackPorts(pid) {
		if p >= n.from && p < n.from+portWindow {
			port = p
			break
		}
	}
	if port == 0 {
		endLeftover(pid)
		logf("a leftover %s node listens on no port in %d-%d: ended", n.key, n.from, n.from+portWindow-1)
		return
	}
	start := time.Now()
	if askToStop(strconv.Itoa(port), n.user, n.pass, n.grace, func(d time.Duration) bool { return waitPID(pid, d) }) {
		logf("a leftover %s node stopped in %v", n.key, time.Since(start).Round(time.Millisecond))
		return
	}
	endLeftover(pid)
	logf("a leftover %s node did not stop in %v: ended", n.key, n.grace)
}

// endLeftover ends a leftover at once and waits a little for it to be gone: Windows ends a process
// only once its pending I/O is done, and the program started in its place could otherwise still
// find its port or its data folder taken.
func endLeftover(pid int) {
	_ = killPID(pid)
	_ = waitPID(pid, 10*time.Second)
}

// runs reports whether pid is one of progs, running exe.
func runs(progs []runningProgram, pid int, exe string) bool {
	for _, p := range progs {
		if p.pid == pid && p.exe == exe {
			return true
		}
	}
	return false
}
