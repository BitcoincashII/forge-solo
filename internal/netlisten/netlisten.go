// Package netlisten opens the TCP listeners of Forge Solo's services so that, on Windows, no other
// program can bind the same port beside them.
package netlisten

import (
	"context"
	"net"
)

// Listen is net.Listen, with Windows's exclusive address use there. Without it Windows let another
// program bind the same port after Forge Solo had -- on IPv4 alone, beside the one socket Go makes
// for IPv6 and IPv4 -- and gave that program every IPv4 connection from then on: tested on a
// Windows 10 PC, a miner port taken that way would have taken every miner. Elsewhere it is plain
// net.Listen.
func Listen(network, addr string) (net.Listener, error) {
	lc := listenConfig()
	return lc.Listen(context.Background(), network, addr)
}
