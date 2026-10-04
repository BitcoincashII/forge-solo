//go:build !windows

package main

import "errors"

// makeJunction has no junctions to make outside Windows, where the launcher ships; the tests put in
// a symbolic link, which os.Remove likewise removes alone.
var makeJunction = func(link, target string) error { return errors.New("only on Windows") }
