# Forge Solo: release notes

One section per version, headed `## <version>`, for every platform: the Umbrel app, Windows and
Linux. The release workflow puts the section at the top of that version's release page, and a
release without one fails rather than publishing an empty page. The Umbrel app's own update
screen shows `releaseNotes` from `umbrel-app.yml` instead, so write that too.

Up to 1.0.12 each platform kept its own notes: [windows/RELEASE_NOTES.md](windows/RELEASE_NOTES.md)
and [packaging/linux/RELEASE_NOTES.md](packaging/linux/RELEASE_NOTES.md).

## 1.0.13

**Installing on Windows.**
- **Windows:** on Windows 11, Smart App Control must be **Off**: Forge Solo is not yet signed by a
  certificate Windows trusts, and while Smart App Control is On, Windows blocks it. When SmartScreen
  says "Windows protected your PC", choose **More info**, then **Run anyway**.
  [Install on Windows](https://github.com/BitcoincashII/forge-solo#install-on-windows) in the README
  has every step.

**One database file on every platform.**
- **Every platform:** Forge Solo keeps your settings, blocks and payouts in one file, `forgesolo.db`,
  as Forge Solo for Linux already did. Umbrel and Windows no longer run a database server (on
  Umbrel, the one 1.0.12 ran could take up to a quarter of the machine's memory). The API and the
  miner share the file without failing each other's writes, and the dashboard keeps answering while
  one of them writes.
- **Umbrel and Windows:** the first start after the update moves your data from the old database
  into `forgesolo.db`, once: your payout and 1175 addresses, coinbase tag, solo or TIDES and the
  TIDES key, each miner's settings and PIN, your blocks and payouts, and the 1175 records. Every row
  and every amount is checked before anything is replaced. It usually takes seconds. Times are
  rounded to the second.
- **Umbrel and Windows:** the old database stays where it was, for going back to 1.0.12: `postgres/`
  in the app's data on Umbrel, `pgdata` in `%APPDATA%\ForgeSolo` on Windows. The move changes none
  of your data in it (PostgreSQL updates its own bookkeeping files when it starts and stops, as it
  always does), and the shares 1.0.12 stored stay in it. Going back to 1.0.12 and forward again
  keeps what both versions recorded, and Forge Solo keeps one copy of its database from before that
  merge, `forgesolo.db.before-merge-<time>`. Once 1.0.13 shows your blocks and settings, you can
  delete the old database to free its space: deleting it, even half way, never stops Forge Solo, but
  1.0.12 then no longer has your data.
- **Umbrel and Windows:** if the move fails, nothing is lost and nothing is replaced. Forge Solo does
  not mine, the nodes keep running, and the dashboard says what went wrong and what you can do; on
  Windows the tray says so too. Restarting Forge Solo tries the move again. **Start without the old
  data** on the dashboard (it asks for the Settings password) starts Forge Solo on a new, empty
  database instead, and Settings can bring the old data in later. A file named
  `SKIP-POSTGRES-MIGRATION` beside `forgesolo.db` does the same by hand; remove it, and the next
  start brings the old data in. Chosen after going back to 1.0.12 and forward again, it starts Forge
  Solo on the database 1.0.13 already had, which mines at once to the payout address saved in it:
  check that address in Settings.
- **Umbrel and Windows:** the dashboard says so when the move finishes only at the next restart
  (something had the database open), when Forge Solo runs without the old data, and when the old
  database's folder is damaged and ignored.
- **Umbrel:** the move runs in a small step of its own (the migrate container, about 22 MB to
  download) before the API and the miner start. Once the data is moved it finds nothing to do and
  ends within a second, at every start. An update made while 1.0.12 still runs stops its database
  cleanly first, and a move cut short (a power cut, say) is made again at the next start.
- **Windows:** PostgreSQL runs only for the move, on this PC only, and is removed once the move is
  checked. A fresh install puts no PostgreSQL on disk. If your data has to be moved again later
  (`forgesolo.db` deleted, or an old data folder copied in), the dashboard says to run the installer
  again: it installs PostgreSQL when the old data is there.

**Much smaller.**
- **Umbrel:** installing downloads about 220 MB instead of 900 MB, and the app's images take about
  0.5 GB of disk instead of 2.3 GB. umbrelOS removes the images of 1.0.12 after the update.
- **Windows:** Forge Solo takes about 110 MB less disk: a fresh install puts no PostgreSQL on disk,
  and an update removes it once your data is moved.
- **Every platform:** every share your miners found was written to the database and never read, and
  nothing trimmed it until a block was found. That stops. On Linux what was stored is cleared at the
  first start, and the file shrinks; on Umbrel and Windows it stays in the old database until you
  delete that.
- **Umbrel:** the app's logs take at most 30 MB per service (umbrelOS allowed 100 MB), and the
  routine lines that filled them (healthchecks, every share, every dashboard refresh) are gone.
  Umbrel's backups skip the nodes' chain data, which the nodes can download again, and the nodes'
  `debug.log`, which repeats the app's own logs.
- **Every platform:** the nodes cap their memory for a machine they share with other things: a
  100 MB database cache, a 50 MB mempool and a 4 MB signature cache. On Umbrel and Windows they also
  run without a wallet, which nothing here uses. On Windows the 1175 node is no longer pruned, as on
  Umbrel: with 25360 forwarded it is listed as a full node and gets more incoming peers.

**Found blocks, and what the dashboard says about them.**
- **Every platform:** a block found while the node is restarting or too busy to answer is kept
  trying for 30 minutes; it was given up after 6 seconds. On Umbrel and Windows a solved 1175 block
  is kept trying for up to 5 minutes while the 1175 node is busy or slow, and one the node took but
  answered late is recorded. A block that cannot be written to the database at once is recorded once
  it can be, at the time it was found, if Forge Solo keeps running meanwhile (for up to a day); it
  used to be missing from the dashboard for good. The block itself pays you either way.
- **Every platform:** changing your payout address takes effect at once, and on Umbrel and Windows
  so does changing or clearing the 1175 address. Miners used to finish the work they had, which paid
  the old address, and a block found on it was recorded under the new one.
- **Every platform:** an orphaned block is marked **Orphaned**, and left out of your totals, about 6
  blocks after it was found, instead of about 17 hours later; a block found before the machine was
  off for days is checked by its hash.
- **Umbrel and Windows:** a 1175 block is no longer marked orphaned while the 1175 node is still
  syncing or rebuilding its chain, and one marked so by mistake is put back once the node has it.
  1175 blocks found before a switch to TIDES, or before the 1175 address was cleared, still mature
  after a restart. Stopping Forge Solo while it credits a 1175 block an older version recorded no
  longer leaves the miner hanging until it is ended.
- **Umbrel and Windows:** a 1175 block found on the job of the BCH2 block before the current one, or
  by a share over the rate limit, is still sent to the 1175 node (the share itself is still
  refused). Merge mining moves to a new 1175 tip within about two seconds of a 1175 block found by
  anyone; it took up to half a minute.
- **Every platform:** the totals stay right past 100 blocks (they stopped growing there), and the
  hashrate reads right a minute after a restart or an update (it started at a fifth of the real
  figure). A miner that is connected shows online between its shares, and a small one before its
  first. A node that is starting says so, with the step it is on, and one that is not answering at
  all (stopped, or restarting in a loop) is shown as that, with where to look, instead of "Starting
  the BCH2 node" for hours. When the node or the database cannot be read, the figures show "--"
  instead of 0.00 and "No blocks found yet", and catch up by themselves.
- **Every platform, TIDES:** Your Blocks Found counts the TIDES blocks your install found. It counted
  solo blocks only, so it read 0 in TIDES mode while the TIDES card listed blocks found by "You".
- **Every platform:** a miner's payout list in the API (`/api/v1/miners/<address>/payouts`) works:
  it failed on Umbrel and Windows and was always empty on Linux. Times in the API are in UTC on every
  platform.

**Steadier difficulty.**
- **Every platform:** the difficulty each miner is given now stays near the level its hashrate calls
  for. Each change was measured partly from shares found before the last one, so it kept
  overshooting: a steady 5 PH/s rental swung between half and 3.4 times its level, a dozen changes in
  7 minutes. Shares were always credited at the difficulty they were found at; now they also arrive
  at a steady pace.
- **Every platform:** no miner is given a share difficulty above the network's, whether it comes
  from `d=` in the password, from the miner, from a remembered level or from vardiff, and when a new
  block lowers the network difficulty, miners above it are brought down to it. A miner set above it
  never sent the blocks it found between the two.
- **Every platform:** a big miner whose first shares arrive together (a packet lost and sent again,
  say) is no longer set far too high; it could show about 0 H/s for up to 30 minutes. The difficulty
  remembered across reconnects is kept per device, so a small rig with the same username as a big
  one no longer opens at the big one's level.

**The dashboard.**
- **Every platform:** on phones, tablets and narrow windows the header keeps its Dashboard and
  Settings links and the SOLO / TIDES badge, and the cards keep a margin from the window's edges.
  The hashrate chart shows its units, and Settings shows the network difficulty in the dashboard's
  short form. The red "lost its network connection" banner now appears when the device you view the
  dashboard on goes offline.
- **Every platform:** a Settings save made while Forge Solo is restarting or down says Forge Solo is
  not answering and nothing was saved, instead of a JavaScript error, and what you typed stays.
  After updating, reload any open Forge Solo tab before you save settings; a Settings page from
  before the update says so itself when a save is refused. The messages after a save use plain
  punctuation.
- **Windows and Linux:** the address the dashboard and Settings give for miners is this machine's
  address on your network, also while a VPN is on; it was the VPN tunnel's, which miners on your
  network cannot reach.
- **Windows:** after an update the browser shows the new dashboard, not the page it kept from the
  version before, which knew nothing of the move.
- **Linux:** Settings names the `secrets.env` that holds its password, such as
  `sudo cat /var/lib/forge-solo/secrets.env` for the service.

**Windows: starts on every PC, and stops cleanly.**
- **Windows:** Forge Solo now works on a PC that has never had Microsoft's Visual C++ runtime, a
  fresh Windows install for example. The database of 1.0.12 never started there, and the tray said
  only that it had failed.
- **Windows:** Forge Solo now works under any Windows user name. 1.0.12 never created its database
  under a name with a letter beyond plain English (Jürgen, or a Chinese name, say), so it did not
  work there at all.
- **Windows:** the installer stops on 32-bit Windows and on Windows 10 on ARM, where Forge Solo
  cannot run, and says it needs 64-bit Windows: Windows 10 or 11 on an x64 PC, or Windows 11 on ARM.
  It used to install there, and Forge Solo then could not start.
- **Windows:** Quit stops everything, also while Forge Solo is still starting, and the tray icon stays,
  showing the stop, until it is done. It used to leave both nodes and the database running after the
  tray icon was gone, and the next start then failed until the PC was restarted. A node still loading
  its blocks is asked again until it stops, instead of being ended.
- **Windows:** shutting down, restarting or signing out stops the miner and both nodes cleanly
  first, in a few seconds. They used to be ended mid-run and had to recover on the next start. If
  the shutdown is cancelled after all, Forge Solo starts again; it used to stay closed.
- **Windows:** if Forge Solo is ended from Task Manager, or crashes, the next start stops what it
  left running and then starts as usual.
- **Windows:** opening Forge Solo while it runs opens its dashboard instead of starting a second
  copy, also when it runs for another Windows account, and updating or uninstalling asks you to close
  it first. If the tray icon cannot be added at sign-in, Forge Solo starts again once.
- **Windows:** if another program uses port 3080, the tray says the dashboard cannot open, instead of
  opening that program in the browser. Mining goes on. If one uses 3333 or 8339, which Forge Solo
  cannot mine without, it starts nothing and the tray names the port; it used to say "running".
  After a start that failed, right-click the tray icon and choose **Try Again**; starting Forge Solo
  a second time only opened a dashboard nothing served. `launcher.log` names the program that holds
  the port, or says that Windows keeps the port for itself. Checking the ports no longer listens on
  them beyond this PC, and a program started later can no longer take the miner's port and its
  miners.
- **Windows:** if another program uses port 3335, the rental port, Forge Solo starts without it, and
  the tray, `launcher.log` and the dashboard say so: rentals have no port of their own until you stop
  that program, then restart Forge Solo. If Windows keeps 3335 for itself (a range reserved for
  Hyper-V, WSL or Docker), they say so: rentals have no port of their own until Windows lets it go
  and you restart Forge Solo. If another program uses 25360, the 1175 node runs without incoming
  peers and merge mining goes on; the node could stop at every start.
- **Windows:** a program Forge Solo cannot start (an antivirus holding or removing it, Smart App
  Control, a file in use) is named in the tray and in `launcher.log` with the reason, and started as
  soon as it can be. One that stops on its own (the miner, the API or a node crashing) is started
  again, and the tray says so. The tray says "running" only once both nodes, the API and the miner
  run; it used to say so with nothing mining. Restart Mining also starts a miner that is not running,
  and no longer holds up the tray menu.
- **Windows:** a BCH2 node whose chain data a power cut damaged is rebuilt once from the blocks on
  disk; it used to stop at every start.
- **Windows:** Forge Solo no longer replaces `secrets.env` when it cannot read it, and the file is
  written so that a power cut cannot leave it half written: it holds the old database's password,
  which moving your data and going back to 1.0.12 need.
- **Windows:** rented hashpower gets its own port, 3335, with the same difficulty settings as on
  Umbrel and Linux. If your NiceHash or MiningRigRentals order points at 3333, move it to 3335 and
  forward TCP 3335 to this PC. The miner's and the API's logs are kept in the data folder.
- **Windows:** miners on your network, and rentals, reach the PC only while Windows treats your
  network as **Private**; Windows 11 makes new networks Public. The dashboard's connect card and
  Settings now say so.
- **Windows:** the tray's messages are short enough for Windows 11 to show them whole; it cut them
  off at 64 characters.
- **Windows:** links of the form `/solo/<address>` open the dashboard, as on Linux and Umbrel, and
  folders such as `/js/` are no longer listed.
- **Windows:** uninstalling keeps your data folder unless you choose to delete it (No is now the
  default), and says what the folder holds. `launcher.log` in the data folder records what Forge Solo
  started and stopped, and is kept under 1 MB. Unticking "Start Forge Solo when I sign in" in an
  update turns that off.

**Safer.**
- **Umbrel:** Settings now asks for Forge Solo's password before it saves a change. Other apps on an
  Umbrel can reach Forge Solo directly, without going through umbrelOS's login, so a change needs the
  password umbrelOS shows for this app: when you open Forge Solo, and any time later by right-clicking
  its icon (**Settings** → **Default credentials**; on umbrelOS 1.x, **Show default credentials**).
  The page remembers it in your browser. Mining is not affected.
- **Umbrel:** other apps now reach only Forge Solo's dashboard. Its nodes, its API and its mining
  service talk to each other over a network of their own. The dashboard cannot be shown inside
  another site's page.
- **Every platform:** the mining ports are hardened. This matters most for the rental port, which is
  open to the internet once it is forwarded. Changes:
  - shares that are malformed, stale or sent again are refused before they cost memory;
  - a connection that never logs in is closed after a minute;
  - a quarter of the connection slots are kept for miners on your own network, also when many
    connections arrive at once;
  - the rental port limits how fast shares arrive;
  - one stuck connection no longer delays new work for every other miner, and a miner that reads
    everything it is sent is no longer dropped after a burst of its own requests, which happened
    most on single-CPU machines;
  - what a client can put in the log is limited, at most 120 lines a minute per port, and worker
    names are kept to plain labels;
  - logging in again on a connection no longer sends the whole job again;
  - a share sent again under a later job that carries the same work counts once (in TIDES mode the
    pool refused the second copy as a duplicate).

  Rented hashpower is no longer disconnected for waiting quietly between jobs.
- **Every platform, TIDES:** a slow or misbehaving pool can no longer hold up new work for your miners
  (registration has one deadline, fallbacks included). What the pool sends is limited in size and
  its split in outputs, and a block that would not fit leaves out its transactions instead of being
  invalid. The dashboard warns if the pool's window leaves out your address while it credits your
  shares. In solo mode the app asks Forge Pool nothing.
- **Windows and Linux:** Settings now asks for a password before it saves a change, as on Umbrel:
  other accounts on the computer could change your payout address. On Windows, right-click the
  Forge Solo tray icon and choose **Copy Settings Password**; on Linux it is `DASHBOARD_PASSWORD` in
  `secrets.env`. The page remembers it in your browser. On Windows the copied password is kept out
  of the clipboard history and the cloud clipboard.
- **Every platform:** a Settings save is stored all at once or not at all. One that failed part way
  kept the new address while the page said nothing was saved.
- **Windows and Linux:** a web page can no longer reach your dashboard by pointing its own name at
  your computer (DNS rebinding). The dashboard and its API answer only to `127.0.0.1` and `localhost`.
- **Windows and Linux:** the dashboard cannot be shown inside another site's page (nor can its pages
  on the API's own port), and the dashboard, not the browser, tells the API which address a request
  came from.
- **Every platform:** the API no longer lets pages on `localhost:3000` read it.
- **Windows:** Defender now skips only the nodes' chain data (their blocks and chainstate folders),
  not the whole data folder; the exclusions earlier versions added for the data folder and the old
  database are removed. A user name with an apostrophe, straight or curly, no longer breaks this.
- **Windows:** the firewall rules let in only Forge Solo's own programs, not anything on their ports,
  and are named for your Windows account ("Forge Solo Miner (3333) for <account>"), so two accounts
  on one PC each keep their own. Those of earlier versions are removed, and uninstalling removes them
  also after the account was renamed. Forge Solo never uses a local port another program holds: if it
  finds none free, it starts nothing and says so in the tray.
- **Windows:** the installer says beforehand what Windows' permission prompt is for: it names
  Windows Command Processor, and adds the firewall rules that let miners on your network connect and
  the Defender exclusions. If the prompt is refused, or a rule is missing afterwards, the installer
  says so and how to put it right: run it again and choose Yes. It used to finish without a word, and
  miners on other devices could not connect. Uninstalling says what it could not remove. Setup
  started with "Run as administrator" names, on its first page, the Windows account it installs for.
- **Windows:** the installer's signature carries a timestamp, so it stays valid after the certificate
  expires, and the release page gives the certificate's fingerprint.

**Linux.**
- **Linux:** another program on port 3335 no longer stops Forge Solo: it starts without it and says
  that another program uses port 3335, the rental port. Rentals have no port of their own until you
  stop that program, then restart Forge Solo. Another program on 3333 or 8339 still stops it.
- **Linux:** a node whose chain data a power cut or a full disk damaged ("Error opening block
  database" or "Corrupted block database detected") is rebuilt once from the blocks on disk, as on
  Windows. It used to be started again and again with mining stopped.
- **Linux:** Forge Solo needs Linux 3.17 or newer (1.0.12 said 3.2). On an older kernel it now says
  so and starts nothing; the node used to stop at every start without a word.
- **Linux:** `install-service` works whatever the umask, checks the ports before it stops a running
  service, and starts the service again if the install fails after stopping it; it used to leave the
  service stopped, and mining with it. It stops if a Forge Solo you started yourself still holds the
  ports, and says it is done only once the service serves its dashboard; otherwise it shows the
  service's last log lines.
- **Linux:** upgrading the service keeps its dashboard address (`--web`); it went back to
  127.0.0.1. With `--web 0.0.0.0:3080` the dashboard address Forge Solo prints is one a browser on
  another computer can open, not http://0.0.0.0:3080.
- **Linux:** `install-service` copies only the release's own files to `/opt/forge-solo`. A data
  directory kept in the release directory was copied too, readable by every account on the machine.
- **Linux:** a run as root on a data directory another account owns (the service's) is refused: the
  files it left there stopped the service's node. The commands it suggests instead work, also for
  LDAP or SSSD accounts and after `uninstall-service`.
- **Linux:** `forge-solo cli` says when no Forge Solo runs with the data directory and gives the
  command for the service's node (`sudo /opt/forge-solo/forge-solo cli ...`), instead of
  "Authorization failed". It works as the `forge-solo` user too.

**Umbrel.**
- **Umbrel:** a BCH2 node whose chain data is damaged (after a power cut or a full disk, say)
  rebuilds it once on its own, as on Windows and Linux; it used to restart for ever with mining
  stopped. If the data is still damaged after that, the app's log says which folders to delete.
- **Umbrel:** the 1175 node no longer lets peers that connect to it choose the address it
  advertises, no longer restarts in a loop when it is reached over IPv6, and a first install that was
  cut short no longer blocks the next one.
- **Umbrel: installing needs Forge Solo's ports free:** 3333 and 3335 for miners, 8339 and 25360 for
  the nodes' peers. If another app already uses one, the install stops with "exit code 1", and the
  parts of Forge Solo that had started, such as the nodes, keep running in the background, also
  after a restart: free the port and install Forge Solo again, which takes them over. An app
  installed after Forge Solo whose own port is 3333 or 3335 takes that port from Forge Solo without
  an error: Bleskomat Server uses 3333 (every miner) and Bitmagnet 3335 (rentals), so neither can run
  beside Forge Solo.

**Up to date.** Built with Go 1.26.8; Go 1.25 no longer gets security fixes. **Umbrel:** nginx 1.30
(was 1.27, which is no longer maintained). **Umbrel and Windows:** PostgreSQL 16.15 is used only to
read the old database during the move.
