// Package gateway is a DATUM gateway's side of the pool API: what Forge Solo in pool mode runs. It
// builds a job from the gateway node's own template so that its coinbase pays the pool's TIDES
// split, registers it, and sends the shares its miners find.
package gateway

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
	"strings"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/datum/wire"
)

// Client talks to one pool's DATUM API.
type Client struct {
	Base string // e.g. https://pool.bch2.org
	Key  ed25519.PrivateKey
	HTTP *http.Client
	Now  func() time.Time
}

// New builds a client for the pool at base.
func New(base string, key ed25519.PrivateKey) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Key: key, HTTP: &http.Client{Timeout: 20 * time.Second}, Now: time.Now}
}

// LoadOrCreateKey reads the gateway's key from path, making one (0600) on first run. The key is
// the gateway's identity at the pool; losing it only means starting as a new gateway.
func LoadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		seed, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("%s does not hold a gateway key", path)
		}
		return ed25519.NewKeyFromSeed(seed), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		return nil, err
	}
	return priv, nil
}

// ID is the gateway's public key, as the pool knows it.
func (c *Client) ID() string { return hex.EncodeToString(c.Key.Public().(ed25519.PublicKey)) }

// Snapshot fetches the pool's newest TIDES snapshot.
func (c *Client) Snapshot() (*wire.Snapshot, error) {
	resp, err := c.HTTP.Get(c.Base + "/datum/v1/tides")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("tides: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var s wire.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("tides: %w", err)
	}
	return &s, nil
}

func (c *Client) post(path string, in, out interface{}) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", c.Base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	wire.Sign(req, c.Key, body, c.Now())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil || resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// TemplateTx is one transaction of the gateway node's template.
type TemplateTx struct {
	TxID string
	Data string
}

// Template is the gateway node's block template: the fields of getblocktemplate a DATUM job needs.
type Template struct {
	Height        int64
	PrevHash      string
	Version       uint32
	Bits          string
	CurTime       uint32
	CoinbaseValue int64 // subsidy + the fees of Txs
	Txs           []TemplateTx
}

// BuildJob turns the gateway node's template into a registration whose coinbase pays snap's split
// (or everything to finder while the pool's DATUM share log is empty).
func BuildJob(snap *wire.Snapshot, t *Template, finder string, tag []byte) (*wire.JobRequest, error) {
	outs, err := wire.Payouts(snap, t.CoinbaseValue, finder)
	if err != nil {
		return nil, err
	}
	c1, c2, err := wire.BuildCoinbase(t.Height, tag, outs)
	if err != nil {
		return nil, err
	}
	req := &wire.JobRequest{Snapshot: snap.Version, Height: t.Height, PrevHash: t.PrevHash, Version: t.Version, Bits: t.Bits,
		Time: t.CurTime, Coinb1: c1, Coinb2: c2, Finder: finder}
	for _, tx := range t.Txs {
		req.TxIDs = append(req.TxIDs, tx.TxID)
	}
	return req, nil
}

// Register registers a job, sending transaction data if the pool asks for it and retrying while the
// pool catches up, until the pool accepts or refuses it for good (or tries runs out).
func (c *Client) Register(req *wire.JobRequest, t *Template, tries int, wait time.Duration) (*wire.JobResponse, error) {
	var resp wire.JobResponse
	for i := 0; i < tries; i++ {
		resp = wire.JobResponse{}
		if err := c.post("/datum/v1/jobs", req, &resp); err != nil {
			return nil, err
		}
		if resp.JobID != "" {
			return &resp, nil
		}
		if strings.Contains(resp.Error, "send its data") && req.TxData == nil {
			req.TxData = map[string]string{}
			for _, tx := range t.Txs {
				req.TxData[tx.TxID] = tx.Data
			}
			continue
		}
		if !resp.Retry {
			return &resp, fmt.Errorf("the pool refused the job: %s", resp.Error)
		}
		time.Sleep(wait)
	}
	return &resp, fmt.Errorf("the pool did not take the job after %d tries: %s", tries, resp.Error)
}

// SubmitShares sends a batch of shares.
func (c *Client) SubmitShares(shares []wire.Share) (*wire.ShareBatchResponse, error) {
	var resp wire.ShareBatchResponse
	if err := c.post("/datum/v1/shares", wire.ShareBatch{Shares: shares}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
