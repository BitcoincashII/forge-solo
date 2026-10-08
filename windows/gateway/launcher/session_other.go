//go:build !windows

package main

// watchSessionEnd has no session to watch outside Windows, where the launcher ships. This lets its
// tests build and run on the CI's Linux runner.
func watchSessionEnd() {}
