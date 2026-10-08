package main

import (
	"encoding/binary"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// A holder is a stand-in for the program on a port: what the system says of process 4242.
type holder struct {
	path, name, exe string // its full path, and its file name, as this account can read them; its file name from a snapshot
	soloRuns        bool   // Forge Solo runs on the PC, for some account
	service         bool   // it is the ForgeGateway service
}

// holdPort makes process 4242 listen on port, as h, and returns the port as a number.
func holdPort(port string, h holder) int {
	n, _ := strconv.Atoi(port)
	tcpListeners = func() []tcpListener { return []tcpListener{{net.IPv4zero.To4(), n, 4242}} }
	of := func(s string) func(int) string {
		return func(pid int) string {
			if pid == 4242 {
				return s
			}
			return ""
		}
	}
	processPath, processName, exeName = of(h.path), of(h.name), of(h.exe)
	forgeSoloRuns = func() bool { return h.soloRuns }
	gatewayServicePID = func() int {
		if h.service {
			return 4242
		}
		return 0
	}
	return n
}

// Another program on a port the gateway listens on stops the start: the gateway would exit at once.
// The tray says who holds it, in a few words, and offers Try Again; launcher.log names the program,
// its process and what to do. Forge Solo, whose TIDES mode is the same gateway, is named as such,
// for this account and for another; so is another forge-gateway.exe and the ForgeGateway service,
// which 1.0.0's guide set up. The gateway is not started.
func TestATakenPortIsSaidWithWhoHoldsIt(t *testing.T) {
	const soloPath = `C:\Users\you\AppData\Local\Programs\ForgeSolo\stratum.exe`
	for _, tc := range []struct {
		code   string
		h      holder
		status bool // the status page's port, not the miners'
		tray   string
		log    string
	}{
		{"GWL-PORT-OTHER", holder{path: `C:\Tools\minerproxy.exe`, name: "minerproxy.exe", exe: "minerproxy.exe"}, false,
			"another program uses port %p",
			"Forge Gateway cannot start: another program uses port %p, the miner port (minerproxy.exe, process 4242 listens on it at 0.0.0.0); close that program, then right-click Forge Gateway's tray icon: Try Again\n"},
		{"GWL-PORT-SOLO", holder{path: soloPath, name: "stratum.exe", exe: "stratum.exe"}, false,
			"Forge Solo uses port %p",
			"Forge Gateway cannot start: Forge Solo uses port %p, the miner port (stratum.exe, process 4242 listens on it at 0.0.0.0); Forge Solo mines on this computer already, and its TIDES mode is the same gateway, built in: run one of the two. To run Forge Gateway, quit Forge Solo (right-click its tray icon, then Quit Forge Solo), then right-click Forge Gateway's tray icon: Try Again\n"},
		{"GWL-PORT-SOLO-OTHER-ACCOUNT", holder{exe: "stratum.exe", soloRuns: true}, false,
			"Forge Solo uses port %p",
			"Forge Gateway cannot start: Forge Solo uses port %p, the miner port (stratum.exe, process 4242 listens on it at 0.0.0.0); Forge Solo mines on this computer already"},
		{"GWL-PORT-GATEWAY", holder{path: `C:\ForgeGateway\forge-gateway.exe`, name: "forge-gateway.exe", exe: "forge-gateway.exe"}, false,
			"forge-gateway.exe uses port %p",
			"Forge Gateway cannot start: forge-gateway.exe uses port %p, the miner port (forge-gateway.exe, process 4242 listens on it at 0.0.0.0); another Forge Gateway runs on this computer, in a Command Prompt or for another Windows account: stop it, then right-click Forge Gateway's tray icon: Try Again\n"},
		{"GWL-PORT-GATEWAY-OTHER-ACCOUNT", holder{exe: "forge-gateway.exe"}, false,
			"forge-gateway.exe uses port %p",
			"Forge Gateway cannot start: forge-gateway.exe uses port %p, the miner port (forge-gateway.exe, process 4242"},
		{"GWL-PORT-SERVICE", holder{exe: "forge-gateway.exe", service: true}, false,
			"its Windows service uses port %p",
			"Forge Gateway cannot start: the ForgeGateway Windows service uses port %p, the miner port (forge-gateway.exe, process 4242 listens on it at 0.0.0.0); it is a Forge Gateway installed as a service, 1.0.0's say, and it starts with Windows. Run Forge Gateway's installer again and choose Yes when it offers to remove the service, or in a Command Prompt run as administrator run sc stop ForgeGateway, then sc delete ForgeGateway; then right-click Forge Gateway's tray icon: Try Again\n"},
		{"GWL-PORT-STATUS", holder{path: `C:\Tools\web.exe`, name: "web.exe", exe: "web.exe"}, true,
			"another program uses port %p",
			"Forge Gateway cannot start: another program uses port %p, the status page (web.exe, process 4242 listens on it at 0.0.0.0); close that program, then right-click Forge Gateway's tray icon: Try Again\n"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			w := gatewayWorld(t, "eof")
			port := stratumPort
			if tc.status {
				port = statusPort
			}
			holdPort(port, tc.h)
			boot()
			tray := "Forge Gateway cannot start: " + strings.ReplaceAll(tc.tray, "%p", port)
			if w.tips.last() != tray || strings.Join(w.tryShown.all(), " ") != "true" {
				t.Errorf("%s: the tray says %q, Try Again %v; want %q and Try Again shown", tc.code, w.tips.last(), w.tryShown.all(), tray)
			}
			if log := strings.ReplaceAll(tc.log, "%p", port); !strings.Contains(launcherLog(), log) {
				t.Errorf("%s: launcher.log does not say who holds the port and what to do:\nwant %s\nlog:\n%s", tc.code, log, launcherLog())
			}
			if w.starts() != 0 || started(gatewayKey) || len(w.opened.all()) != 0 {
				t.Errorf("%s: the gateway was started (%d), or the status page opened (%v), with its port taken", tc.code, w.starts(), w.opened.all())
			}
		})
	}
}

// A port Windows keeps for itself (a range reserved for Hyper-V, WSL or Docker) has no listener,
// and no program may have it: the tray says so, and launcher.log gives Windows's error and where to
// look, rather than blaming another program.
func TestAReservedPortIsSaidSo(t *testing.T) {
	w := gatewayWorld(t, "eof")
	listenProbe = func(network, addr string) (net.Listener, error) {
		return nil, &net.OpError{Op: "listen", Net: network, Err: os.NewSyscallError("bind", accessDenied)}
	}
	boot()
	if want := "Forge Gateway cannot start: Windows keeps port " + stratumPort + " for itself"; w.tips.last() != want {
		t.Errorf("GWL-PORT-RESERVED: the tray says %q, not %q", w.tips.last(), want)
	}
	want := "Forge Gateway cannot start: Windows keeps port " + stratumPort + ", the miner port, for itself ("
	if !strings.Contains(launcherLog(), want) || !strings.Contains(launcherLog(), "excludedportrange protocol=tcp); then right-click Forge Gateway's tray icon: Try Again\n") {
		t.Errorf("GWL-PORT-RESERVED-LOGGED: launcher.log lacks %q with Windows's error, where to look and Try Again:\n%s", want, launcherLog())
	}
	if w.starts() != 0 || started(gatewayKey) {
		t.Error("GWL-PORT-RESERVED: the gateway was started on a port Windows keeps")
	}
}

// A port Windows keeps for IPv6 alone stops the start too: the gateway listens on IPv6 as well. A
// PC with IPv6 turned off has its ports all the same.
func TestAPortReservedForIPv6IsSaidSo(t *testing.T) {
	gatewayWorld(t, "eof")
	refuse6 := func(err error) func(string, string) (net.Listener, error) {
		return func(network, addr string) (net.Listener, error) {
			if network == "tcp6" {
				return nil, &net.OpError{Op: "listen", Net: network, Err: os.NewSyscallError("bind", err)}
			}
			return net.Listen(network, addr)
		}
	}
	listenProbe = refuse6(accessDenied)
	if err := checkPorts(); err == nil || !err.reserved || !strings.Contains(err.Error(), "netsh int ipv6 show excludedportrange") {
		t.Errorf("GWL-PORT-RESERVED-V6: a port the system refuses over IPv6 gave %v", err)
	}
	listenProbe = refuse6(syscall.EADDRNOTAVAIL)
	if err := checkPorts(); err != nil {
		t.Errorf("GWL-PORT-NO-IPV6: with IPv6 off, free ports were reported taken: %v", err)
	}
}

// The check never listens on a public address itself: Windows Firewall would ask the person at the
// screen to let forge-gateway-tray.exe through, which has no rule and needs none. A free port is
// let go again after the check.
func TestPortCheckListensOnlyOnThisMachine(t *testing.T) {
	gatewayWorld(t, "eof")
	var addrs []string
	listenProbe = func(network, addr string) (net.Listener, error) {
		addrs = append(addrs, network+" "+addr)
		return net.Listen(network, addr)
	}
	if err := checkPorts(); err != nil {
		t.Fatalf("free ports were reported taken: %v", err)
	}
	if len(addrs) == 0 {
		t.Fatal("GWL-PORT-PROBED: the check tried no port")
	}
	for _, a := range addrs {
		host, _, _ := net.SplitHostPort(strings.Fields(a)[1])
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			t.Errorf("GWL-PORT-LOOPBACK-ONLY: the check listened on %s", a)
		}
	}
	l, err := net.Listen("tcp4", "127.0.0.1:"+stratumPort)
	if err != nil {
		t.Fatalf("GWL-PORT-RELEASED: the check kept port %s, which the gateway then could not take: %v", stratumPort, err)
	}
	_ = l.Close()
}

// Who holds a port, in the order holderOf goes by: the service first (its path cannot be read),
// then a program in a ForgeSolo folder, then forge-gateway.exe by path or by snapshot, then another
// account's Forge Solo.
func TestHolderOf(t *testing.T) {
	gatewayWorld(t, "eof")
	for _, tc := range []struct {
		h    holder
		want string
	}{
		{holder{path: `C:\Users\a\AppData\Local\Programs\forgesolo\api.exe`}, holderSolo},
		{holder{path: `D:\Apps\ForgeSolo\stratum.exe`, soloRuns: false}, holderSolo},
		{holder{path: `C:\Users\a\AppData\Local\Programs\ForgeGateway\FORGE-GATEWAY.EXE`}, holderGateway},
		{holder{exe: "forge-gateway.exe"}, holderGateway},
		{holder{exe: "forge-gateway.exe", soloRuns: true}, holderGateway},
		{holder{exe: "forge-gateway.exe", service: true}, holderService},
		{holder{path: `C:\Program Files\x\forge-gateway.exe`, service: true}, holderService},
		{holder{exe: "stratum.exe", soloRuns: true}, holderSolo},
		{holder{exe: "stratum.exe"}, holderOther},
		{holder{path: `C:\ForgeSoloTools\x.exe`, soloRuns: true}, holderOther},
		{holder{}, holderOther},
	} {
		holdPort(stratumPort, tc.h)
		if got := holderOf(4242); got != tc.want {
			t.Errorf("GWL-PORT-HOLDER: %+v is held by %s, want %s", tc.h, got, tc.want)
		}
	}
}

// The listener tables as GetExtendedTcpTable gives them: IPv4 rows of 24 bytes, IPv6 rows of 56,
// the port in network byte order. A table cut short is read as far as it goes.
func TestParseTCPTable(t *testing.T) {
	v4 := make([]byte, 4+2*24)
	binary.LittleEndian.PutUint32(v4, 3) // says 3 rows; 2 are there
	row := v4[4:28]
	row[8], row[9] = 0x0d, 0x05 // 3333
	binary.LittleEndian.PutUint32(row[20:24], 4242)
	row = v4[28:52]
	copy(row[4:8], []byte{127, 0, 0, 1})
	row[8], row[9] = 0x0c, 0x12 // 3090
	binary.LittleEndian.PutUint32(row[20:24], 99)
	got := parseTCPTable(v4, false)
	if len(got) != 2 || !got[0].ip.Equal(net.IPv4zero) || got[0].port != 3333 || got[0].pid != 4242 ||
		!got[1].ip.Equal(net.IPv4(127, 0, 0, 1)) || got[1].port != 3090 || got[1].pid != 99 {
		t.Errorf("GWL-PORTS-TABLE-V4: read %+v", got)
	}
	v6 := make([]byte, 4+56)
	binary.LittleEndian.PutUint32(v6, 1)
	row = v6[4:60]
	copy(row[0:16], net.IPv6loopback)
	row[20], row[21] = 0x0d, 0x05
	binary.LittleEndian.PutUint32(row[52:56], 7)
	got = parseTCPTable(v6, true)
	if len(got) != 1 || !got[0].ip.Equal(net.IPv6loopback) || got[0].port != 3333 || got[0].pid != 7 {
		t.Errorf("GWL-PORTS-TABLE-V6: read %+v", got)
	}
	if got := parseTCPTable(nil, true); got != nil {
		t.Errorf("GWL-PORTS-TABLE-EMPTY: read %+v from nothing", got)
	}
}
