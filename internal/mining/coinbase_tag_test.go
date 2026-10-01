package mining

import (
	"encoding/hex"
	"strings"
	"testing"
)

// With no tag chosen, a block carries Forge Solo's own tag rather than "Forge", the public Forge
// Pool's tag, which left a Forge Solo block looking like a Forge Pool one.
func TestBlocksCarryTheDefaultTagWhenNoneIsChosen(t *testing.T) {
	if DefaultCoinbaseTag != "//forgesolo//" {
		t.Fatalf("DefaultCoinbaseTag = %q, want //forgesolo//", DefaultCoinbaseTag)
	}
	for _, tag := range []string{"", "\x01\x02"} {
		jm := NewJobManager("", "", "", "", tag)
		if got := string(jm.CoinbaseTag()); got != DefaultCoinbaseTag {
			t.Errorf("started with tag %q: jobs carry %q, want %q", tag, got, DefaultCoinbaseTag)
		}
		jm.SetCoinbaseTag("MyRig")
		jm.SetCoinbaseTag(tag)
		if got := string(jm.CoinbaseTag()); got != DefaultCoinbaseTag {
			t.Errorf("tag cleared to %q: jobs carry %q, want %q", tag, got, DefaultCoinbaseTag)
		}
	}
	jm := &JobManager{pubkeyHash: make([]byte, 20)}
	_, cb2 := jm.buildCoinbase(&BlockTemplate{Height: 83470, CoinbaseValue: 5000000000}, nil)
	if want := hex.EncodeToString([]byte(DefaultCoinbaseTag)); !strings.HasPrefix(cb2, want) {
		t.Errorf("a coinbase built with no tag set has fixed part %s…, want it to start with the default tag %s", cb2[:len(want)], want)
	}
}
