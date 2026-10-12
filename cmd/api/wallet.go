package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/cashaddr"
	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/gofiber/fiber/v2"
)

// The dashboard's Wallet card: the coins at the payout address, and at the 1175 address, as this
// install's own nodes see them. Each node is asked with scantxoutset, which reads its set of
// unspent outputs and works with its wallet off, as Forge Solo runs every node: no other service is
// asked, and Forge Solo holds no keys. The request takes no input. The addresses are the ones in
// effect (the stored settings, else the app config's), decoded here into the script they pay to,
// and the node is sent only raw(<that script in hex>).
//
// A scan reads the node's whole set of coins, so its answer is kept: a node is asked again at its
// next block, never twice within walletScanFloor and never twice at once.

const (
	walletChainFor   = 5 * time.Second  // a getblockchaininfo answer is used this long
	walletScanFloor  = 30 * time.Second // the least time between two scans of one address
	walletRetryAfter = 2 * time.Minute  // after a scan the node did not finish in walletScanTimeout
)

var (
	walletScanTimeout = 60 * time.Second // a variable, so a test can shorten it
	walletNow         = time.Now         // likewise
)

// The states of an address on the card.
const (
	walletOK          = "ok"          // the figures are for the node's current tip
	walletSyncing     = "syncing"     // the node is behind its headers, and is not asked
	walletUnavailable = "unavailable" // the node could not be asked, or the address cannot be looked up
)

// walletCoin is one address on the card.
type walletCoin struct {
	Address string `json:"address"`
	State   string `json:"state"`
	// Message is one sentence, for any state but ok.
	Message string `json:"message,omitempty"`
	// Figures are the current ones when ok, and otherwise the last good ones for this address.
	Figures *walletFigures `json:"figures,omitempty"`
}

// walletFigures are the coins at an address at one tip, in satoshis.
type walletFigures struct {
	Height        int64         `json:"height"` // the tip they were read at
	SpendableSats int64         `json:"spendable_sats"`
	MaturingSats  int64         `json:"maturing_sats"`
	Outputs       int           `json:"outputs"`  // every unspent output at the address
	Maturing      []maturingRow `json:"maturing"` // soonest first
	CheckedAt     time.Time     `json:"checked_at"`
}

// maturingRow is what one block paid the address in coinbase outputs that cannot be spent yet.
type maturingRow struct {
	Height     int64 `json:"height"`
	Sats       int64 `json:"sats"`
	BlocksToGo int64 `json:"blocks_to_go"`
}

// walletNode is one node the Wallet card asks: the BCH2 node, or the 1175 node.
type walletNode struct {
	node       string                          // "BCH2 node" or "1175 node", as the messages name it
	address    string                          // "payout address" or "1175 address", likewise
	login      func() (url, user, pass string) // read at every question
	decode     func(addr string) ([]byte, error)
	badAddress string // the message for an address decode refuses

	mu        sync.Mutex // held while this node is asked: one question at a time, and the others wait for its answer
	chain     blockchainInfo
	chainAt   time.Time
	script    []byte         // the address the figures below are for
	figures   *walletFigures // nil until a scan of script succeeds
	bestblock string         // the tip the figures were read at (the scan's bestblock)
	scannedAt time.Time
	retryAt   time.Time // after a scan the node did not finish in time, none before this
}

var (
	bch2Wallet = &walletNode{node: "BCH2 node", address: "payout address", login: bch2Login, decode: bch2Script,
		badAddress: "The payout address in Settings is not one Forge Solo can look up: it must be a bitcoincashii:q… address."}
	aux1175Wallet = &walletNode{node: "1175 node", address: "1175 address", login: aux1175Login,
		decode:     func(addr string) ([]byte, error) { return segwitScript(addr, "esf") },
		badAddress: "The 1175 address in Settings is not one Forge Solo can look up: it must be an esf1… address with a valid witness program."}
)

// getWallet answers GET /api/v1/wallet: {} while no payout address is set, and no node is asked
// then; else the payout address's coins, and the 1175 address's where this app runs a 1175 node and
// one is set, in TIDES mode too.
func getWallet(c *fiber.Ctx) error {
	addr, err := payoutAddressInEffect()
	if err != nil {
		return settingsUnreadable(c, err)
	}
	out := fiber.Map{}
	if addr == "" {
		return c.JSON(out)
	}
	_, stored1175, _, err := stats.GetPoolConfig()
	if err != nil {
		return settingsUnreadable(c, err)
	}
	// The two nodes are asked side by side: one that does not answer holds up only its own row.
	addr1175 := address1175InEffect(stored1175)
	var aux walletCoin
	var wg sync.WaitGroup
	if addr1175 != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			aux = aux1175Wallet.coin(addr1175)
		}()
	}
	out["bch2"] = bch2Wallet.coin(addr)
	wg.Wait()
	if addr1175 != "" {
		out["aux1175"] = aux
	}
	return c.JSON(out)
}

// coin is what the card shows for addr. The node is asked only when its tip moved on.
func (n *walletNode) coin(addr string) walletCoin {
	out := walletCoin{Address: addr}
	script, err := n.decode(addr)
	if err != nil {
		out.State, out.Message = walletUnavailable, n.badAddress
		return out
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !bytes.Equal(script, n.script) {
		n.script, n.figures, n.bestblock = script, nil, ""
	}
	out.Figures = n.figures
	now := walletNow()
	if now.Sub(n.chainAt) >= walletChainFor {
		ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
		raw, err := n.call(ctx, "getblockchaininfo", []interface{}{})
		cancel()
		var chain blockchainInfo
		if err == nil && json.Unmarshal(raw, &chain) != nil {
			err = errUnreadableAnswer
		}
		if err != nil {
			out.State, out.Message = walletUnavailable, n.cannotAsk(nodeTrouble(err))
			return out
		}
		n.chain, n.chainAt = chain, now
	}
	switch {
	case !chainSynced(n.chain.Blocks, n.chain.Headers, n.chain.InitialBlockDownload):
		out.State = walletSyncing
		out.Message = "The " + n.node + " is still syncing: the coins at your " + n.address + " show here once it has caught up."
		return out
	case n.figures != nil && (n.bestblock == n.chain.BestBlockHash || now.Sub(n.scannedAt) < walletScanFloor):
		// At the node's tip, or a block or two behind it: Height says which.
		out.State = walletOK
		return out
	case now.Before(n.retryAt):
		out.State, out.Message = walletUnavailable, n.cannotAsk("the node did not finish in a minute")
		return out
	}

	ctx, cancel := context.WithTimeout(context.Background(), walletScanTimeout)
	defer cancel()
	raw, err := n.call(ctx, "scantxoutset", []interface{}{"start", []string{"raw(" + hex.EncodeToString(script) + ")"}})
	if ctx.Err() == context.DeadlineExceeded {
		// A scan left running holds the node's one scan from everyone else, and one of its RPC threads.
		abort, cancelAbort := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = n.call(abort, "scantxoutset", []interface{}{"abort"})
		cancelAbort()
		n.retryAt = now.Add(walletRetryAfter)
		log.Printf("WARNING: wallet: the %s did not finish scanning in %ds; asked it to stop", n.node, int(walletScanTimeout/time.Second))
		out.State, out.Message = walletUnavailable, n.cannotAsk("the node did not finish in a minute")
		return out
	}
	var scan scanAnswer
	if err == nil && json.Unmarshal(raw, &scan) != nil {
		err = errUnreadableAnswer
	}
	var figures *walletFigures
	if err == nil && scan.Success {
		figures, err = splitCoins(scan.Height, scan.Unspents)
	}
	switch {
	case err != nil:
		out.State, out.Message = walletUnavailable, n.cannotAsk(nodeTrouble(err))
		return out
	case !scan.Success:
		// Someone stopped it (scantxoutset abort).
		out.State, out.Message = walletUnavailable, n.cannotAsk("the scan was stopped before it finished")
		return out
	}
	figures.CheckedAt = now.UTC().Truncate(time.Second)
	n.figures, n.bestblock, n.scannedAt = figures, scan.BestBlock, now
	out.State, out.Figures = walletOK, figures
	return out
}

func (n *walletNode) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	url, user, pass := n.login()
	return rpcCallCtx(ctx, url, user, pass, method, params)
}

func (n *walletNode) cannotAsk(why string) string {
	return "Forge Solo cannot ask the " + n.node + " right now: " + strings.TrimRight(why, ".") + ". It asks again in a minute."
}

// nodeTrouble is why a question to a node failed, in a few words.
func nodeTrouble(err error) string {
	var re *rpcError
	switch {
	case errors.As(err, &re) && re.code == rpcInWarmup:
		return "the node is starting (" + re.message + ")"
	case errors.As(err, &re) && re.code == rpcInvalidParameter && strings.Contains(re.message, "Scan already in progress"):
		return "another scan of the node's coins is running"
	case errors.As(err, &re):
		return re.message
	case errors.Is(err, errAnswerTooBig), errors.Is(err, errUnreadableAnswer):
		return err.Error()
	}
	// No answer, or one that is not the node's (a refused login, a page from something else).
	return "the node is not answering"
}

// rpcInvalidParameter is, among others, the node's answer to a scan while another one runs: "Scan
// already in progress, use action "abort" or "status"".
const rpcInvalidParameter = -8

var errUnreadableAnswer = errors.New("the node's answer could not be read")

// scanAnswer is what scantxoutset start answers, as far as the card reads it.
type scanAnswer struct {
	Success   bool          `json:"success"` // false: the scan was stopped before it finished
	Height    int64         `json:"height"`
	BestBlock string        `json:"bestblock"`
	Unspents  []scanUnspent `json:"unspents"`
}

type scanUnspent struct {
	Amount   json.Number `json:"amount"`
	Coinbase bool        `json:"coinbase"`
	Height   int64       `json:"height"`
}

// maxSats is 21 million coins: no amount, and no sum of amounts, is more.
const maxSats = 21_000_000 * 100_000_000

// splitCoins is the coins of a scan at tip, split as the balance card splits a block's reward
// (stats.COINBASE_MATURITY): a coinbase output is spendable once 100 blocks are on top of the block
// that paid it, so one page never calls a coin spendable in one card and maturing in another.
// Coinbase outputs are solo block rewards, TIDES payouts and merge-mined 1175 rewards alike.
func splitCoins(tip int64, unspents []scanUnspent) (*walletFigures, error) {
	f := &walletFigures{Height: tip, Outputs: len(unspents), Maturing: []maturingRow{}}
	byHeight := map[int64]int64{}
	for _, u := range unspents {
		sats, err := satsFromAmount(u.Amount)
		if err != nil {
			return nil, err
		}
		if u.Coinbase && tip-u.Height < stats.COINBASE_MATURITY {
			byHeight[u.Height] += sats
			f.MaturingSats += sats
		} else {
			f.SpendableSats += sats
		}
		if f.SpendableSats+f.MaturingSats > maxSats {
			return nil, errUnreadableAnswer
		}
	}
	for h, sats := range byHeight {
		f.Maturing = append(f.Maturing, maturingRow{Height: h, Sats: sats, BlocksToGo: h + stats.COINBASE_MATURITY - tip})
	}
	sort.Slice(f.Maturing, func(i, j int) bool { return f.Maturing[i].Height < f.Maturing[j].Height })
	return f, nil
}

// satsFromAmount reads an amount as the node writes it, in coins with up to 8 decimals
// ("50.00000000"), into satoshis exactly: never through a float.
func satsFromAmount(a json.Number) (int64, error) {
	whole, frac, _ := strings.Cut(string(a), ".")
	if whole == "" || len(whole) > 8 || len(frac) > 8 || !allDigits(whole) || !allDigits(frac) {
		return 0, errUnreadableAnswer
	}
	var sats int64
	for _, c := range whole + frac + strings.Repeat("0", 8-len(frac)) {
		sats = sats*10 + int64(c-'0')
	}
	if sats > maxSats {
		return 0, errUnreadableAnswer
	}
	return sats, nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// bch2Script is the output script of a BCH2 payout address: P2PKH only, as Settings accepts.
func bch2Script(addr string) ([]byte, error) {
	a, err := cashaddr.Decode(addr, cashaddr.MainnetPrefix)
	if err != nil {
		return nil, err
	}
	if a.Type != cashaddr.P2PKH {
		return nil, errors.New("not a P2PKH address")
	}
	return a.Script(), nil
}

// segwitScript is the output script of a segwit address whose human-readable part is hrp ("esf" for
// 1175): witness version 0 with a bech32 checksum and a 20- or 32-byte program, or version 1 to 16
// with bech32m and 2 to 40 bytes (BIP 173, BIP 350).
func segwitScript(addr, hrp string) ([]byte, error) {
	got, data, checksum, err := bech32.DecodeGeneric(addr)
	if err != nil {
		return nil, err
	}
	if got != hrp || len(data) == 0 {
		return nil, fmt.Errorf("not an address for %s1…", hrp)
	}
	version := data[0]
	program, err := bech32.ConvertBits(data[1:], 5, 8, false)
	if err != nil {
		return nil, err
	}
	switch {
	case version > 16 || len(program) < 2 || len(program) > 40:
		return nil, errors.New("not a witness program")
	case version == 0 && (checksum != bech32.Version0 || len(program) != 20 && len(program) != 32):
		return nil, errors.New("not a version 0 witness program with a bech32 checksum")
	case version > 0 && checksum != bech32.VersionM:
		return nil, errors.New("a witness version above 0 takes a bech32m checksum")
	}
	op := byte(0) // OP_0, or OP_1 to OP_16
	if version > 0 {
		op = 0x50 + version
	}
	return append([]byte{op, byte(len(program))}, program...), nil
}
