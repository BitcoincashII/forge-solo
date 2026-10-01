package forgesolo

import (
	"os"
	"regexp"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/mining"
)

// Forge Solo's default coinbase tag lives in one place, mining.DefaultCoinbaseTag. The compose
// file leaves COINBASE_TAG empty so both services fall back to it rather than to a second copy
// that can drift, and the Settings tag box names it, since a blank box gives it.
func TestOneDefaultCoinbaseTag(t *testing.T) {
	compose, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`COINBASE_TAG=\$\{COINBASE_TAG:-([^}]*)\}`).FindAllSubmatch(compose, -1)
	if len(m) == 0 {
		t.Fatal("docker-compose.yml no longer passes COINBASE_TAG; update this test")
	}
	for _, s := range m {
		if len(s[1]) != 0 {
			t.Errorf("docker-compose.yml defaults COINBASE_TAG to %q; leave it empty so the services use mining.DefaultCoinbaseTag", s[1])
		}
	}
	page, err := os.ReadFile("web/dist/settings.html")
	if err != nil {
		t.Fatal(err)
	}
	p := regexp.MustCompile(`id="cbTag"[^>]*placeholder="([^"]*)"`).FindSubmatch(page)
	if p == nil || string(p[1]) != mining.DefaultCoinbaseTag {
		t.Errorf("the Settings tag box's placeholder must be %q, the tag a blank box gives", mining.DefaultCoinbaseTag)
	}
}
