package stratum

import (
	"bufio"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
)

// A quarter of the slots are kept for this network's own miners: connections from the internet
// can take only the rest.
func TestConnectionLimitKeepsRoomForLocalMiners(t *testing.T) {
	s := &Server{config: &ServerConfig{MaxConnections: 256}}
	for addr, want := range map[string]int64{
		"8.8.8.8":     192, // the internet
		"2001:db8::1": 192,
		"192.168.1.5": 256, // this network
		"10.21.0.1":   256, // the Docker network the Umbrel's port forwards arrive through
		"127.0.0.1":   256,
		"::1":         256,
		"fd00::1":     256,
		"169.254.7.7": 256,
		"100.64.1.1":  256, // behind a carrier-grade NAT
	} {
		if got := s.connectionLimitFor(&net.TCPAddr{IP: net.ParseIP(addr), Port: 4000}); got != want {
			t.Errorf("CONN-LOCAL-RESERVE: a connection from %s may join while fewer than %d are open, want %d", addr, got, want)
		}
	}
}

// A connection that has not authorized by authTimeout is closed; one that has may stay silent far
// longer, as rigs between shares and marketplaces' spare connections do.
func TestUnauthorizedConnectionsCloseAndAuthorizedOnesStay(t *testing.T) {
	old := authTimeout
	authTimeout = 300 * time.Millisecond
	t.Cleanup(func() { authTimeout = old })
	s := NewServer(&ServerConfig{Host: "127.0.0.1", Port: 0, MaxConnections: 10, ExtraNonce1Size: 4, ExtraNonce2Size: 8, SoloOnly: true}, zap.NewNop(), nil)
	s.SetSoloPayoutAddress(testPayout)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)

	idle := dialFirstByte(t, s.ListenAddr())
	r := bufio.NewReader(idle)
	idle.Write([]byte(`{"id":1,"method":"mining.subscribe","params":[]}` + "\n"))
	readLine(t, r, idle, "CONN-SUBSCRIBE")
	if !closedWithin(idle, 2*time.Second) {
		t.Fatal("CONN-AUTH-DEADLINE: a connection that never authorized was still open after the deadline")
	}

	miner := dialFirstByte(t, s.ListenAddr())
	r = bufio.NewReader(miner)
	miner.Write([]byte(`{"id":1,"method":"mining.subscribe","params":[]}` + "\n"))
	readLine(t, r, miner, "CONN-SUBSCRIBE")
	miner.Write([]byte(`{"id":2,"method":"mining.authorize","params":["rig1","x"]}` + "\n"))
	time.Sleep(time.Second) // past authTimeout, silent
	if closedWithin(miner, 500*time.Millisecond) {
		t.Fatal("CONN-AUTHORIZED-STAYS: an authorized miner was disconnected for staying silent past the login deadline")
	}
}
