# Forge Solo for Windows: the build

Native Windows installer for **Forge Solo**: solo-mine BCH2 and merge-mine 1175 (ESF) from a
home PC. A Go tray launcher orchestrates a bundled PostgreSQL, the BCH2 and 1175 nodes, and the
stratum + api services, then serves the dashboard on `127.0.0.1:3080`.

The installed product contains no Docker and no container runtime: everything ships as plain
Windows executables. Docker appears only in the *build* instructions below, where it is used on
a Linux build host to run Inno Setup; see [Build](#build).

Solo means solo: the full block reward is paid **on-chain, directly by the coinbase** to your
address. There is no pool wallet, no fee, and no minimum payout.

## Layout
- `launcher/`: Go tray launcher/orchestrator (`main.go`, `boot.go`, `web.go`) + `forge-solo.ico`.
  Its own Go module. It ships for Windows only; it also builds on Linux, where CI runs its tests
  with stand-ins for the Windows parts.
- `forge-solo.iss`: Inno Setup installer script. It takes the dashboard (`../web/dist`) and the
  initial schema (`../init-db.sql`) straight from this repository, so there is no copy to drift.
- *(not tracked)* `bin/`: compiled exes + prebuilt node binaries; `pgsql/`: portable PostgreSQL

## Ports
Fixed, because the installer's firewall rules and the miner URL you type must match:

| Port | Purpose | Firewall rule |
|---|---|---|
| 3333 | stratum: point your ASIC/Bitaxe here | inbound, private+domain |
| 3335 | stratum for NiceHash / MiningRigRentals (a whole order on one connection, difficulty from 500,000), as on Umbrel and Linux | inbound, private+domain |
| 3080 | dashboard (`http://127.0.0.1:3080`) | none (loopback only) |
| 8339 | BCH2 P2P (incoming peers) | inbound, any profile |
| 25360 | 1175 P2P (incoming peers) | inbound, any profile |

Mining from the same PC (`127.0.0.1:3333`) needs no firewall rule at all.

The installer's rules open these ports on **this machine's** firewall. Nothing is opened on
your router: both nodes run with `upnp=0` and `natpmp=0`, so the app never reconfigures your
network on its own. Outbound peering works regardless; to *accept* inbound peers, forward 8339
and 25360 yourself.

Everything else (PostgreSQL, both node RPCs, ZMQ, the stratum's internal stats listener, and
the api) binds a **dynamically chosen loopback port** (`pickPort`) so it can never collide with
other software or land in a Windows reserved/excluded range. Each service picks from its own
300 ports between 30000 and 32099. Those ports are picked in `main()` before anything binds,
and every config and env var is regenerated from them on each launch. If a service finds none
of its ports free, Forge Solo starts nothing and says so in the tray: the services send their
passwords to these ports, so they never use one another program holds.

## External binaries (place in `bin/` before building the installer)
- `bitcoincashIId.exe`: BCH2 node (Windows release)
- `elevenseventyfived.exe`: 1175 node (Windows release)
- `stratum.exe`, `api.exe`: cross-compiled from this repository's `cmd/stratum` and `cmd/api`
  (see [Building locally](#building-locally))
- `pgsql/`: portable PostgreSQL **16.x**, extracted into `windows/` so that
  `windows\pgsql\bin\postgres.exe` exists

  Get the "Windows x86-64" binaries zip from
  <https://www.enterprisedb.com/download-postgresql-binaries>. The currently bundled build is
  **16.15**. Only `bin/`, `lib/` and `share/` are kept (the full archive is ~2.5x larger and
  the rest is pgAdmin and headers we never invoke).

  That build needs Microsoft's Visual C++ runtime, which a fresh Windows does not have, so its
  three DLLs are put beside PostgreSQL's programs (no admin rights needed):
  `python3 scripts/windows/vcruntime.py windows/pgsql/bin` (needs `7z` and `msiextract`). It takes
  them from Microsoft's installer at a pinned URL and checks each one.

  Stay on 16.x: a PostgreSQL data directory is bound to its major version, so shipping 17.x
  would leave every existing install unable to start its database. Moving *within* 16.x is
  safe and is how this gets patched -- 16.4 shipped for a long time and was roughly two years
  of minor releases behind, which is exactly the drift this note exists to prevent.

## Releases are built by CI

Tagging `v*` runs `.github/workflows/release.yml` at the repository root, which builds the whole
installer on a clean runner and publishes a **single signed `ForgeSolo-Setup-<version>.exe`** on
that version's release page, beside the Linux downloads. There is deliberately no second Windows
asset: two downloads means a user can pick the one that does not install.

Everything inside the installer is fetched during that run and **sha256-verified fail-closed** --
both node binaries from their own published releases, PostgreSQL from EnterpriseDB, and the three
Go executables compiled from this repository at the tag. So a release is reproducible from public sources
rather than from whatever was on someone's laptop. Bumping any pinned version means bumping its
hash in the same commit; the versions and hashes are the `env:` block at the top of the workflow.

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

## Building locally
Only needed to test a change before tagging; releases come from CI. Requires Go and Docker
(Docker only to run Inno Setup, which has no native Linux build).

```sh
# From the repository root.
# 1) services:
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags '-s -w' -o windows/bin/stratum.exe ./cmd/stratum
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags '-s -w' -o windows/bin/api.exe     ./cmd/api

# 2) exe icon resource (regenerate only if the icon changes):
(cd windows/launcher && rsrc -ico forge-solo.ico -arch amd64 -o rsrc.syso)

# 3) launcher:
(cd windows/launcher && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-H=windowsgui -s -w" -o ../bin/forge-solo.exe .)

# 4) installer: Inno Setup in a container, so this works on a Linux build host. Mount the
#    repository root: the script reads ../web/dist and ../init-db.sql. The image is pinned by
#    digest, the one CI uses (INNOSETUP_IMAGE in .github/workflows/release.yml).
docker run --rm -v "$PWD":/work amake/innosetup:innosetup6@sha256:81713b854eb12278021045dcb57701fe35312030b2dc1d37710184f294a23f81 windows/forge-solo.iss
```

The installer is written to `windows/ForgeSolo-Setup-<version>.exe`. CI stamps the tag's version;
`MyAppVersion` in `forge-solo.iss` is only the default for a local build.

## Design notes
- **Graceful shutdown:** the launcher stops both nodes via RPC `stop` so they flush the
  chainstate before exit, and a restart resumes instead of resyncing. The miner stops first (a
  block it is submitting needs the BCH2 node), then both nodes at once. A node still loading its
  blocks refuses to stop until it is ready, so it is asked again every second. Quit while Forge
  Solo is still starting stops what has started, and nothing starts after it. The tray icon stays,
  showing the stop, until everything has stopped. When Windows shuts down, restarts or signs out,
  the launcher hears it before other programs and stops everything at once: Windows gives a
  program with no window only a few seconds. If the shutdown is then cancelled (another program
  held it up, and someone chose Cancel), Forge Solo starts again.
- **Settings password:** saving a change in Settings needs Forge Solo's password, as on Umbrel:
  other programs and accounts on the PC can reach the dashboard and its API on 127.0.0.1. Right-click
  the tray icon and choose **Copy Settings Password**; the browser remembers it once a save works.
  It is made at the first start (for an existing install, at the first start of 1.0.13) and kept
  as `SETTINGS` in `secrets.env` in the data folder. The copy is kept out of Windows' clipboard
  history and cloud clipboard.
- **Secrets:** Forge Solo does not start if `secrets.env` cannot be read, or lacks the database
  password while the database exists, rather than make new passwords the database would refuse.
  The file is rewritten in a way a power cut cannot leave half written.
- **Dashboard port:** the dashboard is always at http://127.0.0.1:3080. If another program holds
  that port, the tray says so and the browser is not sent to that program; mining goes on.
- **A program that stops on its own** (the miner, the API or a node crashing) is started again after
  2 seconds; the wait doubles, up to a minute, while it keeps stopping at once. The tray and
  `launcher.log` say so. Programs Forge Solo stops itself (Quit, Restart Mining) are left stopped.
  A node that stops saying "Corrupted block database detected" (after a power cut, say) is
  started once with `-reindex`, which rebuilds its chain state from the blocks on disk; if that
  does not help, the tray says which folders to delete.
- **Ports kept to Forge Solo:** the miner, the API, the miner's stats and the dashboard listen with
  Windows's exclusive address use. Without it, a program started later could bind the same port
  over IPv4 and take every IPv4 connection: every miner, on 3333.
- **User names in another script:** the bundled PostgreSQL reads paths in the system's code page,
  so under a user name with characters outside it (a Chinese name on an English Windows) the
  database never started. Its paths are now given in their short (8.3) form when they need it; if
  the drive keeps no short names, `launcher.log` says so.
- **Public ports:** if another program holds 3333 (the usual port of mining software) or 8339, Forge
  Solo starts nothing and the tray names the port: the miner or the BCH2 node cannot run without it.
  One holding 3335 or 25360 leaves out rentals or merge mining, and `launcher.log` says so.
- **One at a time:** a second launch opens the running copy's dashboard. Forge Solo running for
  another Windows account counts too: it holds the same ports. The installer and the
  uninstaller ask for Forge Solo to be closed before they touch its files (`AppMutex`). If the tray
  icon cannot be added (Windows still setting up the taskbar at sign-in), Forge Solo starts again
  once after 90 seconds.
- **Signing:** CI signs the installer on the runner, with a timestamp, and publishes the
  certificate's SHA-256 fingerprint on the release page.
- **Installer:** one elevated step (a single UAC prompt) adds the firewall rules above and
  Defender exclusions for the folders written constantly (both nodes' blocks and chainstate,
  and the database), which Defender otherwise rescans on every write, the main cause of disk
  thrash on a laptop. The rest of `%APPDATA%\ForgeSolo` is still scanned. The file copy
  itself is a per-user install and needs no admin rights.
- **Startup:** mining runs only while the app is open. The installer offers an opt-in
  "Start Forge Solo when I sign in" (per-user `HKCU` entry, removed with the app, and by an
  update with the box unticked); without it, a reboot silently stops mining until someone
  launches it again.
- **Uninstall:** asks whether to delete `%APPDATA%\ForgeSolo`. Answering no keeps the chain
  data for a reinstall; answering yes also removes the file holding this install's node and
  database passwords. It is all-or-nothing on purpose: deleting only the secrets would leave a
  database the app can no longer open.
- **Config and secrets** live under `%APPDATA%\ForgeSolo`, which the launcher locks to the
  current user with `icacls` on every start. Go's `0600` file mode does nothing on Windows, so
  without that the folder is protected only by whatever it inherits. `config.yaml` is regenerated on every
  launch (so port changes always take effect); it mirrors the app's
  `docker/stratum/config.template.yaml`, and keys the stratum does not read are ignored silently,
  so keep the two in step: `windows_config_test.go` at the repository root fails when they differ.
- **Payout addresses** are stored in the database and set from the dashboard's Settings page,
  never in a config file in the repo.

## Not yet done (pre public release)
- SmartScreen still warns on first run: the installer is signed, but with a self-signed
  certificate. A CA-issued (OV/EV) certificate would remove the warning.
- Fresh-install test on a clean Windows 10 and Windows 11 box
- Test matrix: varied hardware and antivirus products
