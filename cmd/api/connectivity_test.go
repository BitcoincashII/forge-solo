package main

import (
	"encoding/json"
	"errors"
	"testing"
)

// What the dashboard hands a rental marketplace has to be an address the marketplace can
// actually reach. A LAN or CGNAT address produces a connection that never arrives, so those
// must never be offered -- better to say "unknown" than to send someone's hashrate nowhere.
func TestPublicAddressFromPicksOnlyRoutableAddresses(t *testing.T) {
	for _, tc := range []struct {
		name string
		json string
		want string
	}{
		{"highest score wins", `{"localaddresses":[
			{"address":"198.51.100.4","score":1},{"address":"203.0.113.9","score":7}]}`, "203.0.113.9"},
		{"private is skipped", `{"localaddresses":[
			{"address":"192.168.1.50","score":9},{"address":"203.0.113.9","score":1}]}`, "203.0.113.9"},
		{"rfc1918 10/8 skipped", `{"localaddresses":[{"address":"10.0.0.5","score":9}]}`, ""},
		{"rfc1918 172.16/12 skipped", `{"localaddresses":[{"address":"172.20.1.1","score":9}]}`, ""},
		{"loopback skipped", `{"localaddresses":[{"address":"127.0.0.1","score":9}]}`, ""},
		{"link-local skipped", `{"localaddresses":[{"address":"169.254.3.3","score":9}]}`, ""},
		{"cgnat skipped", `{"localaddresses":[{"address":"100.72.4.9","score":9}]}`, ""},
		{"100.x outside cgnat kept", `{"localaddresses":[{"address":"100.10.4.9","score":1}]}`, "100.10.4.9"},
		{"ipv6 routable kept", `{"localaddresses":[{"address":"2001:db8::1","score":1}]}`, "2001:db8::1"},
		{"no addresses", `{"localaddresses":[]}`, ""},
		{"garbage address", `{"localaddresses":[{"address":"not-an-ip","score":9}]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := publicAddressFrom(json.RawMessage(tc.json), nil); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A node that cannot be consulted must read as unknown, never as "no public address" -- the
// dashboard says different things for the two, and guessing would tell a user their forward
// is broken when the node was merely restarting.
func TestPublicAddressFromDegradesQuietly(t *testing.T) {
	if got := publicAddressFrom(nil, errors.New("node down")); got != "" {
		t.Errorf("rpc error returned %q", got)
	}
	if got := publicAddressFrom(json.RawMessage(`{"localaddresses":`), nil); got != "" {
		t.Errorf("malformed json returned %q", got)
	}
}

// Inbound peers are the evidence that a forward works; outbound say nothing about it.
func TestPeerCountsSeparatesInboundFromOutbound(t *testing.T) {
	raw := json.RawMessage(`[{"inbound":true},{"inbound":false},{"inbound":true},{"inbound":false}]`)
	total, inbound, ok := peerCounts(raw, nil)
	if !ok || total != 4 || inbound != 2 {
		t.Errorf("got total=%d inbound=%d ok=%v, want 4/2/true", total, inbound, ok)
	}

	// Connected but nobody dialled us: that is a working node with a closed port, and the
	// dashboard must be able to tell it apart from a node it could not reach.
	total, inbound, ok = peerCounts(json.RawMessage(`[{"inbound":false},{"inbound":false}]`), nil)
	if !ok || total != 2 || inbound != 0 {
		t.Errorf("outbound-only: got total=%d inbound=%d ok=%v, want 2/0/true", total, inbound, ok)
	}

	if _, _, ok := peerCounts(nil, errors.New("node down")); ok {
		t.Error("an unreachable node reported ok=true")
	}
	if _, _, ok := peerCounts(json.RawMessage(`{"not":"a list"}`), nil); ok {
		t.Error("malformed peer list reported ok=true")
	}
}
