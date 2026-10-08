package main

import (
	"os"
	"strings"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/cashaddr"
)

// testP2SH is a checksum-valid, made-up bitcoincashii:p... address.
var testP2SH = cashaddr.Encode(cashaddr.MainnetPrefix, cashaddr.P2SH, [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20})

// freshConfig is the config the Windows tray app writes on a fresh install.
func freshConfig(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/fresh-config.json")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A P2SH payout address passed the config check in 1.0.0, but the job manager builds a P2PKH
// coinbase only: the gateway started and got no work. It is refused at start now, with a text
// that says which addresses work.
func TestConfigRefusesP2SH(t *testing.T) {
	if !strings.HasPrefix(testP2SH, "bitcoincashii:p") {
		t.Fatalf("GW-CONFIG-P2SH: the test address %s is not a P2SH one", testP2SH)
	}
	p := writeConfig(t, `{"node":{"rpc_user":"u","rpc_password":"p"},"mining":{"payout_address":"`+testP2SH+`"}}`)
	_, err := loadConfig(p)
	want := p + `: mining.payout_address "` + testP2SH + `" is a P2SH address (bitcoincashii:p...): the gateway pays only a bitcoincashii:q... address`
	if err == nil || err.Error() != want {
		t.Fatalf("GW-CONFIG-P2SH: loadConfig said %v\nwant %s", err, want)
	}
	// Read for Settings, it is a mistake to correct there.
	c, err := readConfig(p)
	if err != nil {
		t.Fatalf("GW-CONFIG-P2SH-READ: %v", err)
	}
	if got := c.setupProblem(); !strings.HasPrefix(got, "Settings has a mistake: mining.payout_address") || !strings.Contains(got, "P2SH") {
		t.Fatalf("GW-CONFIG-P2SH-PROBLEM: %q", got)
	}
	// A bitcoincashii:q... address still loads.
	if _, err := loadConfig(writeConfig(t, `{"node":{"rpc_user":"u","rpc_password":"p"},"mining":{"payout_address":"`+testPayout+`"}}`)); err != nil {
		t.Fatalf("GW-CONFIG-P2PKH: %v", err)
	}
}

// readConfig takes a config that Settings has yet to complete; loadConfig, for the console and the
// service, refuses it as 1.0.0 did, with 1.0.0's text and in 1.0.0's order (the payout address
// before the listen addresses).
func TestConfigSplit(t *testing.T) {
	p := writeConfig(t, freshConfig(t))
	c, err := readConfig(p)
	if err != nil {
		t.Fatalf("GW-CONFIG-SPLIT-READ: the fresh config was refused: %v", err)
	}
	if err := c.checkSettings(); err == nil {
		t.Fatal("GW-CONFIG-SPLIT-CHECK: checkSettings took a config with no payout address")
	}
	_, err = loadConfig(p)
	want := p + ": mining.payout_address is required: the BCH2 address your shares are credited to"
	if err == nil || err.Error() != want {
		t.Fatalf("GW-CONFIG-SPLIT-LOAD: %v, want %s", err, want)
	}
	_, err = loadConfig(writeConfig(t, `{"stratum":{"listen":"3333"}}`))
	if err == nil || !strings.Contains(err.Error(), "mining.payout_address is required") {
		t.Fatalf("GW-CONFIG-ORDER: %v", err)
	}
	// What Settings never writes is still checked when the config is read for it.
	if _, err := readConfig(writeConfig(t, `{"stratum":{"listen":"3333"}}`)); err == nil || !strings.Contains(err.Error(), "stratum.listen") {
		t.Fatalf("GW-CONFIG-SPLIT-BASE: %v", err)
	}
	if _, err := readConfig(writeConfig(t, `{"mining":{"payout_adress":"x"}}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("GW-CONFIG-SPLIT-UNKNOWN: %v", err)
	}
}

// Each thing Settings has yet to be given has its own text on the status page and in the tray.
func TestSetupProblemTexts(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"nothing", `{}`, "Set your node and payout address in Settings."},
		{"no payout", `{"node":{"rpc_user":"u","rpc_password":"p"}}`, "Set your payout address in Settings."},
		{"no payout, cookie", `{"node":{"rpc_cookie_file":"/x/.cookie"}}`, "Set your payout address in Settings."},
		{"no login", `{"mining":{"payout_address":"` + testPayout + `"}}`, "Set your node's RPC login in Settings."},
		{"mistake", `{"node":{"rpc_user":"u"},"mining":{"payout_address":"` + testPayout + `"}}`,
			"Settings has a mistake: node.rpc_password is empty. Correct it in Settings."},
		{"set up", `{"node":{"rpc_user":"u","rpc_password":"p"},"mining":{"payout_address":"` + testPayout + `"}}`, ""},
	}
	for _, k := range cases {
		c, err := readConfig(writeConfig(t, k.body))
		if err != nil {
			t.Fatalf("GW-SETUP-TEXT %s: %v", k.name, err)
		}
		if got := c.setupProblem(); got != k.want {
			t.Errorf("GW-SETUP-TEXT %s: %q, want %q", k.name, got, k.want)
		}
	}
}

// The tray app's fresh config is read as not set up, with the text that says what to do.
func TestFreshConfigIsNotSetUp(t *testing.T) {
	c, err := readConfig(writeConfig(t, freshConfig(t)))
	if err != nil {
		t.Fatalf("GW-FRESH-UNCONFIGURED: %v", err)
	}
	if got := c.setupProblem(); got != "Set your node and payout address in Settings." {
		t.Fatalf("GW-FRESH-UNCONFIGURED: %q", got)
	}
	if c.Status.Listen != "127.0.0.1:3090" || c.Stratum.Listen != "0.0.0.0:3333" || c.Mining.CoinbaseTag != "Forge Gateway" {
		t.Fatalf("GW-FRESH-DEFAULTS: %+v", *c)
	}
}
