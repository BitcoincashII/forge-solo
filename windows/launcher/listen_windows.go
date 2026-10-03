package main

import (
	"context"
	"net"
	"syscall"
)

// listenExclusive is net.Listen with Windows's exclusive address use, so that no other program can
// bind the same port beside it and take its connections (internal/netlisten does the same for the
// services).
func listenExclusive(network, addr string) (net.Listener, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, ^syscall.SO_REUSEADDR, 1)
		}); err != nil {
			return err
		}
		return serr
	}}
	return lc.Listen(context.Background(), network, addr)
}
