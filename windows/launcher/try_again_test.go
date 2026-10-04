//go:build !windows

package main

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tryWorld is bootWorld with the tray's tooltips and its Try Again recorded ("true" shown, "false"
// hidden), and node and API ports that answer, so that a boot that gets past its checks ends at
// once: no program is there to start, and none is tried again. boots counts the boots, by the
// tooltip each begins with.
func tryWorld(t *testing.T) (tp, shown *tips, boots func() int) {
	t.Helper()
	bootWorld(t, nil)
	answer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	bch2RPC, aux1175RPC, apiPort = portOf(answer.URL), portOf(answer.URL), portOf(answer.URL)
	tp, shown = &tips{}, &tips{}
	savedTip, savedShow, savedPrep := setTooltip, showTryAgain, prepErr
	setTooltip = tp.add
	showTryAgain = func(show bool) { shown.add(strconv.FormatBool(show)) }
	t.Cleanup(func() {
		mu.Lock()
		stopping = true // nothing more is started
		mu.Unlock()
		minerStart.Wait()
		answer.Close()
		setTooltip, showTryAgain, prepErr = savedTip, savedShow, savedPrep
		retryOffered.Store(noRetry)
		resetStartState()
	})
	boots = func() int {
		n := 0
		for _, s := range tp.all() {
			if s == "Forge Solo: preparing…" {
				n++
			}
		}
		return n
	}
	return tp, shown, boots
}

// shownTimes waits until Try Again has been shown or hidden n times in all, which ends a boot that
// Try Again started in the background, and reports whether it was.
func shownTimes(shown *tips, n int) bool {
	return waitFor(5*time.Second, func() bool { return len(shown.all()) >= n })
}

// holdMinerPort makes another program hold the miner port, and returns that port and its listener.
func holdMinerPort(t *testing.T) (string, net.Listener) {
	t.Helper()
	held, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	_, port, _ := net.SplitHostPort(held.Addr().String())
	publicPorts = []struct {
		port, what string
		required   bool
	}{{port, "the miner port", true}}
	return port, held
}

// Another program on the miner port: Forge Solo cannot start, and keeps its tray icon, which says
// what to do and offers Try Again. A second start of Forge Solo only opened the dashboard of the
// copy already running, which nothing served, and that copy never tried again. Try Again with the
// port still taken says so again; with it free, the start goes on.
func TestTryAgainAfterAPortWasTaken(t *testing.T) {
	tp, shown, boots := tryWorld(t)
	port, held := holdMinerPort(t)
	boot()
	want := "Forge Solo cannot start: another program uses port " + port + ", the miner port. Close it, then right-click here: Try Again."
	if tp.last() != want || strings.Join(shown.all(), " ") != "true" {
		t.Fatalf("TRY-AGAIN-OFFERED: the tray says %q, Try Again %v; want %q and Try Again shown", tp.last(), shown.all(), want)
	}
	tryAgain()
	if !shownTimes(shown, 3) || tp.last() != want || boots() != 2 || dashboardOpen.Load() {
		t.Fatalf("TRY-AGAIN-STILL-TAKEN: with the port still taken, Try Again gave %q, Try Again %v, %d boots", tp.last(), shown.all(), boots())
	}
	_ = held.Close()
	tryAgain()
	if !waitFor(5*time.Second, dashboardOpen.Load) || boots() != 3 || len(shown.all()) != 4 {
		t.Fatalf("TRY-AGAIN-STARTS: with the port free, Try Again did not start Forge Solo (%d boots, Try Again %v); log:\n%s", boots(), shown.all(), launcherLog())
	}
}

// Try Again acts once per offer: a second click while the first one's start runs, or one while
// nothing failed, starts nothing.
func TestTryAgainOncePerOffer(t *testing.T) {
	_, shown, boots := tryWorld(t)
	tryAgain()
	time.Sleep(300 * time.Millisecond)
	if boots() != 0 || len(shown.all()) != 0 {
		t.Fatalf("TRY-AGAIN-UNOFFERED: Try Again started Forge Solo while it had not failed")
	}
	offerTryAgain(retryStart)
	tryAgain()
	tryAgain()
	if !waitFor(5*time.Second, dashboardOpen.Load) {
		t.Fatal("setup: the offered Try Again did not start Forge Solo")
	}
	time.Sleep(300 * time.Millisecond) // time for a second start
	if n := boots(); n != 1 || len(shown.all()) != 2 {
		t.Fatalf("TRY-AGAIN-ONCE: two clicks started Forge Solo %d times (Try Again %v)", n, shown.all())
	}
}

// secrets.env could not be read (another program held it a moment): Try Again reads it again, and
// with it readable, Forge Solo starts (here as far as the miner port another program holds).
func TestTryAgainAfterSecretsCouldNotBeRead(t *testing.T) {
	tp, shown, boots := tryWorld(t)
	// The start ends at the miner port, before the ports prepare picks are waited on.
	holdMinerPort(t)
	// secrets.env cannot be read as a file.
	if err := os.Mkdir(dpath("secrets.env"), 0o755); err != nil {
		t.Fatal(err)
	}
	prepErr = errors.New("secrets.env cannot be read")
	offerTryAgain(retryStart)
	tryAgain()
	if !strings.HasPrefix(tp.last(), "Forge Solo cannot start: secrets.env cannot be read") || retryOffered.Load() != retryStart || boots() != 0 {
		t.Fatalf("TRY-AGAIN-PREPARE-FAILS: the tray says %q, offered %d", tp.last(), retryOffered.Load())
	}
	_ = os.Remove(dpath("secrets.env"))
	if err := os.WriteFile(dpath("secrets.env"), []byte("BCH2=b\nAUX=a\nDB=d\nTOKEN=t\nSETTINGS=s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tryAgain()
	if !shownTimes(shown, 5) || prepErr != nil || boots() != 1 {
		t.Fatalf("TRY-AGAIN-PREPARE: with secrets.env readable, Try Again did not start Forge Solo (%v); log:\n%s", prepErr, launcherLog())
	}
}

// Another program on the dashboard's port: Try Again opens the dashboard once it is free.
func TestTryAgainOpensTheDashboard(t *testing.T) {
	_, shown, _ := tryWorld(t)
	var opened []string
	openBrowser = func(u string) { opened = append(opened, u) }
	other, err := net.Listen("tcp", "127.0.0.1:"+webPort)
	if err != nil {
		t.Fatal(err)
	}
	openDashboard()
	if dashboardOpen.Load() || retryOffered.Load() != retryDashboard || len(shown.all()) != 1 {
		t.Fatalf("TRY-AGAIN-DASHBOARD-OFFERED: with its port taken, Try Again was not offered for the dashboard (%d)", retryOffered.Load())
	}
	_ = other.Close()
	tryAgain()
	if !dashboardOpen.Load() || len(opened) != 1 {
		t.Fatalf("TRY-AGAIN-DASHBOARD: Try Again did not open the dashboard (%v)", opened)
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get("http://127.0.0.1:" + webPort + "/")
	if err != nil {
		t.Fatalf("TRY-AGAIN-DASHBOARD: the dashboard does not answer: %v", err)
	}
	_ = resp.Body.Close()
}

// Another program held the dashboard's port at start, and everything else started: once Try Again
// opens the dashboard, the tray says "running". It stayed on "set your payout address in the
// dashboard" until something else changed the tray.
func TestTryAgainOpensTheDashboardAndSaysRunning(t *testing.T) {
	tp := startFailWorld(t, map[string]string{"bitcoincashIId.exe": sleeper, "elevenseventyfived.exe": sleeper, "api.exe": sleeper, "stratum.exe": sleeper})
	savedShow := showTryAgain
	showTryAgain = func(bool) {}
	t.Cleanup(func() { showTryAgain = savedShow; retryOffered.Store(noRetry) })
	other, err := net.Listen("tcp", "127.0.0.1:"+webPort)
	if err != nil {
		t.Fatal(err)
	}
	boot()
	if !waitFor(5*time.Second, func() bool { return started("stratum") }) {
		t.Fatalf("setup: the miner did not start; log:\n%s", launcherLog())
	}
	minerStart.Wait()
	if tp.has("Forge Solo: running") || retryOffered.Load() != retryDashboard {
		t.Fatalf("setup: with the dashboard's port taken the tray said running, or Try Again was not offered: %q", tp.all())
	}
	_ = other.Close()
	tryAgain()
	if !dashboardOpen.Load() {
		t.Fatalf("setup: Try Again did not open the dashboard; log:\n%s", launcherLog())
	}
	if tp.last() != "Forge Solo: running" {
		t.Fatalf("TRY-AGAIN-DASHBOARD-RUNNING: the dashboard is open and everything runs, yet the tray says %q", tp.last())
	}
}

// Every advice after a failed start fits the 127 characters Windows shows, however long the
// reason: the reason is cut, not the advice.
func TestFailTipsFit(t *testing.T) {
	for _, tc := range []struct{ why, advice string }{
		{"another program uses port 8339, the BCH2 node's peer port", closeItTip},
		{"Windows keeps port 25360, the 1175 node's peer port, for itself", reservedTip},
		{"the database did not start (see launcher.log and pglog.txt)", tryAgainTip},
		{strings.Repeat("secrets.env cannot be read (a long reason) ", 5), tryAgainTip},
	} {
		tip := failTip(tc.why, tc.advice)
		if len([]rune(tip)) > 127 || !strings.HasSuffix(tip, tc.advice) {
			t.Errorf("TRY-TIP-FITS: %d characters, %q", len([]rune(tip)), tip)
		}
	}
}
