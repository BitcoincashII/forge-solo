//go:build !windows

package main

import "errors"

// shortName and codePageHolds have no short names or code page outside Windows; the tests put in
// stand-ins.
var (
	shortName     = func(string) (string, error) { return "", errors.New("only on Windows") }
	codePageHolds = func(string) bool { return false }
)
