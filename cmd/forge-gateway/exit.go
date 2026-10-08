package main

import "errors"

// The exit codes besides 0 (asked to stop), 1 (anything else) and 2 (a wrong flag): exitConfig
// when the config file or SETTINGS_PASSWORD is wrong, exitPort when a listen address (the miners'
// or the status page's) cannot be had. The Windows tray app tells from them what to show.
const exitConfig, exitPort = 3, 4

// exitError ends the program with its own exit code.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// exitCode is the code the program ends with for err.
func exitCode(err error) int {
	var e *exitError
	if errors.As(err, &e) && (e.code == exitConfig || e.code == exitPort) {
		return e.code
	}
	return 1
}
