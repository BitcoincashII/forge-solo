package main

import (
	"net"
	"testing"
)

// The status page's port is the gateway's alone: with plain net.Listen, Windows let another
// program bind the same port beside it, and that program could answer the page and ask for the
// settings password.
func TestStatusPortIsExclusive(t *testing.T) {
	l, err := listenStatus("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	for _, try := range []struct{ network, addr string }{{"tcp4", "0.0.0.0:" + port}, {"tcp", "127.0.0.1:" + port}} {
		if other, err := net.Listen(try.network, try.addr); err == nil {
			other.Close()
			t.Errorf("GW-STATUS-EXCLUSIVE: another listener bound %s %s beside the status page", try.network, try.addr)
		}
	}
}
