package main

import "golang.org/x/sys/windows"

// runningMutex is held for as long as Forge Gateway runs. A second launch finds it and only opens
// the status page. The installer and the uninstaller close this account's Forge Gateway first, then
// look for it (RunningMutex in forge-gateway.iss): still held, it is Forge Gateway running for
// another Windows account, and they change nothing. It is global, not one per sign-in session:
// Forge Gateway running for another Windows account holds the same fixed ports, and a second copy
// could only fail on them.
const runningMutex = `Global\ForgeGatewayRunning`

// running is this process's handle on runningMutex, held until it exits.
var running windows.Handle

// alreadyRunning reports whether Forge Gateway already runs on this PC. If not, this process holds
// the mutex from now until it exits. Refused access counts as running: the mutex then exists, made
// by another account's Forge Gateway or by one started as administrator.
func alreadyRunning() bool {
	name, err := windows.UTF16PtrFromString(runningMutex)
	if err != nil {
		return false
	}
	h, err := windows.CreateMutex(nil, false, name)
	switch err {
	case windows.ERROR_ALREADY_EXISTS:
		_ = windows.CloseHandle(h)
		return true
	case windows.ERROR_ACCESS_DENIED:
		return true
	}
	running = h
	return false
}

// releaseRunning lets a new Forge Gateway start before this one has exited.
func releaseRunning() {
	if running != 0 {
		_ = windows.CloseHandle(running)
		running = 0
	}
}
