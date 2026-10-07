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
- **Umbrel:** installing the app downloads about 240 MB instead of 930 MB; its images take about
  0.6 GB of disk instead of 2.3 GB. umbrelOS removes the images of 1.0.12 after the update.
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
- **Umbrel:** the BCH2 and 1175 node images are smaller, about 99 MB and 95 MB instead of 111 MB
  and 110 MB (amd64): they no longer carry curl and the certificate store, which only the image
  build used.

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
  solo blocks only, so it read 0 in TIDES mode while the TIDES card listed blocks found by "You". The
  balance card's amounts (matured, still maturing and the total), the Blocks table and Payout History
  still count only the blocks that paid your address in full (found in Solo mode or while TIDES was
  paused), so in TIDES mode they now say they are solo, and the table is headed "Your Solo Blocks".
  Your TIDES payouts are in the TIDES card, which the balance card and the empty tables point to.
- **Every platform, TIDES:** the TIDES card's payouts read "Pending (under 2 confirmations)" and "Paid
  to you (2+ confirmations)", each spendable 100 blocks after its block. "Confirmed" there meant 2
  confirmations, while the same page calls a solo block confirmed after 100.
- **Every platform:** a miner's payout list in the API (`/api/v1/miners/<address>/payouts`) works:
  it failed on Umbrel and Windows and was always empty on Linux. Times in the API are in UTC on every
  platform, a miner's last share and connection times included (`/api/v1/miners/<address>` and its
  workers list): on Windows and Linux those were in the computer's time zone. The miner's answer
  gives `roundEffort` (this round so far, share by share; 1 is 100%), and the mining status gives
  `network_difficulty` (the difficulty of the block being mined).
- **Every platform:** the API's workers list (`/api/v1/miners/<address>/workers`) also lists a
  worker that is connected and has no share yet, with the time it connected and a last share of
  null. A connected worker's `connectedAt` is when its connection began; it was when its first share
  was counted. A miner with no share yet has a `lastShare` of null; it said the year 1.
- **Every platform:** each worker's best share (Best Diff in the dashboard's Workers table,
  `athDiff` in the API) is kept in `forgesolo.db`, so a restart, an update or a reboot no longer
  resets it, and the column now reads "Best Diff (all time)". It counts from 1.0.13: 1.0.12 stored
  each share with the difficulty it was asked for, not the one it reached, so an earlier best cannot
  be recovered. A worker with a share in the last day is always kept, also across a restart. Of the
  others, each payout address keeps its best one and the ones seen last, 100 in all, and the 20
  payout addresses seen last keep theirs. Round Best still starts again at each block found and at
  a restart.
- **Every platform:** a miner's `athDiff` in `/api/v1/miners/<address>` is its best share of all
  time, also while the worker that found it is not connected, after a restart or a day without a
  share.
- **Umbrel and Windows:** going back to 1.0.12 and forward again keeps the best shares 1.0.13 kept;
  what was found while 1.0.12 ran is not counted.

**Steadier difficulty.**
- **Every platform:** the difficulty each miner is given now stays near the level its hashrate calls
  for. Each change was measured partly from shares found before the last one, so it kept
  overshooting: a steady 5 PH/s rental swung between half and 3.4 times its level, a dozen changes in
  7 minutes. Vardiff now times each share at the difficulty it was found at, over every share of the
  last 4 minutes, or the latest 30 where those are more. A steady miner's difficulty stays between
  about 0.75 and 1.35 times its level on 3333 and changes about 8 times an hour; on 3335 it stays
  between about 0.7 and 1.8 times its level and changes about 3 times an hour. A miner on 3333
  whose hashrate halves reaches its new level in about 3 and a half minutes; one whose hashrate
  doubles takes about 2 and a half minutes, its shares meanwhile faster, never refused. Shares were
  always credited at the difficulty they were found at; now they also arrive at a steady pace.
- **Every platform:** no miner is given a share difficulty above the network's, whether it comes
  from `d=` in the password, from the miner, from a remembered level or from vardiff, and when a new
  block lowers the network difficulty, miners above it are brought down to it. A miner set above it
  never sent the blocks it found between the two.
- **Every platform:** a big miner whose first shares arrive together (a packet lost and sent again,
  say) is no longer set far too high; it could show about 0 H/s for up to 30 minutes. The difficulty
  remembered across reconnects is kept per device, so a small rig with the same username as a big
  one no longer opens at the big one's level.

**Rented hashpower.**
- **Every platform:** the rental port, 3335, now aims for one share every 25 seconds, not every 5.
  MiningRigRentals shows each rig an "optimal difficulty" range, one share every 10 to 60 seconds at
  the rig's advertised hashrate, and warns below it: a rental's difficulty now stays inside that
  range for a rig that delivers about 60% to 135% of what it advertises. A 4.5 PH/s rental sits near
  26 million instead of 5 to 6 million, and its difficulty changes about 3 times an hour; the
  difficulty of your own miners on 3333 is unchanged. A rig under about 36 TH/s stays above the
  range at the 500,000 floor, as before, and a few listings give a range that does not follow the
  advertised hashrate, so the warning can still show for those. Following a fall in hashrate takes
  longer (a fall to a fifth: about half an hour); the shares meanwhile come slower and are credited
  in full. With about 2 shares a minute instead of 11, a rental's 5-minute hashrate, on the
  dashboard and in MiningRigRentals' own figure, varies by about 30% either way instead of 13%, and
  its 60-minute figure by about 9%. In TIDES mode a job commits to the highest difficulty its miners
  work at, so while a rental hashes it commits to the rental's level (2^26 to 2^27 instead of 2^24
  for 4.5 PH/s): every miner's shares, your own on 3333 included, are forwarded 4 to 8 times less
  often, for the same expected credit, which varies more from one window to the next.
- **Every platform:** a rental's level is remembered for as long as its shares confirm it, so a rig
  that reconnects, and MiningRigRentals' health checks, which log in under the order's name every few
  seconds, open at that level. It was remembered for 30 minutes after it last changed, which with so
  few changes would have sent them back to 500,000 a quarter of the time. After the mining service
  restarts they open at 500,000 until the rig's first shares set its level again.
- **Every platform, TIDES:** a rental that connects or reconnects, also after the mining service
  restarts, is no longer credited 0.2% of its work on its first jobs (the pool's 1,024 a share). A
  TIDES job commits to the difficulty its miners work at, and only miners with a share in the last
  five minutes counted, so the job in flight when a rental logged in committed to nothing. Now a
  miner counts from its login, at the difficulty it was given (a `d=` it only claims counts at the
  port's floor), and for five minutes after its last share once it disconnects; one that logs in on
  a job committed below its difficulty gets a new job at once, and a new rental's first raise counts
  as soon as it is made. A new rental is credited about 99% of its work in its first two minutes;
  the rest is the second or so before its first new job. While anything is logged in on 3335, TIDES
  jobs commit to at least 2^20, so small miners on 3333 see fewer of their shares forwarded, for the
  same expected credit.
- **Every platform:** MiningRigRentals' health checks, about 550 logins an hour during a rental, are
  no longer counted as miners or rentals. Workers, the mining banner and the public rental stats
  count one rented rig as one, and with only health checks connected the dashboard says no miner is
  connected. A health check that sends shares counts as a miner.
- **Every platform:** the rental port, 3335, reads `stratum_rental.vardiff.variance_percent` as the
  main port reads its own; no shipped config sets it, so 3335 keeps its +/-30% band.

**While the BCH2 node catches up.**
- **Every platform:** while the BCH2 node catches up with the chain, after a restart or at the end
  of a first sync, miners get a new block's work at most every 5 seconds instead of one job per old
  block. That was up to a hundred jobs a second, each with clean_jobs, so nearly every share in
  flight was refused as stale. The block that brings the node level goes out at once, and blocks
  that come 5 seconds or more apart, as at the tip, go out as before.
- **Every platform, TIDES:** after a restart with the BCH2 node behind the chain, TIDES comes back
  with the block that brings the node level instead of a minute later, and nothing is registered
  with the pool in the meantime. The log and the dashboard now say that this BCH2 node is not on
  Forge Pool's block yet, with both heights, where they said Forge Pool was unavailable or that
  TIDES would resume when the pool answered again. The same applies when this node and the pool
  have different blocks at the same height.

**The dashboard.**
- **Every platform:** Current Effort counts each share against the difficulty of the block it was
  mined for. BCH2's difficulty changes at every block, and the tile divided the whole round by the
  newest block's difficulty, so it jumped by tens of points with no change in mining (on a 4.7 PH/s
  rental: 236%, then 211%, then 316% within a minute, while the round really went from 214% to
  217%). Estimated Time to Block uses the difficulty of the block being mined, not the last block's.
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

**The mining service's log.**
- **Every platform:** shares refused as stale, for their ntime or as malformed now leave a line: a
  share that arrives just after a new block as information (expected now and then), and an ntime its
  job does not allow or a malformed submit as a warning. Shares refused over the rate limit are
  still not logged, as a miner over the limit sends a hundred a second, but they are now counted in
  the reject figures, so the dashboard no longer reads 0 rejects while the miner is told it had
  some. The line for a duplicate share gives the miner's address, its user agent and the whole
  share, and those for a job not found and a share below its difficulty the address, user agent and
  extranonce1. "Client disconnected" says how many shares the connection sent and how long before it
  ended the last one came.
- **Every platform:** MiningRigRentals' health checks no longer write five lines each. During a
  rental they were 85% of the log, and the 20 MB kept on Windows and Linux (30 MB on Umbrel) held
  under three days. The first is logged, and the rest are counted and logged together every 10
  minutes. The line for each new connection and the mining.configure line are now at debug level;
  login and disconnect lines give the miner's address, user agent and valid shares.
- **Every platform:** a rental's start no longer pushes refused shares and disconnects out of the
  mining service's log. A miner's login, refused shares, difficulty changes and disconnect have a
  budget of their own on each port, 480 lines at once and then 120 a minute, apart from the lines
  about connections (still 120 a minute), so dozens of rigs logging in together through one address
  are logged in full and connections that never log in can no longer crowd them out. A client
  sending shares below the floor could also have a line written for every 20 of them, outside both
  budgets: that line, and every other a client can cause but those about a block or a share close to
  one, now counts in one of them. Lines left out are counted in a line of their own within 30
  seconds, saying how many, why and which, instead of in a count on another connection's line.
- **Umbrel and Windows, TIDES:** the start no longer warns that 1175 merge-mining is off until you set
  a 1175 address in the dashboard, which would not turn it on in TIDES mode. It says that 1175
  merge-mining stays off while TIDES mode is on.

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
  copy, also when Forge Solo 1.0.13 or later runs for another Windows account. If the tray icon
  cannot be added at sign-in, Forge Solo starts again once.
- **Windows:** updating or uninstalling while Forge Solo runs closes it first, as its Quit does, and
  waits until its nodes and its database have stopped, also in a silent run. The installer used to
  stop with "Setup was unable to automatically close all applications", and a silent update undid
  itself. If Forge Solo 1.0.13 or later runs for another Windows account on the PC, the installer
  and the uninstaller say so and change nothing. To go back to 1.0.12, quit Forge Solo
  first: 1.0.12's installer cannot close it.
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
  forward TCP 3335 to this PC. The miner's and the API's logs are kept in the data folder,
  `stratum.log` and `api.log`, each moved to `.1` at 20 MB.
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
  - what a client can put in the log is limited on each port, and worker names are kept to plain
    labels;
  - logging in again on a connection no longer sends the whole job again;
  - a share sent again under a later job that carries the same work counts once (in TIDES mode the
    pool refused the second copy as a duplicate).

  Rented hashpower is no longer disconnected for waiting quietly between jobs.
- **Every platform, TIDES:** a slow or unresponsive Forge Pool holds a new block's work back for 2
  seconds at most, not up to 40 as before: if the pool has not registered the block's work within 2
  seconds, miners get solo work for it, then switch to the pool's job (with clean_jobs) as soon as
  it registers. A refresh on the same block no longer runs ahead of a new block's work. With a
  healthy pool the work goes out as before. What the pool sends is limited in size and its split in
  outputs, and a block that would not fit leaves out its transactions instead of being invalid. The
  dashboard warns if the pool's window leaves out your address while it credits your shares. In solo
  mode the app asks Forge Pool nothing.
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
- **Linux:** a 1175 address in the settings no longer switches 1175 merge-mining on. Forge Solo for
  Linux runs no 1175 node, but an address in a database brought from Umbrel or Windows, or one saved
  through the API, turned it on with no node to mine against: the log warned every minute that
  merge-mining had never worked, and the 1175 payout processor worked through that database's 1175
  records. They now stay as they are, and on Linux the API refuses a 1175 address.
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
- **Linux:** the start says mining waits for a BCH2 payout address only when none is saved; it said
  so at every start.
- **Linux:** the README checks a download with `grep x86_64 SHA256SUMS-linux | sha256sum -c -`,
  which works on Alpine and other systems with BusyBox; `--ignore-missing` stopped there.
- **Linux:** the README says what to delete once the service runs, after installing or upgrading it:
  the unpacked folders, the downloaded `.tar.gz` files and `SHA256SUMS-linux`
  (`rm -r forge-solo-*-linux-* SHA256SUMS-linux`). After upgrading a copy you run yourself, all of
  that but the new release's folder can go.

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

**Gone, since nothing used it.**
- **Every platform:** the API no longer answers routes nothing used: `GET /api/v1/blocks` and
  `/api/blocks`, `/api/v1/network`, `/api/v1/miners`, `/api/v1/workers`, `/api/v1/validate-address`,
  `/api/v1/validate-1175-address` and `POST /api/v1/miners/settings` (with the settings PIN it could
  set), and at the root of the API's own port `/api/stats`, `/metrics`, `/health` and `/favicon.ico`
  (`/api/v1/health` stays). `/api/v1/stats` no longer carries `hashrateRaw`, `workers`, `miners`,
  `currentHeight`, `bestBlockHash`, `uptime`, `luck` and the per-marketplace rental counts
  (`rentals.total` stays); `/api/v1/miners/<address>` no longer carries `workers`, `balance` and
  `currentHeight`; each row of `/api/v1/miners/<address>/workers` no longer carries `validShares`,
  `invalidShares` and `rejectRate`. The dashboard reads none of them.
- **Every platform:** the programs no longer read `HOME_APP`, `CORS_ORIGINS`, `API_HOST`, `API_PORT`,
  `API_RATE_LIMIT`, `FORGE_RPC_USER`, `FORGE_RPC_PASSWORD`, `FORGE_DB_PASSWORD`, `HALVING_INTERVAL`
  and `WEBHOOK_URL`. In the mining service's config, `stratumv2.enabled` is ignored like any unknown
  key instead of logging a refusal, and `stratum.ban_duration`, `node.user` and `node.password` have
  no default.
- **Every platform:** the mining service's `config.yaml` (written by Forge Solo on Windows and Linux,
  rendered from the template on Umbrel) no longer sets `pool.name`, `pool.coin` and
  `pool.coin_symbol`, which the mining service never read; on Windows and Linux the file loses them
  at the next start. Nothing changes in how it runs.
- **Every platform:** with `DB_PATH` unset (a run by hand; every platform sets it), the mining
  service and the API no longer adopt a `forgepool.db` beside the executable.
- **Umbrel:** the 1175 node image no longer lists 25361 as an exposed port; nothing listened on it
  (peers 25360, RPC 25359).
- **Umbrel:** the store listing no longer includes `init-db.sql`, the PostgreSQL schema of 1.0.12 and
  before; nothing in 1.0.13 reads it.

**Up to date.** Built with Go 1.26.8; Go 1.25 no longer gets security fixes. **Umbrel:** nginx 1.30
(was 1.27, which is no longer maintained). **Umbrel and Windows:** PostgreSQL 16.15 is used only to
read the old database during the move.
