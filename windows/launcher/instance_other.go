//go:build !windows

package main

// alreadyRunning has no other instance to find outside Windows, where the launcher ships. This lets
// its tests build and run on the CI's Linux runner.
func alreadyRunning() bool { return false }
