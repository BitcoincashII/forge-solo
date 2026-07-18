package rental

import (
	"bytes"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"golang.org/x/crypto/pbkdf2"
)

// Wallet handles BTC transactions for auto-funding Braiins
type Wallet struct {
	masterKey  *hdkeychain.ExtendedKey
	netParams  *chaincfg.Params
	httpClient *http.Client
}

// UTXO represents an unspent transaction output
type UTXO struct {
	TxID   string `json:"txid"`
	Vout   uint32 `json:"vout"`
	Value  int64  `json:"value"`
	Status struct {
		Confirmed bool `json:"confirmed"`
	} `json:"status"`
}

// NewWallet creates a wallet from an Electrum-compatible mnemonic
// Note: Electrum uses different seed derivation than BIP39 - it uses "electrum" as salt prefix
func NewWallet(mnemonic string) (*Wallet, error) {
	if mnemonic == "" {
		return nil, fmt.Errorf("mnemonic is required")
	}

	// Electrum seed derivation: PBKDF2 with "electrum" salt prefix (not "mnemonic" like BIP39)
	seed := pbkdf2.Key([]byte(mnemonic), []byte("electrum"), 2048, 64, sha512.New)

	// Create master key
	masterKey, err := hdkeychain.NewMaster(seed, &chaincfg.MainNetParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create master key: %w", err)
	}

	return &Wallet{
		masterKey:  masterKey,
		netParams:  &chaincfg.MainNetParams,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// deriveKey derives a key at Electrum native segwit path: m/0'/change/index
// Note: This matches Electrum's standard native segwit wallet derivation
func (w *Wallet) deriveKey(change, index uint32) (*hdkeychain.ExtendedKey, error) {
	// m/0' (account - hardened)
	account, err := w.masterKey.Derive(hdkeychain.HardenedKeyStart + 0)
	if err != nil {
		return nil, err
	}

	// m/0'/change (0 = external/receiving, 1 = internal/change)
	changeKey, err := account.Derive(change)
	if err != nil {
		return nil, err
	}

	// m/0'/change/index
	return changeKey.Derive(index)
}

// GetAddress returns the native segwit address for a derivation index (external chain)
func (w *Wallet) GetAddress(index uint32) (string, error) {
	return w.GetAddressForChain(0, index)
}

// GetChangeAddress returns the native segwit address for the change chain
func (w *Wallet) GetChangeAddress(index uint32) (string, error) {
	return w.GetAddressForChain(1, index)
}

// GetAddressForChain returns the native segwit address for a specific chain and index
func (w *Wallet) GetAddressForChain(chain, index uint32) (string, error) {
	key, err := w.deriveKey(chain, index)
	if err != nil {
		return "", err
	}

	pubKey, err := key.ECPubKey()
	if err != nil {
		return "", err
	}

	witnessAddr, err := btcutil.NewAddressWitnessPubKeyHash(
		btcutil.Hash160(pubKey.SerializeCompressed()),
		w.netParams,
	)
	if err != nil {
		return "", err
	}

	return witnessAddr.EncodeAddress(), nil
}

// chainIndex encodes chain and index for address lookup
type chainIndex struct {
	Chain uint32
	Index uint32
}

// GetAllUTXOs fetches UTXOs for all addresses up to maxIndex on both chains
func (w *Wallet) GetAllUTXOs(maxIndex int) ([]UTXO, map[string]chainIndex, error) {
	var allUTXOs []UTXO
	addressToChainIndex := make(map[string]chainIndex)

	// Scan both external chain (0) and change chain (1)
	for chain := uint32(0); chain <= 1; chain++ {
		for i := uint32(0); i <= uint32(maxIndex); i++ {
			addr, err := w.GetAddressForChain(chain, i)
			if err != nil {
				continue
			}
			addressToChainIndex[addr] = chainIndex{Chain: chain, Index: i}

			utxos, err := w.fetchUTXOs(addr)
			if err != nil {
				log.Printf("Warning: failed to fetch UTXOs for %s: %v", addr, err)
				continue
			}

			allUTXOs = append(allUTXOs, utxos...)
		}
	}

	return allUTXOs, addressToChainIndex, nil
}

// fetchUTXOs fetches UTXOs for a single address from mempool.space
func (w *Wallet) fetchUTXOs(address string) ([]UTXO, error) {
	url := fmt.Sprintf("https://blockstream.info/api/address/%s/utxo", address)

	resp, err := w.httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API error: %d", resp.StatusCode)
	}

	var utxos []UTXO
	if err := json.NewDecoder(resp.Body).Decode(&utxos); err != nil {
		return nil, err
	}

	return utxos, nil
}

// SendToAddress sends BTC to a destination address
// Returns the transaction ID
func (w *Wallet) SendToAddress(destAddress string, amountSat int64, maxIndex int) (string, error) {
	// Validate destination address
	_, err := btcutil.DecodeAddress(destAddress, w.netParams)
	if err != nil {
		return "", fmt.Errorf("invalid destination address: %w", err)
	}

	// Get all UTXOs
	utxos, addressToChainIndex, err := w.GetAllUTXOs(maxIndex)
	if err != nil {
		return "", fmt.Errorf("failed to get UTXOs: %w", err)
	}

	// Filter confirmed UTXOs and sort by value (largest first)
	var confirmedUTXOs []UTXO
	for _, utxo := range utxos {
		if utxo.Status.Confirmed {
			confirmedUTXOs = append(confirmedUTXOs, utxo)
		}
	}
	sort.Slice(confirmedUTXOs, func(i, j int) bool {
		return confirmedUTXOs[i].Value > confirmedUTXOs[j].Value
	})

	// Calculate total available
	var totalAvailable int64
	for _, utxo := range confirmedUTXOs {
		totalAvailable += utxo.Value
	}

	// Estimate fee (use ~5 sat/vB for segwit, ~140 vbytes for 1-in-2-out)
	feeRate := int64(5) // sat/vB
	estimatedVSize := int64(140)
	fee := feeRate * estimatedVSize

	if totalAvailable < amountSat+fee {
		return "", fmt.Errorf("insufficient funds: have %d, need %d + %d fee", totalAvailable, amountSat, fee)
	}

	// Select UTXOs (simple: use largest first until we have enough)
	var selectedUTXOs []UTXO
	var selectedTotal int64
	for _, utxo := range confirmedUTXOs {
		selectedUTXOs = append(selectedUTXOs, utxo)
		selectedTotal += utxo.Value
		// Adjust fee for more inputs (~68 vbytes per input)
		fee = feeRate * (68*int64(len(selectedUTXOs)) + 60)
		if selectedTotal >= amountSat+fee {
			break
		}
	}

	if selectedTotal < amountSat+fee {
		return "", fmt.Errorf("insufficient confirmed funds after selection")
	}

	// Build transaction
	tx := wire.NewMsgTx(wire.TxVersion)

	// Add inputs
	for _, utxo := range selectedUTXOs {
		txHash, err := chainhash.NewHashFromStr(utxo.TxID)
		if err != nil {
			return "", fmt.Errorf("invalid txid: %w", err)
		}
		outPoint := wire.NewOutPoint(txHash, utxo.Vout)
		txIn := wire.NewTxIn(outPoint, nil, nil)
		tx.AddTxIn(txIn)
	}

	// Add destination output
	destAddr, _ := btcutil.DecodeAddress(destAddress, w.netParams)
	destScript, err := txscript.PayToAddrScript(destAddr)
	if err != nil {
		return "", fmt.Errorf("failed to create output script: %w", err)
	}
	tx.AddTxOut(wire.NewTxOut(amountSat, destScript))

	// Add change output to external chain (chain 0) with unique index
	// IMPORTANT: Customer deposit addresses use chain 1 (via HDWallet.DeriveAddress),
	// so change MUST go to chain 0 to avoid collision. Using chain 0 ensures change
	// outputs won't be mistakenly credited as customer deposits.
	change := selectedTotal - amountSat - fee
	if change > 546 { // Dust threshold
		// Use external chain (0) with an index based on input count to vary the address
		// In production, the remote signer tracks change indices in wallet_change_addresses
		changeIndex := uint32(100 + len(selectedUTXOs)) // Start at 100 to avoid early addresses
		changeAddr, err := w.GetAddress(changeIndex)
		if err != nil {
			return "", fmt.Errorf("failed to derive change address: %w", err)
		}
		changeAddrParsed, _ := btcutil.DecodeAddress(changeAddr, w.netParams)
		changeScript, _ := txscript.PayToAddrScript(changeAddrParsed)
		tx.AddTxOut(wire.NewTxOut(change, changeScript))
	}

	// Sign inputs
	for i, utxo := range selectedUTXOs {
		// Find the address for this UTXO
		addr, err := w.getAddressForUTXO(utxo.TxID, utxo.Vout)
		if err != nil {
			return "", fmt.Errorf("failed to get address for UTXO: %w", err)
		}

		ci, ok := addressToChainIndex[addr]
		if !ok {
			return "", fmt.Errorf("address %s not in wallet", addr)
		}

		key, err := w.deriveKey(ci.Chain, ci.Index)
		if err != nil {
			return "", fmt.Errorf("failed to derive key: %w", err)
		}

		if err := w.signInput(tx, i, utxo.Value, key); err != nil {
			return "", fmt.Errorf("failed to sign input %d: %w", i, err)
		}
	}

	// Broadcast transaction
	txid, err := w.broadcastTx(tx)
	if err != nil {
		return "", fmt.Errorf("failed to broadcast: %w", err)
	}

	log.Printf("Wallet: Sent %d sats to %s, txid: %s, fee: %d", amountSat, destAddress, txid, fee)
	return txid, nil
}

// getAddressForUTXO fetches the address that owns a specific UTXO
func (w *Wallet) getAddressForUTXO(txid string, vout uint32) (string, error) {
	url := fmt.Sprintf("https://blockstream.info/api/tx/%s", txid)

	resp, err := w.httpClient.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var txData struct {
		Vout []struct {
			ScriptPubKeyAddress string `json:"scriptpubkey_address"`
		} `json:"vout"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&txData); err != nil {
		return "", err
	}

	if int(vout) >= len(txData.Vout) {
		return "", fmt.Errorf("vout index out of range")
	}

	return txData.Vout[vout].ScriptPubKeyAddress, nil
}

// signInput signs a single input using P2WPKH
func (w *Wallet) signInput(tx *wire.MsgTx, inputIndex int, value int64, key *hdkeychain.ExtendedKey) error {
	privKey, err := key.ECPrivKey()
	if err != nil {
		return err
	}

	pubKey, err := key.ECPubKey()
	if err != nil {
		return err
	}

	// Create P2WPKH script for signing
	pubKeyHash := btcutil.Hash160(pubKey.SerializeCompressed())
	
	// For P2WPKH, we sign against the scriptCode (OP_DUP OP_HASH160 <pubkeyhash> OP_EQUALVERIFY OP_CHECKSIG)
	scriptCode, err := txscript.NewScriptBuilder().
		AddOp(txscript.OP_DUP).
		AddOp(txscript.OP_HASH160).
		AddData(pubKeyHash).
		AddOp(txscript.OP_EQUALVERIFY).
		AddOp(txscript.OP_CHECKSIG).
		Script()
	if err != nil {
		return err
	}

	// Create the prevout fetcher for signature hashing
	prevOutFetcher := txscript.NewCannedPrevOutputFetcher(scriptCode, value)
	sigHashes := txscript.NewTxSigHashes(tx, prevOutFetcher)

	// Generate witness signature
	sig, err := txscript.RawTxInWitnessSignature(tx, sigHashes, inputIndex,
		value, scriptCode, txscript.SigHashAll, privKey)
	if err != nil {
		return err
	}

	// Set witness data
	tx.TxIn[inputIndex].Witness = wire.TxWitness{
		sig,
		pubKey.SerializeCompressed(),
	}

	return nil
}

// broadcastTx broadcasts a signed transaction via mempool.space
func (w *Wallet) broadcastTx(tx *wire.MsgTx) (string, error) {
	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		return "", err
	}

	txHex := hex.EncodeToString(buf.Bytes())

	resp, err := w.httpClient.Post(
		"https://blockstream.info/api/tx",
		"text/plain",
		bytes.NewBufferString(txHex),
	)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("broadcast failed: %s", string(body))
	}

	return string(body), nil
}

// GetBalance returns total confirmed balance across all addresses
func (w *Wallet) GetBalance(maxIndex int) (int64, error) {
	utxos, _, err := w.GetAllUTXOs(maxIndex)
	if err != nil {
		return 0, err
	}

	var total int64
	for _, utxo := range utxos {
		if utxo.Status.Confirmed {
			total += utxo.Value
		}
	}

	return total, nil
}

// VerifyAddress checks if an address at index matches expected
func (w *Wallet) VerifyAddress(index uint32, expected string) bool {
	addr, err := w.GetAddress(index)
	if err != nil {
		return false
	}
	return addr == expected
}

// Compile-time check to ensure btcec is imported (needed for signing)
var _ = btcec.S256()

// ============================================================================
// HDWallet - Watch-only wallet for address derivation from xpub
// ============================================================================

// HDWallet is a watch-only wallet that derives addresses from an xpub
type HDWallet struct {
	accountKey *hdkeychain.ExtendedKey
	netParams  *chaincfg.Params
}

// NewHDWallet creates a watch-only wallet from an xpub
func NewHDWallet(xpub string) (*HDWallet, error) {
	if xpub == "" {
		return nil, fmt.Errorf("xpub is required")
	}

	key, err := hdkeychain.NewKeyFromString(xpub)
	if err != nil {
		return nil, fmt.Errorf("invalid xpub: %w", err)
	}

	// Verify it's a public key (not private)
	if key.IsPrivate() {
		return nil, fmt.Errorf("expected xpub, got xprv")
	}

	return &HDWallet{
		accountKey: key,
		netParams:  &chaincfg.MainNetParams,
	}, nil
}

// DeriveExternalAddress derives an address from the external chain (xpub/0/index)
// Used only for verifying hot wallet matches the xpub
func (w *HDWallet) DeriveExternalAddress(index uint32) (string, error) {
	externalKey, err := w.accountKey.Derive(0)
	if err != nil {
		return "", fmt.Errorf("failed to derive external chain: %w", err)
	}

	addressKey, err := externalKey.Derive(index)
	if err != nil {
		return "", fmt.Errorf("failed to derive address key: %w", err)
	}

	pubKey, err := addressKey.ECPubKey()
	if err != nil {
		return "", fmt.Errorf("failed to get public key: %w", err)
	}

	pubKeyHash := btcutil.Hash160(pubKey.SerializeCompressed())
	addr, err := btcutil.NewAddressWitnessPubKeyHash(pubKeyHash, w.netParams)
	if err != nil {
		return "", fmt.Errorf("failed to create address: %w", err)
	}

	return addr.EncodeAddress(), nil
}

// DeriveAddress derives a native SegWit (bech32) address at the given index
// Uses path: xpub/1/index (change chain) to avoid collision with hot wallet (which uses xpub/0/x)
func (w *HDWallet) DeriveAddress(index uint32) (string, error) {
	// Derive change chain (1) - hot wallet uses external chain (0)
	changeKey, err := w.accountKey.Derive(1)
	if err != nil {
		return "", fmt.Errorf("failed to derive change chain: %w", err)
	}

	// Derive address at index
	addressKey, err := changeKey.Derive(index)
	if err != nil {
		return "", fmt.Errorf("failed to derive address key: %w", err)
	}

	pubKey, err := addressKey.ECPubKey()
	if err != nil {
		return "", fmt.Errorf("failed to get public key: %w", err)
	}

	// Create native SegWit address (P2WPKH)
	pubKeyHash := btcutil.Hash160(pubKey.SerializeCompressed())
	addr, err := btcutil.NewAddressWitnessPubKeyHash(pubKeyHash, w.netParams)
	if err != nil {
		return "", fmt.Errorf("failed to create address: %w", err)
	}

	return addr.EncodeAddress(), nil
}

// DeriveAddressP2SH derives a P2SH-P2WPKH address (starts with 3)
// Uses change chain (1) to avoid collision with hot wallet
func (w *HDWallet) DeriveAddressP2SH(index uint32) (string, error) {
	changeKey, err := w.accountKey.Derive(1)
	if err != nil {
		return "", err
	}

	addressKey, err := changeKey.Derive(index)
	if err != nil {
		return "", err
	}

	pubKey, err := addressKey.ECPubKey()
	if err != nil {
		return "", err
	}

	pubKeyHash := btcutil.Hash160(pubKey.SerializeCompressed())
	witnessAddr, err := btcutil.NewAddressWitnessPubKeyHash(pubKeyHash, w.netParams)
	if err != nil {
		return "", err
	}

	script, err := txscript.PayToAddrScript(witnessAddr)
	if err != nil {
		return "", err
	}

	addr, err := btcutil.NewAddressScriptHash(script, w.netParams)
	if err != nil {
		return "", err
	}

	return addr.EncodeAddress(), nil
}

// DeriveAddressLegacy derives a legacy P2PKH address (starts with 1)
// Uses change chain (1) to avoid collision with hot wallet
func (w *HDWallet) DeriveAddressLegacy(index uint32) (string, error) {
	changeKey, err := w.accountKey.Derive(1)
	if err != nil {
		return "", err
	}

	addressKey, err := changeKey.Derive(index)
	if err != nil {
		return "", err
	}

	pubKey, err := addressKey.ECPubKey()
	if err != nil {
		return "", err
	}

	pubKeyHash := btcutil.Hash160(pubKey.SerializeCompressed())
	addr, err := btcutil.NewAddressPubKeyHash(pubKeyHash, w.netParams)
	if err != nil {
		return "", err
	}

	return addr.EncodeAddress(), nil
}

// GetAllUTXOs fetches UTXOs for all addresses up to maxIndex on both chains (watch-only)
// Returns UTXOs and a map of address -> chainIndex for derivation tracking
func (w *HDWallet) GetAllUTXOs(maxIndex int) ([]UTXO, map[string]chainIndex, error) {
	var allUTXOs []UTXO
	addressToChainIndex := make(map[string]chainIndex)
	httpClient := &http.Client{Timeout: 30 * time.Second}

	// Scan both external chain (0) and change chain (1)
	for chain := uint32(0); chain <= 1; chain++ {
		for i := uint32(0); i <= uint32(maxIndex); i++ {
			var addr string
			var err error
			if chain == 0 {
				addr, err = w.DeriveExternalAddress(i)
			} else {
				addr, err = w.DeriveAddress(i) // Change chain
			}
			if err != nil {
				continue
			}
			addressToChainIndex[addr] = chainIndex{Chain: chain, Index: i}

			utxos, err := fetchUTXOsForAddress(httpClient, addr)
			if err != nil {
				log.Printf("Warning: failed to fetch UTXOs for %s: %v", addr, err)
				continue
			}

			allUTXOs = append(allUTXOs, utxos...)
		}
	}

	return allUTXOs, addressToChainIndex, nil
}

// fetchUTXOsForAddress fetches UTXOs for a single address from mempool.space
func fetchUTXOsForAddress(client *http.Client, address string) ([]UTXO, error) {
	url := fmt.Sprintf("https://blockstream.info/api/address/%s/utxo", address)

	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API error: %d", resp.StatusCode)
	}

	var utxos []UTXO
	if err := json.NewDecoder(resp.Body).Decode(&utxos); err != nil {
		return nil, err
	}

	return utxos, nil
}

// GenerateTestXPub generates a random xpub for testing
func GenerateTestXPub() (string, error) {
	// Generate random seed
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return "", err
	}

	// Create master key
	masterKey, err := hdkeychain.NewMaster(seed, &chaincfg.MainNetParams)
	if err != nil {
		return "", err
	}

	// Derive BIP84 account path: m/84'/0'/0'
	purpose, _ := masterKey.Derive(hdkeychain.HardenedKeyStart + 84)
	coinType, _ := purpose.Derive(hdkeychain.HardenedKeyStart + 0)
	account, _ := coinType.Derive(hdkeychain.HardenedKeyStart + 0)

	// Get the neutered (public) key
	accountPub, err := account.Neuter()
	if err != nil {
		return "", err
	}

	return accountPub.String(), nil
}

// ValidateXPub validates an xpub string
func ValidateXPub(xpub string) error {
	if xpub == "" {
		return fmt.Errorf("xpub is required")
	}

	key, err := hdkeychain.NewKeyFromString(xpub)
	if err != nil {
		return fmt.Errorf("invalid xpub format: %w", err)
	}

	if key.IsPrivate() {
		return fmt.Errorf("expected public key (xpub), got private key")
	}

	return nil
}
