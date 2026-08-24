# Forge Solo (Umbrel)

Solo-mine **BCH2** at home and **merge-mine 1175 (ESF)** at no extra hashrate cost.
Built on the hardened Forge Pool engine, packaged for a single household — **solo only,
no PPLNS, no pool fee**.

## Install on Umbrel

1. In Umbrel: **Settings → App Store → Community App Stores → Add**, and paste:
   `https://github.com/BitcoincashII/umbrel-app-store`
   ⚠️ Add the **app-store** repo — **not** `bitcoincashII-core` (that is the node/wallet source code, not an app store; Umbrel will fail to load it).
2. Open **BCH2 Community Apps** and install **Forge Solo**.
3. Set your BCH2 payout address on the app's **Settings** page, then point your miner at `stratum+tcp://<your-umbrel-ip>:3333`. The worker username can be any label.

## What's inside
- `node` — BCH2 full node (pruned, auto-syncs)
- `node1175` — 1175 (ESF) node for AuxPoW merge-mining (fetched + SHA256-verified)
- `stratum` — solo stratum (port 3333), merge-mining enabled
- `api` + `web` — solo dashboard (hashrate, effort, your blocks, payouts)
- `postgres`

## Connect a miner
Set your **BCH2 payout address** and **1175 payout address** on the app's **Settings**
page (they are stored in the app's database, so they survive updates). Then point your
miner at `stratum+tcp://<your-umbrel-ip>:3333`.

There is no minimum payout and no payout schedule. A block you find pays you **directly in
that block's coinbase** — the reward is yours on-chain as soon as the block is accepted,
spendable after the usual 100-block coinbase maturity.

The worker username is **just a label** — `rig1`, `bitaxe`, anything. It has no payout
role: every block's reward is paid to your configured BCH2 address. Supplying
`<your-address>.<label>` also works.

## Rented hashpower (Braiins and other marketplaces)

There are two stratum ports, and which one you use depends on how the marketplace connects:

| Port | For | Starting difficulty |
|------|-----|--------------------|
| **3333** | Your own hardware, **and Braiins** | 1024, vardiffs up per miner |
| **3335** | **NiceHash / MiningRigRentals** | 500,000 |

Braiins belongs on 3333 because it connects **each miner individually** rather than proxying
them onto one connection — so every connection is one miner's hashrate and needs to find its
own level from a low floor. NiceHash and MRR aggregate many miners behind a single
connection, so that connection carries the whole order's hashrate and starts high.

Both ports use the 8-byte extranonce2 every marketplace requires.

    URL:      stratum+tcp://<your-umbrel-ip>:3333   (Braiins)
              stratum+tcp://<your-umbrel-ip>:3335   (NiceHash / MiningRigRentals)
    Worker:   any label (e.g. rented-01), or <your-bitcoincashii-address>.<label>
    Password: d=2000000        # optional — see below

Point the order at a host the marketplace can actually reach: an Umbrel on your LAN is
not reachable from the internet without a port forward or a tunnel.

**Difficulty.** The stratum sizes each connection to its own hashrate. A large connection
opens at the 1024 floor, and the first vardiff adjustment — which lands within its first
ten shares, typically well under a second — moves it straight to the difficulty its
measured rate warrants rather than climbing in +50% steps. Setting `d=<difficulty>` in the
password skips even that: the connection opens exactly there. The hint is a starting
point, not a lock — vardiff still tracks the connection afterwards, so a hint that turns
out to be wrong corrects itself. It is clamped to the same floor and maximum as every
other path, so `d=1` cannot flood the pool and an absurd value cannot park a connection
where it never submits.

**Payout.** Rented hashpower mines **solo to your address**, exactly like your own
hardware — the reward of any block it finds goes to your configured BCH2 payout address,
and the worker label is only a label.

## Security (vs the old app)
- Every secret (node RPC ×2, DB, internal token) is **generated per-install** (`exports.sh`) — nothing hardcoded.
- Node RPC ports are **not published to the host** — reachable only on the app's private Docker network.
- Internal API fails **closed** without its token.
- 1175 node binary is **checksum-verified**; images are version-pinned.
- Share work is credited as `min(assigned, proven)` — no credit inflation.
