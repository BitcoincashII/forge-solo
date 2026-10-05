# Forge Solo

Mine **BCH2** at home on your own full node: **solo**, where a block you find pays you in full,
or **TIDES**, where blocks are shared through Forge Pool's TIDES window. In solo mode on Umbrel
and Windows it also **merge-mines 1175 (ESF)** at no extra hashrate cost. Built on the hardened
Forge Pool engine, packaged for a single household: **no PPLNS, no pool fee**.

It runs on **Umbrel**, **Windows** and **Linux**, all built from this repository.

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
2. On Windows 11, check Smart App Control first: **Windows Security → App & browser control →
   Smart App Control settings**. Forge Solo is not yet signed by a certificate Windows trusts, so
   while Smart App Control is **On**, Windows blocks it. To use Forge Solo, choose **Off**. On a
   Windows 11 with its current updates you can turn it back on later in the same place; on an older
   one, turning it off lasts until Windows is reset. **Evaluation** does not block Forge Solo, but
   Windows may switch it to On later. Windows 10 has no Smart App Control.
3. Run the installer. When SmartScreen says "Windows protected your PC", choose **More info**, then
   **Run anyway**.
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
Going back to 1.0.12 and forward again keeps what both versions recorded.

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

There is no minimum payout and no payout schedule. A block you find pays you **directly in
that block's coinbase**: the reward is yours on-chain as soon as the block is accepted,
spendable after the usual 100-block coinbase maturity.

The worker username is **just a label**: `rig1`, `bitaxe`, anything. It has no payout
role: every block's reward is paid to your configured BCH2 address. Supplying
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

| Port | For | Starting difficulty |
|------|-----|--------------------|
| **3333** | Your own hardware, **and Braiins** | 1024, vardiffs up per miner |
| **3335** | **NiceHash / MiningRigRentals** | 500,000 |

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

**Difficulty.** The stratum sizes each connection to its own hashrate. A large connection
opens at the 1024 floor, and the first vardiff adjustment (within its first ten shares,
typically well under a second) moves it straight to the difficulty its
measured rate warrants rather than climbing in +50% steps. Setting `d=<difficulty>` in the
password skips even that: the connection opens exactly there. The hint is a starting
point, not a lock: vardiff still tracks the connection afterwards, so a hint that turns
out to be wrong corrects itself. It is clamped to the same floor and maximum as every
other path, so `d=1` cannot flood the miner and an absurd value cannot park a connection
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
5. Copy `umbrel-app.yml`, `docker-compose.yml`, `exports.sh` and `init-db.sql` from that `main`
   into `bch2-apps-forge-solo/` of
   [BitcoincashII/umbrel-app-store](https://github.com/BitcoincashII/umbrel-app-store), and re-run
   the Tests workflow on `main`: its store check passes once the store matches.
6. Publish the release page: `gh release edit v<version> --draft=false`. It tells Umbrel users to
   update from the store and names the Linux files, so it goes public only once both are there.

## License and credits

MIT. See [LICENSE](LICENSE). TIDES mode follows DATUM and TIDES, both designed by
[OCEAN](https://ocean.xyz); the credits and notices at the end of LICENSE say what that means.
This project is not affiliated with or endorsed by OCEAN.
