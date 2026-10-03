//go:build !windows

package main

import "net"

// listenExclusive is plain net.Listen outside Windows, where the system already refuses a second
// listener on a port.
func listenExclusive(network, addr string) (net.Listener, error) { return net.Listen(network, addr) }
