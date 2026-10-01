# Forge Solo for Linux: release notes

Each section is the release page text for that version: `scripts/linux/build-release.sh VERSION`
builds the downloads, and the section headed `## VERSION` describes them.

## 1.0.12

The first release of **Forge Solo for Linux**: BCH2 solo and TIDES mining on your own full node,
from one directory, on any Linux distribution and on every common CPU.

It runs a Bitcoin Cash II node, the mining service your miners connect to, and the dashboard.
In Settings you choose how blocks pay:

- **Solo:** every block your miners find pays your BCH2 address in full, straight from the
  block's coinbase.
- **TIDES pool:** your node still builds every block, and each block found by a Forge Solo in
  TIDES mode or by a Forge Gateway pays everyone with work in Forge Pool's TIDES window, straight
  from its coinbase. You are paid from every such block found while you have work in the window.
  There is no pool wallet and no fee, and while Forge Pool cannot be reached it mines solo.

It is BCH2 only. 1175 (ESF) merge-mining needs a 1175 node, which this version does not include:
the Umbrel app and the Windows version have it.

### Downloads

Pick the file for what `uname -m` prints on your machine:

| `uname -m` | Download | Hardware |
|---|---|---|
| `x86_64` | `forge-solo-1.0.12-linux-x86_64.tar.gz` | 64-bit PCs and servers (Intel, AMD) |
| `aarch64` | `forge-solo-1.0.12-linux-aarch64.tar.gz` | 64-bit ARM: Raspberry Pi 3, 4, 5 and Zero 2 W with a 64-bit OS, other ARM boards and servers |
| `armv7l` | `forge-solo-1.0.12-linux-armv7l.tar.gz` | 32-bit ARM: Raspberry Pi 2, 3, 4 and Zero 2 W with a 32-bit OS |
| `armv6l` | `forge-solo-1.0.12-linux-armv6l.tar.gz` | Raspberry Pi 1, Zero and Zero W |
| `i686` | `forge-solo-1.0.12-linux-i686.tar.gz` | 32-bit PCs with SSE2: Pentium 4, Pentium M, Atom and newer |
| `riscv64` | `forge-solo-1.0.12-linux-riscv64.tar.gz` | 64-bit RISC-V boards (RV64GC) |

Every program in them is fully static: no libraries and no packages are needed, on any
distribution with Linux 3.2 or newer, glibc or musl. Check your download with
`sha256sum -c SHA256SUMS-linux --ignore-missing`.

### Start

```sh
tar xzf forge-solo-1.0.12-linux-x86_64.tar.gz
cd forge-solo-1.0.12-linux-x86_64
./forge-solo
```

Open http://127.0.0.1:3080, set your BCH2 payout address in Settings, and point your miners at
`stratum+tcp://THIS-MACHINE:3333` (NiceHash and MiningRigRentals: 3335). The node syncs the
chain first: about a quarter of an hour on a PC, longer on a small board. To run it as a service
that starts at boot:
`sudo ./forge-solo install-service`. The README.md in the download covers the rest: the
dashboard from another computer, ports and firewalls, upgrading, and stopping cleanly.

### Good to know

- The dashboard listens on 127.0.0.1 only, because anyone who can open it can change your payout
  address. Reach it from another computer through an SSH tunnel, or serve it with `--web` behind
  the password it then asks for.
- To take rented hashrate from the internet, forward TCP 3335 (NiceHash, MiningRigRentals) or
  3333 (Braiins) in your router; to let other nodes connect to yours, forward 8339.
- Stop it with Ctrl-C, or `sudo systemctl stop forge-solo` for the service. The node writes its
  chain state to disk first, which can take up to a few minutes.

### What is in it

- Bitcoin Cash II node v27.0.2, built fully static from the public `bitcoincashII-core` tag
  `v27.0.2` (commit `a1668c6`). The node binaries published with v27.0.2 need a recent glibc
  and libstdc++, and do not start on Debian 12 or Ubuntu 22.04.
- The Forge Solo mining service and dashboard: the same code as the Umbrel app 1.0.12, with a
  SQLite database in place of PostgreSQL.

DATUM and TIDES were designed by OCEAN; Forge Solo's TIDES mode is an independent implementation
for Bitcoin Cash II, not affiliated with or endorsed by OCEAN. See LICENSE.
