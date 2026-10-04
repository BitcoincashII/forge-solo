//go:build !windows

package main

import (
	"net"
	"syscall"
)

// listenExclusive is plain net.Listen outside Windows, where the system already refuses a second
// listener on a port.
func listenExclusive(network, addr string) (net.Listener, error) { return net.Listen(network, addr) }

// accessDenied is the error a program gets for a port it may not have.
var accessDenied error = syscall.EACCES

// Outside Windows, where the launcher ships, there are no listener tables to read; the tests use
// stand-ins.
func tcpListenersOS() []tcpListener { return nil }
func processNameOS(int) string      { return "" }
