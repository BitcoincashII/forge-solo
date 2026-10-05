; Forge Solo: single-exe Windows installer. Bundles the launcher, the stratum and api services, the
; migrator, the BCH2 + 1175 nodes and the dashboard, and PostgreSQL only for an account that has data
; of 1.0.12 or before to move (HasOldData). Per-user install (no admin).
#define MyAppName "Forge Solo"
; Overridable from the command line so CI can stamp the tag it is building:
;   iscc /DMyAppVersion=1.0.10 forge-solo.iss
#ifndef MyAppVersion
  #define MyAppVersion "1.0.13"
#endif
#define MyAppPublisher "BCH2 Team"
#define MyAppURL "https://github.com/BitcoincashII/forge-solo"
#define MyAppExe "forge-solo.exe"

[Setup]
AppId={{9F2C7A31-4B6E-4D8A-9C1F-3E5A7B0D2C64}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
AppSupportURL={#MyAppURL}/issues
DefaultDirName={localappdata}\Programs\ForgeSolo
DisableProgramGroupPage=yes
DisableDirPage=yes
PrivilegesRequired=lowest
; Every program it installs is 64-bit (x64). Setup, a 32-bit program, also installed on 32-bit
; Windows and on Windows 10 on ARM, which run no x64 programs, and Forge Solo could not start there.
; Windows 11 on ARM runs them. No 64-bit install mode: the elevated step keeps the 32-bit cmd, netsh
; and PowerShell it has always used.
ArchitecturesAllowed=x64compatible
OutputDir=.
OutputBaseFilename=ForgeSolo-Setup-{#MyAppVersion}
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
SetupIconFile=forge-solo.ico
UninstallDisplayIcon={app}\{#MyAppExe}
UninstallDisplayName={#MyAppName}
; Held by the launcher of 1.0.13 and later while it runs (runningMutex in
; launcher/instance_windows.go). Setup and the uninstaller ask for Forge Solo to be closed first, so
; that it stops both nodes cleanly, rather than have its files closed under it. 1.0.12 and earlier
; hold none: StopForgeSolo, in [Code], closes them. Test builds of 1.0.13 held the unprefixed name.
AppMutex=ForgeSoloRunning,Global\ForgeSoloRunning

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Messages]
; Shown instead of installing where Forge Solo cannot run (ArchitecturesAllowed).
WindowsVersionNotSupported=Forge Solo needs 64-bit Windows: Windows 10 or 11 on an x64 PC, or Windows 11 on ARM.
; The uninstaller's elevated step gets Windows' prompt for Windows Command Processor, not for Forge
; Solo: say beforehand what it is for.
ConfirmUninstall=Are you sure you want to completely remove %1 and all of its components?%n%nIf Windows then asks whether Windows Command Processor may make changes to your device, choose Yes: that lets the uninstaller remove Forge Solo's firewall rules and Defender exclusions.

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; GroupDescription: "Additional icons:"
; Mining stops when the app is closed, and nothing restarted it after a reboot -- a machine
; that rebooted overnight simply stopped earning until someone noticed. Opt-in, per-user
; (HKCU needs no admin), and removed with the app.
Name: "startup"; Description: "Start Forge Solo when I sign in"; GroupDescription: "Startup:"; Flags: unchecked

[Registry]
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "ForgeSolo"; ValueData: """{app}\{#MyAppExe}"""; Flags: uninsdeletevalue; Tasks: startup
; An update with the box unticked turns the sign-in start off; without this it stayed on.
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: none; ValueName: "ForgeSolo"; Flags: deletevalue; Tasks: not startup

[Files]
Source: "bin\*"; DestDir: "{app}"; Flags: ignoreversion
; PostgreSQL only moves the data of Forge Solo 1.0.12 and before into forgesolo.db, once: it is
; installed only for an account that has that data. Once the move is done and checked, the launcher
; removes it again.
Source: "pgsql\*"; DestDir: "{app}\pgsql"; Flags: ignoreversion recursesubdirs createallsubdirs; Check: HasOldData
Source: "..\web\dist\*"; DestDir: "{app}\web"; Flags: ignoreversion recursesubdirs createallsubdirs

[InstallDelete]
; What earlier versions installed and this one no longer uses: the PostgreSQL schema, and PostgreSQL
; itself for an account with no data of an earlier version (a fresh install, or the old data deleted).
Type: files; Name: "{app}\init-db.sql"
Type: filesandordirs; Name: "{app}\pgsql"; Check: not HasOldData

[UninstallDelete]
; Installed by an earlier version, or by this one for a move.
Type: filesandordirs; Name: "{app}\pgsql"

[Icons]
Name: "{userprograms}\Forge Solo"; Filename: "{app}\{#MyAppExe}"
Name: "{userprograms}\Uninstall Forge Solo"; Filename: "{uninstallexe}"
Name: "{userdesktop}\Forge Solo"; Filename: "{app}\{#MyAppExe}"; Tasks: desktopicon

[Run]
Filename: "{app}\{#MyAppExe}"; Description: "Launch Forge Solo now"; Flags: nowait postinstall skipifsilent

[Code]
// One elevated step (a single UAC prompt) at install:
//  - inbound TCP 3333  : a LAN Bitaxe/ASIC can reach the miner (private/domain only)
//  - inbound TCP 3335  : rented hashpower (NiceHash / MiningRigRentals) on the rental port,
//                        as on Umbrel and Linux (private/domain only)
//  - inbound TCP 8339  : the BCH2 node accepts incoming peers (any profile)
//  - inbound TCP 25360 : the 1175 node accepts incoming peers (any profile)
//  - Defender exclusions for the folders written constantly (both nodes' blocks and chainstate)
//    so it stops rescanning them on every write, the main cause of disk thrash and freezes on a
//    laptop. Not the whole data folder: a folder the user can write to and Defender never scans is
//    a place any other program could hide files. An upgrade removes the whole-folder exclusion
//    earlier versions added, and the one for the database of 1.0.12 and before (pgdata), which
//    nothing writes now.
// Mining from THIS PC (127.0.0.1:3333) needs no rule at all.
//
// Windows' prompt for that step names Windows Command Processor, not Forge Solo, so the Ready page
// and the uninstaller's question say beforehand what it is for. Afterwards the rules themselves are
// checked: if the prompt was refused, or the step failed, Setup says what is missing, what that
// means and how to put it right, and logs it. Without the rules, miners on the network cannot
// connect, and nothing else would say why.

// PSQuote quotes S for PowerShell. A single-quoted string ends at the first single quote unless it
// is doubled, and a Windows user name can have one (C:\Users\O'Brien). PowerShell takes the
// typographic quotes U+2018 to U+201B for single quotes too.
function PSQuote(S: String): String;
begin
  StringChangeEx(S, '''', '''''', True);
  StringChangeEx(S, #$2018, #$2018#$2018, True);
  StringChangeEx(S, #$2019, #$2019#$2019, True);
  StringChangeEx(S, #$201A, #$201A#$201A, True);
  StringChangeEx(S, #$201B, #$201B#$201B, True);
  Result := '''' + S + '''';
end;

// RuleName is the name of one of the firewall rules of the install for the Windows account called
// Account: the base name, for that account. Two accounts on one PC can each install Forge Solo, and
// each install's rules let in its own programs; with one name for all, installing for one account
// replaced the other's rules, and its miners could no longer connect. Earlier releases used the
// base name alone (with no program before 1.0.13): rules of that name are removed.
function RuleName(Base, Account: String): String;
begin
  Result := Base + ' for ' + Account;
end;

const
  // Where the account name the rules carry is kept, so that the uninstaller removes those rules,
  // also after the account has been renamed.
  RulesKey = 'Software\ForgeSolo';
  RulesValue = 'FirewallRulesAccount';

var
  // The account name this install's rules carry, and at install the one an earlier install kept.
  RulesAccount, PreviousRulesAccount: String;

// UsableAccountName is whether Name, the account name an earlier install kept for its rules, can go
// into the commands of the elevated step. Any program running as this account can change the kept
// name, and those commands run as administrator: a double quote would end the rule's name and run
// what follows as a command, cmd replaces %name% with an environment variable, and a control
// character can cut the commands short. No account name is longer than 256 characters.
function UsableAccountName(Name: String): Boolean;
var I: Integer;
begin
  Result := Length(Name) <= 256;
  for I := 1 to Length(Name) do
    if (Ord(Name[I]) < 32) or (Name[I] = '"') or (Name[I] = '%') then
      Result := False;
end;

// KeptRulesAccount is the account name an earlier install kept for its rules, or '' if none is
// kept or it cannot go into the elevated step's commands.
function KeptRulesAccount: String;
var Name: String;
begin
  Result := '';
  if RegQueryStringValue(HKCU, RulesKey, RulesValue, Name) then
  begin
    if UsableAccountName(Name) then
      Result := Name
    else
      Log('The account name kept for the firewall rules is not used: it is too long, or has a ' +
        'character that would change the elevated step''s commands');
  end;
end;

// FirewallRule is the commands that put this install's rule in place, named for RulesAccount, after
// removing the old ones: of the base name, and of the name an earlier install kept, if the account
// has been renamed since.
function FirewallRule(Base, Exe, Port, Profile: String): String;
begin
  Result := 'netsh advfirewall firewall delete rule name="' + Base + '" >nul 2>&1 & ';
  if (PreviousRulesAccount <> '') and (PreviousRulesAccount <> RulesAccount) then
    Result := Result + 'netsh advfirewall firewall delete rule name="' + RuleName(Base, PreviousRulesAccount) + '" >nul 2>&1 & ';
  Result := Result +
    'netsh advfirewall firewall delete rule name="' + RuleName(Base, RulesAccount) + '" >nul 2>&1 & ' +
    'netsh advfirewall firewall add rule name="' + RuleName(Base, RulesAccount) + '" dir=in action=allow program="' +
      ExpandConstant('{app}') + '\' + Exe + '" protocol=TCP localport=' + Port + ' profile=' + Profile + ' & ';
end;

// FirewallRemove is the commands that remove this install's rule, and an old one of the base name.
function FirewallRemove(Base: String): String;
begin
  Result :=
    'netsh advfirewall firewall delete rule name="' + Base + '" & ' +
    'netsh advfirewall firewall delete rule name="' + RuleName(Base, RulesAccount) + '" & ';
end;

// RuleInPlace is whether Windows Firewall has a rule of this name. Reading the rules needs no
// permission.
function RuleInPlace(Name: String): Boolean;
var ResultCode: Integer;
begin
  Result := Exec(ExpandConstant('{sys}\netsh.exe'), 'advfirewall firewall show rule name="' + Name + '"',
    '', SW_HIDE, ewWaitUntilTerminated, ResultCode) and (ResultCode = 0);
end;

const
  // RuleCount is how many rules the install puts in place (FirewallRule).
  RuleCount = 4;

// RulesInPlace is how many of this install's firewall rules Windows Firewall has.
function RulesInPlace: Integer;
begin
  Result := 0;
  if RuleInPlace(RuleName('Forge Solo Miner (3333)', RulesAccount)) then Result := Result + 1;
  if RuleInPlace(RuleName('Forge Solo Rentals (3335)', RulesAccount)) then Result := Result + 1;
  if RuleInPlace(RuleName('Forge Solo BCH2 P2P (8339)', RulesAccount)) then Result := Result + 1;
  if RuleInPlace(RuleName('Forge Solo 1175 P2P (25360)', RulesAccount)) then Result := Result + 1;
end;

// Elevated runs Cmd, the commands of the one elevated step, and logs whether it ran. It is False
// if it did not: Windows' prompt was refused, or no administrator gave permission.
function Elevated(Cmd: String): Boolean;
var ResultCode: Integer;
begin
  Result := ShellExec('runas', ExpandConstant('{cmd}'), Cmd, '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  if Result then
    Log('The firewall and Defender step ran, exit code ' + IntToStr(ResultCode))
  else
    Log('The firewall and Defender step did not run: ' + SysErrorMessage(ResultCode));
end;

// DefenderPaths lists, quoted for PowerShell, the data folders written constantly.
function DefenderPaths(DataDir: String): String;
begin
  Result := PSQuote(DataDir + '\bch2\blocks') + ', ' + PSQuote(DataDir + '\bch2\chainstate') + ', ' +
            PSQuote(DataDir + '\elevenseventyfive\blocks') + ', ' +
            PSQuote(DataDir + '\elevenseventyfive\chainstate');
end;

// OldDefenderPaths lists, quoted for PowerShell, the folders earlier versions excluded and this
// one does not: the whole data folder, and the database of 1.0.12 and before.
function OldDefenderPaths(DataDir: String): String;
begin
  Result := PSQuote(DataDir) + ', ' + PSQuote(DataDir + '\pgdata');
end;

// HasOldData is whether this account has the data of Forge Solo 1.0.12 or before, which the
// bundled PostgreSQL moves into forgesolo.db once.
function HasOldData: Boolean;
begin
  Result := FileExists(ExpandConstant('{userappdata}\ForgeSolo\pgdata\PG_VERSION'));
end;

// Forge Solo running while it is updated or removed.
//
// Forge Solo 1.0.12 and earlier hold no mutex (AppMutex), so Setup did not see them run, and left
// them to Windows' Restart Manager. It gave up after a few seconds, while Forge Solo was still
// stopping its nodes and its database, and Setup stopped with an error (a silent install rolled
// back). Before Setup or the uninstaller touches a file, it now closes a Forge Solo running from
// this install's folder for this account the way its tray's Quit does, and waits for every program
// in that folder to stop. Restart Manager then finds nothing to close. Nothing is ever ended by
// force: a node ended while it writes has to sync again.

const
  WM_CLOSE = $0010;
  WM_QUIT = $0012;
  PM_REMOVE = 1;
  TH32CS_SNAPPROCESS = $2;
  PROCESS_QUERY_LIMITED_INFORMATION = $1000;
  INVALID_HANDLE = $FFFFFFFF;
  // The size of TProcessEntry, PROCESSENTRY32W as a 32-bit program has it (Setup is one): nine
  // fields of 4 bytes and 260 characters of 2.
  ProcessEntrySize = 556;
  // How long Forge Solo gets to stop, in seconds, before the user is asked whether to wait again.
  // It stops the miner first, then both nodes and its database, and each has a time of its own to
  // finish writing. Usually it is done in a few seconds.
  StopWait = 120;

type
  TProcessEntry = record
    Size, Usage, ProcessID, DefaultHeapID, ModuleID, Threads, ParentProcessID: Cardinal;
    PriClassBase: Longint;
    Flags: Cardinal;
    ExeFile: array[0..259] of Char;
  end;
  // MSG, for the uninstaller's window while it waits.
  TWindowMessage = record
    Window: HWND;
    MessageID: Cardinal;
    WParam, LParam: Longint;
    Time: Cardinal;
    X, Y: Longint;
    Spare: Cardinal;
  end;

function CreateToolhelp32Snapshot(Flags, ProcessID: Cardinal): Cardinal;
  external 'CreateToolhelp32Snapshot@kernel32.dll stdcall';
function Process32First(Snapshot: Cardinal; var Entry: TProcessEntry): Bool;
  external 'Process32FirstW@kernel32.dll stdcall';
function Process32Next(Snapshot: Cardinal; var Entry: TProcessEntry): Bool;
  external 'Process32NextW@kernel32.dll stdcall';
function OpenProcess(Access: Cardinal; Inherit: Bool; ProcessID: Cardinal): Cardinal;
  external 'OpenProcess@kernel32.dll stdcall';
function QueryFullProcessImageName(Process, Flags: Cardinal; Name: String; var Size: Cardinal): Bool;
  external 'QueryFullProcessImageNameW@kernel32.dll stdcall';
function GetLongPathName(Path, LongPath: String; Size: Cardinal): Cardinal;
  external 'GetLongPathNameW@kernel32.dll stdcall';
function CloseHandle(Handle: Cardinal): Bool;
  external 'CloseHandle@kernel32.dll stdcall';
function GetCurrentProcessId: Cardinal;
  external 'GetCurrentProcessId@kernel32.dll stdcall';
function FindWindowEx(Parent, After: HWND; ClassName: String; WindowName: Cardinal): HWND;
  external 'FindWindowExW@user32.dll stdcall';
function GetWindowThreadProcessId(Wnd: HWND; var ProcessID: Cardinal): Cardinal;
  external 'GetWindowThreadProcessId@user32.dll stdcall';
function PeekMessage(var Msg: TWindowMessage; Wnd: HWND; First, Last, Remove: Cardinal): Bool;
  external 'PeekMessageW@user32.dll stdcall';
function TranslateMessage(var Msg: TWindowMessage): Bool;
  external 'TranslateMessage@user32.dll stdcall';
function DispatchMessage(var Msg: TWindowMessage): Longint;
  external 'DispatchMessageW@user32.dll stdcall';
procedure PostQuitMessage(ExitCode: Longint);
  external 'PostQuitMessage@user32.dll stdcall';

// LongPath is Path with any short (8.3) names in their long form, in lower case. The launcher starts
// PostgreSQL by its short path where the long one has characters PostgreSQL cannot take.
function LongPath(Path: String): String;
var Long: String; N: Cardinal;
begin
  Long := StringOfChar(' ', 1024);
  N := GetLongPathName(Path, Long, 1024);
  if (N > 0) and (N < 1024) then
    Path := Copy(Long, 1, N);
  Result := AnsiLowercase(Path);
end;

// ProgramPath is the program the process ProcessID runs, as LongPath gives it, or '' if it cannot be
// read: the process has exited, or it is another account's.
function ProgramPath(ProcessID: Cardinal): String;
var Process, Size: Cardinal; Name: String;
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
end;

// InstallFolder is this install's folder as LongPath gives it, with a backslash at the end. It is
// in this account's profile: a program running from it is this account's.
function InstallFolder: String;
begin
  Result := AddBackslash(LongPath(ExpandConstant('{app}')));
end;

// ExeName is the file name of the program in Entry, as Windows gives it.
function ExeName(Entry: TProcessEntry): String;
var I: Integer;
begin
  Result := '';
  I := 0;
  while (I <= 259) and (Entry.ExeFile[I] <> #0) do
  begin
    Result := Result + Entry.ExeFile[I];
    I := I + 1;
  end;
end;

// ProgramsRunning names the programs running from this install's folder, each once: the launcher,
// the miner, the API, both nodes, and PostgreSQL in pgsql. The uninstaller itself is not one.
function ProgramsRunning: String;
var Snapshot: Cardinal; Entry: TProcessEntry; Folder, Path, Name: String;
begin
  Result := '';
  Folder := InstallFolder;
  Snapshot := CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0);
  if Snapshot = INVALID_HANDLE then
  begin
    Log('Cannot list the running programs: Forge Solo is not looked for');
    exit;
  end;
  try
    Entry.Size := ProcessEntrySize;
    if Process32First(Snapshot, Entry) then
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
end;

// AskForgeSoloToQuit asks each Forge Solo running from this install's folder to quit as its tray's
// Quit does, and returns how many it asked: its tray window (SystrayClass) gets WM_CLOSE. On it,
// 1.0.12 and earlier stop the miner, both nodes and the database, and exit; 1.0.13 and later do the
// same. (Their Quit menu items have a different number in each version.)
function AskForgeSoloToQuit: Integer;
var Wnd: HWND; ProcessID: Cardinal; Launcher: String;
begin
  Result := 0;
  Launcher := InstallFolder + AnsiLowercase('{#MyAppExe}');
  Wnd := FindWindowEx(0, 0, 'SystrayClass', 0);
  while Wnd <> 0 do
  begin
    ProcessID := 0;
    GetWindowThreadProcessId(Wnd, ProcessID);
    if ProgramPath(ProcessID) = Launcher then
    begin
      PostMessage(Wnd, WM_CLOSE, 0, 0);
      Result := Result + 1;
    end;
    Wnd := FindWindowEx(0, Wnd, 'SystrayClass', 0);
  end;
end;

var
  // The wizard's page while Setup waits for Forge Solo to stop.
  ClosingPage: TOutputMarqueeProgressWizardPage;
  // What the uninstaller's window said before it said it was closing Forge Solo.
  UninstallStatus: String;
  // Set once the uninstaller's wait has passed on a request to end it: it handles no more messages.
  QuitSeen: Boolean;

// ShowClosing says, while Show is True, that Forge Solo is being closed: on the wizard's page, or in
// the uninstaller's window. Nothing is shown when either runs silently.
procedure ShowClosing(Show: Boolean);
begin
  if IsUninstaller then
  begin
    if UninstallSilent then
      exit;
    if Show then
    begin
      UninstallStatus := UninstallProgressForm.StatusLabel.Caption;
      UninstallProgressForm.StatusLabel.Caption := 'Closing Forge Solo...';
    end else
      UninstallProgressForm.StatusLabel.Caption := UninstallStatus;
  end else if not WizardSilent then
  begin
    if Show then
    begin
      ClosingPage.SetText('Closing Forge Solo...', 'It stops its nodes and its database cleanly ' +
        'first. That can take up to two minutes.');
      ClosingPage.Show;
    end else
      ClosingPage.Hide;
  end;
end;

// Waiting waits about a second, while the window Setup or the uninstaller shows goes on answering.
procedure Waiting;
var I: Integer; Msg: TWindowMessage;
begin
  for I := 1 to 20 do
  begin
    if IsUninstaller then
    begin
      if not QuitSeen then
        while PeekMessage(Msg, 0, 0, 0, PM_REMOVE) do
        begin
          if Msg.MessageID = WM_QUIT then
          begin
            // Passed on, for the uninstaller's own loop.
            QuitSeen := True;
            PostQuitMessage(Msg.WParam);
            break;
          end;
          TranslateMessage(Msg);
          DispatchMessage(Msg);
        end;
    end else if not WizardSilent then
      ClosingPage.Animate;
    Sleep(50);
  end;
end;

// StopForgeSolo closes Forge Solo if it runs from this install's folder, and waits for every program
// there to stop: StopWait seconds, then as often again as the user chooses Retry. It reports whether
// none runs any more. It never ends one by force.
function StopForgeSolo: Boolean;
var Running: String; Seconds, Asked: Integer; Again: Boolean;
begin
  Running := ProgramsRunning;
  Result := Running = '';
  if Result then
    exit;
  ShowClosing(True);
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
        // Cancel when suppressed: a silent install or uninstall then ends, having changed nothing.
        Again := SuppressibleMsgBox('Forge Solo has not stopped yet. Still running from ' +
          ExpandConstant('{app}') + ': ' + Running + '.' + #13#10#13#10 +
          'Forge Solo stops its nodes and its database cleanly, which can take a few minutes on a ' +
          'slow PC. Retry waits for it again. Cancel changes nothing.',
          mbError, MB_RETRYCANCEL, IDCANCEL) = IDRETRY;
      end;
    until not Again;
  finally
    ShowClosing(False);
  end;
  Result := Running = '';
end;

// Forge Solo is closed before Windows' Restart Manager looks for programs that use the files Setup
// replaces: Inno Setup calls PrepareToInstall first.
function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  Result := '';
  if not StopForgeSolo then
    Result := 'Forge Solo did not stop, so Setup changed nothing. Run Setup again once it has stopped.';
end;

// Setup installs for the Windows account it runs as. Started with "Run as administrator", it can
// run as another account than the one signed in: an administrator's, over the shoulder, or the
// separate account Administrator Protection elevates to. Forge Solo then goes to that account's
// profile and Start menu. The first page says which account. No refusal: Setup also runs as
// administrator for the account's own install, with UAC off or for the built-in Administrator.
procedure InitializeWizard;
var Account: String;
begin
  ClosingPage := CreateOutputMarqueeProgressPage('Closing Forge Solo',
    'Setup closes Forge Solo before it replaces its files.');
  if IsAdmin() then
  begin
    Account := ExpandConstant('{username}') + ' (' + ExpandConstant('{%USERPROFILE}') + ')';
    Log('Setup is running as administrator, for the account ' + Account);
    CreateOutputMsgPage(wpWelcome, 'Installing as administrator',
      'Check which Windows account Forge Solo is installed for.',
      'Setup is running as administrator, so Forge Solo will be installed for the Windows account ' +
      Account + ': its files, Start menu entry and data go there.' + #13#10#13#10 +
      'If that is not your account, click Cancel, then run Setup again without "Run as ' +
      'administrator". It asks for permission itself when it needs it.');
  end;
end;

// MemoPart is one part of the Ready page's summary, and the blank line after it, if it has one.
function MemoPart(S, NewLine: String): String;
begin
  Result := '';
  if S <> '' then
    Result := S + NewLine + NewLine;
end;

// The Ready page says what Windows' prompt for the elevated step is for, just before it comes.
// Running as administrator, Setup gets no prompt.
function UpdateReadyMemo(Space, NewLine, MemoUserInfoInfo, MemoDirInfo, MemoTypeInfo,
  MemoComponentsInfo, MemoGroupInfo, MemoTasksInfo: String): String;
begin
  Result := MemoPart(MemoUserInfoInfo, NewLine) + MemoPart(MemoDirInfo, NewLine) +
    MemoPart(MemoTypeInfo, NewLine) + MemoPart(MemoComponentsInfo, NewLine) +
    MemoPart(MemoGroupInfo, NewLine) + MemoPart(MemoTasksInfo, NewLine);
  if not IsAdmin() then
    Result := Result + 'Permission:' + NewLine +
      Space + 'Windows will ask whether Windows Command Processor may make' + NewLine +
      Space + 'changes to your device. Choose Yes: Setup uses it to add the' + NewLine +
      Space + 'firewall rules that let miners on your network connect, and' + NewLine +
      Space + 'Defender exclusions for the blockchains.';
end;

procedure CurStepChanged(CurStep: TSetupStep);
var InPlace: Integer; DataDir, Cmd, Missing: String; Ran: Boolean;
begin
  if CurStep = ssPostInstall then
  begin
    DataDir := ExpandConstant('{userappdata}\ForgeSolo');
    RulesAccount := ExpandConstant('{username}');
    PreviousRulesAccount := KeptRulesAccount;
    // Each rule lets in only the program that listens on its port. With the port alone, any program
    // could take the port while Forge Solo is not running and be reached through the rule.
    Cmd := '/c ' +
      FirewallRule('Forge Solo Miner (3333)', 'stratum.exe', '3333', 'private,domain') +
      FirewallRule('Forge Solo Rentals (3335)', 'stratum.exe', '3335', 'private,domain') +
      'netsh advfirewall firewall delete rule name="Forge Solo BCH2 P2P (8333)" >nul 2>&1 & ' +
      FirewallRule('Forge Solo BCH2 P2P (8339)', 'bitcoincashIId.exe', '8339', 'any') +
      FirewallRule('Forge Solo 1175 P2P (25360)', 'elevenseventyfived.exe', '25360', 'any') +
      'powershell -NoProfile -Command "Remove-MpPreference -ExclusionPath ' + OldDefenderPaths(DataDir) + ' -ErrorAction SilentlyContinue; Add-MpPreference -ExclusionPath ' + DefenderPaths(DataDir) + ' -ErrorAction SilentlyContinue"';
    Ran := Elevated(Cmd);
    // The rules themselves say whether the step worked: cmd's exit code is only that of its last
    // command, and with UAC off a standard account's netsh fails without a prompt.
    InPlace := RulesInPlace;
    Log('Firewall rules in place for ' + RulesAccount + ': ' + IntToStr(InPlace) + ' of ' + IntToStr(RuleCount));
    // Kept only when the rules are in place: the uninstaller removes the rules of this name.
    if InPlace = RuleCount then
      RegWriteStringValue(HKCU, RulesKey, RulesValue, RulesAccount);
    if InPlace < RuleCount then
    begin
      if Ran then
        Missing := 'Forge Solo is installed, but Setup could not add all of its firewall rules.'
      else
        Missing := 'Forge Solo is installed, but Windows did not let Setup add its firewall rules ' +
          'and Defender exclusions.';
      // One button, so that an install run with /SUPPRESSMSGBOXES goes on.
      SuppressibleMsgBox(Missing + #13#10#13#10 +
        'Until the rules are added, miners on other devices on your network cannot connect to ' +
        'this PC. Mining from this PC itself works.' + #13#10#13#10 +
        'To add them, run this installer again and choose Yes when Windows asks whether Windows ' +
        'Command Processor may make changes to your device.', mbError, MB_OK, IDOK);
    end;
  end;
end;

var
  // What the uninstaller could not remove, said once it has finished.
  LeftBehind: String;

// RemoveLinks removes the junctions a move of the old data cut short can leave in
// %ProgramData%\ForgeSolo\links (a folder per Windows account), and the folders that held them once
// they are empty. RemoveDir removes a junction itself, never what it leads to (the data folder and
// the install folder), and no other folder that is not empty: nothing here deletes a tree.
procedure RemoveLinks;
var Links: String; FindRec: TFindRec;
begin
  Links := ExpandConstant('{commonappdata}\ForgeSolo\links');
  if FindFirst(Links + '\*', FindRec) then
  begin
    try
      repeat
        if (FindRec.Name <> '.') and (FindRec.Name <> '..') then
        begin
          RemoveDir(Links + '\' + FindRec.Name + '\data');
          RemoveDir(Links + '\' + FindRec.Name + '\app');
          RemoveDir(Links + '\' + FindRec.Name);
        end;
      until not FindNext(FindRec);
    finally
      FindClose(FindRec);
    end;
  end;
  RemoveDir(Links);
  RemoveDir(ExpandConstant('{commonappdata}\ForgeSolo'));
end;

// NewestBeforeMerge is the name of the newest copy of forgesolo.db a merge kept in DataDir
// (forgesolo.db.before-merge-<UTC time>), or '' if there is none.
function NewestBeforeMerge(DataDir: String): String;
var FindRec: TFindRec;
begin
  Result := '';
  if FindFirst(DataDir + '\forgesolo.db.before-merge-*', FindRec) then
  begin
    try
      repeat
        if FindRec.Name > Result then
          Result := FindRec.Name;
      until not FindNext(FindRec);
    finally
      FindClose(FindRec);
    end;
  end;
end;

// DataFolderContents says what the data folder holds, for the question whether to delete it.
function DataFolderContents(DataDir: String): String;
var Kept: String;
begin
  Result := 'It holds:' + #13#10 + '- the downloaded BCH2 and 1175 blockchains' + #13#10 +
    '- forgesolo.db: your blocks, payouts and settings, your payout address among them' + #13#10;
  Kept := NewestBeforeMerge(DataDir);
  if Kept <> '' then
    Result := Result + '- ' + Kept + ': the copy of forgesolo.db from before the data of ' +
      '1.0.12 was last merged into it' + #13#10;
  if DirExists(DataDir + '\pgdata') then
    Result := Result + '- pgdata: the database of Forge Solo 1.0.12 and before, kept for going ' +
      'back to 1.0.12' + #13#10;
  Result := Result + '- secrets.env: this install''s passwords';
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var InPlace: Integer; DataDir, Cmd: String; Ran: Boolean;
begin
  if CurUninstallStep = usUninstall then
  begin
    // Its files are removed next: Forge Solo is closed first, cleanly.
    if not StopForgeSolo then
    begin
      Log('Forge Solo did not stop: the uninstall ends, and nothing is removed');
      Abort;
    end;
    DataDir := ExpandConstant('{userappdata}\ForgeSolo');
    // The rules carry the account's name as it was when they were put in place.
    RulesAccount := KeptRulesAccount;
    if RulesAccount = '' then
      RulesAccount := ExpandConstant('{username}');
    Cmd := '/c ' +
      FirewallRemove('Forge Solo Miner (3333)') +
      FirewallRemove('Forge Solo Rentals (3335)') +
      FirewallRemove('Forge Solo BCH2 P2P (8339)') +
      'netsh advfirewall firewall delete rule name="Forge Solo BCH2 P2P (8333)" & ' +
      FirewallRemove('Forge Solo 1175 P2P (25360)') +
      'powershell -NoProfile -Command "Remove-MpPreference -ExclusionPath ' + OldDefenderPaths(DataDir) + ', ' + DefenderPaths(DataDir) + ' -ErrorAction SilentlyContinue"';
    Ran := Elevated(Cmd);
    InPlace := RulesInPlace;
    Log('Firewall rules left for ' + RulesAccount + ': ' + IntToStr(InPlace) + ' of ' + IntToStr(RuleCount));
    if not Ran or (InPlace > 0) then
    begin
      if Ran then
        LeftBehind := 'Forge Solo is removed, but the uninstaller could not remove all of its ' +
          'firewall rules.'
      else
        LeftBehind := 'Forge Solo is removed, but Windows did not let the uninstaller remove its ' +
          'firewall rules and Defender exclusions.';
      LeftBehind := LeftBehind + #13#10#13#10 + 'To remove them yourself, open Windows Security:' + #13#10 +
        '- Firewall & network protection > Advanced settings > Inbound Rules: delete the ' +
        'rules named "Forge Solo ... for ' + RulesAccount + '".';
      if not Ran then
        LeftBehind := LeftBehind + #13#10 + '- Virus & threat protection > Manage settings > Add ' +
          'or remove exclusions: remove the folders in ' + DataDir + '.';
    end;
  end;

  // Uninstalling used to leave the data folder untouched, and that folder holds secrets.env
  // (both node RPC passwords, the old database's password and the internal API token) as well
  // as the chain data and your payout address. Silently leaving credentials behind is not a
  // decision to make on someone's behalf, so ask.
  //
  // All or nothing on purpose: deleting only secrets.env would lose the old database's password,
  // which moving its data or going back to 1.0.12 needs.
  if CurUninstallStep = usPostUninstall then
  begin
    // One button, so that an uninstall run with /SUPPRESSMSGBOXES goes on.
    if LeftBehind <> '' then
      SuppressibleMsgBox(LeftBehind, mbError, MB_OK, IDOK);
    RegDeleteValue(HKCU, RulesKey, RulesValue);
    RegDeleteKeyIfEmpty(HKCU, RulesKey);
    RemoveLinks;
    DataDir := ExpandConstant('{userappdata}\ForgeSolo');
    if DirExists(DataDir) then
    begin
      // No is the default: pressing Enter keeps the folder, and so does an uninstall run silently
      // (/SUPPRESSMSGBOXES), which a plain MsgBox would have stopped on, waiting for an answer.
      if SuppressibleMsgBox('Also delete Forge Solo''s data folder?' + #13#10#13#10 +
                DataDir + #13#10#13#10 + DataFolderContents(DataDir) + #13#10#13#10 +
                'Choose No to keep it for a future reinstall.',
                mbConfirmation, MB_YESNO or MB_DEFBUTTON2, IDNO) = IDYES then
        DelTree(DataDir, True, True, True);
    end;
  end;
end;
