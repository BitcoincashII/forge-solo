# Forge Solo

Mine **BCH2** at home on your own full node: **solo**, where a block you find pays you in full,
or **TIDES**, where blocks are shared through Forge Pool's TIDES window. In solo mode on Umbrel
and Windows it also **merge-mines 1175 (ESF)** at no extra hashrate cost. Built on the hardened
Forge Pool engine, packaged for a single household: **no PPLNS, no pool fee**.

It runs on **Umbrel**, **Windows** and **Linux**, all built from this repository
([Build from source](#build-from-source)).

## Install on Umbrel

1. In Umbrel: **Settings → App Store → Community App Stores → Add**, and paste:
   `https://github.com/BitcoincashII/umbrel-app-store`
   Add the **app-store** repo, not `bitcoincashII-core`: that is the node and wallet source code,
   not an app store, and Umbrel fails to load it.
2. Open **BCH2 Community Apps** and install **Forge Solo**.
3. On the app's **Settings** page, set your BCH2 payout address (and your 1175 payout address, to
   merge-mine). Saving asks for Forge Solo's password: umbrelOS shows it when you open the app, and
   any time later when you right-click the Forge Solo icon (**Settings** → **Default credentials**).
4. Point your miner at `stratum+tcp://<your-umbrel-ip>:3333`. The worker username can be any label.

## Install on Windows

Forge Solo needs 64-bit Windows: Windows 10 or 11 on an x64 PC, or Windows 11 on ARM.

1. Download `ForgeSolo-Setup-<version>.exe` from the
   [latest release](https://github.com/BitcoincashII/forge-solo/releases/latest).
2. Run the installer. When SmartScreen says "Windows protected your PC", choose **More info**, then
   **Run anyway**.
3. Forge Solo is not yet signed by a certificate Windows trusts. If Smart App Control on Windows 11
   blocks the installer or Forge Solo, turn Smart App Control off: **Windows Security → App &
   browser control → Smart App Control settings**, then **Off**, and run the installer again. On a
   Windows 11 with its current updates you can turn it back on later in the same place; on an older
   one, turning it off lasts until Windows is reset. Windows 10 has no Smart App Control.
4. Windows asks once whether Windows Command Processor may make changes to your device: choose
   **Yes**. That adds the firewall rules that let miners on your network reach Forge Solo (3333,
   3335) and other nodes reach its nodes (8339, 25360), and Defender exclusions for the blockchains.
5. Set your network to **Private**: in Windows Settings, under **Network & internet**, open your
   connection's properties and set its network profile to Private. While it is Public, miners on
   your network cannot connect. Windows 11 makes new networks Public.
6. Forge Solo starts in the notification area of the taskbar and opens its dashboard. On the
   **Settings** page, set your BCH2 payout address (and your 1175 payout address, to merge-mine).
   Saving asks for the Settings password: right-click the Forge Solo icon, choose **Copy Settings
   Password**, and paste it.
7. Point your miners at `stratum+tcp://<PC-IP>:3333`, where `<PC-IP>` is the PC's address on your
   network; the dashboard shows it. Rentals from NiceHash and MiningRigRentals use port **3335**.

Forge Solo mines while it runs. Tick **Start Forge Solo when I sign in** in the installer to have
it start with Windows. [windows/README.md](windows/README.md) says how the Windows build works.

## Install on Linux

Download `forge-solo-<version>-linux-<arch>.tar.gz` from the
[latest release](https://github.com/BitcoincashII/forge-solo/releases/latest), for x86_64, aarch64,
armv7l, armv6l, i686 or riscv64; it is fully static. [packaging/linux/README.md](packaging/linux/README.md)
has the steps. Saving Settings asks for Forge Solo's password, `DASHBOARD_PASSWORD` in `secrets.env`
in the data directory.

Windows 1.0.12 and earlier are on the [old Windows repository's releases](https://github.com/BitcoincashII/forge-solo-windows/releases).

Running your own node and mining setup instead? [Forge Gateway](https://github.com/BitcoincashII/forge-gateway)
mines into Forge Pool's TIDES window without Forge Solo. Its source is `cmd/forge-gateway` here.

## What's inside

On every platform:
- a BCH2 full node, which syncs on its own and keeps the whole chain so other nodes can sync from it
- on Umbrel and Windows, a 1175 (ESF) node for AuxPoW merge-mining (its program is checksum-verified)
- the mining service (stratum): port 3333 for your miners, 3335 for NiceHash and MiningRigRentals
- the dashboard and its API: hashrate, effort, your blocks, payouts and the TIDES window
- `forgesolo.db`, one SQLite file that holds your settings, blocks and payouts

On Umbrel each part is a container: `node`, `node1175`, `stratum`, `api` and `web`. Before the
api and the stratum, `migrate` runs at each start and exits: it moves the data of Forge Solo
1.0.12 and earlier into `forgesolo.db` once, and afterwards finds nothing to do. A `postgres`
container that does nothing keeps the name of the database server 1.0.12 ran, so that an update
stops that server cleanly first.

## Your data from 1.0.12 (Umbrel and Windows)

Up to 1.0.12, Forge Solo on Umbrel and Windows kept its data in PostgreSQL. The first start of
1.0.13 moves it into `forgesolo.db`, once, and checks every row before it replaces anything.

The old database stays where it was, so you can go back to 1.0.12: `postgres/` in the app's data
on Umbrel, `pgdata` in `%APPDATA%\ForgeSolo` on Windows. It also holds the shares 1.0.12 stored.
Going back to 1.0.12 and forward again keeps what both versions recorded. On Windows, right-click
the Forge Solo icon and choose **Quit Forge Solo** before you run 1.0.12's installer: it cannot
close Forge Solo itself.

Once 1.0.13 shows your blocks and settings, you can delete the old database to free its space:
deleting it, even half way, never stops Forge Solo, but 1.0.12 then no longer has your data. On
Umbrel, from the umbrelOS terminal or over SSH:
`sudo rm -rf ~/umbrel/app-data/bch2-apps-forge-solo/postgres`. On Windows, delete the `pgdata`
folder in `%APPDATA%\ForgeSolo`.

If the move fails, nothing is lost: Forge Solo does not mine, the nodes keep running, and the
dashboard says what went wrong. Restart Forge Solo to try again, or choose **Start without the old
data** there (it asks for the Settings password) to start on a new, empty database; Settings can
bring the old data in later.

## Connect a miner

On the dashboard's **Settings** page, set your **BCH2 payout address**, and on Umbrel and Windows
your **1175 payout address** to merge-mine. They are stored in `forgesolo.db`, so they survive
updates. Saving asks for Forge Solo's password, which other apps on an Umbrel, and other accounts
on a computer, do not have:
- **Umbrel:** umbrelOS shows it when you open Forge Solo, and under right-click on its icon →
  **Settings** → **Default credentials**.
- **Windows:** right-click the Forge Solo icon in the notification area and choose **Copy Settings
  Password**.
- **Linux:** `DASHBOARD_PASSWORD` in `secrets.env` in the data directory
  (`sudo cat /var/lib/forge-solo/secrets.env` for the service).

Then point your miner at `stratum+tcp://<forge-solo-ip>:3333`, the address of the Umbrel, PC or
Linux machine on your network. The dashboard shows it.

There is no payout schedule. In solo mode a block you find pays you **directly in that block's
coinbase**: the reward is yours on-chain as soon as the block is accepted, spendable after the
usual 100-block coinbase maturity. In TIDES mode every TIDES block (a block found on work Forge
Pool registered) pays you the same way, in proportion to your work in the window, and an amount
under 546 satoshis is carried forward to a later one. The dashboard's TIDES page explains it.

The worker username is **just a label**: `rig1`, `bitaxe`, anything. It has no payout
role: every solo block pays your configured BCH2 address, and in TIDES mode every share is
credited to it. Supplying
`<your-address>.<label>` also works. Give each device its own label (`rig1`, `rig2`): the
difficulty Forge Solo remembers across reconnects belongs to the label and the address the miner
connects from, so devices that share both share it.

## Open these ports in your router

Forge Solo sits on your home network, so nothing outside can reach it until you forward the
ports. Forward each of these to the address of the machine Forge Solo runs on:

| Port | Protocol | What it is | Needed for |
|------|----------|-----------|-----------|
| **8339** | TCP | BCH2 peer-to-peer | Accepting inbound peers. Optional, but it helps the network and improves your node's connectivity. |
| **25360** | TCP | 1175 (ESF) peer-to-peer (Umbrel and Windows) | Same, for the merge-mined chain. |
| **3333** | TCP | Stratum: your miners and **Braiins** | Only if hashpower reaches you from outside your network. Miners on your own network do not need it. |
| **3335** | TCP | Stratum: **NiceHash / MiningRigRentals** | Same, and it is required for those marketplaces, which always connect from outside. |

The nodes listen for inbound peers, so forwarding is the only step left on your side. On Windows,
miners on your own network reach the PC only while Windows treats your network as **Private**.

**Rented hashpower will not reach you without forwarding a mining port.** A marketplace
connects from the internet, so `192.168.x.x` is not an address it can use. Forward 3333
(Braiins) or 3335 (NiceHash/MRR), and give the marketplace your public IP or a hostname
that resolves to it.

**If another program uses port 3335, the rental port** (Windows and Linux), Forge Solo starts
without it and says so, and rentals have no port of their own. Stop that program, then restart
Forge Solo. On Umbrel the install stops instead (see **Umbrel: ports other apps use**). Windows
can also keep 3335 for itself (a range reserved for Hyper-V, WSL or Docker): Forge Solo then says
so, and rentals have no port of their own until Windows lets it go and you restart Forge Solo.

The nodes' **RPC ports are never reachable from your network**: on Umbrel they stay on the app's
private network, and on Windows and Linux they listen on 127.0.0.1 only. Never forward them:
anyone reaching them could control the node. A stratum port open to the internet accepts
connections from anyone; in solo that only means a stranger could mine *to your address*, which
costs you nothing, but keep it closed if you have no reason to open it.

## Rented hashpower (Braiins and other marketplaces)

There are two stratum ports, and which one you use depends on how the marketplace connects:

| Port | For | Difficulty |
|------|-----|-----------|
| **3333** | Your own hardware, **and Braiins** | 1024, then about one share every 5 s per miner |
| **3335** | **NiceHash / MiningRigRentals** | 500,000, then about one share every 25 s (MiningRigRentals asks for 10-60 s at the rig's advertised hashrate) |

Braiins belongs on 3333 because it connects **each miner individually** rather than proxying
them onto one connection, so every connection is one miner's hashrate and needs to find its
own level from a low floor. NiceHash and MRR aggregate many miners behind a single
connection, so that connection carries the whole order's hashrate and starts high.

Both ports use the 8-byte extranonce2 every marketplace requires.

    URL:      stratum+tcp://<your-public-ip>:3333   (Braiins)
              stratum+tcp://<your-public-ip>:3335   (NiceHash / MiningRigRentals)
    Worker:   any label (e.g. rented-01), or <your-bitcoincashii-address>.<label>
    Password: d=2000000        # optional, see below

Point the order at a host the marketplace can actually reach: a machine on your home network is
not reachable from the internet without a port forward or a tunnel.

**Difficulty.** The stratum sizes each connection to its own hashrate. A connection opens at
its port's floor, 1024 on 3333 and 500,000 on 3335, unless it resumes the level remembered from
before a reconnect. At its tenth share, a connection whose rate is far above the floor moves
straight to the difficulty that rate warrants rather than climbing in +50% steps: well under a
second for a large miner on 3333, and seconds for a rental on 3335 (about 20 at 1 PH/s, 5 at 4.5
PH/s). From then on vardiff aims for one share every 5 seconds on 3333; on 3335, one share every
25 seconds, inside the 10 to 60 seconds MiningRigRentals asks for at the rig's advertised
hashrate. Setting `d=<difficulty>` in the password skips the climb: the connection opens exactly
there. The hint is a starting point, not a lock: vardiff still tracks the connection afterwards,
so a hint that turns out to be wrong corrects itself. It is clamped to the same floor and maximum
as every other path, so `d=1` cannot flood the miner and an absurd value cannot park a connection
where it never submits. No connection is given a difficulty above the network's.

**Payout.** Rented hashpower mines **solo to your address**, exactly like your own
hardware: the reward of any block it finds goes to your configured BCH2 payout address,
and the worker label is only a label.

## Umbrel: ports other apps use

Forge Solo needs ports 3333 and 3335 for miners, and 8339 and 25360 for the nodes' peers. If
another app already uses one, installing stops with "exit code 1", and the parts of Forge Solo
that had started, such as the nodes, keep running in the background, also after a restart, where
umbrelOS does not show them. Free the port and install Forge Solo again: it takes them over.

An app installed after Forge Solo whose own port is 3333 or 3335 takes that port from Forge Solo
without an error. Bleskomat Server uses 3333, so every miner would reach it instead, and Bitmagnet
uses 3335, the rental port: neither can run beside Forge Solo.

## If a node's chain data is damaged

After a power cut or a full disk, a node can find its chain data damaged and stop at every start.
The BCH2 node then rebuilds it once on its own: it starts again with `-reindex`, which takes a few
minutes, and Forge Solo says so. The 1175 node cannot rebuild a mainnet chain that way. If it is
the 1175 node, or the BCH2 node's data is still damaged after the rebuild, stop Forge Solo, delete
that node's `blocks` and `chainstate` folders, and start Forge Solo again. The node downloads its
chain again, and your settings and block history stay.

- **Umbrel:** from the umbrelOS terminal or over SSH, for the BCH2 node:
  `sudo rm -rf ~/umbrel/app-data/bch2-apps-forge-solo/node/{blocks,chainstate}`; for the 1175 node,
  the same in `node1175`.
- **Windows:** the tray says the node's chain is damaged, or keeps saying the node stopped and is
  started again, and `launcher.log` says its chain data is damaged. Right-click the Forge Solo icon
  and choose **Quit**. Then, in `%APPDATA%\ForgeSolo`, delete `elevenseventyfive\blocks` and
  `elevenseventyfive\chainstate` for the 1175 node, or `bch2\blocks` and `bch2\chainstate` for the
  BCH2 node.
- **Linux:** it has the BCH2 node only. Stop Forge Solo (Ctrl-C, or for the service
  `sudo systemctl stop forge-solo`) and delete `bch2/blocks` and `bch2/chainstate` in the data
  directory. See [If something goes wrong](packaging/linux/README.md#if-something-goes-wrong).

## Security on Umbrel
- Every secret (both nodes' RPC passwords, the internal API token) is **generated per install** (`exports.sh`); nothing is hardcoded.
- Settings takes no change without **Forge Solo's own password**, which umbrelOS shows only to you: other apps on the Umbrel can reach Forge Solo directly.
- Other apps reach only the dashboard. The nodes, the API and the stratum talk over a **network of their own**, and the node RPC ports are not published to the host.
- Internal API fails **closed** without its token.
- 1175 node binary is **checksum-verified**; images are pinned by digest.
- Share work is credited as `min(assigned, proven)`, so credit cannot be inflated.

## Build from source

Every release is built from this repository: the Umbrel images and the Windows installer by CI
(`.github/workflows/docker-build.yml` and `release.yml`), the Linux downloads by
`scripts/linux/build-release.sh`, run by hand (see [Releasing](#releasing)). The commands below are
theirs, for a Linux shell.

You need Git, Go 1.21 or newer, a C compiler for the race tests (gcc, which `-race` needs on Linux),
and Docker for the images, the installers, the Linux downloads and the integration tests. `go.mod`
names the Go the releases are built with, `toolchain go1.27.2`: an older Go downloads go1.27.2 the
first time it runs in the clone and builds with it (unless `GOTOOLCHAIN` is `local`), and a newer
one builds with itself. `GOTOOLCHAIN=go1.27.2` makes any of them build with go1.27.2.

```sh
git clone https://github.com/BitcoincashII/forge-solo
cd forge-solo
go version        # go1.27.2, or the newer Go you have
```

### The programs

Each program has one build, with no build tags. They are static (`CGO_ENABLED=0`: the SQLite driver
is pure Go), so one machine builds them for every platform. The releases stamp the version into the
three that have one, with `-X main.version`.

| Program | Source | What it is |
|---|---|---|
| `stratum` | `cmd/stratum` | the mining service |
| `api` | `cmd/api` | the dashboard's API |
| `forge-solo-migrate` | `cmd/forge-solo-migrate` | moves the data of 1.0.12 and earlier into `forgesolo.db` |
| `forge-solo` | `cmd/forge-solo-linux` | Forge Solo for Linux: runs the node, the stratum, the api and the dashboard |
| `forge-gateway` | `cmd/forge-gateway` | [Forge Gateway](https://github.com/BitcoincashII/forge-gateway) |

On Linux, for this machine, into `dist/`, which Git ignores:

```sh
V=1.0.13
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o dist/stratum ./cmd/stratum
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o dist/api ./cmd/api
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$V" -o dist/forge-solo-migrate ./cmd/forge-solo-migrate
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$V" -o dist/forge-solo ./cmd/forge-solo-linux
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$V" -o dist/forge-gateway ./cmd/forge-gateway
```

For another platform, add `GOOS` and `GOARCH`, and `GOARM` for 32-bit ARM. Forge Solo for Linux
ships for `amd64`, `arm64`, `arm` with `GOARM=7` and with `GOARM=6`, `386` and `riscv64` (what
`uname -m` calls x86_64, aarch64, armv7l, armv6l, i686 and riscv64); the migrator for `linux/amd64`,
`linux/arm64` and `windows/amd64`; Forge Gateway for `linux/amd64`, `linux/arm64` and
`windows/amd64`. `go build ./...` checks that everything builds, as CI's last unit step does.

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags "-s -w -X main.version=$V" -o dist/armv7l/forge-solo ./cmd/forge-solo-linux
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$V" -o dist/forge-gateway.exe ./cmd/forge-gateway
```

### The Umbrel images

`docker-build.yml` builds six images, each for `linux/amd64` and `linux/arm64`, and pushes them to
GHCR. For this machine, with the same Dockerfiles, contexts and build argument:

```sh
docker build -f docker/api/Dockerfile -t forge-solo-api:dev .
docker build -f docker/stratum/Dockerfile -t forge-solo-stratum:dev .
docker build -f docker/web/Dockerfile -t forge-solo-web:dev .
docker build -f docker/migrate/Dockerfile --build-arg VERSION=$V -t forge-solo-migrate:dev .
docker build -f docker/node/Dockerfile -t forge-solo-node:dev docker/node
docker build -f docker/node1175/Dockerfile -t forge-solo-node1175:dev docker/node1175
```

The migrate image refuses to build without `VERSION`, which the migrator records in what it writes.
The node images download the node release for the platform they are built for, which BuildKit
(Docker's builder since Docker 23) tells them, and check its SHA-256. For `linux/arm64` on an x86
machine, register QEMU once, as CI does, and name the platform; the api and stratum images then
compile under QEMU, which is slow:

```sh
docker run --privileged --rm tonistiigi/binfmt --install arm64
docker buildx build --platform linux/arm64 --load -f docker/api/Dockerfile -t forge-solo-api:dev-arm64 .
```

CI builds both platforms in one step and pushes them: that needs a builder of its own
(`docker buildx create --use`) and a registry.

### The Windows installers

Forge Solo's: [Building locally](windows/README.md#building-locally) in windows/README.md has the
commands. They build `stratum.exe`, `api.exe`, `forge-solo-migrate.exe` and the launcher
`forge-solo.exe` (`windows/launcher`, a module of its own) into `windows/bin` with
`GOOS=windows GOARCH=amd64`, and compile `windows/forge-solo.iss` with Inno Setup in Docker, in the
image `release.yml` pins (`INNOSETUP_IMAGE`). The two nodes go in `windows/bin` first, and
PostgreSQL 16.15 with the Visual C++ runtime it needs in `windows/pgsql`, as windows/README.md's
[External binaries](windows/README.md#external-binaries-place-in-bin-before-building-the-installer)
says. The installer job of `release.yml` fetches each at a pinned URL and checks its SHA-256; its
fetch steps run as they are on Linux, with `curl`, `7z`, `msiextract`, `python3` and the versions
and hashes of its `env:`, once `mkdir -p windows/bin` has made the folder (a clone does not have
it; in `release.yml` the build step makes it first).

Forge Gateway's: [Building locally](windows/gateway/README.md#building-locally) in
windows/gateway/README.md. It puts `forge-gateway.exe`, the tray app `forge-gateway-tray.exe`
(`windows/gateway/launcher`) and the forge-gateway repository's LICENSE in `windows/gateway/bin`,
and compiles `windows/gateway/forge-gateway.iss` the same way. Forge Gateway's releases are built by
[its repository](https://github.com/BitcoincashII/forge-gateway), from the forge-solo commit its
`FORGE_SOLO_COMMIT` names; its README's Source section has the commands.

### The Linux downloads

`scripts/linux/build-release.sh <version> [arch...]` builds them on Linux, with Go and Docker, into
`dist/forge-solo-<version>-linux-<arch>.tar.gz` and `dist/SHA256SUMS-linux`, for all six
architectures unless you name some. It builds only a clean checkout of the tag `v<version>`: it
refuses uncommitted or untracked changes, any other commit, and a version `umbrel-app.yml` does not
give.

Each download holds a BCH2 node built fully static from bitcoincashII-core v27.0.2, and the script
packages a node only if its SHA-256 is the one it pins: the node the releases ship. It takes the
nodes from `.linux-build/out/<arch>/`, and builds a missing one from source with
`scripts/linux/build-node.sh` in an Alpine container of that platform: under QEMU for ARM and
RISC-V (`docker run --privileged --rm tonistiigi/binfmt --install arm64,arm,riscv64`), which takes
hours. A node built from source has other bytes, which the script refuses until they are checked
and pinned. To build with the nodes the releases ship, take them from a release's downloads first:

```sh
V=1.0.13
git checkout v$V
for a in x86_64 aarch64 armv7l armv6l i686 riscv64; do
  mkdir -p .linux-build/out/$a
  curl -fsSL https://github.com/BitcoincashII/forge-solo/releases/download/v$V/forge-solo-$V-linux-$a.tar.gz |
    tar -xzf - -C .linux-build/out/$a --strip-components=2 \
      forge-solo-$V-linux-$a/bin/bitcoincashIId forge-solo-$V-linux-$a/bin/bitcoincashII-cli
done
scripts/linux/build-release.sh $V
```

To try a change on Linux without all that, put your programs into an unpacked download, which has
the node, and run `./forge-solo` there as [packaging/linux/README.md](packaging/linux/README.md)
says. For another machine, add `GOOS=linux GOARCH=...` as above.

```sh
d=~/forge-solo-1.0.13-linux-x86_64        # the unpacked download
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $d/bin/stratum ./cmd/stratum
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $d/bin/api ./cmd/api
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=dev" -o $d/forge-solo ./cmd/forge-solo-linux
rm -rf $d/web && cp -r web/dist $d/web
```

### Tests

The unit job of `test.yml` takes a few minutes. The 32-bit run, there because three of the Linux
downloads are 32-bit, needs an x86-64 machine.

```sh
export GOTOOLCHAIN=go1.27.2   # the Go CI runs them with
gofmt -l .        # lists nothing
go vet ./...
go install honnef.co/go/tools/cmd/staticcheck@v0.7.0-0.dev.0.20261009230814-452d5bb86b45 && "$(go env GOPATH)/bin/staticcheck" ./...
go test -count=1 ./...
CGO_ENABLED=0 GOARCH=386 go test -count=1 ./...
go test -count=1 -race ./internal/stratum/ ./cmd/stratum/ ./cmd/api/ ./internal/mining/ ./internal/stats/ ./internal/dblock/ ./internal/pgmigrate/ ./internal/migstatus/ ./cmd/forge-solo-migrate/
```

The two Windows tray apps are modules of their own, which `./...` leaves out: CI runs
`go test -race -count=1 ./...` in `windows/launcher` and in `windows/gateway/launcher`. The rest of
its Windows job (vet, staticcheck and builds with `GOOS=windows`, both installer scripts compiled
with placeholders) and the job that runs on Windows itself are in `test.yml`.

The integration job runs two scripts, each of which fails when a test it runs is skipped:

```sh
./scripts/it-pg-to-sqlite.sh
./scripts/it-1175.sh
```

`it-pg-to-sqlite.sh` makes a 1.0.12 database with 1.0.12's own code (the `v1.0.12` tag, which a
full clone has), moves it into `forgesolo.db` with the migrate, api and stratum images it builds
from this tree and the compose as umbrelOS runs it, and checks every row; then going back to
1.0.12 and forward again, a move cut short or refused, and the Windows shape. It needs Docker with
compose and buildx, Go, python3 and curl, takes about 12 minutes on a CI runner, and removes its
containers and work folder; the `forge-it-*` images it builds stay, for `IT_REUSE_IMAGES=1`. Its
first lines list its options, such as `IT_CASES` and `KEEP=1`. `it-1175.sh` downloads the
1175 node (x86_64 or aarch64), checks its SHA-256, and merge-mines a block on a regtest chain.

## Releasing

1. Write the version's section in [RELEASE_NOTES.md](RELEASE_NOTES.md), which covers every
   platform, and in `umbrel-app.yml` the `releaseNotes` (Umbrel's update screen) and `version`.
   Commit, tag `v<version>`, and push `main`, then the tag.
2. The tag runs two workflows. `docker-build.yml` builds the six Umbrel images and commits their
   digests to `main`; `packaging_test` fails until it has, by design. `release.yml` builds the
   Windows installer, signs it in the `release` environment and creates the release page as a
   draft, with the installer on it. Only people with write access to the repository see a draft.
3. Build the Linux downloads and add them to that draft:
   `scripts/linux/build-release.sh <version> && gh release upload v<version> dist/forge-solo-<version>-linux-*.tar.gz dist/SHA256SUMS-linux`
   Build them on a clean checkout of the tag: the script refuses a tree with uncommitted or
   untracked changes, any other commit, or a version that `umbrel-app.yml` does not give.
4. Pull `main`, so you have CI's re-pinned digests (the commit "release: pin docker-compose to the
   <version> digests CI published"). Then check that every image it pins pulls without a login:
   `scripts/check-images-public.sh`. The Tests run that the re-pin starts on `main` runs the same
   check, as the job "Every pinned image pulls without a login". A package GHCR creates for the
   first time starts out private (in 1.0.13: `forge-solo-migrate`), and then every install and
   update fails. Make it public in the package's settings on GitHub, then run the check again until
   it passes. Run before the pull, the check fails and says to pull: the release commit pins zeros
   for an image new in the release.
5. Copy `umbrel-app.yml`, `docker-compose.yml` and `exports.sh` from that `main` into
   `bch2-apps-forge-solo/` of
   [BitcoincashII/umbrel-app-store](https://github.com/BitcoincashII/umbrel-app-store), and re-run
   the Tests workflow on `main`: its store check passes once the store matches.
6. Publish the release page: `gh release edit v<version> --draft=false`. It tells Umbrel users to
   update from the store and names the Linux files, so it goes public only once both are there.

## License and credits

MIT. See [LICENSE](LICENSE). TIDES mode follows DATUM and TIDES, both designed by
[OCEAN](https://ocean.xyz); the credits and notices at the end of LICENSE say what that means.
This project is not affiliated with or endorsed by OCEAN.
