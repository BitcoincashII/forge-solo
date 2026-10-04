//go:build !windows

package main

import (
	"errors"
	"os"
	"strconv"
)

// shortName, codePageHolds and accountKey have no short names, code page or account SID outside
// Windows; the tests put in stand-ins.
var (
	shortName     = func(string) (string, error) { return "", errors.New("only on Windows") }
	codePageHolds = func(string) bool { return false }
	accountKey    = func() (string, error) { return "uid" + strconv.Itoa(os.Getuid()), nil }
)
