//go:build !windows

package main

import (
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tryWorld is bootWorld with the tray's tooltips and its Try Again recorded ("true" shown, "false"
// hidden), and a pg_ctl stand-in that notes each start of the database and fails it, once the file
// at the path returned as gate exists (it does, until a test removes it).
func tryWorld(t *testing.T) (tp, shown *tips, pgRuns func() int, gate string) {
	t.Helper()
	dir := t.TempDir()
	runsFile, gate := filepath.Join(dir, "pg_ctl-runs"), filepath.Join(dir, "go")
	t.Setenv("FS_PGCTL", runsFile)
	t.Setenv("FS_GO", gate)
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// It waits 10 s at most, so that one left waiting by a test that failed ends by itself.
	bootWorld(t, map[string]string{"pgsql\\bin\\pg_ctl.exe": `echo run >> "$FS_PGCTL"; i=0
while [ ! -f "$FS_GO" ] && [ $i -lt 200 ]; do sleep 0.05; i=$((i+1)); done; exit 1`})
	tp, shown = &tips{}, &tips{}
	savedTip, savedShow, savedPrep := setTooltip, showTryAgain, prepErr
	setTooltip = tp.add
	showTryAgain = func(show bool) { shown.add(strconv.FormatBool(show)) }
	t.Cleanup(func() {
		setTooltip, showTryAgain, prepErr = savedTip, savedShow, savedPrep
		retryOffered.Store(noRetry)
	})
	pgRuns = func() int { b, _ := os.ReadFile(runsFile); return strings.Count(string(b), "run\n") }
	return tp, shown, pgRuns, gate
}

// shownTimes waits until Try Again has been shown or hidden n times in all, which ends a boot that
// Try Again started in the background, and reports whether it was.
func shownTimes(shown *tips, n int) bool {
	return waitFor(5*time.Second, func() bool { return len(shown.all()) >= n })
}

// Another program on the miner port: Forge Solo cannot start, and keeps its tray icon, which says
// what to do and offers Try Again. A second start of Forge Solo only opened the dashboard of the
// copy already running, which nothing served, and that copy never tried again. Try Again with the
// port still taken says so again; with it free, the start goes on.
func TestTryAgainAfterAPortWasTaken(t *testing.T) {
	tp, shown, pgRuns, _ := tryWorld(t)
	held, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(held.Addr().String())
	publicPorts = []struct {
		port, what string
		required   bool
	}{{port, "the miner port", true}}
	boot()
	want := "Forge Solo cannot start: another program uses port " + port + ", the miner port. Close it, then right-click here: Try Again."
	if tp.last() != want || strings.Join(shown.all(), " ") != "true" {
		t.Fatalf("TRY-AGAIN-OFFERED: the tray says %q, Try Again %v; want %q and Try Again shown", tp.last(), shown.all(), want)
	}
	tryAgain()
	if !shownTimes(shown, 3) || tp.last() != want || pgRuns() != 0 {
		t.Fatalf("TRY-AGAIN-STILL-TAKEN: with the port still taken, Try Again gave %q, Try Again %v, %d database starts", tp.last(), shown.all(), pgRuns())
	}
	_ = held.Close()
	tryAgain()
	if !shownTimes(shown, 5) || pgRuns() != 1 {
		t.Fatalf("TRY-AGAIN-STARTS: with the port free, Try Again did not start Forge Solo; log:\n%s", launcherLog())
	}
}

// Try Again acts once per offer: a second click while the first one's start runs, or one while
// nothing failed, starts nothing.
func TestTryAgainOncePerOffer(t *testing.T) {
	_, shown, pgRuns, gate := tryWorld(t)
	tryAgain()
	time.Sleep(300 * time.Millisecond)
	if pgRuns() != 0 || len(shown.all()) != 0 {
		t.Fatalf("TRY-AGAIN-UNOFFERED: Try Again started Forge Solo while it had not failed")
	}
	_ = os.Remove(gate) // the start Try Again makes waits in pg_ctl
	offerTryAgain(retryStart)
	tryAgain()
	tryAgain()
	if !waitFor(5*time.Second, func() bool { return pgRuns() >= 1 }) {
		t.Fatal("setup: the offered Try Again did not start Forge Solo")
	}
	time.Sleep(300 * time.Millisecond) // time for a second start to come to pg_ctl too
	n := pgRuns()
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !shownTimes(shown, 3) || n != 1 {
		t.Fatalf("TRY-AGAIN-ONCE: two clicks started Forge Solo %d times", n)
	}
}

// The database did not start: the tray says where to look and offers Try Again, which starts it
// again.
func TestTryAgainAfterTheDatabaseFailed(t *testing.T) {
	tp, shown, pgRuns, _ := tryWorld(t)
	boot()
	want := "Forge Solo cannot start: the database did not start (see launcher.log and pglog.txt). Right-click here: Try Again."
	if tp.last() != want || strings.Join(shown.all(), " ") != "true" {
		t.Fatalf("TRY-AGAIN-DB-OFFERED: the tray says %q, Try Again %v", tp.last(), shown.all())
	}
	tryAgain()
	if !shownTimes(shown, 3) || pgRuns() != 2 {
		t.Fatalf("TRY-AGAIN-DB: Try Again did not start the database again (%d starts)", pgRuns())
	}
}

// secrets.env could not be read (another program held it a moment): Try Again reads it again, and
// with it readable, Forge Solo starts.
func TestTryAgainAfterSecretsCouldNotBeRead(t *testing.T) {
	tp, shown, pgRuns, _ := tryWorld(t)
	if err := os.Mkdir(dpath("secrets.env"), 0o755); err != nil { // cannot be read as a file
		t.Fatal(err)
	}
	prepErr = errors.New("secrets.env cannot be read")
	offerTryAgain(retryStart)
	tryAgain()
	if !strings.HasPrefix(tp.last(), "Forge Solo cannot start: secrets.env cannot be read") || retryOffered.Load() != retryStart || pgRuns() != 0 {
		t.Fatalf("TRY-AGAIN-PREPARE-FAILS: the tray says %q, offered %d", tp.last(), retryOffered.Load())
	}
	_ = os.Remove(dpath("secrets.env"))
	if err := os.WriteFile(dpath("secrets.env"), []byte("BCH2=b\nAUX=a\nDB=d\nTOKEN=t\nSETTINGS=s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tryAgain()
	if !shownTimes(shown, 5) || prepErr != nil || pgRuns() != 1 {
		t.Fatalf("TRY-AGAIN-PREPARE: with secrets.env readable, Try Again did not start Forge Solo (%v); log:\n%s", prepErr, launcherLog())
	}
}

// Another program on the dashboard's port: Try Again opens the dashboard once it is free.
func TestTryAgainOpensTheDashboard(t *testing.T) {
	_, shown, _, _ := tryWorld(t)
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
