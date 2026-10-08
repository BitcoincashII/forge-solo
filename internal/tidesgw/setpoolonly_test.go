package tidesgw

import (
	"crypto/ed25519"
	"errors"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// Forge Gateway's Settings turn pool_only on and off while it runs: what a fall back says follows.
func TestSetPoolOnlyChangesWhatFallbackSays(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	_, key, _ := ed25519.GenerateKey(nil)
	g := New(Config{PoolURL: "http://127.0.0.1:1", Key: key, Logger: zap.New(core)})
	g.SetPoolOnly(true)
	g.Fallback(errors.New("the pool did not answer"))
	if logs.FilterMessageSnippet("miners are turned away until it is back (pool_only)").Len() != 1 {
		t.Fatalf("TIDES-POOLONLY-SET: on: %v", logs.All())
	}
	g.SetPoolOnly(false)
	g.Reset()
	g.Fallback(errors.New("the pool did not answer"))
	if logs.FilterMessageSnippet("mining SOLO until it is back").Len() != 1 {
		t.Fatalf("TIDES-POOLONLY-SET: off: %v", logs.All())
	}
}
