package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/migstatus"
	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/netlisten"
	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	fiberrecover "github.com/gofiber/fiber/v2/middleware/recover"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

var (
	startTime          = time.Now()
	minerSettings      = make(map[string]MinerSetting)
	settingsLastChange = make(map[string]time.Time)
	settingsMu         sync.RWMutex

	rpcURL           string
	rpcUser          string
	rpcPass          string
	stratumURL       string
	internalAPIToken string
	settingsPassword string                // SETTINGS_PASSWORD (settingsPasswordGateFromEnv): a settings change must carry it
	webRoot          string = "./web/dist" // Web UI root directory, configurable via WEB_ROOT env
	halvingInterval  int64  = 210000       // BCH2 halving interval, configurable via HALVING_INTERVAL env

	// Internal HTTP client with timeout to prevent cascading failures
	internalHTTPClient = &http.Client{Timeout: 10 * time.Second}
)

// CachedBlock stores block data with cache timestamp
type CachedBlock struct {
	Height   int64     `json:"height"`
	Hash     string    `json:"hash"`
	Time     int64     `json:"time"`
	Size     int       `json:"size"`
	TxCount  int       `json:"txCount"`
	CachedAt time.Time `json:"-"`
}

// CashAddr charset for BCH addresses
const cashAddrCharset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

// cashAddrPolymod computes the BCH checksum polymod
func cashAddrPolymod(values []int) uint64 {
	c := uint64(1)
	for _, d := range values {
		c0 := c >> 35
		c = ((c & 0x07ffffffff) << 5) ^ uint64(d)
		if c0&0x01 != 0 {
			c ^= 0x98f2bc8e61
		}
		if c0&0x02 != 0 {
			c ^= 0x79b76d99e2
		}
		if c0&0x04 != 0 {
			c ^= 0xf33e5fb3c4
		}
		if c0&0x08 != 0 {
			c ^= 0xae2eabe2a8
		}
		if c0&0x10 != 0 {
			c ^= 0x1e4f43e470
		}
	}
	return c ^ 1
}

// prefixToValues converts a CashAddr prefix to 5-bit values for checksum
func prefixToValues(prefix string) []int {
	values := make([]int, len(prefix)+1)
	for i, c := range prefix {
		values[i] = int(c) & 0x1f
	}
	values[len(prefix)] = 0 // separator
	return values
}

// isValid1175Address validates a 1175 payout address: a bech32 address with the
// mainnet HRP "esf" and a valid checksum (esf1...). This is the address a miner
// supplies to receive merge-mined 1175 rewards.
// poolNameFromEnv honours POOL_NAME, which .env.example has always advertised and which
// nothing read: it is not substituted into the stratum config template either, so setting
// it in the Umbrel app config changed nothing anywhere.
func poolNameFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("POOL_NAME")); v != "" {
		return v
	}
	return "Forge Solo"
}

func isValid1175Address(address string) bool {
	address = strings.TrimSpace(address)
	if address == "" {
		return false
	}
	hrp, data, err := bech32.Decode(address)
	if err != nil || hrp != "esf" || len(data) == 0 {
		return false
	}
	return true
}

// isValidBCH2Address validates a BCH2 mainnet payout address exactly the way the
// stratum resolves it, so the API never accepts an address the stratum then silently
// rejects (which would leave mining paused while the dashboard reports success). The
// address MUST carry the explicit BCH2 mainnet prefix "bitcoincashii:", its CashAddr
// checksum must verify against THAT prefix, and it must decode to a P2PKH (type 0)
// 20-byte hash -- mirroring mining.parseAddressToPubkeyHash and the node's validateaddress
// P2PKH requirement.
//
// FAIL-CLOSED and mainnet-only: a missing/other prefix, a bad checksum, P2SH (p.../type 1),
// legacy 1.../3..., prefix-less input, and every testnet/regtest prefix (bchtest/bchreg) are
// rejected. A testnet address shares its 20-byte hash with a mainnet address the user may
// not control, so accepting it would mine the reward to the wrong place.
func isValidBCH2Address(address string) bool {
	const prefix = "bitcoincashii"
	address = strings.ToLower(strings.TrimSpace(address))
	if len(address) <= len(prefix)+1 || address[:len(prefix)+1] != prefix+":" {
		return false
	}
	addr := address[len(prefix)+1:]

	// Decode the CashAddr payload to 5-bit symbols.
	data := make([]int, 0, len(addr))
	for _, ch := range addr {
		idx := strings.IndexRune(cashAddrCharset, ch)
		if idx < 0 {
			return false // invalid character
		}
		data = append(data, idx)
	}
	if len(data) < 8 {
		return false
	}

	// Verify the checksum against the bitcoincashii prefix. This rejects typos and any
	// address whose checksum was computed for a different prefix (bitcoincash:/bchtest:).
	chk := append(prefixToValues(prefix), data...)
	if cashAddrPolymod(chk) != 0 {
		return false
	}

	// Drop the 8-symbol (40-bit) checksum and convert the 5-bit payload to bytes.
	payload := data[:len(data)-8]
	var result []byte
	acc, bits := 0, 0
	for _, d := range payload {
		acc = (acc << 5) | d
		bits += 5
		for bits >= 8 {
			bits -= 8
			result = append(result, byte(acc>>bits))
			acc &= (1 << bits) - 1
		}
	}

	// version byte: bit7 reserved (0); bits6..3 = type; bits2..0 = size. Require type 0
	// (P2PKH) and a 20-byte (160-bit) hash. Rejects P2SH (type 1) and any other type.
	if len(result) != 21 {
		return false
	}
	version := result[0]
	if version&0x80 != 0 || (version>>3)&0x1f != 0 {
		return false
	}
	return true
}

// normalizeAddress strips the prefix from a BCH2 address for comparison
func normalizeAddress(address string) string {
	// Lowercase for consistent comparison (stratum stores addresses lowercase)
	address = strings.ToLower(address)
	// Strip any known prefix to get the bare hash
	prefixes := []string{"bitcoincashii:", "bitcoincash:", "bchtest:"}
	for _, prefix := range prefixes {
		if len(address) > len(prefix) && address[:len(prefix)] == prefix {
			// Always return with canonical bitcoincashii: prefix
			return "bitcoincashii:" + address[len(prefix):]
		}
	}
	// Bare hash (q... or p...) - add canonical prefix
	if len(address) >= 42 && (address[0] == 'q' || address[0] == 'p') {
		return "bitcoincashii:" + address
	}
	return address
}

// addressMatches compares two addresses, ignoring prefix differences
func addressMatches(a, b string) bool {
	return normalizeAddress(a) == normalizeAddress(b)
}

func init() {
	// Load configuration from environment variables
	rpcURL = os.Getenv("RPC_URL")
	if rpcURL == "" {
		rpcURL = "http://127.0.0.1:8342"
	}
	rpcUser = os.Getenv("RPC_USER")
	if rpcUser == "" {
		rpcUser = os.Getenv("FORGE_RPC_USER")
	}
	rpcPass = os.Getenv("RPC_PASSWORD")
	if rpcPass == "" {
		rpcPass = os.Getenv("FORGE_RPC_PASSWORD")
	}
	stratumURL = os.Getenv("STRATUM_INTERNAL_URL")
	if stratumURL == "" {
		stratumURL = "http://127.0.0.1:3337"
	}
	internalAPIToken = os.Getenv("INTERNAL_API_TOKEN")
	// Web root directory (default ./web/dist for Docker, ./web for Windows)
	if envWebRoot := os.Getenv("WEB_ROOT"); envWebRoot != "" {
		webRoot = envWebRoot
	}
	// BCH2 halving interval (default 210000, same as Bitcoin/BCH)
	if envHalving := os.Getenv("HALVING_INTERVAL"); envHalving != "" {
		if h, err := strconv.ParseInt(envHalving, 10, 64); err == nil && h > 0 {
			halvingInterval = h
		}
	}
}

type MinerSetting struct {
	Address     string  `json:"address"`
	SoloMining  bool    `json:"solo_mining"`
	ManualDiff  float64 `json:"manual_diff"`
	Password    string  `json:"password"`
	Address1175 string  `json:"address_1175"` // 1175 merge-mining payout address (esf1...)
	Pin         string  `json:"pin"`          // optional settings PIN: proof-of-control for changing address_1175 (rental-friendly, no keys)
}

type WorkerStats struct {
	MinerID       string    `json:"miner_id"`
	WorkerName    string    `json:"worker_name"`
	Online        bool      `json:"online"`
	Hashrate5m    float64   `json:"hashrate_5m"`
	Hashrate60m   float64   `json:"hashrate_60m"`
	ValidShares   int64     `json:"valid_shares"`
	RoundShares   int64     `json:"round_shares"`
	InvalidShares int64     `json:"invalid_shares"`
	BestDiff      float64   `json:"best_diff"`
	RoundBestDiff float64   `json:"round_best_diff"`
	ATHDiff       float64   `json:"ath_diff"`
	TotalWork     float64   `json:"total_work"`
	BlocksFound   int64     `json:"blocks_found"`
	LastShareAt   time.Time `json:"last_share_at"`
	ConnectedAt   time.Time `json:"connected_at"`
}

func main() {
	zapLogger, err := zap.NewProduction()
	if err != nil {
		panic(fmt.Sprintf("Failed to initialize logger: %v", err))
	}
	defer zapLogger.Sync()

	zapLogger.Info("🔥 Forge Solo API Server")

	// Initialize database connection for settings persistence. Not when the move of an earlier
	// version's data failed: maintenance mode (maintenance.go).
	migrationDB = stats.DatabaseFile()
	dbConnStr := stats.GetDBConnStr()
	if st, blocked := migstatus.Blocked(migrationDB); blocked {
		zapLogger.Warn("Maintenance mode: moving the data to the new database failed, so no database is opened (see the dashboard)",
			zap.Int("code", st.Code), zap.String("reason", st.Reason))
		maintenance = newMaintenance(migrationDB, st)
		go maintenance.watch(statusPollEvery)
	} else if err := stats.InitDBWithRetry(dbConnStr, 30, 2*time.Second); err != nil {
		zapLogger.Warn("Database not available, settings will not persist", zap.Error(err))
		// Keep trying. Without this the api runs with a nil handle for the life of the
		// process: the dashboard reports "not configured" while the stratum mines
		// perfectly well, and every attempt to save a payout address returns 500 with no
		// way for the user to recover short of restarting the app.
		//
		// Gated on IsDBInitialized, not on a failed ping: a pool that exists heals itself,
		// and InitDB replaces the handle without closing it, so retrying mid-outage would
		// leak a pool every tick.
		go func() {
			ticker := time.NewTicker(8 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				if stats.IsDBInitialized() {
					return
				}
				if err := stats.InitDB(dbConnStr); err != nil {
					continue
				}
				zapLogger.Info("✅ database connection established: settings will persist")
				loadMinerSettingsFromDB()
				return
			}
		}()
	} else {
		zapLogger.Info("✅ Connected to database")
		// Load miner settings from database
		loadMinerSettingsFromDB()
		// Periodically reload miner settings from database (every 10 seconds) so a
		// restore or an out-of-band edit to the miners table is picked up without a restart.
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				loadMinerSettingsFromDB()
			}
		}()
	}
	defer stats.CloseDB()

	app := fiber.New(fiber.Config{
		AppName: "Forge Solo API",
	})

	// A handler that panics answers 500 and the api goes on: fiber does not recover by itself, so
	// one (a read racing the database's reconnect, say) took the whole dashboard down with it.
	app.Use(fiberrecover.New())
	app.Use(logRequests)
	app.Use(pageSecurityHeaders)

	// On this machine only (Forge Solo for Windows and Linux), answer only to this machine's own
	// names: a web page can rebind its name to 127.0.0.1 and reach the API as same-origin, but the
	// browser still sends the page's name as Host.
	if listenHostIsLoopback(os.Getenv("API_LISTEN_HOST")) {
		app.Use(onlyLocalHost)
	}

	useCORS(app, os.Getenv("CORS_ORIGINS"))

	// Rate limiting: 1000 requests per minute per IP
	apiRateMax := 6000
	if v := os.Getenv("API_RATE_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			apiRateMax = n
		}
	}
	app.Use(limiter.New(limiter.Config{
		Max:          apiRateMax,
		Expiration:   1 * time.Minute,
		KeyGenerator: rateLimitKey,
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error": "Rate limit exceeded. Please try again later.",
			})
		},
	}))

	app.Use(rejectCrossSiteWrites)

	app.Use(settingsPasswordGateFromEnv())

	// In maintenance mode it answers every API request, after the two gates above.
	if maintenance != nil {
		app.Use(maintenance.gate)
	}

	// API routes FIRST
	api := app.Group("/api/v1")
	api.Get("/stats", getPoolStats)
	api.Get("/blocks", getBlocksAPI)
	api.Get("/miners", getMinersListAPI)
	api.Get("/miners/:address", getMiner)
	api.Get("/miners/:address/workers", getMinerWorkers)
	api.Get("/miners/:address/payouts", getMinerPayouts)
	api.Get("/miners/:address/solo-payouts", getMinerSoloPayouts)
	api.Get("/miners/:address/blocks", getMinerBlocks)
	api.Get("/miners/:address/solo-blocks", getMinerSoloBlocks)
	api.Get("/miners/:address/settings", getMinerSettingsAPI)
	api.Post("/miners/settings", saveMinerSettings)
	api.Get("/network", getNetworkInfo)
	api.Get("/connectivity", getConnectivity)
	api.Get("/workers", getAllWorkers)
	api.Get("/validate-address", validateAddress)
	api.Get("/validate-1175-address", validate1175Address)
	api.Get("/health", healthCheck)
	api.Get("/node-status", getNodeStatus)
	api.Get("/mining-status", getMiningStatus)
	api.Get("/pool/config", getPoolConfig)
	api.Post("/pool/config", savePoolConfig)
	// TIDES mode: the pool's window and this install's payouts, read from Forge Pool.
	api.Get("/tides", getTidesPool)
	api.Get("/tides/me", getTidesMine)
	// The data of the version before 1.0.13, while it was left out (maintenance.go).
	api.Post("/old-data", saveOldDataChoice)

	// Alias routes for miningpoolstats and other services that expect /api/stats
	app.Get("/api/stats", getPoolStats)
	app.Get("/api/blocks", getBlocksAPI)

	// Prometheus-style metrics endpoint
	app.Get("/metrics", func(c *fiber.Ctx) error {
		workers := getStratumWorkers()

		var totalHashrate float64
		var onlineWorkers int
		var totalShares int64

		for _, w := range workers {
			if w.Online {
				onlineWorkers++
				totalHashrate += w.Hashrate5m
			}
			totalShares += w.ValidShares
		}

		// Get block count from node
		var blockHeight int64
		if heightResult, err := rpcCall("getblockcount", []interface{}{}); err == nil {
			json.Unmarshal(heightResult, &blockHeight)
		}

		// Get pool blocks from DB
		poolBlocks := stats.GetTotalBlocksDB()

		// Output in Prometheus format
		c.Set("Content-Type", "text/plain; charset=utf-8")
		// The pool_* metric NAMES below are a scrape contract: a Grafana panel or an alert
		// rule referencing them breaks silently on a rename, with no error anywhere. The
		// HELP text is metadata and has been corrected for a solo miner; the names are
		// deliberately left alone. Do not "tidy" them.
		return c.SendString(fmt.Sprintf(`# HELP pool_hashrate_ths Hashrate in TH/s
# TYPE pool_hashrate_ths gauge
pool_hashrate_ths %.6f

# HELP pool_workers_online Number of online workers
# TYPE pool_workers_online gauge
pool_workers_online %d

# HELP pool_workers_total Total number of workers
# TYPE pool_workers_total gauge
pool_workers_total %d

# HELP pool_shares_total Total valid shares submitted
# TYPE pool_shares_total counter
pool_shares_total %d

# HELP pool_blocks_found Total blocks found
# TYPE pool_blocks_found counter
pool_blocks_found %d

# HELP network_block_height Current network block height
# TYPE network_block_height gauge
network_block_height %d

# HELP pool_uptime_seconds API uptime in seconds
# TYPE pool_uptime_seconds gauge
pool_uptime_seconds %.0f
`,
			totalHashrate,
			onlineWorkers,
			len(workers),
			totalShares,
			poolBlocks,
			blockHeight,
			time.Since(startTime).Seconds(),
		))
	})

	app.Get("/health", func(c *fiber.Ctx) error {
		// Check components health
		health := fiber.Map{
			"status": "healthy",
			"uptime": time.Since(startTime).String(),
			"checks": fiber.Map{},
		}

		checks := health["checks"].(fiber.Map)

		// Check RPC connection
		rpcHealthy := false
		if heightResult, err := rpcCall("getblockcount", []interface{}{}); err == nil {
			var height int64
			if json.Unmarshal(heightResult, &height) == nil && height > 0 {
				rpcHealthy = true
				checks["node"] = fiber.Map{"status": "healthy", "height": height}
			}
		}
		if !rpcHealthy {
			checks["node"] = fiber.Map{"status": "unhealthy", "error": "Cannot connect to BCH2 node"}
			health["status"] = "degraded"
		}

		// Check stratum connection
		stratumHealthy := false
		if resp, err := internalAPIGet(stratumURL + "/internal/stats"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				stratumHealthy = true
				checks["stratum"] = fiber.Map{"status": "healthy"}
			}
		}
		if !stratumHealthy {
			checks["stratum"] = fiber.Map{"status": "unhealthy", "error": "Cannot connect to stratum server"}
			health["status"] = "degraded"
		}

		// Check database
		if stats.IsDBConnected() {
			checks["database"] = fiber.Map{"status": "healthy"}
		} else {
			checks["database"] = fiber.Map{"status": "unavailable", "error": "Database not connected"}
		}

		// Set HTTP status based on health
		if health["status"] == "healthy" {
			return c.JSON(health)
		}
		return c.Status(503).JSON(health)
	})

	// Favicon - return empty to prevent 404 spam in logs
	app.Get("/favicon.ico", func(c *fiber.Ctx) error {
		return c.SendStatus(204)
	})

	// Static HTML pages - only serve pages that exist
	app.Get("/settings", func(c *fiber.Ctx) error {
		return c.SendFile(webRoot + "/settings.html")
	})
	app.Get("/solo", func(c *fiber.Ctx) error {
		return c.SendFile(webRoot + "/solo.html")
	})

	// Static files - serve from web directory
	app.Static("/", webRoot)

	// Fallback for "/" and unknown paths.
	//
	// On Umbrel an nginx container fronts this API with `index solo.html`, so this never
	// runs. On Windows the API IS the web server, and the Forge Solo dist ships solo.html
	// with NO index.html -- so sending index.html unconditionally 404s the dashboard root
	// for every Windows user. Probe in order and serve whichever the shipped dist actually
	// contains, rather than assuming a filename.
	app.Use(func(c *fiber.Ctx) error {
		// Don't override API routes
		if len(c.Path()) > 4 && c.Path()[:4] == "/api" {
			return c.Status(404).JSON(fiber.Map{"error": "Not found"})
		}
		for _, page := range []string{"/solo.html", "/index.html"} {
			if _, err := os.Stat(webRoot + page); err == nil {
				return c.SendFile(webRoot + page)
			}
		}
		return c.Status(404).SendString("web UI not found in " + webRoot)
	})

	listenPort := os.Getenv("API_LISTEN_PORT")
	if listenPort == "" {
		listenPort = "8080"
	}
	go func() {
		// API_LISTEN_HOST=127.0.0.1 keeps the API on this machine (Forge Solo for Linux, which may run
		// on a host with a public address: the API is unauthenticated). Unset: every interface.
		// tcp4: fiber's own default for app.Listen.
		ln, err := netlisten.Listen("tcp4", os.Getenv("API_LISTEN_HOST")+":"+listenPort)
		if err != nil {
			zapLogger.Fatal("Server error", zap.Error(err))
		}
		if err := app.Listener(ln); err != nil {
			zapLogger.Fatal("Server error", zap.Error(err))
		}
	}()

	zapLogger.Info("✅ API server running", zap.String("port", listenPort))

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sigCh:
		zapLogger.Info("Shutting down...")
	case <-maintenance.done():
		zapLogger.Info("The move of the old data is no longer failed: stopping, to be started again normally")
	}
	app.Shutdown()
}

// rpcCallURL is rpcCall against an arbitrary node. The 1175 node has its own credentials,
// and its peer counts are needed to tell the user whether their 25360 forward is working.
func rpcCallURL(url, user, pass, method string, params interface{}) (json.RawMessage, error) {
	if url == "" || user == "" || pass == "" {
		return nil, fmt.Errorf("RPC credentials not configured")
	}
	reqBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "1.0", "id": "api", "method": method, "params": params,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(user, pass)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var rpcResp struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, err
	}
	return rpcResp.Result, nil
}

// peerCounts reports total peers and how many of them dialled US.
//
// Inbound is the number that matters: outbound peers say nothing about reachability, but a
// single inbound peer proves the port forward works end to end -- better evidence than any
// self-reported address, and it needs no third-party service.
func peerCounts(raw json.RawMessage, err error) (total, inbound int, ok bool) {
	if err != nil || len(raw) == 0 {
		return 0, 0, false
	}
	var peers []struct {
		Inbound bool `json:"inbound"`
	}
	if json.Unmarshal(raw, &peers) != nil {
		return 0, 0, false
	}
	for _, p := range peers {
		if p.Inbound {
			inbound++
		}
	}
	return len(peers), inbound, true
}

// publicAddressFrom picks the best routable address the node believes it has.
//
// getnetworkinfo.localaddresses holds the addresses the node found on its own interfaces, was
// given with -externalip, or mapped on the router; peers' reports only raise the score of one
// already there. It is the node's own view -- no external lookup, and nothing leaks the
// user's address to a third party. Private and loopback candidates are discarded: a LAN
// address is exactly what a rental cannot use.
func publicAddressFrom(raw json.RawMessage, err error) string {
	if err != nil || len(raw) == 0 {
		return ""
	}
	var info struct {
		LocalAddresses []struct {
			Address string `json:"address"`
			Score   int    `json:"score"`
		} `json:"localaddresses"`
	}
	if json.Unmarshal(raw, &info) != nil {
		return ""
	}
	best, bestScore := "", -1
	for _, la := range info.LocalAddresses {
		if !routablePublicIP(la.Address) {
			continue
		}
		if la.Score > bestScore {
			best, bestScore = la.Address, la.Score
		}
	}
	return best
}

// routablePublicIP reports whether an address is one the outside world could dial.
// Loopback, RFC1918 and link-local are exactly what a rental cannot use, and carrier-grade
// NAT (100.64.0.0/10) is the one case where the user cannot forward a port at all -- not
// "private" by Go's definition, but handing any of these to a marketplace produces a
// connection that never arrives.
func routablePublicIP(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return false
	}
	return true
}

// publicAddressFromPeers derives our public address from what peers report seeing us on.
//
// localaddresses is empty whenever the node runs in a container: its only interface holds
// an RFC1918 address, which is not routable and so is never recorded. getpeerinfo carries
// the same fact one hop further out -- addrlocal is the address a peer observed our
// connection arriving from. That is peer-supplied and therefore spoofable, so no single
// peer is trusted: take the value a majority of reporting peers agree on, and only count
// outbound connections, since an inbound peer can only echo an address we already gave it.
func publicAddressFromPeers(raw json.RawMessage, err error) string {
	if err != nil || len(raw) == 0 {
		return ""
	}
	var peers []struct {
		AddrLocal string `json:"addrlocal"`
		Inbound   bool   `json:"inbound"`
	}
	if json.Unmarshal(raw, &peers) != nil {
		return ""
	}
	counts := map[string]int{}
	reporting := 0
	for _, p := range peers {
		if p.AddrLocal == "" || p.Inbound {
			continue
		}
		host := p.AddrLocal
		if h, _, splitErr := net.SplitHostPort(host); splitErr == nil {
			host = h
		}
		if !routablePublicIP(host) {
			continue
		}
		counts[host]++
		reporting++
	}
	best, bestN := "", 0
	for h, n := range counts {
		if n > bestN {
			best, bestN = h, n
		}
	}
	// Require corroboration: at least two peers, and a strict majority of those reporting.
	// One peer claiming an address it made up must never become what we show a marketplace.
	if bestN < 2 || bestN*2 <= reporting {
		return ""
	}
	return best
}

// getConnectivity answers the two questions a solo miner actually has: what do I give a
// rental marketplace, and are my P2P forwards working?
func getConnectivity(c *fiber.Ctx) error {
	bchPeers, bchPeersErr := rpcCall("getpeerinfo", []interface{}{})
	bchTotal, bchInbound, bchOK := peerCounts(bchPeers, bchPeersErr)

	// localaddresses is empty for a node behind NAT, which is every home node. That does not
	// stop it announcing itself -- with -discover it advertises the address its peers see it
	// on -- so it says nothing about whether peers can find the node, only where to read the
	// address from: fall back to the peers' view.
	public := publicAddressFrom(rpcCall("getnetworkinfo", []interface{}{}))
	if public == "" {
		public = publicAddressFromPeers(bchPeers, bchPeersErr)
	}

	out := fiber.Map{
		"publicIp":    public,
		"lanIp":       lanAddress(),
		"stratumPort": 3333,
		"rentalPort":  3335,
		"bch2": fiber.Map{
			"port": 8339, "peers": bchTotal, "inbound": bchInbound,
			"known": bchOK, "reachable": bchOK && bchInbound > 0,
		},
	}
	// Without a 1175 node (Forge Solo for Linux) there is no 1175 port to report on.
	if mergeMiningAvailable() {
		auxTotal, auxInbound, auxOK := peerCounts(rpcCallURL(
			os.Getenv("AUX1175_URL"), os.Getenv("AUX1175_USER"), os.Getenv("AUX1175_PASSWORD"),
			"getpeerinfo", []interface{}{}))
		out["aux1175"] = fiber.Map{
			"port": 25360, "peers": auxTotal, "inbound": auxInbound,
			"known": auxOK, "reachable": auxOK && auxInbound > 0,
		}
	}
	return c.JSON(out)
}

// rejectCrossSiteWrites refuses a state-changing request that a browser made on behalf of
// another site (cross-site request forgery). The home app's settings save has no login of its
// own -- on Umbrel it sits behind Umbrel's, on Windows and Linux on 127.0.0.1 -- and the body
// parser also accepts form encoding, so any web page the user had open could post a hidden form
// to it and set its own payout address. A form cannot send application/json, and a cross-site
// fetch that does needs a CORS preflight, which this API refuses; Sec-Fetch-Site, which browsers
// set on every request, must say same-origin when it is present. Clients other than browsers send
// no Sec-Fetch-Site and are unaffected as long as they post JSON, as the dashboard does.
// logRequests logs the requests that change something or fail. Every request used to be logged,
// the dashboard's polls and the healthcheck's HEAD every 10 s with them; on Umbrel a container's
// log is kept until the container is replaced, and that alone was megabytes a day.
func logRequests(c *fiber.Ctx) error {
	start := time.Now()
	err := c.Next()
	status := c.Response().StatusCode()
	var fe *fiber.Error
	if errors.As(err, &fe) {
		status = fe.Code
	} else if err != nil {
		status = fiber.StatusInternalServerError
	}
	switch m := c.Method(); {
	case status >= 400, m != fiber.MethodGet && m != fiber.MethodHead && m != fiber.MethodOptions:
		log.Printf("%s %s %d %s", m, c.Path(), status, time.Since(start).Round(time.Millisecond))
	}
	return err
}

// useCORS allows cross-origin reads only from the origins CORS_ORIGINS names. The dashboard is
// served from the API's own origin and needs none; the old default, used whenever the variable was
// empty (on every platform), let any page on localhost:3000 read the API.
func useCORS(app *fiber.App, origins string) {
	if origins == "" {
		return
	}
	app.Use(cors.New(cors.Config{
		AllowOrigins:     origins,
		AllowMethods:     "GET,POST,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,Authorization",
		AllowCredentials: false,
		MaxAge:           3600,
	}))
}

// pageSecurityHeaders sets on the dashboard's pages what its own servers send them with (Umbrel's
// nginx, the Windows and Linux launchers): no page may frame them, no content type is guessed, no
// referrer is sent on. The API serves the same pages on its own port, where a page that framed the
// Settings page could have a save clicked through by someone who never sees it. Not on /api/:
// those go out through the servers above, which set the headers themselves, and twice would be
// one X-Frame-Options a browser may ignore.
func pageSecurityHeaders(c *fiber.Ctx) error {
	if !strings.HasPrefix(c.Path(), "/api/") {
		c.Set("X-Frame-Options", "DENY")
		c.Set("Content-Security-Policy", "frame-ancestors 'none'")
		c.Set("X-Content-Type-Options", "nosniff")
		c.Set("Referrer-Policy", "no-referrer")
	}
	return c.Next()
}

// rateLimitKey is the client a request counts against. Behind the app's nginx, or the Windows and
// Linux dashboards' proxies, c.IP() is always the proxy's address, so all clients would share one
// bucket. Each of them sets X-Real-IP to the true client, so that comes first; then the last
// X-Forwarded-For hop, the one a proxy appended (the first is whatever the client sent); then the
// direct address.
func rateLimitKey(c *fiber.Ctx) string {
	if ip := c.Get("X-Real-IP"); ip != "" {
		return ip
	}
	if xff := c.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(xff[strings.LastIndexByte(xff, ',')+1:])
	}
	return c.IP()
}

// listenHostIsLoopback reports whether API_LISTEN_HOST keeps the API on this machine.
func listenHostIsLoopback(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// onlyLocalHost answers 421 to a request whose Host is not this machine's own name.
func onlyLocalHost(c *fiber.Ctx) error {
	host := c.Get(fiber.HeaderHost)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if !listenHostIsLoopback(host) {
		return c.Status(fiber.StatusMisdirectedRequest).JSON(fiber.Map{"success": false,
			"error": "Forge Solo answers only at 127.0.0.1 or localhost"})
	}
	return c.Next()
}

func rejectCrossSiteWrites(c *fiber.Ctx) error {
	switch c.Method() {
	case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
		return c.Next()
	}
	if site := c.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"success": false, "error": "Refused: a cross-site request cannot change settings"})
	}
	ct, _, _ := strings.Cut(c.Get(fiber.HeaderContentType), ";")
	if !strings.EqualFold(strings.TrimSpace(ct), fiber.MIMEApplicationJSON) {
		return c.Status(fiber.StatusUnsupportedMediaType).JSON(fiber.Map{"success": false, "error": "Refused: send JSON (Content-Type: application/json)"})
	}
	return c.Next()
}

// lanAddress is this machine's address on its own network, which a miner beside it dials. The
// dashboard shows it where the page itself is opened on 127.0.0.1 (Windows, Linux), since that
// address reaches only a miner on this same machine. On Umbrel the API runs in a container, so
// this is the container's address; the dashboard never uses it there, because the page is opened
// by the Umbrel's own name.
func lanAddress() string {
	return pickLANAddress(routeSourceAddress(), systemInterfaces())
}

// routeSourceAddress is the source address of this machine's route to the internet. A UDP
// "connection" only looks the route up: no packet is sent, and 192.0.2.1 is a documentation
// address that routes nowhere. nil when there is no such route.
func routeSourceAddress() net.IP {
	c, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return nil
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP
	}
	return nil
}

// lanInterface is what pickLANAddress needs to know of a network interface.
type lanInterface struct {
	name  string
	flags net.Flags
	mac   net.HardwareAddr
	addrs []net.IP
}

func systemInterfaces() []lanInterface {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []lanInterface
	for _, i := range ifs {
		li := lanInterface{name: i.Name, flags: i.Flags, mac: i.HardwareAddr}
		if addrs, err := i.Addrs(); err == nil {
			for _, a := range addrs {
				if n, ok := a.(*net.IPNet); ok {
					li.addrs = append(li.addrs, n.IP)
				}
			}
		}
		out = append(out, li)
	}
	return out
}

// Interfaces whose names start with these are virtual: bridges for containers and virtual
// machines, and VPN tunnels. Compared in lower case.
var virtualInterfacePrefixes = []string{
	"docker", "br-", "veth", "virbr", "vboxnet", "vmnet", "lxcbr", "lxdbr", "cni", "flannel", "cali", "podman",
	"vethernet", "virtualbox", "vmware", "tun", "tap", "wg", "utun", "ppp", "tailscale", "zt", "nordlynx",
	"protonvpn", "mullvad", "openvpn", "wireguard", "wintun",
}

// onTheLAN reports whether an interface is one on the machine's own network: up, not loopback and
// not point-to-point, with a hardware address (a tunnel has none; a TAP-Windows adapter's starts
// 00:FF), and not named as a virtual one is.
func onTheLAN(i lanInterface) bool {
	if i.flags&net.FlagUp == 0 || i.flags&net.FlagLoopback != 0 || i.flags&net.FlagPointToPoint != 0 {
		return false
	}
	if len(i.mac) == 0 || (len(i.mac) >= 2 && i.mac[0] == 0x00 && i.mac[1] == 0xff) {
		return false
	}
	name := strings.ToLower(i.name)
	for _, p := range virtualInterfacePrefixes {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

// privateRank is how likely a private (RFC 1918) IPv4 address is a home network's, lower first:
// 192.168/16, 10/8, 172.16/12 (Docker's and Hyper-V's own). -1 for any other address.
func privateRank(ip net.IP) int {
	ip4 := ip.To4()
	switch {
	case ip4 == nil:
		return -1
	case ip4[0] == 192 && ip4[1] == 168:
		return 0
	case ip4[0] == 10:
		return 1
	case ip4[0] == 172 && ip4[1]&0xf0 == 16:
		return 2
	}
	return -1
}

// pickLANAddress is the address to tell miners. The route's source address alone is the
// tunnel's while a full-tunnel VPN is on (OpenVPN, WireGuard, most VPN apps), and no miner on
// the LAN can reach that: it is used when it is a private address on a LAN interface. Otherwise
// the likeliest such address is, and on a machine with none, the route's address as before.
func pickLANAddress(route net.IP, ifaces []lanInterface) string {
	var best net.IP
	bestRank := 3
	for _, i := range ifaces {
		if !onTheLAN(i) {
			continue
		}
		for _, ip := range i.addrs {
			rank := privateRank(ip)
			if rank < 0 {
				continue
			}
			if route != nil && ip.Equal(route) {
				return ip.To4().String()
			}
			if rank < bestRank {
				best, bestRank = ip.To4(), rank
			}
		}
	}
	if best != nil {
		return best.String()
	}
	if route == nil || route.IsLoopback() || route.IsUnspecified() {
		return ""
	}
	return route.String()
}

// mergeMiningAvailable is false where the app runs no 1175 node (MERGE_MINING_AVAILABLE=0, set by
// Forge Solo for Linux): the dashboard then leaves out everything about 1175 merge-mining.
func mergeMiningAvailable() bool {
	return os.Getenv("MERGE_MINING_AVAILABLE") != "0"
}

// rpcError is an error the node itself answered a call with.
type rpcError struct {
	method  string
	code    int
	message string
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("%s: node error %d: %s", e.method, e.code, e.message)
}

// rpcInWarmup is the node's answer to every call while it starts (loading its block index,
// verifying its latest blocks); its message says which step it is on.
const rpcInWarmup = -28

func rpcCall(method string, params interface{}) (json.RawMessage, error) {
	if rpcUser == "" || rpcPass == "" {
		return nil, fmt.Errorf("RPC credentials not configured - set RPC_USER and RPC_PASSWORD environment variables")
	}

	reqBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "1.0",
		"id":      "api",
		"method":  method,
		"params":  params,
	})

	req, err := http.NewRequest("POST", rpcURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(rpcUser, rpcPass)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("%s: unreadable answer from the node (HTTP %d)", method, resp.StatusCode)
	}
	// The node's error is an error, not an empty result. A node starting up answers every call
	// with -28 ("Loading block index…") until it has loaded the chain; read as an empty result,
	// that looked like a node at block 0, and the dashboard said it was syncing from 0%.
	if rpcResp.Error != nil {
		return nil, &rpcError{method: method, code: rpcResp.Error.Code, message: rpcResp.Error.Message}
	}
	return rpcResp.Result, nil
}

// internalAPIGet makes a GET request to internal stratum API with auth token
func internalAPIGet(urlPath string) (*http.Response, error) {
	req, err := http.NewRequest("GET", urlPath, nil)
	if err != nil {
		return nil, err
	}
	if internalAPIToken != "" {
		req.Header.Set("X-Internal-Token", internalAPIToken)
	}
	return internalHTTPClient.Do(req)
}

func getStratumWorkers() []WorkerStats {
	resp, err := internalAPIGet(stratumURL + "/internal/workers")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var result struct {
		Workers []WorkerStats `json:"workers"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Workers
}

// Network-stats last-good cache. getdifficulty / getnetworkhashps occasionally fail (RPC
// timeout on a busy home node, node warming up) — the old code discarded the error and returned
// 0, which flashed "0" / "0 H/s" on the dashboard tiles. Hold the last good reading, only
// overwrite it with a fresh value that is actually > 0, and serve it from cache for a few seconds
// so the dashboard's frequent polling doesn't hammer the node RPC with four calls every cycle.
var (
	netStatsMu     sync.RWMutex
	lastGoodDiff   float64
	lastGoodNetHps float64
	netStatsAt     time.Time
)

const netStatsTTL = 15 * time.Second

// fetchNetStats returns network difficulty + hashrate, never regressing to 0 once a good value
// has been seen. A value <= 0 or an RPC failure falls back to the last good reading.
func fetchNetStats() (difficulty, networkHashrate float64) {
	netStatsMu.RLock()
	difficulty, networkHashrate = lastGoodDiff, lastGoodNetHps
	fresh := difficulty > 0 && time.Since(netStatsAt) < netStatsTTL
	netStatsMu.RUnlock()
	if fresh {
		return
	}
	if r, err := rpcCall("getdifficulty", []interface{}{}); err == nil {
		var d float64
		if json.Unmarshal(r, &d) == nil && d > 0 {
			difficulty = d
		}
	}
	if r, err := rpcCall("getnetworkhashps", []interface{}{}); err == nil {
		var h float64
		if json.Unmarshal(r, &h) == nil && h > 0 {
			networkHashrate = h
		}
	}
	netStatsMu.Lock()
	if difficulty > 0 {
		lastGoodDiff = difficulty
	}
	if networkHashrate > 0 {
		lastGoodNetHps = networkHashrate
	}
	difficulty, networkHashrate = lastGoodDiff, lastGoodNetHps
	netStatsAt = time.Now()
	netStatsMu.Unlock()
	return
}

func getPoolStats(c *fiber.Ctx) error {
	heightResult, _ := rpcCall("getblockcount", []interface{}{})
	var height int64
	json.Unmarshal(heightResult, &height)

	// Network difficulty + hashrate with last-good fallback (never flash 0 on an RPC hiccup).
	difficulty, networkHashrate := fetchNetStats()

	infoResult, _ := rpcCall("getblockchaininfo", []interface{}{})
	var info struct {
		Chain         string `json:"chain"`
		BestBlockHash string `json:"bestblockhash"`
	}
	json.Unmarshal(infoResult, &info)

	// Get worker stats and pool stats from stratum
	workers := getStratumWorkers()
	var totalHashrate float64
	var onlineWorkers int
	minerSet := make(map[string]bool)
	for _, w := range workers {
		if w.Online {
			totalHashrate += w.Hashrate5m // Use 5-minute average for more responsive display
			onlineWorkers++
		}
		minerSet[w.MinerID] = true
	}

	// Get total blocks found from database (persists across restarts)
	blocksFound := stats.GetTotalBlocksDB()

	// Get luck stats from stratum internal stats
	var avgLuck float64 = 1.0 // Default to 100% (neutral luck)
	if resp, err := internalAPIGet(stratumURL + "/internal/stats"); err == nil {
		defer resp.Body.Close()
		var poolStats struct {
			AvgLuck float64 `json:"avg_luck"`
		}
		json.NewDecoder(resp.Body).Decode(&poolStats)
		if poolStats.AvgLuck > 0 {
			avgLuck = poolStats.AvgLuck
		}
	}

	// Get rental stats from stratum
	var rentalStats struct {
		NiceHashMiners int64 `json:"nicehash_miners"`
		MRRMiners      int64 `json:"mrr_miners"`
		OtherRentals   int64 `json:"other_rentals"`
		TotalRentals   int64 `json:"total_rentals"`
	}
	if resp, err := internalAPIGet(stratumURL + "/internal/rental-stats"); err == nil {
		defer resp.Body.Close()
		json.NewDecoder(resp.Body).Decode(&rentalStats)
	}

	hashrateStr := "0 H/s"
	if totalHashrate >= 1000 {
		hashrateStr = fmt.Sprintf("%.2f PH/s", totalHashrate/1000)
	} else if totalHashrate >= 1 {
		hashrateStr = fmt.Sprintf("%.1f TH/s", totalHashrate)
	} else if totalHashrate > 0 {
		// Below 1 TH/s, scale down instead of printing "0.00 TH/s". A NerdMiner or a lone
		// Bitaxe is well under a terahash, and the settings page prints this string
		// verbatim -- so the one page a new user checks told them they were doing nothing.
		switch h := totalHashrate * 1e12; {
		case h >= 1e9:
			hashrateStr = fmt.Sprintf("%.2f GH/s", h/1e9)
		case h >= 1e6:
			hashrateStr = fmt.Sprintf("%.2f MH/s", h/1e6)
		case h >= 1e3:
			hashrateStr = fmt.Sprintf("%.2f KH/s", h/1e3)
		default:
			hashrateStr = fmt.Sprintf("%.0f H/s", h)
		}
	}

	return c.JSON(fiber.Map{
		"hashrate":      hashrateStr,
		"hashrateRaw":   totalHashrate * 1e12,
		"workers":       onlineWorkers,
		"miners":        len(minerSet),
		"blocksFound":   blocksFound,
		"blocksPending": 0,
		// All three are constants now, and 0 is the truth rather than a placeholder: this
		// app takes no fee on any path, and there is no minimum because a solo block pays
		// its finder in its own coinbase -- nothing accumulates. These literals once said
		// poolFee 1% and minPayout 5 BCH2, and this route is deliberately aliased at
		// /api/stats for aggregators, so those were the numbers the outside world saw.
		//
		// The keys stay. For a product whose headline claim is "no pool fee", a
		// machine-readable 0 IS the claim; undefined would be worse. The POOL_FEE env
		// branch that used to feed poolFee was set by nothing in the tree and is gone.
		"poolFee":           0.0,
		"soloFee":           0.0,
		"minPayout":         0.0,
		"currentHeight":     height,
		"networkDifficulty": difficulty,
		"networkHashrate":   networkHashrate,
		"bestBlockHash":     info.BestBlockHash,
		"uptime":            time.Since(startTime).String(),
		"luck":              avgLuck, // Average luck over recent blocks (1.0 = 100%)
		"rentals": fiber.Map{
			"nicehash": rentalStats.NiceHashMiners,
			"mrr":      rentalStats.MRRMiners,
			"other":    rentalStats.OtherRentals,
			"total":    rentalStats.TotalRentals,
		},
	})
}

func getBlocksAPI(c *fiber.Ctx) error {
	page := c.QueryInt("page", 1)
	limit := c.QueryInt("limit", 25)
	if limit > 100 {
		limit = 100 // Cap at 100 blocks per request
	}
	if limit < 1 {
		limit = 1
	}
	if page < 1 {
		page = 1
	}

	// Fetch pool-mined blocks from stratum internal endpoint
	url := fmt.Sprintf("%s/internal/pool-blocks?page=%d&limit=%d", stratumURL, page, limit)
	resp, err := internalAPIGet(url)
	if err != nil {
		// Return empty blocks if stratum is unavailable
		return c.JSON(fiber.Map{"blocks": []interface{}{}, "total": 0, "page": page, "limit": limit})
	}
	defer resp.Body.Close()

	// Check for non-200 status
	if resp.StatusCode != 200 {
		return c.JSON(fiber.Map{"blocks": []interface{}{}, "total": 0, "page": page, "limit": limit})
	}

	var data struct {
		Blocks []struct {
			Height    int64   `json:"height"`
			Hash      string  `json:"hash"`
			Reward    float64 `json:"reward"`
			MinerAddr string  `json:"miner_address"`
			Status    string  `json:"status"`
			Time      int64   `json:"time"`
			IsSolo    bool    `json:"is_solo"`
		} `json:"blocks"`
		Total int64 `json:"total"`
		Page  int   `json:"page"`
		Limit int   `json:"limit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed to parse pool blocks"})
	}

	// Get current height for confirmation status
	var currentHeight int64
	if heightResult, err := rpcCall("getblockcount", []interface{}{}); err == nil {
		json.Unmarshal(heightResult, &currentHeight)
	}

	// Transform to API response format
	var blocks []fiber.Map
	for _, b := range data.Blocks {
		blockType := "PPLNS"
		if b.IsSolo {
			blockType = "SOLO"
		}
		blocks = append(blocks, fiber.Map{
			"height": b.Height,
			"hash":   b.Hash,
			"time":   b.Time,
			"miner":  poolNameFromEnv(),
			"reward": b.Reward,
			// Six deep reads as confirmed, but never an orphan: those used to be reported confirmed.
			"confirmed": b.Status == "confirmed" || (b.Status != "orphaned" && currentHeight > 0 && currentHeight-b.Height >= 6),
			"status":    b.Status,
			"type":      blockType,
		})
	}

	return c.JSON(fiber.Map{
		"blocks": blocks,
		"total":  data.Total,
		"page":   data.Page,
		"limit":  data.Limit,
	})
}

func getMiner(c *fiber.Ctx) error {
	address, _ := url.QueryUnescape(c.Params("address"))

	// Validate address format to prevent injection attacks
	if !isValidBCH2Address(address) {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid BCH2 address format"})
	}

	workers := getStratumWorkers()

	var totalHashrate5m, totalHashrate60m float64
	var totalShares, totalRejected int64
	var totalRoundShares int64 // accepted shares since the last block, distinct from all-time
	var bestDiff float64
	var athDiff float64
	var totalWork float64
	var lastShare time.Time
	var workerCount int
	var onlineWorkers int

	for _, w := range workers {
		if addressMatches(w.MinerID, address) {
			workerCount++
			totalShares += w.ValidShares
			totalRoundShares += w.RoundShares
			totalRejected += w.InvalidShares
			totalWork += w.TotalWork
			if w.BestDiff > bestDiff {
				bestDiff = w.BestDiff
			}
			if w.ATHDiff > athDiff {
				athDiff = w.ATHDiff
			}
			if w.LastShareAt.After(lastShare) {
				lastShare = w.LastShareAt
			}
			// Only count hashrate from online workers (consistent with pool stats)
			if w.Online {
				onlineWorkers++
				totalHashrate5m += w.Hashrate5m
				totalHashrate60m += w.Hashrate60m
			}
		}
	}

	// Check settings with both normalized and full address
	settingsMu.RLock()
	settings, hasSettings := minerSettings[address]
	if !hasSettings {
		settings, hasSettings = minerSettings[normalizeAddress(address)]
	}
	settingsMu.RUnlock()

	// Get current height: the last one the node gave when it does not answer now, since which
	// blocks have matured depends on it.
	currentHeight := int64(0)
	if heightResult, err := rpcCall("getblockcount", []interface{}{}); err == nil && json.Unmarshal(heightResult, &currentHeight) == nil && currentHeight > 0 {
		lastNodeHeight.Store(currentHeight)
	} else {
		currentHeight = lastNodeHeight.Load()
	}

	// Get balance from stratum internal endpoint (use normalized address for lookup)
	matureBalance := 0.0
	immatureBalance := 0.0
	normalizedAddr := normalizeAddress(address)
	balanceURL := fmt.Sprintf("%s/internal/miner-balance?miner=%s&height=%d", stratumURL, url.QueryEscape(normalizedAddr), currentHeight)
	if resp, err := internalAPIGet(balanceURL); err == nil {
		defer resp.Body.Close()
		var balanceData struct {
			MatureBalance   float64 `json:"matureBalance"`
			ImmatureBalance float64 `json:"immatureBalance"`
		}
		json.NewDecoder(resp.Body).Decode(&balanceData)
		matureBalance = balanceData.MatureBalance
		immatureBalance = balanceData.ImmatureBalance
	}

	// In solo those two are structurally always zero: the stratum's balance endpoint feeds
	// off payout rows with a NULL/empty txid, and a solo payout is settled
	// txid='coinbase-direct' the instant it is recorded. Report what is actually true
	// instead -- how much of what this miner MINED has matured -- so the card stops saying
	// "0.00 waiting 100 confirms" with a hundred BCH2 genuinely maturing.
	//
	// Unknown is said, not shown as 0.00 or as all maturing: with no height from the node every
	// block counted as maturing, and with no answer from the database nothing was earned.
	balanceKnown := true
	if os.Getenv("HOME_APP") == "1" && matureBalance == 0 && immatureBalance == 0 {
		if currentHeight <= 0 {
			balanceKnown = false
		} else if m, im, err := stats.SoloEarningsErr(normalizedAddr, currentHeight); err != nil {
			balanceKnown = false
		} else if m > 0 || im > 0 {
			matureBalance, immatureBalance = m, im
		}
	}

	// The API's times are in UTC on every platform. The stratum stamps a worker's in its own zone
	// and keeps them so, for the clock reading its online checks rely on: they are converted here.
	return c.JSON(fiber.Map{
		"address":         address,
		"hashrate5m":      totalHashrate5m,
		"hashrate60m":     totalHashrate60m,
		"workers":         workerCount,
		"onlineWorkers":   onlineWorkers,
		"validShares":     totalShares,
		"roundShares":     totalRoundShares,
		"invalidShares":   totalRejected,
		"bestDiff":        bestDiff,
		"athDiff":         athDiff,
		"totalWork":       totalWork,
		"lastShare":       lastShare.UTC(),
		"soloMining":      hasSettings && settings.SoloMining,
		"balance":         matureBalance + immatureBalance,
		"matureBalance":   matureBalance,
		"immatureBalance": immatureBalance,
		"balanceKnown":    balanceKnown,
		"currentHeight":   currentHeight,
		"paid":            0.0,
	})
}

// lastNodeHeight is the last block count the node gave getMiner.
var lastNodeHeight atomic.Int64

func getMinerWorkers(c *fiber.Ctx) error {
	address, _ := url.QueryUnescape(c.Params("address"))
	if !isValidBCH2Address(address) {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid BCH2 address format"})
	}
	var mine []WorkerStats
	for _, w := range getStratumWorkers() {
		if addressMatches(w.MinerID, address) {
			mine = append(mine, w)
		}
	}
	// The busiest first, and no more than the page can show: a list of hundreds of thousands
	// of names (before the stratum capped them) was 35 MB per poll and froze the dashboard.
	sort.SliceStable(mine, func(i, j int) bool {
		if mine[i].Online != mine[j].Online {
			return mine[i].Online
		}
		if mine[i].Hashrate5m != mine[j].Hashrate5m {
			return mine[i].Hashrate5m > mine[j].Hashrate5m
		}
		return mine[i].ValidShares > mine[j].ValidShares
	})
	total := len(mine)
	if len(mine) > maxWorkersListed {
		mine = mine[:maxWorkersListed]
	}

	var result []fiber.Map
	for _, w := range mine {
		rejectRate := 0.0
		if w.ValidShares+w.InvalidShares > 0 {
			rejectRate = float64(w.InvalidShares) / float64(w.ValidShares+w.InvalidShares) * 100
		}

		result = append(result, fiber.Map{
			"name":          w.WorkerName,
			"online":        w.Online,
			"hashrate5m":    w.Hashrate5m,
			"hashrate60m":   w.Hashrate60m,
			"validShares":   w.ValidShares,
			"invalidShares": w.InvalidShares,
			"rejectRate":    rejectRate,
			"bestDiff":      w.BestDiff,
			"roundBestDiff": w.RoundBestDiff,
			"athDiff":       w.ATHDiff,
			"blocksFound":   w.BlocksFound,
			"lastShare":     w.LastShareAt.UTC(), // in UTC, as in getMiner
			"connectedAt":   w.ConnectedAt.UTC(),
		})
	}

	return c.JSON(fiber.Map{
		"workers": result,
		"total":   total,
	})
}

// maxWorkersListed is the most workers one response lists.
const maxWorkersListed = 500

func getMinersListAPI(c *fiber.Ctx) error {
	// Privacy: do not enumerate miner addresses publicly. Return aggregate count only.
	// Per-address lookup remains available at /api/v1/miners/:address (caller must know the address).
	workers := getStratumWorkers()
	minerMap := make(map[string]bool)
	for _, w := range workers {
		if w.MinerID != "" {
			minerMap[w.MinerID] = true
		}
	}
	return c.JSON(fiber.Map{
		"count": len(minerMap),
	})
}

func getAllWorkers(c *fiber.Ctx) error {
	// Privacy: do not enumerate per-worker stats publicly. Return aggregate only.
	// Per-miner workers remain available at /api/v1/miners/:address/workers.
	workers := getStratumWorkers()
	online := 0
	var totalHashrate5m float64
	var totalShares int64
	for _, w := range workers {
		if w.Online {
			online++
			totalHashrate5m += w.Hashrate5m
		}
		totalShares += w.ValidShares
	}
	return c.JSON(fiber.Map{
		"total":            len(workers),
		"online":           online,
		"totalHashrate5m":  totalHashrate5m,
		"totalValidShares": totalShares,
	})
}

// sanitizeTagAPI mirrors the stratum coinbase-tag sanitizer (printable ASCII, <=24 bytes).
func sanitizeTagAPI(tag string) string {
	out := make([]byte, 0, len(tag))
	for i := 0; i < len(tag); i++ {
		if c := tag[i]; c >= 0x20 && c < 0x7f {
			out = append(out, c)
		}
	}
	if len(out) > 24 {
		out = out[:24]
	}
	return string(out)
}

func getPoolConfig(c *fiber.Ctx) error {
	// Dashboard-managed config is the source of truth (DB pool_config). Env vars provide the
	// initial defaults until the miner saves settings from the UI (so a fresh install shows
	// whatever was seeded in the Umbrel app config, then the DB value once configured).
	_, payout1175, tag, err := stats.GetPoolConfig()
	if err != nil {
		return settingsUnreadable(c, err)
	}
	mode, err := stats.GetPayoutMode()
	if err != nil {
		return settingsUnreadable(c, err)
	}
	// The env fallback is validated before it reaches the page. Unvalidated, an Umbrel
	// app-config value with a bitcoincash: prefix or a P2SH p… address made the settings page
	// show the address, hide its "not configured" banner, and then 400 every save -- including
	// a save meant only to change the tag -- while the stratum sat paused. An invalid value is
	// no configuration at all.
	poolAddr, err := payoutAddressInEffect()
	if err != nil {
		return settingsUnreadable(c, err)
	}
	if payout1175 == "" {
		// Validated, like the POOL_ADDRESS fallback beside it. Unvalidated, one typo in the
		// Umbrel app config pre-filled the form with a bad address and then 400'd EVERY
		// save -- including the save that sets the BCH2 address and un-pauses mining.
		if env := strings.TrimSpace(os.Getenv("PAYOUT_ADDRESS_1175")); env != "" {
			if isValid1175Address(env) {
				payout1175 = env
			} else {
				log.Printf("WARNING: PAYOUT_ADDRESS_1175 is set but is not a valid esf1… address (%q); ignoring it", env)
			}
		}
	}
	if tag == "" {
		tag = os.Getenv("COINBASE_TAG")
	}
	if tag == "" {
		tag = mining.DefaultCoinbaseTag
	}
	out := fiber.Map{
		"stratum_port": 3333,
		"pool_name":    poolNameFromEnv(),
		"pool_fee":     0.0,
		"solo_fee":     0.0,
		// Always 0: a solo block pays its finder in its own coinbase, so there is no
		// minimum to cross. Publishing the stored value here reported 1 while /api/stats on
		// the same server reported 0 and the UI said there is no minimum at all.
		"min_payout":          0.0,
		"pool_address":        poolAddr,
		"payout_address_1175": payout1175,
		"coinbase_tag":        tag,
		"configured":          poolAddr != "",
		"payout_mode":         mode,
		// true: a save must carry the app's password (settingsPasswordGate). With it, how long
		// that password is and where the app runs, so the page can say where to find it and spot
		// a cut-off copy. Never the password itself.
		"password_required": settingsPassword != "",
		"password_length":   len(settingsPassword),
		"platform":          platformFromEnv(),
		// false: this app runs no 1175 node, so the dashboard hides 1175 merge-mining.
		"merge_mining_available": mergeMiningAvailable(),
	}
	// Forge Solo for Linux: which secrets.env holds the password, the service's or the one of a
	// copy run by hand, so Settings can name it.
	if p := secretsPathFromEnv(); p != "" {
		out["secrets_path"] = p
	}
	return c.JSON(out)
}

// platformFromEnv is where this app runs, as FORGE_PLATFORM says: "windows" or "linux", which
// their launchers set, and otherwise "umbrel".
func platformFromEnv() string {
	switch p := strings.ToLower(strings.TrimSpace(os.Getenv("FORGE_PLATFORM"))); p {
	case "windows", "linux":
		return p
	}
	return "umbrel"
}

// secretsPathFromEnv is where Forge Solo for Linux keeps secrets.env, with DASHBOARD_PASSWORD (the
// Settings password) in it: in its data directory, beside the database DB_PATH names. "" on the
// other platforms.
func secretsPathFromEnv() string {
	db := strings.TrimSpace(os.Getenv("DB_PATH"))
	if platformFromEnv() != "linux" || db == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(db), "secrets.env")
}

// payoutModeOrSolo is the dashboard's payout mode: solo unless TIDES was chosen.
// settingsUnreadable answers a request that needs the stored settings when the database cannot
// be read. Answering with defaults instead made Settings show "not configured", blank fields and
// Solo after a database hiccup, and the user's next save then wrote those over the real settings.
func settingsUnreadable(c *fiber.Ctx, err error) error {
	log.Printf("pool config: cannot read the stored settings: %v", err)
	return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"success": false,
		"error": "Forge Solo cannot read its saved settings right now (database unavailable). Nothing was changed; try again in a minute."})
}

func savePoolConfig(c *fiber.Ctx) error {
	// Home app (single-tenant behind Umbrel auth): the dashboard IS the admin, so
	// HOME_APP=1 lets it save settings without the internal token. Public pool keeps token auth.
	adminToken := os.Getenv("INTERNAL_API_TOKEN")
	if os.Getenv("HOME_APP") != "1" {
		if adminToken == "" || subtle.ConstantTimeCompare([]byte(c.Get("Authorization")), []byte("Bearer "+adminToken)) != 1 {
			return c.Status(401).JSON(fiber.Map{"success": false, "error": "Unauthorized"})
		}
	}

	var input struct {
		PoolAddress       string `json:"pool_address"`
		PayoutAddress1175 string `json:"payout_address_1175"`
		CoinbaseTag       string `json:"coinbase_tag"`
		// Optional: absent leaves the payout mode as it is, so a page that predates TIDES
		// can still save the other settings.
		PayoutMode *string `json:"payout_mode"`
	}
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid request body"})
	}

	// Load current DB values so a partial save (e.g. only the coinbase tag) preserves the rest.
	// Only the BCH2 address and the minimum are carried forward on a partial save; the two
	// optional fields are cleared by a blank, see below.
	curPool, _, _, err := stats.GetPoolConfig()
	if err != nil {
		return settingsUnreadable(c, err)
	}

	// BCH2 payout address: validate with the full CashAddr checksum validator. Blank keeps
	// the current value (does NOT clear a configured address).
	poolAddr := strings.TrimSpace(input.PoolAddress)
	if poolAddr != "" {
		if !isValidBCH2Address(poolAddr) {
			return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid BCH2 payout address: it must be a mainnet bitcoincashii: P2PKH address (starting bitcoincashii:q)"})
		}
		// Store the canonical lowercase form. isValidBCH2Address lowercases before
		// checking, so an upper/mixed-case CashAddr (what a QR scan or some wallets hand
		// you) validates here -- but the stratum's local CashAddr backstop is
		// case-sensitive and would reject the stored string, leaving mining paused with a
		// payout address the dashboard shows as accepted.
		poolAddr = strings.ToLower(poolAddr)
	} else {
		poolAddr = curPool
	}

	// 1175 (ESF) merge-mining payout address: validate as bech32 esf1…. Blank CLEARS it,
	// which is the only way to turn merge mining off -- previously the server substituted
	// the current value, answered "Settings saved", and the old address sprang straight back
	// into the field, so a user who no longer controlled that address had no path at all.
	payout1175 := strings.TrimSpace(input.PayoutAddress1175)
	if payout1175 != "" && !isValid1175Address(payout1175) {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid 1175 (ESF) address: it must be a valid esf1… address"})
	}

	// Blank clears the tag back to the default rather than silently restoring the old one.
	tag := sanitizeTagAPI(input.CoinbaseTag)
	if strings.TrimSpace(input.CoinbaseTag) == "" {
		tag = ""
	}

	mode := ""
	if input.PayoutMode != nil {
		mode = strings.ToLower(strings.TrimSpace(*input.PayoutMode))
		if !stats.ValidPayoutMode(mode) {
			return c.Status(400).JSON(fiber.Map{"success": false, "error": "Payout mode must be solo or tides"})
		}
		// TIDES credits the payout address in the pool's share log: without one there is
		// nothing to credit, and nothing to mine to in solo either.
		if mode == stats.PayoutModeTides && poolAddr == "" {
			return c.Status(400).JSON(fiber.Map{"success": false, "error": "Set your BCH2 payout address before choosing TIDES: it is the address TIDES pays"})
		}
	}

	// One write: the page says nothing was saved when this fails, and that must be so.
	if err := stats.SavePoolSettings(poolAddr, payout1175, tag, mode); err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": "Failed to save the settings: " + err.Error()})
	}
	// The stratum reads settings every 8 s and uses them from its next job (at least every 15 s).
	msg := "Settings saved. Mining uses them within half a minute (the next job); no restart needed."
	if mode == stats.PayoutModeTides {
		msg = "Settings saved. TIDES mode starts within half a minute (the next job): the blocks your install finds pay " +
			"everyone with work in Forge Pool's TIDES window, and you are paid from every TIDES block found while you have work in it."
		if mergeMiningAvailable() {
			msg += " 1175 merge-mining is off in TIDES mode."
		}
	}
	return c.JSON(fiber.Map{"success": true, "message": msg})
}

// loadMinerSettingsFromDB loads all miner settings from database into memory
func loadMinerSettingsFromDB() {
	dbSettings := stats.LoadAllMinerSettings()
	settingsMu.Lock()
	defer settingsMu.Unlock()

	for addr, s := range dbSettings {
		minerSettings[addr] = MinerSetting{
			Address:     s.Address,
			SoloMining:  s.SoloMining,
			ManualDiff:  s.ManualDiff,
			Address1175: s.Address1175,
		}
	}
}

// PIN brute-force lockout: after too many wrong PINs for an address, lock the sensitive
// (1175-address) path for a cooldown. bcrypt already makes each guess ~expensive; this
// caps online guessing of short PINs.
var (
	pinFailMu    sync.Mutex
	pinFailCount = map[string]int{}
	pinFailUntil = map[string]time.Time{}

	// bcryptSem bounds concurrent bcrypt operations (each ~100ms of CPU) so a flood of
	// PIN checks/registrations cannot saturate all cores.
	bcryptSem = make(chan struct{}, 8)
)

const (
	pinMaxFails   = 5
	pinLockoutDur = 15 * time.Minute
	pinMinLen     = 6
	pinMaxLen     = 64 // bcrypt only hashes the first 72 bytes; keep PINs well under that
)

// pinBeginAttempt atomically records a PIN attempt for the address and reports whether
// it is allowed. The count is incremented BEFORE the (slow) bcrypt compare, so a burst
// of concurrent requests cannot all slip past the cap before any of them is counted — at
// most pinMaxFails compares run before the address locks. A correct PIN later calls
// pinClearFail to reset the count.
func pinBeginAttempt(address string) bool {
	pinFailMu.Lock()
	defer pinFailMu.Unlock()
	if until, ok := pinFailUntil[address]; ok {
		if time.Now().Before(until) {
			return false
		}
		delete(pinFailUntil, address)
		delete(pinFailCount, address)
	}
	pinFailCount[address]++
	if pinFailCount[address] > pinMaxFails {
		pinFailUntil[address] = time.Now().Add(pinLockoutDur)
		return false
	}
	return true
}

func pinClearFail(address string) {
	pinFailMu.Lock()
	defer pinFailMu.Unlock()
	delete(pinFailCount, address)
	delete(pinFailUntil, address)
}

// bcryptCompareLimited / bcryptGenerateLimited wrap the CPU-bound bcrypt calls with the
// concurrency semaphore so a request flood cannot exhaust CPU.
func bcryptCompareLimited(hash, pw []byte) error {
	bcryptSem <- struct{}{}
	defer func() { <-bcryptSem }()
	return bcrypt.CompareHashAndPassword(hash, pw)
}

func bcryptGenerateLimited(pw []byte) ([]byte, error) {
	bcryptSem <- struct{}{}
	defer func() { <-bcryptSem }()
	return bcrypt.GenerateFromPassword(pw, bcrypt.DefaultCost)
}

func saveMinerSettings(c *fiber.Ctx) error {
	var settings MinerSetting
	if err := c.BodyParser(&settings); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
	}

	if settings.Address == "" {
		return c.Status(400).JSON(fiber.Map{"error": "Address required"})
	}

	// MEDIUM FIX: Validate address format before processing
	if !isValidBCH2Address(settings.Address) {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid BCH2 address format"})
	}

	// Validate the 1175 merge-mining payout address when supplied (the get-started
	// UI requires it; other callers may omit it and keep any previously saved one).
	if settings.Address1175 != "" && !isValid1175Address(settings.Address1175) {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid 1175 address (expected esf1...)"})
	}

	// Check for admin token (allows pool operator to manage any miner settings).
	// Constant-time compare so the token cannot be recovered via response timing.
	adminToken := os.Getenv("INTERNAL_API_TOKEN")
	authHeader := c.Get("Authorization")
	isAdmin := adminToken != "" && subtle.ConstantTimeCompare([]byte(authHeader), []byte("Bearer "+adminToken)) == 1
	// The home app is single-tenant behind Umbrel's own auth, so its dashboard IS the
	// admin (same model as savePoolConfig); HOME_APP=1 authorizes sensitive changes.
	authorized := isAdmin || os.Getenv("HOME_APP") == "1"

	// Only a change to the fund-critical, redirectable 1175 payout address (or setting a
	// PIN) is "sensitive" and needs proof-of-control. Mode and difficulty stay open
	// (griefing at worst, never fund loss) so rental miners onboard with no friction.
	settingsMu.RLock()
	oldS, hadOld := minerSettings[settings.Address]
	settingsMu.RUnlock()
	old1175 := ""
	if hadOld {
		old1175 = strings.TrimSpace(oldS.Address1175)
	}
	new1175 := strings.TrimSpace(settings.Address1175)
	// Empty 1175 means "keep whatever is stored" — a blank field never CLEARS a saved
	// payout address (avoids an accidental wipe, and removes a griefing vector). For a
	// non-empty value, persist the TRIMMED form so the sensitivity decision and the stored
	// bytes cannot diverge (a whitespace-padded copy is not a "new" address).
	if new1175 == "" {
		settings.Address1175 = old1175
	} else {
		settings.Address1175 = new1175
	}
	changing1175 := new1175 != "" && new1175 != old1175

	pinHash, pinErr := stats.GetSettingsPinHash(settings.Address)
	hasPin := pinHash != ""
	pin := strings.TrimSpace(settings.Pin)
	registeringPin := !hasPin && pin != ""
	sensitive := changing1175 || registeringPin

	// Fail CLOSED: if we cannot read the PIN state, never treat a sensitive change as
	// unprotected. Deny (retryable) rather than silently proceeding as "no PIN".
	if !authorized && sensitive && pinErr != nil {
		return c.Status(503).JSON(fiber.Map{"success": false, "error": "Temporarily unavailable",
			"message": "Can't verify your PIN right now. Try again in a moment."})
	}

	// No trust-on-first-use: an unauthorized caller may NOT claim/redirect a fund-critical
	// 1175 payout address (or register its PIN) when no PIN exists yet. This closes the
	// LAN hijack where a single request set both a new address and a new PIN with no proof
	// of control. In the home app this never triggers (HOME_APP=1 → authorized); a public
	// deployment must set the first-time address via the admin token.
	if !authorized && sensitive && !hasPin {
		return c.Status(401).JSON(fiber.Map{"success": false, "error": "Not authorized",
			"message": "Set your 1175 payout address from the app's own settings."})
	}

	// Validate a newly-set PIN's length before doing any expensive/persisting work.
	if registeringPin && (len(pin) < pinMinLen || len(pin) > pinMaxLen) {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid PIN length",
			"message": fmt.Sprintf("Choose a PIN of %d–%d characters.", pinMinLen, pinMaxLen)})
	}

	// Authorize a sensitive change: admin always; otherwise the PIN once one is set. Before
	// a PIN exists the first setter claims the address (trust-on-first-use) — the accepted,
	// keyless residual: an unclaimed public address can be claimed by whoever sets a PIN
	// first (logged; admin-resettable).
	if !authorized && sensitive && hasPin {
		if !pinBeginAttempt(settings.Address) {
			return c.Status(429).JSON(fiber.Map{"success": false, "error": "Too many attempts",
				"message": "Too many incorrect PINs. Please wait 15 minutes and try again."})
		}
		if bcryptCompareLimited([]byte(pinHash), []byte(pin)) != nil {
			return c.Status(403).JSON(fiber.Map{"success": false, "error": "Wrong PIN",
				"message": "That PIN is incorrect. Enter the PIN you set to protect your 1175 payout address."})
		}
		pinClearFail(settings.Address)
	}

	// Pre-compute the new PIN hash (if registering) BEFORE persisting anything, so a bcrypt
	// failure aborts cleanly instead of saving settings and silently skipping the PIN.
	var newPinHash string
	if !authorized && registeringPin {
		h, herr := bcryptGenerateLimited([]byte(pin))
		if herr != nil {
			return c.Status(500).JSON(fiber.Map{"success": false, "error": "Could not set PIN",
				"message": "Something went wrong setting your PIN. Please try again."})
		}
		newPinHash = string(h)
	}

	// Validate numeric parameters (check for NaN, Inf, and bounds)
	if math.IsNaN(settings.ManualDiff) || math.IsInf(settings.ManualDiff, 0) {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid manual difficulty value"})
	}
	if settings.ManualDiff < 0 {
		return c.Status(400).JSON(fiber.Map{"error": "Manual difficulty cannot be negative"})
	}
	if settings.ManualDiff > 1e15 {
		return c.Status(400).JSON(fiber.Map{"error": "Manual difficulty too high"})
	}

	settingsMu.Lock()
	defer settingsMu.Unlock()

	// Check cooldown (15 minutes to prevent rapid mode switching)
	cooldownDuration := 15 * time.Minute
	if lastChange, exists := settingsLastChange[settings.Address]; exists {
		timeSince := time.Since(lastChange)
		if timeSince < cooldownDuration {
			remaining := cooldownDuration - timeSince
			return c.Status(429).JSON(fiber.Map{
				"error":     "Please wait before changing settings again",
				"remaining": int(remaining.Minutes()),
				"message":   fmt.Sprintf("You can change settings again in %d minutes", int(remaining.Minutes())+1),
			})
		}
	}

	// Check if solo mode is actually changing
	oldSettings, hadOldSettings := minerSettings[settings.Address]
	modeChanged := !hadOldSettings || oldSettings.SoloMining != settings.SoloMining

	minerSettings[settings.Address] = settings

	// Save to database for persistence
	dbSettings := &stats.MinerSettings{
		Address:     settings.Address,
		SoloMining:  settings.SoloMining,
		ManualDiff:  settings.ManualDiff,
		Address1175: settings.Address1175,
	}
	if err := stats.SaveMinerSettings(dbSettings); err != nil {
		// Log but don't fail - memory is already updated
		fmt.Printf("Warning: failed to persist settings to database: %v\n", err)
	}

	// Register the newly-set PIN (hash pre-computed above) once the settings row exists.
	if newPinHash != "" {
		if serr := stats.SetSettingsPinHash(settings.Address, newPinHash); serr != nil {
			log.Printf("Warning: failed to set settings PIN for %s: %v", settings.Address, serr)
			// Settings persisted, but the PIN did not — report failure so the user retries
			// rather than believing their address is protected when it is not.
			return c.Status(500).JSON(fiber.Map{"success": false, "error": "PIN not set",
				"message": "Your settings were saved but the PIN could not be set. Please try setting your PIN again."})
		}
		log.Printf("🔒 settings PIN set for %s", settings.Address)
	}
	// Log any change to the fund-critical 1175 payout address for detectability.
	if changing1175 {
		log.Printf("🔁 1175 payout address changed for %s: %q -> %q from %s", settings.Address, old1175, new1175, c.IP())
	}

	// Only update cooldown if mode changed
	if modeChanged {
		settingsLastChange[settings.Address] = time.Now()
	}

	return c.JSON(fiber.Map{"success": true, "message": "Settings saved"})
}

func getMinerSettingsAPI(c *fiber.Ctx) error {
	address, _ := url.QueryUnescape(c.Params("address"))
	if !isValidBCH2Address(address) {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid BCH2 address format"})
	}

	settingsMu.RLock()
	settings, exists := minerSettings[address]
	settingsMu.RUnlock()

	pinHash, pinErr := stats.GetSettingsPinHash(address)
	// On a read error, assume protected (has_pin=true) so the UI never tells a protected
	// miner they're unprotected; the save path is independently fail-closed on the same error.
	hasPin := pinErr != nil || pinHash != ""

	if !exists {
		// Default to solo mode for solo-only pools
		return c.JSON(fiber.Map{
			"exists":      false,
			"solo_mining": true,
			"manual_diff": 0.0,
			"vardiff":     true,
			"has_pin":     hasPin,
		})
	}

	return c.JSON(fiber.Map{
		"exists":       true,
		"address":      settings.Address,
		"solo_mining":  settings.SoloMining,
		"manual_diff":  settings.ManualDiff,
		"vardiff":      settings.ManualDiff == 0,
		"address_1175": settings.Address1175,
		"has_pin":      hasPin,
	})
}

func getMinerBlocks(c *fiber.Ctx) error {
	address, _ := url.QueryUnescape(c.Params("address"))
	if !isValidBCH2Address(address) {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid BCH2 address format"})
	}

	// Fetch PPLNS block contributions from stratum internal endpoint
	normalizedAddr := normalizeAddress(address)
	resp, err := internalAPIGet(stratumURL + "/internal/miner-contributions?miner=" + url.QueryEscape(normalizedAddr))
	if err != nil {
		return c.JSON(fiber.Map{"blocks": []fiber.Map{}, "total": 0})
	}
	defer resp.Body.Close()

	var data struct {
		Contributions []struct {
			Height   int64   `json:"height"`
			Amount   float64 `json:"amount"`
			SharePct float64 `json:"share_pct"`
			Time     int64   `json:"time"`
			IsPaid   bool    `json:"is_paid"`
		} `json:"contributions"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return c.JSON(fiber.Map{"blocks": []fiber.Map{}, "total": 0})
	}

	// Convert to blocks format for frontend (BCH2 = the primary chain)
	blocks := make([]fiber.Map, 0, len(data.Contributions))
	for _, c := range data.Contributions {
		blocks = append(blocks, fiber.Map{
			"height":    c.Height,
			"reward":    c.Amount,
			"share_pct": c.SharePct,
			"time":      c.Time,
			"is_paid":   c.IsPaid,
			"coin":      "BCH2",
		})
	}

	// Merge in this miner's 1175 (ESF) PPLNS blocks — same table, tagged by coin.
	if aux, err := stats.Get1175BlocksForMiner(normalizedAddr, false, 50); err == nil {
		for _, b := range aux {
			blocks = append(blocks, fiber.Map{
				"height":    b.Height,
				"reward":    b.Reward,
				"share_pct": b.SharePct,
				"time":      b.Time,
				"coin":      "1175",
				"status":    b.Status,
			})
		}
	}

	return c.JSON(fiber.Map{"blocks": blocks, "total": data.Total})
}

func getMinerSoloBlocks(c *fiber.Ctx) error {
	address, _ := url.QueryUnescape(c.Params("address"))
	if !isValidBCH2Address(address) {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid BCH2 address format"})
	}

	// Fetch BCH2 solo blocks found by this miner (from stratum internal endpoint).
	normalizedAddr := normalizeAddress(address)
	data, err := internalFigures(stratumURL + "/internal/miner-solo-blocks?miner=" + url.QueryEscape(normalizedAddr))
	if err != nil {
		return figuresUnavailable(c, err)
	}

	// Tag existing (BCH2) solo blocks, then merge in this miner's 1175 (ESF) solo
	// blocks — same table, tagged by coin.
	blocks, _ := data["blocks"].([]interface{})
	for _, item := range blocks {
		if m, ok := item.(map[string]interface{}); ok {
			m["coin"] = "BCH2"
		}
	}
	if aux, err := stats.Get1175BlocksForMiner(normalizedAddr, true, 50); err == nil {
		for _, b := range aux {
			blocks = append(blocks, map[string]interface{}{
				"height":    b.Height,
				"hash":      b.Hash,
				"reward":    b.Reward,
				"time":      b.Time,
				"confirmed": b.Status == "confirmed",
				// Carried so the dashboard can distinguish an orphaned aux block from a
				// won one. Without it the renderer showed a block the chain had discarded
				// as found, "Paid by coinbase", and counted its reward in the total.
				"status": b.Status,
				"coin":   "1175",
			})
		}
	}
	data["blocks"] = blocks
	// What all of them come to: the list above holds only the latest 50.
	if n, paid, err := stats.Miner1175Totals(normalizedAddr, true); err == nil {
		data["total1175"], data["totalReward1175"] = n, paid
	}

	return c.JSON(data)
}

// internalFigures reads one of the stratum's internal figures endpoints.
func internalFigures(u string) (map[string]interface{}, error) {
	resp, err := internalAPIGet(u)
	if err != nil {
		return nil, errMiningServiceSilent
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusServiceUnavailable {
		return nil, errDatabaseSilent
	}
	var data map[string]interface{}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&data) != nil || data == nil {
		return nil, errMiningServiceSilent
	}
	return data, nil
}

var (
	errMiningServiceSilent = errors.New("the mining service is not answering")
	errDatabaseSilent      = errors.New("the database is not answering")
)

// figuresUnavailable answers 503 for figures that could not be read, saying which part did not
// answer. Answering with empty lists and zeros, as before, put "No blocks found yet" and 0.00 on
// the dashboard of a miner with blocks whenever the database or the mining service restarted.
func figuresUnavailable(c *fiber.Ctx, err error) error {
	return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "These figures cannot be shown right now: " + err.Error() + "."})
}

func getMinerPayouts(c *fiber.Ctx) error {
	address, _ := url.QueryUnescape(c.Params("address"))
	if !isValidBCH2Address(address) {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid BCH2 address format"})
	}

	// Fetch from stratum internal endpoint (use normalized address for lookup)
	normalizedAddr := normalizeAddress(address)
	payoutsURL := fmt.Sprintf("%s/internal/miner-payouts?miner=%s", stratumURL, url.QueryEscape(normalizedAddr))
	resp, err := internalAPIGet(payoutsURL)
	if err != nil {
		return c.JSON(fiber.Map{
			"address":   address,
			"payouts":   []interface{}{},
			"total":     0,
			"totalPaid": 0,
		})
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil || data == nil {
		return c.JSON(fiber.Map{
			"address":   address,
			"payouts":   []interface{}{},
			"total":     0,
			"totalPaid": 0,
		})
	}
	// A list, also when the stratum has none (it said null).
	payouts := data["payouts"]
	if payouts == nil {
		payouts = []interface{}{}
	}

	return c.JSON(fiber.Map{
		"address":   address,
		"payouts":   payouts,
		"total":     data["total"],
		"totalPaid": data["totalPaid"],
	})
}

func getMinerSoloPayouts(c *fiber.Ctx) error {
	address, _ := url.QueryUnescape(c.Params("address"))
	if !isValidBCH2Address(address) {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid BCH2 address format"})
	}

	// Fetch from stratum internal endpoint (use normalized address for lookup)
	normalizedAddr := normalizeAddress(address)
	data, err := internalFigures(fmt.Sprintf("%s/internal/miner-solo-payouts?miner=%s", stratumURL, url.QueryEscape(normalizedAddr)))
	if err != nil {
		return figuresUnavailable(c, err)
	}

	return c.JSON(fiber.Map{
		"address":   address,
		"payouts":   data["payouts"],
		"total":     data["total"],
		"totalPaid": data["totalPaid"],
	})
}

func getNetworkInfo(c *fiber.Ctx) error {
	heightResult, _ := rpcCall("getblockcount", []interface{}{})
	var height int64
	json.Unmarshal(heightResult, &height)

	diffResult, _ := rpcCall("getdifficulty", []interface{}{})
	var difficulty float64
	json.Unmarshal(diffResult, &difficulty)

	// Calculate current halving epoch and next halving block
	currentEpoch := height / halvingInterval
	nextHalvingBlock := (currentEpoch + 1) * halvingInterval
	blocksToHalving := nextHalvingBlock - height

	// Calculate current block reward (halves every halvingInterval blocks)
	reward := 50.0
	for i := int64(0); i < currentEpoch; i++ {
		reward /= 2
	}

	return c.JSON(fiber.Map{
		"height":          height,
		"difficulty":      difficulty,
		"reward":          reward,
		"halvingInterval": halvingInterval,
		"halvingBlock":    nextHalvingBlock,
		"blocksToHalving": blocksToHalving,
		"halvingEpoch":    currentEpoch,
	})
}

func validateAddress(c *fiber.Ctx) error {
	address := c.Query("address")
	if address == "" {
		return c.JSON(fiber.Map{"valid": false, "error": "No address provided"})
	}

	result, err := rpcCall("validateaddress", []interface{}{address})
	if err != nil {
		return c.JSON(fiber.Map{"valid": false, "error": err.Error()})
	}

	var validResult struct {
		IsValid bool `json:"isvalid"`
	}
	json.Unmarshal(result, &validResult)

	return c.JSON(fiber.Map{"valid": validResult.IsValid})
}

// validate1175Address checks a 1175 merge-mining payout address (bech32 esf1...).
func validate1175Address(c *fiber.Ctx) error {
	address, _ := url.QueryUnescape(c.Query("address"))
	if address == "" {
		return c.JSON(fiber.Map{"valid": false, "error": "No address provided"})
	}
	return c.JSON(fiber.Map{"valid": isValid1175Address(address)})
}

// healthCheck returns the health status of the API including database connectivity
func healthCheck(c *fiber.Ctx) error {
	dbConnected := stats.IsDBConnected()

	settingsMu.RLock()
	settingsCount := len(minerSettings)
	settingsMu.RUnlock()

	status := "healthy"
	if !dbConnected {
		status = "degraded"
	}

	out := fiber.Map{
		"status":          status,
		"db_connected":    dbConnected,
		"settings_loaded": settingsCount,
	}
	// The move of the earlier version's data, when the dashboard has to say something about it.
	if m := migrationNote(migrationDB); m != nil {
		out["migration"] = m
	}
	return c.JSON(out)
}

// getNodeStatus returns BCH2 node sync status
// getMiningStatus proxies the stratum's own view of whether it is producing work.
// The dashboard cannot infer this: a synced node and a saved payout address are both
// necessary and neither is sufficient, and the gap between them is what a user
// experiences as "it syncs to 100% but never hashes".
func getMiningStatus(c *fiber.Ctx) error {
	resp, err := internalAPIGet(stratumURL + "/internal/mining-status")
	if err != nil {
		return c.JSON(fiber.Map{
			"mining":  false,
			"reason":  "stratum_unreachable",
			"message": "The mining service is not responding. If it was just updated, give it a minute; otherwise restart the app.",
		})
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return c.JSON(fiber.Map{
			"mining":  false,
			"reason":  "stratum_unreachable",
			"message": "The mining service is not responding. If it was just updated, give it a minute; otherwise restart the app.",
		})
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return c.JSON(fiber.Map{
			"mining":  false,
			"reason":  "stratum_unreachable",
			"message": "The mining service is not responding. If it was just updated, give it a minute; otherwise restart the app.",
		})
	}
	c.Set("Content-Type", "application/json")
	return c.Send(body)
}

// syncedHeaderSlack is how many blocks behind its own header chain a node may be and still
// count as synced.
//
// Strict equality flaps: headers arrive before the block is connected, so every new block
// opens a window where blocks == headers-1 and the dashboard would announce "syncing, you
// can mine once it reaches 100%" on a node that is mining perfectly. It also sticks
// permanently if the node ever holds a header it never connects. One block of slack keeps
// the check meaningful -- a genuinely catching-up node is many blocks behind -- without
// either failure.
const syncedHeaderSlack = 1

func getNodeStatus(c *fiber.Ctx) error {
	result, err := rpcCall("getblockchaininfo", []interface{}{})
	if err != nil {
		// A node that is starting answers with -28 and says what it is doing. Anything else (no
		// connection, an answer that is not the node's, a refused password) is a node that is not
		// answering: stopped, crashed, or being started again. Shown as "starting", that read the
		// same for hours as a node half a minute into its start.
		var re *rpcError
		if errors.As(err, &re) && re.code == rpcInWarmup {
			return c.JSON(fiber.Map{"status": "starting", "message": re.message})
		}
		return c.JSON(fiber.Map{
			"status":  "offline",
			"message": "The BCH2 node is not answering",
		})
	}

	var info struct {
		Blocks               int64   `json:"blocks"`
		Headers              int64   `json:"headers"`
		VerificationProgress float64 `json:"verificationprogress"`
		InitialBlockDownload bool    `json:"initialblockdownload"`
	}
	if err := json.Unmarshal(result, &info); err != nil {
		return c.JSON(fiber.Map{
			"status":  "offline",
			"message": "The BCH2 node's answer could not be read",
		})
	}

	// Synced = the node itself says IBD is over AND it has connected every header it
	// knows about. Deliberately NOT a verificationprogress threshold: that figure is an
	// ESTIMATE derived from the hardcoded chainTxData tx-rate assumption, which on a
	// young low-volume fork chain can sit just under 1.0 at the actual tip -- pinning the
	// dashboard to "syncing, you can mine once it reaches 100%" on a node that is fully
	// synced and, in fact, already mining. IBD is also exactly what the node gates
	// getblocktemplate on, so the banner now agrees with the mining path instead of
	// contradicting it. Progress is still reported, for the bar.
	if info.InitialBlockDownload || info.Headers <= 0 || info.Blocks < info.Headers-syncedHeaderSlack {
		return c.JSON(fiber.Map{
			"status":   "syncing",
			"blocks":   info.Blocks,
			"headers":  info.Headers,
			"progress": info.VerificationProgress,
			"message":  fmt.Sprintf("Syncing: %.2f%%", info.VerificationProgress*100),
		})
	}

	return c.JSON(fiber.Map{
		"status":  "synced",
		"blocks":  info.Blocks,
		"message": fmt.Sprintf("Synced at block %d", info.Blocks),
	})
}
