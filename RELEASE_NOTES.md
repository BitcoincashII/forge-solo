# Forge Solo for Windows: release notes

The release workflow puts the section for the version it builds at the top of the release page.

## 1.0.12

**Other BCH2 nodes can now connect in to yours.** The node could not accept an inbound IPv4
connection, however your router and firewall were set up: its Tor listener uses port 8339 too and
took it first, so the node's own IPv4 listener on 8339 never started. The node now binds the port
itself, and the Tor listener moves to 8340. It also runs as a full node instead of a pruned one.
Pruning saved no space, since the whole BCH2 chain is about 90 MB, but the DNS seeders that
introduce nodes to each other never list a pruned node. Your data is kept as it is, and nothing is
downloaded again. With TCP 8339 forwarded in your router, expect the first inbound peer within a
few hours: other nodes have to find yours first.

**New: TIDES mode.** In Settings, under *How blocks pay*, choose **Solo** (as before: a block you
find pays you in full) or **TIDES pool**. In TIDES mode your node still builds every block, but
each block found by a Forge Solo in TIDES mode or by a Forge Gateway pays everyone with work in
Forge Pool's TIDES window, straight from its coinbase, and you are paid from every such block
found while you have work in the window. There is no pool wallet and no fee. 1175 merge-mining is
off in TIDES mode, and while Forge Pool cannot be reached the app mines solo. The new TIDES page
in the app explains it in full, and the dashboard shows your share of the window and what the next
pool block pays you.

**Quit and Restart Mining stop the mining service cleanly.** They used to kill it outright, so
its miners were cut off and, in TIDES mode, the shares it still held never reached the pool. It is
now asked to stop, which takes a few seconds, and killed only if it has not stopped within 30
seconds. A block found in its last moments is still submitted.

**Your miners are told this PC's address.** The dashboard and Settings showed
`stratum+tcp://127.0.0.1:3333`, which only a miner on this same PC can reach. They now show this
PC's network address. Settings also no longer sends NiceHash and MiningRigRentals to port 3335,
which this version does not open: they use 3333, like your own miners.

**Mining fixes.**
- A share is judged by the difficulty its job went out with, not by a newer one the miner had not
  received yet, so a miner on a weak link has far fewer refused shares.
- Every miner is credited to your payout address, as Settings says, whatever its username, and a
  new payout address applies to the miners already connected. A rental whose username was a BCH2
  address used to show 0 H/s on the dashboard.
- One share can no longer be counted twice.
- A connection that does not speak stratum is closed at once. MiningRigRentals' pool check tries
  TLS first, was left waiting, and reported the pool as unable to authenticate. Its rig proxy is
  now recognised as MiningRigRentals.

**Settings are safer.** Another web page you have open can no longer change them, the dashboard's
API now listens on this PC only, and if the app's database is briefly unavailable, Settings says
so instead of showing your settings as empty.

Forge Solo is now MIT licensed, with credit to OCEAN, who designed DATUM and TIDES.
