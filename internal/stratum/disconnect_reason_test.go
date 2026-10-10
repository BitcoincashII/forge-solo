package stratum

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// reasonServer is a solo stratum on a free port whose log a test reads.
func reasonServer(t *testing.T) (*Server, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.InfoLevel)
	s := NewServer(&ServerConfig{Host: "127.0.0.1", Port: 0, MaxConnections: 10, ExtraNonce1Size: 4, ExtraNonce2Size: 8, SoloOnly: true}, zap.New(core), nil)
	s.SetSoloPayoutAddress(testPayout)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s, logs
}

// loggedIn is a miner that has subscribed and authorized on s.
func loggedIn(t *testing.T, s *Server) net.Conn {
	t.Helper()
	c := dialFirstByte(t, s.ListenAddr())
	r := bufio.NewReader(c)
	c.Write([]byte(`{"id":1,"method":"mining.subscribe","params":[]}` + "\n"))
	readLine(t, r, c, "REASON-SUBSCRIBE")
	c.Write([]byte(`{"id":2,"method":"mining.authorize","params":["rig1","x"]}` + "\n"))
	for !strings.Contains(readLine(t, r, c, "REASON-AUTHORIZE"), `"id":2`) {
	}
	return c
}

// disconnectReason waits for the "Client disconnected" line and returns its reason.
func disconnectReason(t *testing.T, logs *observer.ObservedLogs, code string) string {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if e := logs.FilterMessage("Client disconnected").All(); len(e) > 0 {
			r, _ := e[0].ContextMap()["reason"].(string)
			return r
		}
	}
	t.Fatalf("%s: no disconnection was logged", code)
	return ""
}

// A disconnection says why it happened, so a marketplace's "sick pool" can be told from a miner
// leaving on its own.
func TestADisconnectionSaysWhy(t *testing.T) {
	t.Run("miner", func(t *testing.T) {
		s, logs := reasonServer(t)
		loggedIn(t, s).Close()
		if r := disconnectReason(t, logs, "REASON-MINER"); r != "the miner closed the connection" {
			t.Fatalf("REASON-MINER: %q", r)
		}
	})
	t.Run("silent", func(t *testing.T) {
		old := authorizedIdleTimeout
		authorizedIdleTimeout = 300 * time.Millisecond
		t.Cleanup(func() { authorizedIdleTimeout = old })
		s, logs := reasonServer(t)
		loggedIn(t, s)
		if r := disconnectReason(t, logs, "REASON-SILENT"); !strings.HasPrefix(r, "it was silent for") {
			t.Fatalf("REASON-SILENT: %q", r)
		}
	})
	t.Run("not stratum", func(t *testing.T) {
		s, logs := reasonServer(t)
		c := loggedIn(t, s)
		c.Write([]byte(strings.Repeat("garbage\n", maxBadLines+1)))
		if r := disconnectReason(t, logs, "REASON-NOT-STRATUM"); r != "it sent lines that are not stratum" {
			t.Fatalf("REASON-NOT-STRATUM: %q", r)
		}
	})
	t.Run("stopping", func(t *testing.T) {
		s, logs := reasonServer(t)
		loggedIn(t, s)
		s.Stop()
		if r := disconnectReason(t, logs, "REASON-STOPPING"); r != "the stratum is stopping" {
			t.Fatalf("REASON-STOPPING: %q", r)
		}
	})
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// The reasons that are hard to bring about over a real connection.
func TestDisconnectReasonsFromTheStratumsSide(t *testing.T) {
	s, _ := perJobServer()

	full := perJobClient(t)
	full.out = make(chan []byte, clientQueue)
	for i := 0; i <= clientQueue; i++ {
		s.sendNotification(full, &Notification{Method: MethodNotify, Params: []interface{}{fmt.Sprint(i)}})
	}
	if r := full.whyClosed(nil, true); !strings.HasPrefix(r, "it stopped reading") {
		t.Errorf("REASON-QUEUE: a miner whose queue filled is said to have gone because %q", r)
	}

	broken := perJobClient(t)
	broken.Conn.Close()
	broken.out = make(chan []byte, clientQueue)
	done := make(chan struct{})
	go func() { s.writeLoop(broken); close(done) }()
	broken.enqueue([]byte("{}\n"))
	broken.closeOut()
	<-done
	if r := broken.whyClosed(errors.New("use of closed network connection"), true); !strings.HasPrefix(r, "a write to it failed") {
		t.Errorf("REASON-WRITE: %q", r)
	}

	quiet := perJobClient(t)
	for err, want := range map[error]string{
		timeoutErr{}:                   "it did not log in within",
		bufio.ErrTooLong:               "it sent a message over 64 KB",
		errors.New("connection reset"): "the connection broke: connection reset",
	} {
		if r := quiet.whyClosed(err, false); !strings.HasPrefix(r, want) {
			t.Errorf("REASON-READ: %v gave %q, want %q", err, r, want)
		}
	}
	// The first reason given is the one kept.
	quiet.closeFor("first")
	quiet.closeFor("second")
	if r := quiet.whyClosed(nil, true); r != "first" {
		t.Errorf("REASON-FIRST: %q", r)
	}
}

// A miner that logs in before the first job exists -- every start, as rentals reconnect at once --
// gets that job when it is made. That is said at info, not as a warning.
func TestLoginBeforeTheFirstJobIsNoWarning(t *testing.T) {
	s, logs := reasonServer(t)
	loggedIn(t, s)
	// The line follows the answer to mining.authorize, which the miner may read before it is written.
	for end := time.Now().Add(3 * time.Second); logs.FilterMessageSnippet("No job yet").Len() == 0 && time.Now().Before(end); {
		time.Sleep(20 * time.Millisecond)
	}
	if logs.FilterMessageSnippet("No job yet").Len() != 1 {
		t.Fatal("LOGIN-BEFORE-JOB-SAID: the log does not say the miner gets the first job when it is made")
	}
	if w := logs.FilterLevelExact(zapcore.WarnLevel).All(); len(w) != 0 {
		t.Fatalf("LOGIN-BEFORE-JOB-NOT-WARN: logging in before the first job logged a warning: %q", w[0].Message)
	}
}
