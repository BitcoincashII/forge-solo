package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/cashaddr"
)

// unreachablePool is a Forge Pool nothing answers at: the gateway mines solo meanwhile.
const unreachablePool = "http://127.0.0.1:1"

// fakeHash is the hash of the block at height h on the chain the fake node has.
func fakeHash(h int64) string { return fmt.Sprintf("%064x", 0x2000000+h) }

// fakeNode is a BCH2 node's RPC as far as the gateway uses it.
type fakeNode struct {
	srv *httptest.Server

	mu      sync.Mutex
	user    string // the login it takes
	pass    string
	blocks  int64
	headers int64
	ibd     bool
	calls   map[string]int
}

func newFakeNode(t *testing.T, user, pass string) *fakeNode {
	n := &fakeNode{user: user, pass: pass, blocks: 100, headers: 100, calls: map[string]int{}}
	n.srv = httptest.NewServer(n)
	t.Cleanup(n.srv.Close)
	return n
}

func (n *fakeNode) url() string { return n.srv.URL }

func (n *fakeNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u, p, _ := r.BasicAuth()
	n.mu.Lock()
	ok := u == n.user && p == n.pass
	blocks, headers, ibd := n.blocks, n.headers, n.ibd
	n.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	n.mu.Lock()
	n.calls[req.Method]++
	n.mu.Unlock()
	var result, rpcErr interface{}
	switch req.Method {
	case "getblocktemplate":
		result = map[string]interface{}{"version": 0x20000000, "previousblockhash": fakeHash(blocks), "transactions": []interface{}{},
			"coinbasevalue": int64(50_0000_0000), "bits": "1902c9b9", "height": blocks + 1, "curtime": time.Now().Unix(),
			"target": "0000000000000002c9b900000000000000000000000000000000000000000000"}
	case "getblockchaininfo":
		result = map[string]interface{}{"chain": "main", "blocks": blocks, "headers": headers, "initialblockdownload": ibd}
	case "validateaddress":
		var addr string
		if len(req.Params) > 0 {
			json.Unmarshal(req.Params[0], &addr)
		}
		if a, err := cashaddr.Decode(addr, cashaddr.MainnetPrefix); err == nil {
			result = map[string]interface{}{"isvalid": true, "scriptPubKey": hex.EncodeToString(a.Script())}
		} else {
			result = map[string]interface{}{"isvalid": false}
		}
	case "submitblock":
		result = nil
	case "getblockhash":
		result = fakeHash(blocks)
	default:
		rpcErr = map[string]interface{}{"code": -32601, "message": "Method not found"}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"result": result, "error": rpcErr, "id": "forge-gateway"})
}

// freeAddr is a 127.0.0.1 address with a port the system has just given out and taken back: the
// config takes no port 0.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// nodeConfig is a config for the fake node with a user login, the miners and the status page on
// free 127.0.0.1 ports, and an unreachable pool.
func nodeConfig(t *testing.T, n *fakeNode) string {
	return nodeConfigAt(n, freeAddr(t), freeAddr(t))
}

// nodeConfigAt is nodeConfig with the miners at stratum and the status page at status.
func nodeConfigAt(n *fakeNode, stratum, status string) string {
	return `{"node":{"rpc_url":"` + n.url() + `","rpc_user":"` + n.user + `","rpc_password":"` + n.pass + `"},` +
		`"mining":{"payout_address":"` + testPayout + `"},"stratum":{"listen":"` + stratum + `"},` +
		`"status":{"listen":"` + status + `"},"pool":{"url":"` + unreachablePool + `"}}`
}
