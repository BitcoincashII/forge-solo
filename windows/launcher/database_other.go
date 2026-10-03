//go:build !windows

package main

import "errors"

// signalPostgresOS has no PostgreSQL to signal outside Windows, where the launcher ships; the tests
// put a stand-in in signalPostgres.
func signalPostgresOS(int, byte) error { return errors.New("only on Windows") }
