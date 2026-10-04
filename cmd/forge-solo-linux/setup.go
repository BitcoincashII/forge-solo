package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// The public ports are fixed, as on Umbrel and Windows: miners, marketplaces and the BCH2
// network expect them, and the dashboard's own copy names them.
const (
	stratumPort = 3333 // miners
	rentalPort  = 3335 // NiceHash / MiningRigRentals: one connection per order, high difficulty floor
	p2pPort     = 8339 // BCH2 peers
	onionPort   = 8340 // the node's Tor onion listener, 127.0.0.1 only; see nodeConf
	defaultWeb  = "127.0.0.1:3080"
)

// ports are the loopback-only service ports, picked at every start so they cannot collide with
// anything else on the machine. Every config and environment below is written from one set, so a
// run is consistent with itself.
type ports struct {
	RPC   int // node RPC
	ZMQ   int // node block notifications
	API   int // dashboard API
	Stats int // stratum internal stats
}

// pickPort returns the first free loopback TCP port at or above start. Scanning a fixed range
// below the kernel's ephemeral range (32768 and up on Linux) keeps the port from being handed to
// an outgoing connection between this check and the service binding it.
func pickPort(start int) (int, error) {
	for p := start; p < start+300; p++ {
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			_ = l.Close()
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free port on 127.0.0.1 between %d and %d", start, start+299)
}

// pickPorts gives each service its own 300-port window so no two can pick the same port.
func pickPorts() (ports, error) {
	var p ports
	for _, f := range []struct {
		dst   *int
		start int
	}{{&p.RPC, 30300}, {&p.ZMQ, 30600}, {&p.Stats, 31500}, {&p.API, 31800}} {
		n, err := pickPort(f.start)
		if err != nil {
			return ports{}, err
		}
		*f.dst = n
	}
	return p, nil
}

// secrets are made once per install and kept in dataDir/secrets.env (0600).
type secrets struct {
	RPCPassword       string // the node's RPC
	Token             string // INTERNAL_API_TOKEN: the stratum's internal stats endpoint
	DashboardPassword string // asked for when the dashboard listens beyond 127.0.0.1
}

var secretKeys = []string{"RPC_PASSWORD", "INTERNAL_API_TOKEN", "DASHBOARD_PASSWORD"}

func (s *secrets) field(k string) *string {
	switch k {
	case "RPC_PASSWORD":
		return &s.RPCPassword
	case "INTERNAL_API_TOKEN":
		return &s.Token
	case "DASHBOARD_PASSWORD":
		return &s.DashboardPassword
	}
	return nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// loadSecrets reads dataDir/secrets.env and makes any secret it lacks, so an install made by an
// earlier release gains new ones and keeps the rest. The caller holds the data-directory lock.
func loadSecrets(dataDir string) (secrets, error) {
	p := filepath.Join(dataDir, "secrets.env")
	var s secrets
	b, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return secrets{}, err
	}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if f := (&s).field(k); ok && f != nil {
			*f = strings.TrimSpace(v)
		}
	}
	missing := false
	for _, k := range secretKeys {
		if f := s.field(k); *f == "" {
			if *f, err = randomHex(24); err != nil {
				return secrets{}, err
			}
			missing = true
		}
	}
	if !missing {
		return s, nil
	}
	var out strings.Builder
	for _, k := range secretKeys {
		fmt.Fprintf(&out, "%s=%s\n", k, *s.field(k))
	}
	return s, writeFileAtomic(p, []byte(out.String()), 0o600)
}

// writeFileAtomic replaces path with data, so a crash never leaves it half written.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// nodeConf is the BCH2 node's config, written at every start so an update applies.
//
// The binds are explicit. The node's mainnet Tor onion target port is 8339, the same as its P2P
// port (upstream uses the P2P port + 1). With no bind= it binds that onion listener on
// 127.0.0.1:8339 first, its own 0.0.0.0:8339 then fails, and no IPv4 peer can ever connect in.
// The onion listener moves one port up. [::] fails harmlessly where IPv6 is off.
//
// Not pruned: the whole chain is about 90 MB, and a pruned node announces NODE_NETWORK_LIMITED,
// which the DNS seeders never list. UPnP and NAT-PMP stay off: opening a port on the router is a
// change to someone's network, not ours to make.
func nodeConf(p ports, s secrets) string {
	return fmt.Sprintf(`# Written by Forge Solo at every start: edits here are overwritten.
server=1
listen=1
port=%[1]d
bind=0.0.0.0:%[1]d
bind=[::]:%[1]d
bind=127.0.0.1:%[2]d=onion
discover=1
dnsseed=1
upnp=0
natpmp=0
rpcbind=127.0.0.1
rpcallowip=127.0.0.1
rpcport=%[3]d
rpcuser=forge
rpcpassword=%[4]s
zmqpubhashblock=tcp://127.0.0.1:%[5]d
printtoconsole=0
dbcache=100
maxmempool=50
maxsigcachesize=4
`, p2pPort, onionPort, p.RPC, s.RPCPassword, p.ZMQ)
}

// stratumConf mirrors docker/stratum/config.template.yaml, the tested config the Umbrel app
// ships, with this run's ports. Keys the stratum does not read are silently ignored, so keep the
// two in step. There is no 1175 node here, so merge-mining is off.
func stratumConf(p ports) string {
	return fmt.Sprintf(`# Written by Forge Solo at every start: edits here are overwritten. The payout address,
# coinbase tag and payout mode are set on the dashboard's Settings page.
pool:
  name: "Forge Solo"
  coin: "Bitcoin Cash II"
  coin_symbol: "BCH2"
  address: ""
  block_reward: 50.0
  payout_scheme: "solo"
  coinbase_tag: ""
stratum:
  host: "0.0.0.0"
  port: %d
  max_connections: 256
  max_connections_per_ip: 128
  max_shares_per_second: 100
  extranonce1_size: 4
  extranonce2_size: 8
  vardiff:
    enabled: true
    min_diff: 1024
    max_diff: 1000000000000
    target_time: 5
    retarget_time: 10
    variance_percent: 25
# NiceHash / MiningRigRentals put a whole order behind one connection: a separate port with a
# difficulty floor that suits an aggregated order.
stratum_rental:
  enabled: true
  host: "0.0.0.0"
  port: %d
  max_connections: 64
  max_connections_per_ip: 32
  max_shares_per_second: 100
  extranonce1_size: 4
  extranonce2_size: 8
  vardiff:
    enabled: true
    min_diff: 500000
    max_diff: 1000000000000
    target_time: 5
    retarget_time: 10
node:
  host: "127.0.0.1"
  port: %d
  use_ssl: false
  zmq_endpoint: "tcp://127.0.0.1:%d"
mergemining:
  enabled: false
logging:
  level: "info"
  format: "json"
`, stratumPort, rentalPort, p.RPC, p.ZMQ)
}

// writeConfigs writes the node's and the stratum's configs into dataDir.
func writeConfigs(dataDir string, p ports, s secrets) error {
	if err := os.MkdirAll(filepath.Join(dataDir, "bch2"), 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dataDir, "bch2", "bch2.conf"), []byte(nodeConf(p, s)), 0o600); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dataDir, "config.yaml"), []byte(stratumConf(p)), 0o600)
}

// currentUser is the name of the account this runs as ("" if unknown); a test stands in another.
var currentUser = func() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

// defaultDataDir is /var/lib/forge-solo for root and for the service's user, else the user's XDG
// data directory. The service's user has /var/lib/forge-solo as its home, so its XDG directory
// was /var/lib/forge-solo/.local/share/forge-solo, where nothing is.
func defaultDataDir() string {
	if os.Geteuid() == 0 || currentUser() == serviceUser {
		return serviceData
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "forge-solo")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "forge-solo")
	}
	return "forge-solo-data"
}
