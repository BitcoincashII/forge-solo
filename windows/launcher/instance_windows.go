package main

import "golang.org/x/sys/windows"

// runningMutex is held for as long as Forge Solo runs. A second launch finds it and only opens the
// dashboard; the installer finds it too (AppMutex in forge-solo.iss) and asks for Forge Solo to be
// closed, so that it stops cleanly, before the files are replaced.
const runningMutex = "ForgeSoloRunning"

// alreadyRunning reports whether Forge Solo already runs in this session. If not, this process
// holds the mutex from now until it exits.
func alreadyRunning() bool {
	name, err := windows.UTF16PtrFromString(runningMutex)
	if err != nil {
		return false
	}
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		_ = windows.CloseHandle(h)
		return true
	}
	return false
}
