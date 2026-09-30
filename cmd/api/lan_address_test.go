package main

import (
	"net"
	"testing"
)

// Where the dashboard is opened on 127.0.0.1 (Windows, Linux) it tells miners to dial this
// address instead, so it must be one of this machine's own addresses, and never loopback.
func TestLanAddressIsThisMachinesRoutedAddress(t *testing.T) {
	a := lanAddress()
	if a == "" {
		t.Skip("this machine has no default route")
	}
	ip := net.ParseIP(a)
	if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() {
		t.Fatalf("lanAddress %q", a)
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	own := false
	for _, x := range addrs {
		if n, ok := x.(*net.IPNet); ok && n.IP.Equal(ip) {
			own = true
		}
	}
	if !own {
		t.Fatalf("%s is not an address of this machine (%v)", a, addrs)
	}
	if conn := getJSON(t, getConnectivity, "/connectivity"); conn["lanIp"] != a {
		t.Fatalf("connectivity lanIp %v, want %s", conn["lanIp"], a)
	}
}
