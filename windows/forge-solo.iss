; Forge Solo — single-exe Windows installer. Bundles the launcher, stratum/api services,
; BCH2 + 1175 nodes, portable PostgreSQL, and the dashboard. Per-user install (no admin).
#define MyAppName "Forge Solo"
; Overridable from the command line so CI can stamp the tag it is building:
;   iscc /DMyAppVersion=1.0.10 forge-solo.iss
#ifndef MyAppVersion
  #define MyAppVersion "1.0.12"
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
; than have its files closed under it.
AppMutex=ForgeSoloRunning

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; GroupDescription: "Additional icons:"
; Mining stops when the app is closed, and nothing restarted it after a reboot -- a machine
; that rebooted overnight simply stopped earning until someone noticed. Opt-in, per-user
; (HKCU needs no admin), and removed with the app.
Name: "startup"; Description: "Start Forge Solo when I sign in"; GroupDescription: "Startup:"; Flags: unchecked

[Registry]
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "ForgeSolo"; ValueData: """{app}\{#MyAppExe}"""; Flags: uninsdeletevalue; Tasks: startup

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
//  - inbound TCP 8339  : the BCH2 node accepts incoming peers (any profile)
//  - inbound TCP 25360 : the 1175 node accepts incoming peers (any profile)
//  - Defender exclusions for the folders written constantly (both nodes' blocks and chainstate,
//    and the database) so it stops rescanning them on every write, the main cause of disk thrash
//    and freezes on a laptop. Not the whole data folder: a folder the user can write to and
//    Defender never scans is a place any other program could hide files. An upgrade removes the
//    whole-folder exclusion earlier versions added.
// Mining from THIS PC (127.0.0.1:3333) needs no rule at all.
// DefenderPaths lists, quoted for PowerShell, the data folders written constantly.
function DefenderPaths(DataDir: String): String;
begin
  Result := '''' + DataDir + '\bch2\blocks'', ''' + DataDir + '\bch2\chainstate'', ''' +
            DataDir + '\elevenseventyfive\blocks'', ''' + DataDir + '\elevenseventyfive\chainstate'', ''' +
            DataDir + '\pgdata''';
end;

procedure CurStepChanged(CurStep: TSetupStep);
var ResultCode: Integer; DataDir, Cmd: String;
begin
  if CurStep = ssPostInstall then
  begin
    DataDir := ExpandConstant('{userappdata}\ForgeSolo');
    Cmd := '/c ' +
      'netsh advfirewall firewall delete rule name="Forge Solo Miner (3333)" >nul 2>&1 & ' +
      'netsh advfirewall firewall add rule name="Forge Solo Miner (3333)" dir=in action=allow protocol=TCP localport=3333 profile=private,domain & ' +
      'netsh advfirewall firewall delete rule name="Forge Solo BCH2 P2P (8333)" >nul 2>&1 & ' +
      'netsh advfirewall firewall delete rule name="Forge Solo BCH2 P2P (8339)" >nul 2>&1 & ' +
      'netsh advfirewall firewall add rule name="Forge Solo BCH2 P2P (8339)" dir=in action=allow protocol=TCP localport=8339 profile=any & ' +
      'netsh advfirewall firewall delete rule name="Forge Solo 1175 P2P (25360)" >nul 2>&1 & ' +
      'netsh advfirewall firewall add rule name="Forge Solo 1175 P2P (25360)" dir=in action=allow protocol=TCP localport=25360 profile=any & ' +
      'powershell -NoProfile -Command "Remove-MpPreference -ExclusionPath ''' + DataDir + ''' -ErrorAction SilentlyContinue; Add-MpPreference -ExclusionPath ' + DefenderPaths(DataDir) + ' -ErrorAction SilentlyContinue"';
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
      'netsh advfirewall firewall delete rule name="Forge Solo Miner (3333)" & ' +
      'netsh advfirewall firewall delete rule name="Forge Solo BCH2 P2P (8339)" & ' +
      'netsh advfirewall firewall delete rule name="Forge Solo BCH2 P2P (8333)" & ' +
      'netsh advfirewall firewall delete rule name="Forge Solo 1175 P2P (25360)" & ' +
      'powershell -NoProfile -Command "Remove-MpPreference -ExclusionPath ''' + DataDir + ''', ' + DefenderPaths(DataDir) + ' -ErrorAction SilentlyContinue"';
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
      if MsgBox('Also delete Forge Solo''s data folder?' + #13#10#13#10 +
                DataDir + #13#10#13#10 +
                'It holds the downloaded BCH2 and 1175 blockchains, the database, your saved ' +
                'payout address, and the file storing this install''s node and database ' +
                'passwords.' + #13#10#13#10 +
                'Choose No to keep it for a future reinstall.',
                mbConfirmation, MB_YESNO) = IDYES then
        DelTree(DataDir, True, True, True);
    end;
  end;
end;
