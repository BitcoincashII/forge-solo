# Forge Solo: release notes

One section per version, headed `## <version>`, for every platform: the Umbrel app, Windows and
Linux. The release workflow puts the section at the top of that version's release page, and a
release without one fails rather than publishing an empty page. The Umbrel app's own update
screen shows `releaseNotes` from `umbrel-app.yml` instead, so write that too.

Up to 1.0.12 each platform kept its own notes: [windows/RELEASE_NOTES.md](windows/RELEASE_NOTES.md)
and [packaging/linux/RELEASE_NOTES.md](packaging/linux/RELEASE_NOTES.md).

## 1.0.13

**Found blocks, and what the dashboard says about them.**
- **Every platform:** a block found while the node is restarting or too busy to answer is kept
  trying for 30 minutes; it was given up after 6 seconds. One found while the database is down is
  recorded once the database is back, if the app keeps running meanwhile (for up to a day); it used
  to be missing from the dashboard for good. The block itself pays you either way.
- **Every platform:** changing your payout address takes effect at once. Miners used to finish the
  work they had, which paid the old address, and a block found on it was recorded under the new one.
- **Every platform:** an orphaned block is marked **Orphaned**, and left out of your totals, about 6
  blocks after it was found, instead of about 17 hours later; a block found before the machine was
  off for days is checked by its hash.
- **Every platform:** the totals stay right past 100 blocks (they stopped growing there), and the
  hashrate reads right a minute after a restart or an update (it started at a fifth of the real
  figure). A miner that is connected shows online between its shares, and a small one before its
  first. When the database or the node is restarting, the dashboard says so instead of showing 0.00
  and "No blocks found yet".
- **Every platform, TIDES:** Your Blocks Found counts the TIDES blocks your install found. It counted
  solo blocks only, so it read 0 in TIDES mode while the TIDES card listed blocks found by "You".

**Steadier difficulty.**
- **Every platform:** the difficulty each miner is given now stays near the level its hashrate calls
  for. Each change was measured partly from shares found before the last one, so it kept
  overshooting: a steady 5 PH/s rental swung between half and 3.4 times its level, a dozen changes in
  7 minutes. Shares were always credited at the difficulty they were found at; now they also arrive
  at a steady pace.

**Much smaller, and it stays small.**
- **Umbrel:** the database image was 1.8 GB, almost all of it tools this app never used. The new one
  is about 300 MB, so the app needs about 0.8 GB of disk instead of 2.3 GB. Your database is kept as
  it is. It is also sized for this app now, instead of for your whole Umbrel (earlier versions let it
  take up to a quarter of the machine's memory), and it no longer sends usage telemetry to Timescale.
- **Every platform:** every share your miners found was written to the database and never read, and
  nothing trimmed it until a block was found. That stops, and what was stored is cleared on the first
  start.
- **Umbrel:** the app's logs were never trimmed. They are now capped, and the routine lines that
  filled them (healthchecks, every share, every dashboard refresh) are gone. Umbrel's backups skip
  the nodes' chain data, which the nodes can download again.
- **Every platform:** both nodes cap their memory for a machine they share with other things: a
  100 MB database cache, a 50 MB mempool and a 4 MB signature cache. On Umbrel they also run without
  a wallet, which nothing here uses.

**Windows: starts on every PC, and stops cleanly.**
- **Windows:** the database now starts on a PC that has never had Microsoft's Visual C++ runtime, a
  fresh Windows install for example. There it never started, and the tray said only that it had
  failed. The runtime now comes with Forge Solo.
- **Windows:** Quit stops everything, also while Forge Solo is still starting, and the tray icon stays,
  showing the stop, until it is done. It used to leave both nodes and the database running after the
  tray icon was gone, and the next start then failed until the PC was restarted. A node still loading
  its blocks is asked again until it stops, instead of being ended.
- **Windows:** shutting down, restarting or signing out stops both nodes and the database cleanly
  first, in a few seconds. They used to be ended mid-run and had to recover on the next start. If
  the shutdown is cancelled after all, Forge Solo starts again; it used to stay closed.
- **Windows:** if Forge Solo is ended from Task Manager, or crashes, the next start stops what it
  left running and then starts as usual.
- **Windows:** opening Forge Solo while it runs opens its dashboard instead of starting a second
  copy, also when it runs for another Windows account, and updating or uninstalling asks you to close
  it first. If the tray icon cannot be added at sign-in, Forge Solo starts again once.
- **Windows:** if another program uses port 3080, the tray says the dashboard cannot open, instead of
  opening that program in the browser. Mining goes on. If one uses 3333 or 8339, which Forge Solo
  cannot mine without, it starts nothing and the tray names the port; it used to say "running". A
  program started later can no longer take the miner's port and its miners.
- **Windows:** the database starts for a Windows account whose name has characters outside the
  system's language (a Chinese name on an English Windows, say); it never started there.
- **Windows:** Forge Solo no longer replaces `secrets.env` when it cannot read it, and the file is
  written so that a power cut cannot leave it half written: new passwords would have locked Forge Solo
  out of its own database.
- **Windows:** a program that stops on its own (the miner, the API or a node crashing) is started
  again, and the tray says so. It stayed stopped, with the tray saying "running" and nothing
  mining, until someone restarted Forge Solo. A node whose chain data a power cut damaged is rebuilt
  from the blocks on disk; it used to stop at every start.
- **Windows:** Restart Mining no longer holds up the tray menu, and unticking "Start Forge Solo when I
  sign in" in an update turns that off.
- **Windows:** rented hashpower gets its own port, 3335, with the same difficulty settings as on
  Umbrel and Linux. The miner's and the API's logs are kept in the data folder.
- **Windows:** uninstalling keeps your data folder unless you choose to delete it (No is now the
  default). `launcher.log` in the data folder records what Forge Solo started and stopped.

**Safer.**
- **Umbrel:** Settings now asks for Forge Solo's password before it saves a change. Other apps on an
  Umbrel can reach Forge Solo directly, without going through umbrelOS's login, so a change needs the
  password umbrelOS shows for this app: when you open Forge Solo, and any time later by right-clicking
  its icon (**Settings** → **Default credentials**; on umbrelOS 1.x, **Show default credentials**).
  The page remembers it in your browser. Mining is not affected.
- **Umbrel:** other apps now reach only Forge Solo's dashboard. Its database, nodes and mining service
  talk to each other over a network of their own. The dashboard cannot be shown inside another
  site's page.
- **Every platform:** the mining ports are hardened. This matters most for the rental port, which is
  open to the internet once it is forwarded. Changes:
  - shares that are malformed, stale or sent again are refused before they cost memory;
  - a connection that never logs in is closed after a minute;
  - a quarter of the connection slots are kept for miners on your own network;
  - the rental port limits how fast shares arrive;
  - one stuck connection no longer delays new work for every other miner;
  - what a client can put in the log is limited, and worker names are kept to plain labels;
  - a share sent again under a later job that carries the same work counts once (in TIDES mode the
    pool refused the second copy as a duplicate).

  Rented hashpower is no longer disconnected for waiting quietly between jobs.
- **Every platform, TIDES:** a slow or misbehaving pool can no longer hold up new work for your miners
  (registration has one deadline, fallbacks included). What the pool sends is limited in size and
  its split in outputs, and a block that would not fit leaves out its transactions instead of being
  invalid. The dashboard warns if the pool's window leaves out your address while it credits your
  shares. In solo mode the app asks Forge Pool nothing.
- **Windows and Linux:** Settings now asks for a password before it saves a change, as on Umbrel:
  other programs and other accounts on the computer could change your payout address. On Windows,
  right-click the Forge Solo tray icon and choose **Copy Settings Password**; on Linux it is
  `DASHBOARD_PASSWORD` in `secrets.env`. The page remembers it in your browser. On Windows the copied
  password is kept out of the clipboard history and the cloud clipboard.
- **Every platform:** a Settings save is stored all at once or not at all. One that failed part way
  kept the new address while the page said nothing was saved.
- **Windows and Linux:** a web page can no longer reach your dashboard by pointing its own name at
  your computer (DNS rebinding). The dashboard and its API answer only to `127.0.0.1` and `localhost`.
- **Windows:** Defender now skips only the folders that change constantly (the nodes' chain data and
  the database), not the whole data folder. A user name with an apostrophe no longer breaks this.
- **Windows:** the firewall rules let in only Forge Solo's own programs, not anything on their ports.
  Forge Solo never uses a local port another program holds: if it finds none free, it starts nothing
  and says so in the tray.
- **Windows and Linux:** the dashboard cannot be shown inside another site's page (nor can its pages
  on the API's own port), and the dashboard, not the browser, tells the API which address a request
  came from.
- **Linux:** `install-service` works whatever the umask, stops if a Forge Solo you started yourself
  still holds the ports, and says it is done only once the service serves its dashboard; otherwise
  it shows the service's last log lines. A run as root on a data directory another account owns (the
  service's) is refused: the files it left there stopped the service's node.
- **Linux:** `forge-solo run --reindex` rebuilds the node's chain state after "Corrupted block
  database detected", which had no way out.
- **Windows:** the installer's signature carries a timestamp, so it stays valid after the certificate
  expires, and the release page gives the certificate's fingerprint.
- **Every platform:** the API no longer lets pages on `localhost:3000` read it.
- **Umbrel:** the 1175 node no longer lets peers that connect to it choose the address it
  advertises, no longer restarts in a loop when it is reached over IPv6, and a first install that was
  cut short no longer blocks the next one.

**Up to date.** Built with Go 1.26.8; Go 1.25 no longer gets security fixes. **Umbrel:** PostgreSQL
16.15 (was 16.6), with TimescaleDB 2.17.2 as before; nginx 1.30 (was 1.27, which is no longer
maintained). **Windows:** PostgreSQL 16.15 (was 16.10).

**Umbrel: installing needs Forge Solo's ports free:** 3333 and 3335 for miners, 8339 and 25360 for
the nodes' peers. If another app already uses one, the install stops with "exit code 1".
