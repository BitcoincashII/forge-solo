; Forge Solo: single-exe Windows installer. Bundles the launcher, stratum/api services,
; BCH2 + 1175 nodes, portable PostgreSQL, and the dashboard. Per-user install (no admin).
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
; Held by the launcher while it runs (runningMutex in launcher/instance_windows.go). Setup and the
; uninstaller ask for Forge Solo to be closed first, so that it stops both nodes cleanly, rather
; than have its files closed under it. Test builds of 1.0.13 held the unprefixed name.
AppMutex=ForgeSoloRunning,Global\ForgeSoloRunning

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Messages]
; Shown instead of installing where Forge Solo cannot run (ArchitecturesAllowed).
WindowsVersionNotSupported=Forge Solo needs 64-bit Windows: Windows 10 or 11 on an x64 PC, or Windows 11 on ARM.

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
Source: "..\init-db.sql"; DestDir: "{app}"; Flags: ignoreversion
Source: "pgsql\*"; DestDir: "{app}\pgsql"; Flags: ignoreversion recursesubdirs createallsubdirs
Source: "..\web\dist\*"; DestDir: "{app}\web"; Flags: ignoreversion recursesubdirs createallsubdirs

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
//  - Defender exclusions for the folders written constantly (both nodes' blocks and chainstate,
//    and the database) so it stops rescanning them on every write, the main cause of disk thrash
//    and freezes on a laptop. Not the whole data folder: a folder the user can write to and
//    Defender never scans is a place any other program could hide files. An upgrade removes the
//    whole-folder exclusion earlier versions added.
// Mining from THIS PC (127.0.0.1:3333) needs no rule at all.
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

// RuleName is the name of one of this install's firewall rules: the base name, for this Windows
// account. Two accounts on one PC can each install Forge Solo, and each install's rules let in its
// own programs; with one name for all, installing for one account replaced the other's rules, and
// its miners could no longer connect. Earlier releases used the base name alone (with no program
// before 1.0.13): rules of that name are removed.
function RuleName(Base: String): String;
begin
  Result := Base + ' for ' + ExpandConstant('{username}');
end;

// FirewallRule is the commands that put this install's rule in place, after removing the old one.
function FirewallRule(Base, Exe, Port, Profile: String): String;
begin
  Result :=
    'netsh advfirewall firewall delete rule name="' + Base + '" >nul 2>&1 & ' +
    'netsh advfirewall firewall delete rule name="' + RuleName(Base) + '" >nul 2>&1 & ' +
    'netsh advfirewall firewall add rule name="' + RuleName(Base) + '" dir=in action=allow program="' +
      ExpandConstant('{app}') + '\' + Exe + '" protocol=TCP localport=' + Port + ' profile=' + Profile + ' & ';
end;

// FirewallRemove is the commands that remove this install's rule, and an old one of the base name.
function FirewallRemove(Base: String): String;
begin
  Result :=
    'netsh advfirewall firewall delete rule name="' + Base + '" & ' +
    'netsh advfirewall firewall delete rule name="' + RuleName(Base) + '" & ';
end;

// DefenderPaths lists, quoted for PowerShell, the data folders written constantly.
function DefenderPaths(DataDir: String): String;
begin
  Result := PSQuote(DataDir + '\bch2\blocks') + ', ' + PSQuote(DataDir + '\bch2\chainstate') + ', ' +
            PSQuote(DataDir + '\elevenseventyfive\blocks') + ', ' +
            PSQuote(DataDir + '\elevenseventyfive\chainstate') + ', ' + PSQuote(DataDir + '\pgdata');
end;

// Setup installs for the Windows account it runs as. Started with "Run as administrator", it can
// run as another account than the one signed in: an administrator's, over the shoulder, or the
// separate account Administrator Protection elevates to. Forge Solo then goes to that account's
// profile and Start menu. The first page says which account. No refusal: Setup also runs as
// administrator for the account's own install, with UAC off or for the built-in Administrator.
procedure InitializeWizard;
var Account: String;
begin
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

procedure CurStepChanged(CurStep: TSetupStep);
var ResultCode: Integer; DataDir, Cmd: String;
begin
  if CurStep = ssPostInstall then
  begin
    DataDir := ExpandConstant('{userappdata}\ForgeSolo');
    // Each rule lets in only the program that listens on its port. With the port alone, any program
    // could take the port while Forge Solo is not running and be reached through the rule.
    Cmd := '/c ' +
      FirewallRule('Forge Solo Miner (3333)', 'stratum.exe', '3333', 'private,domain') +
      FirewallRule('Forge Solo Rentals (3335)', 'stratum.exe', '3335', 'private,domain') +
      'netsh advfirewall firewall delete rule name="Forge Solo BCH2 P2P (8333)" >nul 2>&1 & ' +
      FirewallRule('Forge Solo BCH2 P2P (8339)', 'bitcoincashIId.exe', '8339', 'any') +
      FirewallRule('Forge Solo 1175 P2P (25360)', 'elevenseventyfived.exe', '25360', 'any') +
      'powershell -NoProfile -Command "Remove-MpPreference -ExclusionPath ' + PSQuote(DataDir) + ' -ErrorAction SilentlyContinue; Add-MpPreference -ExclusionPath ' + DefenderPaths(DataDir) + ' -ErrorAction SilentlyContinue"';
    ShellExec('runas', ExpandConstant('{cmd}'), Cmd, '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  end;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var ResultCode: Integer; DataDir, Cmd: String;
begin
  if CurUninstallStep = usUninstall then
  begin
    DataDir := ExpandConstant('{userappdata}\ForgeSolo');
    Cmd := '/c ' +
      FirewallRemove('Forge Solo Miner (3333)') +
      FirewallRemove('Forge Solo Rentals (3335)') +
      FirewallRemove('Forge Solo BCH2 P2P (8339)') +
      'netsh advfirewall firewall delete rule name="Forge Solo BCH2 P2P (8333)" & ' +
      FirewallRemove('Forge Solo 1175 P2P (25360)') +
      'powershell -NoProfile -Command "Remove-MpPreference -ExclusionPath ' + PSQuote(DataDir) + ', ' + DefenderPaths(DataDir) + ' -ErrorAction SilentlyContinue"';
    ShellExec('runas', ExpandConstant('{cmd}'), Cmd, '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  end;

  // Uninstalling used to leave the data folder untouched, and that folder holds secrets.env
  // -- both node RPC passwords, the database password and the internal API token -- as well
  // as the chain data and your payout address. Silently leaving credentials behind is not a
  // decision to make on someone's behalf, so ask.
  //
  // All or nothing on purpose: deleting only secrets.env would regenerate a new database
  // password against the existing pgdata on the next install, and the app could no longer
  // open its own database.
  if CurUninstallStep = usPostUninstall then
  begin
    DataDir := ExpandConstant('{userappdata}\ForgeSolo');
    if DirExists(DataDir) then
    begin
      // No is the default: pressing Enter keeps the folder, and so does an uninstall run silently
      // (/SUPPRESSMSGBOXES), which a plain MsgBox would have stopped on, waiting for an answer.
      if SuppressibleMsgBox('Also delete Forge Solo''s data folder?' + #13#10#13#10 +
                DataDir + #13#10#13#10 +
                'It holds the downloaded BCH2 and 1175 blockchains, the database, your saved ' +
                'payout address, and the file storing this install''s node and database ' +
                'passwords.' + #13#10#13#10 +
                'Choose No to keep it for a future reinstall.',
                mbConfirmation, MB_YESNO or MB_DEFBUTTON2, IDNO) = IDYES then
        DelTree(DataDir, True, True, True);
    end;
  end;
end;
