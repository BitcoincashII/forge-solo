package main

import (
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
)

// The dashboard's port is fixed, so another program can hold it. Then the dashboard cannot open:
// the browser is not sent to that program, and the log says why. Mining goes on regardless.
func TestDashboardPortTaken(t *testing.T) {
	savedData, savedWeb, savedBrowser := dataDir, webPort, openBrowser
	dataDir = t.TempDir()
	var opened []string
	openBrowser = func(u string) { opened = append(opened, u) }
	t.Cleanup(func() {
		dataDir, webPort, openBrowser = savedData, savedWeb, savedBrowser
		dashboardOpen.Store(false)
		if dashboard != nil {
			_ = dashboard.Close()
			dashboard = nil
		}
	})
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, webPort, _ = net.SplitHostPort(other.Addr().String())

	openDashboard()
	b, _ := os.ReadFile(dpath("launcher.log"))
	if len(opened) != 0 || dashboardOpen.Load() {
		t.Fatalf("DASH-PORT-TAKEN: with port %s held by another program the browser was opened at %v", webPort, opened)
	}
	if !strings.Contains(string(b), "the dashboard cannot open: another program uses port "+webPort) {
		t.Fatalf("DASH-PORT-TAKEN-LOGGED: launcher.log does not say why:\n%s", b)
	}

	_ = other.Close()
	openDashboard()
	if len(opened) != 1 || !dashboardOpen.Load() {
		t.Fatalf("DASH-OPENS: with the port free the dashboard did not open in the browser (%v)", opened)
	}
	// At an address the browser kept nothing for in 1.0.12, which the dashboard answers by having
	// the browser drop what it kept.
	if want := "http://127.0.0.1:" + webPort + "/solo?v=" + version; opened[0] != want {
		t.Fatalf("DASH-OPENS-VERSION: the dashboard was opened at %q, not %q", opened[0], want)
	}
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get("http://127.0.0.1:" + webPort + "/")
	if err != nil {
		t.Fatalf("DASH-OPENS: the dashboard does not answer: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("DASH-OPENS: / answered %d, want the redirect to /solo", resp.StatusCode)
	}
}

// Another program listening on a public port over IPv4 alone. On Windows the miner's own listen
// (0.0.0.0 over "tcp", one socket for IPv6 and IPv4) still succeeds beside it, and every IPv4
// connection then goes to the other program: the check must see the port as taken.
func TestAPublicPortHeldOverIPv4IsTaken(t *testing.T) {
	savedData, savedPublic := dataDir, publicPorts
	dataDir = t.TempDir()
	t.Cleanup(func() { dataDir, publicPorts = savedData, savedPublic })
	l, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	publicPorts = []struct {
		port, what string
		required   bool
	}{{port, "the miner port", true}}
	if err := checkPublicPorts(); err == nil {
		t.Fatalf("PUBLIC-PORT-IPV4: port %s, held over IPv4 by another program, was taken as free", port)
	}
}
