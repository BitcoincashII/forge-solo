# Forge Solo — Windows build

Native Windows installer for **Forge Solo**: solo-mine BCH2 and merge-mine 1175 (ESF) from a
home PC. A Go tray launcher orchestrates a bundled PostgreSQL, the BCH2 and 1175 nodes, and the
stratum + api services, then serves the dashboard on `127.0.0.1:3080`.

The installed product contains no Docker and no container runtime — everything ships as plain
Windows executables. Docker appears only in the *build* instructions below, where it is used on
a Linux build host to run Inno Setup; see [Build](#build).

Solo means solo: the full block reward is paid **on-chain, directly by the coinbase** to your
address. There is no pool wallet, no fee, and no minimum payout.

## Layout
- `launcher/` — Go tray launcher/orchestrator (`main.go`, `boot.go`, `web.go`) + `forge-solo.ico`
- `web/` — the dashboard, a verbatim mirror of the app's `web/dist`
- `forge-solo.iss` — Inno Setup installer script
- `init-db.sql` — initial Postgres schema (the services also create any missing tables at
  startup with `CREATE TABLE IF NOT EXISTS`, so this file is a head start, not the whole schema)
- *(not tracked)* `bin/` — compiled exes + prebuilt node binaries; `pgsql/` — portable PostgreSQL

## Ports
Fixed, because the installer's firewall rules and the miner URL you type must match:

| Port | Purpose | Firewall rule |
|---|---|---|
| 3333 | stratum — point your ASIC/Bitaxe here | inbound, private+domain |
| 3080 | dashboard (`http://127.0.0.1:3080`) | none — loopback only |
| 8339 | BCH2 P2P (incoming peers) | inbound, any profile |
| 25360 | 1175 P2P (incoming peers) | inbound, any profile |

Mining from the same PC (`127.0.0.1:3333`) needs no firewall rule at all.

The installer's rules open these ports on **this machine's** firewall. Nothing is opened on
your router: both nodes run with `upnp=0` and `natpmp=0`, so the app never reconfigures your
network on its own. Outbound peering works regardless; to *accept* inbound peers, forward 8339
and 25360 yourself.

Everything else — PostgreSQL, both node RPCs, ZMQ, the stratum's internal stats listener, and
the api — binds a **dynamically chosen loopback port** (`pickPort`) so it can never collide with
other software or land in a Windows reserved/excluded range. Those ports are picked in `main()`
before anything binds, and every config and env var is regenerated from them on each launch.

## External binaries (place in `bin/` before building the installer)
- `bitcoincashIId.exe` — BCH2 node (Windows release)
- `elevenseventyfived.exe` — 1175 node (Windows release)
- `stratum.exe`, `api.exe` — cross-compiled from the forge-solo app (see Build step 1)
- `pgsql/` — portable PostgreSQL **16.x**, extracted at the repo root so that
  `pgsql\bin\postgres.exe` exists

  Get the "Windows x86-64" binaries zip from
  <https://www.enterprisedb.com/download-postgresql-binaries>. The currently bundled build is
  **16.10**. Only `bin/`, `lib/` and `share/` are kept (the full archive is ~2.5x larger and
  the rest is pgAdmin and headers we never invoke).

  Stay on 16.x: a PostgreSQL data directory is bound to its major version, so shipping 17.x
  would leave every existing install unable to start its database. Moving *within* 16.x is
  safe and is how this gets patched -- 16.4 shipped for a long time and was roughly two years
  of minor releases behind, which is exactly the drift this note exists to prevent.

## Releases are built by CI

Tagging `v*` runs `.github/workflows/release.yml`, which builds the whole installer on a clean
runner and publishes a **single signed `ForgeSolo-Setup-<version>.exe`**. There is deliberately
no second zip asset: two downloads means a user can pick the one that does not install.

Everything inside the installer is fetched during that run and **sha256-verified fail-closed** --
both node binaries from their own published releases, PostgreSQL from EnterpriseDB, and the three
Go executables compiled from the app repo. So a release is reproducible from public sources
rather than from whatever was on someone's laptop. Bumping any pinned version means bumping its
hash in the same commit; the versions and hashes are the `env:` block at the top of the workflow.

Signing uses `osslsigncode` inside the same Inno Setup container, from two repository secrets:

| Secret | Contents |
|---|---|
| `WINDOWS_SIGNING_PFX_B64` | the PKCS#12 (`.pfx`) signing certificate, base64-encoded |
| `WINDOWS_SIGNING_PASSWORD` | its export password |

A tag build **fails** rather than publishing unsigned if the secret is missing. A manual
`workflow_dispatch` run still builds without it, so the build itself can be tested. To encode the
certificate: `base64 -w0 signing.pfx`.

## Building locally
Only needed to test a change before tagging; releases come from CI. Requires Go and Docker
(Docker only to run Inno Setup, which has no native Linux build).

```sh
# 1) app services, from a checkout of the forge-solo app at the release tag:
cd /path/to/forge-solo
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags '-s -w' -o /path/to/forge-solo-windows/bin/stratum.exe ./cmd/stratum
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags '-s -w' -o /path/to/forge-solo-windows/bin/api.exe     ./cmd/api

# 2) exe icon resource (regenerate only if the icon changes):
cd launcher && rsrc -ico forge-solo.ico -arch amd64 -o rsrc.syso && cd ..

# 3) launcher:
cd launcher && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-H=windowsgui -s -w" -o ../bin/forge-solo.exe . && cd ..

# 4) installer — Inno Setup in a container, so this works on a Linux build host:
docker run --rm -v "$PWD":/work amake/innosetup forge-solo.iss
```

The installer is written to `ForgeSolo-Setup-<version>.exe`; the version lives in
`forge-solo.iss` (`MyAppVersion`) and should track the app release the exes were built from.

## Design notes
- **Graceful shutdown** — the launcher stops both nodes via RPC `stop` so they flush the
  chainstate before exit, and a restart resumes instead of resyncing.
- **Installer** — one elevated step (a single UAC prompt) adds the firewall rules above and a
  Defender exclusion for `%APPDATA%\ForgeSolo`, which otherwise gets rescanned on every
  blockchain/DB write — the main cause of disk thrash on a laptop. The file copy itself is a
  per-user install and needs no admin rights.
- **Startup** — mining runs only while the app is open. The installer offers an opt-in
  "Start Forge Solo when I sign in" (per-user `HKCU` entry, removed with the app); without it,
  a reboot silently stops mining until someone launches it again.
- **Uninstall** — asks whether to delete `%APPDATA%\ForgeSolo`. Answering no keeps the chain
  data for a reinstall; answering yes also removes the file holding this install's node and
  database passwords. It is all-or-nothing on purpose: deleting only the secrets would leave a
  database the app can no longer open.
- **Config and secrets** live under `%APPDATA%\ForgeSolo`, which the launcher locks to the
  current user with `icacls` on every start. Go's `0600` file mode does nothing on Windows, so
  without that the folder is protected only by whatever it inherits. `config.yaml` is regenerated on every
  launch (so port changes always take effect); it mirrors the app's
  `docker/stratum/config.template.yaml`, and keys the stratum does not read are ignored silently,
  so keep the two in step.
- **Payout addresses** are stored in the database and set from the dashboard's Settings page,
  never in a config file in the repo.

## Not yet done (pre public release)
- Code signing (unsigned → SmartScreen warning on first run)
- Fresh-install test on a clean Windows 10 and Windows 11 box
- Test matrix: varied hardware and antivirus products
