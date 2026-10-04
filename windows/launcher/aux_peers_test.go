package main

import (
	"net"
	"os"
	"strings"
	"testing"
)

// Another program on 25360, the 1175 node's peer port: the node runs without taking incoming peers
// (listen=0), and merge mining goes on. With listen=1 it exited at every start against a program
// that does not share the port, and was started again every minute; beside another 1175 node or
// wallet it shared their incoming peers. Once the port is free again, it listens again.
func TestA1175PeerPortTakenRunsTheNodeWithoutIncomingPeers(t *testing.T) {
	savedData, savedPublic, savedPort := dataDir, publicPorts, aux1175P2P
	t.Cleanup(func() { dataDir, publicPorts, aux1175P2P, auxNoPeers = savedData, savedPublic, savedPort, false })
	dataDir = t.TempDir()
	held, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, aux1175P2P, _ = net.SplitHostPort(held.Addr().String())
	publicPorts = []struct {
		port, what string
		required   bool
	}{{aux1175P2P, "the 1175 node's peer port", false}}

	if err := checkPublicPorts(); err != nil {
		t.Fatalf("the 1175 node's peer port taken stopped the start: %v", err)
	}
	writeConfigs()
	conf, _ := os.ReadFile(dpath("elevenseventyfive", "1175.conf"))
	if !strings.Contains(string(conf), "\nlisten=0\n") || strings.Contains(string(conf), "listen=1") {
		t.Errorf("AUX-PEERS-OFF-CONF: with port %s taken, 1175.conf still listens:\n%s", aux1175P2P, conf)
	}
	b, _ := os.ReadFile(dpath("launcher.log"))
	if !strings.Contains(string(b), "another program uses port "+aux1175P2P+", the 1175 node's peer port: the 1175 node runs without incoming peers") ||
		strings.Contains(string(b), "that part is left out") {
		t.Errorf("AUX-PEERS-OFF-LOG: launcher.log does not say what happens to the 1175 node:\n%s", b)
	}

	_ = held.Close()
	if err := checkPublicPorts(); err != nil {
		t.Fatal(err)
	}
	writeConfigs()
	if conf, _ := os.ReadFile(dpath("elevenseventyfive", "1175.conf")); !strings.Contains(string(conf), "\nlisten=1\n") {
		t.Errorf("AUX-PEERS-BACK: with port %s free again, 1175.conf does not listen:\n%s", aux1175P2P, conf)
	}
}
