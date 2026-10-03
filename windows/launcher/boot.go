package main

import (
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

func pgbin(name string) string { return ipath("pgsql", "bin", name) }

func setupSecrets() {
	f := dpath("secrets.env")
	if b, err := os.ReadFile(f); err == nil {
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
			}
		}
	}
	if sec.BCH2Pass == "" {
		sec = secrets{BCH2Pass: gen(), AuxPass: gen(), DBPass: gen(), Token: gen()}
		_ = os.WriteFile(f, []byte("BCH2="+sec.BCH2Pass+"\nAUX="+sec.AuxPass+"\nDB="+sec.DBPass+"\nTOKEN="+sec.Token+"\n"), 0o600)
	}
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
	// The BCH2 node is not pruned. Pruning saved nothing -- the whole chain is about 90 MB --
	// but it made the node announce NODE_NETWORK_LIMITED instead of NODE_NETWORK, and the DNS
	// seeders list only NODE_NETWORK nodes, so it waited on chance for inbound peers however
	// the router was forwarded. Earlier releases wrote prune=2000, but Core prunes nothing
	// below height 200,000, so no datadir was ever actually pruned and each one starts
	// unpruned as it is.
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
			"zmqpubhashblock=tcp://127.0.0.1:"+bch2ZMQ+"\ndnsseed=1\n")
	writeAlways(dpath("elevenseventyfive", "1175.conf"),
		"server=1\nlisten=1\nrpcbind=127.0.0.1\nrpcallowip=127.0.0.1\nrpcport="+aux1175RPC+
			"\nrpcuser=forge1175\nrpcpassword="+sec.AuxPass+"\nport="+aux1175P2P+"\nprune=2000\n"+
			"upnp=0\nnatpmp=0\ndiscover=1\ndbcache=100\nmaxmempool=50\nmaxsigcachesize=4\npar=1\nmaxconnections=40\ndnsseed=1\n"+
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
# NiceHash / MiningRigRentals put a whole order behind one connection on 3335. Off here:
# the installer opens no rule for that port, so a home box would bind what nothing reaches.
stratum_rental:
  enabled: false
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

func startPostgres() bool {
	pgdata := dpath("pgdata")
	if _, err := os.Stat(filepath.Join(pgdata, "PG_VERSION")); os.IsNotExist(err) {
		md(pgdata)
		pwf := dpath("pgpw.txt")
		_ = os.WriteFile(pwf, []byte(sec.DBPass), 0o600)
		init := hiddenPrio(belowNormal, "pgsql\\bin\\initdb.exe", "-D", pgdata, "-U", "forge", "-A", "scram-sha-256",
			"--pwfile", pwf, "-E", "UTF8", "--no-locale")
		_ = init.Run()
		_ = os.Remove(pwf)
	}
	log := dpath("pglog.txt")
	rotateLog(log, 10<<20)
	// pg_ctl -w succeeds only once the server it started is ready, which it is only after taking
	// 127.0.0.1:pgPort itself. Something else answering on the port is not the database, and the
	// services would hand it the database password (lib/pq sends it in the clear when asked).
	// -t 300: a server recovering from a hard stop can take longer than the default 60 s.
	pgctl := hiddenPrio(belowNormal, "pgsql\\bin\\pg_ctl.exe", "-D", pgdata, "-l", log, "-o", "-p "+pgPort+" -h 127.0.0.1", "-w", "-t", "300", "start")
	if pgctl.Run() != nil {
		return false
	}
	env := append(os.Environ(), "PGPASSWORD="+sec.DBPass)
	// create the database (ignore "already exists")
	cdb := hidden("pgsql\\bin\\createdb.exe", "-h", "127.0.0.1", "-p", pgPort, "-U", "forge", "forgesolo")
	cdb.Env = env
	_ = cdb.Run()
	// load the schema (idempotent; init-db.sql uses IF NOT EXISTS)
	psql := hidden("pgsql\\bin\\psql.exe", "-h", "127.0.0.1", "-p", pgPort, "-U", "forge", "-d", "forgesolo", "-f", ipath("init-db.sql"))
	psql.Env = env
	_ = psql.Run()
	return true
}

func dbEnv() []string {
	return []string{
		"DB_HOST=127.0.0.1", "DB_PORT=" + pgPort, "DB_USER=forge",
		"DB_PASSWORD=" + sec.DBPass, "DB_NAME=forgesolo", "DB_SSLMODE=disable",
	}
}

func startNodes() {
	_ = run("bch2", hiddenPrio(belowNormal, "bitcoincashIId.exe", "-datadir="+dpath("bch2"), "-conf="+dpath("bch2", "bch2.conf")))
	_ = run("aux1175", hiddenPrio(belowNormal, "elevenseventyfived.exe", "-datadir="+dpath("elevenseventyfive"), "-conf="+dpath("elevenseventyfive", "1175.conf")))
}

func startStratum() {
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
	if w, err := c.StdinPipe(); err == nil {
		mu.Lock()
		stdins["stratum"] = w
		mu.Unlock()
	}
	_ = run("stratum", c)
}

func startAPI() {
	c := hidden("api.exe")
	c.Dir = dataDir // run from the data folder, not the install folder
	c.Env = append(append(os.Environ(), dbEnv()...),
		"RPC_URL=http://127.0.0.1:"+bch2RPC, "RPC_USER=forge", "RPC_PASSWORD="+sec.BCH2Pass,
		"STRATUM_INTERNAL_URL=http://127.0.0.1:"+stratumInt, "INTERNAL_API_TOKEN="+sec.Token,
		// API_LISTEN_HOST keeps the API on this PC: it has no login of its own (HOME_APP) and the
		// dashboard server reaches it on 127.0.0.1. Unset, it listened on every interface.
		"API_HOST=127.0.0.1", "API_PORT="+apiPort, "API_LISTEN_HOST=127.0.0.1", "API_LISTEN_PORT="+apiPort, "HOME_APP=1", "CORS_ORIGINS=",
		"AUX1175_URL=http://127.0.0.1:"+aux1175RPC, "AUX1175_USER=forge1175", "AUX1175_PASSWORD="+sec.AuxPass)
	_ = run("api", c)
}

func restartMiner() {
	// Until boot has started the miner (or when it could not), there is none to restart, and
	// starting one here would leave two.
	if !started("stratum") {
		return
	}
	logf("restarting the miner")
	systray.SetTooltip("Forge Solo: restarting the miner…")
	stopGracefully("stratum", stratumStopGrace)
	time.Sleep(2 * time.Second)
	startStratum()
	systray.SetTooltip("Forge Solo: running")
}

func boot() {
	systray.SetTooltip("Forge Solo: preparing…")
	setupSecrets()
	writeConfigs()
	systray.SetTooltip("Forge Solo: starting the database…")
	if !startPostgres() {
		logf("the database did not start (see pglog.txt)")
		systray.SetTooltip("Forge Solo: the database did not start (see pglog.txt in the data folder)")
		return
	}
	logf("database started")
	systray.SetTooltip("Forge Solo: starting the nodes (the first sync can take a while)…")
	startNodes()
	logf("nodes started")

	// The API + dashboard don't need the node's RPC to start (handlers call it lazily and
	// report "offline"/"syncing" on their own), so bring them up right away. The browser
	// opens in seconds and the dashboard's status banner shows live sync progress, instead
	// of the whole UI waiting on the nodes first.
	startAPI()
	waitTCP("127.0.0.1:"+apiPort, 60*time.Second)
	go serveDashboard()
	waitTCP("127.0.0.1:"+webPort, 20*time.Second)
	systray.SetTooltip("Forge Solo: set your payout address in the dashboard")
	openBrowser("http://127.0.0.1:" + webPort)

	// Start the miner once the node RPC is answering (stratum needs block templates).
	go func() {
		waitTCP("127.0.0.1:"+bch2RPC, 600*time.Second)
		waitTCP("127.0.0.1:"+aux1175RPC, 120*time.Second) // best-effort (merge-mining)
		startStratum()
		logf("miner started")
		systray.SetTooltip("Forge Solo: running")
	}()
}

// rpcStop asks a node to shut down via its RPC `stop` method so it FLUSHES the chainstate to
// disk before exiting. A hard kill loses the in-memory (dbcache) chainstate on this small chain
// and forces a full resync on the next launch.
func rpcStop(port, user, pass string) {
	req, err := http.NewRequest("POST", "http://127.0.0.1:"+port+"/",
		strings.NewReader(`{"jsonrpc":"1.0","id":"quit","method":"stop","params":[]}`))
	if err != nil {
		return
	}
	req.SetBasicAuth(user, pass)
	c := &http.Client{Timeout: 5 * time.Second}
	if resp, err := c.Do(req); err == nil {
		_ = resp.Body.Close()
	}
}

// started reports whether this launcher started the process under key and has not stopped it.
func started(key string) bool {
	mu.Lock()
	defer mu.Unlock()
	return procs[key] != nil
}

// waitProcExit blocks until the tracked process exits, or the timeout elapses, and reports whether
// it exited (true too when there is no such process).
func waitProcExit(key string, timeout time.Duration) bool {
	mu.Lock()
	c := procs[key]
	mu.Unlock()
	if c == nil || c.Process == nil {
		return true
	}
	done := make(chan struct{})
	go func() { _, _ = c.Process.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
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
	if w != nil {
		start := time.Now()
		_ = w.Close()
		if waitProcExit(key, grace) {
			logf("%s stopped in %v", key, time.Since(start).Round(time.Millisecond))
		} else {
			logf("%s did not stop in %v: killed", key, grace)
		}
	}
	stop(key)
}

// stopOnce runs the stop once: Quit and Windows ending the session can both ask for it.
var stopOnce sync.Once

// sessionEnding is set when Windows is ending the session, which leaves Forge Solo about 5 s.
var sessionEnding atomic.Bool

// shutdown stops everything cleanly and exits. A second call waits for the first, which exits.
func shutdown() {
	stopOnce.Do(func() {
		systray.SetTooltip("Forge Solo: shutting down cleanly…")
		start := time.Now()
		stopEverything()
		logf("everything stopped in %v", time.Since(start).Round(time.Millisecond))
		os.Exit(0)
	})
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
	pid := postmasterPID()
	if pid == 0 {
		logf("database: not running")
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

// stopNodes flushes + stops the nodes gracefully so the next launch RESUMES instead of resyncing,
// and kills one only if it ignored its grace. Both at once: one waiting on the other only added to
// the time a closing Windows session has to give. Only a node this launcher started is asked:
// otherwise the port may be another program's, and the request carries the node's password.
func stopNodes() {
	var wg sync.WaitGroup
	for _, n := range []struct {
		key, port, user, pass string
		grace                 time.Duration
	}{
		{"bch2", bch2RPC, "forge", sec.BCH2Pass, 45 * time.Second},
		{"aux1175", aux1175RPC, "forge1175", sec.AuxPass, 20 * time.Second},
	} {
		if !started(n.key) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			rpcStop(n.port, n.user, n.pass)
			if waitProcExit(n.key, n.grace) {
				logf("%s node stopped in %v", n.key, time.Since(start).Round(time.Millisecond))
			} else {
				logf("%s node did not stop in %v: killed", n.key, n.grace)
			}
			stop(n.key)
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
