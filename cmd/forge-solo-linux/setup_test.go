package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func testPorts() ports { return ports{RPC: 30301, ZMQ: 30601, API: 31801, Stats: 31501} }

func TestNodeConfBindsAndIsNotPruned(t *testing.T) {
	c := nodeConf(testPorts(), secrets{RPCPassword: "pw"})
	for _, want := range []string{
		"listen=1\n", "port=8339\n", "bind=0.0.0.0:8339\n", "bind=[::]:8339\n", "bind=127.0.0.1:8340=onion\n",
		"rpcbind=127.0.0.1\n", "rpcallowip=127.0.0.1\n", "rpcport=30301\n", "rpcuser=forge\n", "rpcpassword=pw\n",
		"zmqpubhashblock=tcp://127.0.0.1:30601\n", "zmqpubrawblock=tcp://127.0.0.1:30601\n", "upnp=0\n", "natpmp=0\n",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("bch2.conf lacks %q:\n%s", want, c)
		}
	}
	for _, bad := range []string{"prune", "0.0.0.0/0", "rpcbind=0.0.0.0"} {
		if strings.Contains(c, bad) {
			t.Errorf("bch2.conf has %q:\n%s", bad, c)
		}
	}
}

// The Linux stratum config must stay the config the Umbrel app ships and tests, with only this
// machine's node endpoints filled in and merge-mining off: keys the stratum does not read are
// silently ignored, so a drift here would look configured and do nothing.
func TestStratumConfMatchesShippedTemplate(t *testing.T) {
	var got, want map[string]interface{}
	if err := yaml.Unmarshal([]byte(stratumConf(testPorts())), &got); err != nil {
		t.Fatalf("stratumConf is not YAML: %v", err)
	}
	tmpl, err := os.ReadFile("../../docker/stratum/config.template.yaml")
	if err != nil {
		t.Fatal(err)
	}
	filled := strings.NewReplacer(
		"${POOL_ADDRESS}", "", "${COINBASE_TAG}", "Forge Solo", "${NODE_HOST}", "127.0.0.1", "${NODE_PORT}", "30301",
		"${ZMQ_ENDPOINT}", "tcp://127.0.0.1:30601", "${PAYOUT_ADDRESS_1175}", "", "${AUX1175_HOST}", "x",
		"${AUX1175_PORT}", "1", "${AUX1175_USER}", "x", "${AUX1175_PASSWORD}", "x").Replace(string(tmpl))
	if err := yaml.Unmarshal([]byte(filled), &want); err != nil {
		t.Fatalf("template: %v", err)
	}
	want["mergemining"] = map[string]interface{}{"enabled": false}
	if !reflect.DeepEqual(got, want) {
		for k := range want {
			if !reflect.DeepEqual(got[k], want[k]) {
				t.Errorf("%s:\n got  %v\n want %v", k, got[k], want[k])
			}
		}
		for k := range got {
			if _, ok := want[k]; !ok {
				t.Errorf("extra key %s", k)
			}
		}
	}
}

func TestSecretsAreMadeOnceAndKept(t *testing.T) {
	dir := t.TempDir()
	a, err := loadSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.RPCPassword == "" || a.Token == "" || a.DashboardPassword == "" || a.RPCPassword == a.Token {
		t.Fatalf("secrets not made: %+v", a)
	}
	st, err := os.Stat(filepath.Join(dir, "secrets.env"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("secrets.env mode %v, want 0600", st.Mode().Perm())
	}
	b, err := loadSecrets(dir)
	if err != nil || b != a {
		t.Fatalf("second load %+v (%v), want %+v", b, err, a)
	}

	// An install from an earlier release lacks a newer secret: it is added, the rest kept.
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("RPC_PASSWORD=old1\nINTERNAL_API_TOKEN=old2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.RPCPassword != "old1" || c.Token != "old2" || c.DashboardPassword == "" {
		t.Fatalf("upgrade: %+v", c)
	}
	d, _ := loadSecrets(dir)
	if d != c {
		t.Fatalf("the added secret was not saved: %+v then %+v", c, d)
	}
}

func TestWriteConfigs(t *testing.T) {
	dir := t.TempDir()
	if err := writeConfigs(dir, testPorts(), secrets{RPCPassword: "pw"}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"bch2/bch2.conf", "config.yaml"} {
		st, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", f, st.Mode().Perm())
		}
	}
}

// writeFileAtomic sets the mode it is given: the unit file is 0644, secrets and configs 0600.
func TestWriteFileAtomicMode(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []os.FileMode{0o644, 0o600} {
		p := filepath.Join(dir, "f")
		if err := writeFileAtomic(p, []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm() != mode {
			t.Fatalf("mode %v (%v), want %v", st.Mode().Perm(), err, mode)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".f.*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}

	// A write that fails leaves the old file and no temporary one: here the target is a
	// non-empty directory, so the final rename fails.
	d := filepath.Join(dir, "d")
	if err := os.MkdirAll(filepath.Join(d, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(d, []byte("x"), 0o600); err == nil {
		t.Fatal("no error writing over a directory")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".d.*")); len(left) != 0 {
		t.Fatalf("a failed write left %v", left)
	}
}

func TestPickPortsAreDistinctAndFree(t *testing.T) {
	p, err := pickPorts()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for _, n := range []int{p.RPC, p.ZMQ, p.API, p.Stats} {
		if n == 0 || seen[n] {
			t.Fatalf("ports %+v", p)
		}
		seen[n] = true
	}
}
