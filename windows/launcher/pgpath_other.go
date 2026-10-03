//go:build !windows

package main

import "errors"

// shortPath has no short names to give outside Windows; the tests put in a stand-in.
var shortPath = func(string) (string, error) { return "", errors.New("only on Windows") }

func longPath(path string) string { return path }
