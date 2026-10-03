package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	c.Dir = dataDir // so its relative config.yaml + the dashboard's edits share one file
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
	systray.SetTooltip("Forge Solo — restarting miner…")
	stopGracefully("stratum", stratumStopGrace)
	time.Sleep(2 * time.Second)
	startStratum()
	systray.SetTooltip("Forge Solo — mining")
}

func boot() {
	systray.SetTooltip("Forge Solo — preparing…")
	setupSecrets()
	writeConfigs()
	systray.SetTooltip("Forge Solo — starting database…")
	if !startPostgres() {
		systray.SetTooltip("Forge Solo — DATABASE FAILED (see Data Folder\\pglog.txt)")
		return
	}
	systray.SetTooltip("Forge Solo — starting nodes (first sync can take a while)…")
	startNodes()

	// The API + dashboard don't need the node's RPC to start (handlers call it lazily and
	// report "offline"/"syncing" on their own), so bring them up right away. The browser
	// opens in seconds and the dashboard's status banner shows live sync progress, instead
	// of the whole UI waiting on the nodes first.
	startAPI()
	waitTCP("127.0.0.1:"+apiPort, 60*time.Second)
	go serveDashboard()
	waitTCP("127.0.0.1:"+webPort, 20*time.Second)
	systray.SetTooltip("Forge Solo — set your payout address in the dashboard")
	openBrowser("http://127.0.0.1:" + webPort)

	// Start the miner once the node RPC is answering (stratum needs block templates).
	go func() {
		waitTCP("127.0.0.1:"+bch2RPC, 600*time.Second)
		waitTCP("127.0.0.1:"+aux1175RPC, 120*time.Second) // best-effort (merge-mining)
		startStratum()
		systray.SetTooltip("Forge Solo — running")
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

// waitProcExit blocks until the tracked process exits, or the timeout elapses.
func waitProcExit(key string, timeout time.Duration) {
	mu.Lock()
	c := procs[key]
	mu.Unlock()
	if c == nil || c.Process == nil {
		return
	}
	done := make(chan struct{})
	go func() { _, _ = c.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
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
		_ = w.Close()
		waitProcExit(key, grace)
	}
	stop(key)
}

func shutdown() {
	systray.SetTooltip("Forge Solo — shutting down cleanly…")
	stopGracefully("stratum", stratumStopGrace) // miner + api first (they talk to the nodes)
	stop("api")
	stopNodes()
	pgctl := hidden("pgsql\\bin\\pg_ctl.exe", "-D", dpath("pgdata"), "-m", "fast", "stop")
	_ = pgctl.Run()
	os.Exit(0)
}

// stopNodes flushes + stops the nodes gracefully so the next launch RESUMES instead of resyncing,
// and kills one only if it ignored its grace. Only a node this launcher started is asked: otherwise
// the port may be another program's, and the request carries the node's password.
func stopNodes() {
	if started("bch2") {
		rpcStop(bch2RPC, "forge", sec.BCH2Pass)
	}
	if started("aux1175") {
		rpcStop(aux1175RPC, "forge1175", sec.AuxPass)
	}
	waitProcExit("bch2", 45*time.Second)
	waitProcExit("aux1175", 20*time.Second)
	stop("bch2")
	stop("aux1175")
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
