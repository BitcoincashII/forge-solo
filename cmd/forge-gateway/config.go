package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
)

// Config is forge-gateway.json. Everything but the payout address and the node login has a
// default; see forge-gateway.example.json for every key with its default.
type Config struct {
	Node     NodeConfig    `json:"node"`
	Mining   MiningConfig  `json:"mining"`
	Stratum  StratumConfig `json:"stratum"`
	Pool     PoolConfig    `json:"pool"`
	Status   StatusConfig  `json:"status"`
	LogFile  string        `json:"log_file"`  // empty: log to the console
	LogLevel string        `json:"log_level"` // debug, info, warn, error

	dir string // the config file's directory: relative paths in it resolve against this
}

// NodeConfig is how the gateway reaches the miner's own BCH2 node.
type NodeConfig struct {
	RPCURL        string `json:"rpc_url"`
	RPCUser       string `json:"rpc_user"`
	RPCPassword   string `json:"rpc_password"`
	RPCCookieFile string `json:"rpc_cookie_file"` // used when rpc_user is empty
}

// MiningConfig says who is paid.
type MiningConfig struct {
	// PayoutAddress is credited for every share whose miner does not log in with an address of
	// its own, and paid the whole block while mining solo because the pool cannot be reached.
	PayoutAddress string `json:"payout_address"`
	CoinbaseTag   string `json:"coinbase_tag"`
	// PoolOnly turns miners away while the pool cannot be reached, instead of mining solo, so
	// they fail over to their backup pool. Set it on a gateway that serves other people: solo
	// blocks pay PayoutAddress, not the miners' own addresses.
	PoolOnly bool `json:"pool_only"`
}

// StratumConfig is the stratum port the miners connect to.
type StratumConfig struct {
	Listen              string  `json:"listen"`
	MinDifficulty       float64 `json:"min_difficulty"`
	MaxDifficulty       float64 `json:"max_difficulty"`
	TargetShareSeconds  int     `json:"target_share_seconds"`
	RetargetSeconds     int     `json:"retarget_seconds"`
	MaxConnections      int     `json:"max_connections"`
	MaxConnectionsPerIP int     `json:"max_connections_per_ip"`
}

// PoolConfig is Forge Pool's DATUM intake, and this gateway's identity there.
type PoolConfig struct {
	URL     string `json:"url"`
	KeyFile string `json:"key_file"`
}

// StatusConfig is the local status page; "off" disables it.
type StatusConfig struct {
	Listen string `json:"listen"`
}

const (
	defaultRPCURL        = "http://127.0.0.1:8342"
	defaultCoinbaseTag   = "Forge Gateway"
	defaultStratumListen = "0.0.0.0:3333"
	defaultStatusListen  = "127.0.0.1:7152"
	defaultKeyFile       = "forge-gateway.key"
	maxCoinbaseTag       = 32 // the DATUM coinbase layout's limit
)

// loadConfig reads, defaults and checks the config at path. Unknown keys are an error: a
// misspelt key would otherwise be silently ignored and its default used.
func loadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c.dir = filepath.Dir(abs)
	c.defaults()
	if err := c.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) defaults() {
	if c.Node.RPCURL == "" {
		c.Node.RPCURL = defaultRPCURL
	}
	if c.Mining.CoinbaseTag == "" {
		c.Mining.CoinbaseTag = defaultCoinbaseTag
	}
	s := &c.Stratum
	if s.Listen == "" {
		s.Listen = defaultStratumListen
	}
	if s.MinDifficulty == 0 {
		s.MinDifficulty = 1024 // small ASICs (a Bitaxe) still submit steadily
	}
	if s.MaxDifficulty == 0 {
		s.MaxDifficulty = 1e12
	}
	if s.TargetShareSeconds == 0 {
		s.TargetShareSeconds = 5
	}
	if s.RetargetSeconds == 0 {
		s.RetargetSeconds = 10
	}
	if s.MaxConnections == 0 {
		s.MaxConnections = 256
	}
	if s.MaxConnectionsPerIP == 0 {
		s.MaxConnectionsPerIP = s.MaxConnections / 2
		if s.MaxConnectionsPerIP == 0 {
			s.MaxConnectionsPerIP = 1
		}
	}
	if c.Pool.URL == "" {
		c.Pool.URL = tidesgw.DefaultPoolURL
	}
	if c.Pool.KeyFile == "" {
		c.Pool.KeyFile = defaultKeyFile
	}
	if c.Status.Listen == "" {
		c.Status.Listen = defaultStatusListen
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	c.Pool.KeyFile = c.resolve(c.Pool.KeyFile)
	c.Node.RPCCookieFile = c.resolve(c.Node.RPCCookieFile)
	c.LogFile = c.resolve(c.LogFile)
}

// resolve makes a relative path relative to the config file, not to wherever the program was
// started from -- a service starts in a system directory.
func (c *Config) resolve(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.dir, p)
}

func (c *Config) check() error {
	if c.Mining.PayoutAddress == "" {
		return errors.New("mining.payout_address is required: the BCH2 address your shares are credited to")
	}
	canon, err := tidesgw.CanonicalAddress(c.Mining.PayoutAddress)
	if err != nil {
		return fmt.Errorf("mining.payout_address %q is not a BCH2 address: %w", c.Mining.PayoutAddress, err)
	}
	c.Mining.PayoutAddress = canon
	if len(c.Mining.CoinbaseTag) > maxCoinbaseTag {
		return fmt.Errorf("mining.coinbase_tag is %d bytes; at most %d fit", len(c.Mining.CoinbaseTag), maxCoinbaseTag)
	}
	for _, r := range c.Mining.CoinbaseTag {
		if r < 0x20 || r > 0x7e {
			return errors.New("mining.coinbase_tag must be printable ASCII")
		}
	}
	if c.Node.RPCUser == "" && c.Node.RPCCookieFile == "" {
		return errors.New("set node.rpc_user and node.rpc_password (or node.rpc_cookie_file): the gateway builds work from your own node")
	}
	if c.Node.RPCUser != "" && c.Node.RPCPassword == "" {
		return errors.New("node.rpc_password is empty")
	}
	if !strings.HasPrefix(c.Node.RPCURL, "http://") && !strings.HasPrefix(c.Node.RPCURL, "https://") {
		return fmt.Errorf("node.rpc_url %q must start with http:// or https://", c.Node.RPCURL)
	}
	if _, _, err := hostPort(c.Stratum.Listen); err != nil {
		return fmt.Errorf("stratum.listen: %w", err)
	}
	if c.Status.Listen != "off" {
		if _, _, err := hostPort(c.Status.Listen); err != nil {
			return fmt.Errorf("status.listen: %w", err)
		}
	}
	s := c.Stratum
	if s.MinDifficulty <= 0 || s.MaxDifficulty < s.MinDifficulty {
		return fmt.Errorf("stratum difficulty range %g..%g is not a range", s.MinDifficulty, s.MaxDifficulty)
	}
	if s.TargetShareSeconds < 1 || s.RetargetSeconds < 1 || s.MaxConnections < 1 || s.MaxConnectionsPerIP < 1 {
		return errors.New("stratum target_share_seconds, retarget_seconds, max_connections and max_connections_per_ip must be positive")
	}
	if err := tidesgw.CheckPoolURL(c.Pool.URL); err != nil {
		return fmt.Errorf("pool.url: %w", err)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log_level %q: use debug, info, warn or error", c.LogLevel)
	}
	return nil
}

// hostPort splits "host:port" and checks the port.
func hostPort(addr string) (string, int, error) {
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("%q: bad port", addr)
	}
	return host, port, nil
}

// rpcLogin is the node login: the configured user, or the node's cookie file, read now. A
// node that restarts writes a new cookie, so with a cookie the gateway must restart too;
// rpcauth credentials (rpc_user/rpc_password) do not have that problem.
func (c *Config) rpcLogin() (user, pass string, err error) {
	if c.Node.RPCUser != "" {
		return c.Node.RPCUser, c.Node.RPCPassword, nil
	}
	raw, err := os.ReadFile(c.Node.RPCCookieFile)
	if err != nil {
		return "", "", fmt.Errorf("node.rpc_cookie_file: %w", err)
	}
	user, pass, ok := strings.Cut(strings.TrimSpace(string(raw)), ":")
	if !ok || user == "" || pass == "" {
		return "", "", fmt.Errorf("node.rpc_cookie_file %s is not a user:password cookie", c.Node.RPCCookieFile)
	}
	return user, pass, nil
}
