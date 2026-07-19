package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bch2/forge-pool/internal/stats"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
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
	webRoot          string = "./web/dist" // Web UI root directory, configurable via WEB_ROOT env
	halvingInterval  int64  = 210000       // BCH2 halving interval, configurable via HALVING_INTERVAL env

	// Block cache to avoid N+1 RPC queries
	blockCache   = make(map[int64]*CachedBlock)
	blockCacheMu sync.RWMutex

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

// verifyCashAddrChecksum verifies the CashAddr checksum
func verifyCashAddrChecksum(prefix string, payload []int) bool {
	prefixVals := prefixToValues(prefix)
	combined := append(prefixVals, payload...)
	return cashAddrPolymod(combined) == 0
}

// isValid1175Address validates a 1175 payout address: a bech32 address with the
// mainnet HRP "esf" and a valid checksum (esf1...). This is the address a miner
// supplies to receive merge-mined 1175 rewards.
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
	MinPayout   float64 `json:"min_payout"`
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

	zapLogger.Info("🔥 Forge Pool API Server")

	// Initialize database connection for settings persistence
	dbConnStr := stats.GetDBConnStr()
	if err := stats.InitDBWithRetry(dbConnStr, 30, 2*time.Second); err != nil {
		zapLogger.Warn("Database not available, settings will not persist", zap.Error(err))
	} else {
		zapLogger.Info("✅ Connected to PostgreSQL database")
		// Load miner settings from database
		loadMinerSettingsFromDB()
		// Periodically reload miner settings from database (every 10 seconds)
		// This ensures rental service's solo_mining updates are reflected
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
		AppName: "Forge Pool API",
	})

	app.Use(logger.New())

	// Configure CORS - MUST set CORS_ORIGINS env var in production
	// Example: CORS_ORIGINS="https://pool.example.com,https://www.pool.example.com"
	corsOrigins := os.Getenv("CORS_ORIGINS")
	if corsOrigins == "" {
		// Default to localhost only - MUST be configured for production
		corsOrigins = "http://localhost:3000,http://127.0.0.1:3000"
		log.Println("WARNING: CORS_ORIGINS not set, defaulting to localhost only. Set CORS_ORIGINS env var for production.")
	}
	app.Use(cors.New(cors.Config{
		AllowOrigins:     corsOrigins,
		AllowMethods:     "GET,POST,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,Authorization",
		AllowCredentials: false,
		MaxAge:           3600,
	}))

	// Rate limiting: 1000 requests per minute per IP
	apiRateMax := 6000
	if v := os.Getenv("API_RATE_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			apiRateMax = n
		}
	}
	app.Use(limiter.New(limiter.Config{
		Max:        apiRateMax,
		Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			// Behind the app's nginx, c.IP() is always the proxy address, so all clients
			// would share one bucket. nginx sets X-Real-IP to the true client; prefer it
			// (fall back to the first X-Forwarded-For hop, then the direct IP).
			if ip := c.Get("X-Real-IP"); ip != "" {
				return ip
			}
			if xff := c.Get("X-Forwarded-For"); xff != "" {
				if i := strings.IndexByte(xff, ','); i > 0 {
					return strings.TrimSpace(xff[:i])
				}
				return strings.TrimSpace(xff)
			}
			return c.IP()
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error": "Rate limit exceeded. Please try again later.",
			})
		},
	}))

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
	api.Get("/workers", getAllWorkers)
	api.Get("/validate-address", validateAddress)
	api.Get("/validate-1175-address", validate1175Address)
	api.Get("/health", healthCheck)
	api.Get("/node-status", getNodeStatus)
	api.Get("/pool/config", getPoolConfig)
	api.Post("/pool/config", savePoolConfig)

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
		return c.SendString(fmt.Sprintf(`# HELP pool_hashrate_ths Pool hashrate in TH/s
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

# HELP pool_blocks_found Total blocks found by pool
# TYPE pool_blocks_found counter
pool_blocks_found %d

# HELP network_block_height Current network block height
# TYPE network_block_height gauge
network_block_height %d

# HELP pool_uptime_seconds Pool uptime in seconds
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
	app.Get("/blocks", func(c *fiber.Ctx) error {
		return c.SendFile(webRoot + "/blocks.html")
	})

	// Static files - serve from web directory
	app.Static("/", webRoot)

	// Fallback - serve index.html for SPA routing
	app.Use(func(c *fiber.Ctx) error {
		// Don't override API routes
		if len(c.Path()) > 4 && c.Path()[:4] == "/api" {
			return c.Status(404).JSON(fiber.Map{"error": "Not found"})
		}
		return c.SendFile(webRoot + "/index.html")
	})

	listenPort := os.Getenv("API_LISTEN_PORT")
	if listenPort == "" {
		listenPort = "8080"
	}
	go func() {
		if err := app.Listen(":" + listenPort); err != nil {
			zapLogger.Fatal("Server error", zap.Error(err))
		}
	}()

	zapLogger.Info("✅ API server running", zap.String("port", listenPort))

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	zapLogger.Info("Shutting down...")
	app.Shutdown()
}

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
		Error  interface{}     `json:"error"`
	}
	json.Unmarshal(body, &rpcResp)
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

// isActiveMiner checks if a miner has submitted shares in the last 10 minutes
// OR has a balance in the database (proving historical mining activity)
func isActiveMiner(address string) bool {
	normalizedAddr := normalizeAddress(address)

	// First check: actively mining (shares in last 10 minutes)
	workers := getStratumWorkers()
	if workers != nil {
		for _, w := range workers {
			if addressMatches(w.MinerID, address) {
				if time.Since(w.LastShareAt) < 10*time.Minute {
					return true
				}
			}
		}
	}

	// Second check: has balance in database (historical mining activity)
	// This proves they mined before and have pending rewards
	balanceURL := fmt.Sprintf("%s/internal/miner-balance?miner=%s&height=0", stratumURL, url.QueryEscape(normalizedAddr))
	resp, err := internalAPIGet(balanceURL)
	if err == nil {
		defer resp.Body.Close()
		var balanceData struct {
			MatureBalance   float64 `json:"matureBalance"`
			ImmatureBalance float64 `json:"immatureBalance"`
		}
		if json.NewDecoder(resp.Body).Decode(&balanceData) == nil {
			if balanceData.MatureBalance > 0 || balanceData.ImmatureBalance > 0 {
				return true
			}
		}
	}

	return false
}

func getPoolStats(c *fiber.Ctx) error {
	heightResult, _ := rpcCall("getblockcount", []interface{}{})
	var height int64
	json.Unmarshal(heightResult, &height)

	diffResult, _ := rpcCall("getdifficulty", []interface{}{})
	var difficulty float64
	json.Unmarshal(diffResult, &difficulty)

	nethashResult, _ := rpcCall("getnetworkhashps", []interface{}{})
	var networkHashrate float64
	json.Unmarshal(nethashResult, &networkHashrate)

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
		hashrateStr = fmt.Sprintf("%.2f TH/s", totalHashrate)
	}

	return c.JSON(fiber.Map{
		"hashrate":          hashrateStr,
		"hashrateRaw":       totalHashrate * 1e12,
		"workers":           onlineWorkers,
		"miners":            len(minerSet),
		"blocksFound":       blocksFound,
		"blocksPending":     0,
		"poolFee":           1.0,
		"soloFee":           0.0,
		"minPayout":         5.0,
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

// getBlockCached retrieves a block from cache or fetches it from node
func getBlockCached(height int64) (*CachedBlock, error) {
	// Check cache first
	blockCacheMu.RLock()
	if cached, ok := blockCache[height]; ok {
		// Cache blocks for 1 minute (recent) or forever (confirmed)
		if time.Since(cached.CachedAt) < time.Minute {
			blockCacheMu.RUnlock()
			return cached, nil
		}
	}
	blockCacheMu.RUnlock()

	// Fetch from node
	hashResult, err := rpcCall("getblockhash", []interface{}{height})
	if err != nil {
		return nil, err
	}
	var hash string
	if err := json.Unmarshal(hashResult, &hash); err != nil {
		return nil, err
	}

	blockResult, err := rpcCall("getblock", []interface{}{hash})
	if err != nil {
		return nil, err
	}
	var block struct {
		Height int64  `json:"height"`
		Hash   string `json:"hash"`
		Time   int64  `json:"time"`
		Size   int    `json:"size"`
		NumTx  int    `json:"nTx"`
	}
	if err := json.Unmarshal(blockResult, &block); err != nil {
		return nil, err
	}

	cached := &CachedBlock{
		Height:   block.Height,
		Hash:     hash,
		Time:     block.Time,
		Size:     block.Size,
		TxCount:  block.NumTx,
		CachedAt: time.Now(),
	}

	// Store in cache
	blockCacheMu.Lock()
	blockCache[height] = cached
	// Limit cache size to last 1000 blocks
	if len(blockCache) > 1000 {
		// Find and remove oldest entries
		var minHeight int64 = height
		for h := range blockCache {
			if h < minHeight {
				minHeight = h
			}
		}
		delete(blockCache, minHeight)
	}
	blockCacheMu.Unlock()

	return cached, nil
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
			"height":    b.Height,
			"hash":      b.Hash,
			"time":      b.Time,
			"miner":     "Forge Pool",
			"reward":    b.Reward,
			"confirmed": b.Status == "confirmed" || (currentHeight > 0 && currentHeight-b.Height >= 6),
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

	// Get current height
	currentHeight := int64(0)
	if heightResult, err := rpcCall("getblockcount", []interface{}{}); err == nil {
		json.Unmarshal(heightResult, &currentHeight)
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

	return c.JSON(fiber.Map{
		"address":         address,
		"hashrate5m":      totalHashrate5m,
		"hashrate60m":     totalHashrate60m,
		"workers":         workerCount,
		"onlineWorkers":   onlineWorkers,
		"validShares":     totalShares,
		"invalidShares":   totalRejected,
		"bestDiff":        bestDiff,
		"athDiff":         athDiff,
		"totalWork":       totalWork,
		"lastShare":       lastShare,
		"soloMining":      hasSettings && settings.SoloMining,
		"balance":         matureBalance + immatureBalance,
		"matureBalance":   matureBalance,
		"immatureBalance": immatureBalance,
		"currentHeight":   currentHeight,
		"paid":            0.0,
	})
}

func getMinerWorkers(c *fiber.Ctx) error {
	address, _ := url.QueryUnescape(c.Params("address"))
	if !isValidBCH2Address(address) {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid BCH2 address format"})
	}
	allWorkers := getStratumWorkers()

	var result []fiber.Map
	for _, w := range allWorkers {
		if addressMatches(w.MinerID, address) {
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
				"lastShare":     w.LastShareAt,
				"connectedAt":   w.ConnectedAt,
			})
		}
	}

	return c.JSON(fiber.Map{
		"workers": result,
	})
}

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

// Pool config file path
var configFilePath = "config.yaml"

// ConfigFile represents the config.yaml structure
type ConfigFile struct {
	Pool struct {
		Name      string  `yaml:"name"`
		Fee       float64 `yaml:"fee"`
		MinPayout float64 `yaml:"min_payout"`
		Wallet      string  `yaml:"wallet"`
		CoinbaseTag string  `yaml:"coinbase_tag"`
	} `yaml:"pool"`
	Stratum struct {
		Port       int `yaml:"port"`
		VardiffMin int `yaml:"vardiff_min"`
		VardiffMax int `yaml:"vardiff_max"`
	} `yaml:"stratum"`
	Node struct {
		Host     string `yaml:"host"`
		Port     int    `yaml:"port"`
		User     string `yaml:"user"`
		Password string `yaml:"password"`
	} `yaml:"node"`
	Database struct {
		Type string `yaml:"type"`
		Path string `yaml:"path"`
	} `yaml:"database"`
	Web struct {
		Port int `yaml:"port"`
	} `yaml:"web"`
}

func loadConfigFile() (*ConfigFile, error) {
	data, err := os.ReadFile(configFilePath)
	if err != nil {
		return nil, err
	}
	var cfg ConfigFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
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

func updateConfigFile(updates map[string]map[string]interface{}) error {
	var doc map[string]interface{}
	if data, err := os.ReadFile(configFilePath); err == nil {
		yaml.Unmarshal(data, &doc)
	}
	if doc == nil {
		doc = map[string]interface{}{}
	}
	for section, kv := range updates {
		sec, ok := doc[section].(map[string]interface{})
		if !ok {
			sec = map[string]interface{}{}
		}
		for k, v := range kv {
			sec[k] = v
		}
		doc[section] = sec
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(configFilePath, out, 0644)
}

func getPoolConfig(c *fiber.Ctx) error {
	// Dashboard-managed config is the source of truth (DB pool_config). Env vars provide the
	// initial defaults until the miner saves settings from the UI (so a fresh install shows
	// whatever was seeded in the Umbrel app config, then the DB value once configured).
	poolAddr, payout1175, tag, minPayout, err := stats.GetPoolConfig()
	if err != nil {
		poolAddr, payout1175, tag, minPayout = "", "", "", 0
	}
	if poolAddr == "" {
		poolAddr = os.Getenv("POOL_ADDRESS")
	}
	if payout1175 == "" {
		payout1175 = os.Getenv("PAYOUT_ADDRESS_1175")
	}
	if tag == "" {
		tag = os.Getenv("COINBASE_TAG")
	}
	if tag == "" {
		tag = "Forge"
	}
	if minPayout <= 0 {
		if v, perr := strconv.ParseFloat(os.Getenv("MIN_PAYOUT"), 64); perr == nil && v > 0 {
			minPayout = v
		} else {
			minPayout = 1.0
		}
	}
	return c.JSON(fiber.Map{
		"stratum_port":        3333,
		"pool_name":           "Forge Solo",
		"pool_fee":            0.0,
		"solo_fee":            0.0,
		"min_payout":          minPayout,
		"pool_address":        poolAddr,
		"payout_address_1175": payout1175,
		"coinbase_tag":        tag,
		"configured":          poolAddr != "",
	})
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
		PoolAddress       string   `json:"pool_address"`
		PayoutAddress1175 string   `json:"payout_address_1175"`
		CoinbaseTag       string   `json:"coinbase_tag"`
		MinPayout         *float64 `json:"min_payout"`
	}
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid request body"})
	}

	// Load current DB values so a partial save (e.g. only the coinbase tag) preserves the rest.
	curPool, cur1175, curTag, curMin, _ := stats.GetPoolConfig()

	// BCH2 payout address: validate with the full CashAddr checksum validator. Blank keeps
	// the current value (does NOT clear a configured address).
	poolAddr := strings.TrimSpace(input.PoolAddress)
	if poolAddr != "" {
		if !isValidBCH2Address(poolAddr) {
			return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid BCH2 payout address — must be a mainnet bitcoincashii: P2PKH address (starts with bitcoincashii:q)"})
		}
	} else {
		poolAddr = curPool
	}

	// 1175 (ESF) merge-mining payout address: validate as bech32 esf1…. Blank keeps current.
	payout1175 := strings.TrimSpace(input.PayoutAddress1175)
	if payout1175 != "" {
		if !isValid1175Address(payout1175) {
			return c.Status(400).JSON(fiber.Map{"success": false, "error": "Invalid 1175 (ESF) address — must be a valid esf1… address"})
		}
	} else {
		payout1175 = cur1175
	}

	tag := curTag
	if input.CoinbaseTag != "" {
		tag = sanitizeTagAPI(input.CoinbaseTag)
	}

	minPayout := curMin
	if input.MinPayout != nil && *input.MinPayout > 0 {
		minPayout = *input.MinPayout
	}
	if minPayout <= 0 {
		minPayout = 1.0
	}

	if err := stats.SavePoolConfig(poolAddr, payout1175, tag, minPayout); err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "error": "Failed to save config: " + err.Error()})
	}
	return c.JSON(fiber.Map{"success": true, "message": "Settings saved. Mining picks up the new payout address within a few seconds — no restart needed."})
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
			MinPayout:   s.MinPayout,
			Address1175: s.Address1175,
		}
	}
}

// verifyMinerPassword reports whether providedPassword matches the settings
// password the miner set in their own stratum config (captured by the stratum
// server at authorize, read over the internal API). Proof-of-control with no
// accounts: only whoever controls the miner knows the password. Tries the raw
// and normalized address forms so it matches however the miner authorized.
func verifyMinerPassword(address, providedPassword string) bool {
	if providedPassword == "" {
		return false
	}
	sum := sha256.Sum256([]byte(providedPassword))
	providedHash := hex.EncodeToString(sum[:])
	for _, key := range []string{address, normalizeAddress(address)} {
		resp, err := internalAPIGet(stratumURL + "/internal/miner-auth?miner=" + url.QueryEscape(key))
		if err != nil {
			continue
		}
		var data struct {
			Set  bool   `json:"set"`
			Hash string `json:"hash"`
		}
		err = json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()
		if err == nil && data.Set && data.Hash != "" &&
			subtle.ConstantTimeCompare([]byte(providedHash), []byte(data.Hash)) == 1 {
			return true
		}
	}
	return false
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
	// PIN) is "sensitive" and needs proof-of-control. Mode / difficulty / min-payout stay
	// open (griefing at worst, never fund loss) so rental miners onboard with no friction.
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
			"message": "Can't verify your PIN right now — please try again in a moment."})
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
	if math.IsNaN(settings.MinPayout) || math.IsInf(settings.MinPayout, 0) {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid minimum payout value"})
	}
	if settings.MinPayout < 0 {
		return c.Status(400).JSON(fiber.Map{"error": "Minimum payout cannot be negative"})
	}
	if settings.MinPayout > 10000 {
		return c.Status(400).JSON(fiber.Map{"error": "Minimum payout too high"})
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
		MinPayout:   settings.MinPayout,
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
	var data map[string]interface{}
	if resp, err := internalAPIGet(stratumURL + "/internal/miner-solo-blocks?miner=" + url.QueryEscape(normalizedAddr)); err == nil {
		defer resp.Body.Close()
		_ = json.NewDecoder(resp.Body).Decode(&data)
	}
	if data == nil {
		data = map[string]interface{}{}
	}

	// Tag existing (BCH2) solo blocks, then merge in this miner's 1175 (ESF) solo
	// blocks — same table, tagged by coin. Resilient: 1175 blocks still show even if
	// the stratum internal call above failed.
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
				"coin":      "1175",
			})
		}
	}
	data["blocks"] = blocks

	return c.JSON(data)
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
			"payouts":   nil,
			"total":     0,
			"totalPaid": 0,
		})
	}

	return c.JSON(fiber.Map{
		"address":   address,
		"payouts":   data["payouts"],
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
	payoutsURL := fmt.Sprintf("%s/internal/miner-solo-payouts?miner=%s", stratumURL, url.QueryEscape(normalizedAddr))
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
			"payouts":   nil,
			"total":     0,
			"totalPaid": 0,
		})
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

	return c.JSON(fiber.Map{
		"status":          status,
		"db_connected":    dbConnected,
		"settings_loaded": settingsCount,
	})
}

// getNodeStatus returns BCH2 node sync status
func getNodeStatus(c *fiber.Ctx) error {
	// Try to get blockchain info
	result, err := rpcCall("getblockchaininfo", []interface{}{})
	if err != nil {
		return c.JSON(fiber.Map{
			"status":  "offline",
			"message": "Cannot connect to BCH2 node",
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
			"message": "Invalid response from node",
		})
	}

	if info.InitialBlockDownload || info.VerificationProgress < 0.9999 {
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
