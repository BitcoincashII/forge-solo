//go:build !windows

package main

import "syscall"

// noWindow has nothing to hide outside Windows. The launcher ships for Windows only; this lets its
// tests build and run on the CI's Linux runner.
func noWindow(uint32) *syscall.SysProcAttr { return &syscall.SysProcAttr{} }
