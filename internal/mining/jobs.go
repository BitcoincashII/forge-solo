package mining

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bch2/forge-pool/internal/mergemining"
)

type BlockTemplate struct {
	Version           int64    `json:"version"`
	PreviousBlockHash string   `json:"previousblockhash"`
	Transactions      []TxData `json:"transactions"`
	CoinbaseValue     int64    `json:"coinbasevalue"`
	Target            string   `json:"target"`
	Bits              string   `json:"bits"`
	Height            int64    `json:"height"`
	CurTime           int64    `json:"curtime"`
}

type TxData struct {
	Data string `json:"data"`
	TxID string `json:"txid"`
}

type JobManager struct {
	rpcURL      string
	rpcUser     string
	rpcPassword string

	// mu guards the fields that can be reconfigured at runtime from the dashboard
	// (payout pubkeyHash, coinbaseTag) and the merge-mining enable state. buildCoinbase
	// / fetchAuxWork read them under RLock; SetPoolAddress / SetCoinbaseTag /
	// EnableMergeMining write them under Lock.
	mu          sync.RWMutex
	pubkeyHash  []byte
	coinbaseTag []byte
	jobCounter  uint64

	// Merge mining (aux chain, e.g. 1175). Inert unless EnableMergeMining is called.
	auxClient  *mergemining.Client
	auxPayout  string
	auxEnabled bool
	auxLastErr string // throttle repeated aux-error logging (e.g. pre-activation)
}

// EnableMergeMining turns on aux-chain merge mining. Each CreateJob then fetches
// aux work from nodeURL and embeds its commitment in the coinbase. A short RPC
// timeout is used so a slow/unavailable aux node never stalls BCH2 job creation.
func (jm *JobManager) EnableMergeMining(nodeURL, user, pass, payoutAddr string) {
	c := mergemining.NewClient(nodeURL, user, pass)
	c.HTTP.Timeout = 3 * time.Second
	jm.mu.Lock()
	jm.auxClient = c
	jm.auxPayout = payoutAddr
	jm.auxEnabled = true
	jm.mu.Unlock()
	fmt.Printf("Merge mining ENABLED: aux node %s, payout %s\n", nodeURL, payoutAddr)
}

// fetchAuxWork returns current aux work + its coinbase commitment, or (nil,nil)
// if merge mining is off or the aux node has no work (e.g. not yet activated).
// Never returns an error to the caller: BCH2 mining must proceed regardless.
func (jm *JobManager) fetchAuxWork() (*mergemining.AuxWork, []byte) {
	jm.mu.RLock()
	enabled := jm.auxEnabled
	client := jm.auxClient
	payout := jm.auxPayout
	jm.mu.RUnlock()
	if !enabled || client == nil {
		return nil, nil
	}
	w, err := client.GetAuxBlock(payout)
	if err != nil {
		if err.Error() != jm.auxLastErr { // log only when the error changes
			jm.auxLastErr = err.Error()
			fmt.Printf("Merge mining: aux work unavailable (%v) — mining BCH2 only for now\n", err)
		}
		return nil, nil
	}
	jm.auxLastErr = ""
	commitment, cerr := mergemining.BuildCommitment(w.Hash)
	if cerr != nil {
		fmt.Printf("Merge mining: bad aux child hash %q: %v\n", w.Hash, cerr)
		return nil, nil
	}
	return w, commitment
}

func NewJobManager(rpcURL, rpcUser, rpcPassword, poolAddress, coinbaseTag string) *JobManager {
	// Resolve the payout address to a pubkey hash. Unlike the public pool, the home
	// solo app can start with NO payout address configured: the miner sets it later in
	// the dashboard. In that case pubkeyHash stays nil, mining is PAUSED (never mined to
	// a null/burn script), and SetPoolAddress activates it at runtime — no crash-loop.
	var pkh []byte
	if poolAddress != "" {
		// Try to get pubkey hash from node's validateaddress RPC with retries
		for i := 0; i < 10; i++ {
			pkh = getPubkeyHashFromNode(rpcURL, rpcUser, rpcPassword, poolAddress)
			if pkh != nil {
				break
			}
			if i < 9 {
				fmt.Printf("Waiting for node RPC to be ready... (attempt %d/10)\n", i+1)
				time.Sleep(2 * time.Second)
			}
		}
		if pkh == nil {
			// Fallback to local parsing if RPC fails
			pkh = parseAddressToPubkeyHash(poolAddress)
			if pkh != nil {
				fmt.Printf("Pool address pubkey hash (from local parser): %s\n", hex.EncodeToString(pkh))
			}
		}
		if pkh == nil {
			// A configured-but-unparseable address is a mistake we must NOT silently mine
			// past (rewards would burn), but we no longer crash the process — warn and pause
			// until a valid address is set in the dashboard.
			log.Printf("WARNING: pool payout address %q could not be resolved to a pubkey hash - "+
				"mining paused until a valid payout address is set in the dashboard", poolAddress)
		}
	} else {
		log.Printf("WARNING: no payout address configured - mining paused until set in the dashboard")
	}

	return &JobManager{
		rpcURL:      rpcURL,
		rpcUser:     rpcUser,
		rpcPassword: rpcPassword,
		pubkeyHash:  pkh,
		coinbaseTag: sanitizeCoinbaseTag(coinbaseTag),
	}
}

// SetPoolAddress resolves addr to a P2PKH pubkey hash (node RPC first, local CashAddr
// parser as backstop) and, on success, atomically installs it as the coinbase payout
// target. On failure it returns an error and leaves any existing pubkeyHash untouched
// (so a bad dashboard entry never disables an already-working miner).
func (jm *JobManager) SetPoolAddress(addr string) error {
	if addr == "" {
		return fmt.Errorf("empty payout address")
	}
	pkh := getPubkeyHashFromNode(jm.rpcURL, jm.rpcUser, jm.rpcPassword, addr)
	if pkh == nil {
		pkh = parseAddressToPubkeyHash(addr)
	}
	if pkh == nil {
		return fmt.Errorf("payout address %q could not be resolved to a P2PKH pubkey hash", addr)
	}
	jm.mu.Lock()
	jm.pubkeyHash = pkh
	jm.mu.Unlock()
	return nil
}

// IsConfigured reports whether a payout address is set. When false the job manager
// produces no jobs, so the node accepts stratum connections but pauses mining.
func (jm *JobManager) IsConfigured() bool {
	jm.mu.RLock()
	defer jm.mu.RUnlock()
	return jm.pubkeyHash != nil
}

// SetCoinbaseTag atomically updates the coinbase tag used for newly built jobs.
func (jm *JobManager) SetCoinbaseTag(tag string) {
	t := sanitizeCoinbaseTag(tag)
	jm.mu.Lock()
	jm.coinbaseTag = t
	jm.mu.Unlock()
}

// getPubkeyHashFromNode extracts pubkey hash via node RPC validateaddress
func getPubkeyHashFromNode(rpcURL, rpcUser, rpcPassword, address string) []byte {
	reqBody := fmt.Sprintf(`{"jsonrpc":"1.0","id":"pkh","method":"validateaddress","params":["%s"]}`, address)

	req, err := http.NewRequest("POST", rpcURL, bytes.NewBufferString(reqBody))
	if err != nil {
		return nil
	}
	req.SetBasicAuth(rpcUser, rpcPassword)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var rpcResp struct {
		Result struct {
			IsValid      bool   `json:"isvalid"`
			ScriptPubKey string `json:"scriptPubKey"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil
	}

	if !rpcResp.Result.IsValid {
		return nil
	}

	// Extract pubkey hash from P2PKH scriptPubKey: 76a914<20-byte-hash>88ac
	spk := rpcResp.Result.ScriptPubKey
	if len(spk) == 50 && spk[:6] == "76a914" && spk[46:] == "88ac" {
		pkh, err := hex.DecodeString(spk[6:46])
		if err == nil && len(pkh) == 20 {
			fmt.Printf("Pool address pubkey hash (from node): %s\n", spk[6:46])
			return pkh
		}
	}

	return nil
}

// cashaddrPolymod computes the CashAddr BCH checksum polymod over 5-bit values.
// A valid CashAddr yields polymod(prefixLower5 || 0 || data) == 0.
func cashaddrPolymod(v []byte) uint64 {
	c := uint64(1)
	for _, d := range v {
		c0 := byte(c >> 35)
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

// parseAddressToPubkeyHash decodes a BCH2 CashAddr and returns the 20-byte P2PKH
// pubkey hash, or nil if it is not a valid, checksummed, P2PKH (type 0) address.
// FAIL-CLOSED: an invalid checksum, an unknown/absent prefix, or any non-P2PKH type
// (e.g. P2SH) returns nil, so this local backstop never resurrects an address the
// node authoritatively rejected — mining to a wrong/unspendable script would burn the
// block reward. The CashAddr checksum covers the prefix, so a prefix is required.
func parseAddressToPubkeyHash(address string) []byte {
	// Mainnet-only: accept ONLY the BCH2 mainnet prefix. Dropping bchtest/bchreg (and the
	// BCHN bitcoincash: prefix) keeps this local backstop consistent with the API validator and
	// the node, so a testnet/other-chain address can never resolve to a same-hash mainnet script.
	prefixes := []string{"bitcoincashii"}
	var prefix, addr string
	for _, p := range prefixes {
		if len(address) > len(p)+1 && address[:len(p)+1] == p+":" {
			prefix = p
			addr = address[len(p)+1:]
			break
		}
	}
	if prefix == "" {
		return nil
	}

	const charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	data := make([]byte, 0, len(addr))
	for _, c := range addr {
		idx := -1
		for i, ch := range charset {
			if ch == c {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil
		}
		data = append(data, byte(idx))
	}
	if len(data) < 8 {
		return nil
	}

	// Verify the CashAddr checksum against the prefix — reject any mismatch (typos).
	chk := make([]byte, 0, len(prefix)+1+len(data))
	for i := 0; i < len(prefix); i++ {
		chk = append(chk, prefix[i]&0x1f)
	}
	chk = append(chk, 0)
	chk = append(chk, data...)
	if cashaddrPolymod(chk) != 0 {
		return nil
	}

	// Drop the 8-symbol (40-bit) checksum, convert 5-bit -> 8-bit.
	payload := data[:len(data)-8]
	var result []byte
	acc, bits := 0, 0
	for _, d := range payload {
		acc = (acc << 5) | int(d)
		bits += 5
		for bits >= 8 {
			bits -= 8
			result = append(result, byte(acc>>bits))
			acc &= (1 << bits) - 1
		}
	}

	// version byte: top bit reserved (0); bits 6..3 = type; bits 2..0 = size.
	// Require type 0 (P2PKH) and a 20-byte (160-bit) hash. Reject P2SH (type 1) etc.
	if len(result) != 21 {
		return nil
	}
	version := result[0]
	if version&0x80 != 0 || (version>>3)&0x1f != 0 {
		return nil
	}
	return result[1:21]
}

func (jm *JobManager) GetBlockTemplate() (*BlockTemplate, error) {
	// Mining is paused until a payout address is configured (see IsConfigured):
	// skip the template fetch so an unconfigured node stays idle instead of building
	// jobs that would pay a null script.
	if !jm.IsConfigured() {
		return nil, nil
	}
	// Acknowledge the segwit rule. BCH2 mainnet has segwit inactive, so this is a
	// no-op there (verified: identical template to empty params), but a node with
	// segwit active in getblocktemplate (e.g. regtest) rejects an empty rule set
	// with "must be called with the segwit rule set". Sending it is strictly more
	// compatible and never changes the BCH2 mainnet template.
	reqBody := `{"jsonrpc":"1.0","id":"forge","method":"getblocktemplate","params":[{"rules":["segwit"]}]}`
	
	req, err := http.NewRequest("POST", jm.rpcURL, bytes.NewBufferString(reqBody))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(jm.rpcUser, jm.rpcPassword)
	req.Header.Set("Content-Type", "application/json")
	
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	
	var rpcResp struct {
		Result BlockTemplate `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, err
	}
	
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc error: %s", rpcResp.Error.Message)
	}
	
	return &rpcResp.Result, nil
}

func (jm *JobManager) CreateJob(template *BlockTemplate) *Job {
	if template == nil || !jm.IsConfigured() {
		return nil
	}
	jobID := fmt.Sprintf("%x", atomic.AddUint64(&jm.jobCounter, 1))

	// Merge mining: fetch aux work and embed its commitment in the coinbase.
	// commitment is nil (and the coinbase is unchanged) when merge mining is off
	// or the aux node has no work yet — BCH2 mining is never affected.
	auxWork, commitment := jm.fetchAuxWork()

	coinbase1, coinbase2 := jm.buildCoinbase(template, commitment)

	// Full byte reversal for stratum prevhash
	prevHash := stratumPrevHash(template.PreviousBlockHash)
	originalPrevHash := template.PreviousBlockHash

	// Extract txids and raw tx data for merkle branches and block building
	var txids []string
	var txData []string
	for _, tx := range template.Transactions {
		txids = append(txids, tx.TxID)
		txData = append(txData, tx.Data)
	}

	// Build merkle branches from transaction txids
	merkleBranches := buildMerkleBranches(txids)

	return &Job{
		ID:               jobID,
		Height:           template.Height,
		PrevBlockHash:    prevHash,
		CoinBase1:        coinbase1,
		CoinBase2:        coinbase2,
		MerkleBranches:   merkleBranches,
		Version:          fmt.Sprintf("%08x", template.Version),
		NBits:            template.Bits,
		NTime:            fmt.Sprintf("%08x", template.CurTime),
		CleanJobs:        true,
		Target:           template.Target,
		OriginalPrevHash: originalPrevHash,
		Transactions:     txData,
		AuxWork:          auxWork,
	}
}

// CoinbaseExtranonceReserve is the total extranonce byte count (extranonce1 +
// extranonce2, or the V2 single extranonce) reserved in the coinbase scriptSig.
// One coinbase is shared across all stratum servers, so every enabled server's
// extranonce sizes MUST sum to this value or the scriptSig length byte will not
// match the emitted bytes and the assembled block will be rejected. main.go
// asserts this invariant at startup.
const CoinbaseExtranonceReserve = 10

// buildCoinbase builds the split coinbase (cb1, cb2). When commitment is
// non-empty (merge mining), the 44-byte merged-mining commitment is placed in
// the FIXED cb2 region, AFTER the extranonce and the pool tag. Because it sits
// in cb2 (which miners never touch), it is byte-identical regardless of the
// extranonce1/extranonce2 split — so it works unchanged for the standard and
// Braiins stratum ports. scriptSig layout:
//
//	height | <extranonce (reserve)> | "Forge" | [commitment] | <outputs...>
//
// Total scriptSig stays well under the 100-byte limit (height ~4 + reserve 10 +
// tag 5 + commitment 44 = ~63).
func (jm *JobManager) buildCoinbase(template *BlockTemplate, commitment []byte) (string, string) {
	jm.mu.RLock()
	pkh := jm.pubkeyHash
	poolMsg := jm.coinbaseTag
	jm.mu.RUnlock()

	heightBytes := makeHeightScript(template.Height)
	if len(poolMsg) == 0 {
		poolMsg = []byte("Forge")
	}

	scriptLen := len(heightBytes) + CoinbaseExtranonceReserve + len(poolMsg) + len(commitment)

	var cb1 bytes.Buffer
	binary.Write(&cb1, binary.LittleEndian, uint32(1))
	cb1.WriteByte(0x01)
	cb1.Write(make([]byte, 32))
	binary.Write(&cb1, binary.LittleEndian, uint32(0xffffffff))
	cb1.WriteByte(byte(scriptLen))
	cb1.Write(heightBytes)

	var cb2 bytes.Buffer
	cb2.Write(poolMsg)
	if len(commitment) > 0 {
		cb2.Write(commitment) // merged-mining commitment (fabe6d6d + aux hash + ...)
	}
	binary.Write(&cb2, binary.LittleEndian, uint32(0xffffffff))
	cb2.WriteByte(0x01)
	binary.Write(&cb2, binary.LittleEndian, uint64(template.CoinbaseValue))
	cb2.WriteByte(0x19)
	cb2.WriteByte(0x76)
	cb2.WriteByte(0xa9)
	cb2.WriteByte(0x14)
	cb2.Write(pkh)
	cb2.WriteByte(0x88)
	cb2.WriteByte(0xac)
	binary.Write(&cb2, binary.LittleEndian, uint32(0))

	return hex.EncodeToString(cb1.Bytes()), hex.EncodeToString(cb2.Bytes())
}


// sanitizeCoinbaseTag keeps the coinbase scriptSig safe: printable ASCII only,
// defaults to "Forge", capped at 24 bytes (height+reserve+tag+commitment < 100).
func sanitizeCoinbaseTag(tag string) []byte {
	out := make([]byte, 0, len(tag))
	for i := 0; i < len(tag); i++ {
		if c := tag[i]; c >= 0x20 && c < 0x7f {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		out = []byte("Forge")
	}
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}

func makeHeightScript(height int64) []byte {
	if height <= 0 {
		return []byte{0x01, 0x00}
	}
	var heightBytes []byte
	h := height
	for h > 0 {
		heightBytes = append(heightBytes, byte(h&0xff))
		h >>= 8
	}
	if len(heightBytes) > 0 && heightBytes[len(heightBytes)-1] >= 0x80 {
		heightBytes = append(heightBytes, 0x00)
	}
	return append([]byte{byte(len(heightBytes))}, heightBytes...)
}

// stratumPrevHash converts getblocktemplate previousblockhash to stratum format
func stratumPrevHash(gbtHash string) string {
	b, _ := hex.DecodeString(gbtHash)
	if len(b) != 32 {
		return gbtHash
	}
	// First fully reverse (big-endian to little-endian)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	// Then 4-byte swap (so miners swap gets back to little-endian)
	for i := 0; i < 32; i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	return hex.EncodeToString(b)
}

type Job struct {
	ID               string
	Height           int64
	PrevBlockHash    string
	OriginalPrevHash string
	CoinBase1        string
	CoinBase2        string
	MerkleBranches   []string
	Version          string
	NBits            string
	NTime            string
	CleanJobs        bool
	Target           string
	Transactions     []string                // Raw transaction hex data for block building
	AuxWork          *mergemining.AuxWork    // aux-chain work this job commits to (nil = no merge mining)
}

// doubleSHA256 computes double SHA256 hash
func doubleSHA256(data []byte) []byte {
	first := sha256.Sum256(data)
	second := sha256.Sum256(first[:])
	return second[:]
}

// buildMerkleBranches calculates the merkle branches for stratum
// These are the sibling hashes needed to compute merkle root from coinbase
func buildMerkleBranches(txids []string) []string {
	if len(txids) == 0 {
		return []string{}
	}

	// Convert txids to bytes (they come as big-endian hex, need little-endian for merkle)
	var hashes [][]byte
	for _, txid := range txids {
		h, err := hex.DecodeString(txid)
		if err != nil {
			continue
		}
		// Reverse to little-endian for merkle calculation
		for i, j := 0, len(h)-1; i < j; i, j = i+1, j-1 {
			h[i], h[j] = h[j], h[i]
		}
		hashes = append(hashes, h)
	}

	if len(hashes) == 0 {
		return []string{}
	}

	// Build merkle branches - collect the sibling at each level
	// At each level, first hash is our sibling (branch), then we compute
	// the subtree hash from remaining hashes for the next level
	var branches []string

	for len(hashes) > 0 {
		// First hash at current level is our branch (sibling to coinbase path)
		branches = append(branches, hex.EncodeToString(hashes[0]))

		// Remove the branch hash and compute next level from remaining
		hashes = hashes[1:]
		if len(hashes) == 0 {
			break
		}

		// Compute next level from remaining hashes
		var nextLevel [][]byte
		for i := 0; i < len(hashes); i += 2 {
			var combined []byte
			if i+1 < len(hashes) {
				combined = append(hashes[i], hashes[i+1]...)
			} else {
				// Odd number - duplicate last hash
				combined = append(hashes[i], hashes[i]...)
			}
			nextLevel = append(nextLevel, doubleSHA256(combined))
		}
		hashes = nextLevel
	}

	return branches
}
