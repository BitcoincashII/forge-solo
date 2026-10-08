package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// signInArgs is what Windows passes the tray app at sign-in: the arguments of the Run value the
// installer writes (windows/gateway/forge-gateway.iss), after the tray app's path.
func signInArgs(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "forge-gateway.iss"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`ValueName: "ForgeGateway"; ValueData: "((?:[^"]|"")*)";`).FindSubmatch(b)
	if m == nil {
		t.Fatal("setup: the installer writes no Run value named ForgeGateway")
	}
	cmd := strings.ReplaceAll(string(m[1]), `""`, `"`)
	const exe = `"{app}\{#MyAppExe}"`
	if !strings.HasPrefix(cmd, exe) {
		t.Fatalf("setup: the Run value is %s, not the tray app's path first", cmd)
	}
	return strings.Fields(strings.TrimPrefix(cmd, exe))
}

// The first run opens the status page once. On the Windows 11 test PC two tabs opened: Setup's
// "Launch Forge Gateway now" started the tray, which opened Settings, and Windows then ran the
// sign-in start Setup had just written (it runs the Run values a while after sign-in), whose second
// copy found Forge Gateway running and opened the status page again. The sign-in start now says it
// is one, and a second copy started so opens nothing.
func TestTheFirstRunOpensOnePage(t *testing.T) {
	w := gatewayWorld(t, "status")
	w.setState(t, `{"configured":false,"state":"unconfigured","mode":"off"}`)
	saved := alreadyRuns
	t.Cleanup(func() { alreadyRuns = saved })
	boot() // Setup's launch
	// The second copy is another process, which shares nothing with the first: here the first
	// copy's watch of the gateway's state ends before the second reads the config's ports.
	mu.Lock()
	stopping = true
	mu.Unlock()
	stateWatch.Wait()
	alreadyRuns = func() bool { return true }
	args := signInArgs(t)
	if !secondLaunch(args) { // Windows' sign-in start
		t.Fatal("setup: the sign-in start went on to start")
	}
	if got := w.opened.all(); len(got) != 1 || got[0] != settingsURL() {
		t.Fatalf("GWL-SIGNIN-ONCE: the first run opened %q (the sign-in start's arguments: %q)", got, args)
	}
	// A launch someone asked for, the shortcut or the Start menu, still opens the page.
	if !secondLaunch(nil) || len(w.opened.all()) != 2 {
		t.Errorf("GWL-SIGNIN-SHORTCUT: a second launch from the shortcut opened %q", w.opened.all())
	}
}

// The sign-in start is told by its argument, and only by it.
func TestTheSignInStartIsToldByItsArgument(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{signInArg}, true},
		{nil, false},
		{[]string{"-version"}, false},
		{[]string{"--at-sign-in-x"}, false},
	} {
		if got := launchedAtSignIn(tc.args); got != tc.want {
			t.Errorf("GWL-SIGNIN-ARG: %q is taken for the sign-in start: %v", tc.args, got)
		}
	}
}
