package stratum

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bch2/forge-pool/internal/mergemining"
	"go.uber.org/zap"
)

const (
	// DifficultyGracePeriod is the time window after a difficulty change
	// during which shares at the old difficulty are still accepted
	DifficultyGracePeriod = 30 * time.Second

	// MaxDifficultyMultiplier caps how much difficulty can change in one adjustment
	MaxDifficultyMultiplier = 1.5 // Max 50% change per adjustment (miningcore style)

	// VardiffVariancePercent - only adjust if outside this variance from target
	VardiffVariancePercent = 0.30 // 30% variance allowed

	// VardiffMinShares is the minimum shares needed before vardiff adjusts
	VardiffMinShares = 10

	// RecentSubmissionsWindow is the number of submissions to track for rejection rate
	RecentSubmissionsWindow = 50

	// MaxRejectionRate is the rejection rate above which difficulty is reduced
	// Set higher (30%) to allow natural statistical variance in share difficulty
	MaxRejectionRate = 0.30 // 30%

	// DifficultyReductionCooldown is how long to wait before increasing above the ceiling
	DifficultyReductionCooldown = 2 * time.Minute
)

type Server struct {
	config         *ServerConfig
	logger         *zap.Logger
	listener       net.Listener
	clients        sync.Map
	clientCount    int64
	currentJob     atomic.Value
	jobHistory     sync.Map
	extraNonce     uint32
	extraNonceMu   sync.Mutex
	shareProcessor ShareProcessor
	minerSettings  MinerSettingsStore
	authPasswords  sync.Map // minerID(address) -> sha256(settings password) hex; proof-of-control for settings changes
	diffMemory     sync.Map // minerID(address) -> diffMem: last vardiff level, reused across reconnects
	shutdownCh     chan struct{}
	stats          *ServerStats
	// Duplicate share detection
	submittedShares sync.Map // map[shareKey]time.Time
	shareCleanupMu  sync.Mutex

	// Merge mining: aux-chain (1175) submission client. nil unless enabled.
	auxClient *mergemining.Client
	// onAuxBlock, if set, is invoked when a 1175 aux block is accepted so the pool
	// can distribute the reward. Args: aux height, aux child hash, coinbase value
	// (satoshis), the finding miner, and whether that miner is solo.
	onAuxBlock func(height int64, hash string, coinbaseValueSat int64, finder string, isSolo bool)
}

// EnableMergeMining lets this server submit solved aux-chain (1175) blocks. Any
// accepted share whose parent (BCH2) header hash also meets the aux target for
// the job's committed aux work is submitted to the aux node via submitauxblock.
func (s *Server) EnableMergeMining(client *mergemining.Client) {
	s.auxClient = client
}

// SetAuxBlockHandler registers a callback invoked when a 1175 aux block is
// accepted, so the pool can distribute its reward to miners.
func (s *Server) SetAuxBlockHandler(fn func(height int64, hash string, coinbaseValueSat int64, finder string, isSolo bool)) {
	s.onAuxBlock = fn
}

// shareKey uniquely identifies a submitted share
type shareKey struct {
	JobID       string
	ExtraNonce1 string
	ExtraNonce2 string
	NTime       string
	Nonce       string
	VersionBits string
}

type ServerConfig struct {
	Host               string
	Port               int
	MaxConnections     int
	BanDuration        time.Duration
	MaxSharesPerSecond int
	VardiffEnabled     bool
	MinDiff            float64
	RentalMinDiff      float64 // Minimum difficulty for NiceHash/MRR (they require 500k+)
	RentalMaxDiff      float64 // Maximum difficulty for NiceHash/MRR (cap to prevent issues)
	MaxDiff            float64
	TargetShareTime    int
	RetargetTime       int // Seconds between vardiff adjustments
	HighHashThreshold  int
	HighHashDiff       float64
	ExtraNonce2Size    int    // Size of extranonce2 in bytes (default 4, Braiins needs 8)
	ExtraNonce1Size    int    // Size of extranonce1 in bytes (default 6)
	ServerName         string // Name for logging (e.g., "main", "braiins")
	SoloOnly           bool   // Force every miner to SOLO regardless of settings (solo-only deployments)
}

type ServerStats struct {
	TotalConnections  int64
	ActiveConnections int64
	TotalShares       int64
	ValidShares       int64
	InvalidShares     int64
	BlocksFound       int64
	SoloMiners        int64
	PPLNSMiners       int64
}

type ShareProcessor interface {
	ProcessShare(ctx context.Context, share *Share) error
	ProcessBlock(ctx context.Context, block *Block) error
}

type MinerSettingsStore interface {
	GetMinerSettings(minerID string) (*MinerSettings, error)
	SaveMinerSettings(settings *MinerSettings) error // Autosave for new miners
}

type MinerSettings struct {
	MinerID    string
	SoloMining bool
	ManualDiff float64
	MinPayout  float64
	Exists     bool // Whether settings exist in database
}

func NewServer(config *ServerConfig, logger *zap.Logger, sp ShareProcessor, ms MinerSettingsStore) *Server {
	if config.HighHashThreshold == 0 {
		config.HighHashThreshold = 10
	}
	if config.HighHashDiff == 0 {
		config.HighHashDiff = 1000000
	}
	if config.MinDiff == 0 {
		config.MinDiff = 32768
	}
	if config.RentalMinDiff == 0 {
		config.RentalMinDiff = 500000 // NiceHash/MRR require 500k+
	}

	s := &Server{
		config:         config,
		logger:         logger,
		shareProcessor: sp,
		minerSettings:  ms,
		shutdownCh:     make(chan struct{}),
		stats:          &ServerStats{},
	}

	// Start periodic share cleanup
	go s.shareCleanupLoop()
	// Self-correct any connection stuck at a too-high (e.g. resumed) difficulty.
	go s.idleDifficultyLoop()

	return s
}

// getMinDiffForClient returns the appropriate minimum difficulty based on client type
// Rental services (NiceHash/MRR) require higher minimum difficulty
func (s *Server) getMinDiffForClient(client *Client) float64 {
	client.mu.RLock()
	rental := client.RentalService
	client.mu.RUnlock()

	if rental != RentalNone {
		return s.config.RentalMinDiff
	}
	return s.config.MinDiff
}

// idleResetAfter is how long a connection may hold an above-floor difficulty without
// producing a single accepted share before it is reset to its floor. Longer than the
// expected first-share time even for a large miner at a high difficulty, so it only
// ever fires on a connection that genuinely cannot mine at its assigned level (e.g. a
// resumed difficulty that is too high for this connection's hashrate).
const idleResetAfter = 5 * time.Minute

// idleDifficultyLoop resets any connection that was handed an above-floor difficulty but
// has produced zero accepted shares within idleResetAfter, so a too-high resumed level
// self-corrects (vardiff then ramps back up from real shares) instead of stalling the
// miner. It never touches a connection at its floor or one that has submitted a share,
// so a legitimately-mining miner of any size is unaffected.
func (s *Server) idleDifficultyLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.shutdownCh:
			return
		case <-ticker.C:
		}
		now := time.Now()
		s.clients.Range(func(_, v interface{}) bool {
			c, ok := v.(*Client)
			if !ok {
				return true
			}
			c.mu.Lock()
			floor := s.config.MinDiff
			if c.RentalService != RentalNone {
				floor = s.config.RentalMinDiff
			}
			prevDiff := c.Difficulty
			connFor := now.Sub(c.ConnectedAt)
			stuck := c.Authorized && c.ValidShares == 0 &&
				prevDiff > floor && connFor > idleResetAfter
			if stuck {
				c.Difficulty = floor
			}
			minerID := c.MinerID
			c.mu.Unlock()
			if stuck {
				s.rememberDifficulty(minerID, floor) // don't re-hand the too-high level next time
				s.sendDifficulty(c, floor)
				s.logger.Info("Idle difficulty reset",
					zap.String("miner", minerID),
					zap.Float64("from_diff", prevDiff),
					zap.Float64("to_diff", floor),
					zap.Duration("connected_for", connFor))
			}
			return true
		})
	}
}

// shareCleanupLoop periodically removes old share entries
func (s *Server) shareCleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.shutdownCh:
			return
		case <-ticker.C:
			s.cleanupOldShares()
		}
	}
}

// cleanupOldShares removes shares older than 5 minutes
func (s *Server) cleanupOldShares() {
	cutoff := time.Now().Add(-5 * time.Minute)
	s.submittedShares.Range(func(key, value interface{}) bool {
		if t, ok := value.(time.Time); ok && t.Before(cutoff) {
			s.submittedShares.Delete(key)
		}
		return true
	})
}

// clearSharesForJob performs cleanup when a new job is broadcast
// SECURITY: Don't wipe all shares - this would allow replay attacks
// Instead, do time-based cleanup to remove old shares while keeping recent ones
func (s *Server) clearSharesForJob() {
	s.shareCleanupMu.Lock()
	defer s.shareCleanupMu.Unlock()
	// Only remove shares older than 2 minutes to prevent replay while allowing
	// legitimate shares from recent jobs that miners might still be working on
	cutoff := time.Now().Add(-2 * time.Minute)
	s.submittedShares.Range(func(key, value interface{}) bool {
		if t, ok := value.(time.Time); ok && t.Before(cutoff) {
			s.submittedShares.Delete(key)
		}
		return true
	})
}

// isDuplicateShare checks if this share was already submitted
func (s *Server) isDuplicateShare(jobID, en1, en2, ntime, nonce, versionBits string) bool {
	key := shareKey{
		JobID:       jobID,
		ExtraNonce1: en1,
		ExtraNonce2: en2,
		NTime:       ntime,
		Nonce:       nonce,
		// Dedup on only the BIP320-rollable version bits — the exact bits
		// buildBlockHeader combines into the header. Keying on the full submitted
		// version let a miner vary the ignored (non-rollable) bits to replay one
		// proof of work for repeated PPLNS credit; masking closes that.
		VersionBits: canonicalRolledVersion(versionBits),
	}

	_, exists := s.submittedShares.LoadOrStore(key, time.Now())
	return exists
}

// canonicalRolledVersion returns only the BIP320-rollable bits (mask 0x1fffe000)
// of the submitted version as normalized hex. Shares differing only in the
// non-rollable version bits assemble an identical header, so they must share one
// dedup key. Falls back to the raw lowercased string if unparseable.
func canonicalRolledVersion(versionBits string) string {
	if versionBits == "" {
		return ""
	}
	vb, err := hex.DecodeString(strings.ToLower(versionBits))
	if err != nil || len(vb) != 4 {
		return strings.ToLower(versionBits)
	}
	mask := []byte{0x1f, 0xff, 0xe0, 0x00}
	out := make([]byte, 4)
	for i := 0; i < 4; i++ {
		out[i] = vb[i] & mask[i]
	}
	return hex.EncodeToString(out)
}

// validateShare verifies that the submitted share meets the difficulty target
// Returns: (isValid bool, actualDifficulty float64, blockHash []byte, error)

// calculateMerkleRoot computes the merkle root from coinbase and branches
func calculateMerkleRoot(coinbase []byte, merkleBranches []string) []byte {
	result := doubleSHA256(coinbase)
	for _, branchHex := range merkleBranches {
		branch, _ := hex.DecodeString(branchHex)
		combined := make([]byte, 64)
		copy(combined[:32], result)
		copy(combined[32:], branch)
		result = doubleSHA256(combined)
	}
	return result
}

func (s *Server) validateShare(job *Job, extranonce1, extranonce2, ntime, nonce, versionBits string, targetDiff float64) (bool, float64, []byte, error) {
	// Validate hex input lengths based on configured sizes
	en1Size := s.config.ExtraNonce1Size
	if en1Size == 0 {
		en1Size = 6
	}
	en2Size := s.config.ExtraNonce2Size
	if en2Size == 0 {
		en2Size = 4
	}
	if len(extranonce1) != en1Size*2 || len(extranonce2) != en2Size*2 {
		return false, 0, nil, fmt.Errorf("invalid extranonce length (got en1=%d, en2=%d, want en1=%d, en2=%d)",
			len(extranonce1)/2, len(extranonce2)/2, en1Size, en2Size)
	}
	if len(ntime) != 8 || len(nonce) != 8 {
		return false, 0, nil, fmt.Errorf("invalid ntime/nonce length")
	}

	// Validate hex format
	if _, err := hex.DecodeString(extranonce1); err != nil {
		return false, 0, nil, fmt.Errorf("invalid extranonce1 hex")
	}
	if _, err := hex.DecodeString(extranonce2); err != nil {
		return false, 0, nil, fmt.Errorf("invalid extranonce2 hex")
	}
	if _, err := hex.DecodeString(ntime); err != nil {
		return false, 0, nil, fmt.Errorf("invalid ntime hex")
	}
	if _, err := hex.DecodeString(nonce); err != nil {
		return false, 0, nil, fmt.Errorf("invalid nonce hex")
	}

	// Build coinbase transaction
	coinbase := buildCoinbaseFromParts(job.CoinBase1, extranonce1, extranonce2, job.CoinBase2)

	// Calculate merkle root (for single tx, it's just double SHA256 of coinbase)
	merkleRoot := calculateMerkleRoot(coinbase, job.MerkleBranches)

	// Build block header (80 bytes)
	header := buildBlockHeader(job, merkleRoot, ntime, nonce, versionBits)

	// Calculate block hash (double SHA256 of header)
	blockHash := doubleSHA256(header)

	// Reverse for display (Bitcoin uses little-endian internally)
	displayHash := make([]byte, 32)
	copy(displayHash, blockHash)
	reverseBytes(displayHash)

	// Calculate difficulty from hash
	actualDiff := hashToDifficulty(blockHash)

	// Check if meets target difficulty
	isValid := actualDiff >= targetDiff

	return isValid, actualDiff, displayHash, nil
}

// auxHashMeetsTarget reports whether the parent (BCH2) block hash (big-endian
// display bytes, as returned by validateShare) is <= the aux-chain target
// (big-endian hex from getauxblock) — i.e. this share is a valid aux block.
func auxHashMeetsTarget(displayHashBE []byte, targetHexBE string) bool {
	t, ok := new(big.Int).SetString(targetHexBE, 16)
	if !ok {
		return false
	}
	return new(big.Int).SetBytes(displayHashBE).Cmp(t) <= 0
}

// submitAux rebuilds the parent coinbase + header for a winning share, assembles
// the CAuxPow proof, and submits it to the aux node. Called only when a share
// already meets the aux target, so the (small) rebuild cost is paid rarely.
func (s *Server) submitAux(job *Job, en1, en2, ntime, nonce, versionBits, finder string, isSolo bool) {
	coinbase := buildCoinbaseFromParts(job.CoinBase1, en1, en2, job.CoinBase2)
	merkleRoot := calculateMerkleRoot(coinbase, job.MerkleBranches)
	header := buildBlockHeader(job, merkleRoot, ntime, nonce, versionBits)

	// job.MerkleBranches is the coinbase's branch in internal (little-endian) order,
	// exactly what CAuxPow.vMerkleBranch expects (validated by the merkle-branch and
	// integration tests).
	auxHex, err := mergemining.AssembleAuxPowHex(coinbase, header, job.MerkleBranches)
	if err != nil {
		s.logger.Warn("aux: assemble failed", zap.Error(err))
		return
	}
	accepted, err := s.auxClient.SubmitAuxBlock(job.AuxWork.Hash, auxHex)
	if err != nil {
		s.logger.Warn("aux: submitauxblock error",
			zap.String("aux_hash", job.AuxWork.Hash), zap.Error(err))
		return
	}
	if accepted {
		s.logger.Info("🎉 AUX (1175) BLOCK FOUND",
			zap.Int64("aux_height", job.AuxWork.Height),
			zap.String("aux_hash", job.AuxWork.Hash))
		if s.onAuxBlock != nil {
			s.onAuxBlock(job.AuxWork.Height, job.AuxWork.Hash, job.AuxWork.CoinbaseValue, finder, isSolo)
		}
	} else {
		s.logger.Warn("aux: block rejected (likely stale aux tip)",
			zap.String("aux_hash", job.AuxWork.Hash))
	}
}

// buildCoinbaseFromParts constructs the coinbase transaction
func buildCoinbaseFromParts(cb1, en1, en2, cb2 string) []byte {
	cb1Bytes, _ := hex.DecodeString(cb1)
	en1Bytes, _ := hex.DecodeString(en1)
	en2Bytes, _ := hex.DecodeString(en2)
	cb2Bytes, _ := hex.DecodeString(cb2)

	var coinbase bytes.Buffer
	coinbase.Write(cb1Bytes)
	coinbase.Write(en1Bytes)
	coinbase.Write(en2Bytes)
	coinbase.Write(cb2Bytes)

	return coinbase.Bytes()
}

// buildBlockHeader constructs the 80-byte block header
func buildBlockHeader(job *Job, merkleRoot []byte, ntime, nonce, versionBits string) []byte {
	var header bytes.Buffer

	// Version (4 bytes, little-endian)
	versionBytes, _ := hex.DecodeString(job.Version)
	if versionBits != "" {
		vbBytes, _ := hex.DecodeString(versionBits)
		// BIP310 version-rolling: the miner submits the FULL rolled nVersion. Keep
		// the job's non-rollable bits and take only the masked (rollable) bits from
		// the miner. Plain XOR corrupted the header for full-version submitters
		// (NiceHash/Braiins/most ASICs), wrongly rejecting their version-rolled shares.
		versionRollingMask := []byte{0x1f, 0xff, 0xe0, 0x00}
		for i := 0; i < len(versionBytes) && i < len(vbBytes) && i < len(versionRollingMask); i++ {
			versionBytes[i] = (versionBytes[i] &^ versionRollingMask[i]) | (vbBytes[i] & versionRollingMask[i])
		}
	}
	reverseBytes(versionBytes)
	header.Write(versionBytes)

	// Previous block hash (32 bytes)
	// job.PrevBlockHash is in stratum format, need to reverse back
	prevHashBytes, _ := hex.DecodeString(job.PrevBlockHash)
	// Undo the 4-byte swap that stratum does
	for i := 0; i < 32; i += 4 {
		prevHashBytes[i], prevHashBytes[i+1], prevHashBytes[i+2], prevHashBytes[i+3] =
			prevHashBytes[i+3], prevHashBytes[i+2], prevHashBytes[i+1], prevHashBytes[i]
	}
	header.Write(prevHashBytes)

	// Merkle root (32 bytes)
	header.Write(merkleRoot)

	// Time (4 bytes, little-endian)
	ntimeBytes, _ := hex.DecodeString(ntime)
	reverseBytes(ntimeBytes)
	header.Write(ntimeBytes)

	// Bits (4 bytes, little-endian)
	bitsBytes, _ := hex.DecodeString(job.NBits)
	reverseBytes(bitsBytes)
	header.Write(bitsBytes)

	// Nonce (4 bytes, little-endian)
	nonceBytes, _ := hex.DecodeString(nonce)
	reverseBytes(nonceBytes)
	header.Write(nonceBytes)

	return header.Bytes()
}

// doubleSHA256 performs double SHA256 hash
func doubleSHA256(data []byte) []byte {
	first := sha256.Sum256(data)
	second := sha256.Sum256(first[:])
	return second[:]
}

// reverseBytes reverses a byte slice in place
func reverseBytes(b []byte) {
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
}

// hashToDifficulty converts a block hash to its difficulty value
func hashToDifficulty(hash []byte) float64 {
	// Difficulty 1 target (Bitcoin)
	// 0x00000000FFFF0000000000000000000000000000000000000000000000000000
	diff1Target := new(big.Int)
	diff1Target.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

	// Convert hash to big.Int (hash is in internal byte order, need to reverse for big.Int)
	hashReversed := make([]byte, 32)
	copy(hashReversed, hash)
	reverseBytes(hashReversed)

	hashInt := new(big.Int).SetBytes(hashReversed)

	if hashInt.Sign() == 0 {
		return 0
	}

	// Difficulty = diff1Target / hashInt
	// Use floating point for precision
	diff1Float := new(big.Float).SetInt(diff1Target)
	hashFloat := new(big.Float).SetInt(hashInt)

	result := new(big.Float).Quo(diff1Float, hashFloat)
	difficulty, _ := result.Float64()

	return difficulty
}

// difficultyToTarget converts a difficulty value to a target for comparison
func difficultyToTarget(diff float64) *big.Int {
	if diff <= 0 {
		return new(big.Int)
	}

	diff1Target := new(big.Int)
	diff1Target.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

	// Target = diff1Target / difficulty
	diff1Float := new(big.Float).SetInt(diff1Target)
	diffFloat := new(big.Float).SetFloat64(diff)

	targetFloat := new(big.Float).Quo(diff1Float, diffFloat)
	targetInt, _ := targetFloat.Int(nil)

	return targetInt
}

func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	s.listener = listener
	s.logger.Info("Stratum server started", zap.String("addr", addr))
	go s.acceptLoop()
	return nil
}

func (s *Server) Stop() {
	s.logger.Info("Initiating graceful shutdown...")

	// Signal all goroutines to stop
	close(s.shutdownCh)

	// Stop accepting new connections
	if s.listener != nil {
		s.listener.Close()
	}

	// Wait for active clients to disconnect (with timeout)
	shutdownDeadline := time.Now().Add(30 * time.Second)
	for atomic.LoadInt64(&s.clientCount) > 0 {
		if time.Now().After(shutdownDeadline) {
			s.logger.Warn("Shutdown timeout reached, forcing disconnect",
				zap.Int64("remaining_clients", atomic.LoadInt64(&s.clientCount)))
			// Force close all client connections
			s.clients.Range(func(key, value interface{}) bool {
				client := value.(*Client)
				client.Conn.Close()
				return true
			})
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	s.logger.Info("Graceful shutdown complete")
}

func (s *Server) acceptLoop() {
	for {
		select {
		case <-s.shutdownCh:
			return
		default:
		}
		conn, err := s.listener.Accept()
		if err != nil {
			continue
		}
		if atomic.LoadInt64(&s.clientCount) >= int64(s.config.MaxConnections) {
			conn.Close()
			continue
		}
		go s.handleClient(conn)
	}
}

func (s *Server) handleClient(conn net.Conn) {
	// Configure TCP connection for mining
	if tc, ok := conn.(*net.TCPConn); ok {
		// Disable Nagle algorithm for low-latency share responses
		tc.SetNoDelay(true)
		// Enable TCP keepalive to detect dead connections and prevent NAT timeout
		// This is critical for rental services (NiceHash, MRR) that may have
		// intermediate proxies/firewalls that drop idle connections
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second) // Check every 30 seconds
	}

	atomic.AddInt64(&s.clientCount, 1)
	atomic.AddInt64(&s.stats.ActiveConnections, 1)
	defer func() {
		atomic.AddInt64(&s.clientCount, -1)
		atomic.AddInt64(&s.stats.ActiveConnections, -1)
		conn.Close()
	}()

	client := &Client{
		ID:          fmt.Sprintf("%d", time.Now().UnixNano()),
		Conn:        conn,
		IP:          conn.RemoteAddr().String(),
		Difficulty:  s.config.MinDiff,
		ConnectedAt: time.Now(),
		ShareTimes:  make([]time.Time, 0, 100),
	}

	s.clients.Store(client.ID, client)
	defer s.clients.Delete(client.ID)

	// Log external connections at Info level for debugging
	if !strings.HasPrefix(client.IP, "127.0.0.1") {
		s.logger.Info("External client connected", zap.String("ip", client.IP))
	} else {
		s.logger.Debug("Client connected", zap.String("ip", client.IP))
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	// CRITICAL FIX: Read deadline to prevent Slowloris-style DoS attacks
	const readTimeout = 5 * time.Minute

	for {
		// Set read deadline before each scan to prevent connection holding
		conn.SetReadDeadline(time.Now().Add(readTimeout))

		if !scanner.Scan() {
			break
		}

		select {
		case <-s.shutdownCh:
			return
		default:
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		s.handleMessage(client, line)
	}

	// Log disconnection with details
	client.mu.RLock()
	minerID := client.MinerID
	workerName := client.WorkerName
	rental := client.RentalService
	authorized := client.Authorized
	subscribed := client.Subscribed
	difficulty := client.Difficulty
	client.mu.RUnlock()

	duration := time.Since(client.ConnectedAt)
	scanErr := scanner.Err()

	// Log external connections that never subscribed
	if !strings.HasPrefix(client.IP, "127.0.0.1") && !subscribed {
		s.logger.Warn("External client disconnected without subscribing",
			zap.String("ip", client.IP),
			zap.Duration("connected_duration", duration),
			zap.Error(scanErr))
	}

	if authorized && minerID != "" {
		s.logger.Info("Client disconnected",
			zap.String("miner", minerID),
			zap.String("worker", workerName),
			zap.String("rental_service", rental.String()),
			zap.Float64("difficulty", difficulty),
			zap.Duration("connected_duration", duration),
			zap.Error(scanErr))
	}
}

func (s *Server) handleMessage(client *Client, data []byte) {
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		s.logger.Warn("Failed to parse stratum message",
			zap.String("ip", client.IP),
			zap.Error(err),
			zap.ByteString("data", data))
		return
	}

	// Log all messages from NiceHash clients for debugging
	client.mu.RLock()
	rental := client.RentalService
	minerID := client.MinerID
	client.mu.RUnlock()
	if rental == RentalNiceHash && req.Method != "" {
		s.logger.Info("NiceHash message received",
			zap.String("miner", minerID),
			zap.String("method", req.Method))
	}

	switch req.Method {
	case MethodSubscribe:
		resp := s.handleSubscribe(client, &req)
		s.sendResponse(client, resp)
		// Difficulty will be sent after authorize
	case MethodAuthorize:
		resp := s.handleAuthorize(client, &req)
		// Send auth response FIRST
		s.sendResponse(client, resp)
		// Then send difficulty and job
		if resp.Result == true {
			// Send difficulty once, then job
			s.sendDifficulty(client, client.Difficulty)
			if job := s.currentJob.Load(); job != nil {
				// Send initial job with clean=true so miner starts fresh
				initialJob := *job.(*Job)
				initialJob.CleanJobs = true
				s.sendJob(client, &initialJob)
				s.logger.Debug("Sent initial job after auth",
					zap.String("miner", client.MinerID),
					zap.String("job_id", initialJob.ID))
			} else {
				s.logger.Warn("No current job to send after auth",
					zap.String("miner", client.MinerID))
			}
		}
	case MethodConfigure:
		s.logger.Info("mining.configure received",
			zap.String("ip", client.IP),
			zap.String("user_agent", client.UserAgent))
		resp := s.handleConfigure(client, &req)
		s.sendResponse(client, resp)
	case MethodSubmit:
		resp := s.handleSubmit(client, &req)
		s.sendResponse(client, resp)
	case "mining.suggest_difficulty":
		// Handle miner's difficulty suggestion
		var params []float64
		if err := json.Unmarshal(req.Params, &params); err == nil && len(params) > 0 {
			suggestedDiff := params[0]
			// Clamp to our min/max range (use appropriate min for client type)
			minDiff := s.getMinDiffForClient(client)
			if suggestedDiff < minDiff {
				suggestedDiff = minDiff
			}
			if suggestedDiff > s.config.MaxDiff {
				suggestedDiff = s.config.MaxDiff
			}
			client.mu.Lock()
			client.Difficulty = suggestedDiff
			client.mu.Unlock()
			s.logger.Info("Miner suggested difficulty accepted",
				zap.String("ip", client.IP),
				zap.Float64("difficulty", suggestedDiff))
			s.sendDifficulty(client, suggestedDiff)
		}
		s.sendResponse(client, &Response{ID: req.ID, Result: true})
	case "mining.extranonce.subscribe":
		// Support extranonce subscription for rental services (NiceHash, MRR)
		client.mu.Lock()
		client.SupportsExtranonce = true
		rental := client.RentalService
		client.mu.Unlock()

		if rental != RentalNone {
			s.logger.Info("Rental service subscribed to extranonce updates",
				zap.String("ip", client.IP),
				zap.String("rental_service", rental.String()))
		}
		s.sendResponse(client, &Response{ID: req.ID, Result: true})
	default:
		s.logger.Info("Ignoring unsupported stratum method",
			zap.String("method", req.Method),
			zap.String("ip", client.IP))
		s.sendResponse(client, &Response{ID: req.ID, Result: true})
	}
}

func (s *Server) handleSubscribe(client *Client, req *Request) *Response {
	client.mu.Lock()
	defer client.mu.Unlock()

	// Parse subscription params to detect user agent
	// Format: ["user-agent/version", "session-id"] or ["user-agent/version"]
	var params []interface{}
	if err := json.Unmarshal(req.Params, &params); err == nil && len(params) > 0 {
		if ua, ok := params[0].(string); ok {
			client.UserAgent = ua
			client.RentalService = detectRentalService(ua)
			// Bump difficulty to rental minimum if rental service detected
			if client.RentalService != RentalNone && client.Difficulty < s.config.RentalMinDiff {
				client.Difficulty = s.config.RentalMinDiff
			}
		}
	}

	s.extraNonceMu.Lock()
	s.extraNonce++
	// Use configured extranonce1 size (default 6 bytes = 12 hex chars)
	en1Size := s.config.ExtraNonce1Size
	if en1Size == 0 {
		en1Size = 6
	}
	en1Format := fmt.Sprintf("%%0%dx", en1Size*2)
	// Mask the counter to the configured field width so the hex string is ALWAYS
	// exactly en1Size*2 chars. %0Nx pads but never truncates, so without this, once
	// the uint32 counter passes the field max (e.g. 0xFFFF for a 2-byte field) every
	// new connection gets an over-length extranonce1 and validateShare then rejects
	// 100% of its shares. Wrap-around collisions are harmless for a home solo miner
	// (a handful of connections) since extranonce2 still differs per share.
	en1Value := s.extraNonce
	if en1Size < 4 {
		en1Value &= (uint32(1) << (uint(en1Size) * 8)) - 1
	}
	client.ExtraNonce1 = fmt.Sprintf(en1Format, en1Value)
	s.extraNonceMu.Unlock()

	// Use configured extranonce2 size (default 4, Braiins needs 8)
	client.ExtraNonce2Size = s.config.ExtraNonce2Size
	if client.ExtraNonce2Size == 0 {
		client.ExtraNonce2Size = 4
	}
	client.SubscriptionID = fmt.Sprintf("forge_%s", client.ID)
	client.Subscribed = true

	result := []interface{}{
		[][]string{
			{"mining.set_difficulty", client.SubscriptionID},
			{"mining.notify", client.SubscriptionID},
		},
		client.ExtraNonce1,
		client.ExtraNonce2Size,
	}

	// Log with rental service detection
	if client.RentalService != RentalNone {
		s.logger.Info("Rental service client subscribed",
			zap.String("ip", client.IP),
			zap.String("extranonce", client.ExtraNonce1),
			zap.String("rental_service", client.RentalService.String()),
			zap.String("user_agent", client.UserAgent))
	} else {
		s.logger.Info("Client subscribed",
			zap.String("ip", client.IP),
			zap.String("extranonce", client.ExtraNonce1),
			zap.String("user_agent", client.UserAgent))
	}

	return &Response{ID: req.ID, Result: result}
}

// detectRentalService identifies rental services from user agent string
func detectRentalService(userAgent string) RentalService {
	ua := strings.ToLower(userAgent)

	// NiceHash detection patterns
	// Examples: "NiceHash/1.0.0", "nhmp/1.0.0", "excavator/1.6.3"
	nicehashPatterns := []string{
		"nicehash",
		"nhmp",
		"excavator",
		"nh/",
	}
	for _, pattern := range nicehashPatterns {
		if strings.Contains(ua, pattern) {
			return RentalNiceHash
		}
	}

	// Mining Rig Rentals detection patterns
	// Examples: "MiningRigRentals/1.0", "mrr/", "miningrigrentals"
	mrrPatterns := []string{
		"miningrigrentals",
		"mrr/",
		"mrr-",
		"rigrentals",
	}
	for _, pattern := range mrrPatterns {
		if strings.Contains(ua, pattern) {
			return RentalMRR
		}
	}

	// Generic rental/proxy indicators
	rentalPatterns := []string{
		"rental",
		"proxy",
		"stratum-proxy",
	}
	for _, pattern := range rentalPatterns {
		if strings.Contains(ua, pattern) {
			return RentalOther
		}
	}

	return RentalNone
}

// GetAuthPasswordHash returns the hex sha256 of the settings password the miner
// set at authorize for this address, if one was captured.
func (s *Server) GetAuthPasswordHash(minerID string) (string, bool) {
	if v, ok := s.authPasswords.Load(minerID); ok {
		return v.(string), true
	}
	return "", false
}

func (s *Server) handleAuthorize(client *Client, req *Request) *Response {
	var params []string
	// Debug log
	if err := json.Unmarshal(req.Params, &params); err != nil || len(params) < 1 {
		return &Response{ID: req.ID, Result: false, Error: ErrUnauthorized}
	}

	username := params[0]
	minerID, workerName := parseUsername(username)

	// Allow Braiins probe connections (used to verify pool connectivity)
	// These use usernames like "braiinstest" which aren't valid addresses
	if strings.HasPrefix(strings.ToLower(username), "braiins") && minerID == "" {
		s.logger.Info("Braiins probe connection accepted",
			zap.String("username", username),
			zap.String("ip", client.IP))
		// Set a dummy address for probe - won't receive payouts
		minerID = "probe"
		workerName = username
	}

	// Reject invalid addresses - they cannot receive payouts
	if minerID == "" {
		s.logger.Warn("Rejected connection with invalid address",
			zap.String("username", username),
			zap.String("ip", client.IP))
		return &Response{ID: req.ID, Result: false, Error: ErrUnauthorized}
	}

	// Detect rental service from worker name patterns
	rentalFromWorker := detectRentalFromWorker(workerName)

	var soloMode bool
	var manualDiff float64

	// Solo-only deployment (home miner): everyone mines solo, ignore any PPLNS setting.
	if s.config.SoloOnly {
		soloMode = true
	}

	if !s.config.SoloOnly && s.minerSettings != nil {
		if settings, err := s.minerSettings.GetMinerSettings(minerID); err == nil && settings != nil {
			soloMode = settings.SoloMining
			manualDiff = settings.ManualDiff

			// Autosave: Create default settings for new miners
			// Default to SOLO mode for solo-only pools
			if !settings.Exists && minerID != "probe" {
				go func(mid string) {
					newSettings := &MinerSettings{
						MinerID:    mid,
						SoloMining: true,
						ManualDiff: 0,
						MinPayout:  5.0,
					}
					if err := s.minerSettings.SaveMinerSettings(newSettings); err != nil {
						s.logger.Debug("Autosave settings for new miner",
							zap.String("miner", mid),
							zap.Error(err))
					}
				}(minerID)
			}
		}
	}

	// Capture the miner-chosen stratum password as a proof-of-control secret for
	// settings changes (verified by the pool API). The "ignore" default "x" and
	// difficulty hints ("d=...") are not treated as real passwords.
	if len(params) >= 2 && minerID != "probe" {
		if pw := strings.TrimSpace(params[1]); pw != "" && !strings.EqualFold(pw, "x") && !strings.HasPrefix(strings.ToLower(pw), "d=") {
			sum := sha256.Sum256([]byte(pw))
			s.authPasswords.Store(minerID, hex.EncodeToString(sum[:]))
		}
	}

	client.mu.Lock()
	client.Authorized = true
	client.MinerID = minerID
	client.WorkerName = workerName
	client.SoloMining = soloMode
	client.ManualDiff = manualDiff
	client.LastSettingsRefresh = time.Now()

	// Update rental detection if found from worker name (user agent takes priority)
	if client.RentalService == RentalNone && rentalFromWorker != RentalNone {
		client.RentalService = rentalFromWorker
	}

	rental := client.RentalService

	if manualDiff > 0 {
		// For rental services, ensure manual diff meets their minimum requirement
		if rental != RentalNone && manualDiff < s.config.RentalMinDiff {
			client.Difficulty = s.config.RentalMinDiff
		} else {
			client.Difficulty = manualDiff
		}
		// Clamp to the configured maximum so a client-supplied difficulty cannot be
		// pinned to an absurd value.
		if s.config.MaxDiff > 0 && client.Difficulty > s.config.MaxDiff {
			client.Difficulty = s.config.MaxDiff
		}
	} else if client.Difficulty <= s.config.MinDiff {
		// Vardiff mode with no client-suggested difficulty above the floor. Resume the
		// miner's recently-ramped level (or the rental floor) instead of restarting at
		// the tiny default — this stops a reconnecting/churning miner (e.g. a big rental
		// proxy that cycles connections) from flooding the pool with low-difficulty,
		// often-stale shares while vardiff slowly ramps back up. The value is sent to the
		// miner via sendDifficulty right after authorize, so there is no diff mismatch.
		floor := s.config.MinDiff
		if rental != RentalNone {
			floor = s.config.RentalMinDiff
		}
		ceil := s.config.MaxDiff
		if rental != RentalNone && s.config.RentalMaxDiff > 0 {
			ceil = s.config.RentalMaxDiff
		}
		// Bound a RESUMED difficulty to the rental cap even for non-rentals: a level
		// ramped under one connection must not resume at an absurd value on another
		// (defence-in-depth alongside idleDifficultyLoop, which self-corrects a too-high
		// resume within idleResetAfter regardless).
		if s.config.RentalMaxDiff > 0 && ceil > s.config.RentalMaxDiff {
			ceil = s.config.RentalMaxDiff
		}
		start := floor
		// Resume a remembered (ramped-up) difficulty EXCEPT for hashrate marketplaces
		// that fan many individual, differently-sized miners onto one payout address
		// (Braiins Hashpower). Those must each vardiff from the floor for THEIR OWN
		// hashrate — resuming one miner's ramped level onto all of them starves the
		// small ones and floods the pool with stale shares. A single proxy or a normal
		// miner (identified by NOT being such a marketplace) keeps the resume so it
		// does not re-ramp from the floor on every reconnect.
		if !isManyMinerMarketplace(client.UserAgent) {
			if remembered, ok := s.recallDifficulty(minerID); ok && remembered > start {
				start = remembered
			}
		}
		if ceil > 0 && start > ceil {
			start = ceil
		}
		client.Difficulty = start
	}
	client.mu.Unlock()

	if soloMode {
		atomic.AddInt64(&s.stats.SoloMiners, 1)
	} else {
		atomic.AddInt64(&s.stats.PPLNSMiners, 1)
	}

	modeStr := "PPLNS"
	if soloMode {
		modeStr = "SOLO"
	}

	if rental != RentalNone {
		s.logger.Info("Rental miner authorized",
			zap.String("miner", minerID),
			zap.String("worker", workerName),
			zap.String("mode", modeStr),
			zap.String("rental_service", rental.String()),
			zap.Float64("difficulty", client.Difficulty))
	} else {
		s.logger.Info("Miner authorized",
			zap.String("miner", minerID),
			zap.String("worker", workerName),
			zap.String("mode", modeStr),
			zap.Float64("difficulty", client.Difficulty))
	}

	return &Response{ID: req.ID, Result: true}
}

// detectRentalFromWorker detects rental services from worker name patterns
func detectRentalFromWorker(worker string) RentalService {
	w := strings.ToLower(worker)

	// NiceHash often uses worker names like "nh_xxxx" or contains "nicehash"
	if strings.HasPrefix(w, "nh_") || strings.HasPrefix(w, "nh-") ||
		strings.Contains(w, "nicehash") {
		return RentalNiceHash
	}

	// MRR often uses worker names like "mrr_xxxx" or "rig_xxxx"
	if strings.HasPrefix(w, "mrr_") || strings.HasPrefix(w, "mrr-") ||
		strings.HasPrefix(w, "mrr.") || strings.Contains(w, "miningrigrentals") {
		return RentalMRR
	}

	// Generic rental patterns
	if strings.Contains(w, "rental") || strings.Contains(w, "rent_") {
		return RentalOther
	}

	return RentalNone
}

func (s *Server) handleSubmit(client *Client, req *Request) *Response {
	// Intake rate limit: bound submits per client per second so the live stratum
	// cannot be flooded (invalid/duplicate submits still cost parse + dedup work).
	if maxRate := s.config.MaxSharesPerSecond; maxRate > 0 {
		client.mu.Lock()
		now := time.Now()
		if now.Sub(client.submitWindowStart) >= time.Second {
			client.submitWindowStart = now
			client.submitCount = 0
		}
		client.submitCount++
		over := client.submitCount > maxRate
		client.mu.Unlock()
		if over {
			return &Response{ID: req.ID, Result: false, Error: ErrRateLimited}
		}
	}

	client.mu.RLock()
	if !client.Authorized {
		client.mu.RUnlock()
		s.logger.Warn("Submit from unauthorized client", zap.String("ip", client.IP))
		return &Response{ID: req.ID, Result: false, Error: ErrUnauthorized}
	}
	minerID := client.MinerID
	workerName := client.WorkerName
	difficulty := client.Difficulty
	soloMining := client.SoloMining
	manualDiff := client.ManualDiff
	extranonce1 := client.ExtraNonce1
	extranonce2Size := client.ExtraNonce2Size
	lastSettingsRefresh := client.LastSettingsRefresh
	client.mu.RUnlock()

	// Refresh settings every 15 seconds to allow on-the-fly mode changes.
	// Solo-only deployments never read a PPLNS setting: mode is locked to SOLO.
	if !s.config.SoloOnly && s.minerSettings != nil && time.Since(lastSettingsRefresh) > 15*time.Second {
		if settings, err := s.minerSettings.GetMinerSettings(minerID); err == nil && settings != nil {
			client.mu.Lock()
			if client.SoloMining != settings.SoloMining {
				s.logger.Info("Miner mode changed on-the-fly",
					zap.String("miner", minerID),
					zap.Bool("old_solo", client.SoloMining),
					zap.Bool("new_solo", settings.SoloMining))
			}
			client.SoloMining = settings.SoloMining
			client.ManualDiff = settings.ManualDiff
			client.LastSettingsRefresh = time.Now()
			soloMining = settings.SoloMining
			manualDiff = settings.ManualDiff
			client.mu.Unlock()
		}
	}

	// Parse params as []interface{} to handle miners that send mixed types
	var rawParams []interface{}
	if err := json.Unmarshal(req.Params, &rawParams); err != nil {
		s.logger.Warn("Failed to parse submit params",
			zap.String("miner", minerID),
			zap.Error(err),
			zap.ByteString("params", req.Params))
		atomic.AddInt64(&s.stats.InvalidShares, 1)
		atomic.AddInt64(&client.InvalidShares, 1)
		return &Response{ID: req.ID, Result: false, Error: ErrLowDifficulty}
	}

	if len(rawParams) < 5 {
		s.logger.Warn("Insufficient submit params",
			zap.String("miner", minerID),
			zap.Int("count", len(rawParams)))
		atomic.AddInt64(&s.stats.InvalidShares, 1)
		atomic.AddInt64(&client.InvalidShares, 1)
		return &Response{ID: req.ID, Result: false, Error: ErrLowDifficulty}
	}

	// Convert all params to strings (handles both string and numeric values)
	// SECURITY: Validate numeric ranges to prevent integer overflow attacks
	params := make([]string, len(rawParams))
	for i, p := range rawParams {
		switch v := p.(type) {
		case string:
			params[i] = v
		case float64:
			// SECURITY: Validate range before casting to prevent overflow
			if v < 0 || v > float64(^uint32(0)) {
				s.logger.Warn("Invalid numeric parameter - out of range",
					zap.Int("param_index", i),
					zap.Float64("value", v))
				atomic.AddInt64(&s.stats.InvalidShares, 1)
				atomic.AddInt64(&client.InvalidShares, 1)
				return &Response{ID: req.ID, Result: false, Error: ErrLowDifficulty}
			}
			params[i] = fmt.Sprintf("%08x", uint32(v))
		case json.Number:
			if n, err := v.Int64(); err == nil {
				// SECURITY: Validate range before casting
				if n < 0 || n > int64(^uint32(0)) {
					s.logger.Warn("Invalid numeric parameter - out of range",
						zap.Int("param_index", i),
						zap.Int64("value", n))
					atomic.AddInt64(&s.stats.InvalidShares, 1)
					atomic.AddInt64(&client.InvalidShares, 1)
					return &Response{ID: req.ID, Result: false, Error: ErrLowDifficulty}
				}
				params[i] = fmt.Sprintf("%08x", uint32(n))
			} else {
				params[i] = string(v)
			}
		default:
			params[i] = fmt.Sprintf("%v", p)
		}
	}

	jobID := params[1]
	extranonce2 := normalizeHex(params[2], extranonce2Size*2) // Use client's configured size
	ntime := normalizeHex(params[3], 8)
	nonce := normalizeHex(params[4], 8)
	versionBits := ""
	if len(params) > 5 {
		versionBits = params[5]
	}

	s.logger.Info("Share submitted",
		zap.String("miner", minerID),
		zap.String("worker", workerName),
		zap.String("job", jobID),
		zap.String("extranonce2", extranonce2),
		zap.String("ntime", ntime),
		zap.String("nonce", nonce))

	// Check for duplicate share FIRST
	if s.isDuplicateShare(jobID, extranonce1, extranonce2, ntime, nonce, versionBits) {
		s.logger.Warn("Duplicate share rejected",
			zap.String("miner", minerID),
			zap.String("job", jobID))
		atomic.AddInt64(&s.stats.InvalidShares, 1)
		atomic.AddInt64(&client.InvalidShares, 1)
		return &Response{ID: req.ID, Result: false, Error: ErrDuplicateShare}
	}

	// Get the job from history
	jobInterface, exists := s.jobHistory.Load(jobID)
	if !exists {
		s.logger.Warn("Job not found",
			zap.String("miner", minerID),
			zap.String("job", jobID))
		atomic.AddInt64(&s.stats.InvalidShares, 1)
		atomic.AddInt64(&client.InvalidShares, 1)
		return &Response{ID: req.ID, Result: false, Error: ErrJobNotFound}
	}
	job := jobInterface.(*Job)

	// Validate the share - verify proof of work
	// Accept any share that meets min_diff - don't waste miner's work
	// Target difficulty is for rate limiting/vardiff, not rejection
	isValid, actualDiff, blockHash, err := s.validateShare(job, extranonce1, extranonce2, ntime, nonce, versionBits, s.config.MinDiff)
	if err != nil {
		s.logger.Warn("Share validation error",
			zap.String("miner", minerID),
			zap.Error(err))
		atomic.AddInt64(&s.stats.InvalidShares, 1)
		atomic.AddInt64(&client.InvalidShares, 1)
		return &Response{ID: req.ID, Result: false, Error: ErrLowDifficulty}
	}

	if !isValid {
		s.logger.Warn("Share below minimum difficulty",
			zap.String("miner", minerID),
			zap.Float64("required", s.config.MinDiff),
			zap.Float64("actual", actualDiff))
		atomic.AddInt64(&s.stats.InvalidShares, 1)
		atomic.AddInt64(&client.InvalidShares, 1)

		// Track rejection for vardiff adjustment
		client.mu.Lock()
		client.RecentSubmissions = append(client.RecentSubmissions, false)
		if len(client.RecentSubmissions) > RecentSubmissionsWindow {
			client.RecentSubmissions = client.RecentSubmissions[1:]
		}
		// Check if rejection rate is too high and reduce difficulty
		if len(client.RecentSubmissions) >= 20 && manualDiff == 0 && s.config.VardiffEnabled {
			rejections := 0
			for _, accepted := range client.RecentSubmissions {
				if !accepted {
					rejections++
				}
			}
			rejectionRate := float64(rejections) / float64(len(client.RecentSubmissions))
			// Use appropriate min_diff based on client type (rental vs regular)
			minDiff := s.config.MinDiff
			if client.RentalService != RentalNone {
				minDiff = s.config.RentalMinDiff
			}
			if rejectionRate > MaxRejectionRate && client.Difficulty > minDiff {
				// Use more aggressive reduction (2x) to settle faster
				oldDiff := client.Difficulty
				newDiff := client.Difficulty / 2.0
				if newDiff < minDiff {
					newDiff = minDiff
				}
				client.PreviousDifficulty = oldDiff
				client.DifficultyChangedAt = time.Now()
				client.DifficultyReducedAt = time.Now()
				client.DifficultyReducedFrom = oldDiff // Remember the ceiling that caused rejections
				client.Difficulty = newDiff
				client.RecentSubmissions = client.RecentSubmissions[:0] // Reset after adjustment
				client.mu.Unlock()

				s.logger.Info("High rejection rate, reducing difficulty",
					zap.String("miner", minerID),
					zap.Float64("rejection_rate", rejectionRate),
					zap.Float64("old_diff", oldDiff),
					zap.Float64("new_diff", newDiff))

				s.sendDifficulty(client, newDiff)
				return &Response{ID: req.ID, Result: false, Error: ErrLowDifficulty}
			}
		}
		client.mu.Unlock()

		return &Response{ID: req.ID, Result: false, Error: ErrLowDifficulty}
	}

	now := time.Now()

	client.mu.Lock()
	client.ShareTimes = append(client.ShareTimes, now)
	if len(client.ShareTimes) > 100 {
		client.ShareTimes = client.ShareTimes[1:]
	}
	// Track accepted submission for rejection rate calculation
	client.RecentSubmissions = append(client.RecentSubmissions, true)
	if len(client.RecentSubmissions) > RecentSubmissionsWindow {
		client.RecentSubmissions = client.RecentSubmissions[1:]
	}
	shareCount := len(client.ShareTimes)

	// Save the difficulty at which this share was actually submitted
	// (before any adjustments that apply to future shares).
	//
	// Credit the LESSER of the assigned difficulty and the difficulty the share
	// actually proved. Shares are accepted whenever they meet min_diff (not the
	// per-client assigned difficulty), so crediting the vardiff-inflated assigned
	// difficulty for a share that only meets min_diff would let a miner inflate its
	// PPLNS work weight and drain the shared reward pool. Taking min() still credits
	// an honest miner its full assigned difficulty (its shares meet or exceed it),
	// while a miner submitting cheap min_diff shares is credited only what it proved.
	shareDifficulty := difficulty
	if actualDiff < shareDifficulty {
		shareDifficulty = actualDiff
	}

	client.mu.Unlock()

	// Vardiff adjustment - respects retarget_time interval
	if manualDiff == 0 && s.config.VardiffEnabled && shareCount >= VardiffMinShares {
		s.adjustVardiff(client)
	}

	share := &Share{
		JobID:       jobID,
		MinerID:     minerID,
		WorkerName:  workerName,
		Difficulty:  shareDifficulty, // Use target difficulty for hashrate calculation
		ActualDiff:  actualDiff,      // Use actual share difficulty for block candidate detection
		IP:          client.IP,
		ExtraNonce2: extranonce2,
		ExtraNonce1: extranonce1,
		NTime:       ntime,
		Nonce:       nonce,
		VersionBits: versionBits,
		IsValid:     true,
		IsSolo:      soloMining,
		SubmittedAt: now,
		BlockHash:   hex.EncodeToString(blockHash),
	}

	atomic.AddInt64(&s.stats.ValidShares, 1)
	atomic.AddInt64(&client.ValidShares, 1)

	if s.shareProcessor != nil {
		go s.shareProcessor.ProcessShare(context.Background(), share)
	}

	// Merge mining: if this share's parent hash also meets the aux (1175) target,
	// submit the AuxPoW. The target check is cheap and inline; the rebuild+submit
	// runs async only on a winner, so BCH2 share handling is never delayed.
	if s.auxClient != nil && job.AuxWork != nil && auxHashMeetsTarget(blockHash, job.AuxWork.Target) {
		go s.submitAux(job, extranonce1, extranonce2, ntime, nonce, versionBits, minerID, soloMining)
	}

	s.logger.Info("Share accepted",
		zap.String("miner", minerID),
		zap.String("worker", workerName),
		zap.Float64("diff", actualDiff),
		zap.Float64("target_diff", difficulty),
		zap.Bool("solo", soloMining),
		zap.String("hash", hex.EncodeToString(blockHash)[:16]+"..."))

	return &Response{ID: req.ID, Result: true}
}

// diffMemoryTTL bounds how long a miner's last vardiff level is reused across
// reconnects: long enough to survive rapid reconnect churn (e.g. rental proxies that
// cycle connections every fraction of a second), short enough that a miner whose
// hashrate genuinely dropped re-ramps from a sane level.
const diffMemoryTTL = 30 * time.Minute

type diffMem struct {
	diff float64
	at   time.Time
}

// rememberDifficulty records a miner's current vardiff level. Called only after a real
// vardiff adjustment (which is share-driven), so the remembered value reflects hashrate
// the miner actually proved — a share-less connection cannot inflate it.
func (s *Server) rememberDifficulty(minerID string, diff float64) {
	if minerID == "" || diff <= 0 {
		return
	}
	s.diffMemory.Store(minerID, diffMem{diff: diff, at: time.Now()})
}

// isManyMinerMarketplace reports whether the client's user-agent identifies a
// hashrate marketplace that points many individual miners at the pool under one
// shared payout address (e.g. Braiins Hashpower reports "Braiins/Hashpower").
// Such connections must each vardiff for their own hashrate rather than inherit the
// address's aggregate remembered level. Detecting on the user-agent (rather than an
// instantaneous connection count) is robust to reconnect churn and overlapping
// reconnects. Match only "braiins": it is the definitive Braiins signal and, unlike
// "hashpower", cannot false-match a single-proxy miner like NiceHash's excavator
// (which should keep the resume). Add other many-miner marketplaces here as they
// appear. A false positive is harmless anyway — a normal single miner that skips the
// resume simply re-ramps via vardiff, which is its ordinary behaviour.
func isManyMinerMarketplace(userAgent string) bool {
	return strings.Contains(strings.ToLower(userAgent), "braiins")
}

// recallDifficulty returns a miner's remembered vardiff level if it is still fresh.
func (s *Server) recallDifficulty(minerID string) (float64, bool) {
	v, ok := s.diffMemory.Load(minerID)
	if !ok {
		return 0, false
	}
	m, ok := v.(diffMem)
	if !ok || time.Since(m.at) > diffMemoryTTL {
		return 0, false
	}
	return m.diff, true
}

func (s *Server) adjustVardiff(client *Client) {
	client.mu.Lock()

	// Only adjust every RetargetTime seconds (default 60)
	retargetTime := s.config.RetargetTime
	if retargetTime == 0 {
		retargetTime = 60
	}
	if time.Since(client.DifficultyChangedAt) < time.Duration(retargetTime)*time.Second {
		client.mu.Unlock()
		return
	}

	// Use larger sample window for more stable measurements
	sampleSize := VardiffMinShares
	if len(client.ShareTimes) < sampleSize {
		client.mu.Unlock()
		return
	}

	recent := client.ShareTimes[len(client.ShareTimes)-sampleSize:]
	totalTime := recent[sampleSize-1].Sub(recent[0]).Seconds()
	if totalTime <= 0 {
		client.mu.Unlock()
		return
	}
	avgTime := totalTime / float64(sampleSize-1)

	targetTime := float64(s.config.TargetShareTime)
	ratio := targetTime / avgTime

	// Check rejection rate before adjusting
	rejections := 0
	for _, accepted := range client.RecentSubmissions {
		if !accepted {
			rejections++
		}
	}
	rejectionRate := 0.0
	if len(client.RecentSubmissions) > 0 {
		rejectionRate = float64(rejections) / float64(len(client.RecentSubmissions))
	}

	// If rejection rate is high, don't increase difficulty even if timing suggests we should
	if rejectionRate > MaxRejectionRate && ratio > 1.0 {
		client.mu.Unlock()
		return
	}

	// Only adjust if outside variance window (miningcore style)
	// This prevents constant small adjustments
	varianceLow := 1.0 - VardiffVariancePercent
	varianceHigh := 1.0 + VardiffVariancePercent
	if ratio >= varianceLow && ratio <= varianceHigh {
		client.mu.Unlock()
		return
	}

	// Clamp ratio to prevent extreme difficulty changes (max 50% per adjustment)
	if ratio > MaxDifficultyMultiplier {
		ratio = MaxDifficultyMultiplier
	} else if ratio < 1.0/MaxDifficultyMultiplier {
		ratio = 1.0 / MaxDifficultyMultiplier
	}

	// Calculate new difficulty
	newDiff := client.Difficulty * ratio

	// For rental services, apply gentler MaxDelta (max 25% change)
	maxDelta := client.Difficulty * 0.5 // 50% max change for regular miners
	if client.RentalService != RentalNone {
		maxDelta = client.Difficulty * 0.25 // 25% max change for NiceHash/MRR
	}

	diffDelta := newDiff - client.Difficulty
	if diffDelta > maxDelta {
		newDiff = client.Difficulty + maxDelta
	} else if diffDelta < -maxDelta {
		newDiff = client.Difficulty - maxDelta
	}

	// Use appropriate min_diff based on client type (rental vs regular)
	minDiff := s.config.MinDiff
	if client.RentalService != RentalNone {
		minDiff = s.config.RentalMinDiff
	}
	if newDiff < minDiff {
		newDiff = minDiff
	}
	// Use rental-specific max diff for NiceHash/MRR to prevent over-ramping
	maxDiff := s.config.MaxDiff
	if client.RentalService != RentalNone && s.config.RentalMaxDiff > 0 {
		maxDiff = s.config.RentalMaxDiff
	}
	if newDiff > maxDiff {
		newDiff = maxDiff
	}

	// If we recently reduced difficulty due to high rejection rate,
	// don't increase above 80% of the ceiling that caused the rejection
	if client.DifficultyReducedFrom > 0 && time.Since(client.DifficultyReducedAt) < DifficultyReductionCooldown {
		ceiling := client.DifficultyReducedFrom * 0.8
		if newDiff > ceiling {
			newDiff = ceiling
			s.logger.Debug("Vardiff capped at ceiling",
				zap.String("miner", client.MinerID),
				zap.Float64("ceiling", ceiling),
				zap.Float64("original", client.Difficulty*ratio))
		}
	}

	if newDiff != client.Difficulty {
		oldDiff := client.Difficulty
		client.PreviousDifficulty = oldDiff
		client.DifficultyChangedAt = time.Now()
		client.Difficulty = newDiff
		minerID := client.MinerID
		client.mu.Unlock()

		// Remember this share-proven level so the miner resumes near it on reconnect.
		s.rememberDifficulty(minerID, newDiff)

		// Send difficulty synchronously to ensure miner receives it
		s.sendDifficulty(client, newDiff)

		s.logger.Info("Vardiff adjusted",
			zap.String("miner", minerID),
			zap.Float64("avg_time", avgTime),
			zap.Float64("old_diff", oldDiff),
			zap.Float64("new_diff", newDiff))
		return
	}
	client.mu.Unlock()
}

func (s *Server) sendResponse(client *Client, resp *Response) {
	data, _ := json.Marshal(resp)
	data = append(data, '\n')
	client.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := client.Conn.Write(data); err != nil {
		client.Conn.Close() // Force disconnect on write error
	}
}

func (s *Server) sendNotification(client *Client, notif *Notification) {
	data, _ := json.Marshal(notif)
	data = append(data, '\n')
	client.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := client.Conn.Write(data); err != nil {
		// Log write error for NiceHash clients
		client.mu.RLock()
		rental := client.RentalService
		minerID := client.MinerID
		client.mu.RUnlock()
		if rental == RentalNiceHash {
			s.logger.Warn("Write error to NiceHash client",
				zap.String("miner", minerID),
				zap.String("method", notif.Method),
				zap.Error(err))
		}
		client.Conn.Close() // Force disconnect on write error
	}
}

func (s *Server) sendDifficulty(client *Client, diff float64) {
	// Prevent sending duplicate difficulty notifications within 500ms
	// This avoids race conditions between authorize and job broadcast
	client.mu.Lock()
	if time.Since(client.LastDifficultySentAt) < 500*time.Millisecond {
		client.mu.Unlock()
		return
	}
	client.LastDifficultySentAt = time.Now()
	client.mu.Unlock()

	notif := &Notification{
		Method: MethodSetDifficulty,
		Params: []interface{}{diff},
	}
	s.sendNotification(client, notif)
}

func (s *Server) sendJob(client *Client, job *Job) {
	notif := &Notification{
		Method: MethodNotify,
		Params: []interface{}{
			job.ID,
			job.PrevBlockHash,
			job.CoinBase1,
			job.CoinBase2,
			job.MerkleBranches,
			job.Version,
			job.NBits,
			job.NTime,
			job.CleanJobs,
		},
	}

	// Log job delivery for NiceHash clients for debugging
	client.mu.RLock()
	rental := client.RentalService
	minerID := client.MinerID
	client.mu.RUnlock()

	if rental == RentalNiceHash {
		s.logger.Info("Sending job to NiceHash client",
			zap.String("miner", minerID),
			zap.String("job_id", job.ID),
			zap.String("prevhash", job.PrevBlockHash[:16]+"..."),
			zap.String("version", job.Version),
			zap.String("nbits", job.NBits),
			zap.String("ntime", job.NTime),
			zap.Bool("clean", job.CleanJobs))
	}

	s.sendNotification(client, notif)
}

func (s *Server) BroadcastJob(job *Job) {
	s.currentJob.Store(job)
	s.jobHistory.Store(job.ID, job)

	// CRITICAL FIX: Clean up old jobs to prevent unbounded memory growth
	// Keep only the last 100 jobs in history
	s.cleanupJobHistory(500)

	// Clear old shares when broadcasting clean jobs (new block height)
	if job.CleanJobs {
		s.clearSharesForJob()
	}

	s.clients.Range(func(key, value interface{}) bool {
		client := value.(*Client)
		if client.Authorized {
			// For clean jobs (new block), resend difficulty to ensure miners have it
			// Some miners (like Whatsminer) may miss difficulty notifications
			if job.CleanJobs {
				client.mu.RLock()
				diff := client.Difficulty
				client.mu.RUnlock()
				s.sendDifficulty(client, diff)
			}
			s.sendJob(client, job)
		}
		return true
	})
}

// cleanupJobHistory removes old jobs from history to prevent unbounded memory growth
// CRITICAL FIX: Prevents memory exhaustion from accumulating job history
func (s *Server) cleanupJobHistory(maxJobs int) {
	type jobEntry struct {
		id    string
		idNum uint64
		valid bool // id parsed as an integer
	}
	var jobs []jobEntry

	s.jobHistory.Range(func(key, value interface{}) bool {
		id := key.(string)
		n, err := strconv.ParseUint(id, 16, 64) // job IDs are hex; parse as base-16 for correct oldest-first eviction
		jobs = append(jobs, jobEntry{id: id, idNum: n, valid: err == nil})
		return true
	})

	// If under limit, no cleanup needed
	if len(jobs) <= maxJobs {
		return
	}

	// Evict OLDEST-first, DETERMINISTICALLY. Job IDs are monotonically increasing
	// integers, so a smaller ID is an older job. sync.Map.Range order is randomized,
	// so deleting in iteration order (the previous behaviour) could drop recent,
	// still-active jobs and reject their timely shares as "Job not found" — worst for
	// higher-latency miners (e.g. Braiins routed through Cloudflare). Sort ascending
	// and delete only the oldest excess; unparseable IDs sort first so they drain out.
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].valid != jobs[j].valid {
			return !jobs[i].valid
		}
		return jobs[i].idNum < jobs[j].idNum
	})

	var currentID string
	if cj := s.currentJob.Load(); cj != nil {
		currentID = cj.(*Job).ID
	}

	toDelete := len(jobs) - maxJobs
	for i := 0; i < len(jobs) && toDelete > 0; i++ {
		if jobs[i].id == currentID {
			continue // never evict the job miners are currently working on
		}
		s.jobHistory.Delete(jobs[i].id)
		toDelete--
	}
}

func (s *Server) GetStats() *ServerStats {
	return &ServerStats{
		ActiveConnections: atomic.LoadInt64(&s.stats.ActiveConnections),
		ValidShares:       atomic.LoadInt64(&s.stats.ValidShares),
		InvalidShares:     atomic.LoadInt64(&s.stats.InvalidShares),
		BlocksFound:       atomic.LoadInt64(&s.stats.BlocksFound),
		SoloMiners:        atomic.LoadInt64(&s.stats.SoloMiners),
		PPLNSMiners:       atomic.LoadInt64(&s.stats.PPLNSMiners),
	}
}

// RentalStats contains statistics about rental service connections
type RentalStats struct {
	NiceHashMiners int64
	MRRMiners      int64
	OtherRentals   int64
	TotalRentals   int64
}

// GetRentalStats returns statistics about connected rental miners
func (s *Server) GetRentalStats() *RentalStats {
	stats := &RentalStats{}

	s.clients.Range(func(key, value interface{}) bool {
		client := value.(*Client)
		client.mu.RLock()
		rental := client.RentalService
		authorized := client.Authorized
		client.mu.RUnlock()

		if !authorized {
			return true
		}

		switch rental {
		case RentalNiceHash:
			stats.NiceHashMiners++
			stats.TotalRentals++
		case RentalMRR:
			stats.MRRMiners++
			stats.TotalRentals++
		case RentalOther:
			stats.OtherRentals++
			stats.TotalRentals++
		}
		return true
	})

	return stats
}

// sendExtranonce sends an extranonce update to a client that supports it
// This is used when a client's extranonce needs to change (rare, but supported)
func (s *Server) sendExtranonce(client *Client, extranonce1 string, extranonce2Size int) {
	client.mu.RLock()
	supportsExtranonce := client.SupportsExtranonce
	client.mu.RUnlock()

	if !supportsExtranonce {
		return
	}

	notif := &Notification{
		Method: "mining.set_extranonce",
		Params: []interface{}{extranonce1, extranonce2Size},
	}
	s.sendNotification(client, notif)

	s.logger.Info("Sent extranonce update",
		zap.String("ip", client.IP),
		zap.String("extranonce1", extranonce1))
}

// IsRentalClient checks if a client is from a rental service
func (c *Client) IsRentalClient() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.RentalService != RentalNone
}

func parseUsername(username string) (minerID, workerName string) {
	parts := strings.SplitN(username, ".", 2)
	minerID = parts[0]
	if len(parts) > 1 {
		workerName = parts[1]
	} else {
		workerName = "default"
	}

	// Normalize address: ensure bitcoincashii: prefix (lowercase)
	minerID = normalizeMinerAddress(minerID)

	// CashAddr is exactly 42 chars after prefix. If extra chars remain
	// (e.g. NiceHash appends worker suffix without dot separator),
	// split them off as worker name.
	if strings.HasPrefix(minerID, "bitcoincashii:") {
		hash := minerID[len("bitcoincashii:"):]
		if len(hash) > 42 {
			extra := hash[42:]
			minerID = "bitcoincashii:" + hash[:42]
			if workerName == "default" {
				workerName = extra
			}
		}
	}

	return
}

// normalizeMinerAddress ensures the address has the correct bitcoincashii: prefix
// Returns empty string for invalid/rejected address formats
func normalizeMinerAddress(addr string) string {
	// Convert to lowercase for comparison
	lowerAddr := strings.ToLower(addr)

	// REJECT bitcoincash2: prefix - invalid format
	if strings.HasPrefix(lowerAddr, "bitcoincash2:") {
		return "" // Signal rejection
	}

	// If already has correct prefix, validate it has an actual hash after the prefix
	if strings.HasPrefix(lowerAddr, "bitcoincashii:") {
		hash := lowerAddr[len("bitcoincashii:"):]
		if len(hash) < 42 || (hash[0] != 'q' && hash[0] != 'p') {
			return "" // Reject: prefix without valid hash
		}
		return lowerAddr
	}

	// Handle truncated prefix: bitcoinii: -> bitcoincashii: (WhatsMiner firmware bug)
	if strings.HasPrefix(lowerAddr, "bitcoinii:") {
		hash := lowerAddr[len("bitcoinii:"):]
		if len(hash) >= 42 && (hash[0] == 'q' || hash[0] == 'p') {
			return "bitcoincashii:" + hash
		}
		return "" // Reject: invalid hash after prefix
	}

	// Reject bare prefix variants with no hash (firmware truncation)
	if lowerAddr == "bitcoincashii" || lowerAddr == "bitcoincash" || lowerAddr == "bitcoinii" {
		return "" // Signal rejection
	}

	// If it's just the hash part (starts with 'q' for mainnet), add prefix
	if len(addr) >= 42 && (strings.HasPrefix(lowerAddr, "q") || strings.HasPrefix(lowerAddr, "p")) {
		return "bitcoincashii:" + lowerAddr
	}

	// Reject anything else that isn't a valid address
	return ""
}

// normalizeHex pads a hex string to the required length with leading zeros
func normalizeHex(s string, length int) string {
	// Remove any "0x" prefix
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")

	// Hex is case-insensitive; lowercase so case-permuted duplicates of the
	// same proof-of-work collapse to one shareKey (prevents dup-share credit inflation).
	s = strings.ToLower(s)

	// If already correct length, return as-is
	if len(s) == length {
		return s
	}

	// If shorter, pad with leading zeros
	if len(s) < length {
		return strings.Repeat("0", length-len(s)) + s
	}

	// If longer, return as-is (validation will catch it)
	return s
}

func (s *Server) handleConfigure(client *Client, req *Request) *Response {
	// mining.configure for version rolling (AsicBoost) and other extensions
	// Request format: [["extension1", "extension2", ...], {"param1": value1, ...}]

	var params []json.RawMessage
	var supportsMultiVersion bool
	if err := json.Unmarshal(req.Params, &params); err == nil && len(params) >= 1 {
		var extensions []string
		if err := json.Unmarshal(params[0], &extensions); err == nil {
			// Check requested extensions
			for _, ext := range extensions {
				if ext == "version-rolling" {
					client.mu.Lock()
					client.SupportsVersionRolling = true
					client.VersionRollingMask = "1fffe000"
					client.mu.Unlock()
				}
				if ext == "multi_version" {
					supportsMultiVersion = true
				}
			}
		}

		// Parse extension parameters if provided
		if len(params) >= 2 {
			var extParams map[string]interface{}
			if err := json.Unmarshal(params[1], &extParams); err == nil {
				// Check for version-rolling.mask request
				if mask, ok := extParams["version-rolling.mask"].(string); ok {
					// Intersect with our supported mask
					client.mu.Lock()
					client.VersionRollingMask = intersectMasks("1fffe000", mask)
					client.mu.Unlock()
				}
			}
		}
	}

	client.mu.RLock()
	rental := client.RentalService
	mask := client.VersionRollingMask
	if mask == "" {
		mask = "1fffe000"
	}
	client.mu.RUnlock()

	if rental != RentalNone {
		s.logger.Info("Rental service configured",
			zap.String("ip", client.IP),
			zap.String("rental_service", rental.String()),
			zap.String("version_rolling_mask", mask))
	}

	// Return supported extensions
	// Use min-bit-count of 0 - we don't require any minimum bits
	result := map[string]interface{}{
		"version-rolling":               true,
		"version-rolling.mask":          mask,
		"version-rolling.min-bit-count": 0,
	}
	// Add multi_version support if requested (NiceHash compatibility)
	if supportsMultiVersion {
		result["multi_version"] = true
	}
	return &Response{ID: req.ID, Result: result}
}

// intersectMasks returns the intersection of two hex masks
func intersectMasks(mask1, mask2 string) string {
	var m1, m2 uint32
	fmt.Sscanf(mask1, "%x", &m1)
	fmt.Sscanf(mask2, "%x", &m2)
	return fmt.Sprintf("%08x", m1&m2)
}
