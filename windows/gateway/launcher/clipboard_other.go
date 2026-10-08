//go:build !windows

package main

import "errors"

// copyText has no clipboard to use outside Windows, where the launcher ships.
func copyText(string) error { return errors.New("only on Windows") }
