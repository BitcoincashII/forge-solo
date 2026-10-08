package main

import (
	"io"
	"os"
	"syscall"
)

// stopOnEOF reads r until it ends, then asks for shutdown as SIGTERM would. It never blocks on a
// shutdown already asked for.
func stopOnEOF(r io.Reader, stop chan<- os.Signal) {
	_, _ = io.Copy(io.Discard, r)
	select {
	case stop <- syscall.SIGTERM:
	default:
	}
}
