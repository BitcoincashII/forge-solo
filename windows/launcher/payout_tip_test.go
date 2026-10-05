package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

// The tray said "set your payout address in the dashboard" at every start, for about a second
// until the miner ran, also with an address set. It asks only when the dashboard's API says none is
// set, and not when it cannot tell: no answer, an answer that is not one (the database cannot be
// read), or one without the field. The start waits for that answer before it starts the miner, so
// an API that never answers holds it a few seconds at most.
func TestTheTrayAsksForAPayoutAddressOnlyWhenNoneIsSet(t *testing.T) {
	savedData, savedWeb, savedAPI, savedBrowser, savedTip, savedUnset, savedWait := dataDir, webPort, apiPort, openBrowser, setTooltip, payoutAddressUnset, payoutAskTimeout
	var mu sync.Mutex
	var shown []string
	dataDir = t.TempDir()
	openBrowser = func(string) {}
	setTooltip = func(s string) { mu.Lock(); shown = append(shown, s); mu.Unlock() }
	tipMu.Lock()
	savedStop := stopShown
	stopShown = false
	tipMu.Unlock()
	troubleMu.Lock()
	clear(trouble)
	troubleMu.Unlock()
	t.Cleanup(func() {
		dataDir, webPort, apiPort, openBrowser, setTooltip, payoutAddressUnset, payoutAskTimeout = savedData, savedWeb, savedAPI, savedBrowser, savedTip, savedUnset, savedWait
		tipMu.Lock()
		stopShown = savedStop
		tipMu.Unlock()
		dashboardOpen.Store(false)
		dashboard = nil
	})

	// What the API's answer means.
	var status int
	var body string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/pool/config" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		s, b := status, body
		mu.Unlock()
		w.WriteHeader(s)
		_, _ = w.Write([]byte(b))
	}))
	_, apiPort, _ = net.SplitHostPort(api.Listener.Addr().String())
	for _, c := range []struct {
		code, body string
		status     int
		unset      bool
	}{
		{"TIP-ADDRESS-SET", `{"configured":true,"pool_address":"bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2"}`, 200, false},
		{"TIP-ADDRESS-UNSET", `{"configured":false,"pool_address":""}`, 200, true},
		{"TIP-ADDRESS-NOT-OK", `{"configured":false,"success":false}`, 503, false},
		{"TIP-ADDRESS-NO-FIELD", `{"pool_address":""}`, 200, false},
	} {
		mu.Lock()
		status, body = c.status, c.body
		mu.Unlock()
		if got := askPayoutAddressUnset(); got != c.unset {
			t.Errorf("%s: the API answered %d %s, taken as no payout address set: %v", c.code, c.status, c.body, got)
		}
	}
	api.Close()
	if askPayoutAddressUnset() {
		t.Error("TIP-ADDRESS-NO-API: with no API answering, the tray asks for a payout address")
	}

	// An API that takes the request and never answers holds the start, and the miner with it, only
	// as long as payoutAskTimeout, and the tray does not ask then either.
	if payoutAskTimeout <= 0 || payoutAskTimeout > 5*time.Second {
		t.Errorf("TIP-ADDRESS-WAIT: the tray waits %v for the API's answer; the miner starts only after it", payoutAskTimeout)
	}
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	_, apiPort, _ = net.SplitHostPort(slow.Listener.Addr().String())
	payoutAskTimeout = 200 * time.Millisecond
	answer := make(chan bool, 1)
	go func() { answer <- askPayoutAddressUnset() }()
	waited := false
	select {
	case unset := <-answer:
		if unset {
			t.Error("TIP-ADDRESS-SLOW-API: with an API that does not answer, the tray asks for a payout address")
		}
	case <-time.After(5 * time.Second):
		waited = true
		t.Error("TIP-ADDRESS-SLOW-API: with an API that takes the request and never answers, the tray still waits after 5 s, and the miner with it")
	}
	close(release)
	if waited {
		<-answer
	}
	slow.Close()

	// What the tray says when the dashboard opens.
	for _, c := range []struct {
		code  string
		unset bool
	}{{"TIP-ADDRESS-ASKED", true}, {"TIP-ADDRESS-NOT-ASKED", false}} {
		payoutAddressUnset = func() bool { return c.unset }
		mu.Lock()
		shown = nil
		mu.Unlock()
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		_, webPort, _ = net.SplitHostPort(l.Addr().String())
		_ = l.Close()
		openDashboard()
		if !dashboardOpen.Load() {
			t.Fatalf("setup: the dashboard did not open on %s", webPort)
		}
		// Once it answers, its server has read what it serves, before it is closed.
		resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get("http://127.0.0.1:" + webPort + "/")
		if err != nil {
			t.Fatalf("setup: the dashboard does not answer: %v", err)
		}
		_ = resp.Body.Close()
		_ = dashboard.Close()
		dashboardOpen.Store(false)
		mu.Lock()
		got := append([]string(nil), shown...)
		mu.Unlock()
		if slices.Contains(got, tipSetAddress) != c.unset {
			t.Errorf("%s: with no payout address set %v, the tray showed %q", c.code, c.unset, got)
		}
	}
}
