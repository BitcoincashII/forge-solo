# Forge Gateway

Mine into **Forge Pool's TIDES window from your own BCH2 node**, without Forge Solo.

Your node builds every block template. Your miners connect to Forge Gateway. Forge Pool registers
each job and counts the shares your miners find, and every block found through the pool's DATUM
gateways pays its TIDES split **straight from the coinbase**: no pool fee, no pool balance, no
minimum payout.
If Forge Pool cannot be reached, the gateway keeps your miners busy mining solo on your node (or,
with `pool_only`, sends them to their backup pool) and rejoins by itself when the pool is back.

Forge Solo's TIDES mode is the same gateway, built in. Use Forge Gateway when you run your own
node and your own mining setup instead.

It is a DATUM-style gateway. DATUM and TIDES were designed by [OCEAN](https://ocean.xyz); see
[Credits](#credits).

## What you need

- A fully synced **Bitcoin Cash II node** on mainnet with RPC enabled.
- A **BCH2 address** for your payouts (`bitcoincashii:q…`).
- Miners that speak stratum V1 (any SHA-256 ASIC, Bitaxe, NerdQAxe, a rental proxy…).

In your node's config file, enable RPC for the gateway (use your own long random password):

```
server=1
rpcuser=forgegateway
rpcpassword=CHANGE-THIS-TO-A-LONG-RANDOM-PASSWORD
rpcbind=127.0.0.1
rpcallowip=127.0.0.1
```

Restart the node after changing it. The gateway can use the node's `.cookie` file instead
(`rpc_cookie_file`), but a node writes a new cookie every time it restarts, so the gateway would
need restarting too; a user and password do not have that problem.

## Set up

1. Copy `forge-gateway.example.json` to `forge-gateway.json` and fill in at least
   `node.rpc_user`, `node.rpc_password` and `mining.payout_address`.
2. Check everything before you leave it running:

   ```
   forge-gateway -check -config forge-gateway.json
   ```

   It checks the config, logs in to your node, and asks Forge Pool for its TIDES window.
3. Run it (`forge-gateway -config forge-gateway.json`), or install it as a service, below.
4. Point your miners at `stratum+tcp://<this machine's LAN address>:3333`.

### Linux: run as a service

```
sudo useradd --system --home /var/lib/forge-gateway --shell /usr/sbin/nologin forge-gateway
sudo install -m 755 forge-gateway /usr/local/bin/
sudo install -d -m 750 -o root -g forge-gateway /etc/forge-gateway
sudo install -m 640 -o root -g forge-gateway forge-gateway.json /etc/forge-gateway/
sudo install -m 644 forge-gateway.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now forge-gateway
journalctl -u forge-gateway -f
```

With the service, set `"key_file": "/var/lib/forge-gateway/forge-gateway.key"` in the config:
the service can write only to its state directory.

### Windows: run as a service

Put `forge-gateway.exe` and `forge-gateway.json` in a folder of their own, for example
`C:\ForgeGateway`. From an **Administrator** Command Prompt:

```
cd C:\ForgeGateway
forge-gateway.exe -check -config C:\ForgeGateway\forge-gateway.json
forge-gateway.exe install -config C:\ForgeGateway\forge-gateway.json
```

The service starts now and with Windows, and restarts itself if it stops. Its log is
`C:\ForgeGateway\forge-gateway.log` (or the `log_file` you set). To remove it:
`forge-gateway.exe uninstall`. You can also just run `forge-gateway.exe -config forge-gateway.json`
in a Command Prompt window.

Allow the stratum port (3333) through Windows Firewall for your local network if your miners are
on other machines.

## Your miners

| Setting | Value |
|---|---|
| Pool URL | `stratum+tcp://<gateway machine>:3333` |
| Username | your BCH2 address, optionally with `.workername` — or just a worker name |
| Password | anything (`x`) |

A username that is a BCH2 address is credited to **that address** at the pool; any other username
is credited to the gateway's `payout_address`, with the username as the worker name. So one
gateway can serve several people, each paid to their own address.

## Status page

Open `http://127.0.0.1:7152/` on the gateway machine: the pool connection, your node, what a block
found now would pay you, each worker's hashrate, and blocks found. The same data is at
`/api/status` as JSON. It has no login, so it listens on this machine only; set `status.listen`
to `0.0.0.0:7152` only on a network you trust.

To start on a new block the moment your node has it (instead of within a second), add to the
node's config: `blocknotify=curl -s -X POST http://127.0.0.1:7152/notify`

## How you are paid

Forge Pool's TIDES window is the most recent shares from every DATUM gateway — Forge Gateways
and Forge Solo installs in TIDES mode. Every block any of them finds, yours included, pays each
address in the window its share of the reward, directly in that block's coinbase. (Blocks found
by the pool's own stratum miners are paid by the pool's usual payouts, not this window.) The payout reaches your address when the block is
mined, and can be spent after the usual 100-block coinbase maturity.

While Forge Pool cannot be reached, the gateway mines **solo**: a block found then pays your
`payout_address` the whole reward. It tries the pool again every minute and moves your miners
back as soon as the pool answers. With `"pool_only": true` it turns miners away instead, so they
fail over to their backup pool — set that on a gateway that serves other people's addresses,
because a solo block pays only `payout_address`.

## Config reference

Every key is optional except `node.rpc_user`/`rpc_password` (or `rpc_cookie_file`) and
`mining.payout_address`. `forge-gateway.example.json` lists every key with its default. A
misspelt key is an error, not silently ignored. Relative paths are relative to the config file.

| Key | Default | Meaning |
|---|---|---|
| `node.rpc_url` | `http://127.0.0.1:8342` | your node's RPC address |
| `node.rpc_user`, `node.rpc_password` | — | RPC login |
| `node.rpc_cookie_file` | — | or read the login from the node's `.cookie` |
| `mining.payout_address` | — | credited for worker-name logins; paid solo blocks |
| `mining.coinbase_tag` | `Forge Gateway` | text in your blocks' coinbase, up to 32 characters |
| `mining.pool_only` | `false` | turn miners away instead of mining solo while the pool is down |
| `stratum.listen` | `0.0.0.0:3333` | where miners connect |
| `stratum.min_difficulty` | `1024` | lowest share difficulty a miner is given |
| `stratum.max_difficulty` | `1e12` | highest |
| `stratum.target_share_seconds` | `5` | vardiff aims for a share this often per miner |
| `stratum.retarget_seconds` | `10` | how often vardiff may adjust |
| `stratum.max_connections` | `256` | connections in total |
| `stratum.max_connections_per_ip` | `128` | connections from one address |
| `pool.url` | `https://pool.bch2.org` | Forge Pool |
| `pool.key_file` | `forge-gateway.key` | this gateway's identity at the pool, created on first start — keep it |
| `status.listen` | `127.0.0.1:7152` | status page; `off` disables it |
| `log_file` | console (a Windows service: `forge-gateway.log`) | where the log goes |
| `log_level` | `info` | `debug`, `info`, `warn` or `error` |

## Building from source

Forge Gateway lives in the Forge Solo repository and needs Go (the version in `go.mod`):

```
go build -trimpath -o forge-gateway ./cmd/forge-gateway
GOOS=windows GOARCH=amd64 go build -trimpath -o forge-gateway.exe ./cmd/forge-gateway
```

## Credits

DATUM (Decentralized Alternative Templates for Universal Mining) and TIDES were designed and
built by OCEAN: <https://ocean.xyz/docs/datum>, <https://ocean.xyz/docs/tides>, and the original
[DATUM Gateway](https://github.com/OCEAN-xyz/datum_gateway) (Copyright (c) 2024-2025 Bitcoin
Ocean, LLC, Jason Hughes, and individual contributors; MIT License). Forge Gateway follows their
design — your node builds the template, and the coinbase pays the pool's miners directly — as an
independent implementation for Bitcoin Cash II and Forge Pool: it contains no DATUM Gateway code
and does not speak OCEAN's DATUM Protocol. This project is not affiliated with or endorsed by
OCEAN. See the credits and notices in [LICENSE](../../LICENSE).

MIT License — see [LICENSE](../../LICENSE).
