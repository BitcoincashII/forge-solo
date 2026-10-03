package netlisten

import (
	"net"
	"testing"
)

// A port Listen holds cannot be bound by anyone else, IPv4 alone included, and is free again once
// it is closed. On Windows this is exclusive address use; elsewhere the system already refuses.
func TestNoOneBindsBeside(t *testing.T) {
	l, err := Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	for _, network := range []string{"tcp4", "tcp"} {
		if other, err := net.Listen(network, "0.0.0.0:"+port); err == nil {
			other.Close()
			t.Errorf("NETLISTEN-EXCLUSIVE: another listener bound %s 0.0.0.0:%s beside it", network, port)
		}
	}
	l.Close()
	again, err := Listen("tcp", "0.0.0.0:"+port)
	if err != nil {
		t.Fatalf("NETLISTEN-AGAIN: the port could not be listened on again once closed: %v", err)
	}
	again.Close()
}
