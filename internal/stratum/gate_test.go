package stratum

import (
	"bufio"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// A closed gate turns every new connection away at once; an open one lets it subscribe; and
// DisconnectAll drops the connections already in.
func TestAcceptGateAndDisconnectAll(t *testing.T) {
	s := NewServer(&ServerConfig{Host: "127.0.0.1", Port: 0, MaxConnections: 10, ExtraNonce1Size: 4, ExtraNonce2Size: 8},
		zap.NewNop(), nil, nil)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	addr := s.ListenAddr()
	var open atomic.Bool
	s.SetAcceptGate(open.Load)

	// Reads past whole lines -- this stratum announces a difficulty right after subscribe, and
	// that line may still be in flight -- until the connection ends (closed) or the deadline
	// passes (still open).
	closedSoon := func(c net.Conn) bool {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		r := bufio.NewReader(c)
		for {
			if _, err := r.ReadString('\n'); err != nil {
				return !strings.Contains(err.Error(), "timeout")
			}
		}
	}
	c1, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if !closedSoon(c1) {
		t.Fatal("GATE-CLOSED: a connection stayed open while the gate was closed")
	}
	c1.Close()

	open.Store(true)
	c2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	c2.Write([]byte(`{"id":1,"method":"mining.subscribe","params":["probe/1.0"]}` + "\n"))
	c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	if line, err := bufio.NewReader(c2).ReadString('\n'); err != nil || !strings.Contains(line, `"result"`) {
		t.Fatalf("GATE-OPEN: subscribe through an open gate: %q %v", line, err)
	}
	if n := s.DisconnectAll("test"); n != 1 {
		t.Fatalf("DISCONNECT-ALL: closed %d clients, want 1", n)
	}
	if !closedSoon(c2) {
		t.Fatal("DISCONNECT-ALL: the client connection is still open")
	}
}
