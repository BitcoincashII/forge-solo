package main

import (
	"time"
)

// A runningProgram is a process running one of this install's programs; exe is the program's file
// name in lower case ("forge-gateway.exe").
type runningProgram struct {
	pid int
	exe string
}

// The operating system's part of finding and ending leftover programs (stand-ins in the tests).
var (
	installedPrograms = installedProgramsOS // the processes, other than this one, running programs from installDir
	waitPID           = waitPIDOS           // waits up to d for a process to exit; reports whether it did
	killPID           = killPIDOS
)

// stopLeftovers stops a forge-gateway.exe that a tray which did not stop left running from this
// install. A tray ended from Task Manager closes the gateway's stdin as it goes, so the gateway
// stops by itself; one that crashed mid-start may still leave it, holding the ports, so that
// nothing could start again. It is given the time a stop has, then ended.
func stopLeftovers() {
	for _, p := range installedPrograms() {
		if p.exe != gatewayExe {
			continue
		}
		logf("a previous run left %s running (process %d): waiting for it to stop", gatewayExe, p.pid)
		start := time.Now()
		if waitPID(p.pid, gatewayStopGrace) {
			logf("the %s a previous run left stopped in %v", gatewayExe, time.Since(start).Round(time.Millisecond))
			continue
		}
		// Windows ends a process only once its pending I/O is done, and the gateway started in its
		// place could otherwise still find its ports taken.
		_ = killPID(p.pid)
		_ = waitPID(p.pid, 10*time.Second)
		logf("the %s a previous run left did not stop in %v: ended", gatewayExe, gatewayStopGrace)
	}
}
