# Forge Gateway: release notes

The release workflow (`gateway-v*` tags) puts the section for the version it builds on the release
page.

## 1.0.0

The first release of **Forge Gateway**: mine into Forge Pool's TIDES window from your own BCH2
node, without Forge Solo.

Your node builds every block template, your miners connect to the gateway, and Forge Pool
registers each job and counts your miners' shares. Every block found by a Forge Gateway or by a
Forge Solo in TIDES mode pays everyone with work in the TIDES window, straight from its coinbase:
no pool fee, no pool balance, no minimum payout. It is the same gateway as Forge Solo's TIDES mode,
on its own, for people who run their own node and their own mining setup.

- **Downloads:** Linux (x86_64 and arm64, with a systemd unit) and Windows (x86_64;
  `forge-gateway.exe install` sets it up as a Windows service). Each holds the program, its
  README, an example config and the license. Check them against `SHA256SUMS`.
- **What you need:** a fully synced Bitcoin Cash II node with RPC enabled, a BCH2 payout address,
  and miners that speak stratum V1.
- **Your miners' usernames:** a username that is a BCH2 address is credited to that address; any
  other username is credited to the gateway's payout address, as a worker name. So one gateway can
  serve several people.
- **Your miners' work counts in full:** each job commits to a share difficulty above your busiest
  miner's (the power of two at or above twice it), and the shares that reach it are credited at
  that difficulty, so their work counts in full on average.
- **When Forge Pool cannot be reached,** the gateway mines solo on your node (a block found then
  pays your payout address in full) and goes back to the pool by itself. With `pool_only` set, it
  turns miners away instead, so they fail over to their backup pool.
- **Status page** at http://127.0.0.1:7152 (and `/api/status`): the pool connection, your node,
  what a block found now would pay you, each worker, and blocks found.
- `forge-gateway -check` checks the config, logs in to your node and asks Forge Pool for its
  window before you leave it running.

DATUM and TIDES were designed by OCEAN; Forge Gateway is an independent implementation for
Bitcoin Cash II, not affiliated with or endorsed by OCEAN. See LICENSE.
