package main

import (
	"os"
	"strings"
	"testing"
)

// Neither node runs pruned (see writeConfigs), as on Umbrel.
func TestGeneratedConfsAreUnpruned(t *testing.T) {
	saved := dataDir
	dataDir = t.TempDir()
	t.Cleanup(func() { dataDir = saved })
	writeConfigs()
	bch2, err := os.ReadFile(dpath("bch2", "bch2.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bch2), "prune") {
		t.Fatalf("bch2.conf still prunes the BCH2 node:\n%s", bch2)
	}
	if !strings.Contains(string(bch2), "\nlisten=1\n") || !strings.Contains(string(bch2), "\nport="+bch2P2P+"\n") {
		t.Fatalf("bch2.conf lost its listen/port lines:\n%s", bch2)
	}
	// Without explicit binds the node's onion listener (127.0.0.1:8339) takes the P2P port first
	// and 0.0.0.0:8339 fails: no IPv4 inbound peer, however the router is forwarded.
	for _, line := range []string{"bind=0.0.0.0:" + bch2P2P, "bind=[::]:" + bch2P2P, "bind=127.0.0.1:8340=onion"} {
		if !strings.Contains(string(bch2), "\n"+line+"\n") {
			t.Fatalf("bch2.conf lacks %q:\n%s", line, bch2)
		}
	}
	aux, err := os.ReadFile(dpath("elevenseventyfive", "1175.conf"))
	if err != nil {
		t.Fatal(err)
	}
	// A pruned node announces NODE_NETWORK_LIMITED, which the DNS seeders skip: few inbound peers
	// however the router is forwarded. Umbrel's 1175 node is not pruned.
	if strings.Contains(string(aux), "prune") {
		t.Fatalf("PRUNE-1175: 1175.conf still prunes the 1175 node:\n%s", aux)
	}
	if !strings.Contains(string(aux), "\nlisten=1\n") || !strings.Contains(string(aux), "\nport="+aux1175P2P+"\n") {
		t.Fatalf("1175.conf lost its listen/port lines:\n%s", aux)
	}
}

// Neither node has a wallet, as on Umbrel (-disablewallet) and Linux (built without one): nothing
// uses it, and anything holding the RPC password could make and use one.
func TestNodesRunWithoutAWallet(t *testing.T) {
	saved := dataDir
	dataDir = t.TempDir()
	t.Cleanup(func() { dataDir = saved })
	writeConfigs()
	for _, conf := range []string{dpath("bch2", "bch2.conf"), dpath("elevenseventyfive", "1175.conf")} {
		b, err := os.ReadFile(conf)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "\ndisablewallet=1\n") {
			t.Errorf("WIN-NO-WALLET: %s does not disable the node's wallet:\n%s", conf, b)
		}
	}
}
