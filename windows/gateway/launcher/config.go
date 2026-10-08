package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strconv"
)

// freshConfig is the config a new install starts with: the gateway reads it as not set up, and its
// status page asks for the node and the payout address in Settings, which then writes them here.
// cmd/forge-gateway/testdata/fresh-config.json holds the same bytes.
const freshConfig = `{
  "node": {
    "rpc_url": "http://127.0.0.1:8342",
    "rpc_user": "",
    "rpc_password": "",
    "rpc_cookie_file": ""
  },
  "mining": {
    "payout_address": "",
    "coinbase_tag": "Forge Gateway",
    "pool_only": false
  },
  "stratum": {
    "listen": "0.0.0.0:3333"
  },
  "status": {
    "listen": "127.0.0.1:3090"
  },
  "log_level": "info"
}
`

// A prepError is a start that failed, with what launcher.log says and the shorter why the tray has
// room for.
type prepError struct{ log, tray string }

func (e *prepError) Error() string   { return e.log }
func (e *prepError) trayWhy() string { return e.tray }

// ensureConfig writes the fresh config when there is no forge-gateway.json. A file that is there is
// never written, whatever is in it: a config edited by hand, or one copied from Forge Gateway 1.0.0,
// is the user's, and the gateway says what is wrong with one it cannot use.
func ensureConfig() error {
	path := dpath(configName)
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := writeDurably(path, freshConfig); err != nil {
		return &prepError{fmt.Sprintf("forge-gateway.json cannot be written (%v)", err), "its config file cannot be written"}
	}
	logf("wrote a new forge-gateway.json: set the node and payout address in the status page's Settings")
	return nil
}

// The ports the gateway listens on, from forge-gateway.json: the miners' (stratum.listen, on every
// interface) and the status page's (status.listen), where the tray finds it.
var (
	stratumPort = "3333"
	statusHost  = "127.0.0.1"
	statusPort  = "3090"
)

// readConfigPorts reads the ports from forge-gateway.json. A file that cannot be read is logged, and
// the usual ports are checked: the gateway itself then says what is wrong with it.
func readConfigPorts() {
	if err := configPorts(); err != nil {
		logf("forge-gateway.json cannot be read for its ports (%v): checking %s and %s", err, stratumPort, statusPort)
	}
}

// configPorts sets the ports from forge-gateway.json, or to the usual ones (3333, and 3090 on
// 127.0.0.1) with the reason it could not.
func configPorts() error {
	stratumPort, statusHost, statusPort = "3333", "127.0.0.1", "3090"
	b, err := os.ReadFile(dpath(configName))
	if err != nil {
		return err
	}
	var c struct {
		Stratum struct {
			Listen string `json:"listen"`
		} `json:"stratum"`
		Status struct {
			Listen string `json:"listen"`
		} `json:"status"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return err
	}
	sp, hp := "3333", "3090"
	host := "127.0.0.1"
	if c.Stratum.Listen != "" {
		if _, sp, err = net.SplitHostPort(c.Stratum.Listen); err != nil {
			return fmt.Errorf("stratum.listen: %v", err)
		}
	}
	switch c.Status.Listen {
	case "":
	case "off":
		return errors.New(`status.listen is "off"`)
	default:
		if host, hp, err = net.SplitHostPort(c.Status.Listen); err != nil {
			return fmt.Errorf("status.listen: %v", err)
		}
	}
	for _, p := range []struct{ key, port string }{{"stratum.listen", sp}, {"status.listen", hp}} {
		if n, err := strconv.Atoi(p.port); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%s: %q is not a port", p.key, p.port)
		}
	}
	// The status page listens on the address given; on every address, it answers at 127.0.0.1.
	switch host {
	case "", "0.0.0.0", "::", "localhost":
		host = "127.0.0.1"
	}
	stratumPort, statusHost, statusPort = sp, host, hp
	return nil
}
