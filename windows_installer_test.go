package forgesolo

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// installerScript is windows/forge-solo.iss.
func installerScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("windows/forge-solo.iss")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// installerSection is the body of the installer script's [name] section, or "" if it has none.
func installerSection(t *testing.T, name string) string {
	t.Helper()
	s := installerScript(t)
	i := strings.Index(s, "\n["+name+"]")
	if i < 0 {
		return ""
	}
	s = s[i+len("\n["+name+"]"):]
	if j := strings.Index(s, "\n["); j >= 0 {
		s = s[:j]
	}
	return s
}

// installerFunc is the installer script's routine declared as decl, up to its closing "end;", or ""
// if there is none.
func installerFunc(t *testing.T, decl string) string {
	t.Helper()
	s := installerScript(t)
	i := strings.Index(s, "\n"+decl)
	if i < 0 {
		return ""
	}
	s = s[i+1:]
	if j := strings.Index(s, "\nend;"); j >= 0 {
		s = s[:j+len("\nend;")]
	}
	return s
}

var pascalToken = regexp.MustCompile(`^(?:'((?:[^']|'')*)'|#\$([0-9A-Fa-f]+)|#([0-9]+)|\s*\+\s*|\s+)`)

// pascalString is the value of a Pascal string expression: quoted literals and #$hex or #decimal
// character codes, written next to each other or joined by +.
func pascalString(expr string) (string, error) {
	var b strings.Builder
	for s := strings.TrimSpace(expr); s != ""; {
		m := pascalToken.FindStringSubmatchIndex(s)
		if m == nil {
			return "", fmt.Errorf("not a Pascal string: %s", expr)
		}
		switch {
		case m[2] >= 0:
			b.WriteString(strings.ReplaceAll(s[m[2]:m[3]], "''", "'"))
		case m[4] >= 0:
			n, _ := strconv.ParseUint(s[m[4]:m[5]], 16, 32)
			b.WriteRune(rune(n))
		case m[6] >= 0:
			n, _ := strconv.ParseUint(s[m[6]:m[7]], 10, 32)
			b.WriteRune(rune(n))
		}
		s = s[m[1]:]
	}
	return b.String(), nil
}

// psSingleQuote is whether PowerShell takes r for a single quote: the apostrophe, and the
// typographic quotes U+2018 to U+201B.
func psSingleQuote(r rune) bool {
	return r == '\'' || r >= '\u2018' && r <= '\u201b'
}

// psSingleQuoted reads the single-quoted string at the start of s as PowerShell's tokenizer does:
// it ends at a single quote that is not doubled, and a doubled one stands for its second character.
// It returns the string's value and what follows it.
func psSingleQuoted(s string) (value, rest string, ok bool) {
	r := []rune(s)
	if len(r) == 0 || !psSingleQuote(r[0]) {
		return "", s, false
	}
	var b strings.Builder
	for i := 1; i < len(r); i++ {
		if psSingleQuote(r[i]) {
			if i+1 < len(r) && psSingleQuote(r[i+1]) {
				b.WriteRune(r[i+1])
				i++
				continue
			}
			return b.String(), string(r[i+1:]), true
		}
		b.WriteRune(r[i])
	}
	return "", "", false
}

// PowerShell ends a single-quoted string at any of five characters: the apostrophe and the
// typographic quotes U+2018 to U+201B. PSQuote doubled only the apostrophe, so for a profile such
// as C:\Users\O’Brien the Defender command broke at the ’ and failed: no exclusion was added at
// install, none was removed at uninstall, and nothing said so. Each must stay inside the string.
func TestInstallerPSQuoteKeepsEveryPathWhole(t *testing.T) {
	body := installerFunc(t, "function PSQuote(S: String): String;")
	if !strings.Contains(body, "Result := '''' + S + '''';") {
		t.Fatalf("PSQUOTE-SHAPE: PSQuote no longer puts S between apostrophes:\n%s", body)
	}
	var change [][2]string
	for _, m := range regexp.MustCompile(`StringChangeEx\(S, ([^,]+), ([^,]+), True\);`).FindAllStringSubmatch(body, -1) {
		from, err := pascalString(m[1])
		if err != nil {
			t.Fatal(err)
		}
		to, err := pascalString(m[2])
		if err != nil {
			t.Fatal(err)
		}
		change = append(change, [2]string{from, to})
	}
	quote := func(s string) string {
		for _, c := range change {
			s = strings.ReplaceAll(s, c[0], c[1])
		}
		return "'" + s + "'"
	}
	for _, c := range []struct{ code, name string }{
		{"PSQUOTE-0027", "O'Brien"},
		{"PSQUOTE-2018", "D\u2018Arcy"},
		{"PSQUOTE-2019", "O\u2019Brien"},
		{"PSQUOTE-201A", "X\u201aY"},
		{"PSQUOTE-201B", "X\u201bY"},
	} {
		path := `C:\Users\` + c.name + `\AppData\Roaming\ForgeSolo\pgdata`
		q := quote(path)
		if got, rest, ok := psSingleQuoted(q + ", 'next'"); !ok || got != path || rest != ", 'next'" {
			t.Errorf("%s: PowerShell reads %s as the string %q, followed by %q", c.code, q, got, rest)
		}
	}
}

// Every program the installer puts in place is 64-bit (x64). It also installed on 32-bit Windows
// and on Windows 10 on ARM, which run no x64 programs, and Forge Solo could not start there. There
// it now refuses, and says why; Windows 11 on ARM, which runs x64 programs, is still allowed.
func TestInstallerOnlyWhereForgeSoloRuns(t *testing.T) {
	if !regexp.MustCompile(`(?m)^ArchitecturesAllowed=x64compatible\r?$`).MatchString(installerSection(t, "Setup")) {
		t.Error("ARCH-ALLOWED: [Setup] does not keep the installer to Windows that runs x64 programs (ArchitecturesAllowed=x64compatible)")
	}
	m := regexp.MustCompile(`(?m)^WindowsVersionNotSupported=(.*?)\r?$`).FindStringSubmatch(installerSection(t, "Messages"))
	if m == nil || !strings.Contains(m[1], "64-bit Windows") {
		t.Errorf("ARCH-MESSAGE: the refusal does not say that Forge Solo needs 64-bit Windows: %q", m)
	}
}

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
// there as on Umbrel and Linux. The rules are this Windows account's: another account's install of
// Forge Solo keeps its own, and the names earlier releases used are removed.
func TestInstallerFirewallRules(t *testing.T) {
	b, err := os.ReadFile("windows/forge-solo.iss")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	install := s[strings.Index(s, "procedure CurStepChanged"):strings.Index(s, "procedure CurUninstallStepChanged")]
	uninstall := s[strings.Index(s, "procedure CurUninstallStepChanged"):]
	got := map[string]string{}
	for _, r := range regexp.MustCompile(`FirewallRule\('([^']+)', '([^']+)', '(\d+)', '([^']+)'\)`).FindAllStringSubmatch(install, -1) {
		got[r[3]] = r[2]
		if !strings.Contains(uninstall, "FirewallRemove('"+r[1]+"')") {
			t.Errorf("FIREWALL-UNINSTALL: the uninstaller leaves the rule %q", r[1])
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
	body := func(name string) string {
		i := strings.Index(s, name)
		if i < 0 {
			t.Fatalf("forge-solo.iss has no %s", name)
		}
		return s[i : i+strings.Index(s[i:], "\nend;")]
	}
	if !strings.Contains(body("function RuleName"), "Base + ' for ' + ExpandConstant('{username}')") {
		t.Error("FIREWALL-PER-ACCOUNT: a rule's name is not this Windows account's")
	}
	for _, must := range []string{
		`'netsh advfirewall firewall delete rule name="' + Base + '" >nul 2>&1 & '`,
		`'netsh advfirewall firewall delete rule name="' + RuleName(Base) + '" >nul 2>&1 & '`,
		`'netsh advfirewall firewall add rule name="' + RuleName(Base) + '" dir=in action=allow program="' +`,
	} {
		if !strings.Contains(body("function FirewallRule"), must) {
			t.Errorf("FIREWALL-RULE-PUT: FirewallRule lacks %s", must)
		}
	}
	for _, must := range []string{`delete rule name="' + Base + '" & '`, `delete rule name="' + RuleName(Base) + '" & '`} {
		if !strings.Contains(body("function FirewallRemove"), must) {
			t.Errorf("FIREWALL-RULE-REMOVE: FirewallRemove lacks %s", must)
		}
	}
}
