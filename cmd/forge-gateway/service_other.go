//go:build !windows

package main

// On Linux and macOS the gateway runs under systemd (see README.md) or in a terminal.
const serviceSupported = false

func isWindowsService() bool              { return false }
func runService(string) error             { return nil }
func serviceCommand(string, []string) int { return 2 }
