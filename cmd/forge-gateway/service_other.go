//go:build !windows

package main

// On Linux the gateway runs under systemd (forge-gateway.service) or in a terminal, and on macOS
// under launchd (org.bitcoincashii.forge-gateway.plist, started at login) or in Terminal.
const serviceSupported = false

func isWindowsService() bool              { return false }
func runService(string) error             { return nil }
func serviceCommand(string, []string) int { return 2 }
