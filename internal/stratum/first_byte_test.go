package stratum

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func startFirstByteServer(t *testing.T) string {
	t.Helper()
	s := NewServer(&ServerConfig{Host: "127.0.0.1", Port: 0, MaxConnections: 10, ExtraNonce1Size: 4, ExtraNonce2Size: 8}, zap.NewNop(), nil)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s.ListenAddr()
}

func dialFirstByte(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// closedWithin reports whether the server closed the connection within d (a read that ends in an
// error other than our own deadline).
func closedWithin(c net.Conn, d time.Duration) bool {
	c.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 256)
	for {
		if _, err := c.Read(buf); err != nil {
			return !strings.Contains(err.Error(), "timeout")
		}
	}
}

func readLine(t *testing.T, r *bufio.Reader, c net.Conn, code string) string {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("%s: no answer: %v", code, err)
	}
	return line
}

// A TLS ClientHello has no newline to wait for: the first byte alone closes the connection, so a
// client that tries TLS first learns at once that this port speaks plain stratum.
func TestTLSHandshakeIsClosedAtOnce(t *testing.T) {
	c := dialFirstByte(t, startFirstByteServer(t))
	c.Write([]byte{0x16, 0x03, 0x01, 0x02, 0x00, 0x01, 0x00, 0x01, 0xfc, 0x03, 0x03})
	if !closedWithin(c, time.Second) {
		t.Fatal("FIRST-BYTE-TLS: a TLS handshake was kept open")
	}
}

func TestHTTPRequestIsClosedAtOnce(t *testing.T) {
	c := dialFirstByte(t, startFirstByteServer(t))
	c.Write([]byte("GET / HTTP/1.1\r\nHost: pool\r\n\r\n"))
	if !closedWithin(c, time.Second) {
		t.Fatal("FIRST-BYTE-HTTP: an HTTP request was kept open")
	}
}

// Blank space before the first message is not a reason to hang up.
func TestBlankLinesBeforeSubscribeAreFine(t *testing.T) {
	c := dialFirstByte(t, startFirstByteServer(t))
	c.Write([]byte("\r\n\n \t" + `{"id":1,"method":"mining.subscribe","params":["cgminer/4.12"]}` + "\n"))
	if line := readLine(t, bufio.NewReader(c), c, "FIRST-BYTE-BLANK"); !strings.Contains(line, `"result"`) || !strings.Contains(line, `"id":1`) {
		t.Fatalf("FIRST-BYTE-BLANK: %q", line)
	}
}

// A connection that says nothing is let go after firstMessageTimeout, not the 5-minute read deadline.
func TestSilentConnectionIsLetGo(t *testing.T) {
	saved := firstMessageTimeout
	firstMessageTimeout = 200 * time.Millisecond
	t.Cleanup(func() { firstMessageTimeout = saved })
	c := dialFirstByte(t, startFirstByteServer(t))
	if !closedWithin(c, 2*time.Second) {
		t.Fatal("FIRST-BYTE-SILENT: a silent connection was kept past the first-message timeout")
	}
}

// Before subscribing, a line that is not JSON ends the connection.
func TestGarbageBeforeSubscribeCloses(t *testing.T) {
	c := dialFirstByte(t, startFirstByteServer(t))
	c.Write([]byte("{not json at all}\n"))
	if !closedWithin(c, time.Second) {
		t.Fatal("FIRST-BYTE-GARBAGE: a client that never subscribed kept its connection after a non-JSON line")
	}
}

// After subscribing, one garbled line is only logged: the miner keeps its connection and its next
// message is answered.
func TestGarbageAfterSubscribeIsTolerated(t *testing.T) {
	c := dialFirstByte(t, startFirstByteServer(t))
	r := bufio.NewReader(c)
	c.Write([]byte(`{"id":1,"method":"mining.subscribe","params":["cgminer/4.12"]}` + "\n"))
	if line := readLine(t, r, c, "FIRST-BYTE-SUBSCRIBE"); !strings.Contains(line, `"id":1`) {
		t.Fatalf("FIRST-BYTE-SUBSCRIBE: %q", line)
	}
	c.Write([]byte("garbled line\n" + `{"id":5,"method":"mining.extranonce.subscribe","params":[]}` + "\n"))
	// This server follows subscribe with mining.set_difficulty; read past notifications.
	line := readLine(t, r, c, "FIRST-BYTE-TOLERATED")
	for i := 0; i < 5 && strings.Contains(line, `"method"`); i++ {
		line = readLine(t, r, c, "FIRST-BYTE-TOLERATED")
	}
	if !strings.Contains(line, `"id":5`) || !strings.Contains(line, `"result":true`) {
		t.Fatalf("FIRST-BYTE-TOLERATED: a subscribed miner lost its connection over one garbled line: %q", line)
	}
}
