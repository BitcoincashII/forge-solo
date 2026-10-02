package forgesolo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// peeraddr runs a function from docker/node1175/peeraddr.sh with stdin and arguments.
func peeraddr(t *testing.T, stdin, call string, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{"-c", ". docker/node1175/peeraddr.sh; " + call, "sh"}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatal(err)
		}
	}
	return string(out), err == nil
}

// The 1175 node advertises the address its OUTBOUND peers saw it on. Anyone can connect in and claim
// anything, so inbound peers' claims must not count. The input is real getpeerinfo output of the
// node this image runs (1175 v29.1.0, regtest): peer 0 is outbound, peer 1 inbound, and each entry
// also carries "connection_type" -- an earlier filter matched "inbound" there too and counted every
// inbound peer.
func TestOnlyOutboundPeersChooseThe1175Address(t *testing.T) {
	b, err := os.ReadFile("testdata/1175-v29.1.0-getpeerinfo.json")
	if err != nil {
		t.Fatal(err)
	}
	real := string(b)
	if !strings.Contains(real, `"connection_type": "inbound"`) || !strings.Contains(real, `"inbound": true`) {
		t.Fatal("PEERADDR-FIXTURE: the captured output lost its inbound peer")
	}
	out, _ := peeraddr(t, real, "outbound_addrlocals")
	if got := strings.Fields(out); strings.Join(got, " ") != "11.77.0.3" {
		t.Errorf("PEERADDR-REAL: outbound addresses %q, want only the outbound peer's [11.77.0.3]", got)
	}
	// The same output with the inbound peer claiming another address: it still must not count.
	lying := strings.Replace(real, `"addrlocal": "11.77.0.3:19500"`, `"addrlocal": "6.6.6.6:19500"`, 1)
	if lying == real {
		t.Fatal("PEERADDR-FIXTURE: the inbound peer's addrlocal was not found")
	}
	out, _ = peeraddr(t, lying, "outbound_addrlocals")
	if got := strings.Fields(out); strings.Join(got, " ") != "11.77.0.3" {
		t.Errorf("PEERADDR-INBOUND-CLAIM: outbound addresses %q, want [11.77.0.3]", got)
	}
}

// Only a public IPv4 address may be learned and passed to the node as -externalip: an IPv6 one
// without brackets stops the node from starting.
func TestPublicIPv4(t *testing.T) {
	for ip, want := range map[string]bool{
		"47.160.211.234": true, "8.8.8.8": true, "1.1.1.1": true, "223.255.255.254": true,
		"172.15.0.1": true, "172.32.0.1": true, "100.63.0.1": true, "100.128.0.1": true, "11.77.0.3": true,
		"": false, "10.0.0.1": false, "127.0.0.1": false, "0.1.2.3": false, "192.168.1.1": false,
		"172.16.0.1": false, "172.31.255.255": false, "169.254.1.1": false, "100.64.0.1": false,
		"100.127.0.1": false, "192.0.2.1": false, "192.0.0.1": false, "198.51.100.1": false,
		"203.0.113.1": false, "198.18.0.1": false, "198.19.0.1": false, "224.0.0.1": false,
		"255.255.255.255": false, "2a01:4f8::1": false, "[2a01:4f8::1]": false, "1.2.3": false,
		"1.2.3.4.5": false, "01.2.3.4": false, "256.1.1.1": false, "1.2.3.4 ": false, "a.b.c.d": false,
	} {
		if _, got := peeraddr(t, "", `public_ipv4 "$1"`, ip); got != want {
			t.Errorf("PEERADDR-PUBLIC-IPV4: public_ipv4(%q) = %v, want %v", ip, got, want)
		}
	}
}

// A saved address the node can't use (an IPv6 one, saved before 1.0.13) is dropped at start, not
// passed to the node, which would refuse to start on it every time.
func TestSavedExternalIPIsChecked(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "external-ip")
	for _, tc := range []struct {
		saved, want string
		kept        bool
	}{
		{"47.160.211.234\n", "47.160.211.234", true},
		{"2a01:4f8::1\n", "", false},
		{"10.0.0.7\n", "", false},
		{"47.160.2", "", false},
	} {
		if err := os.WriteFile(f, []byte(tc.saved), 0o600); err != nil {
			t.Fatal(err)
		}
		out, _ := peeraddr(t, "", `saved_external_ip "$1"`, f)
		_, statErr := os.Stat(f)
		if strings.TrimSpace(out) != tc.want || (statErr == nil) != tc.kept {
			t.Errorf("PEERADDR-SAVED: saved %q gave %q (file kept: %v), want %q (kept: %v)", tc.saved, strings.TrimSpace(out), statErr == nil, tc.want, tc.kept)
		}
	}
	os.Remove(f)
	if out, ok := peeraddr(t, "", `saved_external_ip "$1"`, f); !ok || out != "" {
		t.Errorf("PEERADDR-SAVED-MISSING: no saved file gave %q (ok %v), want nothing", out, ok)
	}
}
