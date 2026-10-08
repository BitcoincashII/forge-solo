; Forge Gateway: single-exe Windows installer. Installs the tray app and the gateway program for this
; Windows account (no admin), and with one elevated step a firewall rule for miners on port 3333.
#define MyAppName "Forge Gateway"
; Overridable from the command line so CI can stamp the tag it is building:
;   iscc /DMyAppVersion=1.1.0 forge-gateway.iss
#ifndef MyAppVersion
  #define MyAppVersion "1.1.0"
#endif
#define MyAppPublisher "BCH2 Team"
#define MyAppURL "https://github.com/BitcoincashII/forge-gateway"
#define MyAppExe "forge-gateway-tray.exe"

[Setup]
AppId={{61A087E0-4C4F-499C-8A19-BCBCAC149866}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
AppSupportURL={#MyAppURL}/issues
DefaultDirName={localappdata}\Programs\ForgeGateway
DisableProgramGroupPage=yes
DisableDirPage=yes
PrivilegesRequired=lowest
; Both programs are 64-bit (x64): Windows that runs no x64 programs could not start them. No 64-bit
; install mode: the elevated step uses the 32-bit cmd, netsh and sc, as Forge Solo's does.
ArchitecturesAllowed=x64compatible
OutputDir=.
OutputBaseFilename=ForgeGateway-Setup-{#MyAppVersion}
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
SetupIconFile=launcher\forge-gateway.ico
UninstallDisplayIcon={app}\{#MyAppExe}
UninstallDisplayName={#MyAppName}
; No AppMutex: Setup and the uninstaller would check it at start, before [Code] closes a running
; Forge Gateway, and a silent update or uninstall would end there. [Code] closes it first, then
; checks the mutex (NoOtherForgeGateway).

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Messages]
; Shown instead of installing where Forge Gateway cannot run (ArchitecturesAllowed).
WindowsVersionNotSupported=Forge Gateway needs 64-bit Windows: Windows 10 or 11 on an x64 PC, or Windows 11 on ARM.
; The uninstaller's elevated step gets Windows' prompt for Windows Command Processor, not for Forge
; Gateway: say beforehand what it is for.
ConfirmUninstall=Are you sure you want to completely remove %1 and all of its components?%n%nIf Windows then asks whether Windows Command Processor may make changes to your device, choose Yes: that lets the uninstaller remove Forge Gateway's firewall rule.

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; GroupDescription: "Additional icons:"
; Mining stops when the app is closed. Opt-in, per-user (HKCU needs no admin), and removed with
; the app.
Name: "startup"; Description: "Start Forge Gateway when I sign in"; GroupDescription: "Startup:"; Flags: unchecked

[Registry]
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "ForgeGateway"; ValueData: """{app}\{#MyAppExe}"""; Flags: uninsdeletevalue; Tasks: startup
; An update with the box unticked turns the sign-in start off.
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: none; ValueName: "ForgeGateway"; Flags: deletevalue; Tasks: not startup

[Files]
; bin holds forge-gateway-tray.exe, forge-gateway.exe and LICENSE.txt.
Source: "bin\*"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{userprograms}\Forge Gateway"; Filename: "{app}\{#MyAppExe}"
Name: "{userprograms}\Uninstall Forge Gateway"; Filename: "{uninstallexe}"
Name: "{userdesktop}\Forge Gateway"; Filename: "{app}\{#MyAppExe}"; Tasks: desktopicon

[Run]
Filename: "{app}\{#MyAppExe}"; Description: "Launch Forge Gateway now"; Flags: nowait postinstall skipifsilent

[Code]
// One elevated step (a single UAC prompt) at install:
//  - inbound TCP 3333 for forge-gateway.exe: a miner on the network can reach the gateway
//    (private/domain only). Mining from THIS PC (127.0.0.1:3333) needs no rule at all.
//  - the rule named "Forge Gateway" goes: the guide of Forge Gateway 1.0.0 had users add it, and it
//    let any program in on port 3333.
//  - with the user's Yes, a Forge Gateway Windows service (1.0.0's, say) is stopped and removed:
//    it starts with Windows and holds port 3333, so the tray's gateway could not start beside it.
//
// Windows' prompt for that step names Windows Command Processor, not Forge Gateway, so the Ready
// page and the uninstaller's question say beforehand what it is for. Afterwards the rule and the
// service are checked: if the prompt was refused, or the step failed, Setup says what is missing,
// what that means and how to put it right, and logs it.

// RuleName is the name of the firewall rule of the install for the Windows account called Account:
// the base name, for that account. Two accounts on one PC can each install Forge Gateway, and each
// install's rule lets in its own program.
function RuleName(Base, Account: String): String;
begin
  Result := Base + ' for ' + Account;
end;

const
  // Where the account name the rule carries is kept, so that the uninstaller removes that rule,
  // also after the account has been renamed.
  RulesKey = 'Software\ForgeGateway';
  RulesValue = 'FirewallRulesAccount';

var
  // The account name this install's rule carries, and at install the one an earlier install kept.
  RulesAccount, PreviousRulesAccount: String;

// UsableAccountName is whether Name, the account name an earlier install kept for its rule, can go
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

// KeptRulesAccount is the account name an earlier install kept for its rule, or '' if none is kept
// or it cannot go into the elevated step's commands.
function KeptRulesAccount: String;
var Name: String;
begin
  Result := '';
  if RegQueryStringValue(HKCU, RulesKey, RulesValue, Name) then
  begin
    if UsableAccountName(Name) then
      Result := Name
    else
      Log('The account name kept for the firewall rule is not used: it is too long, or has a ' +
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
  RuleCount = 1;

// RulesInPlace is how many of this install's firewall rules Windows Firewall has.
function RulesInPlace: Integer;
begin
  Result := 0;
  if RuleInPlace(RuleName('Forge Gateway Miner (3333)', RulesAccount)) then Result := Result + 1;
end;

// Elevated runs Cmd, the commands of the one elevated step, and logs whether it ran. It is False
// if it did not: Windows' prompt was refused, or no administrator gave permission.
function Elevated(Cmd: String): Boolean;
var ResultCode: Integer;
begin
  Result := ShellExec('runas', ExpandConstant('{cmd}'), Cmd, '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  if Result then
    Log('The elevated step ran, exit code ' + IntToStr(ResultCode))
  else
    Log('The elevated step did not run: ' + SysErrorMessage(ResultCode));
end;

// Forge Gateway running while it is updated or removed.
//
// Before Setup or the uninstaller touches a file, it closes a Forge Gateway running from this
// install's folder for this account the way its tray's Quit does, and waits for every program in
// that folder to stop. Restart Manager then finds nothing to close. Nothing is ever ended by force:
// the gateway sends Forge Pool the shares it still holds before it stops.
//
// The tray app holds a mutex while it runs. It is checked only once this account's Forge Gateway
// has stopped (NoOtherForgeGateway): one still holding it runs for another Windows account, and
// only that account may close it.

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
  // How long Forge Gateway gets to stop, in seconds, before the user is asked whether to wait
  // again. The tray gives the gateway 30 s to close its miners' connections and send the pool the
  // shares it still holds; usually it is done in a few seconds.
  StopWait = 60;
  // The mutex the tray app holds while it runs (runningMutex in launcher/instance_windows.go).
  // There is one for the whole PC: Forge Gateway running for another Windows account holds it too.
  RunningMutex = 'Global\ForgeGatewayRunning';
  SYNCHRONIZE_ACCESS = $00100000;
  ERROR_ACCESS_DENIED = 5;

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
function OpenMutex(Access: Cardinal; Inherit: Bool; Name: String): Cardinal;
  external 'OpenMutexW@kernel32.dll stdcall';
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

// LongPath is Path with any short (8.3) names in their long form, in lower case, so that two
// spellings of one path compare equal.
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

// ProgramsRunning names the programs running from this install's folder, each once: the tray app
// and the gateway. The uninstaller itself is not one.
function ProgramsRunning: String;
var Snapshot: Cardinal; Entry: TProcessEntry; Folder, Path, Name: String;
begin
  Result := '';
  Folder := InstallFolder;
  Snapshot := CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0);
  if Snapshot = INVALID_HANDLE then
  begin
    Log('Cannot list the running programs: Forge Gateway is not looked for');
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

// AskForgeGatewayToQuit asks each Forge Gateway running from this install's folder to quit as its
// tray's Quit does, and returns how many it asked: its tray window (SystrayClass) gets WM_CLOSE.
// fyne's tray then ends its loop, and the tray app stops the gateway and exits.
function AskForgeGatewayToQuit: Integer;
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
  // The wizard's page while Setup waits for Forge Gateway to stop.
  ClosingPage: TOutputMarqueeProgressWizardPage;
  // What the uninstaller's window said before it said it was closing Forge Gateway.
  UninstallStatus: String;
  // Set once the uninstaller's wait has passed on a request to end it: it handles no more messages.
  QuitSeen: Boolean;

// ShowClosing says, while Show is True, that Forge Gateway is being closed: on the wizard's page, or
// in the uninstaller's window. Nothing is shown when either runs silently.
procedure ShowClosing(Show: Boolean);
begin
  if IsUninstaller then
  begin
    if UninstallSilent then
      exit;
    if Show then
    begin
      UninstallStatus := UninstallProgressForm.StatusLabel.Caption;
      UninstallProgressForm.StatusLabel.Caption := 'Closing Forge Gateway...';
    end else
      UninstallProgressForm.StatusLabel.Caption := UninstallStatus;
  end else if not WizardSilent then
  begin
    if Show then
    begin
      ClosingPage.SetText('Closing Forge Gateway...', 'It sends Forge Pool the shares it still ' +
        'holds first. That takes a few seconds.');
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

// StopForgeGateway closes Forge Gateway if it runs from this install's folder, and waits for every
// program there to stop: StopWait seconds, then as often again as the user chooses Retry. It
// reports whether none runs any more. It never ends one by force.
function StopForgeGateway: Boolean;
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
        // Cancel when suppressed: a silent install or uninstall then ends, having changed nothing.
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
end;

// MutexHeld reports whether the mutex Name exists. A program running for another Windows account
// holds it with that account's security, which does not let this account open it: Windows refuses
// access, and that refusal means it is held. Inno Setup's CheckForMutexes, and AppMutex, take it for
// no mutex at all.
function MutexHeld(Name: String): Boolean;
var Mutex: Cardinal;
begin
  Mutex := OpenMutex(SYNCHRONIZE_ACCESS, False, Name);
  Result := Mutex <> 0;
  if Result then
    CloseHandle(Mutex)
  else
    Result := DLLGetLastError = ERROR_ACCESS_DENIED;
end;

// ForgeGatewayRuns reports whether the tray app runs on this PC, for any Windows account.
function ForgeGatewayRuns: Boolean;
begin
  Result := MutexHeld(RunningMutex);
end;

// NoOtherForgeGateway reports, once this account's Forge Gateway has stopped (StopForgeGateway),
// whether no Forge Gateway runs on this PC any more. One that still holds the mutex runs for another
// Windows account: this installer does not close it. The mutex is looked at again for a few
// seconds, as a tray app that has just exited can hold it a moment longer, then as often as the user
// chooses Retry. Cancel, the answer of a silent run, changes nothing.
function NoOtherForgeGateway: Boolean;
var Seconds: Integer;
begin
  repeat
    Result := not ForgeGatewayRuns;
    Seconds := 0;
    while not Result and (Seconds < 3) do
    begin
      Waiting;
      Seconds := Seconds + 1;
      Result := not ForgeGatewayRuns;
    end;
    if Result then
      exit;
    Log('Forge Gateway runs on this PC for another Windows account: it is not closed');
  until SuppressibleMsgBox('Forge Gateway is running for another Windows account on this PC, and only ' +
    'that account can close it.' + #13#10#13#10 +
    'Quit it there (right-click its tray icon, then Quit Forge Gateway) and choose Retry. Cancel ' +
    'changes nothing.', mbError, MB_RETRYCANCEL, IDCANCEL) <> IDRETRY;
end;

// A Forge Gateway Windows service.
//
// forge-gateway.exe install (the zip's way, Forge Gateway 1.0.0's only one on Windows) makes a
// service that starts with Windows as LocalSystem and holds port 3333 and the status page's port,
// so the tray's gateway could not start beside it. Setup says so and asks; with a Yes the elevated
// step stops it through Windows, which is its own clean stop (its miners closed, its queued shares
// sent), and deletes it. No, or a silent run, changes nothing. Its config file and its key stay
// where they are. Its key in the registry and its state can be read without administrator rights.

const
  // serviceName in cmd/forge-gateway/service_windows.go.
  ServiceName = 'ForgeGateway';
  ServiceKey = 'SYSTEM\CurrentControlSet\Services\ForgeGateway';
  SC_MANAGER_CONNECT = $0001;
  SERVICE_QUERY_STATUS = $0004;
  SERVICE_STOPPED = 1;
  ERROR_SERVICE_DOES_NOT_EXIST = 1060;

type
  // SERVICE_STATUS: seven DWORDs, the state the second.
  TServiceStatus = record
    ServiceType, CurrentState, ControlsAccepted, Win32ExitCode, ServiceSpecificExitCode, CheckPoint, WaitHint: Cardinal;
  end;

function OpenSCManager(Machine, Database: String; Access: Cardinal): Cardinal;
  external 'OpenSCManagerW@advapi32.dll stdcall';
function OpenService(Manager: Cardinal; Name: String; Access: Cardinal): Cardinal;
  external 'OpenServiceW@advapi32.dll stdcall';
function QueryServiceStatus(Service: Cardinal; var Status: TServiceStatus): Bool;
  external 'QueryServiceStatus@advapi32.dll stdcall';
function CloseServiceHandle(Handle: Cardinal): Bool;
  external 'CloseServiceHandle@advapi32.dll stdcall';

var
  // Set in PrepareToInstall when the user said Yes to removing the service.
  RemoveService: Boolean;

// ServiceInstalled is whether a ForgeGateway Windows service is installed on this PC.
function ServiceInstalled: Boolean;
begin
  Result := RegKeyExists(HKLM, ServiceKey);
end;

// ServiceProgram is the service's command line (ImagePath), or '' if it has none.
function ServiceProgram: String;
var Image: String;
begin
  Result := '';
  if RegQueryStringValue(HKLM, ServiceKey, 'ImagePath', Image) then
    Result := Image;
end;

// ServiceExe is the program in the command line Image: the quoted part at its start, or, unquoted,
// up to the end of its .exe.
function ServiceExe(Image: String): String;
var I: Integer;
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
end;

// ServiceInThisFolder is whether the service with the command line Image runs a program in this
// install's folder.
function ServiceInThisFolder(Image: String): Boolean;
begin
  Result := Pos(InstallFolder, LongPath(ServiceExe(Image))) = 1;
end;

// ServiceStopped is whether the ForgeGateway service is stopped, or gone.
function ServiceStopped: Boolean;
var Manager, Service: Cardinal; Status: TServiceStatus;
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
end;

// ServiceMarkedForDeletion is whether Windows deletes the service once nothing holds it open any
// more (the Services window, say).
function ServiceMarkedForDeletion: Boolean;
var Flag: Cardinal;
begin
  Result := RegQueryDWordValue(HKLM, ServiceKey, 'DeleteFlag', Flag) and (Flag = 1);
end;

// ServiceRemoval is the commands that stop the service through Windows, then delete it.
function ServiceRemoval: String;
begin
  Result := 'sc.exe stop ' + ServiceName + ' >nul 2>&1 & sc.exe delete ' + ServiceName + ' >nul 2>&1 & ';
end;

// ServiceRemoved waits, StopWait seconds at most, for the service the elevated step stopped and
// deleted to stop, logs what it finds, and reports whether the service is gone: stopped, and deleted
// or marked for deletion. When the elevated step did not run (Ran False), nothing stopped it, and
// it is not waited for.
function ServiceRemoved(Ran: Boolean): Boolean;
var Seconds: Integer; Stopped: Boolean;
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
end;

// Setup closes Forge Gateway before Windows' Restart Manager looks for programs that use the files
// it replaces: Inno Setup calls PrepareToInstall first. A ForgeGateway service is looked at before
// anything is closed: No to removing it changes nothing.
function PrepareToInstall(var NeedsRestart: Boolean): String;
var Image: String;
begin
  Result := '';
  RemoveService := False;
  if ServiceInstalled then
  begin
    Image := ServiceProgram;
    Log('A ForgeGateway Windows service is installed: ' + Image);
    // Setup cannot replace a program a service runs.
    if ServiceInThisFolder(Image) then
    begin
      Result := 'Forge Gateway runs as a Windows service from this install''s folder, so Setup ' +
        'changed nothing. Remove the service first: in a Command Prompt run as administrator, run ' +
        'sc stop ForgeGateway, then sc delete ForgeGateway. Then run Setup again.';
      exit;
    end;
    // No is the default, and the answer of a silent run (/SUPPRESSMSGBOXES).
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
  if not StopForgeGateway then
    Result := 'Forge Gateway did not stop, so Setup changed nothing. Run Setup again once it has stopped.'
  else if not NoOtherForgeGateway then
    Result := 'Forge Gateway runs for another Windows account on this PC, so Setup changed nothing. ' +
      'Run Setup again once it has been quit there.';
end;

// Setup installs for the Windows account it runs as. Started with "Run as administrator", it can
// run as another account than the one signed in: an administrator's, over the shoulder, or the
// separate account Administrator Protection elevates to. Forge Gateway then goes to that account's
// profile and Start menu. The first page says which account. No refusal: Setup also runs as
// administrator for the account's own install, with UAC off or for the built-in Administrator.
procedure InitializeWizard;
var Account: String;
begin
  ClosingPage := CreateOutputMarqueeProgressPage('Closing Forge Gateway',
    'Setup closes Forge Gateway before it replaces its files.');
  if IsAdmin() then
  begin
    Account := ExpandConstant('{username}') + ' (' + ExpandConstant('{%USERPROFILE}') + ')';
    Log('Setup is running as administrator, for the account ' + Account);
    CreateOutputMsgPage(wpWelcome, 'Installing as administrator',
      'Check which Windows account Forge Gateway is installed for.',
      'Setup is running as administrator, so Forge Gateway will be installed for the Windows account ' +
      Account + ': its files, Start menu entry and data go there.' + #13#10#13#10 +
      'If that is not your account, click Cancel, then run Setup again without "Run as ' +
      'administrator". It asks for permission itself when it needs it.');
  end;
end;

const
  // Forge Solo's uninstall entry for this account (the AppId of windows/forge-solo.iss) and the
  // mutex its launcher holds while it runs, for any account.
  ForgeSoloUninstallKey = 'Software\Microsoft\Windows\CurrentVersion\Uninstall\{9F2C7A31-4B6E-4D8A-9C1F-3E5A7B0D2C64}_is1';
  ForgeSoloMutex = 'Global\ForgeSoloRunning';

// MemoPart is one part of the Ready page's summary, and the blank line after it, if it has one.
function MemoPart(S, NewLine: String): String;
begin
  Result := '';
  if S <> '' then
    Result := S + NewLine + NewLine;
end;

// The Ready page says, when Forge Solo is installed for this account or runs on this PC, that only
// one of the two can have port 3333; and what Windows' prompt for the elevated step is for, just
// before it comes. Running as administrator, Setup gets no prompt.
function UpdateReadyMemo(Space, NewLine, MemoUserInfoInfo, MemoDirInfo, MemoTypeInfo,
  MemoComponentsInfo, MemoGroupInfo, MemoTasksInfo: String): String;
begin
  Result := MemoPart(MemoUserInfoInfo, NewLine) + MemoPart(MemoDirInfo, NewLine) +
    MemoPart(MemoTypeInfo, NewLine) + MemoPart(MemoComponentsInfo, NewLine) +
    MemoPart(MemoGroupInfo, NewLine) + MemoPart(MemoTasksInfo, NewLine);
  if RegKeyExists(HKCU, ForgeSoloUninstallKey) or MutexHeld(ForgeSoloMutex) then
    Result := Result + 'Forge Solo:' + NewLine +
      Space + 'Forge Solo is on this PC too. Both use port 3333, so only' + NewLine +
      Space + 'one of the two can run at a time; Forge Solo''s TIDES mode is' + NewLine +
      Space + 'the same gateway, built in.' + NewLine + NewLine;
  if not IsAdmin() then
    Result := Result + 'Permission:' + NewLine +
      Space + 'Windows will ask whether Windows Command Processor may make' + NewLine +
      Space + 'changes to your device. Choose Yes: Setup uses it to add the' + NewLine +
      Space + 'firewall rule that lets miners on your network connect.';
end;

procedure CurStepChanged(CurStep: TSetupStep);
var InPlace: Integer; Cmd, Missing: String; Ran: Boolean;
begin
  if CurStep = ssPostInstall then
  begin
    RulesAccount := ExpandConstant('{username}');
    PreviousRulesAccount := KeptRulesAccount;
    Cmd := '/c ';
    // The service is stopped first: it holds port 3333 until it has closed its miners' connections.
    if RemoveService then
      Cmd := Cmd + ServiceRemoval;
    // The rule lets in only forge-gateway.exe. With the port alone, any program could take the port
    // while Forge Gateway is not running and be reached through the rule, as through the one the
    // guide of 1.0.0 had users add.
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
    // The rule itself says whether the step worked: cmd's exit code is only that of its last
    // command, and with UAC off a standard account's netsh fails without a prompt.
    InPlace := RulesInPlace;
    Log('Firewall rules in place for ' + RulesAccount + ': ' + IntToStr(InPlace) + ' of ' + IntToStr(RuleCount));
    // Kept only when the rule is in place: the uninstaller removes the rule of this name.
    if InPlace = RuleCount then
      RegWriteStringValue(HKCU, RulesKey, RulesValue, RulesAccount);
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
    // One box, with one button, so that an install run with /SUPPRESSMSGBOXES goes on.
    if Missing <> '' then
      SuppressibleMsgBox(Missing, mbError, MB_OK, IDOK);
  end;
end;

var
  // What the uninstaller could not remove, said once it has finished.
  LeftBehind: String;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var InPlace: Integer; DataDir, Cmd: String; Ran, OwnService: Boolean;
begin
  if CurUninstallStep = usUninstall then
  begin
    // Its files are removed next: Forge Gateway is closed first, cleanly.
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
    // The rule carries the account's name as it was when it was put in place.
    RulesAccount := KeptRulesAccount;
    if RulesAccount = '' then
      RulesAccount := ExpandConstant('{username}');
    // A ForgeGateway service installed from this install's folder (forge-gateway.exe install, run
    // from it) would keep its program running and point at nothing once the files are gone.
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
  end;

  // The data folder holds the node's login and the settings password: leaving it behind, or
  // deleting it, is not a decision to make on someone's behalf, so ask.
  if CurUninstallStep = usPostUninstall then
  begin
    // One button, so that an uninstall run with /SUPPRESSMSGBOXES goes on.
    if LeftBehind <> '' then
      SuppressibleMsgBox(LeftBehind, mbError, MB_OK, IDOK);
    RegDeleteValue(HKCU, RulesKey, RulesValue);
    RegDeleteKeyIfEmpty(HKCU, RulesKey);
    DataDir := ExpandConstant('{userappdata}\ForgeGateway');
    if DirExists(DataDir) then
    begin
      // No is the default: pressing Enter keeps the folder, and so does an uninstall run silently
      // (/SUPPRESSMSGBOXES), which a plain MsgBox would have stopped on, waiting for an answer.
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
  end;
end;
