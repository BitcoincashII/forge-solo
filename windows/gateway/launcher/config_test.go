package main

import (
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The config a fresh install starts with, as Forge Gateway's spec gives it: the gateway reads it as
// not set up, and its status page then asks for the node and the payout address.
const wantFreshConfig = "{\n" +
	"  \"node\": {\n" +
	"    \"rpc_url\": \"http://127.0.0.1:8342\",\n" +
	"    \"rpc_user\": \"\",\n" +
	"    \"rpc_password\": \"\",\n" +
	"    \"rpc_cookie_file\": \"\"\n" +
	"  },\n" +
	"  \"mining\": {\n" +
	"    \"payout_address\": \"\",\n" +
	"    \"coinbase_tag\": \"Forge Gateway\",\n" +
	"    \"pool_only\": false\n" +
	"  },\n" +
	"  \"stratum\": {\n" +
	"    \"listen\": \"0.0.0.0:3333\"\n" +
	"  },\n" +
	"  \"status\": {\n" +
	"    \"listen\": \"127.0.0.1:3090\"\n" +
	"  },\n" +
	"  \"log_level\": \"info\"\n" +
	"}\n"

// A fresh install gets a config to set up in Settings; a config that is there is the user's, and
// is never written, whatever it holds.
func TestTheFreshConfigIsWrittenOnce(t *testing.T) {
	withSecretsDir(t)
	if err := ensureConfig(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dpath(configName))
	if string(b) != wantFreshConfig || freshConfig != wantFreshConfig {
		t.Fatalf("GWL-FRESH-CONFIG: a fresh install's forge-gateway.json is not the fresh config:\n%s", b)
	}
	if !strings.Contains(launcherLog(), "wrote a new forge-gateway.json: set the node and payout address in the status page's Settings") {
		t.Errorf("GWL-FRESH-CONFIG-LOGGED: launcher.log does not say a new config was written:\n%s", launcherLog())
	}
	if _, err := os.Stat(dpath(configName + ".tmp")); !os.IsNotExist(err) {
		t.Error("GWL-FRESH-CONFIG: the temporary copy was left behind")
	}
	for name, kept := range map[string]string{
		"valid":   `{"node":{"rpc_url":"http://10.0.0.5:8342","rpc_user":"me","rpc_password":"pw"},"mining":{"payout_address":"bitcoincashii:q"}}`,
		"invalid": `{"node": {`,
		"empty":   "",
	} {
		writeFile(t, dpath(configName), kept)
		if err := ensureConfig(); err != nil {
			t.Fatalf("GWL-FRESH-CONFIG-KEPT: %s: %v", name, err)
		}
		if b, _ := os.ReadFile(dpath(configName)); string(b) != kept {
			t.Errorf("GWL-FRESH-CONFIG-KEPT: a %s forge-gateway.json was written over: %q", name, b)
		}
	}
}

// The config cannot be written: Forge Gateway cannot start, and the tray says why in its room.
func TestAConfigThatCannotBeWrittenStopsTheStart(t *testing.T) {
	withSecretsDir(t)
	dataDir = dpath("no-such-folder")
	err := ensureConfig()
	if err == nil || !strings.HasPrefix(err.Error(), "forge-gateway.json cannot be written (") || startWhy(err) != "its config file cannot be written" {
		t.Fatalf("GWL-FRESH-CONFIG-FAILS: a config that cannot be written gave %v", err)
	}
}

// The ports are the config's: the miners' and the status page's, which the tray checks before the
// start and opens. Keys left out are the usual ports; a status page on every address answers at
// 127.0.0.1, one on [::1] there.
func TestThePortsAreTheConfigs(t *testing.T) {
	withSecretsDir(t)
	t.Cleanup(func() { stratumPort, statusHost, statusPort = "3333", "127.0.0.1", "3090" })
	for _, tc := range []struct{ config, stratum, url string }{
		{`{"stratum":{"listen":"0.0.0.0:4444"},"status":{"listen":"127.0.0.1:4090"}}`, "4444", "http://127.0.0.1:4090/"},
		{`{}`, "3333", "http://127.0.0.1:3090/"},
		{wantFreshConfig, "3333", "http://127.0.0.1:3090/"},
		{`{"status":{"listen":"0.0.0.0:3091"}}`, "3333", "http://127.0.0.1:3091/"},
		{`{"status":{"listen":"localhost:3092"}}`, "3333", "http://127.0.0.1:3092/"},
		{`{"status":{"listen":"[::1]:3093"}}`, "3333", "http://[::1]:3093/"},
		{`{"stratum":{"listen":":5555"},"status":{"listen":":3094"}}`, "5555", "http://127.0.0.1:3094/"},
	} {
		writeFile(t, dpath(configName), tc.config)
		readConfigPorts()
		if stratumPort != tc.stratum || statusURL() != tc.url || settingsURL() != tc.url+"#settings" {
			t.Errorf("GWL-CONFIG-PORTS: %s gave the miner port %s and %s, %s; want %s and %s", tc.config, stratumPort, statusURL(), settingsURL(), tc.stratum, tc.url)
		}
	}
	if b, _ := os.ReadFile(dpath("launcher.log")); len(b) != 0 {
		t.Errorf("GWL-CONFIG-PORTS: configs that can be read were logged as not:\n%s", b)
	}

	// A file that cannot be read for its ports: the usual ones, logged.
	for _, broken := range []string{`{"stratum":`, `{"status":{"listen":"off"}}`, `{"stratum":{"listen":"0.0.0.0"}}`, `{"status":{"listen":"127.0.0.1:99999"}}`} {
		_ = os.Remove(dpath("launcher.log"))
		writeFile(t, dpath(configName), `{"stratum":{"listen":"0.0.0.0:4444"},"status":{"listen":"127.0.0.1:4090"}}`)
		readConfigPorts()
		writeFile(t, dpath(configName), broken)
		readConfigPorts()
		if stratumPort != "3333" || statusURL() != "http://127.0.0.1:3090/" {
			t.Errorf("GWL-CONFIG-PORTS-BROKEN: %s gave %s and %s, not the usual ports", broken, stratumPort, statusURL())
		}
		if !strings.Contains(launcherLog(), "forge-gateway.json cannot be read for its ports (") || !strings.Contains(launcherLog(), "): checking 3333 and 3090\n") {
			t.Errorf("GWL-CONFIG-PORTS-LOGGED: %s is not logged as a config that cannot be read for its ports:\n%s", broken, launcherLog())
		}
	}
	_ = os.Remove(dpath(configName))
	readConfigPorts()
	if stratumPort != "3333" || statusURL() != "http://127.0.0.1:3090/" {
		t.Errorf("GWL-CONFIG-PORTS-NONE: with no config the ports are %s and %s", stratumPort, statusURL())
	}
}

// The ports checked before the start are the config's: another program on the config's miner port
// or status port stops it, one on 3333 or 3090 does not.
func TestThePortsCheckedAreTheConfigs(t *testing.T) {
	gatewayWorld(t, "eof")
	for _, which := range []*string{&stratumPort, &statusPort} {
		n, _ := strconv.Atoi(*which)
		tcpListeners = func() []tcpListener {
			return []tcpListener{{net.IPv4zero, 3333, 7}, {net.IPv4zero, 3090, 7}, {net.IPv4(127, 0, 0, 1), n, 4242}}
		}
		err := checkPorts()
		if err == nil || err.port != *which {
			t.Errorf("GWL-CONFIG-PORTS-CHECKED: with port %s held the check gave %v", *which, err)
		}
	}
	// The world's ports are ones the system handed out, never 3333 or 3090.
	tcpListeners = func() []tcpListener { return []tcpListener{{net.IPv4zero, 3333, 7}, {net.IPv4zero, 3090, 7}} }
	if err := checkPorts(); err != nil {
		t.Errorf("GWL-CONFIG-PORTS-CHECKED: ports the config does not name stopped the start: %v", err)
	}
}
