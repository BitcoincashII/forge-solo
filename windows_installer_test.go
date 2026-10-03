package forgesolo

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The installer and the uninstaller ask for Forge Solo to be closed before touching its files
// (AppMutex), by the name of the mutex the launcher holds while it runs. If the two names drift
// apart, the installer no longer sees Forge Solo running, and its files are closed under it, the
// nodes with them.
func TestInstallerWaitsForTheRunningLauncher(t *testing.T) {
	src, err := os.ReadFile("windows/launcher/instance_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("(?m)^const runningMutex = [\"`]([^\"`]+)[\"`]").FindSubmatch(src)
	if m == nil {
		t.Fatal("windows/launcher/instance_windows.go has no const runningMutex")
	}
	iss, err := os.ReadFile("windows/forge-solo.iss")
	if err != nil {
		t.Fatal(err)
	}
	setup := string(iss)
	if i := strings.Index(setup, "\n[Setup]"); i >= 0 {
		setup = setup[i+len("\n[Setup]"):]
	} else {
		t.Fatal("forge-solo.iss has no [Setup] section")
	}
	if i := strings.Index(setup, "\n["); i >= 0 {
		setup = setup[:i]
	}
	names := regexp.MustCompile(`(?m)^AppMutex=(.+?)\r?$`).FindStringSubmatch(setup)
	if names == nil || !slices.Contains(strings.Split(names[1], ","), string(m[1])) {
		t.Fatalf("APPMUTEX: the installer's [Setup] AppMutex (%v) does not name %s, the mutex the launcher holds", names, m[1])
	}
}

// An update with "Start Forge Solo when I sign in" unticked removes the sign-in start; the
// installer only ever added it, so unticking it changed nothing.
func TestInstallerStartupCanBeTurnedOff(t *testing.T) {
	b, err := os.ReadFile("windows/forge-solo.iss")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^Root: HKCU; Subkey: "Software\\Microsoft\\Windows\\CurrentVersion\\Run"; ValueType: none; ValueName: "ForgeSolo"; Flags: deletevalue; Tasks: not startup\r?$`).Match(b) {
		t.Fatal("STARTUP-OFF: no [Registry] entry deletes the ForgeSolo sign-in start when the startup task is unticked")
	}
}

// Each port reachable from other machines has its firewall rule, letting in only the program that
// listens there, and the uninstaller removes every rule the installer adds. The rental port is
// there as on Umbrel and Linux.
func TestInstallerFirewallRules(t *testing.T) {
	b, err := os.ReadFile("windows/forge-solo.iss")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	adds := regexp.MustCompile(`add rule name="([^"]+)" dir=in action=allow program="' \+ AppDir \+ '\\([^"]+)" protocol=TCP localport=(\d+)`).FindAllStringSubmatch(s, -1)
	got := map[string]string{}
	for _, a := range adds {
		got[a[3]] = a[2]
		uninstall := s[strings.Index(s, "procedure CurUninstallStepChanged"):]
		if !strings.Contains(uninstall, `delete rule name="`+a[1]+`"`) {
			t.Errorf("FIREWALL-UNINSTALL: the uninstaller leaves the rule %q", a[1])
		}
	}
	want := map[string]string{"3333": "stratum.exe", "3335": "stratum.exe", "8339": "bitcoincashIId.exe", "25360": "elevenseventyfived.exe"}
	if len(got) != len(want) {
		t.Errorf("FIREWALL-RULES: rules for %v, want %v", got, want)
	}
	for port, exe := range want {
		if got[port] != exe {
			t.Errorf("FIREWALL-RULES: port %s lets in %q, want %q", port, got[port], exe)
		}
	}
}
