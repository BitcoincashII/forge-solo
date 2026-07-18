package rental

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
)

// RemoteSigner handles transaction signing via remote service
type RemoteSigner struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
}

// RemoteSignRequest represents a signing request to the remote service
type RemoteSignRequest struct {
	RequestID   string            `json:"request_id"`
	Destination string            `json:"destination"`
	AmountSat   int64             `json:"amount_sat"`
	UTXOs       []RemoteUTXOInput `json:"utxos"`
	ChangeChain uint32            `json:"change_chain"`
	ChangeIndex uint32            `json:"change_index"`
	FeeRateSat  int64             `json:"fee_rate_sat_vb"`
}

// RemoteUTXOInput represents a UTXO input for remote signing
type RemoteUTXOInput struct {
	TxID            string `json:"txid"`
	Vout            uint32 `json:"vout"`
	AmountSat       int64  `json:"amount_sat"`
	DerivationChain uint32 `json:"derivation_chain"`
	DerivationIndex uint32 `json:"derivation_index"`
}

// RemoteSignResponse represents the signing response
type RemoteSignResponse struct {
	RequestID   string `json:"request_id"`
	SignedTxHex string `json:"signed_tx_hex"`
	TxID        string `json:"txid"`
	FeeSat      int64  `json:"fee_sat"`
	Error       string `json:"error,omitempty"`
}

// NewRemoteSigner creates a new remote signer client
func NewRemoteSigner(endpoint, apiKey string) *RemoteSigner {
	// The signer presents a long-lived self-signed certificate on a bare IP, so
	// standard chain/hostname verification cannot be used. Instead PIN the exact
	// leaf certificate by SHA-256 fingerprint: any substituted certificate (an
	// on-path party) is rejected, preventing capture or alteration of signing
	// requests. The static X-Signer-Key remains the caller credential.
	const pinnedSignerCertSHA256 = "995c2972264e68b6ccc8b7d755d8e74f2e68a171e5eba1da7648089b16e63b5f"
	tlsConfig := &tls.Config{}
	if strings.HasPrefix(endpoint, "https://") {
		tlsConfig.InsecureSkipVerify = true // chain/hostname skipped; the fingerprint pin below is the real check
		tlsConfig.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("signer presented no certificate")
			}
			sum := sha256.Sum256(rawCerts[0])
			if hex.EncodeToString(sum[:]) != pinnedSignerCertSHA256 {
				return fmt.Errorf("signer certificate fingerprint mismatch (possible interception): %s", hex.EncodeToString(sum[:]))
			}
			return nil
		}
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig}

	return &RemoteSigner{
		endpoint: endpoint,
		apiKey:   apiKey,
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}
}

// SignAndBroadcast signs a transaction via remote service and broadcasts it
func (r *RemoteSigner) SignAndBroadcast(
	destAddress string,
	amountSat int64,
	utxos []UTXO,
	addressToChainIndex map[string]chainIndex,
	changeChain uint32,
	changeIndex uint32,
) (string, error) {

	// Build the signing request
	req := RemoteSignRequest{
		RequestID:   uuid.New().String(),
		Destination: destAddress,
		AmountSat:   amountSat,
		ChangeChain: changeChain,
		ChangeIndex: changeIndex,
		FeeRateSat:  5, // 5 sat/vB
	}

	// Convert UTXOs to remote format with derivation info
	for _, utxo := range utxos {
		// Get the address for this UTXO to find derivation info
		addr, err := getAddressForUTXO(utxo.TxID, utxo.Vout)
		if err != nil {
			return "", fmt.Errorf("failed to get address for UTXO %s:%d: %w", utxo.TxID, utxo.Vout, err)
		}

		ci, ok := addressToChainIndex[addr]
		if !ok {
			return "", fmt.Errorf("address %s not found in wallet", addr)
		}

		req.UTXOs = append(req.UTXOs, RemoteUTXOInput{
			TxID:            utxo.TxID,
			Vout:            utxo.Vout,
			AmountSat:       utxo.Value,
			DerivationChain: ci.Chain,
			DerivationIndex: ci.Index,
		})
	}

	log.Printf("RemoteSigner: Sending sign request %s for %d sats to %s", req.RequestID, amountSat, destAddress)

	// Send signing request
	signResp, err := r.sign(&req)
	if err != nil {
		return "", fmt.Errorf("remote signing failed: %w", err)
	}

	log.Printf("RemoteSigner: Received signed tx %s (fee: %d sats)", signResp.TxID, signResp.FeeSat)

	// Broadcast the signed transaction
	txid, err := r.broadcast(signResp.SignedTxHex)
	if err != nil {
		return "", fmt.Errorf("broadcast failed: %w", err)
	}

	log.Printf("RemoteSigner: Broadcast successful, txid: %s", txid)
	return txid, nil
}

// sign sends the signing request to the remote service
func (r *RemoteSigner) sign(req *RemoteSignRequest) (*RemoteSignResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", r.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Signer-Key", r.apiKey)

	resp, err := r.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("signing service error (%d): %s", resp.StatusCode, string(respBody))
	}

	var signResp RemoteSignResponse
	if err := json.Unmarshal(respBody, &signResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if signResp.Error != "" {
		return nil, fmt.Errorf("signing error: %s", signResp.Error)
	}

	return &signResp, nil
}

// broadcast sends the signed transaction to the network via mempool.space
func (r *RemoteSigner) broadcast(txHex string) (string, error) {
	// Validate tx hex before broadcasting
	txBytes, err := hex.DecodeString(txHex)
	if err != nil {
		return "", fmt.Errorf("invalid tx hex: %w", err)
	}

	tx := wire.NewMsgTx(wire.TxVersion)
	if err := tx.Deserialize(bytes.NewReader(txBytes)); err != nil {
		return "", fmt.Errorf("invalid transaction: %w", err)
	}

	// Broadcast via mempool.space
	resp, err := r.httpClient.Post(
		"https://blockstream.info/api/tx",
		"text/plain",
		bytes.NewBufferString(txHex),
	)
	if err != nil {
		return "", fmt.Errorf("broadcast request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("broadcast failed: %s", string(body))
	}

	return string(body), nil
}

// getAddressForUTXO fetches the address that owns a specific UTXO from mempool.space
func getAddressForUTXO(txid string, vout uint32) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	url := fmt.Sprintf("https://blockstream.info/api/tx/%s", txid)

	resp, err := client.Get(url)
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

// HealthCheck verifies the remote signing service is available
func (r *RemoteSigner) HealthCheck() error {
	// Extract base URL from endpoint
	// endpoint format: http://host:port/api/v1/sign
	healthURL := r.endpoint[:len(r.endpoint)-len("/api/v1/sign")] + "/health"

	resp, err := r.httpClient.Get(healthURL)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned status %d", resp.StatusCode)
	}

	return nil
}
