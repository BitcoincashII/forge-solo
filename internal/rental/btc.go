package rental

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// BTCWatcher handles BTC address generation and deposit monitoring
type BTCWatcher struct {
	xpub          string
	mempoolAPIURL string
	httpClient    *http.Client
}

// NewBTCWatcher creates a new BTC watcher
func NewBTCWatcher(xpub string) *BTCWatcher {
	return &BTCWatcher{
		xpub:          xpub,
		mempoolAPIURL: "https://blockstream.info/api",
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// MempoolAddressResponse from mempool.space API
type MempoolAddressResponse struct {
	Address    string `json:"address"`
	ChainStats struct {
		FundedTxoCount int   `json:"funded_txo_count"`
		FundedTxoSum   int64 `json:"funded_txo_sum"`
		SpentTxoCount  int   `json:"spent_txo_count"`
		SpentTxoSum    int64 `json:"spent_txo_sum"`
	} `json:"chain_stats"`
	MempoolStats struct {
		FundedTxoCount int   `json:"funded_txo_count"`
		FundedTxoSum   int64 `json:"funded_txo_sum"`
		SpentTxoCount  int   `json:"spent_txo_count"`
		SpentTxoSum    int64 `json:"spent_txo_sum"`
	} `json:"mempool_stats"`
}

// MempoolTx represents a transaction from mempool.space
type MempoolTx struct {
	TxID   string `json:"txid"`
	Status struct {
		Confirmed   bool  `json:"confirmed"`
		BlockHeight int64 `json:"block_height,omitempty"`
		BlockTime   int64 `json:"block_time,omitempty"`
	} `json:"status"`
	Vout []struct {
		ScriptPubKey         string `json:"scriptpubkey"`
		ScriptPubKeyAsm      string `json:"scriptpubkey_asm"`
		ScriptPubKeyType     string `json:"scriptpubkey_type"`
		ScriptPubKeyAddress  string `json:"scriptpubkey_address"`
		Value                int64  `json:"value"`
	} `json:"vout"`
}

// MempoolBlockTip represents current block height
type MempoolBlockTip struct {
	Height int64  `json:"height"`
	Hash   string `json:"hash"`
}

// GetAddressInfo fetches address information from mempool.space
func (w *BTCWatcher) GetAddressInfo(address string) (*MempoolAddressResponse, error) {
	url := fmt.Sprintf("%s/address/%s", w.mempoolAPIURL, address)

	resp, err := w.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch address info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("mempool API error: %s", string(body))
	}

	var result MempoolAddressResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// GetAddressTransactions fetches transactions for an address
func (w *BTCWatcher) GetAddressTransactions(address string) ([]MempoolTx, error) {
	url := fmt.Sprintf("%s/address/%s/txs", w.mempoolAPIURL, address)

	resp, err := w.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch transactions: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("mempool API error: %s", string(body))
	}

	var txs []MempoolTx
	if err := json.NewDecoder(resp.Body).Decode(&txs); err != nil {
		return nil, fmt.Errorf("failed to decode transactions: %w", err)
	}

	return txs, nil
}

// GetBlockHeight fetches the current block height
func (w *BTCWatcher) GetBlockHeight() (int64, error) {
	url := fmt.Sprintf("%s/blocks/tip/height", w.mempoolAPIURL)

	resp, err := w.httpClient.Get(url)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch block height: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("mempool API error: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var height int64
	if err := json.Unmarshal(body, &height); err != nil {
		return 0, fmt.Errorf("failed to decode height: %w", err)
	}

	return height, nil
}

// GetTxConfirmations gets the number of confirmations for a transaction
func (w *BTCWatcher) GetTxConfirmations(txid string) (int, error) {
	url := fmt.Sprintf("%s/tx/%s", w.mempoolAPIURL, txid)

	resp, err := w.httpClient.Get(url)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch tx: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("mempool API error: status %d", resp.StatusCode)
	}

	var tx MempoolTx
	if err := json.NewDecoder(resp.Body).Decode(&tx); err != nil {
		return 0, fmt.Errorf("failed to decode tx: %w", err)
	}

	if !tx.Status.Confirmed {
		return 0, nil
	}

	// Get current height to calculate confirmations
	currentHeight, err := w.GetBlockHeight()
	if err != nil {
		return 0, err
	}

	confirmations := int(currentHeight - tx.Status.BlockHeight + 1)
	return confirmations, nil
}

// FindDepositsToAddress finds all incoming deposits to an address
func (w *BTCWatcher) FindDepositsToAddress(address string) ([]DepositInfo, error) {
	txs, err := w.GetAddressTransactions(address)
	if err != nil {
		return nil, err
	}

	currentHeight, err := w.GetBlockHeight()
	if err != nil {
		currentHeight = 0 // Continue without confirmations
	}

	var deposits []DepositInfo
	for _, tx := range txs {
		// Find outputs to our address
		for i, vout := range tx.Vout {
			if vout.ScriptPubKeyAddress == address {
				confirmations := 0
				if tx.Status.Confirmed && currentHeight > 0 {
					confirmations = int(currentHeight - tx.Status.BlockHeight + 1)
				}

				deposits = append(deposits, DepositInfo{
					TxID:          tx.TxID,
					Vout:          uint32(i),
					Address:       address,
					AmountSat:     vout.Value,
					Confirmations: confirmations,
					Confirmed:     tx.Status.Confirmed,
				})
			}
		}
	}

	return deposits, nil
}

// DepositInfo contains deposit details
type DepositInfo struct {
	TxID          string
	Vout          uint32
	Address       string
	AmountSat     int64
	Confirmations int
	Confirmed     bool
}

// ValidateBTCAddress validates a BTC address format
func ValidateBTCAddress(address string) bool {
	// Basic validation - starts with 1, 3, or bc1
	if len(address) < 26 || len(address) > 62 {
		return false
	}

	// Legacy P2PKH
	if address[0] == '1' {
		return len(address) >= 26 && len(address) <= 35
	}

	// P2SH
	if address[0] == '3' {
		return len(address) >= 26 && len(address) <= 35
	}

	// Bech32
	if len(address) >= 4 && address[:4] == "bc1q" {
		return len(address) == 42 || len(address) == 62
	}

	// Bech32m (Taproot)
	if len(address) >= 4 && address[:4] == "bc1p" {
		return len(address) == 62
	}

	return false
}
