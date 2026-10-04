package mergemining

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// AuxWork is the getauxblock work-request result from the 1175 node.
type AuxWork struct {
	Hash              string `json:"hash"`    // child block hash (big-endian display hex)
	ChainID           int    `json:"chainid"` // 1175
	PreviousBlockHash string `json:"previousblockhash"`
	CoinbaseValue     int64  `json:"coinbasevalue"`
	Bits              string `json:"bits"` // compact target (hex)
	Height            int64  `json:"height"`
	Target            string `json:"target"` // uint256 target (big-endian hex)
}

// Client is a minimal JSON-RPC client for the aux (1175) node's AuxPoW RPCs.
// It mirrors Forge Pool's existing raw JSON-RPC style (jsonrpc 1.0).
type Client struct {
	URL  string // e.g. http://127.0.0.1:25361
	User string
	Pass string
	HTTP *http.Client
}

func NewClient(url, user, pass string) *Client {
	return &Client{URL: url, User: user, Pass: pass, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) call(method string, params ...any) (json.RawMessage, error) {
	if params == nil {
		params = []any{} // "params": [], not null
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "1.0", "id": "forge-mm", "method": method, "params": params,
	})
	req, err := http.NewRequest("POST", c.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.User, c.Pass)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r rpcResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("%s decode: %w", method, err)
	}
	if r.Error != nil {
		return nil, &RPCError{Method: method, Code: r.Error.Code, Message: r.Error.Message}
	}
	return r.Result, nil
}

// RPCError is an error the 1175 node answered with, as opposed to no answer at all.
type RPCError struct {
	Method  string
	Code    int
	Message string
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("%s rpc error %d: %s", e.Method, e.Code, e.Message)
}

// GetBestBlockHash asks the node for its chain tip: a cheap call, made every second.
func (c *Client) GetBestBlockHash() (string, error) {
	res, err := c.call("getbestblockhash")
	if err != nil {
		return "", err
	}
	var tip string
	if err := json.Unmarshal(res, &tip); err != nil {
		return "", fmt.Errorf("getbestblockhash unmarshal: %w", err)
	}
	return tip, nil
}

// BlockConfirmations asks the node for a block's confirmations on its active chain: -1 for a
// block it has off that chain. found is false when the node does not know the block.
func (c *Client) BlockConfirmations(hash string) (confirmations int64, found bool, err error) {
	res, err := c.call("getblock", hash)
	if err != nil {
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) && rpcErr.Code == rpcInvalidAddressOrKey {
			return 0, false, nil
		}
		return 0, false, err
	}
	var b struct {
		Confirmations *int64 `json:"confirmations"`
	}
	if err := json.Unmarshal(res, &b); err != nil || b.Confirmations == nil {
		return 0, false, fmt.Errorf("getblock: no confirmations in %.80s", res)
	}
	return *b.Confirmations, true, nil
}

// The node's RPC error codes this package acts on (src/rpc/protocol.h).
const (
	rpcInvalidAddressOrKey = -5  // getblock: "Block not found"
	RPCInvalidParameter    = -8  // submitauxblock: "Block hash not found in pending work"
	RPCInWarmup            = -28 // the node is starting
)

// GetAuxBlock requests aux work paying the coinbase reward to payoutAddress.
// Returns an error (e.g. "AuxPoW not yet active") until the aux chain reaches
// its activation height.
func (c *Client) GetAuxBlock(payoutAddress string) (*AuxWork, error) {
	res, err := c.call("getauxblock", payoutAddress)
	if err != nil {
		return nil, err
	}
	var w AuxWork
	if err := json.Unmarshal(res, &w); err != nil {
		return nil, fmt.Errorf("getauxblock unmarshal: %w", err)
	}
	return &w, nil
}

// SubmitAuxBlock submits a solved AuxPoW proof for childHash. Returns whether
// the aux node accepted the block.
func (c *Client) SubmitAuxBlock(childHash, auxpowHex string) (bool, error) {
	res, err := c.call("submitauxblock", childHash, auxpowHex)
	if err != nil {
		return false, err
	}
	var ok bool
	if err := json.Unmarshal(res, &ok); err != nil {
		return false, fmt.Errorf("submitauxblock unmarshal: %w", err)
	}
	return ok, nil
}
