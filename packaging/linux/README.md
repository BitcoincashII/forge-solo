# Forge Solo for Linux

Solo and TIDES mining for Bitcoin Cash II (BCH2) on your own full node. Forge Solo runs a BCH2
node, a mining service your miners connect to, and a dashboard, all from this one directory.

- **Solo:** every block your miners find pays your BCH2 address in full, straight from the
  block's coinbase.
- **TIDES pool:** your node still builds every block, and each block found through Forge Pool's
  gateways pays everyone with work in Forge Pool's TIDES window, straight from its coinbase. You
  are paid from every such block found while you have work in the window. There is no pool wallet
  and no pool fee. The dashboard's TIDES page (`/tides`) explains it in full.

This version is BCH2 only. 1175 (ESF) merge-mining is not part of it; the Umbrel app and the
Windows version have it.

## What it runs on

The programs are fully static: they need no libraries and no packages, so they run on any Linux
distribution with kernel 3.2 or newer, glibc or musl, old or new. Pick the download that matches
what `uname -m` prints:

| `uname -m` | Download | Hardware |
|---|---|---|
| `x86_64` | `forge-solo-VERSION-linux-x86_64.tar.gz` | 64-bit PCs and servers (Intel, AMD) |
| `aarch64` | `forge-solo-VERSION-linux-aarch64.tar.gz` | 64-bit ARM: Raspberry Pi 3, 4 and 5 and Zero 2 W with a 64-bit OS, other ARM boards and servers |
| `armv7l` | `forge-solo-VERSION-linux-armv7l.tar.gz` | 32-bit ARM: Raspberry Pi 2, 3, 4 and Zero 2 W with a 32-bit OS |
| `armv6l` | `forge-solo-VERSION-linux-armv6l.tar.gz` | Raspberry Pi 1, Zero and Zero W (it also runs on any 32-bit ARM board) |
| `i686` | `forge-solo-VERSION-linux-i686.tar.gz` | 32-bit PCs with SSE2: Pentium 4, Pentium M, Atom and newer |
| `riscv64` | `forge-solo-VERSION-linux-riscv64.tar.gz` | 64-bit RISC-V boards (RV64GC) |

About 1 GB of disk is plenty (the whole BCH2 chain is about 110 MB today), and it uses about
150 MB of memory.

## Start it

```sh
sha256sum -c SHA256SUMS-linux --ignore-missing      # check the download (SHA256SUMS-linux beside it)
tar xzf forge-solo-VERSION-linux-x86_64.tar.gz
cd forge-solo-VERSION-linux-x86_64
./forge-solo
```

It runs in the foreground; Ctrl-C stops it (see **Stopping**). Then:

1. Open **http://127.0.0.1:3080** in a browser on this machine (from another computer, see
   **The dashboard from another computer**).
2. In **Settings**, enter your BCH2 payout address (`bitcoincashii:q…`), choose **Solo** or
   **TIDES pool**, and press **Save settings**. Mining waits for a payout address. Saving asks
   for Forge Solo's password, `DASHBOARD_PASSWORD` in `secrets.env` in the data directory: other
   accounts on this machine can reach the dashboard too. The browser remembers it after that.
3. Point your miners at **stratum+tcp://THIS-MACHINE:3333**. The worker name is only a label for
   the dashboard, and the password can be anything (`x`). Every block pays the payout address in
   Settings, whatever the worker name. NiceHash and MiningRigRentals, which put a whole order
   behind one connection, use port **3335**.

The node syncs the chain first, about a quarter of an hour on a PC and longer on a small board;
the dashboard shows its progress, and mining starts once it is done. Everything is kept in the data directory:
`~/.local/share/forge-solo`, or `/var/lib/forge-solo` when run as root or as the service.

## Run it as a service (systemd)

```sh
sudo ./forge-solo install-service
```

This copies the program to `/opt/forge-solo`, creates a `forge-solo` system user, keeps the data
in `/var/lib/forge-solo`, and starts the `forge-solo` service now and at every boot.

- Logs: `journalctl -u forge-solo -f`, and `/var/lib/forge-solo/logs/`
- Stop and start: `sudo systemctl stop forge-solo`, `sudo systemctl start forge-solo`
- Upgrade: unpack the new release and run `sudo ./forge-solo install-service` from it. The data
  and your settings are kept.
- Remove: `sudo /opt/forge-solo/forge-solo uninstall-service`. It stops and removes the service
  and says how to delete the program and the data if you want to.

A data directory used before with `sudo ./forge-solo` (`/var/lib/forge-solo`) is taken over by
the service as it is.

**Without systemd** (Alpine, Void, Devuan, Slackware and others), create a data directory owned
by an unprivileged user, run `forge-solo run --data-dir DIR` as that user from your init system,
and stop it with SIGTERM, allowing up to 3 minutes.

## The dashboard from another computer

The dashboard listens on 127.0.0.1 only, because anyone who can open it can change your payout
address. To open it from another computer, use an SSH tunnel:

```sh
ssh -L 3080:127.0.0.1:3080 you@this-machine
```

then open http://127.0.0.1:3080 there. Opened this way, or on this machine, the dashboard shows
miners this machine's own network address to connect to.

To serve it to your network directly instead, give it an address:
`./forge-solo --web 0.0.0.0:3080` (or `sudo ./forge-solo install-service --web 0.0.0.0:3080`).
It then asks for a password: the user is `forge`, and the password is `DASHBOARD_PASSWORD` in
`secrets.env` in the data directory. It is plain HTTP, so do this only on a network you trust.

## Ports

| Port | For | Listens on |
|---|---|---|
| 3333 | your miners, and Braiins rentals | all interfaces |
| 3335 | NiceHash and MiningRigRentals orders | all interfaces |
| 8339 | BCH2 peers | all interfaces |
| 3080 | the dashboard | 127.0.0.1 (see above) |

Your own miners on your network need no forwarding. To take rented hashrate from the internet,
forward TCP 3335 (NiceHash, MiningRigRentals) or 3333 (Braiins) in your router to this machine;
to let other BCH2 nodes connect to yours, forward 8339. The node's RPC and block notifications,
the dashboard's API and the mining service's internal port listen on 127.0.0.1 only, on free
ports chosen at each start.

If a host firewall blocks incoming connections, open the ports your miners and peers use:

```sh
sudo firewall-cmd --permanent --add-port=3333/tcp --add-port=3335/tcp --add-port=8339/tcp && sudo firewall-cmd --reload   # firewalld
sudo ufw allow 3333/tcp && sudo ufw allow 3335/tcp && sudo ufw allow 8339/tcp                                              # ufw
```

## Stopping

Ctrl-C (or SIGTERM) stops it cleanly: the mining service disconnects its miners and hands Forge
Pool any TIDES shares it still holds, then the dashboard's API stops, then the node writes its
chain state to disk. This takes a few seconds, and at most about 3 minutes. A second Ctrl-C stops
everything at once, and the node may then take longer to start next time.

## Other commands

```sh
./forge-solo help                  # all commands and options
./forge-solo version
./forge-solo cli getblockchaininfo # a bitcoincashII-cli command against the running node
```

As the service, run `cli` as root or as the `forge-solo` user:
`sudo /opt/forge-solo/forge-solo cli getblockchaininfo`.

## Files

In the data directory:

| Path | What |
|---|---|
| `bch2/` | the node: the chain, `bch2.conf` (written at every start), `debug.log` |
| `forgesolo.db` | the database: your settings and your blocks |
| `config.yaml` | the mining service's configuration (written at every start) |
| `secrets.env` | the node's RPC password, an internal token and the dashboard password (which Settings asks for) |
| `logs/` | the mining service's and the API's logs (`stratum.log`, `api.log`) |

## If something goes wrong

- **"… is already in use by another program"**: another program has port 3333, 3335 or 8339,
  usually another BCH2 node or mining pool on this machine. Stop it first.
- **"another Forge Solo is already running with the data directory …"**: another copy is running with the
  same data, perhaps the service. Stop that one first.
- The dashboard shows why mining is paused. The logs are in `logs/` in the data directory, the
  node's in `bch2/debug.log`. When a program stops unexpectedly, Forge Solo prints its last lines
  and starts it again.

## About this build

The BCH2 node is Bitcoin Cash II v27.0.2, built fully static from the public
`bitcoincashII-core` tag `v27.0.2` (`bin/COPYING-bitcoincashII-core` is its license). The mining
service and the API are the same code as the Umbrel app's, with a SQLite database in place of
PostgreSQL.

## Credits

DATUM and TIDES were designed and built by OCEAN (https://ocean.xyz/docs/datum,
https://ocean.xyz/docs/tides). Forge Solo's TIDES mode follows their design as an independent
implementation for Bitcoin Cash II. It does not speak OCEAN's DATUM Protocol, and this project is
not affiliated with or endorsed by OCEAN. See LICENSE.
