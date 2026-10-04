package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/systray"
)

// setupSecrets reads secrets.env, making what it lacks. It never replaces a file it could not read
// (another program may hold it a moment), and never makes a new database password for a database
// that exists: the database would then never open again.
func setupSecrets() error {
	f := dpath("secrets.env")
	b, err := os.ReadFile(f)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("secrets.env cannot be read (%v)", err)
	}
	if err == nil {
		// crude parse KEY=VAL lines
		for _, line := range splitLines(string(b)) {
			k, v := cut(line, '=')
			switch k {
			case "BCH2":
				sec.BCH2Pass = v
			case "AUX":
				sec.AuxPass = v
			case "DB":
				sec.DBPass = v
			case "TOKEN":
				sec.Token = v
			case "SETTINGS":
				sec.Settings = v
			}
		}
	}
	changed := false
	if sec.BCH2Pass == "" || sec.AuxPass == "" || sec.DBPass == "" || sec.Token == "" {
		if _, err := os.Stat(dpath("pgdata", "PG_VERSION")); err == nil {
			return errors.New("secrets.env in the data folder lacks the database password")
		}
		sec = secrets{BCH2Pass: gen(), AuxPass: gen(), DBPass: gen(), Token: gen(), Settings: sec.Settings}
		changed = true
	}
	// The Settings page's password: other programs and accounts on this PC can reach the
	// dashboard and its API, so a change needs it. Installs from 1.0.12 and before gain it here,
	// their other secrets unchanged (the database's password must stay what the database has).
	if sec.Settings == "" {
		sec.Settings = genHex(32)
		changed = true
	}
	if changed {
		content := "BCH2=" + sec.BCH2Pass + "\nAUX=" + sec.AuxPass + "\nDB=" + sec.DBPass + "\nTOKEN=" + sec.Token +
			"\nSETTINGS=" + sec.Settings + "\n"
		// Written aside, on the disk, and only then moved into place: a crash or a power cut part-way
		// must not lose the database password.
		if err := writeDurably(f, content); err != nil {
			return fmt.Errorf("secrets.env cannot be written (%v)", err)
		}
	}
	return nil
}

// writeDurably replaces path with content: written to path.tmp, flushed to the disk, then renamed
// over path, so the file is the old one or the new one, never a part.
func writeDurably(path, content string) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.WriteString(content); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func writeConfigs() {
	md(dpath("bch2"))
	md(dpath("elevenseventyfive"))
	// listen so the node ACCEPTS incoming peers once the router forwards the port. UPnP and
	// NAT-PMP are off: opening a port on someone's router is a change to their network, and
	// it is not this installer's to make silently. The Umbrel build never did it either, and
	// Bitcoin Core ships both off. Outbound peering is unaffected; inbound needs a forward.
	// dbcache/maxmempool/maxsigcachesize/par/maxconnections keep it light on a laptop. writeAlways so upgrades apply.
	//
	// Neither node is pruned, as on Umbrel. Pruning saved nothing (each chain is under 100 MB),
	// but it made the node announce NODE_NETWORK_LIMITED instead of NODE_NETWORK, and the DNS
	// seeders list only NODE_NETWORK nodes, so it waited on chance for inbound peers however
	// the router was forwarded. Earlier releases wrote prune=2000, but a node prunes nothing
	// below its prune height (BCH2 200,000, 1175 100,000) or under the 2000 MB it was given, so
	// no datadir was ever actually pruned and each one starts unpruned as it is.
	//
	// No wallet, as on Umbrel: nothing here uses one, and anything holding the RPC password
	// could make and use it.
	//
	// The binds are explicit because the node's mainnet Tor onion target port is 8339 too (its
	// chainparamsbase; upstream uses the P2P port + 1): with no bind= it takes 127.0.0.1:8339 for
	// that onion listener first, its own 0.0.0.0:8339 then fails ("Unable to bind ... probably
	// already running"), and no IPv4 peer could ever connect in. The onion listener moves to 8340.
	writeAlways(dpath("bch2", "bch2.conf"),
		"server=1\nlisten=1\nrpcbind=127.0.0.1\nrpcallowip=127.0.0.1\nrpcport="+bch2RPC+
			"\nrpcuser=forge\nrpcpassword="+sec.BCH2Pass+"\nport="+bch2P2P+"\n"+
			"bind=0.0.0.0:"+bch2P2P+"\nbind=[::]:"+bch2P2P+"\nbind=127.0.0.1:8340=onion\n"+
			"upnp=0\nnatpmp=0\ndiscover=1\ndbcache=100\nmaxmempool=50\nmaxsigcachesize=4\npar=1\nmaxconnections=40\n"+
			"disablewallet=1\nzmqpubhashblock=tcp://127.0.0.1:"+bch2ZMQ+"\ndnsseed=1\n")
	auxListen := "1"
	if auxNoPeers {
		auxListen = "0" // another program holds its peer port (checkPublicPorts)
	}
	writeAlways(dpath("elevenseventyfive", "1175.conf"),
		"server=1\nlisten="+auxListen+"\nrpcbind=127.0.0.1\nrpcallowip=127.0.0.1\nrpcport="+aux1175RPC+
			"\nrpcuser=forge1175\nrpcpassword="+sec.AuxPass+"\nport="+aux1175P2P+"\n"+
			"upnp=0\nnatpmp=0\ndiscover=1\ndbcache=100\nmaxmempool=50\nmaxsigcachesize=4\npar=1\nmaxconnections=40\n"+
			"disablewallet=1\ndnsseed=1\n"+
			"addnode=213.181.112.83\naddnode=46.7.7.113\naddnode=93.127.117.218\n")
	// writeAlways so a port change (e.g. moving the 1175 RPC off a Windows-blocked port)
	// propagates to the stratum's merge-mining config on upgrade. Fully generated file.
	writeAlways(dpath("config.yaml"), configYAML())
}

func configYAML() string {
	// Mirrors the app's docker/stratum/config.template.yaml (the tested, shipped config).
	// Keep the two in step: keys the stratum does not read are silently ignored, so a stale
	// key here looks configured but does nothing.
	return `pool:
  name: "Forge Solo"
  coin: "Bitcoin Cash II"
  coin_symbol: "BCH2"
  address: ""
  block_reward: 50.0
  payout_scheme: "solo"
  coinbase_tag: ""
stratum:
  host: "0.0.0.0"
  port: ` + minerPort + `
  max_connections: 256
  max_connections_per_ip: 128
  max_shares_per_second: 100
  extranonce1_size: 4
  extranonce2_size: 8
  vardiff:
    enabled: true
    min_diff: 1024
    max_diff: 1000000000000
    target_time: 5
    retarget_time: 10
    variance_percent: 25
# NiceHash / MiningRigRentals put a whole order behind one connection: a separate port with a
# difficulty floor that suits an aggregated order, as on Umbrel and Linux.
stratum_rental:
  enabled: true
  host: "0.0.0.0"
  port: ` + rentalPort + `
  max_connections: 64
  max_connections_per_ip: 32
  max_shares_per_second: 100
  extranonce1_size: 4
  extranonce2_size: 8
  vardiff:
    enabled: true
    min_diff: 500000
    max_diff: 1000000000000
    target_time: 5
    retarget_time: 10
node:
  host: "127.0.0.1"
  port: ` + bch2RPC + `
  use_ssl: false
  zmq_endpoint: "tcp://127.0.0.1:` + bch2ZMQ + `"
mergemining:
  enabled: true
  payout_address: ""
  aux_node:
    host: "127.0.0.1"
    port: ` + aux1175RPC + `
    user: "forge1175"
    pass: "` + sec.AuxPass + `"
logging:
  level: "info"
  format: "json"
`
}

// rotateLog moves a log past limit bytes to <name>.1, replacing the one before. pg_ctl -l appends to
// its log for as long as the install lives, and nothing else trims it.
func rotateLog(path string, limit int64) {
	if st, err := os.Stat(path); err == nil && st.Size() > limit {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
}

// dbStarting is set while pg_ctl starts the database. Until the server has written postmaster.pid
// the stop cannot see it, and would leave it running.
var dbStarting atomic.Bool

func startPostgres() bool {
	pgdata := dpath("pgdata")
	md(pgdata)
	for _, p := range []string{pgdata, ipath("pgsql", "bin")} {
		if _, bad := pgForm(p); bad != "" {
			logf("the database cannot start: the bundled PostgreSQL cannot take the folder name %q in %s. It has characters outside this PC's language for non-Unicode programs, and the drive keeps no short (8.3) name for it", bad, p)
			return false
		}
	}
	if _, err := os.Stat(filepath.Join(pgdata, "PG_VERSION")); os.IsNotExist(err) {
		pwf := dpath("pgpw.txt")
		_ = os.WriteFile(pwf, []byte(sec.DBPass), 0o600)
		init := pgCmd("pgsql\\bin\\initdb.exe", "-D", pgPath(pgdata), "-U", "forge", "-A", "scram-sha-256",
			"--pwfile", pgPath(pwf), "-E", "UTF8", "--no-locale")
		_ = runToEnd(init, nil)
		_ = os.Remove(pwf)
	}
	log := dpath("pglog.txt")
	rotateLog(log, 10<<20)
	// pg_ctl -w succeeds only once the server it started is ready, which it is only after taking
	// 127.0.0.1:pgPort itself. Something else answering on the port is not the database, and the
	// services would hand it the database password (lib/pq sends it in the clear when asked).
	// -t 300: a server recovering from a hard stop can take longer than the default 60 s.
	pgctl := pgCmd("pgsql\\bin\\pg_ctl.exe", "-D", pgPath(pgdata), "-l", pgPath(log), "-o", "-p "+pgPort+" -h 127.0.0.1", "-w", "-t", "300", "start")
	if runToEnd(pgctl, &dbStarting) != nil {
		return false
	}
	env := append(os.Environ(), "PGPASSWORD="+sec.DBPass)
	// create the database (ignore "already exists")
	cdb := pgCmd("pgsql\\bin\\createdb.exe", "-h", "127.0.0.1", "-p", pgPort, "-U", "forge", "forgesolo")
	cdb.Env = env
	_ = runToEnd(cdb, nil)
	// load the schema (idempotent; init-db.sql uses IF NOT EXISTS)
	psql := pgCmd("pgsql\\bin\\psql.exe", "-h", "127.0.0.1", "-p", pgPort, "-U", "forge", "-d", "forgesolo", "-f", pgPath(ipath("init-db.sql")))
	psql.Env = env
	_ = runToEnd(psql, nil)
	return true
}

// dbEnv is how the API and the miner reach the database. Each keeps at most 10 connections, as on
// Umbrel; uncapped, each could open 100, all the server allows.
func dbEnv() []string {
	return []string{
		"DB_HOST=127.0.0.1", "DB_PORT=" + pgPort, "DB_USER=forge",
		"DB_PASSWORD=" + sec.DBPass, "DB_NAME=forgesolo", "DB_SSLMODE=disable",
		"DB_MAX_OPEN_CONNS=10", "DB_MAX_IDLE_CONNS=2",
	}
}

// startNodes starts both nodes. One that cannot start is tried again in the background, and boot
// goes on: the miner waits for the BCH2 node.
func startNodes() {
	if bch2, aux := startOrKeepTrying("bch2"), startOrKeepTrying("aux1175"); bch2 && aux {
		logf("nodes started")
	}
}

func startBCH2() error { return startNode("bch2") }
func startAux() error  { return startNode("aux1175") }

// startNode starts the node under key, with extra arguments (-reindex). Where its debug.log ends is
// noted first, so that what it writes in this run can be told from the runs before.
func startNode(key string, extra ...string) error {
	dir, exe, conf := dpath("bch2"), "bitcoincashIId.exe", dpath("bch2", "bch2.conf")
	if key == "aux1175" {
		dir, exe, conf = dpath("elevenseventyfive"), "elevenseventyfived.exe", dpath("elevenseventyfive", "1175.conf")
	}
	noteLogEnd(key, filepath.Join(dir, "debug.log"))
	return run(key, hiddenPrio(belowNormal, exe, append([]string{"-datadir=" + dir, "-conf=" + conf}, extra...)...))
}

func startStratum() error {
	c := hidden("stratum.exe", "-config", dpath("config.yaml"))
	c.Env = append(append(os.Environ(), dbEnv()...),
		// API_PORT points the stratum at api.exe for miner-settings lookups; INTERNAL_STATS_PORT
		// is the stratum's own stats listener that api.exe polls via STRATUM_INTERNAL_URL. Two
		// different services -- swapping them silently zeroes every stratum-sourced dashboard tile.
		"INTERNAL_API_TOKEN="+sec.Token, "API_HOST=127.0.0.1", "API_PORT="+apiPort,
		"INTERNAL_STATS_HOST=127.0.0.1", "INTERNAL_STATS_PORT="+stratumInt,
		"RPC_USER=forge", "RPC_PASSWORD="+sec.BCH2Pass, "HOME_APP=1",
		// Windows cannot signal it, so closing its stdin is how it is asked to stop cleanly: it
		// then disconnects its miners and sends the pool the TIDES shares it still holds.
		"FORGE_STOP_ON_STDIN_EOF=1")
	c.Stdout, c.Stderr = serviceLog("stratum"), serviceLog("stratum")
	w, err := c.StdinPipe()
	if err != nil {
		return err
	}
	return runPiped("stratum", c, w)
}

func startAPI() error {
	c := hidden("api.exe")
	c.Dir = dataDir // run from the data folder, not the install folder
	c.Env = append(append(os.Environ(), dbEnv()...),
		"RPC_URL=http://127.0.0.1:"+bch2RPC, "RPC_USER=forge", "RPC_PASSWORD="+sec.BCH2Pass,
		"STRATUM_INTERNAL_URL=http://127.0.0.1:"+stratumInt, "INTERNAL_API_TOKEN="+sec.Token,
		// API_LISTEN_HOST keeps the API on this PC: it has no login of its own (HOME_APP) and the
		// dashboard server reaches it on 127.0.0.1. Unset, it listened on every interface.
		"API_HOST=127.0.0.1", "API_PORT="+apiPort, "API_LISTEN_HOST=127.0.0.1", "API_LISTEN_PORT="+apiPort, "HOME_APP=1", "CORS_ORIGINS=",
		"AUX1175_URL=http://127.0.0.1:"+aux1175RPC, "AUX1175_USER=forge1175", "AUX1175_PASSWORD="+sec.AuxPass,
		"SETTINGS_PASSWORD="+sec.Settings, "FORGE_PLATFORM=windows")
	c.Stdout, c.Stderr = serviceLog("api"), serviceLog("api")
	return run("api", c)
}

// restarting is set while Restart Mining runs: a second click meanwhile does nothing.
var restarting atomic.Bool

// minerDue is set once boot comes to start the miner, after the nodes' RPC.
var minerDue atomic.Bool

// minerStart is boot's start of the miner while it runs (the tests wait for it).
var minerStart sync.WaitGroup

// restartMiner is Restart Mining: the miner is stopped cleanly and started again, or, when it is not
// running (its program could not start), started at once. Before boot comes to the miner it does
// nothing: the miner would wait on a node still starting.
func restartMiner() {
	if !restarting.CompareAndSwap(false, true) {
		return
	}
	defer restarting.Store(false)
	if started("stratum") {
		logf("restarting the miner")
		status("Forge Solo: restarting the miner…")
		stopGracefully("stratum", stratumStopGrace)
		time.Sleep(2 * time.Second)
	} else if minerDue.Load() {
		logf("starting the miner, which is not running")
	} else {
		return
	}
	if startOrKeepTrying("stratum") {
		showRunning()
	}
}

// boot starts everything. Quit may come at any point of it: from then on nothing more starts, and
// boot stops where it is.
func boot() {
	status("Forge Solo: preparing…")
	stopLeftovers()
	if err := checkPublicPorts(); err != nil {
		logf("Forge Solo cannot start: %v", err)
		if err.reserved {
			startFailed(failTip(err.why(), reservedTip))
		} else {
			startFailed(failTip(err.why(), closeItTip))
		}
		return
	}
	writeConfigs()
	status("Forge Solo: starting the database…")
	if !startPostgres() {
		if isStopping() {
			return
		}
		logf("the database did not start (see pglog.txt)")
		startFailed(failTip("the database did not start (see launcher.log and pglog.txt)", tryAgainTip))
		return
	}
	logf("database started")
	watchDatabase()
	status("Forge Solo: starting the nodes (the first sync can take a while)…")
	startNodes()
	if isStopping() {
		return
	}

	// The API + dashboard don't need the node's RPC to start (handlers call it lazily and
	// report "offline"/"syncing" on their own), so bring them up right away. The browser
	// opens in seconds and the dashboard's status banner shows live sync progress, instead
	// of the whole UI waiting on the nodes first. An API that cannot start is tried again in
	// the background, and the dashboard and the miner start all the same.
	if startOrKeepTrying("api") {
		waitTCP("127.0.0.1:"+apiPort, 60*time.Second)
	}
	if isStopping() {
		return
	}
	openDashboard()

	// Start the miner once the node RPC is answering (stratum needs block templates).
	minerStart.Add(1)
	go func() {
		defer minerStart.Done()
		waitTCP("127.0.0.1:"+bch2RPC, 600*time.Second)
		waitTCP("127.0.0.1:"+aux1175RPC, 120*time.Second) // best-effort (merge-mining)
		if isStopping() {
			return
		}
		minerDue.Store(true)
		if startOrKeepTrying("stratum") {
			logf("miner started")
		}
		showRunning()
	}()
}

// dashboardOpen is set once the dashboard is served, on dashboard.
var (
	dashboardOpen atomic.Bool
	dashboard     net.Listener
)

// openDashboard serves the dashboard and opens it in the browser. Its port is fixed, so another
// program may hold it: the dashboard then cannot open, the browser would show that program, and
// the tray says so instead, with Try Again. Mining goes on.
func openDashboard() {
	l, err := listenExclusive("tcp", "127.0.0.1:"+webPort)
	if err != nil {
		logf("the dashboard cannot open: another program uses port %s (%v)", webPort, err)
		status("Forge Solo: another program uses port " + webPort + ", so the dashboard cannot open." + closeItTip)
		offerTryAgain(retryDashboard)
		return
	}
	dashboard = l
	dashboardOpen.Store(true)
	go serveDashboard(l)
	if !showTrouble() {
		status("Forge Solo: set your payout address in the dashboard")
		showRunning() // opened by Try Again, with everything else running already
	}
	openBrowser("http://127.0.0.1:" + webPort)
}

// rpcStop asks a node to shut down via its RPC `stop` method so it FLUSHES the chainstate to
// disk before exiting. A hard kill loses the in-memory (dbcache) chainstate on this small chain
// and forces a full resync on the next launch.
func rpcStop(port, user, pass string, timeout time.Duration) {
	req, err := http.NewRequest("POST", "http://127.0.0.1:"+port+"/",
		strings.NewReader(`{"jsonrpc":"1.0","id":"quit","method":"stop","params":[]}`))
	if err != nil {
		return
	}
	req.SetBasicAuth(user, pass)
	c := &http.Client{Timeout: timeout}
	if resp, err := c.Do(req); err == nil {
		_ = resp.Body.Close()
	}
}

// askToStop asks a node to stop, again every second until exited reports it gone or grace runs
// out, and reports whether it stopped. A node still loading its blocks refuses every request, stop
// among them, until it is ready; asked once, it was killed at the end of its grace instead.
func askToStop(port, user, pass string, grace time.Duration, exited func(time.Duration) bool) bool {
	deadline := time.Now().Add(grace)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return exited(0)
		}
		rpcStop(port, user, pass, min(5*time.Second, left))
		if exited(min(time.Second, max(time.Until(deadline), 0))) {
			return true
		}
	}
}

// started reports whether this launcher started the process under key and has not stopped it.
func started(key string) bool {
	mu.Lock()
	defer mu.Unlock()
	return procs[key] != nil
}

// stratumStopGrace is how long the stratum gets to stop cleanly once asked: 2 s for connected
// miners (both ports at once), up to 15 s for shares and a block still being processed or
// submitted (only when there is one), then sending the pool its queued TIDES shares (5 s per
// request). Usually it takes 2-3 s.
const stratumStopGrace = 30 * time.Second

// stopGracefully asks a process to stop by closing its stdin, waits up to grace for it to exit,
// and kills it only if it has not.
func stopGracefully(key string, grace time.Duration) {
	mu.Lock()
	w := stdins[key]
	delete(stdins, key)
	mu.Unlock()
	c, done := untrack(key) // before it is asked: its exit is the launcher's doing
	if w != nil {
		start := time.Now()
		_ = w.Close()
		if waitDone(done, grace) {
			logf("%s stopped in %v", key, time.Since(start).Round(time.Millisecond))
		} else {
			logf("%s did not stop in %v: killed", key, grace)
		}
	}
	kill(c, done)
}

// stopOnce runs the stop once: Quit and Windows ending the session can both ask for it.
var stopOnce sync.Once

// sessionEnding is set when Windows is ending the session, which leaves Forge Solo about 5 s.
var sessionEnding atomic.Bool

// stopDone is closed when everything has stopped, just before the process exits.
var stopDone = make(chan struct{})

// shutdown stops everything cleanly and exits. The tray icon stays, showing the stop, until
// everything has stopped: gone at once, as the tray takes it on Quit, it looked as if Forge Solo
// had exited, and a start meanwhile found it still running and only opened its dashboard. A second
// call waits for the first, which exits.
func shutdown() {
	stopOnce.Do(func() {
		stopForExit()
		close(stopDone)
		if relaunchAfterSessionStop() {
			// Windows asked to end the session, and then did not: someone cancelled the shutdown or
			// the restart that another program held up. Left stopped, the PC would not mine again
			// until someone started Forge Solo.
			logf("Windows did not end the session after all: starting Forge Solo again")
			releaseRunning()
			exe, _ := os.Executable()
			if err := relaunch(exe); err != nil {
				logf("could not start Forge Solo again: %v", err)
			}
		}
		systray.Quit() // removes the icon
		os.Exit(0)
	})
}

// sessionOutcome carries Windows's answer, after it asked to end the session: whether it ends.
var sessionOutcome = make(chan bool, 1)

// sessionOutcomeWait is how long the answer may take: Windows waits on the person at the screen
// when another program holds up the shutdown (shorter in the tests).
var sessionOutcomeWait = 15 * time.Minute

// noteSessionOutcome passes on Windows's answer, if Windows asked first: an answer to a question
// Forge Solo was never asked (another program refused it before) must not be kept for a later one.
func noteSessionOutcome(ends bool) {
	if !sessionEnding.Load() {
		return
	}
	select {
	case sessionOutcome <- ends:
	default:
	}
}

// relaunchAfterSessionStop reports, after the stop Windows asked for, whether Forge Solo must start
// again: when Windows then did not end the session, or never said.
func relaunchAfterSessionStop() bool {
	if !sessionEnding.Load() {
		return false
	}
	select {
	case ends := <-sessionOutcome:
		return !ends
	case <-time.After(sessionOutcomeWait):
		logf("Windows did not say in %v whether the session ends", sessionOutcomeWait)
		return true
	}
}

// stopForExit stops everything for the exit that follows. Nothing starts once it has begun: boot
// may still be starting things, and one started after the stop had passed it was left running.
func stopForExit() {
	mu.Lock()
	stopping = true
	mu.Unlock()
	if sessionEnding.Load() {
		// No one sees the tooltip while Windows ends the session, and the taskbar can be slow to
		// answer then: the nodes need those seconds more.
		go showStopping()
	} else {
		showStopping()
	}
	start := time.Now()
	stopEverything()
	logf("everything stopped in %v", time.Since(start).Round(time.Millisecond))
}

// waitStopped waits up to max for the stop under way to finish, and reports whether it did.
func waitStopped(max time.Duration) bool {
	select {
	case <-stopDone:
		return true
	case <-time.After(max):
		return false
	}
}

// stopEverything is the stop for Quit, or, everything at once, for a closing Windows session.
func stopEverything() {
	if sessionEnding.Load() {
		stopAllNow()
	} else {
		stopAll()
	}
}

// stopAll stops the miner first, since a block it is still submitting needs the BCH2 node, and
// the API with it; then both nodes at once; then the database.
func stopAll() {
	logf("stopping: the miner first, then the nodes, then the database")
	stopGracefully("stratum", stratumStopGrace)
	stop("api")
	stopNodes()
	stopDatabase()
}

// stopAllNow stops everything at once, for a closing Windows session: the nodes are asked to write
// their chainstate and stop while the miner disconnects and the database closes. A block the miner
// is still submitting may then miss the node, which matters less than a node killed mid-write.
func stopAllNow() {
	var wg sync.WaitGroup
	for _, f := range []func(){stopNodes, func() { stopGracefully("stratum", stratumStopGrace) }, func() { stop("api") }, stopDatabase} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}
	wg.Wait()
}

// pgSIGINT asks PostgreSQL for a fast shutdown: open connections are ended, nothing is lost. It is
// what pg_ctl -m fast sends; SIGINT is 2 in the Windows C runtime.
const pgSIGINT = 2

// signalPostgres sends the database server a signal (a stand-in in the tests).
var signalPostgres = signalPostgresOS

// postmasterPID is the running database server's process id, the first line of postmaster.pid, or
// 0 when there is none.
func postmasterPID() int {
	b, err := os.ReadFile(dpath("pgdata", "postmaster.pid"))
	if err != nil {
		return 0
	}
	first, _ := cut(string(b), '\n')
	pid, _ := strconv.Atoi(strings.TrimSpace(first))
	return pid
}

// stopDatabase stops PostgreSQL the way pg_ctl -m fast does, signalling the server and waiting for
// its postmaster.pid to go, but without starting pg_ctl: once Windows is ending the session it
// starts no new program (they fail with 0xC0000142), and the database was then killed instead.
func stopDatabase() {
	// A start still under way may not have written postmaster.pid yet, or the file may still name
	// the server before it, which crashed: wait for the new server, or for the start to end.
	pid := postmasterPID()
	for wait := time.Now(); dbStarting.Load() && time.Since(wait) < 10*time.Second; pid = postmasterPID() {
		if pid != 0 && runs(installedPrograms(), pid, "postgres.exe") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pid == 0 {
		logf("database: not running")
		return
	}
	// postmaster.pid outlives a server that crashed, and its number may since be another program's.
	if !runs(installedPrograms(), pid, "postgres.exe") {
		logf("database: postmaster.pid names process %d, which is not this install's database", pid)
		return
	}
	start := time.Now()
	if err := signalPostgres(pid, pgSIGINT); err != nil {
		logf("database stop: %v", err)
		return
	}
	for postmasterPID() != 0 {
		if time.Since(start) > 30*time.Second {
			logf("database did not stop in 30 s")
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	logf("database stopped in %v", time.Since(start).Round(time.Millisecond))
}

// Each node may take this long to stop once asked, before it is killed.
var bch2StopGrace, auxStopGrace = 45 * time.Second, 20 * time.Second

// nodeInfo is one node: its key in procs, its program, its RPC port and login, the window that
// port is picked from, and how long it may take to stop.
type nodeInfo struct {
	key, exe, port, user, pass string
	from                       int
	grace                      time.Duration
}

func nodes() []nodeInfo {
	return []nodeInfo{
		{"bch2", "bitcoincashiid.exe", bch2RPC, "forge", sec.BCH2Pass, windowOf(&bch2RPC), bch2StopGrace},
		{"aux1175", "elevenseventyfived.exe", aux1175RPC, "forge1175", sec.AuxPass, windowOf(&aux1175RPC), auxStopGrace},
	}
}

// windowOf is the first port of the window the port is picked from.
func windowOf(port *string) int {
	for _, s := range portPlan {
		if s.port == port {
			return s.from
		}
	}
	return 0
}

// stopNodes flushes + stops the nodes gracefully so the next launch RESUMES instead of resyncing,
// and kills one only if it ignored its grace. Both at once: one waiting on the other only added to
// the time a closing Windows session has to give. Only a node this launcher started is asked:
// otherwise the port may be another program's, and the request carries the node's password.
func stopNodes() {
	var wg sync.WaitGroup
	for _, n := range nodes() {
		c, done := untrack(n.key) // before it is asked: its exit is the launcher's doing
		if c == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			if askToStop(n.port, n.user, n.pass, n.grace, func(d time.Duration) bool { return waitDone(done, d) }) {
				logf("%s node stopped in %v", n.key, time.Since(start).Round(time.Millisecond))
			} else {
				logf("%s node did not stop in %v: killed", n.key, n.grace)
			}
			kill(c, done)
		}()
	}
	wg.Wait()
}

// small helpers to avoid extra imports
func splitLines(s string) []string {
	var out, cur = []string{}, ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
		} else if r != '\r' {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
func cut(s string, sep byte) (string, string) {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}
