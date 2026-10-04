package main

import (
	"encoding/binary"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
)

// portsWorld points the public-port check at ports (all required), with stand-ins for the
// system's listener tables and program names, and restores everything after the test.
func portsWorld(t *testing.T, listeners []tcpListener, ports ...string) {
	t.Helper()
	savedData, savedPublic, savedTable, savedName, savedProbe := dataDir, publicPorts, tcpListeners, processName, listenProbe
	t.Cleanup(func() {
		dataDir, publicPorts, tcpListeners, processName, listenProbe = savedData, savedPublic, savedTable, savedName, savedProbe
	})
	dataDir = t.TempDir()
	publicPorts = publicPorts[:0:0]
	for _, p := range ports {
		publicPorts = append(publicPorts, struct {
			port, what string
			required   bool
		}{p, "the miner port", true})
	}
	tcpListeners = func() []tcpListener { return listeners }
	processName = func(pid int) string {
		if pid == 4242 {
			return "minerproxy.exe"
		}
		return ""
	}
}

// The check never listens on a public address itself: Windows Firewall would ask the person at the
// screen to let forge-solo.exe through, which has no rule and needs none.
func TestPortCheckListensOnlyOnThisMachine(t *testing.T) {
	free := freeTCPPort(t)
	portsWorld(t, nil, free)
	var addrs []string
	listenProbe = func(network, addr string) (net.Listener, error) {
		addrs = append(addrs, network+" "+addr)
		return net.Listen(network, addr)
	}
	if err := checkPublicPorts(); err != nil {
		t.Fatalf("a free port was reported taken: %v", err)
	}
	for _, a := range addrs {
		host, _, _ := net.SplitHostPort(strings.Fields(a)[1])
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			t.Errorf("PUBLIC-PORT-LOOPBACK-ONLY: the check listened on %s", a)
		}
	}
}

// Another program listening on a public port, over IPv4 or IPv6, on any address, is found in the
// system's listener tables, and the log names it.
func TestPortCheckReadsTheListenerTables(t *testing.T) {
	v4, v6, other := freeTCPPort(t), freeTCPPort(t), freeTCPPort(t)
	n := func(p string) int { i, _ := strconv.Atoi(p); return i }
	listeners := []tcpListener{
		{net.IPv4zero.To4(), n(v4), 4242},
		{net.IPv6unspecified, n(v6), 7},
	}
	for _, tc := range []struct{ port, code, holder string }{
		{v4, "PUBLIC-PORT-TABLE-V4", "minerproxy.exe, process 4242 listens on it at 0.0.0.0"},
		{v6, "PUBLIC-PORT-TABLE-V6", "process 7 listens on it at ::"},
	} {
		portsWorld(t, listeners, tc.port)
		err := checkPublicPorts()
		if err == nil {
			t.Errorf("%s: port %s, listened on by another program, was taken as free", tc.code, tc.port)
			continue
		}
		if err.reserved || !strings.Contains(err.Error(), "another program uses port "+tc.port+", the miner port ("+tc.holder+")") {
			t.Errorf("PUBLIC-PORT-CAUSE: %s: the error does not say who holds the port: %v", tc.code, err)
		}
	}
	portsWorld(t, listeners, other)
	if err := checkPublicPorts(); err != nil {
		t.Errorf("PUBLIC-PORT-OTHERS: a port no one listens on was reported taken: %v", err)
	}
}

// A port Windows keeps for itself (a range reserved for Hyper-V, WSL or Docker) has no listener,
// and no program may have it: the check says so, with Windows's own error and where to look,
// rather than blaming another program.
func TestAReservedPortIsSaidSo(t *testing.T) {
	port := freeTCPPort(t)
	portsWorld(t, nil, port)
	listenProbe = func(network, addr string) (net.Listener, error) {
		return nil, &net.OpError{Op: "listen", Net: network, Err: os.NewSyscallError("bind", accessDenied)}
	}
	err := checkPublicPorts()
	if err == nil || !err.reserved || err.why() != "Windows keeps port "+port+", the miner port, for itself" {
		t.Fatalf("PUBLIC-PORT-RESERVED: a port the system refuses gave %v", err)
	}
	if !strings.Contains(err.Error(), accessDenied.Error()) || !strings.Contains(err.Error(), "excludedportrange") {
		t.Errorf("PUBLIC-PORT-RESERVED-CAUSE: the log line lacks the system's error or where to look: %v", err)
	}
}

// The listener tables as GetExtendedTcpTable gives them: IPv4 rows of 24 bytes, IPv6 rows of 56,
// the port in network byte order. A table cut short is read as far as it goes.
func TestParseTCPTable(t *testing.T) {
	v4 := make([]byte, 4+2*24)
	binary.LittleEndian.PutUint32(v4, 3) // says 3 rows; 2 are there
	row := v4[4:28]
	copy(row[4:8], []byte{0, 0, 0, 0})
	row[8], row[9] = 0x0d, 0x05 // 3333
	binary.LittleEndian.PutUint32(row[20:24], 4242)
	row = v4[28:52]
	copy(row[4:8], []byte{127, 0, 0, 1})
	row[8], row[9] = 0x76, 0x5d // 30301
	binary.LittleEndian.PutUint32(row[20:24], 99)
	got := parseTCPTable(v4, false)
	if len(got) != 2 || !got[0].ip.Equal(net.IPv4zero) || got[0].port != 3333 || got[0].pid != 4242 ||
		!got[1].ip.Equal(net.IPv4(127, 0, 0, 1)) || got[1].port != 30301 || got[1].pid != 99 {
		t.Errorf("PORTS-TABLE-V4: read %+v", got)
	}

	v6 := make([]byte, 4+56)
	binary.LittleEndian.PutUint32(v6, 1)
	row = v6[4:60]
	copy(row[0:16], net.IPv6loopback)
	row[20], row[21] = 0x20, 0x93 // 8339
	binary.LittleEndian.PutUint32(row[52:56], 7)
	got = parseTCPTable(v6, true)
	if len(got) != 1 || !got[0].ip.Equal(net.IPv6loopback) || got[0].port != 8339 || got[0].pid != 7 {
		t.Errorf("PORTS-TABLE-V6: read %+v", got)
	}
	if got := parseTCPTable(nil, true); got != nil {
		t.Errorf("PORTS-TABLE-EMPTY: read %+v from nothing", got)
	}
}

func freeTCPPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, p, _ := net.SplitHostPort(l.Addr().String())
	return p
}
