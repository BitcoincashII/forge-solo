package mergemining

import (
	"bytes"
	"encoding/json"
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
		return nil, fmt.Errorf("%s rpc error %d: %s", method, r.Error.Code, r.Error.Message)
	}
	return r.Result, nil
}

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
