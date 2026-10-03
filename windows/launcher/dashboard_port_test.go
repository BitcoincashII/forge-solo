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
	if len(opened) != 1 || opened[0] != "http://127.0.0.1:"+webPort || !dashboardOpen.Load() {
		t.Fatalf("DASH-OPENS: with the port free the dashboard did not open in the browser (%v)", opened)
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
