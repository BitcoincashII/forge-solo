package forgesolo

import (
	"regexp"
	"strings"
	"testing"
)

// Forge Gateway's Windows installer, windows/gateway/forge-gateway.iss, is Forge Solo's installer
// for one program: per-user, one elevated step for the firewall, the running app closed cleanly
// before its files are replaced, the data folder kept unless the user says otherwise. Pascal
// Script cannot run here, so what it does is pinned statement by statement, and the code it shares
// with windows/forge-solo.iss is compared with Forge Solo's, whose own tests pin it.

// gwInstScript is windows/gateway/forge-gateway.iss.
func gwInstScript(t *testing.T) string {
	t.Helper()
	return string(mustRead(t, "windows/gateway/forge-gateway.iss"))
}

// gwInstSection is the body of the gateway installer script's [name] section, or "" if it has none.
func gwInstSection(t *testing.T, name string) string {
	t.Helper()
	s := gwInstScript(t)
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

// gwInstFunc is the gateway installer script's routine declared as decl, up to its closing "end;",
// without comments, or "" if there is none.
func gwInstFunc(t *testing.T, decl string) string {
	t.Helper()
	s := gwInstScript(t)
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

// gwInstCode is the gateway installer script's [Code] without comments.
func gwInstCode(t *testing.T) string {
	t.Helper()
	return pascalCode(gwInstSection(t, "Code"))
}

// gwInstEntries is the entries of an Inno Setup section: its lines without comments and blank ones.
func gwInstEntries(section string) []string {
	var out []string
	for _, l := range strings.Split(section, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) != "" && !strings.HasPrefix(strings.TrimSpace(l), ";") {
			out = append(out, l)
		}
	}
	return out
}

// gwInstSame fails the test with code when the gateway's routine gw is not Forge Solo's routine solo
// with Forge Solo's name changed to Forge Gateway's.
func gwInstSame(t *testing.T, code, solo, gw string) {
	t.Helper()
	want := strings.NewReplacer("Forge Solo", "Forge Gateway", "ForgeSolo", "ForgeGateway").Replace(installerFunc(t, solo))
	got := gwInstFunc(t, gw)
	if want == "" || got != want {
		t.Errorf("%s: %s is not Forge Solo's %s, whose tests pin it:\n%s\n--- Forge Solo's, renamed:\n%s", code, gw, solo, got, want)
	}
}

// Forge Gateway installs for the Windows account that runs Setup, with no administrator rights,
// into a folder of its own, on Windows that runs x64 programs, and its AppId is its own: with Forge
// Solo's, each installer would update or remove the other app. The release names the installer
// ForgeGateway-Setup-<version>.exe and stamps the version on the command line.
func TestGatewayInstallerSetup(t *testing.T) {
	setup := gwInstSection(t, "Setup")
	for _, c := range []struct{ code, line string }{
		{"GWI-SETUP-PER-USER", "PrivilegesRequired=lowest"},
		{"GWI-SETUP-FOLDER", `DefaultDirName={localappdata}\Programs\ForgeGateway`},
		{"GWI-SETUP-ARCH", "ArchitecturesAllowed=x64compatible"},
		{"GWI-SETUP-APPID", "AppId={{61A087E0-4C4F-499C-8A19-BCBCAC149866}"},
		{"GWI-SETUP-OUTPUT", "OutputBaseFilename=ForgeGateway-Setup-{#MyAppVersion}"},
		{"GWI-SETUP-ICON", `SetupIconFile=launcher\forge-gateway.ico`},
		{"GWI-SETUP-UNINSTALL-ICON", `UninstallDisplayIcon={app}\{#MyAppExe}`},
		{"GWI-SETUP-NAME", "UninstallDisplayName={#MyAppName}"},
		{"GWI-SETUP-NO-DIR-PAGE", "DisableDirPage=yes"},
	} {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(c.line) + `\r?$`).MatchString(setup) {
			t.Errorf("%s: [Setup] lacks the line %s", c.code, c.line)
		}
	}
	appID := regexp.MustCompile(`(?m)^AppId=(.*?)\r?$`)
	gw, solo := appID.FindAllStringSubmatch(setup, -1), appID.FindStringSubmatch(installerSection(t, "Setup"))
	if len(gw) != 1 || solo == nil || strings.EqualFold(gw[0][1], solo[1]) {
		t.Errorf("GWI-SETUP-APPID-OWN: the AppId is %v, Forge Solo's %v: one each, never the same", gw, solo)
	}
	if regexp.MustCompile(`(?mi)^\s*AppMutex\s*=`).MatchString(setup) {
		t.Error("GWI-SETUP-NO-APPMUTEX: [Setup] has AppMutex: Setup checks it at start, before Forge Gateway is closed, and a silent update or uninstall of a running Forge Gateway ends there")
	}
	script := gwInstScript(t)
	for _, c := range []struct{ code, want string }{
		{"GWI-SETUP-EXE", "\n#define MyAppExe \"forge-gateway-tray.exe\"\n"},
		{"GWI-SETUP-VERSION", "\n#ifndef MyAppVersion\n  #define MyAppVersion \"1.1.0\"\n#endif\n"},
		{"GWI-SETUP-APPNAME", "\n#define MyAppName \"Forge Gateway\"\n"},
		{"GWI-SETUP-URL", "\n#define MyAppURL \"https://github.com/BitcoincashII/forge-gateway\"\n"},
	} {
		if !strings.Contains(script, c.want) {
			t.Errorf("%s: the script lacks %q", c.code, c.want)
		}
	}
	if m := regexp.MustCompile(`(?m)^WindowsVersionNotSupported=(.*?)\r?$`).FindStringSubmatch(gwInstSection(t, "Messages")); m == nil ||
		m[1] != "Forge Gateway needs 64-bit Windows: Windows 10 or 11 on an x64 PC, or Windows 11 on ARM." {
		t.Errorf("GWI-SETUP-ARCH-MESSAGE: the refusal on Windows that runs no x64 programs is %q", m)
	}
}

// The installer takes bin whole: the tray app, the gateway and the license, and nothing of Forge
// Solo's (PostgreSQL, the nodes, the dashboard). Its shortcuts and its "launch now" start the tray
// app, which starts the gateway.
func TestGatewayInstallerFiles(t *testing.T) {
	for _, c := range []struct {
		code, section string
		want          []string
	}{
		{"GWI-FILES", "Files", []string{`Source: "bin\*"; DestDir: "{app}"; Flags: ignoreversion`}},
		{"GWI-FILES-ICONS", "Icons", []string{
			`Name: "{userprograms}\Forge Gateway"; Filename: "{app}\{#MyAppExe}"`,
			`Name: "{userprograms}\Uninstall Forge Gateway"; Filename: "{uninstallexe}"`,
			`Name: "{userdesktop}\Forge Gateway"; Filename: "{app}\{#MyAppExe}"; Tasks: desktopicon`,
		}},
		{"GWI-FILES-RUN", "Run", []string{`Filename: "{app}\{#MyAppExe}"; Description: "Launch Forge Gateway now"; Flags: nowait postinstall skipifsilent`}},
	} {
		if got := gwInstEntries(gwInstSection(t, c.section)); strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s: [%s] is\n%s\nwant\n%s", c.code, c.section, strings.Join(got, "\n"), strings.Join(c.want, "\n"))
		}
	}
	script := gwInstScript(t)
	for _, s := range []string{"\n[InstallDelete]", "\n[UninstallDelete]", "\n[Dirs]", "\n[UninstallRun]"} {
		if strings.Contains(script, s) {
			t.Errorf("GWI-FILES-NOTHING-ELSE: the script has a %s section", strings.TrimSpace(s))
		}
	}
	if m := regexp.MustCompile(`(?i)pgsql|postgres|web\\dist|bitcoincashIId|elevenseventyfive|junction`).FindString(script); m != "" {
		t.Errorf("GWI-FILES-NOT-SOLOS: the script names %q, which is Forge Solo's", m)
	}
}

// "Start Forge Gateway when I sign in" is the installer's, unticked unless the user ticks it, and
// an update with it unticked turns the sign-in start off. Inno Setup keeps the choice for the next
// install, a silent one included. The sign-in start passes --at-sign-in, so that a copy Windows
// starts while Forge Gateway runs opens no second status page.
func TestGatewayInstallerStartup(t *testing.T) {
	want := map[string][]string{
		"Tasks": {
			`Name: "desktopicon"; Description: "Create a desktop shortcut"; GroupDescription: "Additional icons:"`,
			`Name: "startup"; Description: "Start Forge Gateway when I sign in"; GroupDescription: "Startup:"; Flags: unchecked`,
		},
		"Registry": {
			`Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "ForgeGateway"; ValueData: """{app}\{#MyAppExe}"" --at-sign-in"; Flags: uninsdeletevalue; Tasks: startup`,
			`Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: none; ValueName: "ForgeGateway"; Flags: deletevalue; Tasks: not startup`,
		},
	}
	for _, section := range []string{"Tasks", "Registry"} {
		if got := gwInstEntries(gwInstSection(t, section)); strings.Join(got, "\n") != strings.Join(want[section], "\n") {
			t.Errorf("GWI-STARTUP: [%s] is\n%s\nwant\n%s", section, strings.Join(got, "\n"), strings.Join(want[section], "\n"))
		}
	}
	if regexp.MustCompile(`(?m)^\s*UsePreviousTasks\s*=\s*no`).MatchString(gwInstSection(t, "Setup")) {
		t.Error("GWI-STARTUP-KEPT: an update forgets the user's choice (UsePreviousTasks=no)")
	}
}

// One firewall rule, for miners on the network: TCP 3333, private and domain networks only, for
// forge-gateway.exe alone, named for the Windows account. The uninstaller removes it by the name the
// install kept, also after the account was renamed.
func TestGatewayInstallerFirewall(t *testing.T) {
	code := gwInstCode(t)
	install := gwInstFunc(t, "procedure CurStepChanged(CurStep: TSetupStep);")
	uninstall := gwInstFunc(t, "procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);")
	rules := regexp.MustCompile(`FirewallRule\('([^']*)', '([^']*)', '([^']*)', '([^']*)'\)`).FindAllStringSubmatch(code, -1)
	if len(rules) != 1 || rules[0][1] != "Forge Gateway Miner (3333)" || rules[0][2] != "forge-gateway.exe" || rules[0][3] != "3333" || rules[0][4] != "private,domain" {
		t.Errorf("GWI-FIREWALL-RULE: the install puts %v in place, want one rule: Forge Gateway Miner (3333), forge-gateway.exe, 3333, private,domain", rules)
	}
	if !strings.Contains(install, "FirewallRule('Forge Gateway Miner (3333)', 'forge-gateway.exe', '3333', 'private,domain');") {
		t.Error("GWI-FIREWALL-INSTALL: the install's elevated step does not put the rule in place")
	}
	if !regexp.MustCompile(`(?m)^  RuleCount = 1;$`).MatchString(code) ||
		!strings.Contains(gwInstFunc(t, "function RulesInPlace: Integer;"), "\nbegin\n  Result := 0;\n  if RuleInPlace(RuleName('Forge Gateway Miner (3333)', RulesAccount)) then Result := Result + 1;\nend;") {
		t.Errorf("GWI-FIREWALL-COUNT: the check does not count the one rule the install puts in place, by the name it carries:\n%s", gwInstFunc(t, "function RulesInPlace: Integer;"))
	}
	if strings.Count(uninstall, "FirewallRemove('Forge Gateway Miner (3333)')") != 1 || strings.Count(code, "FirewallRemove(") != 2 {
		t.Error("GWI-FIREWALL-UNINSTALL: the uninstaller does not remove the rule the install puts in place")
	}
	for _, c := range []struct{ code, want string }{
		{"GWI-FIREWALL-KEY", "\n  RulesKey = 'Software\\ForgeGateway';\n  RulesValue = 'FirewallRulesAccount';\n"},
	} {
		if !strings.Contains(code, c.want) {
			t.Errorf("%s: [Code] lacks %q", c.code, c.want)
		}
	}
	read := strings.Index(install, "\n    RulesAccount := ExpandConstant('{username}');\n    PreviousRulesAccount := KeptRulesAccount;\n")
	if read < 0 || read > strings.Index(install, "Cmd := ") {
		t.Error("GWI-FIREWALL-PREVIOUS: the install does not read the name an earlier install kept before it builds its commands")
	}
	if !strings.Contains(install, "\n    if InPlace = RuleCount then\n      RegWriteStringValue(HKCU, RulesKey, RulesValue, RulesAccount);\n") {
		t.Error("GWI-FIREWALL-KEEP: the install does not keep the name its rule carries, once it is in place")
	}
	kept := strings.Index(uninstall, "\n    RulesAccount := KeptRulesAccount;\n    if RulesAccount = '' then\n      RulesAccount := ExpandConstant('{username}');\n")
	if kept < 0 || kept > strings.Index(uninstall, "Cmd := ") {
		t.Error("GWI-FIREWALL-UNINSTALL-KEPT: the uninstaller does not remove the rule of the name the install kept")
	}
	if !strings.Contains(uninstall, "\n    RegDeleteValue(HKCU, RulesKey, RulesValue);\n    RegDeleteKeyIfEmpty(HKCU, RulesKey);\n") {
		t.Error("GWI-FIREWALL-FORGET: the uninstaller leaves the kept name behind")
	}
	// Each rule lets in only its program: with the port alone, any program could take the port
	// while Forge Gateway is not running and be reached through the rule.
	if !strings.Contains(gwInstFunc(t, "function FirewallRule(Base, Exe, Port, Profile: String): String;"),
		`'netsh advfirewall firewall add rule name="' + RuleName(Base, RulesAccount) + '" dir=in action=allow program="' +`+"\n      ExpandConstant('{app}') + '\\' + Exe + '\" protocol=TCP localport=' + Port + ' profile=' + Profile + ' & ';") {
		t.Error("GWI-FIREWALL-PROGRAM: the rule does not let in only the program in the install folder")
	}
}

// The account name kept for the firewall rule goes into the commands of the elevated step, and any
// program running as the user can change it: a kept name is used only if it cannot change those
// commands, and nothing else reads it.
func TestGatewayInstallerAccountName(t *testing.T) {
	usable := accountNameCheck(gwInstFunc(t, "function UsableAccountName(Name: String): Boolean;"))
	if usable == nil {
		t.Error("GWI-ACCOUNT-SAFE-SHAPE: the installer has no UsableAccountName in the form this test reads; taken as one that lets every name through")
		usable = func(string) bool { return true }
	}
	for _, c := range []struct {
		code  string
		names []string
	}{
		{"GWI-ACCOUNT-SAFE-QUOTE", []string{`x" & echo INJECTED>> C:\fwstub\pwned.txt & echo "`}},
		{"GWI-ACCOUNT-SAFE-PERCENT", []string{"x%COMSPEC%y"}},
		{"GWI-ACCOUNT-SAFE-CONTROL", []string{"x\x00y", "x\ty", "x\ny", "x\ry", "x\x1fy"}},
		{"GWI-ACCOUNT-SAFE-LENGTH", []string{strings.Repeat("a", 257)}},
	} {
		for _, name := range c.names {
			if usable(name) {
				t.Errorf("%s: the kept name %.60q (%d characters) goes into the elevated step's commands", c.code, name, len(name))
			}
		}
	}
	for _, name := range []string{"xclient", "O'Brien", "O\u2019Brien", "Zoë", "张伟", "Ann (Home)", "A&B", "x^y!z", strings.Repeat("a", 256)} {
		if !usable(name) {
			t.Errorf("GWI-ACCOUNT-SAFE-NAMES: the account name %.60q (%d characters) is not used", name, len(name))
		}
	}
	kept := gwInstFunc(t, "function KeptRulesAccount: String;")
	if !strings.Contains(kept, "\n  Result := '';\n  if RegQueryStringValue(HKCU, RulesKey, RulesValue, Name) then\n  begin\n    if UsableAccountName(Name) then\n      Result := Name\n    else\n      Log(") ||
		pascalAssignments(kept, "Result") != 2 {
		t.Errorf("GWI-ACCOUNT-SAFE-CHECKED: KeptRulesAccount does not return the kept name only when UsableAccountName lets it through:\n%s", kept)
	}
	code := gwInstCode(t)
	read := regexp.MustCompile(`RegQueryStringValue\(HKCU`)
	if n, in := len(read.FindAllString(code, -1)), len(read.FindAllString(kept, -1)); n != 1 || in != 1 {
		t.Errorf("GWI-ACCOUNT-SAFE-ONLY: the installer reads this account's registry %d times, %d of them in KeptRulesAccount; a kept name read elsewhere goes unchecked", n, in)
	}
	install := gwInstFunc(t, "procedure CurStepChanged(CurStep: TSetupStep);")
	uninstall := gwInstFunc(t, "procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);")
	for _, c := range []struct {
		code, name, in string
		n              int
	}{
		{"GWI-ACCOUNT-ONCE-PREVIOUS", "PreviousRulesAccount", code, 1},
		{"GWI-ACCOUNT-ONCE", "RulesAccount", code, 3},
		{"GWI-ACCOUNT-ONCE-INSTALL", "RulesAccount", install, 1},
		{"GWI-ACCOUNT-ONCE-UNINSTALL", "RulesAccount", uninstall, 2},
	} {
		if n := pascalAssignments(c.in, c.name); n != c.n {
			t.Errorf("%s: %s is set %d times, want %d", c.code, c.name, n, c.n)
		}
	}
}

// Forge Gateway's data folder holds no blockchain: nothing of it is excluded from Defender, and the
// installer runs no PowerShell.
func TestGatewayInstallerNoDefender(t *testing.T) {
	if m := regexp.MustCompile(`(?i)MpPreference|powershell|Defender`).FindString(gwInstScript(t)); m != "" {
		t.Errorf("GWI-NO-DEFENDER: the installer script has %q", m)
	}
}

// Before Setup replaces a file, or the uninstaller removes one, a Forge Gateway running from this
// install's folder is closed as its tray's Quit does (WM_CLOSE to its tray window), and every
// program in that folder is waited for, a minute at a time, never ended by force: the gateway sends
// Forge Pool the shares it still holds before it stops. Only then is the tray app's mutex checked:
// one still held is Forge Gateway running for another Windows account, which Setup must not close.
// A silent run that waited in vain changes nothing.
func TestGatewayInstallerCloses(t *testing.T) {
	code := gwInstCode(t)
	prepare := gwInstFunc(t, "function PrepareToInstall(var NeedsRestart: Boolean): String;")
	if !strings.HasSuffix(prepare, `
  if not StopForgeGateway then
    Result := 'Forge Gateway did not stop, so Setup changed nothing. Run Setup again once it has stopped.'
  else if not NoOtherForgeGateway then
    Result := 'Forge Gateway runs for another Windows account on this PC, so Setup changed nothing. ' +
      'Run Setup again once it has been quit there.';
end;`) {
		t.Errorf("GWI-CLOSE-SETUP: Setup does not close this account's Forge Gateway, then stop for one running for another account, last in PrepareToInstall:\n%s", prepare)
	}
	uninstall := gwInstFunc(t, "procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);")
	if !strings.Contains(uninstall, `
  if CurUninstallStep = usUninstall then
  begin
    if not StopForgeGateway then
    begin
      Log('Forge Gateway did not stop: the uninstall ends, and nothing is removed');
      Abort;
    end;
    if not NoOtherForgeGateway then
    begin
      Log('Forge Gateway runs for another Windows account: the uninstall ends, and nothing is removed');
      Abort;
    end;
    RulesAccount := KeptRulesAccount;
`) {
		t.Errorf("GWI-CLOSE-UNINSTALL: the uninstaller does not close this account's Forge Gateway, then stop for one running for another account, before it removes anything:\n%s", uninstall)
	}
	if strings.Count(code, "StopForgeGateway") != 3 || strings.Count(code, "NoOtherForgeGateway") != 3 || strings.Count(code, "ForgeGatewayRuns") != 3 ||
		strings.Contains(code, "CheckForMutexes(") || regexp.MustCompile(`(?i)\b(function InitializeSetup|function InitializeUninstall)\b`).MatchString(code) {
		t.Error("GWI-CLOSE-NO-EARLY-CHECK: Forge Gateway is closed, or the mutex checked, elsewhere than in PrepareToInstall and the uninstall step, or Setup or the uninstaller can refuse at start")
	}

	ask := gwInstFunc(t, "function AskForgeGatewayToQuit: Integer;")
	if !strings.Contains(ask, "\n  Launcher := InstallFolder + AnsiLowercase('{#MyAppExe}');\n") ||
		!strings.Contains(ask, "\n  Wnd := FindWindowEx(0, 0, 'SystrayClass', 0);\n") ||
		!strings.Contains(ask, "\n    if ProgramPath(ProcessID) = Launcher then\n    begin\n      PostMessage(Wnd, WM_CLOSE, 0, 0);\n") {
		t.Errorf("GWI-CLOSE-AS-QUIT: only this install's forge-gateway-tray.exe is not asked to quit as its tray's Quit does (WM_CLOSE to its SystrayClass window):\n%s", ask)
	}
	if m := regexp.MustCompile(`(?i)TerminateProcess|taskkill|Stop-Process|\bkill\b`).FindString(code); m != "" {
		t.Errorf("GWI-CLOSE-NO-FORCE: the installer ends a program by force (%s); the gateway would not send the pool the shares it holds", m)
	}

	stop := gwInstFunc(t, "function StopForgeGateway: Boolean;")
	if stop != `function StopForgeGateway: Boolean;
var Running: String; Seconds, Asked: Integer; Again: Boolean;
begin
  Running := ProgramsRunning;
  Result := Running = '';
  if Result then
    exit;
  ShowClosing(True);
  try
    repeat
      Asked := AskForgeGatewayToQuit;
      Log('Forge Gateway runs from ' + ExpandConstant('{app}') + ' (' + Running + '): ' + IntToStr(Asked) +
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
        Log('Forge Gateway stopped within ' + IntToStr(Seconds) + ' s')
      else
      begin
        Log('Forge Gateway still runs after ' + IntToStr(Seconds) + ' s: ' + Running);
        Again := SuppressibleMsgBox('Forge Gateway has not stopped yet. Still running from ' +
          ExpandConstant('{app}') + ': ' + Running + '.' + #13#10#13#10 +
          'Forge Gateway sends Forge Pool the shares it still holds before it stops, which can take ' +
          'up to a minute. Retry waits for it again. Cancel changes nothing.',
          mbError, MB_RETRYCANCEL, IDCANCEL) = IDRETRY;
      end;
    until not Again;
  finally
    ShowClosing(False);
  end;
  Result := Running = '';
end;` {
		t.Errorf("GWI-CLOSE-WAIT: StopForgeGateway does not ask Forge Gateway to quit, then wait StopWait seconds from 0 while any program of this install runs, with Retry for another turn and Cancel for a silent run:\n%s", stop)
	}
	// StopWait counts calls of Waiting, each a second (Forge Solo's, compared below). The tray
	// gives the gateway 30 s to stop, so a minute is enough for both.
	if wait := regexp.MustCompile(`(?m)^  StopWait = (\d+);$`).FindStringSubmatch(code); wait == nil || wait[1] != "60" {
		t.Errorf("GWI-CLOSE-BOUNDED: StopWait is %v, not the minute the question says", wait)
	}
	show := gwInstFunc(t, "procedure ShowClosing(Show: Boolean);")
	if !strings.Contains(show, "UninstallProgressForm.StatusLabel.Caption := 'Closing Forge Gateway...';") ||
		!strings.Contains(show, "ClosingPage.SetText('Closing Forge Gateway...', 'It sends Forge Pool the shares it still ' +\n        'holds first. That takes a few seconds.');\n      ClosingPage.Show;\n") {
		t.Errorf("GWI-CLOSE-STATUS: Setup or the uninstaller does not say it is closing Forge Gateway while it waits:\n%s", show)
	}
}

// The tray app holds Global\ForgeGatewayRunning while it runs (windows/gateway/launcher), one for
// the whole PC. The installer looks for that name alone.
func TestGatewayInstallerMutex(t *testing.T) {
	code := gwInstCode(t)
	if !strings.Contains(code, "\n  RunningMutex = 'Global\\ForgeGatewayRunning';\n") || strings.Count(code, "ForgeGatewayRunning") != 1 ||
		!strings.Contains(gwInstFunc(t, "function ForgeGatewayRuns: Boolean;"), "\nbegin\n  Result := MutexHeld(RunningMutex);\nend;") {
		t.Errorf("GWI-MUTEX: the installer does not look for the tray app's mutex, Global\\ForgeGatewayRunning, as RunningMutex alone")
	}
}

// Uninstalling asks whether to delete the data folder, which holds the node's login and the
// settings password, and says what is in it. No is the default, and the answer of a silent
// uninstall: the folder is kept for a reinstall.
func TestGatewayInstallerKeepsData(t *testing.T) {
	uninstall := gwInstFunc(t, "procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);")
	if !strings.Contains(uninstall, `
    DataDir := ExpandConstant('{userappdata}\ForgeGateway');
    if DirExists(DataDir) then
    begin
      if SuppressibleMsgBox('Also delete Forge Gateway''s data folder?' + #13#10#13#10 +
                DataDir + #13#10#13#10 +
                'It holds:' + #13#10 +
                '- forge-gateway.json: your node''s address and login, and your payout address' + #13#10 +
                '- forge-gateway.key: this gateway''s identity at Forge Pool' + #13#10 +
                '- secrets.env: the settings password' + #13#10 +
                '- launcher.log and forge-gateway.log' + #13#10#13#10 +
                'Choose No to keep it for a future reinstall.',
                mbConfirmation, MB_YESNO or MB_DEFBUTTON2, IDNO) = IDYES then
        DelTree(DataDir, True, True, True);
    end;
`) {
		t.Errorf("GWI-KEEP-DATA: the uninstaller does not ask, with No the default and the silent answer, before it deletes the data folder, or does not say what it holds:\n%s", uninstall)
	}
	if n := strings.Count(gwInstCode(t), "DelTree("); n != 1 {
		t.Errorf("GWI-KEEP-DATA-ONCE: the installer deletes a tree %d times", n)
	}
}

// The elevated step shows Windows' prompt, which names Windows Command Processor, not Forge
// Gateway: the Ready page and the uninstaller's question say what it is for, and the Ready page says
// before it that the rule leaves out Public networks, which neither the tray nor the status page
// can see. Afterwards the rule itself is checked, and what is missing is logged and said, with what
// it means and how to put it right, in one box with one button, which an install run with
// /SUPPRESSMSGBOXES goes past.
func TestGatewayInstallerTells(t *testing.T) {
	memo := gwInstFunc(t, "function UpdateReadyMemo(")
	if !strings.HasSuffix(memo, `
  Result := Result + 'Miners on other devices:' + NewLine +
    Space + 'The firewall rule lets them in on Private and domain networks,' + NewLine +
    Space + 'not on Public ones. Windows 11 makes new networks Public: set' + NewLine +
    Space + 'yours to Private in Windows Settings, Network & internet, in' + NewLine +
    Space + 'your connection''s properties.' + NewLine + NewLine;
  if not IsAdmin() then
    Result := Result + 'Permission:' + NewLine +
      Space + 'Windows will ask whether Windows Command Processor may make' + NewLine +
      Space + 'changes to your device. Choose Yes: Setup uses it to add the' + NewLine +
      Space + 'firewall rule that lets miners on your network connect.';
end;`) {
		t.Errorf("GWI-TELL-READY: the Ready page does not end with the networks the rule covers and what Windows' prompt is for:\n%s", memo)
	}
	if m := regexp.MustCompile(`(?m)^ConfirmUninstall=(.*?)\r?$`).FindStringSubmatch(gwInstSection(t, "Messages")); m == nil ||
		m[1] != "Are you sure you want to completely remove %1 and all of its components?%n%nIf Windows then asks whether Windows Command Processor may make changes to your device, choose Yes: that lets the uninstaller remove Forge Gateway's firewall rule." {
		t.Errorf("GWI-TELL-UNINSTALL-ASK: the uninstaller's question does not say what Windows' prompt is for: %q", m)
	}
	elevated := gwInstFunc(t, "function Elevated(Cmd: String): Boolean;")
	if !strings.Contains(elevated, "\n  Result := ShellExec('runas', ExpandConstant('{cmd}'), Cmd, '', SW_HIDE, ewWaitUntilTerminated, ResultCode);\n  if Result then\n    Log('The elevated step ran, exit code ' + IntToStr(ResultCode))\n  else\n    Log('The elevated step did not run: ' + SysErrorMessage(ResultCode));\nend;") {
		t.Errorf("GWI-TELL-ELEVATED: Elevated does not return and log whether the elevated step ran:\n%s", elevated)
	}

	install := gwInstFunc(t, "procedure CurStepChanged(CurStep: TSetupStep);")
	if !strings.Contains(install, "\n    Ran := Elevated(Cmd);\n") || strings.Count(gwInstCode(t), "ShellExec(") != 1 ||
		!regexp.MustCompile(`\n    Ran := Elevated\(Cmd\);\n(?s:.*)\n    InPlace := RulesInPlace;\n    Log\('Firewall rules in place for ' \+ RulesAccount \+ ': ' \+ IntToStr\(InPlace\) \+ ' of ' \+ IntToStr\(RuleCount\)\);\n`).MatchString(install) {
		t.Errorf("GWI-TELL-CHECK: the install does not check and log its rule after the elevated step:\n%s", install)
	}
	if !strings.Contains(install, `
    if InPlace < RuleCount then
    begin
      if Ran then
        Missing := Missing + 'Forge Gateway is installed, but Setup could not add its firewall rule.'
      else
        Missing := Missing + 'Forge Gateway is installed, but Windows did not let Setup add its ' +
          'firewall rule.';
      Missing := Missing + #13#10#13#10 +
        'Until the rule is added, miners on other devices on your network cannot connect to this ' +
        'PC. Mining from this PC itself works.' + #13#10#13#10 +
        'To add it, run this installer again and choose Yes when Windows asks whether Windows ' +
        'Command Processor may make changes to your device.';
    end;
    if Missing <> '' then
      SuppressibleMsgBox(Missing, mbError, MB_OK, IDOK);
  end;
end;`) || pascalAssignments(install, "Missing") != 5 || !strings.Contains(install, "\n    Missing := '';\n") {
		t.Errorf("GWI-TELL: a missing rule is not said, with what it means and what to do, in one box with one button:\n%s", install)
	}

	uninstall := gwInstFunc(t, "procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);")
	if !strings.Contains(uninstall, `
    InPlace := RulesInPlace;
    Log('Firewall rules left for ' + RulesAccount + ': ' + IntToStr(InPlace) + ' of ' + IntToStr(RuleCount));
    if not Ran or (InPlace > 0) then
    begin
      if Ran then
        LeftBehind := LeftBehind + 'Forge Gateway is removed, but the uninstaller could not remove ' +
          'its firewall rule.'
      else
        LeftBehind := LeftBehind + 'Forge Gateway is removed, but Windows did not let the uninstaller ' +
          'remove its firewall rule.';
      LeftBehind := LeftBehind + #13#10#13#10 + 'To remove it yourself, open Windows Security:' + #13#10 +
        '- Firewall & network protection > Advanced settings > Inbound Rules: delete the ' +
        'rule named "Forge Gateway Miner (3333) for ' + RulesAccount + '".';
    end;
`) {
		t.Errorf("GWI-TELL-UNINSTALL: what the uninstaller leaves is not said, with how to remove it:\n%s", uninstall)
	}
	if !strings.Contains(uninstall, "\n  if CurUninstallStep = usPostUninstall then\n  begin\n    if LeftBehind <> '' then\n      SuppressibleMsgBox(LeftBehind, mbError, MB_OK, IDOK);") ||
		pascalAssignments(gwInstCode(t), "LeftBehind") != 4 {
		t.Error("GWI-TELL-UNINSTALL-SHOWN: what the uninstaller leaves is not shown once it has finished, in a box with one button")
	}

	// A run with message boxes suppressed takes each question's default: Cancel, No or OK, so that it
	// ends, or goes on, rather than asks again for ever or does what the user did not choose.
	code := gwInstCode(t)
	boxes := regexp.MustCompile(`(?s)\bSuppressibleMsgBox\(.*?,\s+(mb[A-Za-z]+),\s+(MB_[A-Z]+(?: or MB_[A-Z0-9]+)?),\s+(ID[A-Z]+)\)`).FindAllStringSubmatch(code, -1)
	for _, m := range boxes {
		if want := map[string]string{"MB_RETRYCANCEL": "IDCANCEL", "MB_YESNO or MB_DEFBUTTON2": "IDNO", "MB_OK": "IDOK"}[m[2]]; want == "" || m[3] != want {
			t.Errorf("GWI-TELL-SILENT: a question with %s answers %s when message boxes are suppressed", m[2], m[3])
		}
	}
	if len(boxes) != strings.Count(code, "SuppressibleMsgBox(") || len(boxes) != 6 {
		t.Errorf("GWI-TELL-SILENT: %d of the %d message boxes are read here, want 6", len(boxes), strings.Count(code, "SuppressibleMsgBox("))
	}
	if regexp.MustCompile(`(^|[^A-Za-z])MsgBox\(`).MatchString(code) {
		t.Error("GWI-TELL-SILENT: [Code] has a MsgBox, which waits for an answer also when message boxes are suppressed")
	}
}

// windows/gateway/README.md builds the installer with the Inno Setup image the release pins, with
// no network, as CI does.
func TestGatewayInstallerImage(t *testing.T) {
	release := mustRead(t, ".github/workflows/release.yml")
	m := regexp.MustCompile(`(?m)^  INNOSETUP_IMAGE: '([^']+)'$`).FindSubmatch(release)
	if m == nil {
		t.Fatal("GWI-IMAGE: release.yml has no INNOSETUP_IMAGE")
	}
	readme := string(mustRead(t, "windows/gateway/README.md"))
	if !strings.Contains(readme, "\ndocker run --rm --network none -v \"$PWD\":/work "+string(m[1])+" /DMyAppVersion=<version> windows/gateway/forge-gateway.iss\n") {
		t.Errorf("GWI-IMAGE: windows/gateway/README.md does not compile the installer in %s with --network none", m[1])
	}
	for _, u := range regexp.MustCompile(`amake/innosetup\S*`).FindAllString(readme, -1) {
		if u != string(m[1]) {
			t.Errorf("GWI-IMAGE-SAME: windows/gateway/README.md runs %s, not the pinned %s", u, m[1])
		}
	}
	for _, want := range []string{
		`CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=<version>" -o windows/gateway/bin/forge-gateway.exe ./cmd/forge-gateway`,
		`(cd windows/gateway/launcher && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-H=windowsgui -s -w -X main.version=<version>" -o ../bin/forge-gateway-tray.exe .)`,
		`cp <forge-gateway repository>/LICENSE windows/gateway/bin/LICENSE.txt`,
	} {
		if !strings.Contains(readme, "\n"+want+"\n") {
			t.Errorf("GWI-IMAGE-BUILD: windows/gateway/README.md does not build bin as the installer takes it:\n%s", want)
		}
	}
}

// Forge Gateway 1.0.0 on Windows was a service (forge-gateway.exe install): it starts with Windows
// as LocalSystem and holds port 3333, so the installed Forge Gateway could not start beside it.
// Setup looks for it before it closes or replaces anything. One that runs a program in this
// install's folder is refused: Setup cannot replace it. Otherwise Setup asks, No the default and the
// answer of a silent run, which changes nothing; Yes stops it through Windows (its own clean stop),
// then deletes it, in the one elevated step, before the firewall commands, and Setup waits for it
// to stop and says if it could not be removed. The uninstaller does the same for a service that
// runs a program from the install's folder.
func TestGatewayInstallerService(t *testing.T) {
	code := gwInstCode(t)
	if !strings.Contains(code, "\n  ServiceName = 'ForgeGateway';\n  ServiceKey = 'SYSTEM\\CurrentControlSet\\Services\\ForgeGateway';\n") {
		t.Error("GWI-SERVICE-NAME: [Code] does not name the service ForgeGateway, with its key under SYSTEM\\CurrentControlSet\\Services")
	}
	for _, c := range []struct{ code, decl, want string }{
		{"GWI-SERVICE-INSTALLED", "function ServiceInstalled: Boolean;", "\nbegin\n  Result := RegKeyExists(HKLM, ServiceKey);\nend;"},
		{"GWI-SERVICE-PROGRAM", "function ServiceProgram: String;", "\n  Result := '';\n  if RegQueryStringValue(HKLM, ServiceKey, 'ImagePath', Image) then\n    Result := Image;\nend;"},
		{"GWI-SERVICE-IN-FOLDER", "function ServiceInThisFolder(Image: String): Boolean;", "\nbegin\n  Result := Pos(InstallFolder, LongPath(ServiceExe(Image))) = 1;\nend;"},
		{"GWI-SERVICE-EXE", "function ServiceExe(Image: String): String;", `
begin
  Image := Trim(Image);
  if Copy(Image, 1, 1) = '"' then
  begin
    Image := Copy(Image, 2, Length(Image));
    I := Pos('"', Image);
  end else
  begin
    I := Pos('.exe', Lowercase(Image));
    if I > 0 then
      I := I + 4;
  end;
  if I > 0 then
    Image := Copy(Image, 1, I - 1);
  Result := Image;
end;`},
		{"GWI-SERVICE-STOPPED", "function ServiceStopped: Boolean;", `
begin
  Result := False;
  Manager := OpenSCManager('', 'ServicesActive', SC_MANAGER_CONNECT);
  if Manager = 0 then
  begin
    Log('Cannot ask Windows about the ForgeGateway service: ' + SysErrorMessage(DLLGetLastError));
    exit;
  end;
  Service := OpenService(Manager, ServiceName, SERVICE_QUERY_STATUS);
  if Service = 0 then
    Result := DLLGetLastError = ERROR_SERVICE_DOES_NOT_EXIST
  else
  begin
    if QueryServiceStatus(Service, Status) then
      Result := Status.CurrentState = SERVICE_STOPPED;
    CloseServiceHandle(Service);
  end;
  CloseServiceHandle(Manager);
end;`},
		{"GWI-SERVICE-MARKED", "function ServiceMarkedForDeletion: Boolean;", "\nbegin\n  Result := RegQueryDWordValue(HKLM, ServiceKey, 'DeleteFlag', Flag) and (Flag = 1);\nend;"},
		{"GWI-SERVICE-ORDER", "function ServiceRemoval: String;", "\nbegin\n  Result := 'sc.exe stop ' + ServiceName + ' >nul 2>&1 & sc.exe delete ' + ServiceName + ' >nul 2>&1 & ';\nend;"},
		{"GWI-SERVICE-WAIT", "function ServiceRemoved(Ran: Boolean): Boolean;", `
begin
  Seconds := 0;
  Stopped := ServiceStopped;
  while Ran and not Stopped and (Seconds < StopWait) do
  begin
    Waiting;
    Seconds := Seconds + 1;
    Stopped := ServiceStopped;
  end;
  Result := False;
  if not Stopped then
    Log('The Forge Gateway service still runs after ' + IntToStr(Seconds) + ' s')
  else if ServiceInstalled and not ServiceMarkedForDeletion then
    Log('The Forge Gateway service is still installed')
  else
  begin
    Log('The Forge Gateway service stopped within ' + IntToStr(Seconds) + ' s');
    Result := True;
  end;
end;`},
	} {
		if body := gwInstFunc(t, c.decl); !strings.HasSuffix(body, c.want) {
			t.Errorf("%s: %s is not as pinned:\n%s", c.code, c.decl, body)
		}
	}
	// The Windows calls as Windows declares them, the Unicode one where there are two, and
	// SERVICE_STATUS's seven DWORDs with the state second.
	for _, decl := range []string{
		"function OpenSCManager(Machine, Database: String; Access: Cardinal): Cardinal;\n  external 'OpenSCManagerW@advapi32.dll stdcall';",
		"function OpenService(Manager: Cardinal; Name: String; Access: Cardinal): Cardinal;\n  external 'OpenServiceW@advapi32.dll stdcall';",
		"function QueryServiceStatus(Service: Cardinal; var Status: TServiceStatus): Bool;\n  external 'QueryServiceStatus@advapi32.dll stdcall';",
		"function CloseServiceHandle(Handle: Cardinal): Bool;\n  external 'CloseServiceHandle@advapi32.dll stdcall';",
		"\n  TServiceStatus = record\n    ServiceType, CurrentState, ControlsAccepted, Win32ExitCode, ServiceSpecificExitCode, CheckPoint, WaitHint: Cardinal;\n  end;\n",
		"\n  SC_MANAGER_CONNECT = $0001;\n  SERVICE_QUERY_STATUS = $0004;\n  SERVICE_STOPPED = 1;\n  ERROR_SERVICE_DOES_NOT_EXIST = 1060;\n",
	} {
		if !strings.Contains(code, decl) {
			t.Errorf("GWI-SERVICE-CALLS: [Code] lacks, as Windows declares it:\n%s", decl)
		}
	}

	// Setup: before anything is closed; its own folder refused; Yes alone removes it.
	prepare := gwInstFunc(t, "function PrepareToInstall(var NeedsRestart: Boolean): String;")
	if i, j := strings.Index(prepare, "\n  if ServiceInstalled then\n"), strings.Index(prepare, "StopForgeGateway"); i < 0 || j < 0 || i > j {
		t.Errorf("GWI-SERVICE-FIRST: Setup does not look at the service before it closes Forge Gateway:\n%s", prepare)
	}
	if !strings.Contains(prepare, `
  Result := '';
  RemoveService := False;
  if ServiceInstalled then
  begin
    Image := ServiceProgram;
    Log('A ForgeGateway Windows service is installed: ' + Image);
    if ServiceInThisFolder(Image) then
    begin
      Result := 'Forge Gateway runs as a Windows service from this install''s folder, so Setup ' +
        'changed nothing. Remove the service first: in a Command Prompt run as administrator, run ' +
        'sc stop ForgeGateway, then sc delete ForgeGateway. Then run Setup again.';
      exit;
    end;
`) {
		t.Errorf("GWI-SERVICE-OWN-FOLDER: a service that runs a program in this install's folder is not refused first, with what to do:\n%s", prepare)
	}
	if !strings.Contains(prepare, `
    if SuppressibleMsgBox('Forge Gateway is installed on this PC as a Windows service (ForgeGateway):' + #13#10 +
      Image + #13#10#13#10 +
      'It starts with Windows and uses port 3333, so the Forge Gateway you are installing could not ' +
      'start beside it. Setup can stop it cleanly and remove it, in the step that adds the firewall ' +
      'rule, so Windows asks only once. Its config file and its key stay where they are; enter the ' +
      'same node and payout address in Settings afterwards.' + #13#10#13#10 +
      'Stop and remove the service? No changes nothing.',
      mbConfirmation, MB_YESNO or MB_DEFBUTTON2, IDNO) = IDYES then
    begin
      RemoveService := True;
      Log('The ForgeGateway service is stopped and removed in the elevated step');
    end else
    begin
      Log('The ForgeGateway service stays: Setup changes nothing');
      Result := 'Forge Gateway''s Windows service is still installed, so Setup changed nothing. Run ' +
        'Setup again and choose Yes to remove it, or remove it yourself: in a Command Prompt run as ' +
        'administrator, run sc stop ForgeGateway, then sc delete ForgeGateway.';
      exit;
    end;
  end;
`) {
		t.Errorf("GWI-SERVICE-ASK: Setup does not ask, No the default and the silent answer, and change nothing on No:\n%s", prepare)
	}
	if n := pascalAssignments(code, "RemoveService"); n != 2 {
		t.Errorf("GWI-SERVICE-ONLY-YES: RemoveService is set %d times, want 2 (False, then True on Yes)", n)
	}

	// Install: removed only with the Yes, before any netsh command, then waited for.
	install := gwInstFunc(t, "procedure CurStepChanged(CurStep: TSetupStep);")
	if !strings.Contains(install, `
    Cmd := '/c ';
    if RemoveService then
      Cmd := Cmd + ServiceRemoval;
    Cmd := Cmd +
      'netsh advfirewall firewall delete rule name="Forge Gateway" >nul 2>&1 & ' +
      FirewallRule('Forge Gateway Miner (3333)', 'forge-gateway.exe', '3333', 'private,domain');
    Ran := Elevated(Cmd);
    Missing := '';
    if RemoveService then
      if not ServiceRemoved(Ran) then
        Missing := 'Forge Gateway is installed, but Setup could not remove the ForgeGateway Windows ' +
          'service, which keeps port 3333, so Forge Gateway cannot start until it is gone. In a ' +
          'Command Prompt run as administrator, run sc stop ForgeGateway, then sc delete ForgeGateway.' + #13#10#13#10;
    InPlace := RulesInPlace;
`) {
		t.Errorf("GWI-SERVICE-INSTALL: the install's elevated step does not stop, then delete, the service before its firewall commands only when the user said Yes, or does not wait for it and say when it could not be removed:\n%s", install)
	}
	// Uninstall: only a service whose program is in this install's folder.
	uninstall := gwInstFunc(t, "procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);")
	if !strings.Contains(uninstall, `
    OwnService := ServiceInstalled and ServiceInThisFolder(ServiceProgram);
    Cmd := '/c ';
    if OwnService then
      Cmd := Cmd + ServiceRemoval;
    Cmd := Cmd +
      FirewallRemove('Forge Gateway Miner (3333)') +
      'netsh advfirewall firewall delete rule name="Forge Gateway" & ';
    Ran := Elevated(Cmd);
    if OwnService then
      if not ServiceRemoved(Ran) then
        LeftBehind := 'Forge Gateway is removed, but the uninstaller could not remove the ForgeGateway ' +
          'Windows service, which runs forge-gateway.exe from its folder. In a Command Prompt run as ' +
          'administrator, run sc stop ForgeGateway, then sc delete ForgeGateway.' + #13#10#13#10;
`) {
		t.Errorf("GWI-SERVICE-UNINSTALL: the uninstaller does not remove, before the rules, a service that runs a program from this install's folder, or does not say when it could not:\n%s", uninstall)
	}
	if n := strings.Count(code, "ServiceRemoval"); n != 3 {
		t.Errorf("GWI-SERVICE-ONLY-HERE: ServiceRemoval appears %d times, want its declaration and the two steps", n)
	}
	// Every command the texts give names the service by ServiceName.
	for _, m := range regexp.MustCompile(`sc (?:stop|delete) (\w+)`).FindAllStringSubmatch(pascalLiterals(code), -1) {
		if m[1] != "ForgeGateway" {
			t.Errorf("GWI-SERVICE-TEXT-NAME: a text says %q; the service is ForgeGateway", m[0])
		}
	}
}

// Forge Gateway 1.0.0's guide had users add a rule named "Forge Gateway": TCP 3333 for any program.
// The installer's rule lets in only forge-gateway.exe, and the install and the uninstall both delete
// the old one; nothing adds it.
func TestGatewayInstallerOldRule(t *testing.T) {
	install := gwInstFunc(t, "procedure CurStepChanged(CurStep: TSetupStep);")
	uninstall := gwInstFunc(t, "procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);")
	if strings.Count(install, `'netsh advfirewall firewall delete rule name="Forge Gateway" >nul 2>&1 & '`) != 1 {
		t.Error("GWI-OLD-RULE-INSTALL: the install does not delete the rule named Forge Gateway")
	}
	if strings.Count(uninstall, `'netsh advfirewall firewall delete rule name="Forge Gateway" & '`) != 1 {
		t.Error("GWI-OLD-RULE-UNINSTALL: the uninstall does not delete the rule named Forge Gateway")
	}
	code := gwInstCode(t)
	if strings.Contains(code, `add rule name="Forge Gateway"`) || strings.Count(code, "add rule name=") != 1 ||
		regexp.MustCompile(`Firewall(?:Rule|Remove)\('Forge Gateway'`).MatchString(code) {
		t.Error("GWI-OLD-RULE-NOT-ADDED: the installer adds a rule other than its own, or one named Forge Gateway")
	}
}

// Forge Solo also takes port 3333, and its TIDES mode is this gateway: the Ready page says so when
// Forge Solo is installed for this account (its uninstall entry, by windows/forge-solo.iss's AppId)
// or runs on this PC (its launcher's mutex). It only says so: Setup goes on.
func TestGatewayInstallerSoloNote(t *testing.T) {
	id := regexp.MustCompile(`(?m)^AppId=\{(\{[0-9A-F-]+\})\r?$`).FindStringSubmatch(installerSection(t, "Setup"))
	mutex := regexp.MustCompile(`(?m)^  RunningMutex = ('[^']*');$`).FindStringSubmatch(pascalCode(installerSection(t, "Code")))
	if id == nil || mutex == nil {
		t.Fatal("GWI-SOLO-NOTE: windows/forge-solo.iss has no AppId or RunningMutex this test can read")
	}
	code := gwInstCode(t)
	for _, want := range []string{
		"\n  ForgeSoloUninstallKey = 'Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\" + id[1] + "_is1';\n",
		"\n  ForgeSoloMutex = " + mutex[1] + ";\n",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("GWI-SOLO-NOTE-WHICH: [Code] lacks %q, from windows/forge-solo.iss", want)
		}
	}
	memo := gwInstFunc(t, "function UpdateReadyMemo(")
	if !strings.Contains(memo, `
  if RegKeyExists(HKCU, ForgeSoloUninstallKey) or MutexHeld(ForgeSoloMutex) then
    Result := Result + 'Forge Solo:' + NewLine +
      Space + 'Forge Solo is on this PC too. Both use port 3333, so only' + NewLine +
      Space + 'one of the two can run at a time; Forge Solo''s TIDES mode is' + NewLine +
      Space + 'the same gateway, built in.' + NewLine + NewLine;
`) {
		t.Errorf("GWI-SOLO-NOTE: the Ready page does not say that Forge Solo is on this PC too:\n%s", memo)
	}
	if strings.Count(code, "ForgeSoloUninstallKey") != 2 || strings.Count(code, "ForgeSoloMutex") != 2 || regexp.MustCompile(`\b(Abort|exit)\b`).MatchString(memo) {
		t.Error("GWI-SOLO-NOTE-GOES-ON: Forge Solo is looked for elsewhere than on the Ready page, or stops Setup")
	}
}

// The code that finds, closes and waits for the programs, the firewall rule's commands and the
// account name's check are Forge Solo's, whose tests pin them statement by statement, with its
// name changed. A fix to one installer is a fix to both.
func TestGatewayInstallerSharesForgeSolosCode(t *testing.T) {
	for _, d := range [][2]string{
		{"function RuleName(Base, Account: String): String;", ""},
		{"function UsableAccountName(Name: String): Boolean;", ""},
		{"function FirewallRule(Base, Exe, Port, Profile: String): String;", ""},
		{"function FirewallRemove(Base: String): String;", ""},
		{"function RuleInPlace(Name: String): Boolean;", ""},
		{"function LongPath(Path: String): String;", ""},
		{"function ProgramPath(ProcessID: Cardinal): String;", ""},
		{"function InstallFolder: String;", ""},
		{"function ExeName(Entry: TProcessEntry): String;", ""},
		{"function ProgramsRunning: String;", ""},
		{"function AskForgeSoloToQuit: Integer;", "function AskForgeGatewayToQuit: Integer;"},
		{"procedure Waiting;", ""},
		{"function MutexHeld(Name: String): Boolean;", ""},
		{"function ForgeSoloRuns: Boolean;", "function ForgeGatewayRuns: Boolean;"},
		{"function NoOtherForgeSolo: Boolean;", "function NoOtherForgeGateway: Boolean;"},
		{"procedure InitializeWizard;", ""},
		{"function MemoPart(S, NewLine: String): String;", ""},
	} {
		gw := d[1]
		if gw == "" {
			gw = d[0]
		}
		gwInstSame(t, "GWI-SHARED", d[0], gw)
	}
	// The constants, records and Windows calls they use, from WM_CLOSE to the last declaration:
	// Forge Solo's, with its mutex the tray app's and its wait a minute.
	block := func(code string) string {
		i, j := strings.Index(code, "\n  WM_CLOSE = $0010;"), strings.Index(code, "external 'PostQuitMessage@user32.dll stdcall';")
		if i < 0 || j < i {
			return ""
		}
		return code[i:j]
	}
	solo := strings.NewReplacer("ForgeSoloRunning", "ForgeGatewayRunning", "\n  StopWait = 120;\n", "\n  StopWait = 60;\n").Replace(block(pascalCode(installerSection(t, "Code"))))
	if gw := block(gwInstCode(t)); solo == "" || gw != solo {
		t.Errorf("GWI-SHARED-DECLARATIONS: the constants, records and Windows calls are not Forge Solo's:\n%s\n--- Forge Solo's:\n%s", gw, solo)
	}
}

// The installer's texts and its build notes use plain punctuation, as Forge Solo's do.
func TestGatewayInstallerPlainText(t *testing.T) {
	docs := []docText{
		{"windows/gateway/forge-gateway.iss", gwInstScript(t)},
		{"windows/gateway/README.md", string(mustRead(t, "windows/gateway/README.md"))},
	}
	checkPlainPunctuation(t, docs)
	for _, d := range docs {
		if strings.ContainsRune(d.text, '\u2013') {
			t.Errorf("GWI-PLAIN-EN-DASH: %s has an en-dash", d.name)
		}
	}
}
