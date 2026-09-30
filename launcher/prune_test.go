package main

import (
	"os"
	"strings"
	"testing"
)

// The BCH2 node runs unpruned (see writeConfigs); the 1175 node is left as it was.
func TestGeneratedBCH2ConfIsUnpruned(t *testing.T) {
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
	if !strings.Contains(string(aux), "\nprune=2000\n") {
		t.Fatalf("1175.conf changed; this change is scoped to the BCH2 node:\n%s", aux)
	}
}
