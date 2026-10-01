package forgesolo

import (
	"os/exec"
	"strings"
	"testing"
)

// The 1175 node advertises the address its OUTBOUND peers saw it on. Anyone can connect in and claim
// anything, so inbound peers' claims must not count.
func TestOnlyOutboundPeersChooseThe1175Address(t *testing.T) {
	getpeerinfo := `[
  {
    "id": 1,
    "addr": "203.0.113.5:25360",
    "addrlocal": "6.6.6.6:4444",
    "bytessent_per_msg": {
      "ping": 32
    },
    "inbound": true
  },
  {
    "id": 2,
    "addrlocal": "6.6.6.6:5555",
    "inbound": true
  },
  {
    "id": 3,
    "addrlocal": "47.160.211.234:33764",
    "inbound": false
  },
  {
    "id": 4,
    "addrlocal": "[2001:db8::1]:25360",
    "inbound": false
  },
  {
    "id": 5,
    "inbound": false
  }
]`
	cmd := exec.Command("sh", "-c", ". docker/node1175/peeraddr.sh; outbound_addrlocals")
	cmd.Stdin = strings.NewReader(getpeerinfo)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Fields(string(out)), []string{"47.160.211.234", "2001:db8::1"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("outbound addresses %q, want %q", got, want)
	}
}
