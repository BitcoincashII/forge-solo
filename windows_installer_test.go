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

// installerFunc is the installer script's routine declared as decl, up to its closing "end;",
// without comments, or "" if there is none.
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
	return pascalCode(s)
}

// pascalCode is Pascal source without its comments (// to the end of the line, and { }) and blank
// lines, so that a statement commented out does not count. String literals are kept as they are.
func pascalCode(s string) string {
	var lines []string
	for _, l := range strings.Split(pascalUncommented(s), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

func pascalUncommented(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\'':
			j := i + 1
			for j < len(s) && (s[j] != '\'' || j+1 < len(s) && s[j+1] == '\'') {
				if s[j] == '\'' {
					j++
				}
				j++
			}
			b.WriteString(s[i:min(j+1, len(s))])
			i = j
		case strings.HasPrefix(s[i:], "//"):
			for i+1 < len(s) && s[i+1] != '\n' {
				i++
			}
		case s[i] == '{':
			for i < len(s) && s[i] != '}' {
				i++
			}
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
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

// pascalLiterals is the string literals in Pascal code, joined: the text of a message built with +.
func pascalLiterals(code string) string {
	var b strings.Builder
	for _, m := range regexp.MustCompile(`'((?:[^']|'')*)'`).FindAllStringSubmatch(code, -1) {
		b.WriteString(strings.ReplaceAll(m[1], "''", "'"))
	}
	return b.String()
}

// Run as administrator, Setup can run as another account than the one signed in (an
// administrator's, over the shoulder, or the separate account Administrator Protection elevates
// to), and it installs for the account it runs as: Forge Solo went to that account's profile and
// Start menu without a word. Its first page now says which account, and how to install for your
// own instead. It does not refuse: with UAC off, or for the built-in Administrator, an elevated
// Setup is the account's own.
func TestInstallerSaysWhichAccountWhenElevated(t *testing.T) {
	body := installerFunc(t, "procedure InitializeWizard;")
	page := regexp.MustCompile(`(?s)\n  if IsAdmin\(\) then\n  begin\n(.*?)\n  end;`).FindStringSubmatch(body)
	if page == nil {
		t.Fatalf("ELEVATED-ONLY: InitializeWizard does not add a page when, and only when, Setup runs as administrator:\n%s", body)
	}
	if !strings.Contains(page[1], "CreateOutputMsgPage(wpWelcome,") {
		t.Error("ELEVATED-FIRST: the account page is not Setup's first page (CreateOutputMsgPage(wpWelcome, ...))")
	}
	if !strings.Contains(page[1], "Account := ExpandConstant('{username}')") || !strings.Contains(page[1], "' +\n      Account + '") {
		t.Error("ELEVATED-ACCOUNT: the page does not name the account Setup installs for")
	}
	if text := pascalLiterals(page[1]); !strings.Contains(text, `click Cancel, then run Setup again without "Run as administrator"`) {
		t.Errorf("ELEVATED-REMEDY: the page does not say how to install for your own account: %q", text)
	}
	if !regexp.MustCompile(`(?m)^\s*Log\('Setup is running as administrator, for the account ' \+ Account\);`).MatchString(page[1]) {
		t.Error("ELEVATED-LOG: a silent install does not log which account it installs for")
	}
	if strings.Contains(body, "Abort") || strings.Contains(installerFunc(t, "function InitializeSetup"), "Result := False") {
		t.Error("ELEVATED-NO-REFUSAL: Setup refuses to run as administrator; with UAC off that locks the account out")
	}
}

// The installer's one elevated step shows Windows' prompt, which names Windows Command Processor,
// not Forge Solo, and nothing said what it was for. Refused or failed, the step left no firewall
// rules, and Setup finished without a word: miners on the network could not connect and nothing
// said why. The Ready page and the uninstaller's question now say what the prompt is for.
// Afterwards the rules themselves are checked, and what is missing is logged and said, with what
// it means and how to put it right, in a box an install run with /SUPPRESSMSGBOXES goes past.
func TestInstallerSaysWhenTheElevatedStepFails(t *testing.T) {
	memo := installerFunc(t, "function UpdateReadyMemo(")
	if text := pascalLiterals(memo); !regexp.MustCompile(`\n  if not IsAdmin\(\) then\n    Result := Result \+ 'Permission:'`).MatchString(memo) ||
		!strings.Contains(text, "Windows will ask whether Windows Command Processor may make") || !strings.Contains(text, "Choose Yes") {
		t.Errorf("UAC-WARN-INSTALL: the Ready page does not say what Windows' prompt is for:\n%s", memo)
	}
	m := regexp.MustCompile(`(?m)^ConfirmUninstall=(.*?)\r?$`).FindStringSubmatch(installerSection(t, "Messages"))
	if m == nil || !strings.Contains(m[1], "%1") || !strings.Contains(m[1], "Windows Command Processor") || !strings.Contains(m[1], "choose Yes") {
		t.Errorf("UAC-WARN-UNINSTALL: the uninstaller's question does not say what Windows' prompt is for: %q", m)
	}

	elevated := installerFunc(t, "function Elevated(Cmd: String): Boolean;")
	if !strings.Contains(elevated, "\n  Result := ShellExec('runas', ExpandConstant('{cmd}'), Cmd, '', SW_HIDE, ewWaitUntilTerminated, ResultCode);\n  if Result then") {
		t.Errorf("UAC-RESULT: Elevated does not return whether the elevated step ran:\n%s", elevated)
	}
	if !regexp.MustCompile(`(?m)^\s+Log\('The firewall and Defender step did not run: ' \+ SysErrorMessage\(ResultCode\)\);`).MatchString(elevated) {
		t.Error("UAC-LOG: a step that did not run is not logged, with why")
	}
	if !strings.Contains(installerFunc(t, "function RuleInPlace(Name: String): Boolean;"),
		`Result := Exec(ExpandConstant('{sys}\netsh.exe'), 'advfirewall firewall show rule name="' + Name + '"',`+"\n    '', SW_HIDE, ewWaitUntilTerminated, ResultCode) and (ResultCode = 0);") {
		t.Error("UAC-CHECK-HOW: RuleInPlace does not ask netsh whether the rule is there (exit code 0)")
	}
	var checked, put []string
	for _, r := range regexp.MustCompile(`RuleInPlace\(RuleName\('([^']+)'`).FindAllStringSubmatch(installerFunc(t, "function RulesInPlace: Integer;"), -1) {
		checked = append(checked, r[1])
	}
	for _, r := range regexp.MustCompile(`FirewallRule\('([^']+)'`).FindAllStringSubmatch(installerFunc(t, "procedure CurStepChanged(CurStep: TSetupStep);"), -1) {
		put = append(put, r[1])
	}
	count := regexp.MustCompile(`(?m)^  RuleCount = (\d+);`).FindStringSubmatch(installerSection(t, "Code"))
	slices.Sort(checked)
	slices.Sort(put)
	if !slices.Equal(checked, put) || count == nil || count[1] != strconv.Itoa(len(put)) {
		t.Errorf("UAC-CHECK-RULES: the check looks for %v (RuleCount %v), the install puts %v in place", checked, count, put)
	}

	install := installerFunc(t, "procedure CurStepChanged(CurStep: TSetupStep);")
	if !strings.Contains(install, "\n    Ran := Elevated(Cmd);\n") || strings.Contains(install, "ShellExec(") {
		t.Error("UAC-RESULT-INSTALL: the install does not keep whether its elevated step ran")
	}
	if !regexp.MustCompile(`\n    Ran := Elevated\(Cmd\);\n\s+InPlace := RulesInPlace;\n`).MatchString(install) {
		t.Error("UAC-CHECK-INSTALL: the install does not check its rules after the elevated step")
	}
	tell := regexp.MustCompile(`(?s)\n    if InPlace < RuleCount then\n    begin\n(.*?)\n    end;`).FindStringSubmatch(install)
	if tell == nil || !strings.Contains(tell[1], "SuppressibleMsgBox(Missing + ") || !strings.Contains(tell[1], "changes to your device.', mbError, MB_OK, IDOK);") {
		t.Fatalf("UAC-TELL-INSTALL: a missing rule is not said, in a box with one button:\n%s", install)
	}
	for _, want := range []string{
		"Forge Solo is installed, but Setup could not add all of its firewall rules.",
		"Forge Solo is installed, but Windows did not let Setup add its firewall rules and Defender exclusions.",
		"Until the rules are added, miners on other devices on your network cannot connect to this PC.",
		"To add them, run this installer again and choose Yes when Windows asks",
	} {
		if !strings.Contains(pascalLiterals(tell[1]), want) {
			t.Errorf("UAC-TELL-INSTALL-TEXT: the message does not say %q", want)
		}
	}

	uninstall := installerFunc(t, "procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);")
	if !strings.Contains(uninstall, "\n    Ran := Elevated(Cmd);\n") || strings.Contains(uninstall, "ShellExec(") {
		t.Error("UAC-RESULT-UNINSTALL: the uninstall does not keep whether its elevated step ran")
	}
	if !regexp.MustCompile(`\n    Ran := Elevated\(Cmd\);\n\s+InPlace := RulesInPlace;\n`).MatchString(uninstall) {
		t.Error("UAC-CHECK-UNINSTALL: the uninstall does not check for rules left after the elevated step")
	}
	left := regexp.MustCompile(`(?s)\n    if not Ran or \(InPlace > 0\) then\n    begin\n(.*?)\n    end;`).FindStringSubmatch(uninstall)
	if left == nil {
		t.Fatalf("UAC-TELL-UNINSTALL: what the uninstaller leaves is not said:\n%s", uninstall)
	}
	for _, want := range []string{
		"Forge Solo is removed, but the uninstaller could not remove all of its firewall rules.",
		"Forge Solo is removed, but Windows did not let the uninstaller remove its firewall rules and Defender exclusions.",
		"Inbound Rules: delete the rules named",
		"Add or remove exclusions: remove the folders in ",
	} {
		if !strings.Contains(pascalLiterals(left[1]), want) {
			t.Errorf("UAC-TELL-UNINSTALL-TEXT: the message does not say %q", want)
		}
	}
	if !strings.Contains(uninstall, "\n  if CurUninstallStep = usPostUninstall then\n  begin\n    if LeftBehind <> '' then\n      SuppressibleMsgBox(LeftBehind, mbError, MB_OK, IDOK);") {
		t.Error("UAC-TELL-UNINSTALL-SHOWN: what the uninstaller leaves is not shown once it has finished, in a box with one button")
	}

	// Each value the checks above look at is set only where they look: set again after that, the
	// installer would say nothing while the checks still passed.
	for _, c := range []struct {
		code, name, in string
		n              int
	}{
		{"UAC-ONCE-RESULT", "Result", elevated, 1},
		{"UAC-ONCE-RAN-INSTALL", "Ran", install, 1},
		{"UAC-ONCE-INPLACE-INSTALL", "InPlace", install, 1},
		{"UAC-ONCE-MISSING", "Missing", install, 2},
		{"UAC-ONCE-RAN-UNINSTALL", "Ran", uninstall, 1},
		{"UAC-ONCE-INPLACE-UNINSTALL", "InPlace", uninstall, 1},
		{"UAC-ONCE-LEFTBEHIND", "LeftBehind", pascalCode(installerSection(t, "Code")), 4},
	} {
		if n := pascalAssignments(c.in, c.name); n != c.n {
			t.Errorf("%s: %s is set %d times, want %d", c.code, c.name, n, c.n)
		}
	}
}

// pascalAssignments is how many times Pascal code assigns to the variable name.
func pascalAssignments(code, name string) int {
	return len(regexp.MustCompile(`\b`+regexp.QuoteMeta(name)+`\s*:=`).FindAllStringIndex(code, -1))
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
