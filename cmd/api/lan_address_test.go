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

// With a full-tunnel VPN on, the route to the internet goes through the tunnel, and its source
// address is the tunnel's (10.8.0.2 and the like), which no miner on the LAN can reach. The
// address shown is the machine's own on its LAN.
func TestLanAddressIsNotAVPNTunnels(t *testing.T) {
	up := net.FlagUp | net.FlagBroadcast | net.FlagMulticast | net.FlagRunning
	mac := net.HardwareAddr{0x52, 0x54, 0x00, 0x12, 0x34, 0x56}
	tapMAC := net.HardwareAddr{0x00, 0xff, 0x6b, 0x2a, 0x11, 0x01}
	nic := func(name string, flags net.Flags, hw net.HardwareAddr, addrs ...string) lanInterface {
		i := lanInterface{name: name, flags: flags, mac: hw}
		for _, a := range addrs {
			i.addrs = append(i.addrs, net.ParseIP(a))
		}
		return i
	}
	eth := nic("eth0", up, mac, "192.168.1.20", "fe80::1")
	tun := nic("tun0", net.FlagUp|net.FlagPointToPoint|net.FlagRunning, nil, "10.8.0.2")
	for _, tc := range []struct {
		code, route string
		ifaces      []lanInterface
		want        string
	}{
		{"LAN-PLAIN", "192.168.1.20", []lanInterface{eth}, "192.168.1.20"},
		{"LAN-OPENVPN", "10.8.0.2", []lanInterface{eth, tun}, "192.168.1.20"},
		// WireGuard and most VPN apps on Windows (Wintun): no hardware address, not point-to-point.
		{"LAN-WINTUN", "10.5.0.2", []lanInterface{nic("Wi-Fi", up, mac, "192.168.1.20"), nic("Ethernet 3", up, nil, "10.5.0.2")}, "192.168.1.20"},
		{"LAN-TAP-WINDOWS", "10.8.0.2", []lanInterface{nic("Ethernet", up, mac, "10.0.0.15"), nic("Ethernet 2", up, tapMAC, "10.8.0.2")}, "10.0.0.15"},
		{"LAN-POINT-TO-POINT", "10.9.0.2", []lanInterface{eth, nic("vpn0", up|net.FlagPointToPoint, mac, "10.9.0.2")}, "192.168.1.20"},
		{"LAN-VIRTUAL-BRIDGE", "10.8.0.2", []lanInterface{nic("virbr0", up, mac, "192.168.122.1"), nic("enp3s0", up, mac, "10.0.0.5"), tun}, "10.0.0.5"},
		{"LAN-DOWN", "10.8.0.2", []lanInterface{nic("wlan0", net.FlagBroadcast, mac, "192.168.1.30"), nic("eth0", up, mac, "10.0.0.5"), tun}, "10.0.0.5"},
		{"LAN-ORDER", "100.101.102.103", []lanInterface{nic("eth1", up, mac, "172.20.0.5"), nic("eth2", up, mac, "10.0.0.5"), eth, nic("tailscale0", up, nil, "100.101.102.103")}, "192.168.1.20"},
		// A home network on 10.x (common with some routers) is kept when the route says so.
		{"LAN-ROUTE-FIRST", "10.0.0.15", []lanInterface{nic("eth1", up, mac, "192.168.50.1"), nic("eth0", up, mac, "10.0.0.15")}, "10.0.0.15"},
		{"LAN-SECOND-ADDRESS", "10.0.0.15", []lanInterface{nic("eth0", up, mac, "192.168.50.1", "10.0.0.15")}, "10.0.0.15"},
		// No private address on a LAN interface: the route's answer, as before.
		{"LAN-PUBLIC", "203.0.113.5", []lanInterface{nic("eth0", up, mac, "203.0.113.5")}, "203.0.113.5"},
		{"LAN-ONLY-LINK-LOCAL", "10.8.0.2", []lanInterface{nic("eth0", up, mac, "169.254.10.10"), tun}, "10.8.0.2"},
		{"LAN-NO-ROUTE", "", []lanInterface{eth}, "192.168.1.20"},
		{"LAN-NOTHING", "", nil, ""},
		{"LAN-LOOPBACK", "127.0.0.1", []lanInterface{nic("lo", net.FlagUp|net.FlagLoopback, nil, "127.0.0.1")}, ""},
	} {
		if got := pickLANAddress(net.ParseIP(tc.route), tc.ifaces); got != tc.want {
			t.Errorf("%s: route %q gives %q, want %q", tc.code, tc.route, got, tc.want)
		}
	}
}
