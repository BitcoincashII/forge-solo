package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// The tray says what the gateway is doing, as its status page says it: not set up, the node or the
// pool out of reach, the node syncing, mining. An answer the gateway could not give leaves the tray
// as it is; a gateway that could not start is said before its state.
func TestTheTrayFollowsTheGatewaysState(t *testing.T) {
	w := gatewayWorld(t, "status")
	w.setState(t, `{"configured":false,"state":"unconfigured","mode":"off"}`)
	if !startOrKeepTrying(gatewayKey) {
		t.Fatal("setup: the gateway did not start")
	}
	if !waitFor(10*time.Second, func() bool { return askState(t) }) {
		t.Fatal("setup: the helper gateway's status page does not answer")
	}
	watchGatewayState()
	for _, tc := range []struct{ state, mode, tip string }{
		{"unconfigured", "off", tipSetUp},
		{"node_unreachable", "off", tipNodeUnreachable},
		{"node_login", "off", tipNodeLogin},
		{"node_forbidden", "off", tipNodeForbidden},
		{"node_syncing", "starting", tipNodeSyncing},
		{"pool_unreachable", "solo", tipPoolSolo},
		{"pool_unreachable", "waiting", tipPoolWaiting},
		{"starting", "starting", tipWaitingForWork},
		{"active", "tides", tipActive},
		{"a_state_to_come", "tides", tipRunning},
	} {
		w.setState(t, `{"configured":true,"state":"`+tc.state+`","mode":"`+tc.mode+`","state_reason":"x","pool":{"state":"x"}}`)
		if !waitFor(5*time.Second, func() bool { return w.tips.last() == tc.tip }) {
			t.Errorf("GWL-STATE-TIPS: with the gateway in %s (mode %s) the tray says %q, not %q", tc.state, tc.mode, w.tips.last(), tc.tip)
		}
	}
	if !strings.Contains(launcherLog(), "the gateway says: node_login\n") {
		t.Errorf("GWL-STATE-LOGGED: launcher.log does not follow the gateway's state:\n%s", launcherLog())
	}

	// No answer, or one with no state: the tray stays as it is.
	for _, none := range []string{"503", `{"version":"1.0.0"}`, "not json"} {
		w.setState(t, none)
		time.Sleep(300 * time.Millisecond)
		if w.tips.last() != tipRunning {
			t.Errorf("GWL-STATE-NO-ANSWER: an answer %q made the tray say %q", none, w.tips.last())
		}
	}

	// A gateway that could not start is said first.
	setTrouble(gatewayKey, tipProgramCannotStart(gatewayExe, "Access is denied."))
	w.setState(t, `{"configured":true,"state":"active","mode":"tides"}`)
	time.Sleep(300 * time.Millisecond)
	if w.tips.last() != tipProgramCannotStart(gatewayExe, "Access is denied.") {
		t.Errorf("GWL-STATE-TROUBLE-FIRST: with a trouble, the tray says %q", w.tips.last())
	}
}

// The first start opens the status page, at Settings while the gateway is not set up: a fresh
// install is led to the node and the payout address. Whether it is set up is what decides, not its
// state, which is "starting" for a moment after a start.
func TestTheFirstStartOpensSettingsUntilSetUp(t *testing.T) {
	for _, tc := range []struct {
		code, answer string
		settings     bool
	}{
		{"GWL-OPEN-SETTINGS", `{"configured":false,"state":"unconfigured","mode":"off"}`, true},
		{"GWL-OPEN-SETTINGS-STARTING", `{"configured":false,"state":"starting","mode":"off"}`, true},
		{"GWL-OPEN-STATUS", `{"configured":true,"state":"starting","mode":"starting"}`, false},
		{"GWL-OPEN-STATUS-ACTIVE", `{"configured":true,"state":"active","mode":"tides"}`, false},
	} {
		t.Run(tc.code, func(t *testing.T) {
			w := gatewayWorld(t, "status")
			w.setState(t, tc.answer)
			boot()
			want := statusURL()
			if tc.settings {
				want = settingsURL()
			}
			if got := w.opened.all(); len(got) != 1 || got[0] != want {
				t.Fatalf("%s: the first start opened %q, want %q; log:\n%s", tc.code, got, want, launcherLog())
			}
			if pageToOpen() != want {
				t.Errorf("%s: Open Status Page then opens %q, not %q", tc.code, pageToOpen(), want)
			}
		})
	}
}

// A gateway whose status page never answers: the status page all the same, whose banner leads to
// Settings once it answers.
func TestTheFirstStartOpensTheStatusPageWithNoAnswer(t *testing.T) {
	w := gatewayWorld(t, "eof")
	statusPageWait = time.Second
	boot()
	if got := w.opened.all(); len(got) != 1 || got[0] != statusURL() {
		t.Fatalf("GWL-OPEN-NO-ANSWER: the first start opened %q, want the status page", got)
	}
}

// A start of the gateway alone, after it stopped on its own or by Restart Forge Gateway, opens no
// page; Open Status Page follows what the gateway last said of its settings.
func TestOnlyTheFirstStartOpensAPage(t *testing.T) {
	w := gatewayWorld(t, "status")
	startSupervising(t)
	w.setState(t, `{"configured":false,"state":"unconfigured","mode":"off"}`)
	boot()
	if len(w.opened.all()) != 1 {
		t.Fatalf("setup: the first start opened %q", w.opened.all())
	}
	first := pid(gatewayKey)
	mu.Lock()
	c := procs[gatewayKey]
	mu.Unlock()
	_ = c.Process.Kill()
	if !waitFor(10*time.Second, func() bool { p := pid(gatewayKey); return p != 0 && p != first }) {
		t.Fatalf("setup: the gateway was not started again; log:\n%s", launcherLog())
	}
	restartGateway()
	if !waitFor(10*time.Second, func() bool { return askState(t) }) {
		t.Fatal("setup: the gateway's status page does not answer after the restart")
	}
	time.Sleep(300 * time.Millisecond)
	if got := w.opened.all(); len(got) != 1 {
		t.Errorf("GWL-OPEN-ONCE: starts of the gateway alone opened pages: %q", got)
	}
	w.setState(t, `{"configured":false,"state":"node_login","mode":"off"}`)
	noteAnswerNow(t)
	if pageToOpen() != settingsURL() {
		t.Errorf("GWL-OPEN-MENU: with the gateway not set up, Open Status Page opens %q", pageToOpen())
	}
	w.setState(t, `{"configured":true,"state":"node_login","mode":"off"}`)
	noteAnswerNow(t)
	if pageToOpen() != statusURL() {
		t.Errorf("GWL-OPEN-MENU: with the gateway set up, Open Status Page opens %q", pageToOpen())
	}
}

// noteAnswerNow asks the gateway at once, as the watch does.
func noteAnswerNow(t *testing.T) {
	t.Helper()
	a, known := gatewayStateSaid()
	if !known {
		t.Fatal("setup: the gateway did not answer")
	}
	noteState(a)
}

// A second launch, the shortcut while the sign-in start runs, opens the status page of the copy
// running, at the address its config gives, and starts nothing and writes nothing.
func TestASecondLaunchOpensTheStatusPage(t *testing.T) {
	w := gatewayWorld(t, "eof")
	saved := alreadyRuns
	t.Cleanup(func() { alreadyRuns = saved })
	writeFile(t, dpath(configName), `{"status":{"listen":"127.0.0.1:4090"}}`)
	stratumPort, statusHost, statusPort = "1", "1", "1"
	alreadyRuns = func() bool { return true }
	if !secondLaunch() {
		t.Fatal("GWL-SECOND: a second launch went on to start")
	}
	if got := w.opened.all(); len(got) != 1 || got[0] != "http://127.0.0.1:4090/" {
		t.Errorf("GWL-SECOND: a second launch opened %q, not the status page", got)
	}
	if _, err := os.Stat(dpath("launcher.log")); !os.IsNotExist(err) || w.starts() != 0 || started(gatewayKey) {
		t.Errorf("GWL-SECOND: a second launch wrote launcher.log (%v) or started the gateway (%d)", err, w.starts())
	}
	alreadyRuns = func() bool { return false }
	if secondLaunch() || len(w.opened.all()) != 1 {
		t.Error("GWL-SECOND-FIRST: the first launch was taken for a second")
	}
}
