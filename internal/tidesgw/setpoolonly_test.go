package tidesgw

import (
	"crypto/ed25519"
	"errors"
	"reflect"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// Forge Gateway's Settings turn pool_only on and off while it runs: what a fall back says follows.
func TestSetPoolOnlyChangesWhatFallbackSays(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	_, key, _ := ed25519.GenerateKey(nil)
	g := New(Config{PoolURL: "http://127.0.0.1:1", Key: key, Logger: zap.New(core)})
	SetPoolOnly(g, true)
	g.Fallback(errors.New("the pool did not answer"))
	if logs.FilterMessageSnippet("miners are turned away until it is back (pool_only)").Len() != 1 {
		t.Fatalf("TIDES-POOLONLY-SET: on: %v", logs.All())
	}
	SetPoolOnly(g, false)
	g.Reset()
	g.Fallback(errors.New("the pool did not answer"))
	if logs.FilterMessageSnippet("mining SOLO until it is back").Len() != 1 {
		t.Fatalf("TIDES-POOLONLY-SET: off: %v", logs.All())
	}
}

// SetPoolOnly is a function, not a method of Gateway: Forge Solo never calls it, and an exported
// method is kept in the method table of every program that uses a Gateway, Forge Solo's stratum
// among them, which would then differ from the one built without it.
func TestSetPoolOnlyIsNotAMethod(t *testing.T) {
	if _, ok := reflect.TypeOf(&Gateway{}).MethodByName("SetPoolOnly"); ok {
		t.Fatal("TIDES-POOLONLY-NOMETHOD: *Gateway has a method SetPoolOnly, which changes Forge Solo's stratum")
	}
}
