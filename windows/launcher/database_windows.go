package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var pCallNamedPipeW = kernel32.NewProc("CallNamedPipeW")

// signalPostgresOS sends PostgreSQL's postmaster a signal the way pg_ctl does on Windows (pgkill in
// src/port/kill.c): one byte down \\.\pipe\pgsignal_<pid>, and the same byte back. A pipe already
// broken means the server is exiting, which pgkill also takes as delivered.
func signalPostgresOS(pid int, sig byte) error {
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`\\.\pipe\pgsignal_%d`, pid))
	if err != nil {
		return err
	}
	in, out := sig, byte(0)
	var n uint32
	r, _, callErr := pCallNamedPipeW.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&in)), 1,
		uintptr(unsafe.Pointer(&out)), 1, uintptr(unsafe.Pointer(&n)), 1000)
	if r == 0 {
		if callErr == windows.ERROR_BROKEN_PIPE || callErr == windows.ERROR_BAD_PIPE {
			return nil
		}
		return fmt.Errorf("signal %d to the database (pid %d): %v", sig, pid, callErr)
	}
	if n != 1 || out != sig {
		return fmt.Errorf("the database answered %d to signal %d", out, sig)
	}
	return nil
}
