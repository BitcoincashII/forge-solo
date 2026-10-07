package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
// (another program may hold it a moment).
//
// DB= is the password of the PostgreSQL database 1.0.12 and before kept their data in. Only moving
// that data needs it, and going back to 1.0.12 needs it as it was: it is never replaced, and a
// missing one stops only a move that is needed (moveData), never a start. One is made only where
// there is no such database: 1.0.12, installed again, then keeps the other secrets and makes its
// database with it.
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
	for _, s := range []*string{&sec.BCH2Pass, &sec.AuxPass, &sec.Token} {
		if *s == "" {
			*s = gen()
			changed = true
		}
	}
	if sec.DBPass == "" {
		if _, err := os.Stat(dpath("pgdata", "PG_VERSION")); errors.Is(err, fs.ErrNotExist) {
			sec.DBPass = gen()
			changed = true
		}
	}
	// The Settings page's password: other programs and accounts on this PC can reach the
	// dashboard and its API, so a change needs it. Installs from 1.0.12 and before gain it here,
	// their other secrets unchanged.
	if sec.Settings == "" {
		sec.Settings = genHex(32)
		changed = true
	}
	if changed {
		content := "BCH2=" + sec.BCH2Pass + "\nAUX=" + sec.AuxPass + "\n"
		if sec.DBPass != "" {
			content += "DB=" + sec.DBPass + "\n"
		}
		content += "TOKEN=" + sec.Token + "\nSETTINGS=" + sec.Settings + "\n"
		// Written aside, on the disk, and only then moved into place: a crash or a power cut part-way
		// must not lose the old database's password.
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
    # MiningRigRentals wants a share every 10 to 60 s at the rig's advertised hashrate (its
    # "optimal difficulty"); 25 s keeps vardiff's spread inside that.
    target_time: 25
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
// its log, and nothing else trims it.
func rotateLog(path string, limit int64) {
	if st, err := os.Stat(path); err == nil && st.Size() > limit {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
}

// dbStarting is set while pg_ctl starts the old database for a move. Until the server has written
// postmaster.pid the stop cannot see it, and would leave it running.
var dbStarting atomic.Bool

// dbEnv is where the API and the miner keep their data: forgesolo.db in the data folder, one SQLite
// file both open, as on Umbrel and Linux.
func dbEnv() []string { return []string{"DB_PATH=" + dbPath()} }

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
		// INTERNAL_STATS_PORT is the stratum's own stats listener that api.exe polls via
		// STRATUM_INTERNAL_URL.
		"INTERNAL_API_TOKEN="+sec.Token,
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
		// API_LISTEN_HOST keeps the API on this PC: it has no login of its own and the dashboard
		// server reaches it on 127.0.0.1. Unset, it listened on every interface.
		"API_LISTEN_HOST=127.0.0.1", "API_LISTEN_PORT="+apiPort,
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
		status(tipRestartingMiner)
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
	status(tipPreparing)
	stopLeftovers()
	if err := checkPublicPorts(); err != nil {
		if err.reserved {
			logf("Forge Solo cannot start: %v; %s", err, tryAgainAdvice)
		} else {
			logf("Forge Solo cannot start: %v; %s", err, closeItAdvice)
		}
		startFailed(tipCannotStart(startWhy(err)))
		return
	}
	writeConfigs()
	prepareDatabase()
	if isStopping() {
		return
	}
	status(tipStartingNodes)
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
		logf("the dashboard cannot open: another program uses port %s (%v); %s", webPort, err, closeItAdvice)
		status(tipNoDashboard(webPort))
		offerTryAgain(retryDashboard)
		return
	}
	dashboard = l
	dashboardOpen.Store(true)
	go serveDashboard(l)
	openBrowser(dashboardURL())
	watchPayoutAddress()
	showRunning() // opened by Try Again, with everything else running already
}

// noPayoutAddress is set while the dashboard's API last said no payout address is set: the miner
// then mines nothing, and the tray asks for one instead of saying "running" (showRunning).
var noPayoutAddress atomic.Bool

// payoutPollEvery is how often the tray asks the dashboard's API whether a payout address is set,
// so that it says "running" soon after one is saved (shorter in the tests).
var payoutPollEvery = 10 * time.Second

// payoutAddressSaid is what the dashboard's API says of the payout address (a stand-in in the
// tests).
var payoutAddressSaid = askPayoutAddress

// payoutWatching is set while the watch of the payout address runs, which payoutWatch waits for.
var (
	payoutWatching atomic.Bool
	payoutWatch    sync.WaitGroup
)

// watchPayoutAddress starts, once, the watch of the payout address: the dashboard's API is asked at
// once, and then every payoutPollEvery until the stop begins, whether one is set, and the tray says
// so when that changes. It runs on its own, so that neither the start nor the tray menu waits on the
// API. An answer the API could not give changes nothing.
func watchPayoutAddress() {
	if !payoutWatching.CompareAndSwap(false, true) {
		return
	}
	payoutWatch.Add(1)
	go func() {
		defer payoutWatch.Done()
		defer payoutWatching.Store(false)
		for {
			if unset, known := payoutAddressSaid(); known && noPayoutAddress.Swap(unset) != unset {
				if unset {
					logf("no payout address is set: the tray asks for one")
				} else {
					logf("a payout address is set")
				}
				showRunning()
			}
			if pause(payoutPollEvery) {
				return
			}
		}
	}()
}

// payoutAskTimeout is how long one question to the API may take; the watch asks again later.
var payoutAskTimeout = 3 * time.Second

// askPayoutAddress asks the dashboard's API whether a payout address is set. unset is whether none
// is, and known whether the API could say: it answered, and could read the database.
func askPayoutAddress() (unset, known bool) {
	c := &http.Client{Timeout: payoutAskTimeout}
	resp, err := c.Get("http://127.0.0.1:" + apiPort + "/api/v1/pool/config")
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	var cfg struct {
		Configured *bool `json:"configured"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&cfg) != nil || cfg.Configured == nil {
		return false, false
	}
	return !*cfg.Configured, true
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
// the API with it; then both nodes at once. A move under way is ended, and the old database it
// started is stopped: nothing is put in place, and the next start moves the data.
func stopAll() {
	logf("stopping: the miner first, then the nodes")
	stopGracefully("stratum", stratumStopGrace)
	stop("api")
	stopNodes()
	stopMove()
}

// stopMove ends the migrator, then the old database a move started, if either runs.
func stopMove() {
	stop("migrate")
	stopDatabase()
}

// stopAllNow stops everything at once, for a closing Windows session: the nodes are asked to write
// their chainstate and stop while the miner disconnects and a move under way is ended. A block the
// miner is still submitting may then miss the node, which matters less than a node killed mid-write.
func stopAllNow() {
	var wg sync.WaitGroup
	for _, f := range []func(){stopNodes, func() { stopGracefully("stratum", stratumStopGrace) }, func() { stop("api") }, stopMove} {
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

// stopDatabase stops the old database a move started the way pg_ctl -m fast does, signalling the
// server and waiting for its postmaster.pid to go, but without starting pg_ctl: once Windows is
// ending the session it starts no new program (they fail with 0xC0000142), and the database was
// then killed instead. It reports whether no server is left on the old data: postmaster.pid gone.
func stopDatabase() bool {
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
		return true
	}
	// postmaster.pid outlives a server that crashed, and its number may since be another program's.
	if !runs(installedPrograms(), pid, "postgres.exe") {
		logf("old database: postmaster.pid names process %d, which is not this install's database", pid)
		return false
	}
	start := time.Now()
	if err := signalPostgres(pid, pgSIGINT); err != nil {
		logf("old database stop: %v", err)
		return false
	}
	for postmasterPID() != 0 {
		if time.Since(start) > 30*time.Second {
			logf("old database did not stop in 30 s")
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	logf("old database stopped in %v", time.Since(start).Round(time.Millisecond))
	return true
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
