# Forge Solo for Windows: the build

Native Windows installer for **Forge Solo**: solo-mine BCH2 and merge-mine 1175 (ESF) from a
home PC. A Go tray launcher starts the BCH2 and 1175 nodes and the stratum and api services,
which keep their data in one SQLite file, `forgesolo.db`, then serves the dashboard on
`127.0.0.1:3080`. PostgreSQL runs only to move the data of Forge Solo 1.0.12 and earlier into
that file.

To install and use Forge Solo, see [Install on Windows](../README.md#install-on-windows) in the
root README. This file is about how the Windows build is made and how it works.

The installed product contains no Docker and no container runtime: everything ships as plain
Windows executables. Docker appears only in the *build* instructions below, where it is used on
a Linux build host to run Inno Setup; see [Building locally](#building-locally).

Solo means solo: the full block reward is paid **on-chain, directly by the coinbase** to your
address. There is no pool wallet, no fee, and no minimum payout.

## Layout
- `launcher/`: Go tray launcher/orchestrator (`main.go`, `boot.go`, `web.go`, `migrate.go`) +
  `forge-solo.ico`. Its own Go module. It ships for Windows only; it also builds on Linux, where
  CI runs its tests with stand-ins for the Windows parts.
- `forge-solo.iss`: Inno Setup installer script. It takes the dashboard (`../web/dist`) straight
  from this repository, so there is no copy to drift.
- `gateway/`: Forge Gateway's tray app (its own Go module) and installer script; Forge Gateway is
  released from its own repository.
- *(not tracked)* `bin/`: compiled exes + prebuilt node binaries; `pgsql/`: PostgreSQL, for moving
  the data of 1.0.12 and earlier

## Ports
Fixed, because the installer's firewall rules and the miner URL you type must match:

| Port | Purpose | Firewall rule |
|---|---|---|
| 3333 | stratum: point your ASIC/Bitaxe here | inbound, private+domain |
| 3335 | stratum for NiceHash / MiningRigRentals, as on Umbrel and Linux: a whole order on one connection, from 500,000, then about one share every 25 s (MiningRigRentals asks for 10-60 s at the rig's advertised hashrate) | inbound, private+domain |
| 3080 | dashboard (`http://127.0.0.1:3080`) | none (loopback only) |
| 8339 | BCH2 P2P (incoming peers) | inbound, any profile |
| 25360 | 1175 P2P (incoming peers) | inbound, any profile |

Mining from the same PC (`127.0.0.1:3333`) needs no firewall rule at all. The rules for 3333 and
3335 apply only while Windows treats the network as Private (or a domain network): on a Public
network, miners on your network and rentals cannot reach the PC. Windows 11 makes new networks
Public. The dashboard's connect card and Settings say so.

The installer's rules open these ports on **this machine's** firewall. Nothing is opened on
your router: both nodes run with `upnp=0` and `natpmp=0`, so the app never reconfigures your
network on its own. Outbound peering works regardless; to *accept* inbound peers, forward 8339
and 25360 yourself.

Everything else (both node RPCs, ZMQ, the stratum's internal stats listener, the api, and during
a move the old database) binds a **dynamically chosen loopback port** (`pickPort`) so it can never
collide with other software or land in a Windows reserved/excluded range. Each service picks from
its own 300 ports between 30000 and 32099. Those ports are picked in `main()` before anything
binds, and every config and env var is regenerated from them on each launch. If a service finds
none of its ports free, Forge Solo starts nothing and says so in the tray: the services send their
passwords to these ports, so they never use one another program holds.

## The database
The api and the stratum keep everything in `%APPDATA%\ForgeSolo\forgesolo.db`, one SQLite file,
as on Umbrel and Linux. Each program that has it open holds `forgesolo.db.inuse` beside it.

Up to 1.0.12 the data was in PostgreSQL, in `pgdata` in the same folder. The launcher moves it once
(`migrate.go`), before the api and the stratum start:
- It decides from the files alone when it can: no `pgdata\PG_VERSION` means nothing to move, and
  `postgres-migrated.json` with the old data's `pg_control` unchanged means the move is done. A
  start then runs neither PostgreSQL nor the migrator.
- Otherwise `forge-solo-migrate.exe plan` decides: move, merge (1.0.12 ran on the old data again
  after the move), or nothing. For a move, the bundled PostgreSQL starts on the old data with
  `default_transaction_read_only` on, on 127.0.0.1 only, on a port from 30000-30299;
  `forge-solo-migrate prepare` copies the data beside `forgesolo.db` and checks every row and
  amount; PostgreSQL stops; `forge-solo-migrate commit` puts the copy in place. A merge keeps one
  `forgesolo.db.before-merge-<time>` copy of what it replaced.
- `pgdata` itself is kept, for going back to 1.0.12. The move changes none of the data in it;
  PostgreSQL updates its own bookkeeping files when it starts and stops. Deleting `pgdata`, even
  half way, never stops a start after a move.
- Once the move is checked, the launcher deletes `{app}\pgsql` and `pglog.txt`: only a move needs
  them. A move needed later (`forgesolo.db` deleted, or old data copied in after a fresh install)
  fails with a message that says to run the installer again, which installs PostgreSQL because
  `pgdata\PG_VERSION` exists.
- PostgreSQL reads its paths in the system's code page. A folder name it cannot take (a user name
  in another script) is given in its short (8.3) form, or else through a junction in a folder of
  the account's own under `%ProgramData%\ForgeSolo\links`, named from the SHA-256 of the account's
  SID (8 hex digits, such as `links\a5bfe9f8`) and removed once PostgreSQL has stopped. User names
  need nothing special.
- A move that fails replaces nothing and is recorded in `migration-status.json`. The api then runs
  in maintenance: the dashboard says what failed and offers **Start without the old data**, which
  writes `SKIP-POSTGRES-MIGRATION` beside `forgesolo.db`; the stratum does not mine; the nodes run;
  the tray says the move failed. Quit or Windows ending the session during a move ends it and puts
  nothing in place: the next start moves the data.

## External binaries (place in `bin/` before building the installer)
- `bitcoincashIId.exe`: BCH2 node (Windows release)
- `elevenseventyfived.exe`: 1175 node (Windows release)
- `stratum.exe`, `api.exe`, `forge-solo-migrate.exe`: cross-compiled from this repository's
  `cmd/stratum`, `cmd/api` and `cmd/forge-solo-migrate` (see [Building locally](#building-locally))
- `pgsql/`: portable PostgreSQL **16.x**, extracted into `windows/` so that
  `windows\pgsql\bin\postgres.exe` exists. Only a move of the old data runs it.

  Get the "Windows x86-64" binaries zip from
  <https://www.enterprisedb.com/download-postgresql-binaries>. The currently bundled build is
  **16.15**. Only `bin/`, `lib/` and `share/` are kept (the full archive is ~2.5x larger and
  the rest is pgAdmin and headers we never invoke).

  That build needs Microsoft's Visual C++ runtime, which a fresh Windows does not have, so its
  three DLLs are put beside PostgreSQL's programs (no admin rights needed):
  `python3 scripts/windows/vcruntime.py windows/pgsql/bin` (needs `7z` and `msiextract`). It takes
  them from Microsoft's installer at a pinned URL and checks each one.

  Stay on 16.x: a PostgreSQL data directory is bound to its major version, and 1.0.12 left a 16.x
  one. Moving *within* 16.x is safe and is how this gets patched.

## Releases are built by CI

Tagging `v*` runs `.github/workflows/release.yml` at the repository root, which builds the whole
installer on a clean runner and publishes a **single signed `ForgeSolo-Setup-<version>.exe`** on
that version's release page, beside the Linux downloads. There is deliberately no second Windows
asset: two downloads means a user can pick the one that does not install.

Everything inside the installer is fetched during that run and **sha256-verified fail-closed**:
both node binaries from their own published releases, PostgreSQL from EnterpriseDB, and the four
Go executables (`stratum.exe`, `api.exe`, `forge-solo-migrate.exe` and the launcher
`forge-solo.exe`) compiled from this repository at the tag. So a release is reproducible from public
sources rather than from whatever was on someone's laptop.
Bumping any pinned version means bumping its hash in the same commit; the versions and hashes are
the `env:` block at the top of the workflow.

Signing uses Ubuntu's own `osslsigncode` on the runner, never inside the Inno Setup container, so
the certificate never goes into a third-party image. It comes from two secrets of the `release`
environment, which only `v*` tags can use, and only a pushed tag's run asks for them, so the
certificate never reaches an ordinary test run, a manual run or a pull request:

| Secret | Contents |
|---|---|
| `WINDOWS_SIGNING_PFX_B64` | the PKCS#12 (`.pfx`) signing certificate, base64-encoded |
| `WINDOWS_SIGNING_PASSWORD` | its export password |

A tag build **fails** rather than publishing unsigned if the secret is missing, and it publishes
nothing that does not verify as signed by the Forge Solo certificate with a valid timestamp. The
release page it makes is a draft until the owner publishes it (the root README, "Releasing"). A
manual `workflow_dispatch` run builds the installer unsigned and publishes nothing, so the build
itself can be tested; its installer is kept for a day. To encode the certificate:
`base64 -w0 signing.pfx`.

Only the installer is signed, and with a self-signed certificate: see
[Smart App Control and SmartScreen](#smart-app-control-and-smartscreen).

## Building locally
Only needed to test a change before tagging; releases come from CI. Requires Go and Docker
(Docker only to run Inno Setup, which has no native Linux build). Put the
[external binaries](#external-binaries-place-in-bin-before-building-the-installer) in place first:
without `pgsql/` the installer does not compile.

```sh
# From the repository root.
# 1) services and the migrator:
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags '-s -w' -o windows/bin/stratum.exe ./cmd/stratum
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags '-s -w' -o windows/bin/api.exe     ./cmd/api
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-s -w -X main.version=<version>" -o windows/bin/forge-solo-migrate.exe ./cmd/forge-solo-migrate

# 2) exe icon resource, with the rsrc the release uses:
go install github.com/akavel/rsrc@v0.10.2
(cd windows/launcher && "$(go env GOPATH)/bin/rsrc" -ico forge-solo.ico -arch amd64 -o rsrc.syso)

# 3) launcher:
(cd windows/launcher && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-H=windowsgui -s -w -X main.version=<version>" -o ../bin/forge-solo.exe .)

# 4) installer: Inno Setup in a container, so this works on a Linux build host. Mount the
#    repository root: the script reads ../web/dist. The image is pinned by digest, the one CI
#    uses (INNOSETUP_IMAGE in .github/workflows/release.yml).
docker run --rm -v "$PWD":/work amake/innosetup:innosetup6@sha256:81713b854eb12278021045dcb57701fe35312030b2dc1d37710184f294a23f81 windows/forge-solo.iss
```

The installer is written to `windows/ForgeSolo-Setup-<version>.exe`. CI stamps the tag's version;
`MyAppVersion` in `forge-solo.iss` is only the default for a local build. The image runs as uid
1000: if that is not you, make `windows` writable for it first (`chmod a+w windows`), as CI does.

## Design notes
- **Graceful shutdown:** the launcher stops both nodes via RPC `stop` so they flush the
  chainstate before exit, and a restart resumes instead of resyncing. The miner stops first (a
  block it is submitting needs the BCH2 node), then both nodes at once. A node still loading its
  blocks refuses to stop until it is ready, so it is asked again every second. Quit while Forge
  Solo is still starting stops what has started, and nothing starts after it; a move under way is
  ended and its PostgreSQL stopped. The tray icon stays, showing the stop, until everything has
  stopped. When Windows shuts down, restarts or signs out, the launcher hears it before other
  programs and stops everything at once: Windows gives a program with no window only a few seconds.
  If the shutdown is then cancelled (another program held it up, and someone chose Cancel), Forge
  Solo starts again.
- **Settings password:** saving a change in Settings needs Forge Solo's password, as on Umbrel:
  other programs and accounts on the PC can reach the dashboard and its API on 127.0.0.1. Right-click
  the tray icon and choose **Copy Settings Password**; the browser remembers it once a save works.
  It is made at the first start (for an existing install, at the first start of 1.0.13) and kept
  as `SETTINGS` in `secrets.env` in the data folder. It keeps out other accounts, which cannot read
  that folder; a program running under your own account can. The copy is kept out of Windows'
  clipboard history and cloud clipboard.
- **Secrets:** Forge Solo does not start if `secrets.env` cannot be read, and never replaces a file
  it could not read. `DB=`, the old database's password, is needed only to move the old data and to
  go back to 1.0.12: it is never replaced, and without it only a move fails. The file is rewritten in
  a way a power cut cannot leave half written.
- **Dashboard port:** the dashboard is always at http://127.0.0.1:3080. If another program holds
  that port, the tray says so and the browser is not sent to that program; mining goes on, and the
  tray's **Try Again** opens the dashboard once the port is free. Its pages are served with
  `no-cache`. 1.0.12 sent them with none, and a browser showed its copy of 1.0.12's page for hours
  after an update, so Forge Solo opens the dashboard at `/solo?v=<version>` (after the start, from
  **Open Dashboard**, at a second launch and from Try Again), an address the browser has kept
  nothing for. A page asked for with this version's `v` is sent with `Clear-Site-Data: "cache"`:
  the browser drops what it kept of the dashboard, so the pages its links lead to, the scripts and
  the styles are this version's too.
- **A start that fails** (another program on 3333 or 8339, a port Windows reserves, `secrets.env`
  unreadable) leaves Forge Solo in the tray, which says why and offers **Try Again**: it starts
  Forge Solo again in place. A second launch only opens the dashboard's address, which nothing
  serves then, so use Try Again (or Quit and start again).
- **A program that cannot start** (an antivirus holding or removing it, Smart App Control, a file
  in use) is named in the tray and in `launcher.log` with the reason, and tried again after
  2 seconds, the wait doubling up to a minute. The tray says "running" only once both nodes, the
  api and the miner run. Restart Mining also starts a miner that is not running.
- **A program that stops on its own** (the miner, the api or a node crashing) is started again after
  2 seconds; the wait doubles, up to a minute, while it keeps stopping at once. The tray and
  `launcher.log` say so. Programs Forge Solo stops itself (Quit, Restart Mining) are left stopped.
  A node that stops saying "Error opening block database" or "Corrupted block database detected"
  (after a power cut, say) is started once with `-reindex`, which rebuilds its chain state from the
  blocks on disk; if that does not help, the tray says its chain is damaged, and `launcher.log`
  says which folders to delete. This works for the BCH2 node. The 1175 node (29.1.0) cannot
  rebuild a mainnet chain with `-reindex`: for it, delete `elevenseventyfive\blocks` and
  `elevenseventyfive\chainstate` in the data folder and start Forge Solo, and the node downloads
  its chain again.
- **Firewall rules per Windows account:** the rules are named for the account that installed them
  ("Forge Solo Miner (3333) for <account>"), so two accounts on one PC each keep their own; the
  unsuffixed rules of earlier releases are removed. Once the rules are in place, the installer
  keeps the account name they carry in `HKCU\Software\ForgeSolo` (`FirewallRulesAccount`), and the
  uninstaller removes the rules of that name, also after the account has been renamed; an update
  after a rename replaces the old-name rules. The kept name is used only if it cannot change the
  elevated commands (no double quote, `%` or control character, at most 256 characters); otherwise
  the account's current name is used. Two accounts with the same short name on a domain- or
  Entra-joined PC still share rule names.
- **Ports kept to Forge Solo:** the miner, the api, the miner's stats and the dashboard listen with
  Windows's exclusive address use. Without it, a program started later could bind the same port
  over IPv4 and take every IPv4 connection: every miner, on 3333.
- **Public ports:** at start the launcher reads Windows's TCP listener tables, IPv4 and IPv6, and
  tries only 127.0.0.1 and ::1 itself, so the check never listens beyond this PC. If another
  program holds 3333 (the usual port of mining software) or 8339, Forge Solo starts nothing and the
  tray names the port: the miner or the BCH2 node cannot run without it. `launcher.log` names the
  program and its process id. A port Windows keeps for itself, for IPv4 or for IPv6 alone, is
  reported as such; `netsh int ipv4 show excludedportrange protocol=tcp` (or `ipv6`) lists those
  ranges. If another program uses port 3335, the rental port, Forge Solo starts without it, and the
  tray, `launcher.log` and the dashboard say so: rentals have no port of their own until you stop
  that program, then restart Forge Solo. If Windows keeps 3335 for itself, they say that instead:
  rentals have no port of their own until Windows lets it go and you restart Forge Solo. If another
  program holds 25360, the 1175 node runs without incoming peers (`listen=0`), merge mining goes
  on, and `launcher.log` says so.
- **One at a time:** a second launch opens the running copy's dashboard. Forge Solo running for
  another Windows account counts too: it holds the same ports. If the tray icon cannot be added
  (Windows still setting up the taskbar at sign-in), Forge Solo starts again once after 90 seconds.
- **Updating or uninstalling while Forge Solo runs:** before the installer replaces a file, or the
  uninstaller removes one, it closes a Forge Solo running from its folder for this account the way
  the tray's Quit does (its tray window gets `WM_CLOSE`), says "Closing Forge Solo...", and waits
  until no program in that folder runs, both nodes and PostgreSQL included, whatever the version
  and also in a silent run. It never ends one by force. After two minutes it names those still
  running and offers Retry or Cancel; Cancel changes nothing, nor does a silent run that waited in
  vain. Only then does it check the mutex Forge Solo 1.0.13 and later hold while they run: one still
  held is Forge Solo running for another Windows account, which only that account can close. It
  says so and offers Retry or Cancel; a silent run ends there, having changed nothing. Before
  1.0.13, Windows' Restart Manager was left to close Forge Solo: it gave up after a few seconds,
  while Forge Solo was still stopping its nodes and database, and the installer stopped with an
  error (a silent install undid itself). To go back to 1.0.12, quit Forge Solo first: 1.0.12's
  installer cannot close it, and stops with that error.
- **Tray texts:** the Windows 11 taskbar shows only the first 64 characters of a tray tooltip, so
  every text the tray shows is made in `tips.go`, at most 63 characters, the point first: what is
  wrong or under way, then where to look. A program's own reason for not starting is cut to fit.
  `launcher.log` has the rest: which program holds a port and what to do, which folders to delete.
- **Signing:** CI signs the installer on the runner, with a timestamp, and publishes the
  certificate's SHA-256 fingerprint on the release page.
- **Installer:** 64-bit Windows only (Windows 10 or 11 on x64, Windows 11 on ARM); it refuses
  32-bit Windows and Windows 10 on ARM. One elevated step (a single UAC prompt, which names
  Windows Command Processor) adds the firewall rules above and Defender exclusions for the folders
  the nodes write constantly (both nodes' blocks and chainstate), which Defender otherwise rescans
  on every write, the main cause of disk thrash on a laptop. The rest of `%APPDATA%\ForgeSolo` is
  still scanned, and an update removes the whole-folder exclusion earlier versions added. The
  Ready page says beforehand what the prompt is for. Afterwards the installer checks that
  the four rules exist; if the prompt was refused or a rule is missing, it says so (miners on other
  devices cannot connect; run the installer again and choose Yes) and logs it (`/LOG`). The
  uninstaller's question mentions the prompt too, and if it cannot remove the rules or exclusions it
  says so, with the Windows Security steps. The file copy itself is a per-user install and needs no
  admin rights: run Setup normally, not with "Run as administrator". Run as administrator, Setup
  installs for the account it runs as, and its first page names that account.
- **What the installer puts on disk:** PostgreSQL (`{app}\pgsql`) only for an account with the data
  of 1.0.12 or earlier (`{userappdata}\ForgeSolo\pgdata\PG_VERSION` exists), so a fresh install has
  none. An update deletes `{app}\init-db.sql`, and `{app}\pgsql` for an account with no old data.
- **Startup:** mining runs only while the app is open. The installer offers an opt-in
  "Start Forge Solo when I sign in" (per-user `HKCU` entry, removed with the app, and by an
  update with the box unticked); without it, a reboot silently stops mining until someone
  launches it again.
- **Uninstall:** removes `{app}\pgsql` if it is there and the junctions a move cut short left
  under `%ProgramData%\ForgeSolo\links` (never what they lead to), then asks whether to delete
  `%APPDATA%\ForgeSolo`, saying what it holds: the blockchains, `forgesolo.db`, the newest
  before-merge copy, `pgdata` and `secrets.env`. No is the default and keeps everything for a
  reinstall. It is all-or-nothing on purpose: deleting only `secrets.env` would lose the old
  database's password, which moving its data or going back to 1.0.12 needs.
- **Config and secrets** live under `%APPDATA%\ForgeSolo`, which the launcher locks to the
  current user with `icacls` on every start. Go's `0600` file mode does nothing on Windows, so
  without that the folder is protected only by whatever it inherits. `config.yaml` is regenerated
  on every launch (so port changes always take effect); it mirrors the app's
  `docker/stratum/config.template.yaml`, and keys the stratum does not read are ignored silently,
  so keep the two in step: `windows_config_test.go` at the repository root fails when they differ.
- **Node settings:** both nodes run without a wallet and unpruned, as on Umbrel. Two settings are
  Windows only, a profile for a laptop: `par=1` and `maxconnections=40`. The 1175 node's
  `1175.conf` also names three `addnode` peers that Umbrel's does not.
- **Payout addresses** are stored in the database and set from the dashboard's Settings page,
  never in a config file in the repo.
- **launcher.log** in the data folder records what Forge Solo started and stopped, and is kept
  under 1 MB while Forge Solo runs. The miner's and the API's logs are beside it, `stratum.log` and
  `api.log`, each moved to `.1` at 20 MB, replacing the one before. While a log viewer holds a log
  open without letting it be renamed, the log grows past 20 MB and keeps its `.1`. The move is
  tried again each time the log has grown by another 1 MB, so it is moved at the first of those
  after the viewer is closed: hours later during a rental, days later on a quiet log.

## Smart App Control and SmartScreen
The installer is signed with a self-signed certificate (CN=BCH2 Software), and Forge Solo's own
programs inside it are not signed. Windows treats a self-signed signature the same as none.

- **Smart App Control** (Windows 11 only; Windows 10 has none): while it is On, it blocks the
  installer and the programs it installs, and Windows offers no exception for one app. To use
  Forge Solo, turn it off in **Windows Security → App & browser control → Smart App Control
  settings**. On a Windows 11 with its current updates it can be turned back on later in the same
  place; on an older one, turning it off lasts until Windows is reset. In Evaluation it blocks
  nothing, but Windows may switch it to On later.
- **SmartScreen** warns on every release ("Windows protected your PC": **More info**, then **Run
  anyway**), because a self-signed installer starts with no reputation.

A certificate from a CA in Microsoft's Trusted Root Program (Azure Artifact Signing, an OV
certificate, or SignPath Foundation for open source) shows a verified publisher, and Smart App
Control runs the files signed with it. Smart App Control checks every program and DLL that loads,
not only the installer, so each one the installer carries would need that signature: Forge Solo's
own, the two nodes', and PostgreSQL's (installed only to move old data; EnterpriseDB leaves most of
its files unsigned, and SignPath signs only what is built from your own source). SmartScreen would
still warn until reputation builds; an EV certificate no longer skips that.

## Known limitations and still to do
- The certificate is self-signed: Smart App Control blocks Forge Solo while it is On, and
  SmartScreen warns on every release (above).
- The automatic `-reindex` of a damaged chain helps the BCH2 node only (see the design notes).
- Fresh-install and update tests on a real Windows 10 PC.
- Test matrix: varied hardware and antivirus products.
