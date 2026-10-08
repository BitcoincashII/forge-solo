//go:build !windows

package main

import (
	"errors"
	"time"
)

// Outside Windows, where the tray app ships, there is nothing to find; the tests use stand-ins.
func installedProgramsOS() []runningProgram { return nil }
func waitPIDOS(int, time.Duration) bool     { return true }
func killPIDOS(int) error                   { return errors.New("only on Windows") }
