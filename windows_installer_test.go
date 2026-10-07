package forgesolo

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
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

// The firewall rules carry the account's name. The uninstaller named them from the account's name
// at uninstall, so after the account was renamed it removed nothing and left the rules behind. The
// installer now keeps the name its rules carry, once they are in place; the uninstaller removes the
// rules of that name, and an update after a rename removes the rules of the old name.
func TestInstallerRemovesTheRulesItAdded(t *testing.T) {
	install := installerFunc(t, "procedure CurStepChanged(CurStep: TSetupStep);")
	read := strings.Index(install, "\n    PreviousRulesAccount := KeptRulesAccount;\n")
	if read < 0 || read > strings.Index(install, "Cmd := ") {
		t.Error("RULES-PREVIOUS-READ: the install does not read the name an earlier install kept before it builds its commands")
	}
	if !strings.Contains(installerFunc(t, "function FirewallRule(Base, Exe, Port, Profile: String): String;"),
		"\n  if (PreviousRulesAccount <> '') and (PreviousRulesAccount <> RulesAccount) then\n    Result := Result + 'netsh advfirewall firewall delete rule name=\"' + RuleName(Base, PreviousRulesAccount) + '\" >nul 2>&1 & ';\n") {
		t.Error("RULES-PREVIOUS-DELETE: an update after a rename leaves the rules of the old name")
	}
	if !strings.Contains(install, "\n    if InPlace = RuleCount then\n      RegWriteStringValue(HKCU, RulesKey, RulesValue, RulesAccount);\n") {
		t.Error("RULES-KEEP: the install does not keep the name its rules carry, once they are in place")
	}
	if n := len(regexp.MustCompile(`RuleInPlace\(RuleName\('[^']+', RulesAccount\)\)`).FindAllString(installerFunc(t, "function RulesInPlace: Integer;"), -1)); n != 4 {
		t.Errorf("RULES-CHECKED-NAME: %d of the 4 rules are checked under the name they carry", n)
	}
	uninstall := installerFunc(t, "procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);")
	kept := strings.Index(uninstall, "\n    RulesAccount := KeptRulesAccount;\n    if RulesAccount = '' then\n      RulesAccount := ExpandConstant('{username}');\n")
	if kept < 0 || kept > strings.Index(uninstall, "Cmd := ") {
		t.Error("RULES-UNINSTALL-KEPT: the uninstaller does not remove the rules of the name the install kept")
	}
	if !strings.Contains(uninstall, "'rules named \"Forge Solo ... for ' + RulesAccount + '\".';") {
		t.Error("RULES-SAID: the uninstaller's message does not name the rules it left by the name they carry")
	}
	if !strings.Contains(uninstall, "\n    RegDeleteValue(HKCU, RulesKey, RulesValue);\n    RegDeleteKeyIfEmpty(HKCU, RulesKey);\n") {
		t.Error("RULES-FORGET: the uninstaller leaves the kept name behind")
	}
}

// The account name kept for the firewall rules goes into the commands of the elevated step, and any
// program running as the user can change it. Read back as it was, a name with a double quote ended
// the rule's name in those commands, and what followed ran as administrator at the next update or
// uninstall the user said Yes to. A kept name is now used only if it cannot change the commands,
// and nothing else reads it; without one, the uninstaller uses the account's own name.
func TestInstallerUsesAKeptAccountNameOnlyIfSafe(t *testing.T) {
	usable := accountNameCheck(installerFunc(t, "function UsableAccountName(Name: String): Boolean;"))
	if usable == nil {
		t.Error("RULES-KEPT-SHAPE: the installer has no UsableAccountName in the form this test reads; taken as one that lets every name through")
		usable = func(string) bool { return true }
	}
	for _, c := range []struct {
		code  string
		names []string
	}{
		{"RULES-KEPT-QUOTE", []string{`x" & echo INJECTED>> C:\fwstub\pwned.txt & echo "`}},
		{"RULES-KEPT-PERCENT", []string{"x%COMSPEC%y"}},
		{"RULES-KEPT-CONTROL", []string{"x\x00y", "x\ty", "x\ny", "x\ry", "x\x1fy"}},
		{"RULES-KEPT-LENGTH", []string{strings.Repeat("a", 257)}},
	} {
		for _, name := range c.names {
			if usable(name) {
				t.Errorf("%s: the kept name %.60q (%d characters) goes into the elevated step's commands", c.code, name, len(name))
			}
		}
	}
	for _, name := range []string{"xclient", "O'Brien", "O\u2019Brien", "Zoë", "张伟", "Ann (Home)", "A&B", "x^y!z", strings.Repeat("a", 256)} {
		if !usable(name) {
			t.Errorf("RULES-KEPT-NAMES: the account name %.60q (%d characters) is not used", name, len(name))
		}
	}

	kept := installerFunc(t, "function KeptRulesAccount: String;")
	if !strings.Contains(kept, "\n  Result := '';\n  if RegQueryStringValue(HKCU, RulesKey, RulesValue, Name) then\n  begin\n    if UsableAccountName(Name) then\n      Result := Name\n    else\n      Log(") ||
		pascalAssignments(kept, "Result") != 2 {
		t.Errorf("RULES-KEPT-CHECKED: KeptRulesAccount does not return the kept name only when UsableAccountName lets it through:\n%s", kept)
	}
	code := pascalCode(installerSection(t, "Code"))
	read := regexp.MustCompile(`RegQueryStringValue\(`)
	if n, in := len(read.FindAllString(code, -1)), len(read.FindAllString(kept, -1)); n != 1 || in != 1 {
		t.Errorf("RULES-KEPT-ONLY: the installer reads the registry %d times, %d of them in KeptRulesAccount; a kept name read elsewhere goes unchecked", n, in)
	}
	install := installerFunc(t, "procedure CurStepChanged(CurStep: TSetupStep);")
	uninstall := installerFunc(t, "procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);")
	for _, c := range []struct {
		code, name, in string
		n              int
	}{
		{"RULES-ONCE-PREVIOUS", "PreviousRulesAccount", code, 1},
		{"RULES-ONCE-ACCOUNT", "RulesAccount", code, 3},
		{"RULES-ONCE-ACCOUNT-INSTALL", "RulesAccount", install, 1},
		{"RULES-ONCE-ACCOUNT-UNINSTALL", "RulesAccount", uninstall, 2},
	} {
		if n := pascalAssignments(c.in, c.name); n != c.n {
			t.Errorf("%s: %s is set %d times, want %d", c.code, c.name, n, c.n)
		}
	}
}

// accountNameCheck is the installer's UsableAccountName (its code as installerFunc gives it) as a Go
// function, or nil if it is not in the one form this reads: a length limit, then a loop that sets
// Result to False for any character that one of the conditions (Ord(Name[I]) < n) or
// (Name[I] = c) matches. As in Pascal Script, it counts and compares UTF-16 code units.
func accountNameCheck(body string) func(string) bool {
	m := regexp.MustCompile(`^function UsableAccountName\(Name: String\): Boolean;\nvar I: Integer;\nbegin\n  Result := Length\(Name\) <= (\d+);\n  for I := 1 to Length\(Name\) do\n    if (.+) then\n      Result := False;\nend;$`).FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	limit, _ := strconv.Atoi(m[1])
	var below []int
	var equal []uint16
	for _, c := range strings.Split(m[2], " or ") {
		if r := regexp.MustCompile(`^\(Ord\(Name\[I\]\) < (\d+)\)$`).FindStringSubmatch(c); r != nil {
			n, _ := strconv.Atoi(r[1])
			below = append(below, n)
			continue
		}
		r := regexp.MustCompile(`^\(Name\[I\] = (.+)\)$`).FindStringSubmatch(c)
		if r == nil {
			return nil
		}
		s, err := pascalString(r[1])
		u := utf16.Encode([]rune(s))
		if err != nil || len(u) != 1 {
			return nil
		}
		equal = append(equal, u[0])
	}
	return func(name string) bool {
		u := utf16.Encode([]rune(name))
		ok := len(u) <= limit
		for _, ch := range u {
			for _, n := range below {
				if int(ch) < n {
					ok = false
				}
			}
			if slices.Contains(equal, ch) {
				ok = false
			}
		}
		return ok
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

// Once this account's Forge Solo has stopped, the installer and the uninstaller check the mutex the
// launcher holds while it runs (RunningMutex): one still held is Forge Solo running for another
// Windows account, and they stop rather than replace or remove anything. If the two names drift
// apart, that check finds nothing.
func TestInstallerKnowsTheLaunchersMutex(t *testing.T) {
	src, err := os.ReadFile("windows/launcher/instance_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("(?m)^const runningMutex = [\"`]([^\"`]+)[\"`]").FindSubmatch(src)
	if m == nil {
		t.Fatal("windows/launcher/instance_windows.go has no const runningMutex")
	}
	name := regexp.MustCompile(`(?m)^  RunningMutex = '([^']*)';$`).FindStringSubmatch(pascalCode(installerSection(t, "Code")))
	if name == nil || name[1] != string(m[1]) {
		t.Fatalf("RUNNING-MUTEX: the installer's RunningMutex (%v) is not %s, the mutex the launcher holds", name, m[1])
	}
}

// Forge Solo running for another Windows account holds its mutex with that account's security,
// which does not let this account open it, and Inno Setup's CheckForMutexes (as AppMutex) takes a
// mutex it cannot open for none: the installer and the uninstaller went on. The mutex is now opened
// with OpenMutexW, and Windows refusing access counts as held.
func TestInstallerSeesAnotherAccountsMutex(t *testing.T) {
	code := pascalCode(installerSection(t, "Code"))
	held := installerFunc(t, "function MutexHeld(Name: String): Boolean;")
	runs := installerFunc(t, "function ForgeSoloRuns: Boolean;")
	if !strings.Contains(code, "function OpenMutex(Access: Cardinal; Inherit: Bool; Name: String): Cardinal;\n  external 'OpenMutexW@kernel32.dll stdcall';") ||
		!strings.Contains(code, "\n  SYNCHRONIZE_ACCESS = $00100000;\n") || !strings.Contains(code, "\n  ERROR_ACCESS_DENIED = 5;\n") ||
		!strings.Contains(held, `
begin
  Mutex := OpenMutex(SYNCHRONIZE_ACCESS, False, Name);
  Result := Mutex <> 0;
  if Result then
    CloseHandle(Mutex)
  else
    Result := DLLGetLastError = ERROR_ACCESS_DENIED;
end;`) || !strings.Contains(runs, "\nbegin\n  Result := MutexHeld(RunningMutex);\nend;") {
		t.Errorf("CLOSE-OTHER-DENIED: a mutex this account may not open (another account's) is not taken for one that is held:\n%s\n%s", held, runs)
	}
	// Only the launcher's mutex is looked for: no released build held a name of its own.
	if strings.Count(code, "ForgeSoloRunning") != 1 || !strings.Contains(code, "\n  RunningMutex = 'Global\\ForgeSoloRunning';\n") {
		t.Errorf("CLOSE-OTHER-ONE-NAME: [Code] names ForgeSoloRunning %d times, not once as RunningMutex = 'Global\\ForgeSoloRunning'", strings.Count(code, "ForgeSoloRunning"))
	}
}

// Inno Setup's own check of the launcher's mutex (AppMutex) came at start, before anything in
// [Code]. With Forge Solo 1.0.13 running, Setup and the uninstaller asked for it to be closed, and a
// silent update or uninstall ended at once with exit code 1, having changed nothing. Forge Solo
// running for this account is now closed first, as 1.0.12 is: in PrepareToInstall, and before the
// uninstaller removes anything. Only then is the mutex checked: one still held is Forge Solo running
// for another Windows account, which they must not close, and they stop, with Retry or Cancel, and
// Cancel in a silent run.
func TestInstallerClosesForgeSoloBeforeItChecksTheMutex(t *testing.T) {
	if regexp.MustCompile(`(?mi)^\s*AppMutex\s*=`).MatchString(installerSection(t, "Setup")) {
		t.Error("CLOSE-FIRST-NO-APPMUTEX: [Setup] has AppMutex: Setup checks it at start, before Forge Solo is closed, and a silent update or uninstall of a running Forge Solo ends there")
	}
	code := pascalCode(installerSection(t, "Code"))
	other := installerFunc(t, "function NoOtherForgeSolo: Boolean;")
	if strings.Contains(code, "CheckForMutexes(") || strings.Count(code, "ForgeSoloRuns") != 3 || strings.Count(other, "Result := not ForgeSoloRuns;") != 2 ||
		strings.Count(code, "NoOtherForgeSolo") != 3 || regexp.MustCompile(`(?i)\b(function InitializeSetup|function InitializeUninstall)\b`).MatchString(code) {
		t.Errorf("CLOSE-FIRST-NO-EARLY-CHECK: the mutex is checked elsewhere than in NoOtherForgeSolo, or NoOtherForgeSolo is called elsewhere than after StopForgeSolo, or Setup or the uninstaller can refuse at start")
	}

	prepare := installerFunc(t, "function PrepareToInstall(var NeedsRestart: Boolean): String;")
	if !strings.Contains(prepare, "\n  if not StopForgeSolo then\n    Result := 'Forge Solo did not stop, so Setup changed nothing. Run Setup again once it has stopped.'\n  else if not NoOtherForgeSolo then\n    Result := '") ||
		!strings.Contains(pascalLiterals(prepare), "Forge Solo runs for another Windows account on this PC, so Setup changed nothing.") {
		t.Errorf("CLOSE-FIRST-SETUP: Setup does not close this account's Forge Solo first, then stop for one running for another account:\n%s", prepare)
	}
	uninstall := installerFunc(t, "procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);")
	if !strings.Contains(uninstall, `
  if CurUninstallStep = usUninstall then
  begin
    if not StopForgeSolo then
    begin
      Log('Forge Solo did not stop: the uninstall ends, and nothing is removed');
      Abort;
    end;
    if not NoOtherForgeSolo then
    begin
      Log('Forge Solo runs for another Windows account: the uninstall ends, and nothing is removed');
      Abort;
    end;
    DataDir := ExpandConstant('{userappdata}\ForgeSolo');
`) {
		t.Errorf("CLOSE-FIRST-UNINSTALL: the uninstaller does not close this account's Forge Solo first, then stop for one running for another account, before it removes anything:\n%s", uninstall)
	}

	// The mutex is looked at again for a few seconds, then the user is told, with Retry and Cancel.
	if !strings.Contains(other, `
begin
  repeat
    Result := not ForgeSoloRuns;
    Seconds := 0;
    while not Result and (Seconds < 3) do
    begin
      Waiting;
      Seconds := Seconds + 1;
      Result := not ForgeSoloRuns;
    end;
    if Result then
      exit;
    Log('Forge Solo runs on this PC for another Windows account: it is not closed');
  until SuppressibleMsgBox('Forge Solo is running for another Windows account on this PC, and only ' +
    'that account can close it.' + #13#10#13#10 +
    'Quit it there (right-click its tray icon, then Quit Forge Solo) and choose Retry. Cancel ' +
    'changes nothing.', mbError, MB_RETRYCANCEL, IDCANCEL) <> IDRETRY;
end;`) {
		t.Errorf("CLOSE-OTHER-ACCOUNT: NoOtherForgeSolo does not wait a few seconds for the mutex to go, then say Forge Solo runs for another account, with Retry and Cancel:\n%s", other)
	}

	// A run with message boxes suppressed (/SUPPRESSMSGBOXES) takes each question's default: Cancel,
	// so that it ends rather than asks again for ever. A MsgBox would wait for an answer even then.
	for _, m := range regexp.MustCompile(`(?s)\bSuppressibleMsgBox\(.*?\b(MB_[A-Z]+(?: or MB_[A-Z0-9]+)?), (ID[A-Z]+)\)`).FindAllStringSubmatch(code, -1) {
		if strings.HasPrefix(m[1], "MB_RETRYCANCEL") && m[2] != "IDCANCEL" {
			t.Errorf("CLOSE-SILENT-CANCELS: a Retry/Cancel question answers %s when message boxes are suppressed, not Cancel", m[2])
		}
	}
	if regexp.MustCompile(`(^|[^A-Za-z])MsgBox\(`).MatchString(code) {
		t.Error("CLOSE-SILENT-CANCELS: [Code] has a MsgBox, which waits for an answer also when message boxes are suppressed")
	}

	win, notes := flat(string(mustRead(t, "windows/README.md"))), flat(releaseSection(t, "1.0.13"))
	if strings.Contains(win, "ask, when they start, for Forge Solo to be closed") || strings.Contains(notes, "ask you to close Forge Solo when they start") ||
		!strings.Contains(win, "Only then does it check the mutex Forge Solo 1.0.13 and later hold while they run: one still held is Forge Solo running for another Windows account") ||
		!strings.Contains(notes, "updating or uninstalling while Forge Solo runs closes it first, as its Quit does") ||
		!strings.Contains(notes, "If Forge Solo 1.0.13 or later runs for another Windows account on the PC, the installer and the uninstaller say so and change nothing.") {
		t.Error("CLOSE-FIRST-DOCS: windows/README.md or RELEASE_NOTES.md ## 1.0.13 does not say that a running Forge Solo is closed first, and that only Forge Solo 1.0.13 or later running for another account stops the installer")
	}
}

// Forge Solo 1.0.12 holds no mutex, so the installer did not see it run and left it to Windows'
// Restart Manager, which gave up after about 4.5 s while 1.0.12 was still stopping its nodes and
// its database: Setup stopped with "Setup was unable to automatically close all applications", and
// a silent install rolled back (exit code 5). Before Restart Manager looks (PrepareToInstall), and
// before the uninstaller removes anything, a Forge Solo running from this install's folder is now
// closed as its tray's Quit does (WM_CLOSE to its tray window, which fyne's systray turns into the
// stop), and every program in that folder is waited for, two minutes at a time, never ended by
// force. A silent run that waited in vain changes nothing. Pascal Script cannot run here, so the
// code that finds the programs and waits for them is pinned statement by statement: a slip in it
// finds nothing, or waits a tenth as long, and the update fails as before.
func TestInstallerClosesForgeSoloCleanly(t *testing.T) {
	code := pascalCode(installerSection(t, "Code"))
	prepare := installerFunc(t, "function PrepareToInstall(var NeedsRestart: Boolean): String;")
	if !strings.Contains(prepare, "\n  Result := '';\n  if not StopForgeSolo then\n    Result := '") ||
		!strings.Contains(pascalLiterals(prepare), "Forge Solo did not stop, so Setup changed nothing.") {
		t.Errorf("CLOSE-SETUP: Setup does not close Forge Solo before Restart Manager looks, or goes on when it did not stop:\n%s", prepare)
	}
	uninstall := installerFunc(t, "procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);")
	if !strings.Contains(uninstall, "\n  if CurUninstallStep = usUninstall then\n  begin\n    if not StopForgeSolo then\n    begin\n") ||
		!regexp.MustCompile(`\n    if not StopForgeSolo then\n    begin\n      Log\('[^']*'\);\n      Abort;\n    end;\n`).MatchString(uninstall) {
		t.Errorf("CLOSE-UNINSTALL: the uninstaller does not close Forge Solo before it removes anything, or goes on when it did not stop:\n%s", uninstall)
	}

	// Each Windows call is declared as Windows has it, the Unicode one where there are two: with a
	// wrong one nothing is found, and Restart Manager fails the update as before.
	var imports []string
	for _, decl := range []string{
		"function CreateToolhelp32Snapshot(Flags, ProcessID: Cardinal): Cardinal;\n  external 'CreateToolhelp32Snapshot@kernel32.dll stdcall';",
		"function Process32First(Snapshot: Cardinal; var Entry: TProcessEntry): Bool;\n  external 'Process32FirstW@kernel32.dll stdcall';",
		"function Process32Next(Snapshot: Cardinal; var Entry: TProcessEntry): Bool;\n  external 'Process32NextW@kernel32.dll stdcall';",
		"function OpenProcess(Access: Cardinal; Inherit: Bool; ProcessID: Cardinal): Cardinal;\n  external 'OpenProcess@kernel32.dll stdcall';",
		"function QueryFullProcessImageName(Process, Flags: Cardinal; Name: String; var Size: Cardinal): Bool;\n  external 'QueryFullProcessImageNameW@kernel32.dll stdcall';",
		"function GetLongPathName(Path, LongPath: String; Size: Cardinal): Cardinal;\n  external 'GetLongPathNameW@kernel32.dll stdcall';",
		"function CloseHandle(Handle: Cardinal): Bool;\n  external 'CloseHandle@kernel32.dll stdcall';",
		"function GetCurrentProcessId: Cardinal;\n  external 'GetCurrentProcessId@kernel32.dll stdcall';",
		"function FindWindowEx(Parent, After: HWND; ClassName: String; WindowName: Cardinal): HWND;\n  external 'FindWindowExW@user32.dll stdcall';",
		"function GetWindowThreadProcessId(Wnd: HWND; var ProcessID: Cardinal): Cardinal;\n  external 'GetWindowThreadProcessId@user32.dll stdcall';",
		"function PeekMessage(var Msg: TWindowMessage; Wnd: HWND; First, Last, Remove: Cardinal): Bool;\n  external 'PeekMessageW@user32.dll stdcall';",
		"function TranslateMessage(var Msg: TWindowMessage): Bool;\n  external 'TranslateMessage@user32.dll stdcall';",
		"function DispatchMessage(var Msg: TWindowMessage): Longint;\n  external 'DispatchMessageW@user32.dll stdcall';",
		"procedure PostQuitMessage(ExitCode: Longint);\n  external 'PostQuitMessage@user32.dll stdcall';",
		"\n  TH32CS_SNAPPROCESS = $2;\n", "\n  PROCESS_QUERY_LIMITED_INFORMATION = $1000;\n", "\n  INVALID_HANDLE = $FFFFFFFF;\n",
		"\n  WM_QUIT = $0012;\n", "\n  PM_REMOVE = 1;\n",
	} {
		if !strings.Contains(code, decl) {
			imports = append(imports, decl)
		}
	}
	if len(imports) > 0 {
		t.Errorf("CLOSE-IMPORTS: these are not in [Code] as Windows declares them:\n%s", strings.Join(imports, "\n"))
	}

	ask := installerFunc(t, "function AskForgeSoloToQuit: Integer;")
	if !strings.Contains(ask, "\n  Wnd := FindWindowEx(0, 0, 'SystrayClass', 0);\n  while Wnd <> 0 do\n  begin\n") ||
		!strings.Contains(ask, "\n    Wnd := FindWindowEx(0, Wnd, 'SystrayClass', 0);\n  end;\nend;") ||
		!strings.Contains(ask, "PostMessage(Wnd, WM_CLOSE, 0, 0);") || !regexp.MustCompile(`(?m)^  WM_CLOSE = \$0010;$`).MatchString(code) {
		t.Errorf("CLOSE-AS-QUIT: Forge Solo is not asked to quit as its tray's Quit does (WM_CLOSE to each SystrayClass window):\n%s", ask)
	}
	if mod, err := os.ReadFile("windows/launcher/go.mod"); err != nil || !regexp.MustCompile(`(?m)^\s*fyne\.io/systray v`).Match(mod) {
		t.Error("CLOSE-TRAY-LIB: the launcher's tray is no longer fyne's systray, whose window class (SystrayClass) the installer closes")
	}
	if !strings.Contains(ask, "\n  Launcher := InstallFolder + AnsiLowercase('{#MyAppExe}');\n") ||
		!strings.Contains(ask, "\n    ProcessID := 0;\n    GetWindowThreadProcessId(Wnd, ProcessID);\n    if ProgramPath(ProcessID) = Launcher then\n    begin\n      PostMessage(") ||
		!strings.Contains(installerFunc(t, "function InstallFolder: String;"), "\nbegin\n  Result := AddBackslash(LongPath(ExpandConstant('{app}')));\nend;") {
		t.Errorf("CLOSE-OWN-ONLY: a tray window is closed without checking that its program is this install's launcher:\n%s", ask)
	}

	running := installerFunc(t, "function ProgramsRunning: String;")
	if !strings.Contains(running, "\n  Folder := InstallFolder;\n  Snapshot := CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0);\n  if Snapshot = INVALID_HANDLE then\n") ||
		!strings.Contains(running, `
      repeat
        if Entry.ProcessID <> GetCurrentProcessId then
        begin
          Path := ProgramPath(Entry.ProcessID);
          Name := ExeName(Entry);
          if (Copy(Path, 1, Length(Folder)) = Folder) and (Copy(ExtractFileName(Path), 1, 5) <> 'unins') and
             (Pos(', ' + AnsiLowercase(Name) + ',', ', ' + AnsiLowercase(Result) + ',') = 0) then
          begin
            if Result <> '' then
              Result := Result + ', ';
            Result := Result + Name;
          end;
        end;
      until not Process32Next(Snapshot, Entry);
  finally
    CloseHandle(Snapshot);
  end;
end;`) ||
		!strings.Contains(installerFunc(t, "function ExeName(Entry: TProcessEntry): String;"),
			"\n  while (I <= 259) and (Entry.ExeFile[I] <> #0) do\n  begin\n    Result := Result + Entry.ExeFile[I];\n    I := I + 1;\n  end;\nend;") {
		t.Errorf("CLOSE-ALL-PROGRAMS: what is waited for is not every program in this install's folder (the nodes and PostgreSQL too), each named:\n%s", running)
	}
	if !strings.Contains(running, "if Entry.ProcessID <> GetCurrentProcessId then") || !strings.Contains(running, "(Copy(ExtractFileName(Path), 1, 5) <> 'unins')") {
		t.Errorf("CLOSE-NOT-ITSELF: the uninstaller would wait for itself:\n%s", running)
	}
	path := installerFunc(t, "function ProgramPath(ProcessID: Cardinal): String;")
	if !strings.Contains(path, `
begin
  Result := '';
  Process := OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, False, ProcessID);
  if Process = 0 then
    exit;
  Name := StringOfChar(' ', 1024);
  Size := 1024;
  if QueryFullProcessImageName(Process, 0, Name, Size) then
    Result := LongPath(Copy(Name, 1, Size));
  CloseHandle(Process);
end;`) {
		t.Errorf("CLOSE-PROGRAM-PATH: a process's program is not read as its full Windows path:\n%s", path)
	}
	long := installerFunc(t, "function LongPath(Path: String): String;")
	if !strings.Contains(long, `
begin
  Long := StringOfChar(' ', 1024);
  N := GetLongPathName(Path, Long, 1024);
  if (N > 0) and (N < 1024) then
    Path := Copy(Long, 1, N);
  Result := AnsiLowercase(Path);
end;`) || !strings.Contains(path, "Result := LongPath(Copy(Name, 1, Size));") {
		t.Errorf("CLOSE-LONG-PATH: a program started by its short path, as PostgreSQL can be, is not seen as this install's:\n%s", long)
	}

	stop := installerFunc(t, "function StopForgeSolo: Boolean;")
	if !strings.Contains(stop, "\nbegin\n  Running := ProgramsRunning;\n  Result := Running = '';\n  if Result then\n    exit;\n  ShowClosing(True);\n") {
		t.Errorf("CLOSE-ONLY-WHEN-RUNNING: Setup goes on without closing Forge Solo, or waits when none of its programs runs:\n%s", stop)
	}
	if !strings.Contains(stop, "\n      while (Running <> '') and (Seconds < StopWait) do\n      begin\n        Waiting;\n        Seconds := Seconds + 1;\n        Running := ProgramsRunning;\n      end;\n") ||
		!strings.Contains(stop, "\n  Result := Running = '';\nend;") || !strings.Contains(stop, "Asked := AskForgeSoloToQuit;") {
		t.Errorf("CLOSE-WAIT: Setup does not wait until no program of this install runs:\n%s", stop)
	}
	// Each turn asks Forge Solo to quit, then waits StopWait seconds from 0 while any program runs,
	// and Retry starts a turn again: the whole turn, as one text.
	if !strings.Contains(stop, `
  try
    repeat
      Asked := AskForgeSoloToQuit;
      Log('Forge Solo runs from ' + ExpandConstant('{app}') + ' (' + Running + '): ' + IntToStr(Asked) +
        ' asked to quit, as its tray''s Quit does');
      Seconds := 0;
      while (Running <> '') and (Seconds < StopWait) do
      begin
        Waiting;
        Seconds := Seconds + 1;
        Running := ProgramsRunning;
      end;
      Again := False;
      if Running = '' then
        Log('Forge Solo stopped within ' + IntToStr(Seconds) + ' s')
      else
      begin
        Log('Forge Solo still runs after ' + IntToStr(Seconds) + ' s: ' + Running);
        Again := SuppressibleMsgBox('Forge Solo has not stopped yet. Still running from ' +
          ExpandConstant('{app}') + ': ' + Running + '.' + #13#10#13#10 +
          'Forge Solo stops its nodes and its database cleanly, which can take a few minutes on a ' +
          'slow PC. Retry waits for it again. Cancel changes nothing.',
          mbError, MB_RETRYCANCEL, IDCANCEL) = IDRETRY;
      end;
    until not Again;
  finally
`) {
		t.Errorf("CLOSE-RETRY-WAITS: a turn of the wait does not ask Forge Solo to quit, then wait StopWait seconds from 0, with Retry for another turn:\n%s", stop)
	}
	// StopWait counts calls of Waiting, each a second: the closing page and windows/README.md say
	// Forge Solo gets two minutes.
	wait := regexp.MustCompile(`(?m)^  StopWait = (\d+);$`).FindStringSubmatch(code)
	if wait == nil || wait[1] != "120" || !strings.Contains(pascalLiterals(installerFunc(t, "procedure ShowClosing(Show: Boolean);")), "That can take up to two minutes.") ||
		!strings.Contains(flat(string(mustRead(t, "windows/README.md"))), "After two minutes it names those still running and offers Retry or Cancel") {
		t.Errorf("CLOSE-BOUNDED: the wait (StopWait %v) is not the two minutes the closing page and windows/README.md say", wait)
	}
	// Waiting is a second: turns that each answer the window, then sleep.
	answer := `
    if IsUninstaller then
    begin
      if not QuitSeen then
        while PeekMessage(Msg, 0, 0, 0, PM_REMOVE) do
        begin
          if Msg.MessageID = WM_QUIT then
          begin
            QuitSeen := True;
            PostQuitMessage(Msg.WParam);
            break;
          end;
          TranslateMessage(Msg);
          DispatchMessage(Msg);
        end;
    end else if not WizardSilent then
      ClosingPage.Animate;
`
	waiting := installerFunc(t, "procedure Waiting;")
	turns := regexp.MustCompile(`(?s)\n  for I := 1 to (\d+) do\n  begin(\n.*\n)    Sleep\((\d+)\);\n  end;\nend;$`).FindStringSubmatch(waiting)
	sleep := 0
	if turns == nil || turns[2] != answer || strings.Count(waiting, "Sleep(") != 1 {
		t.Errorf("CLOSE-BOUNDED: Waiting is not one loop that answers the window, then sleeps, on each turn:\n%s", waiting)
	} else {
		n, _ := strconv.Atoi(turns[1])
		sleep, _ = strconv.Atoi(turns[3])
		if n*sleep != 1000 {
			t.Errorf("CLOSE-BOUNDED: Waiting waits %d turns of %d ms, not a second: StopWait counts seconds", n, sleep)
		}
	}

	// While it waits, the uninstaller's window goes on answering (its messages are handled between
	// turns, and a request to end it is passed on), and so does the wizard's page.
	msg := regexp.MustCompile(`(?s)\n  TWindowMessage = record\n(.*?)\n  end;`).FindStringSubmatch(code)
	if msg == nil || msg[1] != "    Window: HWND;\n    MessageID: Cardinal;\n    WParam, LParam: Longint;\n    Time: Cardinal;\n    X, Y: Longint;\n    Spare: Cardinal;" ||
		sleep > 100 || !strings.Contains(waiting, answer) {
		t.Errorf("CLOSE-ANSWERS: while Setup or the uninstaller waits, its window stops answering:\n%s", waiting)
	}
	if !strings.Contains(stop, "mbError, MB_RETRYCANCEL, IDCANCEL) = IDRETRY;") || !strings.Contains(stop, "until not Again;") ||
		!strings.Contains(pascalLiterals(stop), "Retry waits for it again. Cancel changes nothing.") {
		t.Errorf("CLOSE-RETRY: when Forge Solo has not stopped in time, the user is not offered Retry or Cancel, with Cancel for a silent run:\n%s", stop)
	}
	if m := regexp.MustCompile(`(?i)TerminateProcess|taskkill|Stop-Process|\bkill\b`).FindString(code); m != "" {
		t.Errorf("CLOSE-NO-FORCE: the installer ends a program by force (%s); a node ended while it writes has to sync again", m)
	}

	if !strings.Contains(installerFunc(t, "procedure InitializeWizard;"), "\n  ClosingPage := CreateOutputMarqueeProgressPage('Closing Forge Solo',") {
		t.Error("CLOSE-STATUS: the wizard has no page saying it is closing Forge Solo")
	}
	show := installerFunc(t, "procedure ShowClosing(Show: Boolean);")
	if !strings.Contains(show, "ClosingPage.SetText('Closing Forge Solo...',") || !strings.Contains(show, "      ClosingPage.Show;\n") ||
		!strings.Contains(show, "UninstallProgressForm.StatusLabel.Caption := 'Closing Forge Solo...';") ||
		!strings.Contains(stop, "\n  ShowClosing(True);\n  try\n") || !strings.Contains(stop, "\n  finally\n    ShowClosing(False);\n  end;\n") {
		t.Errorf("CLOSE-STATUS: Setup or the uninstaller does not say it is closing Forge Solo while it waits:\n%s", show)
	}

	// TProcessEntry is PROCESSENTRY32W: Process32FirstW writes ProcessEntrySize bytes into it.
	rec := regexp.MustCompile(`(?s)\n  TProcessEntry = record\n(.*?)\n  end;`).FindStringSubmatch(code)
	size := regexp.MustCompile(`(?m)^  ProcessEntrySize = (\d+);$`).FindStringSubmatch(code)
	if rec == nil || size == nil {
		t.Fatal("CLOSE-ENTRY-SIZE: no TProcessEntry record, or no ProcessEntrySize")
	}
	got := 0
	for _, l := range strings.Split(rec[1], "\n") {
		names, typ, _ := strings.Cut(strings.TrimSpace(l), ":")
		typ = strings.TrimSuffix(strings.TrimSpace(typ), ";")
		switch {
		case typ == "Cardinal" || typ == "Longint":
			got += 4 * len(strings.Split(names, ","))
		case typ == "array[0..259] of Char":
			got += 260 * 2
		default:
			t.Errorf("CLOSE-ENTRY-SIZE: TProcessEntry has a field this test cannot size: %q", l)
		}
	}
	if want, _ := strconv.Atoi(size[1]); got != 556 || want != got {
		t.Errorf("CLOSE-ENTRY-SIZE: TProcessEntry is %d bytes and ProcessEntrySize %s; PROCESSENTRY32W is 556 in a 32-bit program", got, size[1])
	}
	// Its fields in PROCESSENTRY32W's order, and its size set before the first call, which fails
	// on any other.
	if rec[1] != "    Size, Usage, ProcessID, DefaultHeapID, ModuleID, Threads, ParentProcessID: Cardinal;\n    PriClassBase: Longint;\n    Flags: Cardinal;\n    ExeFile: array[0..259] of Char;" ||
		!strings.Contains(running, "\n  try\n    Entry.Size := ProcessEntrySize;\n    if Process32First(Snapshot, Entry) then\n      repeat\n") {
		t.Errorf("CLOSE-ENTRY-SIZE: TProcessEntry's fields are not PROCESSENTRY32W's, or its size is not set before Process32First:\n%s", rec[1])
	}

	if win := flat(string(mustRead(t, "windows/README.md"))); !strings.Contains(win, "To go back to 1.0.12, quit Forge Solo first: 1.0.12's installer cannot close it") {
		t.Error("CLOSE-DOCS-ROLLBACK: windows/README.md does not say that going back to 1.0.12 needs Forge Solo quit first")
	}
}

// Forge Solo 1.0.13 keeps its data in forgesolo.db, and PostgreSQL only moves the data of 1.0.12
// and before into it, once. The installer puts PostgreSQL on disk only for an account that has that
// data (pgdata\PG_VERSION in its data folder): a fresh install never has it, and an update for an
// account without old data removes what an earlier version installed, with the PostgreSQL schema,
// which nothing reads now. The migrator ships always, in bin. Going back to 1.0.12 and forward
// again installs PostgreSQL again, since the old data is there, so the merge has it. The uninstaller
// removes it, whoever installed it.
func TestInstallerPostgreSQLOnlyForAMove(t *testing.T) {
	files := installerSection(t, "Files")
	if regexp.MustCompile(`(?mi)^Source: "[^"]*init-db\.sql"`).MatchString(files) {
		t.Error("INST-NO-SCHEMA: the installer still installs init-db.sql")
	}
	if !regexp.MustCompile(`(?m)^Source: "bin\\\*"; DestDir: "\{app\}"; Flags: ignoreversion\r?$`).MatchString(files) {
		t.Error("INST-BIN: bin, with forge-solo-migrate.exe in it, is not installed whole")
	}
	if !regexp.MustCompile(`(?m)^Source: "pgsql\\\*"; DestDir: "\{app\}\\pgsql"; Flags: ignoreversion recursesubdirs createallsubdirs; Check: HasOldData\r?$`).MatchString(files) {
		t.Error("INST-PGSQL-OLD-DATA: PostgreSQL is not installed, or not only, for an account with the data of 1.0.12 or before")
	}
	if !strings.Contains(installerFunc(t, "function HasOldData: Boolean;"),
		"\n  Result := FileExists(ExpandConstant('{userappdata}\\ForgeSolo\\pgdata\\PG_VERSION'));\n") {
		t.Errorf("INST-PGSQL-CHECK: HasOldData does not look for the old data where 1.0.12 kept it:\n%s", installerFunc(t, "function HasOldData: Boolean;"))
	}
	del := installerSection(t, "InstallDelete")
	if !regexp.MustCompile(`(?m)^Type: files; Name: "\{app\}\\init-db\.sql"\r?$`).MatchString(del) {
		t.Error("INST-DELETE-SCHEMA: an update leaves the init-db.sql an earlier version installed")
	}
	if !regexp.MustCompile(`(?m)^Type: filesandordirs; Name: "\{app\}\\pgsql"; Check: not HasOldData\r?$`).MatchString(del) {
		t.Error("INST-DELETE-PGSQL: an update for an account with no old data leaves the PostgreSQL an earlier version installed")
	}
	if !regexp.MustCompile(`(?m)^Type: filesandordirs; Name: "\{app\}\\pgsql"\r?$`).MatchString(installerSection(t, "UninstallDelete")) {
		t.Error("UNINST-PGSQL: the uninstaller leaves PostgreSQL")
	}
}

// Defender skips only the folders written constantly, both nodes' blocks and chainstate, never
// the old database folder (pgdata). The install and the uninstall remove the whole-folder
// exclusion earlier versions added; none of them excluded pgdata, so there is none to remove.
func TestInstallerDefenderExclusions(t *testing.T) {
	paths := installerFunc(t, "function DefenderPaths(DataDir: String): String;")
	for _, f := range []string{`bch2\blocks`, `bch2\chainstate`, `elevenseventyfive\blocks`, `elevenseventyfive\chainstate`} {
		if !strings.Contains(paths, "PSQuote(DataDir + '\\"+f+"')") {
			t.Errorf("INST-DEFENDER-CHAINS: DefenderPaths lacks %s", f)
		}
	}
	if strings.Contains(paths, "pgdata") || strings.Count(paths, "PSQuote(") != 4 {
		t.Errorf("INST-DEFENDER-NO-PGDATA: DefenderPaths is not the four chain folders alone:\n%s", paths)
	}
	old := installerFunc(t, "function OldDefenderPaths(DataDir: String): String;")
	if !strings.Contains(old, "\n  Result := PSQuote(DataDir);\n") || strings.Contains(old, "pgdata") {
		t.Errorf("INST-DEFENDER-OLD: OldDefenderPaths is not the whole data folder alone:\n%s", old)
	}
	install := installerFunc(t, "procedure CurStepChanged(CurStep: TSetupStep);")
	if !strings.Contains(install, `Remove-MpPreference -ExclusionPath ' + OldDefenderPaths(DataDir) + ' -ErrorAction SilentlyContinue; Add-MpPreference -ExclusionPath ' + DefenderPaths(DataDir) + ' -ErrorAction SilentlyContinue"';`) {
		t.Error("INST-DEFENDER-REMOVE-OLD: the install does not remove the old exclusions before it adds the chain folders'")
	}
	if !strings.Contains(installerFunc(t, "procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);"),
		`Remove-MpPreference -ExclusionPath ' + OldDefenderPaths(DataDir) + ', ' + DefenderPaths(DataDir) + ' -ErrorAction SilentlyContinue"';`) {
		t.Error("UNINST-DEFENDER: the uninstall does not remove every exclusion this and earlier versions added")
	}
}

// A move of the old data cut short can leave junctions in %ProgramData%\ForgeSolo\links, which lead
// to the data folder and the install folder. The uninstaller removes them, and their folders once
// empty, and never what they lead to: RemoveDir removes a junction itself, and nothing that is not
// empty, and nothing there deletes a tree.
func TestUninstallerRemovesLinksWithoutFollowingThem(t *testing.T) {
	links := installerFunc(t, "procedure RemoveLinks;")
	if !strings.Contains(links, "\n  Links := ExpandConstant('{commonappdata}\\ForgeSolo\\links');\n") {
		t.Fatalf("UNINST-LINKS: RemoveLinks does not work on %%ProgramData%%\\ForgeSolo\\links:\n%s", links)
	}
	for _, want := range []string{
		"RemoveDir(Links + '\\' + FindRec.Name + '\\data');",
		"RemoveDir(Links + '\\' + FindRec.Name + '\\app');",
		"RemoveDir(Links + '\\' + FindRec.Name);",
		"RemoveDir(Links);",
		"RemoveDir(ExpandConstant('{commonappdata}\\ForgeSolo'));",
	} {
		if !strings.Contains(links, want) {
			t.Errorf("UNINST-LINKS: RemoveLinks lacks %s", want)
		}
	}
	for _, follows := range []string{"DelTree", "DeleteFile", "rd /s", "rmdir /s", "Remove-Item", "Exec("} {
		if strings.Contains(links, follows) {
			t.Errorf("UNINST-LINKS-NO-FOLLOW: RemoveLinks uses %s, which can delete what a junction leads to", follows)
		}
	}
	uninstall := installerFunc(t, "procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);")
	if !regexp.MustCompile(`(?s)if CurUninstallStep = usPostUninstall then\n  begin\n.*\n    RemoveLinks;\n`).MatchString(uninstall) {
		t.Error("UNINST-LINKS-CALLED: the uninstaller does not remove the links")
	}
	if regexp.MustCompile(`DelTree\([^)]*commonappdata`).MatchString(pascalCode(installerSection(t, "Code"))) {
		t.Error("UNINST-LINKS-NO-FOLLOW: the installer deletes a tree under %ProgramData%")
	}
}

// Asked whether to delete the data folder, the uninstaller says what is in it: forgesolo.db, the
// copy a merge kept of it, if there is one, and the old database kept for going back to 1.0.12, if
// it is there.
func TestUninstallerSaysWhatTheDataFolderHolds(t *testing.T) {
	contents := installerFunc(t, "function DataFolderContents(DataDir: String): String;")
	text := pascalLiterals(contents)
	for _, want := range []string{
		"- forgesolo.db: your blocks, payouts and settings",
		": the copy of forgesolo.db from before the data of 1.0.12 was last merged into it",
		"- pgdata: the database of Forge Solo 1.0.12 and before, kept for going back to 1.0.12",
		"- secrets.env: this install's passwords",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("UNINST-PROMPT: the data folder's contents do not say %q", want)
		}
	}
	if !strings.Contains(contents, "\n  Kept := NewestBeforeMerge(DataDir);\n  if Kept <> '' then\n") ||
		!strings.Contains(contents, "\n  if DirExists(DataDir + '\\pgdata') then\n") {
		t.Errorf("UNINST-PROMPT-ONLY-THERE: the copy and the old database are not named only when they are there:\n%s", contents)
	}
	newest := installerFunc(t, "function NewestBeforeMerge(DataDir: String): String;")
	if !strings.Contains(newest, "FindFirst(DataDir + '\\forgesolo.db.before-merge-*', FindRec)") ||
		!strings.Contains(newest, "\n        if FindRec.Name > Result then\n          Result := FindRec.Name;\n") {
		t.Errorf("UNINST-PROMPT-NEWEST: NewestBeforeMerge does not name the newest copy:\n%s", newest)
	}
	if !strings.Contains(installerFunc(t, "procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);"),
		"DataDir + #13#10#13#10 + DataFolderContents(DataDir) + #13#10#13#10 +") {
		t.Error("UNINST-PROMPT-SHOWN: the question does not say what the data folder holds")
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
	if !strings.Contains(body("function RuleName"), "Result := Base + ' for ' + Account;") ||
		!strings.Contains(install, "\n    RulesAccount := ExpandConstant('{username}');\n") {
		t.Error("FIREWALL-PER-ACCOUNT: a rule's name is not this Windows account's")
	}
	for _, must := range []string{
		`'netsh advfirewall firewall delete rule name="' + Base + '" >nul 2>&1 & '`,
		`'netsh advfirewall firewall delete rule name="' + RuleName(Base, RulesAccount) + '" >nul 2>&1 & '`,
		`'netsh advfirewall firewall add rule name="' + RuleName(Base, RulesAccount) + '" dir=in action=allow program="' +`,
	} {
		if !strings.Contains(body("function FirewallRule"), must) {
			t.Errorf("FIREWALL-RULE-PUT: FirewallRule lacks %s", must)
		}
	}
	for _, must := range []string{`delete rule name="' + Base + '" & '`, `delete rule name="' + RuleName(Base, RulesAccount) + '" & '`} {
		if !strings.Contains(body("function FirewallRemove"), must) {
			t.Errorf("FIREWALL-RULE-REMOVE: FirewallRemove lacks %s", must)
		}
	}
}
