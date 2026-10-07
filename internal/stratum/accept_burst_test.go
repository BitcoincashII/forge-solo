package stratum

import (
	"bufio"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// remoteListener reports the connections it accepts as coming from 8.8.8.8 while internet is set,
// so a test on loopback can play a client on the internet.
type remoteListener struct {
	net.Listener
	internet atomic.Bool
	accepted atomic.Int64
}

type remoteConn struct {
	net.Conn
	remote net.Addr
}

func (c remoteConn) RemoteAddr() net.Addr { return c.remote }

func (l *remoteListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.accepted.Add(1)
	if l.internet.Load() {
		port := c.RemoteAddr().(*net.TCPAddr).Port
		return remoteConn{Conn: c, remote: &net.TCPAddr{IP: net.ParseIP("8.8.8.8"), Port: port}}, nil
	}
	return c, nil
}

// serveOn starts s on a loopback listener wrapped in a remoteListener.
func serveOn(t *testing.T, s *Server) *remoteListener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rl := &remoteListener{Listener: ln}
	s.listener = rl
	go s.acceptLoop()
	t.Cleanup(s.Stop)
	return rl
}

// burst opens n connections at once and keeps them open until the test ends. It returns once the
// server has accepted every one and its handlers have had time to run.
func burst(t *testing.T, rl *remoteListener, n int) {
	t.Helper()
	before := rl.accepted.Load()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.Dial("tcp", rl.Addr().String())
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}()
	}
	wg.Wait()
	t.Cleanup(func() {
		for _, c := range conns {
			c.Close()
		}
	})
	for end := time.Now().Add(5 * time.Second); rl.accepted.Load() < before+int64(len(conns)) && time.Now().Before(end); {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
}

// held is how many connections s is serving, and checks that its count of them agrees.
func held(t *testing.T, s *Server) int64 {
	t.Helper()
	var n int64
	s.clients.Range(func(_, _ interface{}) bool { n++; return true })
	if c := s.clientCount.Load(); c != n {
		t.Fatalf("CONN-BURST-COUNT: %d connections counted, %d held", c, n)
	}
	return n
}

// The connection limits hold under a burst. The count was taken only once each connection's
// handler ran, so on a 1- or 2-CPU host a burst got far past both: a cap of 64 admitted all 64
// internet connections and kept the owner's own miners out.
func TestConnectionBurstStaysWithinTheLimits(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))

	t.Run("internet", func(t *testing.T) {
		s := NewServer(&ServerConfig{MaxConnections: 16, ExtraNonce1Size: 4, ExtraNonce2Size: 8, SoloOnly: true}, zap.NewNop(), nil)
		rl := serveOn(t, s)
		rl.internet.Store(true)
		burst(t, rl, 300)
		if n := held(t, s); n > 12 {
			t.Fatalf("CONN-BURST-INTERNET: a burst left %d internet connections open, limit 12", n)
		}
		// A miner on this network still gets one of the slots kept for it.
		rl.internet.Store(false)
		c, err := net.Dial("tcp", rl.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.Write([]byte(`{"id":1,"method":"mining.subscribe","params":[]}` + "\n"))
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := bufio.NewReader(c).ReadString('\n'); err != nil {
			t.Fatalf("CONN-BURST-LAN: after an internet burst a miner on this network was refused: %v", err)
		}
	})

	t.Run("total", func(t *testing.T) {
		s := NewServer(&ServerConfig{MaxConnections: 16, ExtraNonce1Size: 4, ExtraNonce2Size: 8, SoloOnly: true}, zap.NewNop(), nil)
		rl := serveOn(t, s)
		burst(t, rl, 300)
		if n := held(t, s); n > 16 {
			t.Fatalf("CONN-BURST-TOTAL: a burst left %d connections open, limit 16", n)
		}
	})

	// A connection refused by the per-IP cap gives its slot back.
	t.Run("per IP", func(t *testing.T) {
		s := NewServer(&ServerConfig{MaxConnections: 16, MaxConnectionsPerIP: 4, ExtraNonce1Size: 4, ExtraNonce2Size: 8, SoloOnly: true}, zap.NewNop(), nil)
		rl := serveOn(t, s)
		rl.internet.Store(true)
		burst(t, rl, 100)
		if n := held(t, s); n != 4 {
			t.Fatalf("CONN-BURST-PERIP: %d connections counted after a burst from one address, want its 4", n)
		}
	})
}
