package main

import (
	"bufio"
	"encoding/json"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
	"go.uber.org/zap"
)

// The same checksum-valid, obviously-made-up address the stratum tests use.
const testPayout = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "forge-gateway.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A config with only the payout address and the node login gets every other default, and its
// relative paths are relative to the config file, not to where the program was started.
func TestConfigDefaultsAndPaths(t *testing.T) {
	p := writeConfig(t, `{"node":{"rpc_user":"u","rpc_password":"p"},"mining":{"payout_address":"`+testPayout+`"}}`)
	c, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(p)
	checks := []struct {
		name      string
		got, want interface{}
	}{
		{"rpc_url", c.Node.RPCURL, defaultRPCURL},
		{"stratum.listen", c.Stratum.Listen, defaultStratumListen},
		{"min_difficulty", c.Stratum.MinDifficulty, 1024.0},
		{"target_share_seconds", c.Stratum.TargetShareSeconds, 5},
		{"max_connections_per_ip", c.Stratum.MaxConnectionsPerIP, 128},
		{"pool.url", c.Pool.URL, tidesgw.DefaultPoolURL},
		{"key_file", c.Pool.KeyFile, filepath.Join(dir, defaultKeyFile)},
		{"status.listen", c.Status.Listen, defaultStatusListen},
		{"coinbase_tag", c.Mining.CoinbaseTag, defaultCoinbaseTag},
		{"payout_address", c.Mining.PayoutAddress, testPayout},
		{"pool_only", c.Mining.PoolOnly, false},
	}
	for _, k := range checks {
		if k.got != k.want {
			t.Errorf("CONFIG-DEFAULT: %s = %v, want %v", k.name, k.got, k.want)
		}
	}
}

// Each mistake a user can make is caught at start, with a message naming the key.
func TestConfigRejects(t *testing.T) {
	login := `"node":{"rpc_user":"u","rpc_password":"p"}`
	cases := []struct{ name, body, want string }{
		{"no payout", `{` + login + `}`, "mining.payout_address is required"},
		{"not an address", `{` + login + `,"mining":{"payout_address":"bitcoincashii:qqqqqq"}}`, "is not a BCH2 address"},
		{"misspelt key", `{` + login + `,"mining":{"payout_adress":"` + testPayout + `"}}`, "unknown field"},
		{"no login", `{"mining":{"payout_address":"` + testPayout + `"}}`, "rpc_user"},
		{"user without password", `{"node":{"rpc_user":"u"},"mining":{"payout_address":"` + testPayout + `"}}`, "rpc_password is empty"},
		{"long tag", `{` + login + `,"mining":{"payout_address":"` + testPayout + `","coinbase_tag":"` + strings.Repeat("x", 33) + `"}}`, "coinbase_tag is 33 bytes"},
		{"bad listen", `{` + login + `,"mining":{"payout_address":"` + testPayout + `"},"stratum":{"listen":"3333"}}`, "stratum.listen"},
		{"bad pool url", `{` + login + `,"mining":{"payout_address":"` + testPayout + `"},"pool":{"url":"pool.bch2.org"}}`, "pool.url"},
		{"plain-http pool", `{` + login + `,"mining":{"payout_address":"` + testPayout + `"},"pool":{"url":"http://pool.bch2.org"}}`, "must use https://"},
		{"bad level", `{` + login + `,"mining":{"payout_address":"` + testPayout + `"},"log_level":"loud"}`, "log_level"},
		{"bad range", `{` + login + `,"mining":{"payout_address":"` + testPayout + `"},"stratum":{"min_difficulty":10,"max_difficulty":5}}`, "not a range"},
	}
	for _, k := range cases {
		_, err := loadConfig(writeConfig(t, k.body))
		if err == nil || !strings.Contains(err.Error(), k.want) {
			t.Errorf("CONFIG-REJECT %s: got %v, want an error containing %q", k.name, err, k.want)
		}
	}
}

// A cookie file is read as user:password, relative to the config.
func TestCookieLogin(t *testing.T) {
	p := writeConfig(t, `{"node":{"rpc_cookie_file":"node/.cookie"},"mining":{"payout_address":"`+testPayout+`"}}`)
	os.MkdirAll(filepath.Join(filepath.Dir(p), "node"), 0o700)
	os.WriteFile(filepath.Join(filepath.Dir(p), "node", ".cookie"), []byte("__cookie__:s3cret\n"), 0o600)
	c, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	u, pw, err := c.rpcLogin()
	if err != nil || u != "__cookie__" || pw != "s3cret" {
		t.Fatalf("COOKIE: %q %q %v", u, pw, err)
	}
}

// The gateway's identity is made once and kept: the pool knows a gateway by it.
func TestKeyIsCreatedOnceAndKept(t *testing.T) {
	f := filepath.Join(t.TempDir(), "sub", "gw.key")
	k1, created, err := loadOrCreateKey(f)
	if err != nil || !created {
		t.Fatalf("KEY-CREATE: created=%v err=%v", created, err)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(f); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("KEY-MODE: %v %v", st.Mode(), err)
		}
	}
	k2, created, err := loadOrCreateKey(f)
	if err != nil || created || !k1.Equal(k2) {
		t.Fatalf("KEY-KEPT: created=%v err=%v same key=%v", created, err, err == nil && k1.Equal(k2))
	}
	os.WriteFile(f, []byte("not hex\n"), 0o600)
	if _, _, err := loadOrCreateKey(f); err == nil {
		t.Fatal("KEY-CORRUPT: a corrupt key file was accepted")
	}
}

// A share solves a block when its hash is at or under the job's target -- by the exact
// comparison, or by the difficulty the stratum computed. Neither alone must be needed.
func TestSolvesBlock(t *testing.T) {
	diff1 := compactTarget("1d00ffff")
	want, _ := new(big.Int).SetString("00000000ffff0000000000000000000000000000000000000000000000000000", 16)
	if diff1 == nil || diff1.Cmp(want) != 0 {
		t.Fatalf("TARGET-DECODE: %x", diff1)
	}
	job := &mining.Job{NBits: "1d00ffff"}
	under := "00000000fffe0000000000000000000000000000000000000000000000000000"
	over := "00000001000000000000000000000000000000000000000000000000000000ff"
	if !solvesBlock(&stratum.Share{BlockHash: under}, job) {
		t.Error("BLOCK-EXACT: a hash under the target was not a block")
	}
	if !solvesBlock(&stratum.Share{BlockHash: want.Text(16)}, job) {
		t.Error("BLOCK-EQUAL: a hash equal to the target was not a block")
	}
	if solvesBlock(&stratum.Share{BlockHash: over, ActualDiff: 0.9}, job) {
		t.Error("BLOCK-OVER: a hash over the target was a block")
	}
	if !solvesBlock(&stratum.Share{BlockHash: "", ActualDiff: 1.5}, job) {
		t.Error("BLOCK-DIFF: a share proving more than the network difficulty was not a block")
	}
}

// Hashrate is credited difficulty x 2^32 over the window; an idle worker leaves the page.
func TestMinerStatsHashrate(t *testing.T) {
	m := newMinerStats()
	t0 := time.Unix(1_800_000_000, 0)
	for i := 0; i < 10; i++ {
		m.add(&stratum.Share{MinerID: testPayout, WorkerName: "rig1", Difficulty: 1000}, t0.Add(time.Duration(i)*time.Minute))
	}
	v := m.view(t0.Add(9 * time.Minute))
	if len(v) != 1 || v[0].Shares != 10 {
		t.Fatalf("STATS-VIEW: %+v", v)
	}
	// Ten shares of 1000 over the 9 minutes since the first (under the 10-minute window).
	want := 10 * 1000 * 4294967296.0 / (9 * 60)
	if got := v[0].Hashrate; got < want*0.999 || got > want*1.001 {
		t.Fatalf("STATS-HASHRATE: %g, want %g", got, want)
	}
	if v := m.view(t0.Add(2 * time.Hour)); len(v) != 0 {
		t.Fatalf("STATS-FORGET: an hour-idle worker is still listed: %+v", v)
	}
}

// With pool_only, miners are let in only while the pool takes the gateway's work: turned away
// before the first registration, let in after it, and dropped when the pool is lost.
func TestPoolOnlyDoor(t *testing.T) {
	srv := stratum.NewServer(&stratum.ServerConfig{Host: "127.0.0.1", Port: 0, MaxConnections: 10,
		ExtraNonce1Size: 4, ExtraNonce2Size: 8, SoloOnly: true}, zap.NewNop(), nil, nil)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	l := newJobLoop(zap.NewNop(), nil, nil, srv, newJobHistory(), testPayout, true)

	dial := func() net.Conn {
		c, err := net.Dial("tcp", srv.ListenAddr())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	closedSoon := func(c net.Conn) bool {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		r := bufio.NewReader(c)
		for {
			if _, err := r.ReadString('\n'); err != nil {
				return !strings.Contains(err.Error(), "timeout")
			}
		}
	}
	if !closedSoon(dial()) {
		t.Fatal("DOOR-START: a miner was let in before the pool registered any work")
	}
	l.openDoor()
	c := dial()
	c.Write([]byte(`{"id":1,"method":"mining.subscribe","params":["probe/1.0"]}` + "\n"))
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if line, err := bufio.NewReader(c).ReadString('\n'); err != nil || !strings.Contains(line, `"result"`) {
		t.Fatalf("DOOR-OPEN: %q %v", line, err)
	}
	if job := l.solo(nil); job != nil {
		t.Fatal("DOOR-SOLO: pool_only handed out solo work")
	}
	if !closedSoon(c) {
		t.Fatal("DOOR-CLOSE: a connected miner was kept when the pool was lost")
	}
	if !closedSoon(dial()) {
		t.Fatal("DOOR-CLOSED: a new miner was let in while the pool was lost")
	}

	// Without pool_only the door never closes.
	srv2 := stratum.NewServer(&stratum.ServerConfig{Host: "127.0.0.1", Port: 0, MaxConnections: 10,
		ExtraNonce1Size: 4, ExtraNonce2Size: 8, SoloOnly: true}, zap.NewNop(), nil, nil)
	if err := srv2.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv2.Stop()
	newJobLoop(zap.NewNop(), nil, nil, srv2, newJobHistory(), testPayout, false)
	c2, err := net.Dial("tcp", srv2.ListenAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	c2.Write([]byte(`{"id":1,"method":"mining.subscribe","params":["probe/1.0"]}` + "\n"))
	c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	if line, err := bufio.NewReader(c2).ReadString('\n'); err != nil || !strings.Contains(line, `"result"`) {
		t.Fatalf("DOOR-DEFAULT-OPEN: without pool_only a miner was turned away: %q %v", line, err)
	}
}

// forge-gateway.example.json is the documentation of every key and its default: it must load
// once its placeholders are filled in, and every value in it must be the default the gateway
// uses when the key is left out -- so the example can never drift from the code.
func TestExampleConfigIsTheDefaults(t *testing.T) {
	raw, err := os.ReadFile("forge-gateway.example.json")
	if err != nil {
		t.Fatal(err)
	}
	filled := strings.NewReplacer("bitcoincashii:YOUR-BCH2-ADDRESS", testPayout, "CHANGE-ME", "x").Replace(string(raw))
	ex, err := loadConfig(writeConfig(t, filled))
	if err != nil {
		t.Fatalf("EXAMPLE-LOADS: %v", err)
	}
	def, err := loadConfig(writeConfig(t, `{"node":{"rpc_user":"x","rpc_password":"x"},"mining":{"payout_address":"`+testPayout+`"}}`))
	if err != nil {
		t.Fatal(err)
	}
	// Paths resolve against each config's own directory; compare what they name.
	if filepath.Base(ex.Pool.KeyFile) != filepath.Base(def.Pool.KeyFile) {
		t.Errorf("EXAMPLE-DEFAULT: key_file %q vs default %q", ex.Pool.KeyFile, def.Pool.KeyFile)
	}
	ex.Pool.KeyFile, def.Pool.KeyFile, ex.dir, def.dir = "", "", "", ""
	if ex.Node != def.Node || ex.Mining != def.Mining || ex.Stratum != def.Stratum || ex.Pool != def.Pool ||
		ex.Status != def.Status || ex.LogFile != def.LogFile || ex.LogLevel != def.LogLevel {
		t.Errorf("EXAMPLE-DEFAULT: the example is not the defaults\nexample: %+v\ndefault: %+v", *ex, *def)
	}
}

// A TIDES job whose window holds none of the payout address's work pays it nothing if a block is
// found now. The status page must say so; with omitempty the field vanished at 0 and the page
// showed "—", as if it did not know.
func TestStatusJobReportsZeroFinderSats(t *testing.T) {
	b, err := json.Marshal(jobView{ID: "1", Height: 83456, Tides: true, Coinbase: 5000000000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"finder_sats":0`) {
		t.Fatalf("GW-STATUS-FINDER: %s has no finder_sats", b)
	}
}
