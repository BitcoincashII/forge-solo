package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The gateway is started from the install folder with the config in the data folder, from the data
// folder, with the Settings password and the stop on stdin's end; what it prints goes to
// forge-gateway.log.
func TestTheGatewayIsStartedWithItsConfigAndPassword(t *testing.T) {
	w := gatewayWorld(t, "args")
	if err := startGateway(); err != nil {
		t.Fatal(err)
	}
	var rec []byte
	if !waitFor(10*time.Second, func() bool { rec, _ = os.ReadFile(filepath.Join(w.dir, "args")); return len(rec) > 0 }) {
		t.Fatal("setup: the helper gateway did not start")
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(rec)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		got[k] = v
	}
	if want := "-config|" + dpath("forge-gateway.json"); got["args"] != want {
		t.Errorf("GWL-START-ARGS: the gateway's arguments are %q, want %q", got["args"], want)
	}
	cwd, err1 := os.Stat(got["cwd"])
	data, err2 := os.Stat(dataDir)
	if err1 != nil || err2 != nil || !os.SameFile(cwd, data) {
		t.Errorf("GWL-START-DIR: the gateway runs in %q, not the data folder %q", got["cwd"], dataDir)
	}
	if got["SETTINGS_PASSWORD"] != testPassword {
		t.Errorf("GWL-START-PASSWORD: the gateway was not given the Settings password (%d characters)", len(got["SETTINGS_PASSWORD"]))
	}
	if got["FORGE_STOP_ON_STDIN_EOF"] != "1" {
		t.Errorf("GWL-START-EOF: FORGE_STOP_ON_STDIN_EOF is %q, not 1", got["FORGE_STOP_ON_STDIN_EOF"])
	}
	stopGracefully(gatewayKey, 10*time.Second)
	var b []byte
	waitFor(5*time.Second, func() bool {
		b, _ = os.ReadFile(dpath("forge-gateway.log"))
		return strings.Contains(string(b), "the gateway's stdout") && strings.Contains(string(b), "the gateway's stderr")
	})
	if !strings.Contains(string(b), "the gateway's stdout\n") || !strings.Contains(string(b), "the gateway's stderr\n") {
		t.Errorf("GWL-START-LOG: forge-gateway.log has %q, want what the gateway printed to stdout and to stderr", b)
	}
}

// Quit closes the gateway's stdin, and the gateway stops by itself: it closes its miners'
// connections and sends the pool the shares it holds on the way, which a kill would lose.
func TestQuitLetsTheGatewayStop(t *testing.T) {
	w := gatewayWorld(t, "eof")
	if !startOrKeepTrying(gatewayKey) {
		t.Fatal("setup: the gateway did not start")
	}
	time.Sleep(300 * time.Millisecond) // let it come to read stdin
	start := time.Now()
	stopForExit()
	if took := time.Since(start); took > 8*time.Second {
		t.Errorf("GWL-STOP-EOF: the stop took %v for a gateway that stops when its stdin closes", took)
	}
	if !w.has("clean") {
		t.Fatalf("GWL-STOP-EOF: the gateway did not stop by itself when its stdin closed (no marker): it was killed; log:\n%s", launcherLog())
	}
	if !strings.Contains(launcherLog(), "stopping: the gateway\n") || !strings.Contains(launcherLog(), "gateway stopped in ") {
		t.Errorf("GWL-STOP-LOGGED: launcher.log does not say the gateway was stopped, and how long it took:\n%s", launcherLog())
	}
	if w.tips.last() != tipStopping || started(gatewayKey) {
		t.Errorf("GWL-STOP-TRAY: after the stop the tray says %q, the gateway tracked: %v", w.tips.last(), started(gatewayKey))
	}
}

// A gateway that does not stop within its grace is killed.
func TestQuitKillsAGatewayThatDoesNotStop(t *testing.T) {
	w := gatewayWorld(t, "stuck")
	gatewayStopGrace = time.Second
	if !startOrKeepTrying(gatewayKey) {
		t.Fatal("setup: the gateway did not start")
	}
	if !waitFor(10*time.Second, func() bool { return w.has("beat") }) {
		t.Fatal("setup: the helper gateway did not start")
	}
	start := time.Now()
	stopForExit()
	if took := time.Since(start); took < time.Second {
		t.Fatalf("GWL-STOP-KILL: the gateway was ended after %v, before its grace", took)
	}
	time.Sleep(500 * time.Millisecond)
	a, _ := os.ReadFile(filepath.Join(w.dir, "beat"))
	time.Sleep(time.Second)
	b, _ := os.ReadFile(filepath.Join(w.dir, "beat"))
	if string(a) != string(b) {
		t.Fatalf("GWL-STOP-KILL: the gateway still runs after its grace (heartbeat %s, then %s)", a, b)
	}
	if !strings.Contains(launcherLog(), "gateway did not stop in 1s: killed") {
		t.Errorf("GWL-STOP-KILL-LOGGED: launcher.log does not say the gateway was killed:\n%s", launcherLog())
	}
}

// slowLog stands in for forge-gateway.log on a slow disk: each write takes a while.
type slowLog struct{}

func (slowLog) Write(p []byte) (int, error) { time.Sleep(500 * time.Millisecond); return len(p), nil }

// The gateway's last line on stderr says why it exited, and launcher.log has it whole, even when
// it reaches the tray after the exit: the tray waits for the end of the gateway's stderr.
func TestTheLastLineIsReadToItsEnd(t *testing.T) {
	gatewayWorld(t, "lastline")
	startSupervising(t)
	stderrEndWait = 5 * time.Second
	gatewayOutput = func() io.Writer { return slowLog{} }
	if err := startGateway(); err != nil {
		t.Fatal(err)
	}
	if !waitFor(15*time.Second, logHas("exited on its own")) {
		t.Fatalf("setup: the gateway's exit was not seen; log:\n%s", launcherLog())
	}
	if !waitFor(5*time.Second, logHas("forge-gateway.exe said: "+helperLastLine+"\n")) {
		t.Fatalf("GWL-LASTLINE: launcher.log does not have the gateway's whole last line:\n%s", launcherLog())
	}
	if strings.Index(launcherLog(), "exited on its own") > strings.Index(launcherLog(), "forge-gateway.exe said: ") {
		t.Errorf("GWL-LASTLINE-ORDER: what the gateway said is logged before its exit:\n%s", launcherLog())
	}
}

// lastLine keeps the last line that is not empty, at most lastLineMax bytes of it, and a last line
// with no new line at its end.
func TestLastLine(t *testing.T) {
	l := &lastLine{done: make(chan struct{})}
	for _, s := range []string{"first\n", "sec", "ond\r\n", "\n", "  \n"} {
		_, _ = l.Write([]byte(s))
	}
	if l.text() != "second" {
		t.Errorf("GWL-LASTLINE-TEXT: the last line is %q, want second", l.text())
	}
	_, _ = l.Write([]byte("third, with no new line"))
	if l.text() != "third, with no new line" {
		t.Errorf("GWL-LASTLINE-PARTIAL: the last line is %q", l.text())
	}
	_, _ = l.Write([]byte("\n" + strings.Repeat("x", 3*lastLineMax) + "\n"))
	if l.text() != strings.Repeat("x", lastLineMax) {
		t.Errorf("GWL-LASTLINE-LONG: a long line kept %d bytes, want %d", len(l.text()), lastLineMax)
	}
}

// Restart Forge Gateway stops the gateway cleanly and starts it again. Two clicks at once make one
// restart: two would leave two gateways, one no longer tracked.
func TestRestartForgeGateway(t *testing.T) {
	w := gatewayWorld(t, "eof")
	restartGateway()
	if w.starts() != 0 {
		t.Fatal("GWL-RESTART-UNSTARTED: Restart Forge Gateway started a gateway that boot had not come to")
	}
	gatewayDue.Store(true)
	if !startOrKeepTrying(gatewayKey) || !waitFor(10*time.Second, func() bool { return w.starts() == 1 }) {
		t.Fatal("setup: the gateway did not start")
	}
	first := pid(gatewayKey)
	time.Sleep(300 * time.Millisecond)
	done := make(chan struct{})
	go func() { restartGateway(); close(done) }()
	restartGateway()
	<-done
	if !waitFor(10*time.Second, func() bool { return w.starts() >= 2 }) {
		t.Fatalf("GWL-RESTART: the gateway was not started again; log:\n%s", launcherLog())
	}
	time.Sleep(500 * time.Millisecond)
	if n := w.starts(); n != 2 {
		t.Errorf("GWL-RESTART-ONCE: two Restart clicks at once started the gateway %d times in all, want 2", n)
	}
	// The second click did nothing: neither a second stop nor a start beside the gateway stopping.
	if n := strings.Count(launcherLog(), "restarting the gateway\n"); n != 1 || strings.Contains(launcherLog(), "which is not running") {
		t.Errorf("GWL-RESTART-ONCE: two Restart clicks at once made %d restarts; log:\n%s", n, launcherLog())
	}
	if !w.has("clean") || !strings.Contains(launcherLog(), "restarting the gateway") {
		t.Errorf("GWL-RESTART-CLEAN: the gateway was not stopped cleanly for the restart; log:\n%s", launcherLog())
	}
	if p := pid(gatewayKey); p == 0 || p == first {
		t.Errorf("GWL-RESTART: after the restart the gateway runs as process %d, before it %d", p, first)
	}
	if !w.tips.has(tipRestartingGW) {
		t.Errorf("GWL-RESTART-TRAY: the tray never said the gateway restarts: %q", w.tips.all())
	}

	// Not running, once boot came to it: started at once.
	stopNow(gatewayKey)
	restartGateway()
	if !started(gatewayKey) || !strings.Contains(launcherLog(), "starting the gateway, which is not running") {
		t.Errorf("GWL-RESTART-NOT-RUNNING: Restart Forge Gateway did not start a gateway that was not running; log:\n%s", launcherLog())
	}
}
