# Forge Solo (Umbrel)

Solo-mine **BCH2** at home and **merge-mine 1175 (ESF)** at no extra hashrate cost.
Built on the hardened Forge Pool engine, packaged for a single household — **solo only,
no PPLNS, no pool fee**.

## What's inside
- `node` — BCH2 full node (pruned, auto-syncs)
- `node1175` — 1175 (ESF) node for AuxPoW merge-mining (fetched + SHA256-verified)
- `stratum` — solo stratum (port 3333), merge-mining enabled
- `api` + `web` — solo dashboard (hashrate, effort, your blocks, payouts)
- `postgres`

## Connect a miner
Set your **BCH2 payout address** (`POOL_ADDRESS`), **1175 payout address**, and
**min payout** in the Umbrel app config. Then point your miner at
`stratum+tcp://<your-umbrel-ip>:3333` — the worker name is just a label. Every
block's reward is paid to your configured BCH2 address.

## Security (vs the old app)
- Every secret (node RPC ×2, DB, internal token) is **generated per-install** (`exports.sh`) — nothing hardcoded.
- Node RPC ports are **not published to the host**; reachable only on the private app network and pinned via `-rpcallowip=10.21.21.0/24`.
- Internal API fails **closed** without its token.
- 1175 node binary is **checksum-verified**; images are version-pinned.
- Share work is credited as `min(assigned, proven)` — no credit inflation.
