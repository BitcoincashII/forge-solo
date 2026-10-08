package main

import "syscall"

// noWindow starts a child with no console window (CREATE_NO_WINDOW), plus extraFlags (a priority
// class, say).
func noWindow(extraFlags uint32) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000 | extraFlags}
}
