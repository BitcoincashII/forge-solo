package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
)

// node is a JSON-RPC client for the miner's own BCH2 node: the calls the gateway makes itself.
// Block templates come through mining.JobManager, which has its own client.
type node struct {
	url, user, pass string
	http            *http.Client
}

func newNode(url, user, pass string) *node {
	return &node{url: url, user: user, pass: pass, http: &http.Client{Timeout: 30 * time.Second}}
}

// errUnauthorized is the node refusing the login.
var errUnauthorized = errors.New("the node refused the RPC login: check node.rpc_user and node.rpc_password")

// errNotJSONRPC is an answer that is not a node's: something else listens at the RPC address.
var errNotJSONRPC = errors.New("not JSON-RPC")

func (n *node) call(method string, params []interface{}, out interface{}) error {
	if params == nil {
		params = []interface{}{}
	}
	body, err := json.Marshal(map[string]interface{}{"jsonrpc": "1.0", "id": "forge-gateway", "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, n.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(n.user, n.pass)
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errUnauthorized
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("%s: HTTP %d, %w: %.200s", method, resp.StatusCode, errNotJSONRPC, raw)
	}
	if r.Error != nil {
		return fmt.Errorf("%s: %s (code %d)", method, r.Error.Message, r.Error.Code)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// chainInfo is the part of getblockchaininfo the gateway reports.
type chainInfo struct {
	Chain                string  `json:"chain"`
	Blocks               int64   `json:"blocks"`
	Headers              int64   `json:"headers"`
	InitialBlockDownload bool    `json:"initialblockdownload"`
	VerificationProgress float64 `json:"verificationprogress"`
}

func (n *node) chainInfo() (*chainInfo, error) {
	var ci chainInfo
	if err := n.call("getblockchaininfo", nil, &ci); err != nil {
		return nil, err
	}
	return &ci, nil
}

// submitBlock hands the node a block. "" means accepted; anything else is the node's reason.
func (n *node) submitBlock(blockHex string) (string, error) {
	var result interface{}
	if err := n.call("submitblock", []interface{}{blockHex}, &result); err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return fmt.Sprint(result), nil
}

func (n *node) blockHash(height int64) (string, error) {
	var h string
	err := n.call("getblockhash", []interface{}{height}, &h)
	return h, err
}

// loadOrCreateKey is the gateway's Ed25519 identity at the pool, kept in keyFile as a hex seed.
// The pool knows a gateway by this key, so it is created once and kept: a new key on every
// start would make the same gateway look like a new one each time.
func loadOrCreateKey(keyFile string) (ed25519.PrivateKey, bool, error) {
	raw, err := os.ReadFile(keyFile)
	if err == nil {
		key, err := tidesgw.KeyFromSeed(strings.TrimSpace(string(raw)))
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", keyFile, err)
		}
		return key, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		return nil, false, err
	}
	// O_EXCL: two gateways started at once on the same file must not each write a key and
	// then run with different identities.
	f, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return loadOrCreateKey(keyFile)
		}
		return nil, false, err
	}
	if _, err := f.WriteString(hex.EncodeToString(seed) + "\n"); err != nil {
		f.Close()
		return nil, false, err
	}
	if err := f.Close(); err != nil {
		return nil, false, err
	}
	key, err := tidesgw.KeyFromSeed(hex.EncodeToString(seed))
	return key, true, err
}
